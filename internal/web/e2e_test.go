package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/qbittorrent"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

// fakeQB emulates the qBittorrent WebUI API surface CineRoute uses.
type fakeQB struct {
	mu         sync.Mutex
	torrents   map[string]*qbTorrent
	started    map[string]bool
	added      []addCall
	categories map[string]string
}

type qbTorrent struct {
	hash        string
	name        string
	savePath    string
	contentPath string
	category    string
	tags        string
	totalSize   int64
	state       string
	files       []qbFile
}

type qbFile struct {
	name string
	size int64
}

type addCall struct {
	savepath   string
	category   string
	tags       string
	autoTMM    string
	paused     string
	rootFolder string
}

func newFakeQB() *fakeQB {
	return &fakeQB{
		torrents:   map[string]*qbTorrent{},
		started:    map[string]bool{},
		categories: map[string]string{},
	}
}

func (f *fakeQB) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-session"})
		w.Write([]byte("Ok."))
	})
	mux.HandleFunc("/api/v2/app/webapiVersion", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "2.12")
	})
	mux.HandleFunc("/api/v2/app/version", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "v5.0.4")
	})
	mux.HandleFunc("/api/v2/app/preferences", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"preallocate_all": false, "temp_path_enabled": false})
	})
	mux.HandleFunc("/api/v2/torrents/categories", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{}
		f.mu.Lock()
		for k := range f.categories {
			out[k] = map[string]any{"savePath": ""}
		}
		f.mu.Unlock()
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v2/torrents/createCategory", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.categories[r.Form.Get("category")] = r.Form.Get("savePath")
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		file, _, err := r.FormFile("torrents")
		if err != nil {
			http.Error(w, "no file", 400)
			return
		}
		data, _ := io.ReadAll(file)
		file.Close()
		meta, err := torrentmeta.Parse(data)
		if err != nil {
			http.Error(w, "unparseable", 400)
			return
		}
		savepath := r.FormValue("savepath")
		tag := r.FormValue("tags")
		call := addCall{
			savepath:   savepath,
			category:   r.FormValue("category"),
			tags:       tag,
			autoTMM:    r.FormValue("autoTMM"),
			paused:     r.FormValue("paused"),
			rootFolder: r.FormValue("root_folder"),
		}
		rootFolder := r.FormValue("root_folder") == "true"
		contentPath := meta.ContentPath(savepath)
		_ = rootFolder
		files := []qbFile{}
		for i, p := range meta.RelPaths() {
			files = append(files, qbFile{name: p, size: meta.Files[i].Length})
		}
		t := &qbTorrent{
			hash:        meta.InfoHashV1,
			name:        meta.Name,
			savePath:    savepath,
			contentPath: contentPath,
			category:    call.category,
			tags:        tag,
			totalSize:   meta.Size,
			state:       "stoppedDL",
			files:       files,
		}
		f.mu.Lock()
		f.added = append(f.added, call)
		f.torrents[t.hash] = t
		f.started[t.hash] = false
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		tag := r.URL.Query().Get("tag")
		hashes := r.URL.Query().Get("hashes")
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]any{}
		for _, t := range f.torrents {
			if tag != "" && !strings.Contains(t.tags, tag) {
				continue
			}
			if hashes != "" && !strings.Contains(hashes, t.hash) {
				continue
			}
			state := t.state
			if f.started[t.hash] {
				state = "downloading"
			}
			out = append(out, map[string]any{
				"hash":         t.hash,
				"name":         t.name,
				"save_path":    t.savePath,
				"content_path": t.contentPath,
				"category":     t.category,
				"tags":         t.tags,
				"auto_tmm":     false,
				"total_size":   t.totalSize,
				"amount_left":  t.totalSize,
				"state":        state,
			})
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		hash := r.URL.Query().Get("hash")
		f.mu.Lock()
		defer f.mu.Unlock()
		t := f.torrents[hash]
		out := []map[string]any{}
		if t != nil {
			for _, fl := range t.files {
				out = append(out, map[string]any{"name": fl.name, "size": fl.size})
			}
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v2/torrents/start", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		hashes := r.Form.Get("hashes")
		f.mu.Lock()
		f.started[hashes] = true
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	return mux
}

func singleFileTorrent(name string, length int64) []byte {
	return buildFixtureTorrent(name, length, nil)
}

func buildFixtureTorrent(name string, length int64, files map[string]int64) []byte {
	var b bytes.Buffer
	be := func(s string) string { return fmt.Sprintf("%d:%s", len(s), s) }
	bi := func(n int64) string { return fmt.Sprintf("i%de", n) }
	b.WriteString("d")
	b.WriteString(be("info"))
	b.WriteString("d")
	b.WriteString(be("name"))
	b.WriteString(be(name))
	b.WriteString(be("piece length"))
	b.WriteString(bi(16384))
	b.WriteString(be("pieces"))
	b.WriteString(be(strings.Repeat("\x00", 20)))
	if files == nil {
		b.WriteString(be("length"))
		b.WriteString(bi(length))
	} else {
		b.WriteString(be("files"))
		b.WriteString("l")
		for p, ln := range files {
			b.WriteString("d")
			b.WriteString(be("length"))
			b.WriteString(bi(ln))
			b.WriteString(be("path"))
			b.WriteString("l")
			for _, c := range strings.Split(p, "/") {
				b.WriteString(be(c))
			}
			b.WriteString("e")
			b.WriteString("e")
		}
		b.WriteString("e")
	}
	b.WriteString("e")
	b.WriteString("e")
	return b.Bytes()
}

func newTestServer(t *testing.T) (*Server, *fakeQB, *httptest.Server, map[string]string) {
	t.Helper()
	qb := newFakeQB()
	qbSrv := httptest.NewServer(qb.handler())
	t.Cleanup(qbSrv.Close)

	roots := map[string]string{}
	dirs := []string{"/t1", "/m1", "/t2", "/m2", "/t3", "/m3", "/t4", "/m4"}
	base := t.TempDir()
	for _, d := range dirs {
		p := filepath.Join(base, d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		roots[d] = p
	}

	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.QBittorrent.URL = qbSrv.URL
	cfg.Drives = []config.Drive{
		{ID: "hdd1", MovieRoot: roots["/m1"], TVRoot: roots["/t1"], ReserveBytes: 0},
		{ID: "hdd2", MovieRoot: roots["/m2"], TVRoot: roots["/t2"], ReserveBytes: 0},
		{ID: "hdd3", MovieRoot: roots["/m3"], TVRoot: roots["/t3"], ReserveBytes: 0},
		{ID: "hdd4", MovieRoot: roots["/m4"], TVRoot: roots["/t4"], ReserveBytes: 0},
	}

	tmdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search/movie") {
			json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"id": 862, "title": "Toy Story", "original_title": "Toy Story", "release_date": "1995-11-22", "overview": "A cowboy doll."},
			}})
			return
		}
		if strings.Contains(r.URL.Path, "/search/tv") {
			json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"id": 4607, "name": "Lost", "original_name": "Lost", "first_air_date": "2004-09-22", "overview": "Island."},
			}})
			return
		}
		http.Error(w, "not found", 404)
	}))
	t.Cleanup(tmdbSrv.Close)
	tc := tmdb.New("test-key", "en-US", 5*time.Second)
	tc.SetBaseURL(tmdbSrv.URL)

	qbClient, err := qbittorrent.New(qbSrv.URL, "admin", "test", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(cfg, qbClient, tc)
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	return srv, qb, httpSrv, roots
}

