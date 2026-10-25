package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cineroute/internal/config"
	"golang.org/x/crypto/bcrypt"
)

type account struct {
	Name string `json:"name"`
	Role string `json:"role"`
}
type accountContext struct{}
type accounts struct {
	key      []byte
	err      error
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newAccounts(cfg *config.Config) *accounts {
	a := &accounts{failures: map[string][]time.Time{}}
	if len(cfg.Users) == 0 && cfg.AuthPassword == "" {
		return a
	}
	path := filepath.Join(filepath.Dir(cfg.Requests.StatePath), "session.key")
	key, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		_, err = rand.Read(key)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(path), 0o700)
		}
		if err == nil {
			var file *os.File
			file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if os.IsExist(err) {
				key, err = os.ReadFile(path)
			} else if err == nil {
				_, err = file.Write(key)
				if closeErr := file.Close(); err == nil {
					err = closeErr
				}
			}
		}
	}
	if err == nil && len(key) != 32 {
		err = fmt.Errorf("invalid session key length")
	}
	if err == nil {
		err = os.Chmod(path, 0o600)
	}
	a.key, a.err = key, err
	return a
}

func actor(r *http.Request) account { a, _ := r.Context().Value(accountContext{}).(account); return a }
func (s *Server) authEnabled() bool { return len(s.cfg.Users) > 0 || s.cfg.AuthPassword != "" }
func (s *Server) findAccount(name string) *config.User {
	for i := range s.cfg.Users {
		u := &s.cfg.Users[i]
		if !u.Disabled && strings.EqualFold(u.Name, strings.TrimSpace(name)) {
			return u
		}
	}
	return nil
}
func passwordMatches(u *config.User, pass string) bool {
	if u.PasswordHash != "" {
		return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pass)) == nil
	}
	a, b := sha256.Sum256([]byte(pass)), sha256.Sum256([]byte(u.Password))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
func (s *Server) accountSignature(u *config.User, payload string, kind listenerKind) string {
	mac := hmac.New(sha256.New, s.accounts.key)
	fmt.Fprintf(mac, "cineroute-account-v2:%s\x00%s\x00%s\x00%s\x00%s\x00%s", kind, u.Name, u.Role, u.Password, u.PasswordHash, payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) accountToken(u *config.User, expires time.Time, kind listenerKind) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(strings.ToLower(u.Name))) + ":" + strconv.FormatInt(expires.Unix(), 10)
	return payload + "." + s.accountSignature(u, payload, kind)
}
func (s *Server) authenticate(r *http.Request) (account, bool) {
	if s.accounts.err != nil {
		return account{}, false
	}
	if len(s.cfg.Users) == 0 {
		name := s.cfg.AuthUsername
		if name == "" {
			name = "admin"
		}
		a := account{name, "admin"}
		if s.cfg.AuthPassword == "" || audience(r) == publicListener {
			return account{}, false
		}
		if cookie, err := r.Cookie(cookieName(r)); err == nil && validSession(s.legacySessionSecret(), cookie.Value) {
			return a, true
		}
		user, pass, ok := r.BasicAuth()
		if !ok || s.loginBlocked(r) {
			return account{}, false
		}
		valid := user == s.cfg.AuthUsername && subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AuthPassword)) == 1
		if valid {
			s.passwordSucceeded(r)
		}
		return a, valid
	}
	if cookie, err := r.Cookie(cookieName(r)); err == nil {
		payload, sig, ok := strings.Cut(cookie.Value, ".")
		encoded, stamp, found := strings.Cut(payload, ":")
		name, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
		expires, parseErr := strconv.ParseInt(stamp, 10, 64)
		if ok && found && decodeErr == nil && parseErr == nil && expires > time.Now().Unix() && expires <= time.Now().Add(cookieTTL(r)+time.Minute).Unix() {
			if u := s.findAccount(string(name)); u != nil && hmac.Equal([]byte(sig), []byte(s.accountSignature(u, payload, audience(r)))) {
				return account{u.Name, u.Role}, accountAllowed(r, u.Role)
			}
		}
	}
	name, pass, ok := r.BasicAuth()
	if ok {
		if s.loginBlocked(r) {
			return account{}, false
		}
		if u := s.findAccount(name); u != nil && accountAllowed(r, u.Role) && passwordMatches(u, pass) {
			s.passwordSucceeded(r)
			return account{u.Name, u.Role}, true
		}
	}
	return account{}, false
}
func publicPath(p string) bool {
	return p == "/" || p == "/health" || p == "/favicon.png" || p == "/favicon.svg" || p == "/logo.svg" || p == "/requests.js" || p == "/catalog.js" || p == "/portal.js" || p == "/app.css" || p == "/api/login"
}
func userPath(r *http.Request) bool {
	p := r.URL.Path
	return (r.Method == http.MethodGet && (p == "/api/session" || p == "/api/catalog/search" || strings.HasPrefix(p, "/api/catalog/movie/") || strings.HasPrefix(p, "/api/catalog/tv/") || p == "/api/user/requests")) ||
		(r.Method == http.MethodPost && (p == "/api/logout" || p == "/api/user/requests" || (strings.HasPrefix(p, "/api/user/requests/") && strings.HasSuffix(p, "/cancel"))))
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		a, ok := s.authenticate(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !accountAllowed(r, a.Role) || (a.Role != "admin" && !userPath(r)) {
			writeErr(w, http.StatusForbidden, "access denied")
			return
		}
		// Cookie-authenticated mutations require a token fetched from this listener.
		// Explicit Basic Auth remains available to command-line clients.
		if unsafeMethod(r.Method) {
			if _, err := r.Cookie(cookieName(r)); err == nil && !validCSRF(r) {
				writeErr(w, http.StatusForbidden, "CSRF token required")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountContext{}, a)))
	})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"account": actor(r), "auth_enabled": s.authEnabled(), "csrf_token": csrfToken(r)})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() || (audience(r) == publicListener && len(s.cfg.Users) == 0) {
		writeErr(w, http.StatusServiceUnavailable, "Account configuration is required.")
		return
	}
	if mediaType := strings.Split(r.Header.Get("Content-Type"), ";")[0]; mediaType != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "Use application/json.")
		return
	}
	if s.accounts.err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Account sessions are unavailable. Check the writable data directory.")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	if s.loginBlocked(r) {
		writeErr(w, http.StatusTooManyRequests, "Too many failed sign-ins. Try again in a few minutes.")
		return
	}
	u := s.findAccount(body.Username)
	valid := false
	name, role := body.Username, "admin"
	if len(s.cfg.Users) > 0 {
		if u != nil && accountAllowed(r, u.Role) {
			valid = passwordMatches(u, body.Password)
			name, role = u.Name, u.Role
		}
	} else {
		valid = (s.cfg.AuthUsername == "" || body.Username == s.cfg.AuthUsername) && subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.cfg.AuthPassword)) == 1
	}
	if !valid {
		writeErr(w, 401, "invalid username or password")
		return
	}
	s.passwordSucceeded(r)
	expires := time.Now().Add(cookieTTL(r))
	token := sessionToken(s.legacySessionSecret(), expires)
	if u != nil {
		token = s.accountToken(u, expires, audience(r))
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: token, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: sessionSameSite(r), Expires: expires, MaxAge: int(cookieTTL(r).Seconds())})
	writeJSON(w, 200, map[string]any{"account": account{name, role}})
}

