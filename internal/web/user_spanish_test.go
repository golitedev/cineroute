package web

import (
	"context"
	"encoding/json"
	"errors"
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

	"cineroute/internal/prowlarr"
	"cineroute/internal/requests"
	"cineroute/internal/torrentmeta"
)

func TestPublicSpanishPreferencePersistsForMoviesAndTV(t *testing.T) {
	for _, tv := range []bool{false, true} {
		t.Run(fmt.Sprint(tv), func(t *testing.T) {
			f := newWorkflowFixture(t, tv)
			settings, _ := f.s.requests.Load()
			settings.AutoApprove = false
			settings.MovieLimit = 1
			settings.TVLimit = 1
			if err := f.s.requests.Save(settings); err != nil {
				t.Fatal(err)
			}
			kind, id := "movie", 101
			extra := ""
			if tv {
				kind, id = "tv", 202
				extra = `,"seasons":[1]`
			}
			body := fmt.Sprintf(`{"media_type":%q,"tmdb_id":%d,"prefer_spanish_audio":true%s}`, kind, id, extra)
			out := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", body)
			if out.Code != 201 {
				t.Fatalf("create: %d %s", out.Code, out.Body.String())
			}
			var result struct {
				Request publicJob `json:"request"`
			}
			if json.Unmarshal(out.Body.Bytes(), &result) != nil || !result.Request.PreferSpanishAudio || result.Request.Status != "pending" {
				t.Fatal("preference missing or approval bypassed")
			}
			body = strings.Replace(body, `"prefer_spanish_audio":true`, `"prefer_spanish_audio":false`, 1)
			if out = userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", body); out.Code != 201 {
				t.Fatalf("update exceeded quota: %d %s", out.Code, out.Body.String())
			}
			job, _ := f.s.requests.Job(result.Request.ID)
			if job.PreferSpanishAudio || job.Status != "pending" {
				t.Fatal("uncheck failed or request approved")
			}
			// Older clients omit the field when changing seasons; that must preserve it.
			yes := true
			if _, err := f.s.requests.Create(job, settings, false, &yes); err != nil {
				t.Fatal(err)
			}
			omitted := strings.Replace(body, `,"prefer_spanish_audio":false`, "", 1)
			if out = userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", omitted); out.Code != 201 {
				t.Fatal("omitted preference update failed")
			}
			own := userWorkflowCall(t, f.s, "alice", "GET", "/api/user/requests", "")
			if !strings.Contains(own.Body.String(), `"prefer_spanish_audio":true`) {
				t.Fatal("own requests lost preference")
			}
			other := userWorkflowCall(t, f.s, "bob", "GET", "/api/user/requests", "")
			if strings.Contains(other.Body.String(), result.Request.ID) {
				t.Fatal("another user saw the request")
			}
			queue := userWorkflowCall(t, f.s, "admin", "GET", "/api/requests/queue", "")
			history := readHistory(t, f.s, "")
			if !strings.Contains(queue.Body.String(), `"prefer_spanish_audio":true`) || !history.Entries[0].Request.PreferSpanishAudio {
				t.Fatal("admin queue/history lack user preference")
			}
			for _, invalid := range []string{`"prefer_spanish_audio":"yes"`, `"prefer_spanish_audio":1`, `"prefer_spanish_audio":{}`, `"prefer_spanish_audio":true,"trackers":[{"id":17}]`, `"prefer_spanish_audio":true,"destination":"main"`, `"prefer_spanish_audio":true,"priority":0`} {
				bad := fmt.Sprintf(`{"media_type":%q,"tmdb_id":%d,%s%s}`, kind, id, invalid, extra)
				if response := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", bad); response.Code != 400 {
					t.Errorf("invalid/privileged option accepted: %s HTTP %d", invalid, response.Code)
				}
			}
		})
	}
}

