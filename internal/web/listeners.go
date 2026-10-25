package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type listenerKind string

const (
	adminListener  listenerKind = "admin"
	publicListener listenerKind = "public"
)

type listenerContext struct{}

func audience(r *http.Request) listenerKind {
	if value, ok := r.Context().Value(listenerContext{}).(listenerKind); ok {
		return value
	}
	return adminListener
}
func cookieName(r *http.Request) string {
	if audience(r) == publicListener {
		return publicSessionCookie
	}
	return sessionCookie
}
func cookieTTL(r *http.Request) time.Duration {
	if audience(r) == publicListener {
		return publicSessionTTL
	}
	return sessionTTL
}
func accountAllowed(r *http.Request, role string) bool {
	return (audience(r) == adminListener && role == "admin") || (audience(r) == publicListener && role == "user")
}
func peerAddress(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr, _ := netip.ParseAddr(host)
	return addr.Unmap()
}
func requestHost(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}
func (s *Server) adminNetworkAllowed(r *http.Request) bool {
	addr := peerAddress(r)
	if !addr.IsValid() {
		return false
	}
	permitted := false
	for _, text := range s.cfg.Admin.AllowedNetworks {
		prefix, err := netip.ParsePrefix(text)
		if err == nil && prefix.Contains(addr) {
			permitted = true
			break
		}
	}
	if !permitted {
		return false
	}
	// Administration is direct LAN access. Never infer trust from proxy headers.
	// This also fails closed if a Cloudflare hostname is aimed at the admin port.
	for name := range r.Header {
		name = strings.ToLower(name)
		if name == "forwarded" || strings.HasPrefix(name, "x-forwarded-") || strings.HasPrefix(name, "cf-") || name == "true-client-ip" || name == "x-real-ip" {
			return false
		}
	}
	host := requestHost(r.Host)
	if len(s.cfg.Admin.AllowedHosts) > 0 {
		for _, allowed := range s.cfg.Admin.AllowedHosts {
			if host == requestHost(allowed) {
				return true
			}
		}
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && (ip.Unmap().IsPrivate() || ip.Unmap().IsLoopback())
}
func unsafeMethod(method string) bool {
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}
func browserRequestAllowed(r *http.Request) bool {
	if !unsafeMethod(r.Method) && audience(r) != adminListener {
		return true
	}
	// Same-site subdomains/ports are not the same origin as administration.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	} // non-browser clients still require credentials / CSRF below
	parsed, err := url.Parse(origin)
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	return err == nil && parsed.Scheme == scheme && strings.EqualFold(parsed.Host, r.Host) && parsed.User == nil
}
func (s *Server) listenerBoundary(kind listenerKind, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), listenerContext{}, kind))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' https://image.tmdb.org; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; object-src 'none'")
		if kind == adminListener && !s.adminNetworkAllowed(r) {
			writeErr(w, 403, "administration is available only through direct LAN access")
			return
		}
		if !browserRequestAllowed(r) {
			writeErr(w, 403, "cross-origin request rejected")
			return
		}
		if r.URL.Path != "/health" && (!s.authEnabled() || (kind == publicListener && len(s.cfg.Users) == 0)) {
			writeErr(w, 503, "Account configuration is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func csrfToken(r *http.Request) string {
	cookie, err := r.Cookie(cookieName(r))
	if err != nil || cookie.Value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("cineroute-csrf-v1:" + string(audience(r)) + ":" + cookie.Value))
	return hex.EncodeToString(digest[:])
}
func validCSRF(r *http.Request) bool {
	expected := csrfToken(r)
	return expected != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(r.Header.Get("X-CineRoute-CSRF"))) == 1
}

// PublicHandler owns a separate route table. It never delegates unknown paths
// to Handler, regardless of credentials, Host, proxy headers, or URL encoding.
func (s *Server) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.page.ExecuteTemplate(w, "public.html", nil); err != nil {
			return
		}
	})
	for path, asset := range map[string]string{"/favicon.png": "logo/favicon.png", "/favicon.svg": "logo/favicon.svg", "/logo.svg": "logo/logo.svg", "/app.css": "templates/app.css", "/catalog.js": "templates/catalog.js", "/viewport.js": "templates/viewport.js", "/portal.js": "templates/portal.js"} {
		mux.HandleFunc("GET "+path, s.serveAsset(asset))
	}
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("GET /api/catalog/search", s.catalogSearch)
	mux.HandleFunc("GET /api/catalog/{type}/{id}", s.catalogDetails)
	mux.HandleFunc("GET /api/user/requests", s.userRequests)
	mux.HandleFunc("POST /api/user/requests", s.createUserRequest)
	mux.HandleFunc("POST /api/user/requests/{id}/cancel", s.cancelUserRequest)
	return s.logRequests(s.listenerBoundary(publicListener, s.auth(mux)))
}
