package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cineroute/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestConfigAccountsEnforceRolesAndInvalidateSessions(t *testing.T) {
	cfg := requestTestConfig(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("alice-secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Users = []config.User{{Name: "admin", Role: "admin", Password: "admin-secret"}, {Name: "Alice", Role: "user", PasswordHash: string(hash)}, {Name: "bob", Role: "user", Password: "bob-secret"}}
	srv := New(cfg, nil, nil)
	handler := srv.PublicHandler()
	login := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"ALICE","password":"alice-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, req)
	if login.Code != 200 {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing user session")
	}
	call := func(h http.Handler, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	session := call(handler, "GET", "/api/session", cookies[0])
	var identity struct {
		Account account `json:"account"`
	}
	if err := json.Unmarshal(session.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.Account.Role != "user" || identity.Account.Name != "Alice" {
		t.Fatalf("identity: %+v", identity)
	}
	for _, path := range []string{"/api/requests/settings", "/api/requests/indexers", "/api/requests/queue", "/api/status", "/api/intakes", "/api/hardlinks", "/api/subtitles", "/api/copies"} {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
			if out := call(handler, method, path, cookies[0]); out.Code != http.StatusForbidden {
				t.Errorf("user %s %s: %d", method, path, out.Code)
			}
		}
	}
	if out := call(handler, "GET", "/api/user/requests", cookies[0]); out.Code != 200 {
		t.Fatalf("own requests: %s", out.Body.String())
	}
	for _, change := range []string{"password", "role", "disabled", "removed"} {
		t.Run(change, func(t *testing.T) {
			changed := *cfg
			changed.Users = append([]config.User{}, cfg.Users...)
			switch change {
			case "password":
				changed.Users[1].PasswordHash = ""
				changed.Users[1].Password = "new-password"
			case "role":
				changed.Users[1].Role = "admin"
			case "disabled":
				changed.Users[1].Disabled = true
			case "removed":
				changed.Users = append(changed.Users[:1], changed.Users[2:]...)
			}
			out := call(New(&changed, nil, nil).PublicHandler(), "GET", "/api/session", cookies[0])
			if out.Code != 401 {
				t.Fatalf("old session survived %s: %d", change, out.Code)
			}
		})
	}
	restarted := New(cfg, nil, nil).PublicHandler()
	if out := call(restarted, "GET", "/api/session", cookies[0]); out.Code != 200 {
		t.Fatalf("session lost on restart: %d", out.Code)
	}
	forged := *cookies[0]
	forged.Value = strings.Replace(forged.Value, ".", ".forged", 1)
	if out := call(handler, "GET", "/api/session", &forged); out.Code != 401 {
		t.Fatal("forged session accepted")
	}
}