func TestSpanishUserPreferencePromotesOnlySelectedLatTeamAndConfirmedAudio(t *testing.T) {
	cases := []struct {
		name                                                 string
		checked, selected, enabled, otherSpanish, latSpanish bool
		wantLat                                              bool
	}{
		{"ordinary priorities", false, true, true, true, true, false},
		{"promote selected Lat-Team", true, true, true, true, true, true},
		{"do not enable unchecked Lat-Team", true, false, true, true, true, false},
		{"do not enable disabled Lat-Team", true, true, false, true, true, false},
		{"confirmed Spanish beats unknown Lat-Team", true, true, true, true, false, false},
		{"Lat-Team fallback when languages unknown", true, true, true, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkflowFixture(t, false)
			var chosen atomic.Int32
			var provider *httptest.Server
			provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/indexer":
					json.NewEncoder(w).Encode([]prowlarr.Indexer{{ID: 7, Name: "Other tracker", DefinitionName: "aither-api", Enable: true, Protocol: "torrent"}, {ID: 17, Name: "My renamed tracker", DefinitionName: "lat-team-api", Enable: tc.enabled, Protocol: "torrent"}})
				case "/api/v1/indexer/7", "/api/v1/indexer/17":
					id := 7
					if strings.HasSuffix(r.URL.Path, "17") {
						id = 17
					}
					json.NewEncoder(w).Encode(map[string]any{"id": id, "enable": true, "protocol": "torrent", "fields": []map[string]string{{"name": "baseUrl", "value": provider.URL}, {"name": "apikey", "value": fmt.Sprint(id)}}})
				case "/api/v1/search":
					ids := r.URL.Query()["indexerIds"]
					if slices.Contains(ids, "17") != (tc.selected && tc.enabled) {
						t.Errorf("search escaped selected/enabled policy: %v", ids)
					}
					seeders := 10
					items := []prowlarr.Release{{Guid: "other", IndexerID: 7, Indexer: "Lat-Team (forged release name)", Title: f.title, InfoURL: provider.URL + "/torrents/7", Size: 512, Seeders: &seeders, DownloadURL: provider.URL + "/download/7"}}
					if tc.selected && tc.enabled {
						items = append(items, prowlarr.Release{Guid: "lat", IndexerID: 17, Title: f.title, InfoURL: provider.URL + "/torrents/17", Size: 512, Seeders: &seeders, DownloadURL: provider.URL + "/download/17"})
					}
					json.NewEncoder(w).Encode(items)
				case "/api/torrents/filter":
					id := 7
					spanish := tc.otherSpanish
					if r.Header.Get("Authorization") == "Bearer 17" {
						id = 17
						spanish = tc.latSpanish
					}
					report := ""
					if spanish {
						report = "Audio\nLanguage : Spanish"
					}
					json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"attributes": map[string]string{"details_link": fmt.Sprintf("%s/torrents/%d", provider.URL, id), "media_info": report}}}})
				case "/download/7":
					chosen.Store(7)
					w.Write(f.torrent)
				case "/download/17":
					chosen.Store(17)
					w.Write(f.torrent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			f.s.prowlarr = prowlarr.New(provider.URL, "private-prowlarr-key", time.Second)
			f.s.engine = requests.NewEngine(f.s.requests, f.s.tmdb, f.s.prowlarr, requestBackend{f.s})
			settings, _ := f.s.requests.Load()
			settings.Trackers = []requests.Tracker{{ID: 7, Name: "Other tracker", Priority: 1}}
			if tc.selected {
				settings.Trackers = append(settings.Trackers, requests.Tracker{ID: 17, Name: "Renamed", Priority: 99})
			}
			if err := f.s.requests.Save(settings); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(f.cfg.Drives[1].MovieRoot, "Example Movie (2024)")
			if err := os.Mkdir(main, 0755); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"media_type":"movie","tmdb_id":101,"prefer_spanish_audio":%v}`, tc.checked)
			out := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", body)
			if out.Code != 201 {
				t.Fatal(out.Body.String())
			}
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := int32(7)
			if tc.wantLat {
				want = 17
			}
			if chosen.Load() != want {
				t.Fatalf("picked %d want %d", chosen.Load(), want)
			}
			downloads, _ := f.s.requests.Downloads()
			if len(downloads) != 1 || downloads[0].DriveID != "disk2" || downloads[0].Target != "remote" || downloads[0].PreferSpanishAudio != tc.checked {
				t.Fatalf("receipt/placement wrong: %+v", downloads)
			}
			stored, _ := f.s.requests.Load()
			if stored.Trackers[0].Priority != 1 || (tc.selected && stored.Trackers[1].Priority != 99) || len(stored.Movie.PreferredAudioLanguages) != 0 {
				t.Fatal("request changed admin priorities/languages")
			}
			// A second user's unchecked preference remains independent while sharing
			// the active title download instead of starting a duplicate.
			createWorkflowJob(t, f, "bob", false)
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			downloads, _ = f.s.requests.Downloads()
			bobJobs, _ := f.s.requests.Jobs("bob")
			if len(downloads) != 1 || len(bobJobs) != 1 || bobJobs[0].PreferSpanishAudio {
				t.Fatal("shared download duplicated or preference crossed accounts")
			}
		})
	}
}
func TestChangedSpanishPreferenceRejectsStaleSubmission(t *testing.T) {
	f := newWorkflowFixture(t, false)
	job := createWorkflowJob(t, f, "alice", false)
	yes := true
	settings, _ := f.s.requests.Load()
	if _, err := f.s.requests.Create(job, settings, false, &yes); err != nil {
		t.Fatal(err)
	}
	meta, err := torrentmeta.Parse(f.torrent)
	if err != nil {
		t.Fatal(err)
	}
	_, err = requestBackend{f.s}.Submit(context.Background(), job, requests.Candidate{}, meta, f.torrent, nil, func(requests.Download) error { t.Fatal("stale preference wrote a download intent"); return nil })
	if !errors.Is(err, requests.ErrPreferencesChanged) {
		t.Fatalf("stale preference submission not rejected: %v", err)
	}
	downloads, _ := f.s.requests.Downloads()
	if len(downloads) != 0 {
		t.Fatal("stale submission persisted a download")
	}
}
