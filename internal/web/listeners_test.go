package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/requests"
)

func securityServer(t *testing.T) *Server {
	t.Helper()
	cfg := requestTestConfig(t)
	cfg.Users = []config.User{{Name: "admin", Role: "admin", Password: "admin-secret"}, {Name: "alice", Role: "user", Password: "alice-secret"}}
	return New(cfg, nil, nil)
}
func securityRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Host = "192.168.18.29:8787"
	r.RemoteAddr = "192.168.18.50:12345"
	return r
}
func securityCall(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}
func securityLogin(t *testing.T, s *Server, kind listenerKind, name, password string) *http.Cookie {
	t.Helper()
	r := securityRequest("POST", "/api/login")
	r.Body = http.NoBody
	r = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"`+name+`","password":"`+password+`"}`))
	r.Host = "192.168.18.29:8787"
	r.RemoteAddr = "192.168.18.50:12345"
	r.Header.Set("Content-Type", "application/json")
	h := s.Handler()
	if kind == publicListener {
		h = s.PublicHandler()
	}
	out := securityCall(h, r)
	if out.Code != 200 || len(out.Result().Cookies()) != 1 {
		t.Fatalf("%s login: %d %s", kind, out.Code, out.Body.String())
	}
	return out.Result().Cookies()[0]
}
func securityCSRF(t *testing.T, h http.Handler, cookie *http.Cookie) string {
	t.Helper()
	r := securityRequest("GET", "/api/session")
	r.AddCookie(cookie)
	out := securityCall(h, r)
	var session struct {
		Token string `json:"csrf_token"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &session); err != nil || out.Code != 200 || session.Token == "" {
		t.Fatalf("session/CSRF: %d %s %v", out.Code, out.Body.String(), err)
	}
	return session.Token
}

