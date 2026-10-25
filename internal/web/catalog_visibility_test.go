package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cineroute/internal/requests"
	"cineroute/internal/tmdb"
)

func TestDigitalCatalogVisibilitySearchDetailsAndRequestBoundaries(t *testing.T) {
	f := newWorkflowFixture(t, false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/multi":
			fmt.Fprint(w, `{"page":1,"total_pages":2,"results":[{"id":1,"media_type":"movie","title":"Upcoming"},{"id":2,"media_type":"movie","title":"Digital"},{"id":3,"media_type":"movie","title":"Physical only"},{"id":4,"media_type":"tv","name":"Aired"},{"id":5,"media_type":"tv","name":"Future show"}]}`)
		case "/movie/1":
			fmt.Fprint(w, `{"id":1,"title":"Upcoming","status":"Released","release_dates":{"results":[{"release_dates":[{"type":3,"release_date":"2020-01-01"},{"type":4,"release_date":"2099-01-01"}]}]}}`)
		case "/movie/2":
			fmt.Fprint(w, `{"id":2,"title":"Digital","release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2020-01-01"}]}]}}`)
		case "/movie/3":
			fmt.Fprint(w, `{"id":3,"title":"Physical only","release_dates":{"results":[{"release_dates":[{"type":5,"release_date":"2020-01-01"}]}]}}`)
		case "/tv/4":
			fmt.Fprint(w, `{"id":4,"name":"Aired","first_air_date":"2020-01-01","seasons":[{"season_number":1,"episode_count":1,"air_date":"2020-01-01"}]}`)
		case "/tv/5":
			fmt.Fprint(w, `{"id":5,"name":"Future show","first_air_date":"2099-01-01"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	f.s.tmdb = tmdb.New("test-key", "en-US", time.Second)
	f.s.tmdb.SetBaseURL(upstream.URL)
	settings, _ := f.s.requests.Load()
	settings.CatalogAvailability = "digital"
	if err := f.s.requests.Save(settings); err != nil {
		t.Fatal(err)
	}
	out := userWorkflowCall(t, f.s, "alice", "GET", "/api/catalog/search?q=Example", "")
	var page tmdb.CatalogPage
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &page) != nil || len(page.Results) != 2 || page.Results[0].ID != 2 || page.Results[1].ID != 4 || page.TotalPages != 2 || !page.AvailabilityFiltered {
		t.Fatalf("search leaked unreleased titles: %d %s", out.Code, out.Body.String())
	}
	for _, tc := range []struct {
		kind string
		id   int
	}{{"movie", 1}, {"movie", 3}, {"tv", 5}} {
		path := fmt.Sprintf("/api/catalog/%s/%d", tc.kind, tc.id)
		if response := userWorkflowCall(t, f.s, "alice", "GET", path, ""); response.Code != 404 {
			t.Errorf("unreleased details allowed: %s HTTP %d", path, response.Code)
		}
		body := fmt.Sprintf(`{"media_type":%q,"tmdb_id":%d,"prefer_spanish_audio":true}`, tc.kind, tc.id)
		if response := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", body); response.Code != 400 {
			t.Errorf("unreleased request allowed: %d %s", response.Code, response.Body.String())
		}
	}
	response := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", `{"media_type":"movie","tmdb_id":2,"prefer_spanish_audio":true}`)
	if response.Code != 201 {
		t.Fatalf("digital title blocked: %d %s", response.Code, response.Body.String())
	}
	settings.CatalogAvailability = "all"
	if err := f.s.requests.Save(settings); err != nil {
		t.Fatal(err)
	}
	out = userWorkflowCall(t, f.s, "alice", "GET", "/api/catalog/search?q=Example", "")
	page = tmdb.CatalogPage{}
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &page) != nil || len(page.Results) != 5 || page.AvailabilityFiltered {
		t.Fatal("all-titles mode did not restore upcoming titles")
	}
	if response := userWorkflowCall(t, f.s, "alice", "GET", "/api/catalog/movie/1", ""); response.Code != 200 {
		t.Fatal("all-titles mode blocked upcoming details")
	}
}
func TestCatalogVisibilityOnlyChangeDoesNotRequeueExistingRequests(t *testing.T) {
	f := newWorkflowFixture(t, false)
	job := createWorkflowJob(t, f, "alice", false)
	job.Status = "complete"
	job.Progress = 1
	if err := f.s.requests.Update(job); err != nil {
		t.Fatal(err)
	}
	settings, _ := f.s.requests.Load()
	settings.CatalogAvailability = "digital"
	raw, _ := json.Marshal(settings)
	out := userWorkflowCall(t, f.s, "admin", "PUT", "/api/requests/settings", string(raw))
	if out.Code != 200 {
		t.Fatalf("catalog setting save failed: %d %s", out.Code, out.Body.String())
	}
	current, _ := f.s.requests.Job(job.ID)
	if current.Status != "complete" || current.PreferSpanishAudio {
		t.Fatal("catalog setting changed an existing request")
	}
	reloaded, _ := requests.NewStore(f.cfg.Requests.StatePath).Load()
	if reloaded.CatalogAvailability != "digital" {
		t.Fatal("catalog setting did not persist")
	}
	settings.CatalogAvailability = "invalid"
	raw, _ = json.Marshal(settings)
	if response := userWorkflowCall(t, f.s, "admin", "PUT", "/api/requests/settings", string(raw)); response.Code != 400 {
		t.Fatal("invalid catalog mode accepted")
	}
	if response := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", `{"media_type":"movie","tmdb_id":101,"catalog_availability":"all"}`); response.Code != 400 || !strings.Contains(response.Body.String(), "Choose a movie") {
		t.Fatal("user could override catalog policy")
	}
}
