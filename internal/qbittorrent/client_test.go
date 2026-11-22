package qbittorrent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestSessionExpiryRecovery verifies that a 403 after a previously successful
// call (expired SID / qBittorrent restart) triggers a fresh login and retry
// instead of surfacing the raw 403 until the app restarts.
func TestSessionExpiryRecovery(t *testing.T) {
	t.Run("legacy OK", func(t *testing.T) {
		testSessionExpiryRecovery(t, "SID", http.StatusOK)
	})
	t.Run("no content", func(t *testing.T) {
		testSessionExpiryRecovery(t, "QBT_SID_8080", http.StatusNoContent)
	})
}

func testSessionExpiryRecovery(t *testing.T, cookieName string, loginStatus int) {
	t.Helper()
	var acceptedSID atomic.Value
	acceptedSID.Store("v1")
	var logins int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			atomic.AddInt64(&logins, 1)
			if r.FormValue("username") != "admin" || r.FormValue("password") != "secret" {
				io.WriteString(w, "Fails.")
				return
			}
			// Hand out the currently accepted SID so the retried request
			// immediately succeeds. Path "/" makes the cookie apply to the
			// whole API surface, like qBittorrent's real session cookie.
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: acceptedSID.Load().(string), Path: "/"})
			w.WriteHeader(loginStatus)
			if loginStatus == http.StatusOK {
				io.WriteString(w, "Ok.")
			}
		case "/api/v2/app/version":
			c, err := r.Cookie(cookieName)
			if err != nil || c.Value != acceptedSID.Load().(string) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			io.WriteString(w, "v5.0.4")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, "admin", "secret", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// First call authenticates and caches the SID "v1" in the cookie jar.
	if _, err := c.AppVersion(context.Background()); err != nil {
		t.Fatalf("first AppVersion: %v", err)
	}

	// Simulate expiry: the server only accepts SID "v2" now, which the next
	// login will hand out. The cached "v1" cookie must not block re-auth.
	acceptedSID.Store("v2")

	if _, err := c.AppVersion(context.Background()); err != nil {
		t.Fatalf("second AppVersion after session expiry: %v", err)
	}
	if got := atomic.LoadInt64(&logins); got != 2 {
		t.Fatalf("logins: got %d want 2", got)
	}
}

func TestStartWithNoContentResponses(t *testing.T) {
	var starts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "QBT_SID_8080", Value: "session", Path: "/"})
			w.WriteHeader(http.StatusNoContent)
		case "/api/v2/torrents/start":
			c, err := r.Cookie("QBT_SID_8080")
			if err != nil || c.Value != "session" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if r.Method != http.MethodPost || r.FormValue("hashes") != "movie-hash" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, "admin", "secret", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background(), "movie-hash"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("start operations: got %d want 1", got)
	}
}

// TestWrongCredentialsStillFail verifies that a client with wrong credentials
// still fails: the server replies 200 "Fails." on the login and the request
// error surfaces instead of looping or succeeding.
func TestWrongCredentialsStillFail(t *testing.T) {
	var acceptedSID atomic.Value
	acceptedSID.Store("v1")
	var logins int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			atomic.AddInt64(&logins, 1)
			if r.FormValue("username") != "admin" || r.FormValue("password") != "right" {
				io.WriteString(w, "Fails.")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: acceptedSID.Load().(string), Path: "/"})
			io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			c, err := r.Cookie("SID")
			if err != nil || c.Value != acceptedSID.Load().(string) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			io.WriteString(w, "v5.0.4")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, "admin", "wrong", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.AppVersion(context.Background()); err == nil {
		t.Fatal("expected AppVersion to fail with wrong credentials")
	}
	if got := atomic.LoadInt64(&logins); got != 1 {
		t.Fatalf("logins: got %d want 1", got)
	}
}
