package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cineroute/internal/config"
	"cineroute/internal/music"
	musicconfig "cineroute/internal/music/config"
	"cineroute/internal/music/jobs"
	musiclibrary "cineroute/internal/music/library"
	"cineroute/internal/music/lyrics"
	"cineroute/internal/music/qobuz"
	"time"
)

type musicFixture struct{}

func (musicFixture) Album(_ context.Context, id string) (qobuz.Album, error) {
	return qobuz.Album{ID: id, Title: "Test album", Artist: "Test artist", Streamable: true, Tracks: []qobuz.Track{{ID: "1", Title: "Song"}}}, nil
}
func (musicFixture) Search(context.Context, string, int, int) (json.RawMessage, error) {
	return json.RawMessage(`{"albums":{"items":[]},"tracks":{"items":[]},"artists":{"items":[]}}`), nil
}
func (musicFixture) AlbumRaw(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"id":"album1"}`), nil
}
func (musicFixture) ArtistPage(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"id":"artist1"}`), nil
}
func (musicFixture) ArtistReleases(context.Context, string, string, int, int, int) (json.RawMessage, error) {
	return json.RawMessage(`{"items":[]}`), nil
}
func musicServer(t *testing.T) *Server {
	t.Helper()
	s := securityServer(t)
	s.cfg.Users = append(s.cfg.Users, config.User{Name: "bob", Role: "user", Password: "bob-secret"})
	cfg := musicconfig.Default()
	root := t.TempDir()
	cfg.Paths.Library = filepath.Join(root, "library")
	cfg.Paths.Staging = filepath.Join(root, "staging")
	cfg.Paths.Data = filepath.Join(root, "state")
	m, err := jobs.New(&cfg, jobs.Deps{Albums: musicFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	s.music = &music.Service{Jobs: m, Catalog: musicFixture{}}
	return s
}
func musicCall(s *Server, user, method, path, body string) *httptest.ResponseRecorder {
	r := securityRequest(method, path)
	r.Body = http.NoBody
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.SetBasicAuth(user, user+"-secret")
	r.Header.Set("Content-Type", "application/json")
	return securityCall(s.PublicHandler(), r)
}
func decodeMusicJob(t *testing.T, out *httptest.ResponseRecorder) jobs.Job {
	t.Helper()
	if out.Code != 200 {
		t.Fatalf("music request: %d %s", out.Code, out.Body.String())
	}
	var result struct {
		Data jobs.Job `json:"data"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}
func TestMusicRequestsAreOwnedByUser(t *testing.T) {
	s := musicServer(t)
	alice := decodeMusicJob(t, musicCall(s, "alice", "POST", "/api/music/jobs", `{"album_id":"album1"}`))
	bob := decodeMusicJob(t, musicCall(s, "bob", "POST", "/api/music/jobs", `{"album_id":"album1"}`))
	if alice.ID == bob.ID || alice.RequestedBy != "alice" || bob.RequestedBy != "bob" {
		t.Fatal("album request ownership was shared")
	}
	again := decodeMusicJob(t, musicCall(s, "alice", "POST", "/api/music/jobs", `{"album_id":"album1"}`))
	if again.ID != alice.ID {
		t.Fatal("same user's pending request was duplicated")
	}
	for _, path := range []string{"/api/music/jobs/" + alice.ID + "/cancel", "/api/music/jobs/" + alice.ID + "/retry"} {
		if out := musicCall(s, "bob", "POST", path, ""); out.Code != 404 {
			t.Fatalf("other user can mutate request: %d", out.Code)
		}
	}
	if out := musicCall(s, "bob", "DELETE", "/api/music/jobs/"+alice.ID, ""); out.Code != 404 {
		t.Fatal("other user can dismiss request")
	}
	out := musicCall(s, "bob", "GET", "/api/music/jobs", "")
	if out.Code != 200 || strings.Contains(out.Body.String(), alice.ID) || !strings.Contains(out.Body.String(), bob.ID) {
		t.Fatalf("queue leaked another user's request: %s", out.Body.String())
	}
	decodeMusicJob(t, musicCall(s, "alice", "POST", "/api/music/jobs/"+alice.ID+"/cancel", ""))
	retry := decodeMusicJob(t, musicCall(s, "alice", "POST", "/api/music/jobs/"+alice.ID+"/retry", ""))
	if retry.ID != alice.ID || retry.RequestedBy != "alice" {
		t.Fatal("retry returned another user's pending album")
	}
}
func TestMusicUsesCineRouteSessionAndCSRF(t *testing.T) {
	s := musicServer(t)
	h := s.PublicHandler()
	cookie := securityLogin(t, s, publicListener, "alice", "alice-secret")
	r := securityRequest("POST", "/api/music/jobs")
	r.Body = http.NoBody
	r.AddCookie(cookie)
	if out := securityCall(h, r); out.Code != 403 {
		t.Fatal("music mutation accepted without CSRF")
	}
	csrf := securityCSRF(t, h, cookie)
	r = httptest.NewRequest("POST", "/api/music/jobs", strings.NewReader(`{"album_id":"album1"}`))
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CineRoute-CSRF", csrf)
	decodeMusicJob(t, securityCall(h, r))
	r = securityRequest("GET", "/api/music/jobs")
	if out := securityCall(h, r); out.Code != 401 {
		t.Fatal("anonymous music queue access")
	}
	for _, body := range []string{`{"album_id":"album1","requested_by":"bob"}`, `{"album_id":"../../private"}`, `{"album_id":"album1","quality":4}`} {
		if out := musicCall(s, "alice", "POST", "/api/music/jobs", body); out.Code != 400 {
			t.Fatalf("unsafe album payload accepted: %s", body)
		}
	}
	if out := musicCall(s, "alice", "GET", "/api/music/jobs/private/log", ""); out.Code < 400 {
		t.Fatal("raw music logs exposed")
	}
}
func TestDisabledMusicDoesNotBreakVideoPortal(t *testing.T) {
	s := securityServer(t)
	if out := musicCall(s, "alice", "GET", "/api/music/jobs", ""); out.Code != 200 || !strings.Contains(out.Body.String(), `"jobs":[]`) {
		t.Fatalf("disabled music queue: %d %s", out.Code, out.Body.String())
	}
	if out := musicCall(s, "alice", "POST", "/api/music/jobs", `{"album_id":"album1"}`); out.Code != 503 {
		t.Fatal("disabled music accepted a download")
	}
	if out := musicCall(s, "alice", "GET", "/api/user/requests", ""); out.Code != 200 {
		t.Fatal("video requests unavailable with music disabled")
	}
}

func TestMusicCatalogBadgesUseLibraryFolders(t *testing.T) {
	s := musicServer(t)
	cfg := musicconfig.Default()
	root := t.TempDir()
	cfg.Paths.Library = filepath.Join(root, "library")
	cfg.Paths.Staging = filepath.Join(root, "staging")
	cfg.Paths.Data = filepath.Join(root, "state")
	m, err := jobs.New(&cfg, jobs.Deps{Albums: musicFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	s.music.Jobs = m
	raw := json.RawMessage(`{"albums":{"items":[{"id":"a1","title":"A single","artist":{"name":"Artist"},"release_date_original":"2024-01-01"}]},"tracks":{"items":[{"id":123,"title":"Song","album":{"id":"a1","title":"A single","artist":{"name":"Artist"},"release_date_original":"2024-01-01"}}]}}`)
	album, err := qobuz.ParseAlbumMetadata(json.RawMessage(`{"id":"a1","title":"A single","artist":{"name":"Artist"},"release_date_original":"2024-01-01"}`))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := musiclibrary.FolderFor(cfg.Naming.Folder, album, musiclibrary.Quality{}, cfg.Qobuz.Quality, cfg.Naming.RestrictCharacters)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Paths.Library, rel)
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "01. Song.flac"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	decorated := string(s.musicLibraryBadges(raw))
	if strings.Count(decorated, `"in_library":true`) != 2 || strings.Contains(decorated, root) {
		t.Fatalf("music badges: %s", decorated)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	m.RefreshLibrary()
	if strings.Contains(string(s.musicLibraryBadges(raw)), `"in_library":true`) {
		t.Fatal("removed release still in library")
	}
}

func TestLyricsStatusAndRetryAreOwnedAndDoNotExposePrivatePaths(t *testing.T) {
	s := musicServer(t)
	job := decodeMusicJob(t, musicCall(s, "alice", "POST", "/api/music/jobs", `{"album_id":"album1"}`))
	root := t.TempDir()
	rel := "Private Artist/Private Album"
	os.MkdirAll(filepath.Join(root, "music", rel), 0755)
	q, err := lyrics.New(lyrics.Options{Library: filepath.Join(root, "music"), Data: filepath.Join(root, "private-data"), MaxAttempts: 1, RetryDelay: time.Minute, Timeout: time.Minute}, func(context.Context, lyrics.Task) (lyrics.Summary, error) { return lyrics.Summary{Deferred: 1}, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	s.music.Lyrics = q
	if err := q.Enqueue(job.ID, rel); err != nil {
		t.Fatal(err)
	}
	out := musicCall(s, "alice", "GET", "/api/music/jobs", "")
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"lyrics":{"status":"queued"`) {
		t.Fatal(out.Body.String())
	}
	for _, secret := range []string{root, "Private Artist", "queue.db", "lyrics_requested", "lyrics_enqueued"} {
		if strings.Contains(out.Body.String(), secret) {
			t.Fatalf("private lyrics data exposed: %s", out.Body.String())
		}
	}
	if out := musicCall(s, "bob", "POST", "/api/music/jobs/"+job.ID+"/retry-lyrics", ""); out.Code != 404 {
		t.Fatal("other user can retry lyrics", out.Code)
	}
	// A queued task cannot be duplicated by repeated clicks.
	if out := musicCall(s, "alice", "POST", "/api/music/jobs/"+job.ID+"/retry-lyrics", ""); out.Code != 409 {
		t.Fatal("queued task duplicated", out.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); q.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state := q.Get(job.ID)
		if state != nil && state.Status == "partial" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if q.Get(job.ID).Status != "partial" {
		t.Fatal("lyrics did not finish with retryable partial result")
	}
	retried := decodeMusicJob(t, musicCall(s, "alice", "POST", "/api/music/jobs/"+job.ID+"/retry-lyrics", ""))
	if retried.Lyrics == nil || retried.Lyrics.Status != "queued" {
		t.Fatal("owner retry did not queue only lyrics")
	}
}
