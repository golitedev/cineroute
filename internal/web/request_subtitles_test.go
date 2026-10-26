package web

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"cineroute/internal/requests"
	"cineroute/internal/subtitles"
)

func TestRequestedMovieSubtitleHandoffRequiresEnabledOptionAndCompletedFiles(t *testing.T) {
	for _, mode := range []string{"off", "remote", "main", "tv", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkflowFixture(t, mode == "tv")
			cfg := subtitleRuntimeConfig(f.cfg)
			cfg.Enabled = mode != "disabled"
			cfg.StatePath = filepath.Join(t.TempDir(), "subtitles.db")
			cfg.WorkDir = t.TempDir()
			f.s.subtitles = subtitles.NewManager(cfg, f.s.lib)
			settings, err := f.s.requests.Load()
			if err != nil {
				t.Fatal(err)
			}
			settings.MovieAutoSubtitles = mode != "off"
			if mode == "main" {
				settings.Movie.Destination = "main"
			}
			if err := f.s.requests.Save(settings); err != nil {
				t.Fatal(err)
			}
			job := createWorkflowJob(t, f, "alice", mode == "tv")
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(f.s.subtitles.View("").Items) != 0 {
				t.Fatal("subtitle processing was queued before download completion")
			}
			finishWorkflowDownload(t, f)
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			current, _ := f.s.requests.Job(job.ID)
			if current.Status != "complete" {
				t.Fatalf("subtitle work prevented request completion: %+v", current)
			}
			downloads, err := f.s.requests.Downloads()
			if err != nil || len(downloads) != 1 {
				t.Fatalf("unexpected download receipts: %+v %v", downloads, err)
			}
			want := mode == "remote" || mode == "main"
			wantPolicy := mode != "off" && mode != "tv"
			if downloads[0].AutoSubtitles != wantPolicy || downloads[0].SubtitlesQueued != want {
				t.Fatalf("incorrect completion policy: %+v", downloads[0])
			}
			if items := f.s.subtitles.View("").Items; want && (len(items) != 1 || !items[0].AutomaticPending || items[0].LibraryTarget != mode) || !want && len(items) != 0 {
				t.Fatalf("incorrect subtitle handoff: %+v", items)
			}
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if want && len(f.s.subtitles.View("").Items) != 1 {
				t.Fatal("repeated completion queued duplicate subtitle work")
			}
		})
	}
}

func TestAutomaticMovieSubtitleOptionIsAdminOnlyAndDoesNotRequeueDownloads(t *testing.T) {
	f := newWorkflowFixture(t, false)
	job := createWorkflowJob(t, f, "alice", false)
	job.Status = "complete"
	job.NextSearch = job.CreatedAt.AddDate(0, 0, 1)
	if err := f.s.requests.Update(job); err != nil {
		t.Fatal(err)
	}
	settings, _ := f.s.requests.Load()
	settings.MovieAutoSubtitles = true
	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	response := userWorkflowCall(t, f.s, "admin", "PUT", "/api/requests/settings", string(body))
	if response.Code != 200 {
		t.Fatalf("admin could not save option: %d %s", response.Code, response.Body.String())
	}
	stored, err := requests.NewStore(f.cfg.Requests.StatePath).Load()
	if err != nil || !stored.MovieAutoSubtitles {
		t.Fatal("automatic subtitle option did not persist")
	}
	current, _ := f.s.requests.Job(job.ID)
	if current.Status != "complete" || !current.NextSearch.Equal(job.NextSearch) {
		t.Fatal("a subtitle-only setting change requeued tracker downloads")
	}
	response = userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", `{"media_type":"movie","tmdb_id":101,"movie_auto_subtitles":true}`)
	if response.Code != 400 {
		t.Fatalf("user could override the admin subtitle policy: %d", response.Code)
	}
}