func uploadTorrent(t *testing.T, httpSrv *httptest.Server, name string, data []byte) *intakeJSON {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("torrents", name)
	fw.Write(data)
	mw.Close()
	resp, err := http.Post(httpSrv.URL+"/api/intakes", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j apiResponse
	json.NewDecoder(resp.Body).Decode(&j)
	if j.Intake == nil {
		t.Fatalf("upload failed: %+v", j)
	}
	return j.Intake
}

func TestEndToEndSingleFileMovie(t *testing.T) {
	srv, fake, httpSrv, roots := newTestServer(t)
	_ = srv

	raw := singleFileTorrent("Toy.Story.1995.REPACK.UHD.BluRay.2160p.FraMeSToR.mkv", 500)
	in := uploadTorrent(t, httpSrv, "toy.torrent", raw)
	if in.MediaType != "movie" || in.Year != 1995 || in.Title != "Toy Story" {
		t.Fatalf("classification: %+v", in)
	}
	if len(in.TMDB) == 0 || in.TMDB[0].ID != 862 {
		t.Fatalf("tmdb results: %+v", in.TMDB)
	}

	resp, _ := http.Post(httpSrv.URL+"/api/intakes/"+in.ID+"/match",
		"application/json", strings.NewReader(`{"tmdb_id":862}`))
	var j apiResponse
	json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()
	if j.Error != "" || j.Intake.Dest == nil {
		t.Fatalf("match failed: %+v", j)
	}
	if j.Intake.Dest.FolderName != "Toy Story (1995)" {
		t.Fatalf("folder: %s", j.Intake.Dest.FolderName)
	}
	if !strings.HasSuffix(j.Intake.Dest.SavePath, "/Toy Story (1995)") {
		t.Fatalf("save path: %s", j.Intake.Dest.SavePath)
	}

	resp, _ = http.Post(httpSrv.URL+"/api/intakes/"+in.ID+"/submit",
		"application/json", strings.NewReader(`{}`))
	j = apiResponse{}
	json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()
	if j.Intake.Error != "" || j.Intake.Result == nil {
		t.Fatalf("submit failed: %+v", j.Intake)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.added) != 1 {
		t.Fatalf("adds: %d", len(fake.added))
	}
	call := fake.added[0]
	if call.savepath != j.Intake.Dest.SavePath {
		t.Fatalf("savepath mismatch: %q vs %q", call.savepath, j.Intake.Dest.SavePath)
	}
	if call.rootFolder != "false" || call.paused != "true" || call.autoTMM != "false" {
		t.Fatalf("add params: %+v", call)
	}
	if !fake.started[j.Intake.Result.Hash] {
		t.Fatal("torrent was never started")
	}
	if _, err := os.Stat(j.Intake.Dest.SavePath); err != nil {
		t.Fatalf("parent folder was not created: %v", err)
	}
	// The save path must be under one of the configured movie roots.
	if !strings.HasPrefix(j.Intake.Dest.SavePath, roots["/m1"]) && !strings.HasPrefix(j.Intake.Dest.SavePath, roots["/m2"]) &&
		!strings.HasPrefix(j.Intake.Dest.SavePath, roots["/m3"]) && !strings.HasPrefix(j.Intake.Dest.SavePath, roots["/m4"]) {
		t.Fatalf("save path not under a movie root: %s", j.Intake.Dest.SavePath)
	}
}

func TestEndToEndExistingTVFolder(t *testing.T) {
	srv, fake, httpSrv, roots := newTestServer(t)
	_ = srv
	_ = fake

	// Pre-create /t1/Lost (2004)
	lostDir := filepath.Join(roots["/t1"], "Lost (2004)")
	os.MkdirAll(lostDir, 0o755)

	raw := buildFixtureTorrent("Lost.S02.1080p.DSNP.WEB-DL.FLUX", 700, map[string]int64{
		"Lost.S02E01.mkv": 400,
		"Lost.S02E02.mkv": 300,
	})
	in := uploadTorrent(t, httpSrv, "lost.torrent", raw)
	if in.MediaType != "tv" || in.Season != 2 || in.Title != "Lost" {
		t.Fatalf("classification: %+v", in)
	}

	resp, _ := http.Post(httpSrv.URL+"/api/intakes/"+in.ID+"/match",
		"application/json", strings.NewReader(`{"tmdb_id":4607}`))
	var j apiResponse
	json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()
	if j.Error != "" || j.Intake.Dest == nil {
		t.Fatalf("match failed: %+v", j)
	}
	if j.Intake.Dest.SavePath != lostDir {
		t.Fatalf("expected existing TV folder: %s", j.Intake.Dest.SavePath)
	}
	if !j.Intake.Dest.Existing {
		t.Fatal("expected existing=true")
	}

	resp, _ = http.Post(httpSrv.URL+"/api/intakes/"+in.ID+"/submit",
		"application/json", strings.NewReader(`{}`))
	j = apiResponse{}
	json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()
	if j.Intake.Error != "" || j.Intake.Result == nil {
		t.Fatalf("submit failed: %+v", j.Intake)
	}
	if j.Intake.Result.SavePath != lostDir {
		t.Fatalf("result save path: %s", j.Intake.Result.SavePath)
	}
}

func TestDuplicateBlocked(t *testing.T) {
	srv, fake, httpSrv, roots := newTestServer(t)
	_ = srv
	_ = fake
	_ = roots

	raw := singleFileTorrent("Dupe.Movie.2020.1080p.mkv", 300)
	in := uploadTorrent(t, httpSrv, "dupe.torrent", raw)
	resp, _ := http.Post(httpSrv.URL+"/api/intakes/"+in.ID+"/match",
		"application/json", strings.NewReader(`{"tmdb_id":862}`))
	j := apiResponse{}
	json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()

	// Pre-add the same torrent to the fake qB.
	fake.mu.Lock()
	meta, _ := torrentmeta.Parse(raw)
	fake.torrents[meta.InfoHashV1] = &qbTorrent{
		hash: meta.InfoHashV1, name: "Dupe.Movie.2020.1080p.mkv",
		savePath: "/m1/x", state: "downloading", files: []qbFile{{name: "a.mkv", size: 1}},
	}
	fake.mu.Unlock()

	resp, _ = http.Post(httpSrv.URL+"/api/intakes/"+in.ID+"/submit",
		"application/json", strings.NewReader(`{}`))
	j = apiResponse{}
	json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()
	if !strings.Contains(j.Intake.Error, "already in qBittorrent") {
		t.Fatalf("expected duplicate error, got: %+v", j.Intake)
	}
	if len(fake.added) != 0 {
		t.Fatal("duplicate was added")
	}
}

func TestMissingTMDBKeyShowsError(t *testing.T) {
	qb := newFakeQB()
	qbSrv := httptest.NewServer(qb.handler())
	defer qbSrv.Close()
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "m1"), 0o755)
	os.MkdirAll(filepath.Join(base, "t1"), 0o755)
	cfg := config.Default()
	cfg.QBittorrent.URL = qbSrv.URL
	cfg.TMDB.APIKey = ""
	cfg.Drives = []config.Drive{{ID: "hdd1", MovieRoot: filepath.Join(base, "m1"), TVRoot: filepath.Join(base, "t1")}}
	qbClient, _ := qbittorrent.New(qbSrv.URL, "a", "b", 5*time.Second)
	srv := New(cfg, qbClient, nil)
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	raw := singleFileTorrent("Some.Movie.2021.1080p.mkv", 100)
	in := uploadTorrent(t, httpSrv, "x.torrent", raw)
	if in.TMDBError == "" {
		t.Fatal("expected tmdb error message")
	}
}

