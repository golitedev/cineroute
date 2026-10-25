package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/prowlarr"
	"cineroute/internal/qbittorrent"
	"cineroute/internal/requests"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

type workflowFixture struct {
	s        *Server
	qb       *fakeQB
	cfg      *config.Config
	mu       sync.Mutex
	torrent  []byte
	title    string
	searches int
}

func newWorkflowFixture(t *testing.T, tv bool) *workflowFixture {
	t.Helper()
	cfg := requestTestConfig(t)
	base := t.TempDir()
	cfg.Drives = nil
	for _, id := range []string{"disk1", "disk2"} {
		drive := config.Drive{ID: id, MovieRoot: filepath.Join(base, id, "movies"), MovieRemoteRoot: filepath.Join(base, id, "movies-remote"), TVRoot: filepath.Join(base, id, "tv"), TVRemoteRoot: filepath.Join(base, id, "tv-remote")}
		cfg.Drives = append(cfg.Drives, drive)
		for _, p := range []string{drive.MovieRoot, drive.MovieRemoteRoot, drive.TVRoot, drive.TVRemoteRoot} {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg.Users = []config.User{{Name: "admin", Role: "admin", Password: "admin-secret"}, {Name: "alice", Role: "user", Password: "alice-secret"}, {Name: "bob", Role: "user", Password: "bob-secret"}}
	f := &workflowFixture{cfg: cfg, qb: newFakeQB(), title: "Example.Movie.2024.1080p.WEB-DL.x264"}
	f.torrent = singleFileTorrent(f.title+".mkv", 512)
	if tv {
		f.title = "Example.Show.S01.1080p.WEB-DL.x264"
		f.torrent = buildFixtureTorrent(f.title, 0, map[string]int64{"Example.Show.S01E01.mkv": 512, "Example.Show.S01E02.mkv": 512})
	}
	qbs := httptest.NewServer(f.qb.handler())
	t.Cleanup(qbs.Close)
	qb, err := qbittorrent.New(qbs.URL, "admin", "password", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/101":
			fmt.Fprint(w, `{"id":101,"title":"Example Movie","release_date":"2024-01-01","original_language":"en","status":"Released","poster_path":"/movie.jpg"}`)
		case "/tv/202":
			fmt.Fprint(w, `{"id":202,"name":"Example Show","first_air_date":"2024-01-01","original_language":"en","seasons":[{"season_number":1,"name":"Season 1","episode_count":2,"air_date":"2024-01-01"}]}`)
		case "/tv/202/season/1":
			fmt.Fprint(w, `{"episodes":[{"season_number":1,"episode_number":1,"air_date":"2024-01-01"},{"season_number":1,"episode_number":2,"air_date":"2024-01-08"}]}`)
		case "/search/multi":
			fmt.Fprint(w, `{"page":1,"total_pages":1,"results":[{"id":101,"title":"Example Movie","media_type":"movie","release_date":"2024-01-01","poster_path":"/movie.jpg"},{"id":202,"name":"Example Show","media_type":"tv"},{"id":999,"name":"A Person","media_type":"person"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(metadata.Close)
	tm := tmdb.New("private-tmdb-key", "en-US", time.Second)
	tm.SetBaseURL(metadata.URL)
	var indexers *httptest.Server
	indexers = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/indexer":
			fmt.Fprint(w, `[{"id":7,"name":"Selected tracker","protocol":"torrent","enable":true},{"id":9,"name":"Unselected tracker","protocol":"torrent","enable":true}]`)
		case "/api/v1/search":
			f.searches++
			ids := r.URL.Query()["indexerIds"]
			if len(ids) != 1 || ids[0] != "7" {
				t.Errorf("search used unselected trackers: %v", ids)
			}
			seeders := 10
			json.NewEncoder(w).Encode([]prowlarr.Release{{Guid: "release-one", IndexerID: 7, Title: f.title, Seeders: &seeders, Size: 512, DownloadURL: indexers.URL + "/download?apikey=private-download-key"}})
		case "/download":
			w.Write(f.torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(indexers.Close)
	f.s = New(cfg, qb, tm, prowlarr.New(indexers.URL, "private-indexer-key", time.Second))
	t.Cleanup(f.s.Close)
	settings := requests.Defaults()
	settings.Trackers = []requests.Tracker{{ID: 7, Name: "Selected tracker", Priority: 1}}
	settings.SearchIntervalSeconds = 1
	if err := f.s.requests.Save(settings); err != nil {
		t.Fatal(err)
	}
	return f
}
func userWorkflowCall(t *testing.T, s *Server, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetBasicAuth(user, user+"-secret")
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, r)
	return out
}
func createWorkflowJob(t *testing.T, f *workflowFixture, user string, tv bool) requests.Job {
	t.Helper()
	body := `{"media_type":"movie","tmdb_id":101}`
	if tv {
		body = `{"media_type":"tv","tmdb_id":202,"seasons":[1],"follow_future":false}`
	}
	out := userWorkflowCall(t, f.s, user, "POST", "/api/user/requests", body)
	if out.Code != 201 {
		t.Fatalf("create: %d %s", out.Code, out.Body.String())
	}
	var response struct {
		Request publicJob `json:"request"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	job, err := f.s.requests.Job(response.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
func finishWorkflowDownload(t *testing.T, f *workflowFixture) {
	t.Helper()
	downloads, err := f.s.requests.Downloads()
	if err != nil || len(downloads) != 1 {
		t.Fatalf("downloads: %+v %v", downloads, err)
	}
	d := downloads[0]
	for path, size := range d.FileSizes {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.qb.mu.Lock()
	f.qb.started[d.Hash] = false
	f.qb.torrents[d.Hash].state = "uploading"
	f.qb.mu.Unlock()
}
func TestAutomaticMovieRequestUsesAnchorAndCompletesOnce(t *testing.T) {
	f := newWorkflowFixture(t, false)
	main := filepath.Join(f.cfg.Drives[1].MovieRoot, "Example Movie (2024)")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "old.mkv"), []byte("main copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := createWorkflowJob(t, f, "alice", false)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, err := f.s.requests.Downloads()
	if err != nil || len(downloads) != 1 {
		current, _ := f.s.requests.Job(job.ID)
		t.Fatalf("request did not download: %+v; %v", current, err)
	}
	want := filepath.Join(f.cfg.Drives[1].MovieRemoteRoot, "Example Movie (2024)")
	if downloads[0].SavePath != want || downloads[0].DriveID != "disk2" {
		t.Fatalf("wrong routing: %+v", downloads[0])
	}
	finishWorkflowDownload(t, f)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, err := f.s.requests.Job(job.ID)
	if err != nil || current.Status != "complete" {
		t.Fatalf("completion: %+v %v", current, err)
	}
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.qb.mu.Lock()
	defer f.qb.mu.Unlock()
	if len(f.qb.added) != 1 {
		t.Fatalf("duplicate adds: %d", len(f.qb.added))
	}
	response := userWorkflowCall(t, f.s, "alice", "GET", "/api/user/requests", "")
	for _, secret := range []string{want, "private-download-key", "private-indexer-key", "torrent_data", "admin_error"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Errorf("user response leaked %q", secret)
		}
	}
}
func TestTVSeasonPackStaysOnExistingShowDisk(t *testing.T) {
	f := newWorkflowFixture(t, true)
	if err := os.Mkdir(filepath.Join(f.cfg.Drives[1].TVRoot, "Example Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	job := createWorkflowJob(t, f, "alice", true)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, _ := f.s.requests.Downloads()
	if len(downloads) != 1 {
		current, _ := f.s.requests.Job(job.ID)
		t.Fatalf("season pack not added: %+v", current)
	}
	if downloads[0].DriveID != "disk2" || downloads[0].SavePath != filepath.Join(f.cfg.Drives[1].TVRemoteRoot, "Example Show") || len(downloads[0].Episodes) != 2 {
		t.Fatalf("TV routing/coverage: %+v", downloads[0])
	}
	finishWorkflowDownload(t, f)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := f.s.requests.Job(job.ID)
	if current.Status != "complete" || current.Progress != 1 {
		t.Fatalf("TV completion: %+v", current)
	}
}
func TestRequestOwnershipApprovalAndQuotas(t *testing.T) {
	f := newWorkflowFixture(t, false)
	settings, _ := f.s.requests.Load()
	settings.AutoApprove = false
	settings.MovieLimit = 1
	if err := f.s.requests.Save(settings); err != nil {
		t.Fatal(err)
	}
	alice := createWorkflowJob(t, f, "alice", false)
	bob := createWorkflowJob(t, f, "bob", false)
	if alice.Status != "pending" {
		t.Fatalf("approval bypassed: %+v", alice)
	}
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if downloads, _ := f.s.requests.Downloads(); len(downloads) != 0 {
		t.Fatal("pending request downloaded")
	}
	if out := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests/"+bob.ID+"/cancel", ""); out.Code != 404 {
		t.Fatalf("cross-user cancel: %d", out.Code)
	}
	if out := userWorkflowCall(t, f.s, "alice", "POST", "/api/requests/queue/"+alice.ID+"/approve", ""); out.Code != 403 {
		t.Fatalf("user approved own request: %d", out.Code)
	}
	if out := userWorkflowCall(t, f.s, "admin", "POST", "/api/requests/queue/"+alice.ID+"/approve", ""); out.Code != 200 {
		t.Fatalf("admin approval: %d %s", out.Code, out.Body.String())
	}
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := f.s.requests.Job(alice.ID)
	if current.Status != "downloading" {
		t.Fatalf("approved request: %+v", current)
	}
	media := alice.Media
	media.ID = 303
	media.Title = "Another Movie"
	_, err := f.s.requests.Create(requests.Job{Owner: "alice", Media: media, Target: "remote", Status: "queued"}, settings, false)
	if err != requests.ErrQuota {
		t.Fatalf("quota not enforced: %v", err)
	}
	if out := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", `{"media_type":"movie","tmdb_id":101,"destination":"main"}`); out.Code != 400 {
		t.Fatalf("user supplied routing: %d", out.Code)
	}
	search := userWorkflowCall(t, f.s, "alice", "GET", "/api/catalog/search?q=Example", "")
	if search.Code != 200 || strings.Contains(search.Body.String(), "A Person") {
		t.Fatalf("movie/show search: %s", search.Body.String())
	}
}

func TestVerifiedExistingTorrentIsHardlinkedWithoutMovingSeed(t *testing.T) {
	f := newWorkflowFixture(t, false)
	meta, err := torrentmeta.Parse(f.torrent)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(f.cfg.Drives[1].MovieRoot, "Example Movie (2024)")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, meta.Name)
	if err := os.WriteFile(filepath.Join(source, "unrequested.mkv"), []byte("other torrent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.s.qb.AddTorrent(context.Background(), f.torrent, qbittorrent.AddOptions{SavePath: source, RootFolder: meta.RootFolder, Stopped: true, Filename: "manual.torrent"}); err != nil {
		t.Fatal(err)
	}
	f.qb.mu.Lock()
	f.qb.torrents[meta.PrimaryHash()].state = "uploading"
	f.qb.mu.Unlock()
	job := createWorkflowJob(t, f, "alice", false)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.cfg.Drives[1].MovieRemoteRoot, "Example Movie (2024)", meta.Name)
	a, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(target)
	if err != nil {
		current, _ := f.s.requests.Job(job.ID)
		t.Fatalf("remote copy missing: %v; %+v", err, current)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), "unrequested.mkv")); !os.IsNotExist(err) {
		t.Fatal("unrequested source file was linked")
	}
	if !os.SameFile(a, b) {
		t.Fatal("existing seed was copied instead of hardlinked")
	}
	f.qb.mu.Lock()
	defer f.qb.mu.Unlock()
	if len(f.qb.added) != 1 || f.qb.torrents[meta.PrimaryHash()].savePath != source {
		t.Fatal("existing seeding torrent was resubmitted or moved")
	}
}

func TestRequestRecoveryDoesNotDuplicateAndRejectsMovedDestination(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprint(moved), func(t *testing.T) {
			f := newWorkflowFixture(t, false)
			job := createWorkflowJob(t, f, "alice", false)
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			downloads, err := f.s.requests.Downloads()
			if err != nil || len(downloads) != 1 {
				t.Fatalf("download intent missing: %v", err)
			}
			d := downloads[0]
			d.Status = "planned"
			d.TorrentData = f.torrent
			if err = f.s.requests.SaveDownload(d); err != nil {
				t.Fatal(err)
			}
			f.qb.mu.Lock()
			f.qb.started[d.Hash] = false
			f.qb.torrents[d.Hash].state = "stoppedDL"
			if moved {
				f.qb.torrents[d.Hash].savePath = t.TempDir()
			}
			f.qb.mu.Unlock()
			f.s.engine.Stop()
			restarted := New(f.cfg, f.s.qb, f.s.tmdb, f.s.prowlarr)
			t.Cleanup(restarted.Close)
			if err := restarted.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.qb.mu.Lock()
			started := f.qb.started[d.Hash]
			adds := len(f.qb.added)
			f.qb.mu.Unlock()
			if adds != 1 || started == moved {
				t.Fatalf("recovery moved=%v started=%v adds=%d", moved, started, adds)
			}
			current, err := restarted.requests.Job(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if moved && current.Status != "error" {
				t.Fatalf("moved torrent did not require review: %+v", current)
			}
		})
	}
}

func TestMainDestinationAndFixedNewTitleDiskAreApplied(t *testing.T) {
	f := newWorkflowFixture(t, false)
	settings, _ := f.s.requests.Load()
	settings.Movie.Destination = "main"
	settings.Movie.NewTitleDrive = "disk1"
	if err := f.s.requests.Save(settings); err != nil {
		t.Fatal(err)
	}
	job := createWorkflowJob(t, f, "alice", false)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, _ := f.s.requests.Downloads()
	if len(downloads) != 1 {
		current, _ := f.s.requests.Job(job.ID)
		t.Fatalf("no download: %+v", current)
	}
	if downloads[0].SavePath != filepath.Join(f.cfg.Drives[0].MovieRoot, "Example Movie (2024)") {
		t.Fatalf("main/fixed disk ignored: %+v", downloads[0])
	}
}

func TestManuallyPausedRequestIsNotFailedAsStalled(t *testing.T) {
	f := newWorkflowFixture(t, false)
	job := createWorkflowJob(t, f, "alice", false)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, _ := f.s.requests.Downloads()
	d := downloads[0]
	d.LastProgress = time.Now().Add(-24 * time.Hour)
	if err := f.s.requests.SaveDownload(d); err != nil {
		t.Fatal(err)
	}
	f.qb.mu.Lock()
	f.qb.started[d.Hash] = false
	f.qb.torrents[d.Hash].state = "stoppedDL"
	f.qb.mu.Unlock()
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, _ = f.s.requests.Downloads()
	if downloads[0].Status != "downloading" || !downloads[0].Paused {
		t.Fatalf("manual pause treated as failure: %+v", downloads[0])
	}
	current, _ := f.s.requests.Job(job.ID)
	if current.Status != "downloading" {
		t.Fatalf("paused job: %+v", current)
	}
}

func TestMovieUpgradeKeepsOriginalAndCompletesAtCutoff(t *testing.T) {
	f := newWorkflowFixture(t, false)
	folder := filepath.Join(f.cfg.Drives[1].MovieRemoteRoot, "Example Movie (2024)")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(folder, "Example.Movie.2024.1080p.BluRay.x264.mkv")
	if err := os.WriteFile(original, []byte("original seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, _ := f.s.requests.Load()
	settings.Movie.UpgradeAllowed = true
	if err := f.s.requests.Save(settings); err != nil {
		t.Fatal(err)
	}
	job := createWorkflowJob(t, f, "alice", false)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, _ := f.s.requests.Downloads()
	if len(downloads) != 1 {
		current, _ := f.s.requests.Job(job.ID)
		t.Fatalf("upgrade missing: %+v", current)
	}
	finishWorkflowDownload(t, f)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := f.s.requests.Job(job.ID)
	if current.Status != "complete" {
		t.Fatalf("cutoff not satisfied: %+v", current)
	}
	if content, err := os.ReadFile(original); err != nil || string(content) != "original seed" {
		t.Fatalf("original changed: %s %v", content, err)
	}
}