// HashPassword is shared by the CLI and config validation examples.
func HashPassword(input io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil {
		return "", err
	}
	b = []byte(strings.TrimRight(string(b), "\r\n"))
	if len(b) == 0 || len(b) > 72 {
		return "", fmt.Errorf("password must be 1–72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword(b, bcrypt.DefaultCost)
	return string(hash), err
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func sessionSameSite(r *http.Request) http.SameSite {
	if audience(r) == adminListener {
		return http.SameSiteStrictMode
	}
	return http.SameSiteLaxMode
}

// The legacy admin password invalidates sessions when changed, but is never
// itself the signing key: a stolen cookie cannot be used to guess it offline.
func (s *Server) legacySessionSecret() string {
	mac := hmac.New(sha256.New, s.accounts.key)
	fmt.Fprintf(mac, "cineroute-legacy-admin-v2:%s\x00%s", s.cfg.AuthUsername, s.cfg.AuthPassword)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: "", Path: "/", Secure: secureRequest(r), HttpOnly: true, SameSite: sessionSameSite(r), Expires: time.Unix(1, 0), MaxAge: -1})
	writeJSON(w, 200, map[string]any{})
}
func (s *Server) failureKey(r *http.Request) string {
	return string(audience(r)) + ":" + peerAddress(r).String()
}
func (s *Server) loginBlocked(r *http.Request) bool {
	s.accounts.mu.Lock()
	defer s.accounts.mu.Unlock()
	if len(s.accounts.failures) >= 10000 {
		for key, failures := range s.accounts.failures {
			if len(failures) == 0 || time.Since(failures[len(failures)-1]) >= 15*time.Minute {
				delete(s.accounts.failures, key)
			}
		}
		if len(s.accounts.failures) >= 10000 {
			if _, known := s.accounts.failures[s.failureKey(r)]; !known {
				return true
			}
		}
	}
	key := s.failureKey(r)
	recent := []time.Time{}
	for _, stamp := range s.accounts.failures[key] {
		if time.Since(stamp) < 15*time.Minute {
			recent = append(recent, stamp)
		}
	}
	if len(recent) >= 10 {
		s.accounts.failures[key] = recent
		return true
	}
	// Reserve before checking the password, so concurrent attempts cannot all
	// pass the limit before any failure is recorded.
	s.accounts.failures[key] = append(recent, time.Now())
	return false
}
func (s *Server) passwordSucceeded(r *http.Request) {
	s.accounts.mu.Lock()
	delete(s.accounts.failures, s.failureKey(r))
	s.accounts.mu.Unlock()
}