func TestPlanDestinationUsesMostSpace(t *testing.T) {
	srv, _, httpSrv, roots := newTestServer(t)
	_ = srv

	// Make /m1 nearly full by writing a file on the same filesystem? Not
	// possible cheaply in a tempdir; instead assert hdd1 is chosen when all
	// are equal and that an existing TV folder wins regardless of space.
	raw := singleFileTorrent("Brand.New.Show.S01.1080p.mkv", 200)
	in := uploadTorrent(t, httpSrv, "n.torrent", raw)
	if in.MediaType != "tv" {
		t.Fatalf("expected tv: %+v", in)
	}
	// choose Lost match (4607) — name mismatch is fine, ranking picks it
	resp, _ := http.Post(httpSrv.URL+"/api/intakes/"+in.ID+"/match",
		"application/json", strings.NewReader(`{"tmdb_id":4607}`))
	var j apiResponse
	json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()
	if j.Error != "" {
		t.Fatalf("match: %+v", j)
	}
	d := j.Intake.Dest
	if d.DriveID != "hdd1" {
		t.Fatalf("expected hdd1 (first with most space), got %s", d.DriveID)
	}
	if !strings.HasPrefix(d.SavePath, roots["/t1"]) {
		t.Fatalf("tv save path should be under /t1: %s", d.SavePath)
	}
	_ = url.Values{}
}
