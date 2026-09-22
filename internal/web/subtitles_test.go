package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/subtitles"
)

// newSubtitleTestServer builds a server whose subtitle subsystem uses temp state
// and stub tool binaries, so no network or media probing happens.
func newSubtitleTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "movies-remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatalf("create remote root: %v", err)
	}
	cfg := config.Default()
	cfg.AuthPassword = ""
	cfg.Subtitles.Enabled = true
	cfg.Subtitles.StatePath = filepath.Join(base, "subtitles.db")
	cfg.Subtitles.WorkDir = filepath.Join(base, "work")
	cfg.Subtitles.MinVideoBytes = 1
	// `false` exits non-zero immediately, so ffprobe "fails fast" instead of
	// reading a fake media file.
	cfg.Subtitles.FFmpegPath = "false"
	cfg.Subtitles.FFprobePath = "false"
	cfg.Subtitles.AlassPath = "false"
	cfg.Drives = []config.Drive{{
		ID:              "hdd1",
		MovieRoot:       filepath.Join(base, "movies"),
		MovieRemoteRoot: remote,
		TVRoot:          filepath.Join(base, "tv"),
	}}
	srv := New(cfg, nil, nil)
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	return srv, httpSrv, remote
}

func subtitleView(t *testing.T, httpSrv *httptest.Server) subtitles.View {
	t.Helper()
	response, err := http.Get(httpSrv.URL + "/api/subtitles")
	if err != nil {
		t.Fatalf("GET /api/subtitles: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/subtitles status = %d", response.StatusCode)
	}
	var view subtitles.View
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	return view
}

func TestSubtitleEndpointsListAndScan(t *testing.T) {
	_, httpSrv, remote := newSubtitleTestServer(t)

	empty := subtitleView(t, httpSrv)
	if !empty.Enabled || empty.Stats.Total != 0 {
		t.Fatalf("initial view = %+v", empty)
	}
	if len(empty.TargetLanguages) != 3 || empty.TargetLanguages[0] != "sv" {
		t.Errorf("target languages = %v, want sv, es, en", empty.TargetLanguages)
	}

	folder := filepath.Join(remote, "Movie (2019)")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "Movie.2019.1080p.WEB-DL.mkv"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("write movie: %v", err)
	}

	response, err := http.Post(httpSrv.URL+"/api/subtitles/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("POST scan: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("scan status = %d", response.StatusCode)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := subtitleView(t, httpSrv)
		if !view.Batch.Running {
			if view.Stats.Total != 1 {
				t.Fatalf("stats after scan = %+v, want one item", view.Stats)
			}
			if view.Stats.NoExternalSubtitle != 1 || view.Stats.WithExternalSubtitle != 0 {
				t.Fatalf("external-SRT stats = %+v, want one movie without an external SRT", view.Stats)
			}
			item := view.Items[0]
			if item.Title != "Movie" || item.Year != 2019 {
				t.Fatalf("item = %+v", item)
			}
			if item.HasExternalSubtitle || item.HasAllTargets() {
				t.Fatalf("item flags = %+v", item)
			}
			if item.DriveID != "hdd1" {
				t.Errorf("drive = %q", item.DriveID)
			}

			// Skip and reset round-trip.
			skip, err := http.Post(httpSrv.URL+"/api/subtitles/"+item.ID+"/skip", "application/json", nil)
			if err != nil {
				t.Fatalf("skip: %v", err)
			}
			skip.Body.Close()
			if skip.StatusCode != http.StatusOK {
				t.Fatalf("skip status = %d", skip.StatusCode)
			}
			if got := subtitleView(t, httpSrv).Items[0].Status; got != subtitles.StatusSkipped {
				t.Errorf("status after skip = %q", got)
			}
			reset, err := http.Post(httpSrv.URL+"/api/subtitles/"+item.ID+"/reset", "application/json", nil)
			if err != nil {
				t.Fatalf("reset: %v", err)
			}
			reset.Body.Close()
			if got := subtitleView(t, httpSrv).Items[0].Status; got != subtitles.StatusPending {
				t.Errorf("status after reset = %q", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("scan did not finish")
}

func TestSubtitleSettingsEndpoint(t *testing.T) {
	srv, httpSrv, _ := newSubtitleTestServer(t)

	body := map[string]any{"run_batch_size": 4, "max_candidates": 9, "request_interval_ms": 0}
	encoded, _ := json.Marshal(body)
	request, _ := http.NewRequest(http.MethodPatch, httpSrv.URL+"/api/subtitles/settings", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PATCH settings: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", response.StatusCode)
	}
	settings := srv.subtitles.Settings()
	if settings.RunBatchSize != 4 || settings.MaxCandidates != 9 {
		t.Fatalf("settings = %+v", settings)
	}

	invalid, _ := json.Marshal(map[string]any{"max_candidates": 99})
	badRequest, _ := http.NewRequest(http.MethodPatch, httpSrv.URL+"/api/subtitles/settings", bytes.NewReader(invalid))
	badRequest.Header.Set("Content-Type", "application/json")
	badResponse, err := http.DefaultClient.Do(badRequest)
	if err != nil {
		t.Fatalf("PATCH invalid settings: %v", err)
	}
	badResponse.Body.Close()
	if badResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid settings status = %d, want 400", badResponse.StatusCode)
	}
}

func TestSubtitleItemActionsRequireKnownItem(t *testing.T) {
	_, httpSrv, _ := newSubtitleTestServer(t)
	response, err := http.Post(httpSrv.URL+"/api/subtitles/does-not-exist/run", "application/json", nil)
	if err != nil {
		t.Fatalf("POST run: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("unknown item status = %d, want 409", response.StatusCode)
	}
}