func TestPublicListenerNeverExposesAdministrativeRoutes(t *testing.T) {
	s := securityServer(t)
	userCookie := securityLogin(t, s, publicListener, "alice", "alice-secret")
	adminCookie := securityLogin(t, s, adminListener, "admin", "admin-secret")
	sentinel := filepath.Join(t.TempDir(), "array-sentinel.mkv")
	if err := os.WriteFile(sentinel, []byte("original media"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/api/status", "/api/history", "/api/intakes", "/api/intakes/anything", "/api/intakes/anything/submit", "/api/intakes/anything/destination", "/api/copies", "/api/copies/scan", "/api/copies/anything/hardlink", "/api/hardlinks", "/api/hardlinks/scan", "/api/hardlinks/anything", "/api/hardlinks/anything/relink", "/api/subtitles", "/api/subtitles/settings", "/api/subtitles/work/clear", "/api/subtitles/run", "/api/subtitles/anything/run", "/api/requests/settings", "/api/requests/indexers", "/api/requests/queue", "/api/requests/history", "/api/requests/queue/anything/approve", "/api/requests/queue/anything/retry"}
	for _, path := range paths {
		for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			for _, credential := range []string{"none", "user", "admin-cookie", "admin-basic"} {
				r := securityRequest(method, path)
				switch credential {
				case "user":
					r.AddCookie(userCookie)
				case "admin-cookie":
					r.AddCookie(adminCookie)
				case "admin-basic":
					r.SetBasicAuth("admin", "admin-secret")
				}
				r.Header.Set("X-Forwarded-For", "192.168.18.50")
				r.Header.Set("X-Forwarded-Host", "192.168.18.29:8787")
				r.Header.Set("X-Original-URL", path)
				out := securityCall(s.PublicHandler(), r)
				if out.Code >= 200 && out.Code < 400 {
					t.Errorf("public %s %s with %s: %d", method, path, credential, out.Code)
				}
			}
		}
	}
	// Public assets do not contain the administration interface or its scripts.
	for _, path := range []string{"/", "/catalog.js", "/portal.js"} {
		out := securityCall(s.PublicHandler(), securityRequest("GET", path))
		if out.Code != 200 {
			t.Fatalf("public asset %s: %d", path, out.Code)
		}
		for _, text := range []string{"id=\"appShell\"", "id=\"hardlinksTab\"", "/api/hardlinks", "/api/subtitles", "/api/requests/queue", "/api/requests/history", "openAdministration"} {
			if strings.Contains(out.Body.String(), text) {
				t.Errorf("admin interface in public %s: %s", path, text)
			}
		}
	}
	if out := securityCall(s.PublicHandler(), securityRequest("GET", "/requests.js")); out.Code == 200 {
		t.Fatal("admin script exposed publicly")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "original media" {
		t.Fatalf("media changed: %s %v", data, err)
	}
	settings, _ := s.requests.Load()
	if settings.Movie.Destination != requests.Defaults().Movie.Destination {
		t.Fatal("policy changed through public listener")
	}
}

func TestPublicListenerRejectsAdminLoginAndCrossListenerSessions(t *testing.T) {
	s := securityServer(t)
	r := securityRequest("POST", "/api/login")
	r.Body = http.NoBody
	r = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"admin-secret"}`))
	r.Header.Set("Content-Type", "application/json")
	if out := securityCall(s.PublicHandler(), r); out.Code != 401 || len(out.Result().Cookies()) != 0 {
		t.Fatalf("public admin login: %d %s", out.Code, out.Body.String())
	}
	adminCookie := securityLogin(t, s, adminListener, "admin", "admin-secret")
	userCookie := securityLogin(t, s, publicListener, "alice", "alice-secret")
	for _, test := range []struct {
		h      http.Handler
		cookie *http.Cookie
		name   string
	}{
		{s.PublicHandler(), adminCookie, publicSessionCookie}, {s.Handler(), userCookie, sessionCookie},
	} {
		copied := *test.cookie
		copied.Name = test.name
		r := securityRequest("GET", "/api/session")
		r.AddCookie(&copied)
		if out := securityCall(test.h, r); out.Code != 401 {
			t.Fatalf("renamed cross-listener cookie accepted: %d %s", out.Code, out.Body.String())
		}
	}
	// Even a correctly signed token for a role on the wrong interface is rejected.
	for _, kind := range []listenerKind{publicListener, adminListener} {
		user := &s.cfg.Users[0]
		h := s.PublicHandler()
		name := publicSessionCookie
		if kind == adminListener {
			user = &s.cfg.Users[1]
			h = s.Handler()
			name = sessionCookie
		}
		r := securityRequest("GET", "/api/session")
		r.AddCookie(&http.Cookie{Name: name, Value: s.accountToken(user, time.Now().Add(time.Hour), kind)})
		if out := securityCall(h, r); out.Code != 401 {
			t.Fatalf("wrong-role session accepted on %s: %d", kind, out.Code)
		}
	}
	if userCookie.Name == adminCookie.Name || adminCookie.SameSite != http.SameSiteStrictMode || adminCookie.MaxAge != int((90*24*time.Hour).Seconds()) || userCookie.MaxAge != int((90*24*time.Hour).Seconds()) {
		t.Fatal("session isolation or lifetimes incorrect")
	}
}

func TestAdminListenerEnforcesRealLANPeerAndHost(t *testing.T) {
	s := securityServer(t)
	for name, mutate := range map[string]func(*http.Request){
		"public IPv4": func(r *http.Request) { r.RemoteAddr = "203.0.113.5:44" },
		"public IPv6": func(r *http.Request) { r.RemoteAddr = "[2001:db8::5]:44" },
		"forged LAN header": func(r *http.Request) {
			r.RemoteAddr = "203.0.113.5:44"
			r.Header.Set("X-Forwarded-For", "192.168.18.5")
		},
		"tunnel":                   func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.5") },
		"forwarded":                func(r *http.Request) { r.Header.Set("Forwarded", "for=192.168.18.5") },
		"proxy":                    func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") },
		"public host":              func(r *http.Request) { r.Host = "requests.example.com" },
		"rebinding host":           func(r *http.Request) { r.Host = "192.168.18.29.attacker.example" },
		"invalid peer":             func(r *http.Request) { r.RemoteAddr = "invalid" },
		"public browser origin":    func(r *http.Request) { r.Header.Set("Origin", "http://192.168.18.29:8788") },
		"same-site public browser": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
	} {
		t.Run(name, func(t *testing.T) {
			r := securityRequest("GET", "/api/requests/settings")
			r.SetBasicAuth("admin", "admin-secret")
			mutate(r)
			if out := securityCall(s.Handler(), r); out.Code != 403 {
				t.Fatalf("admin network bypass: %d %s", out.Code, out.Body.String())
			}
		})
	}
	r := securityRequest("GET", "/api/requests/settings")
	r.SetBasicAuth("admin", "admin-secret")
	if out := securityCall(s.Handler(), r); out.Code != 200 {
		t.Fatalf("LAN admin blocked: %d %s", out.Code, out.Body.String())
	}
	s.cfg.Admin.AllowedHosts = []string{"nas.lan"}
	r = securityRequest("GET", "/api/requests/settings")
	r.SetBasicAuth("admin", "admin-secret")
	r.Host = "nas.lan:8787"
	if out := securityCall(s.Handler(), r); out.Code != 200 {
		t.Fatal("configured LAN hostname blocked")
	}
	r.Host = "192.168.18.29:8787"
	if out := securityCall(s.Handler(), r); out.Code != 403 {
		t.Fatal("explicit Host policy bypassed")
	}
}

func TestAdminMutationsRequireCSRFAndSameOrigin(t *testing.T) {
	s := securityServer(t)
	cookie := securityLogin(t, s, adminListener, "admin", "admin-secret")
	token := securityCSRF(t, s.Handler(), cookie)
	for name, mutate := range map[string]func(*http.Request){
		"missing token":      func(r *http.Request) { r.Header.Del("X-CineRoute-CSRF") },
		"wrong token":        func(r *http.Request) { r.Header.Set("X-CineRoute-CSRF", "wrong") },
		"dummy basic header": func(r *http.Request) { r.Header.Del("X-CineRoute-CSRF"); r.SetBasicAuth("wrong", "wrong") },
		"other origin":       func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
		"public port origin": func(r *http.Request) { r.Header.Set("Origin", "http://192.168.18.29:8788") },
		"same-site sibling":  func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"cross-site":         func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"null origin":        func(r *http.Request) { r.Header.Set("Origin", "null") },
		"scheme mismatch":    func(r *http.Request) { r.Header.Set("Origin", "https://192.168.18.29:8787") },
		"referer fallback":   func(r *http.Request) { r.Header.Set("Referer", "http://attacker.example/page") },
	} {
		t.Run(name, func(t *testing.T) {
			r := securityRequest("POST", "/api/logout")
			r.AddCookie(cookie)
			r.Header.Set("X-CineRoute-CSRF", token)
			mutate(r)
			out := securityCall(s.Handler(), r)
			if out.Code != 403 || len(out.Result().Cookies()) != 0 {
				t.Fatalf("CSRF bypass: %d %s", out.Code, out.Body.String())
			}
		})
	}
	r := securityRequest("POST", "/api/logout")
	r.AddCookie(cookie)
	r.Header.Set("X-CineRoute-CSRF", token)
	r.Header.Set("Origin", "http://192.168.18.29:8787")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if out := securityCall(s.Handler(), r); out.Code != 200 {
		t.Fatalf("valid logout rejected: %d %s", out.Code, out.Body.String())
	}
}

func TestNoAnonymousAdministrationOrLegacyPublicFallback(t *testing.T) {
	cfg := requestTestConfig(t)
	s := New(cfg, nil, nil)
	if err := s.Start(context.Background()); err == nil {
		s.Close()
		t.Fatal("anonymous admin server started")
	}
	r := securityRequest("GET", "/api/requests/settings")
	if out := securityCall(s.Handler(), r); out.Code != 503 {
		t.Fatalf("anonymous admin handler: %d", out.Code)
	}
	cfg.AuthPassword = "legacy-secret"
	r = securityRequest("GET", "/api/session")
	r.SetBasicAuth("cineroute", "legacy-secret")
	if out := securityCall(s.PublicHandler(), r); out.Code != 503 {
		t.Fatalf("legacy public fallback: %d", out.Code)
	}
}

func TestPasswordAndBasicAuthAttemptsAreRateLimited(t *testing.T) {
	for _, basic := range []bool{false, true} {
		t.Run(map[bool]string{false: "login", true: "basic"}[basic], func(t *testing.T) {
			s := securityServer(t)
			for i := 0; i < 11; i++ {
				r := securityRequest("GET", "/api/requests/settings")
				r.SetBasicAuth("admin", "wrong")
				if !basic {
					r = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
					r.Header.Set("Content-Type", "application/json")
					testLANRequest(r)
				}
				out := securityCall(s.Handler(), r)
				if out.Code != 401 && out.Code != 429 {
					t.Fatalf("bad credentials: %d", out.Code)
				}
			}
			r := securityRequest("GET", "/api/requests/settings")
			r.SetBasicAuth("admin", "admin-secret")
			if !basic {
				r = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"admin-secret"}`))
				r.Header.Set("Content-Type", "application/json")
				testLANRequest(r)
			}
			if out := securityCall(s.Handler(), r); out.Code == 200 {
				t.Fatal("rate limit bypassed by correct credentials after failed attempts")
			}
		})
	}
}

