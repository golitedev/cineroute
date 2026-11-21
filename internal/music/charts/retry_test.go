package charts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
)

type retryCatalog struct{ err error }

func (c *retryCatalog) SearchRecordings(context.Context, string) ([]qobuz.Recording, error) {
	return nil, c.err
}

func TestChartQobuzRetryBackoff(t *testing.T) {
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"subsonic-response":{"status":"ok","searchResult3":{}}}`)
	}))
	defer nav.Close()
	catalog := &retryCatalog{err: errors.New("Qobuz unavailable")}
	m, err := New(config.Paths{Data: t.TempDir()}, catalog, navidrome.New(nav.URL, "user", "pass"), new(testQueue))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.lookupIdentity = func(context.Context, string) (trackIdentity, error) {
		return trackIdentity{}, errors.New("Credits.fm unavailable")
	}
	e := entry{Track: Track{SpotifyID: "track", Title: "Missing Song", Artists: []string{"Artist"}}}
	now := time.Now()
	for _, hours := range []int{1, 2, 4, 8, 16, 24, 24} {
		if err := m.resolve(t.Context(), playlist{}, &e, now); err != nil {
			t.Fatal(err)
		}
		if delay := e.NextAttempt.Sub(now); e.State != "retrying" || delay != time.Duration(hours)*time.Hour {
			t.Fatalf("state %q, retry delay %v, want retrying after %dh", e.State, delay, hours)
		}
		now = e.NextAttempt
	}
	catalog.err = nil
	if err := m.resolve(t.Context(), playlist{}, &e, now); err != nil {
		t.Fatal(err)
	}
	if e.State != "needs_review" || e.QobuzFailures != 0 || e.NextAttempt.Sub(now) != 24*time.Hour {
		t.Fatalf("expected a daily check and a reset failure count, got %+v", e)
	}
}
