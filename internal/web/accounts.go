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
	"net"
	"net/http"
	"net/url"
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
	if len(cfg.Users) == 0 {
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
func (s *Server) accountSignature(u *config.User, payload string) string {
	mac := hmac.New(sha256.New, s.accounts.key)
	fmt.Fprintf(mac, "cineroute-account-v1:%s\x00%s\x00%s\x00%s\x00%s", u.Name, u.Role, u.Password, u.PasswordHash, payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) accountToken(u *config.User, expires time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(strings.ToLower(u.Name))) + ":" + strconv.FormatInt(expires.Unix(), 10)
	return payload + "." + s.accountSignature(u, payload)
}
func (s *Server) authenticate(r *http.Request) (account, bool) {
	if len(s.cfg.Users) == 0 {
		name := s.cfg.AuthUsername
		if name == "" {
			name = "admin"
		}
		a := account{name, "admin"}
		if s.cfg.AuthPassword == "" {
			return a, true
		}
		if cookie, err := r.Cookie(sessionCookie); err == nil && validSession(s.cfg.AuthPassword, cookie.Value) {
			return a, true
		}
		user, pass, ok := r.BasicAuth()
		return a, ok && user == s.cfg.AuthUsername && subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AuthPassword)) == 1
	}
	if s.accounts.err != nil {
		return account{}, false
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		payload, sig, ok := strings.Cut(cookie.Value, ".")
		encoded, stamp, found := strings.Cut(payload, ":")
		name, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
		expires, parseErr := strconv.ParseInt(stamp, 10, 64)
		if ok && found && decodeErr == nil && parseErr == nil && expires > time.Now().Unix() && expires <= time.Now().Add(sessionTTL+time.Minute).Unix() {
			if u := s.findAccount(string(name)); u != nil && hmac.Equal([]byte(sig), []byte(s.accountSignature(u, payload))) {
				return account{u.Name, u.Role}, true
			}
		}
	}
	name, pass, ok := r.BasicAuth()
	if ok {
		if u := s.findAccount(name); u != nil && passwordMatches(u, pass) {
			return account{u.Name, u.Role}, true
		}
	}
	return account{}, false
}
func publicPath(p string) bool {
	return p == "/" || p == "/health" || p == "/favicon.png" || p == "/favicon.svg" || p == "/logo.svg" || p == "/requests.js" || p == "/catalog.js" || p == "/api/login"
}
func userPath(r *http.Request) bool {
	p := r.URL.Path
	return (r.Method == http.MethodGet && (p == "/api/session" || p == "/api/catalog/search" || strings.HasPrefix(p, "/api/catalog/movie/") || strings.HasPrefix(p, "/api/catalog/tv/") || p == "/api/user/requests")) ||
		(r.Method == http.MethodPost && (p == "/api/logout" || p == "/api/user/requests" || (strings.HasPrefix(p, "/api/user/requests/") && strings.HasSuffix(p, "/cancel"))))
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if origin := r.Header.Get("Origin"); origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Host != r.Host || (parsed.Scheme != "https" && parsed.Scheme != "http") {
					writeErr(w, 403, "cross-origin request rejected")
					return
				}
			}
		}
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		a, ok := s.authenticate(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if a.Role != "admin" && !userPath(r) {
			writeErr(w, http.StatusForbidden, "administrator access required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountContext{}, a)))
	})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"account": actor(r), "auth_enabled": s.authEnabled()})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		writeJSON(w, http.StatusOK, map[string]any{"account": account{"admin", "admin"}})
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
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	s.accounts.mu.Lock()
	recent := []time.Time{}
	for _, stamp := range s.accounts.failures[ip] {
		if time.Since(stamp) < 15*time.Minute {
			recent = append(recent, stamp)
		}
	}
	s.accounts.failures[ip] = recent
	blocked := len(recent) >= 10
	s.accounts.mu.Unlock()
	if blocked {
		writeErr(w, http.StatusTooManyRequests, "Too many failed sign-ins. Try again in a few minutes.")
		return
	}
	u := s.findAccount(body.Username)
	valid := false
	name, role := body.Username, "admin"
	if len(s.cfg.Users) > 0 {
		if u != nil {
			valid = passwordMatches(u, body.Password)
			name, role = u.Name, u.Role
		}
	} else {
		valid = (s.cfg.AuthUsername == "" || body.Username == s.cfg.AuthUsername) && subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.cfg.AuthPassword)) == 1
	}
	if !valid {
		s.accounts.mu.Lock()
		s.accounts.failures[ip] = append(recent, time.Now())
		s.accounts.mu.Unlock()
		writeErr(w, 401, "invalid username or password")
		return
	}
	s.accounts.mu.Lock()
	delete(s.accounts.failures, ip)
	s.accounts.mu.Unlock()
	expires := time.Now().Add(sessionTTL)
	token := sessionToken(s.cfg.AuthPassword, expires)
	if u != nil {
		token = s.accountToken(u, expires)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(sessionTTL.Seconds())})
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
