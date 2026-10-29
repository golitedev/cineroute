package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"cineroute/internal/prowlarr"
	"cineroute/internal/requests"
)

func TestAlternativeTitleSelectionHonorsConfiguredTrackerPriority(t *testing.T) {
	for _, preferLat := range []bool{false, true} {
		t.Run(fmt.Sprintf("preferLat=%v", preferLat), func(t *testing.T) {
			f := newWorkflowFixture(t, true)
			var aitherHits, latHits atomic.Int32
			aitherTorrent := buildFixtureTorrent("Example.Show.AKA.Programa.Ejemplo.S01.1080p.WEB-DL.x264", 0, map[string]int64{"Programa.Ejemplo.S01E01.mkv": 512, "Programa.Ejemplo.S01E02.mkv": 512})
			var provider *httptest.Server
			provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/tv/202":
					fmt.Fprint(w, `{"id":202,"name":"Example Show","original_name":"Programa Ejemplo","first_air_date":"2024-01-01","original_language":"en","seasons":[{"season_number":1,"episode_count":2,"air_date":"2024-01-01"}]}`)
				case "/tv/202/season/1":
					fmt.Fprint(w, `{"episodes":[{"season_number":1,"episode_number":1,"air_date":"2024-01-01"},{"season_number":1,"episode_number":2,"air_date":"2024-01-08"}]}`)
				case "/api/v1/indexer":
					fmt.Fprint(w, `[{"id":1,"name":"Aither (API)","definitionName":"aither-api","protocol":"torrent","enable":true},{"id":17,"name":"Lat-Team (API)","definitionName":"lat-team-api","protocol":"torrent","enable":true}]`)
				case "/api/v1/search":
					ids := r.URL.Query()["indexerIds"]
					if len(ids) != 2 || !slices.Contains(ids, "1") || !slices.Contains(ids, "17") {
						t.Errorf("unexpected tracker selection: %v", ids)
					}
					many, few := 100, 3
					json.NewEncoder(w).Encode([]prowlarr.Release{
						{Guid: "lat", IndexerID: 17, Indexer: "Lat-Team (API)", Title: "Programa Ejemplo 2024 S01 1080p WEB-DL x264", TmdbID: 202, Seeders: &many, Size: 1024, DownloadURL: provider.URL + "/lat"},
						{Guid: "aither", IndexerID: 1, Indexer: "Aither (API)", Title: "Example Show AKA Programa Ejemplo S01 SPANISH 1080p WEB-DL x264", TmdbID: 202, Seeders: &few, Size: 1024, DownloadURL: provider.URL + "/aither"},
					})
				case "/aither":
					aitherHits.Add(1)
					w.Write(aitherTorrent)
				case "/lat":
					latHits.Add(1)
					w.Write(f.torrent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			f.s.tmdb.SetBaseURL(provider.URL)
			f.s.prowlarr = prowlarr.New(provider.URL, "private-indexer-key", time.Second)
			f.s.engine = requests.NewEngine(f.s.requests, f.s.tmdb, f.s.prowlarr, requestBackend{f.s})
			settings, err := f.s.requests.Load()
			if err != nil {
				t.Fatal(err)
			}
			settings.Trackers = []requests.Tracker{{ID: 17, Name: "Lat-Team (API)", Priority: 22}, {ID: 1, Name: "Aither (API)", Priority: 20}}
			if preferLat {
				settings.Trackers[0].Priority = 10
			}
			if err := f.s.requests.Save(settings); err != nil {
				t.Fatal(err)
			}
			job := createWorkflowJob(t, f, "alice", true)
			seedRemovedRequestOptions(t, f.s, job.ID)
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			downloads, err := f.s.requests.Downloads()
			if err != nil || len(downloads) != 1 {
				current, _ := f.s.requests.Job(job.ID)
				t.Fatalf("request did not download: %+v; %v", current, err)
			}
			want, aitherWant, latWant := 1, int32(1), int32(0)
			if preferLat {
				want, aitherWant, latWant = 17, 0, 1
			}
			if downloads[0].IndexerID != want || len(downloads[0].Episodes) != 2 || aitherHits.Load() != aitherWant || latHits.Load() != latWant {
				t.Fatalf("wrong tracker/contents selected: %+v; downloads Aither=%d Lat-Team=%d", downloads[0], aitherHits.Load(), latHits.Load())
			}
		})
	}
}
