package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cineroute/internal/requests"
	"cineroute/internal/tmdb"
)

// Existing installations may still store the removed filter. Loading those
// settings must preserve the rest of the policy and expose the whole catalog.
func seedLegacyCatalogPolicy(t *testing.T, s *Server) {
	t.Helper()
	settings, err := s.requests.Load()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	stored["catalog_availability"] = "digital"
	raw, err = json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.cfg.Requests.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE request_settings SET payload=? WHERE id=1", string(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogIncludesAllReleasesAndAvailabilityIsAdvisory(t *testing.T) {
	f := newWorkflowFixture(t, false)
	seedLegacyCatalogPolicy(t, f.s)
	var detailCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/multi":
			fmt.Fprint(w, `{"page":1,"total_pages":2,"results":[{"id":1,"media_type":"movie","title":"Upcoming"},{"id":2,"media_type":"movie","title":"Digital"},{"id":3,"media_type":"movie","title":"The Big Short"},{"id":4,"media_type":"tv","name":"Aired"},{"id":5,"media_type":"tv","name":"Future show"}]}`)
		case "/movie/1":
			detailCalls.Add(1)
			fmt.Fprint(w, `{"id":1,"title":"Upcoming","release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2099-01-01"}]}]}}`)
		case "/movie/2":
			detailCalls.Add(1)
			fmt.Fprint(w, `{"id":2,"title":"Digital","release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2020-01-01"}]}]}}`)
		case "/movie/3":
			detailCalls.Add(1)
			fmt.Fprint(w, `{"id":3,"title":"The Big Short","release_date":"2015-12-11","release_dates":{"results":[{"release_dates":[{"type":5,"release_date":"2016-05-23"}]}]}}`)
		case "/tv/4":
			detailCalls.Add(1)
			fmt.Fprint(w, `{"id":4,"name":"Aired","first_air_date":"2020-01-01","seasons":[{"season_number":1,"episode_count":1,"air_date":"2020-01-01"}]}`)
		case "/tv/5":
			detailCalls.Add(1)
			fmt.Fprint(w, `{"id":5,"name":"Future show","first_air_date":"2099-01-01","seasons":[{"season_number":1,"episode_count":1,"air_date":"2099-01-01"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	f.s.tmdb = tmdb.New("test-key", "en-US", time.Second)
	f.s.tmdb.SetBaseURL(upstream.URL)
	out := userWorkflowCall(t, f.s, "alice", "GET", "/api/catalog/search?q=Example", "")
	var page tmdb.CatalogPage
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &page) != nil || len(page.Results) != 5 || page.TotalPages != 2 {
		t.Fatalf("catalog still filters releases: %d %s", out.Code, out.Body.String())
	}
	if detailCalls.Load() != 0 {
		t.Fatal("search still performs per-title release checks")
	}
	for _, tc := range []struct {
		kind      string
		id        int
		available bool
	}{{"movie", 1, false}, {"movie", 2, true}, {"movie", 3, false}, {"tv", 4, true}, {"tv", 5, false}} {
		t.Run(fmt.Sprintf("%s/%d", tc.kind, tc.id), func(t *testing.T) {
			out := userWorkflowCall(t, f.s, "alice", "GET", fmt.Sprintf("/api/catalog/%s/%d", tc.kind, tc.id), "")
			var details struct {
				Available *bool `json:"digitally_available"`
			}
			if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &details) != nil || details.Available == nil || *details.Available != tc.available {
				t.Fatalf("availability should be advisory metadata: %d %s", out.Code, out.Body.String())
			}
			body := fmt.Sprintf(`{"media_type":%q,"tmdb_id":%d}`, tc.kind, tc.id)
			if tc.kind == "tv" {
				body = fmt.Sprintf(`{"media_type":"tv","tmdb_id":%d,"seasons":[],"follow_future":true}`, tc.id)
			}
			if created := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", body); created.Code != 201 {
				t.Fatalf("availability warning blocked a request: %d %s", created.Code, created.Body.String())
			}
		})
	}
}

func TestRemovedCatalogSettingDoesNotChangePolicyOrExistingRequests(t *testing.T) {
	f := newWorkflowFixture(t, false)
	job := createWorkflowJob(t, f, "alice", false)
	job.Status = "complete"
	job.Progress = 1
	if err := f.s.requests.Update(job); err != nil {
		t.Fatal(err)
	}
	before, err := f.s.requests.Load()
	if err != nil {
		t.Fatal(err)
	}
	seedLegacyCatalogPolicy(t, f.s)
	reloaded, err := requests.NewStore(f.cfg.Requests.StatePath).Load()
	if err != nil {
		t.Fatal(err)
	}
	originalJSON, _ := json.Marshal(before)
	reloadedJSON, _ := json.Marshal(reloaded)
	if string(originalJSON) != string(reloadedJSON) {
		t.Fatal("ignoring the removed setting changed other policy")
	}
	out := userWorkflowCall(t, f.s, "admin", "GET", "/api/requests/settings", "")
	if out.Code != 200 || strings.Contains(out.Body.String(), "catalog_availability") {
		t.Fatalf("removed setting still exposed: %d %s", out.Code, out.Body.String())
	}
	out = userWorkflowCall(t, f.s, "admin", "PUT", "/api/requests/settings", string(reloadedJSON))
	if out.Code != 200 {
		t.Fatalf("saving migrated policy: %d %s", out.Code, out.Body.String())
	}
	current, _ := f.s.requests.Job(job.ID)
	if current.Status != "complete" || current.PreferSpanishAudio {
		t.Fatal("removing the filter requeued an existing request")
	}
}
