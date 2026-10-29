package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cineroute/internal/prowlarr"
	"cineroute/internal/requests"
)

func TestSpanishRequestsPrioritizeTrackersAndNeverFallBackAutomatically(t *testing.T) {
	for _, mode := range []string{"movie", "tv", "missing"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkflowFixture(t, mode == "tv")
			var preferred, ordinary atomic.Int32
			var provider *httptest.Server
			provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/indexer":
					fmt.Fprint(w, `[{"id":7,"name":"Normal","protocol":"torrent","enable":true},{"id":9,"name":"Spanish","protocol":"torrent","enable":true}]`)
				case "/api/v1/indexer/7", "/api/v1/indexer/9":
					json.NewEncoder(w).Encode(map[string]any{"id": int(r.URL.Path[len(r.URL.Path)-1] - '0'), "enable": true, "protocol": "torrent", "fields": []map[string]string{{"name": "baseUrl", "value": provider.URL}, {"name": "apikey", "value": "test-key"}}})
				case "/api/v1/search":
					ids := r.URL.Query()["indexerIds"]
					if len(ids) == 2 && ids[0] != "9" {
						t.Errorf("Spanish tracker not first: %v", ids)
					}
					seeds := 10
					releases := []prowlarr.Release{{Guid: "normal", IndexerID: 7, Title: f.title + ".PreferredGroup", InfoURL: provider.URL + "/torrents/7", Seeders: &seeds, Size: 512, DownloadURL: provider.URL + "/normal"}}
					if len(ids) == 2 {
						releases = append(releases, prowlarr.Release{Guid: "spanish", IndexerID: 9, Title: f.title, InfoURL: provider.URL + "/torrents/9", Seeders: &seeds, Size: 512, DownloadURL: provider.URL + "/spanish"})
					}
					json.NewEncoder(w).Encode(releases)
				case "/api/torrents/filter":
					audio := "Spanish"
					if mode == "missing" {
						audio = "English"
					}
					data := []any{}
					for _, id := range []int{7, 9} {
						data = append(data, map[string]any{"attributes": map[string]string{"details_link": fmt.Sprintf("%s/torrents/%d", provider.URL, id), "media_info": "Audio\nLanguage : " + audio + "\nText\nLanguage : Spanish"}})
					}
					json.NewEncoder(w).Encode(map[string]any{"data": data})
				case "/normal":
					ordinary.Add(1)
					w.Write(f.torrent)
				case "/spanish":
					preferred.Add(1)
					w.Write(f.torrent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			f.s.prowlarr = prowlarr.New(provider.URL, "test", time.Second)
			f.s.engine = requests.NewEngine(f.s.requests, f.s.tmdb, f.s.prowlarr, requestBackend{f.s})
			settings, _ := f.s.requests.Load()
			settings.SpanishTrackers = []requests.Tracker{{ID: 9, Name: "Spanish"}}
			settings.Movie.Keywords = []requests.Keyword{{Pattern: "PreferredGroup", Match: "contains", Mode: "preferred", Score: 100}}
			settings.TV.Keywords = settings.Movie.Keywords
			if err := f.s.requests.Save(settings); err != nil {
				t.Fatal(err)
			}
			body := `{"media_type":"movie","tmdb_id":101,"spanish_audio":true}`
			if mode == "tv" {
				body = `{"media_type":"tv","tmdb_id":202,"seasons":[1],"spanish_audio":true}`
			}
			response := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", body)
			if response.Code != 201 {
				t.Fatalf("create: %s", response.Body)
			}
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			downloads, _ := f.s.requests.Downloads()
			if mode != "missing" {
				if len(downloads) != 1 || downloads[0].IndexerID != 9 || preferred.Load() != 1 || ordinary.Load() != 0 {
					t.Fatalf("preferred tracker lost: %+v", downloads)
				}
			} else {
				jobs, _ := f.s.requests.Jobs("alice")
				if len(downloads) != 0 || ordinary.Load() != 0 || preferred.Load() != 0 || !strings.Contains(jobs[0].Message, "Untick Spanish audio") {
					t.Fatalf("unsafe fallback: %+v %+v", downloads, jobs)
				}
				response = userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", `{"media_type":"movie","tmdb_id":101,"spanish_audio":false}`)
				if response.Code != 201 {
					t.Fatalf("relax: %s", response.Body)
				}
				if err := f.s.engine.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				downloads, _ = f.s.requests.Downloads()
				if len(downloads) != 1 || downloads[0].IndexerID != 7 || ordinary.Load() != 1 || preferred.Load() != 0 {
					t.Fatalf("normal routing not restored: %+v", downloads)
				}
			}
		})
	}
}