func TestPublicRouteNormalizationCannotReachAdmin(t *testing.T) {
	s := securityServer(t)
	cookie := securityLogin(t, s, publicListener, "alice", "alice-secret")
	server := httptest.NewServer(s.PublicHandler())
	defer server.Close()
	for _, path := range []string{"//api/hardlinks", "/api/user/../hardlinks", "/api/catalog/../../hardlinks", "/api/%68ardlinks", "/api/user/requests/%2e%2e/%2e%2e/hardlinks", "/api/user/requests/a%2fb/cancel", "/api/hardlinks%2fscan", "/api\\hardlinks", "/api/requests/settings;anything"} {
		r, _ := http.NewRequest("GET", server.URL+path, nil)
		r.AddCookie(cookie)
		out, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		out.Body.Close()
		if out.StatusCode >= 200 && out.StatusCode < 400 {
			t.Errorf("normalized path reached public admin API: %s %d", path, out.StatusCode)
		}
	}
}

func TestConcurrentPasswordAttemptsReserveTheLimit(t *testing.T) {
	s := securityServer(t)
	var accepted atomic.Int64
	var group sync.WaitGroup
	for i := 0; i < 100; i++ {
		group.Go(func() {
			if !s.loginBlocked(securityRequest("POST", "/api/login")) {
				accepted.Add(1)
			}
		})
	}
	group.Wait()
	if accepted.Load() != 10 {
		t.Fatalf("concurrent rate limit admitted %d attempts", accepted.Load())
	}
}
