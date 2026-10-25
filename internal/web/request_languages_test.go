package web

import (
	"cineroute/internal/prowlarr"
	"cineroute/internal/requests"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdminLanguagePrioritiesRankReportsWithoutChangingRoutingOrKeywordMinimum(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			f := newWorkflowFixture(t, false)
			var nativeHits, preferredHits, ordinaryHits atomic.Int32
			var provider *httptest.Server
			provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/indexer":
					fmt.Fprint(w, `[{"id":7,"name":"UNIT3D","protocol":"torrent","enable":true}]`)
				case "/api/v1/indexer/7":
					json.NewEncoder(w).Encode(map[string]any{"id": 7, "enable": true, "protocol": "torrent", "fields": []map[string]string{{"name": "baseUrl", "value": provider.URL}, {"name": "apikey", "value": "PRIVATE_TRACKER_KEY"}}})
				case "/api/v1/search":
					seeders := 100
					json.NewEncoder(w).Encode([]prowlarr.Release{{Guid: "ordinary", IndexerID: 7, InfoURL: provider.URL + "/torrents/1", Title: f.title + ".PreferredGroup", Seeders: &seeders, Size: 512, DownloadURL: provider.URL + "/ordinary"}, {Guid: "spanish", IndexerID: 7, InfoURL: provider.URL + "/torrents/2", Title: f.title, Seeders: &seeders, Size: 512, DownloadURL: provider.URL + "/preferred"}})
				case "/api/torrents/filter":
					nativeHits.Add(1)
					if r.Header.Get("Authorization") != "Bearer PRIVATE_TRACKER_KEY" || r.Header.Get("X-Api-Key") != "" {
						t.Error("native lookup authentication wrong")
					}
					json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"attributes": map[string]string{"details_link": provider.URL + "/torrents/1", "media_info": "Audio\nLanguage : English\nText\nLanguage : Swedish\nForced : Yes"}}, map[string]any{"attributes": map[string]string{"details_link": provider.URL + "/torrents/2", "media_info": "Audio\nLanguage : Spanish\nText\nLanguage : Swedish\nForced : No"}}}})
				case "/ordinary":
					ordinaryHits.Add(1)
					w.Write(f.torrent)
				case "/preferred":
					preferredHits.Add(1)
					w.Write(f.torrent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			f.s.prowlarr = prowlarr.New(provider.URL, "PRIVATE_PROWLARR_KEY", time.Second)
			f.s.engine = requests.NewEngine(f.s.requests, f.s.tmdb, f.s.prowlarr, requestBackend{f.s})
			settings, _ := f.s.requests.Load()
			settings.Movie.Keywords = []requests.Keyword{{Pattern: "PreferredGroup", Match: "contains", Mode: "preferred", Score: 100}}
			if enabled {
				settings.Movie.PreferredAudioLanguages = []string{"es"}
				settings.Movie.PreferredSubtitleLanguages = []string{"sv"}
			}
			if err := f.s.requests.Save(settings); err != nil {
				t.Fatal(err)
			}
			// Existing main copy anchors the request's remote placement on disk2.
			main := filepath.Join(f.cfg.Drives[1].MovieRoot, "Example Movie (2024)")
			if err := os.Mkdir(main, 0755); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(main, "old.mkv")
			if err := os.WriteFile(sentinel, []byte("original main copy"), 0644); err != nil {
				t.Fatal(err)
			}
			job := createWorkflowJob(t, f, "alice", false)
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			downloads, err := f.s.requests.Downloads()
			if err != nil || len(downloads) != 1 {
				t.Fatalf("download not started: %+v %v", downloads, err)
			}
			d := downloads[0]
			if d.DriveID != "disk2" || d.Target != "remote" || d.SavePath != filepath.Join(f.cfg.Drives[1].MovieRemoteRoot, "Example Movie (2024)") {
				t.Fatalf("routing changed: %+v", d)
			}
			if enabled {
				if nativeHits.Load() != 1 || preferredHits.Load() != 1 || ordinaryHits.Load() != 0 || d.LanguageScore != 2 || !slices.Contains(d.Languages.Audio, "es") || !slices.Contains(d.Languages.Subtitles, "sv") {
					t.Fatalf("language ranking did not beat keyword preference: %+v hits %d/%d/%d", d, nativeHits.Load(), preferredHits.Load(), ordinaryHits.Load())
				}
			} else if nativeHits.Load() != 0 || ordinaryHits.Load() != 1 || preferredHits.Load() != 0 {
				t.Fatal("language lookups changed default behavior")
			}
			if data, _ := os.ReadFile(sentinel); string(data) != "original main copy" {
				t.Fatal("existing main copy modified")
			}
			if enabled {
				settings.Movie.AudioLanguages = []string{"es"}
				settings.Movie.UpgradeAllowed = true
				if err := f.s.requests.Save(settings); err != nil {
					t.Fatal(err)
				}
				finishWorkflowDownload(t, f)
				if err := f.s.engine.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				current, _ := f.s.requests.Job(job.ID)
				if current.Status != "complete" || preferredHits.Load() != 1 {
					t.Fatalf("stored language evidence did not satisfy the completed copy: %+v", current)
				}
			}
			history := readHistory(t, f.s, "")
			if enabled && (history.Entries[0].Downloads[0].LanguageScore != 2 || history.Entries[0].Downloads[0].Languages.Status != "reported") {
				t.Fatal("history lacks reported languages/priority points")
			}
			public := userWorkflowCall(t, f.s, "alice", "GET", "/api/user/requests", "")
			for _, secret := range []string{"PRIVATE_TRACKER_KEY", "PRIVATE_PROWLARR_KEY", "preferred_audio_languages", "languages", "language_score"} {
				if strings.Contains(public.Body.String(), secret) {
					t.Errorf("user response exposed admin metadata %q", secret)
				}
			}
		})
	}
}
func TestMissingTrackerLanguageMetadataIsANeutralPreference(t *testing.T) {
	f := newWorkflowFixture(t, false)
	settings, _ := f.s.requests.Load()
	settings.Movie.PreferredSubtitleLanguages = []string{"sv"}
	if err := f.s.requests.Save(settings); err != nil {
		t.Fatal(err)
	}
	createWorkflowJob(t, f, "alice", false)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	downloads, _ := f.s.requests.Downloads()
	if len(downloads) != 1 || downloads[0].LanguageScore != 0 {
		t.Fatalf("missing metadata blocked a permitted release: %+v", downloads)
	}
}
func TestLanguagePreferencesAreOnlyAdminPolicy(t *testing.T) {
	f := newWorkflowFixture(t, false)
	response := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", `{"media_type":"movie","tmdb_id":101,"preferred_audio_languages":["es"]}`)
	if response.Code != 400 {
		t.Fatalf("user could submit language policy: %d", response.Code)
	}
	response = userWorkflowCall(t, f.s, "alice", "PUT", "/api/requests/settings", `{}`)
	if response.Code < 400 {
		t.Fatal("public user could save admin policy")
	}
	response = userWorkflowCall(t, f.s, "admin", "GET", "/api/requests/settings", "")
	var data struct {
		Languages []requests.Option `json:"languages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil || !slices.ContainsFunc(data.Languages, func(o requests.Option) bool { return o.ID == "sv" }) {
		t.Fatal("admin language choices missing")
	}
	for _, asset := range []string{"/", "/catalog.js", "/portal.js"} {
		out := httptest.NewRecorder()
		f.s.PublicHandler().ServeHTTP(out, httptest.NewRequest("GET", asset, nil))
		for _, text := range []string{"Language priorities", "preferred_audio_languages", "preferred_subtitle_languages", "toggleRequestLanguagePreference"} {
			if strings.Contains(out.Body.String(), text) {
				t.Errorf("admin language UI exposed through %s", asset)
			}
		}
	}
}
