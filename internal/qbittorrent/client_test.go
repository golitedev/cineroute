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

// TestIncompleteBytesUnder verifies the pure snapshot helper sums amount_left
// only for torrents whose save path is under one of the given roots.
func TestIncompleteBytesUnder(t *testing.T) {
	ts := []Torrent{
		{Hash: "a", SavePath: "/m1/folder", AmountLeft: 10},
		{Hash: "b", SavePath: "/m1/folder2", AmountLeft: 0}, // complete: skipped
		{Hash: "c", SavePath: "/m2/folder", AmountLeft: 25},
		{Hash: "d", SavePath: "/elsewhere", AmountLeft: 100},
		{Hash: "e", SavePath: "/m1", AmountLeft: 5}, // exactly at root
	}
	if got := IncompleteBytesUnder(ts, "/m1"); got != 15 {
		t.Fatalf("IncompleteBytesUnder(/m1): got %d want 15", got)
	}
	if got := IncompleteBytesUnder(ts, "/m2"); got != 25 {
		t.Fatalf("IncompleteBytesUnder(/m2): got %d want 25", got)
	}
	if got := IncompleteBytesUnder(ts, "/m1", "/m2"); got != 40 {
		t.Fatalf("IncompleteBytesUnder(/m1,/m2): got %d want 40", got)
	}
	if got := IncompleteBytesUnder(nil, "/m1"); got != 0 {
		t.Fatalf("IncompleteBytesUnder(nil): got %d want 0", got)
	}
}
