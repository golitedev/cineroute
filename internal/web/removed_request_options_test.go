package web

import (
	"cineroute/internal/requests"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func seedRemovedRequestOptions(t *testing.T, s *Server, id string) {
	t.Helper()
	db, err := sql.Open("sqlite", s.cfg.Requests.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow("SELECT data FROM media_requests WHERE id=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"prefer_spanish_audio", "follow_future", "only_future"} {
		value[field] = true
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE media_requests SET data=? WHERE id=?", string(data), id); err != nil {
		t.Fatal(err)
	}
}
func TestRemovedRequestOptionsCannotBeSubmittedOrLeakFromLegacyJobs(t *testing.T) {
	for _, tv := range []bool{false, true} {
		t.Run(fmt.Sprint(tv), func(t *testing.T) {
			f := newWorkflowFixture(t, tv)
			kind, id, extra := "movie", 101, ""
			if tv {
				kind, id, extra = "tv", 202, `,"seasons":[1]`
			}
			for _, option := range []string{"prefer_spanish_audio", "follow_future", "only_future"} {
				body := fmt.Sprintf(`{"media_type":%q,"tmdb_id":%d%s,%q:true}`, kind, id, extra, option)
				if out := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", body); out.Code != 400 {
					t.Fatalf("removed option %s accepted: %d", option, out.Code)
				}
			}
			job := createWorkflowJob(t, f, "alice", tv)
			seedRemovedRequestOptions(t, f.s, job.ID)
			for _, path := range []string{"/api/user/requests", "/api/requests/queue", "/api/requests/history"} {
				user := "admin"
				if path == "/api/user/requests" {
					user = "alice"
				}
				out := userWorkflowCall(t, f.s, user, "GET", path, "")
				if out.Code != 200 {
					t.Fatalf("legacy request not readable: %d %s", out.Code, out.Body.String())
				}
				for _, removed := range []string{"prefer_spanish_audio", "follow_future", "only_future"} {
					if strings.Contains(out.Body.String(), removed) {
						t.Fatalf("removed option %s exposed in %s", removed, path)
					}
				}
			}
			before, err := f.s.requests.Job(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			same, err := f.s.requests.Create(before, mustRequestSettings(t, f.s), false)
			if err != nil || same.ID != job.ID || same.Status != before.Status {
				t.Fatal("legacy flags changed identity or request state")
			}
		})
	}
}
func mustRequestSettings(t *testing.T, s *Server) (settings requests.Settings) {
	t.Helper()
	settings, err := s.requests.Load()
	if err != nil {
		t.Fatal(err)
	}
	return settings
}
func TestTVRequestsRequireAnExplicitSeason(t *testing.T) {
	f := newWorkflowFixture(t, true)
	for _, body := range []string{`{"media_type":"tv","tmdb_id":202}`, `{"media_type":"tv","tmdb_id":202,"seasons":[]}`} {
		if out := userWorkflowCall(t, f.s, "alice", "POST", "/api/user/requests", body); out.Code != 400 {
			t.Fatalf("seasonless TV request accepted: %d %s", out.Code, out.Body.String())
		}
	}
}
