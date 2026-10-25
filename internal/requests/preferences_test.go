package requests

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"cineroute/internal/prowlarr"
	"cineroute/internal/tmdb"
)

func TestSpanishPreferencePersistsAndUpdatesWithoutChangingApprovalOrQuota(t *testing.T) {
	settings := Defaults()
	settings.AutoApprove = false
	settings.MovieLimit = 1
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	initial := Job{Owner: "alice", Media: tmdb.Details{Result: tmdb.Result{ID: 1, MediaType: "movie"}}, Target: "remote", Status: "pending", Message: "Waiting for administrator approval"}
	job, err := store.Create(initial, settings, false)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	updated, err := store.Create(initial, settings, false, &yes)
	if err != nil || !updated.PreferSpanishAudio || updated.ID != job.ID || updated.Status != "pending" || !updated.ApprovedAt.IsZero() {
		t.Fatalf("preference update lost identity or bypassed approval/quota: %+v %v", updated, err)
	}
	again, err := store.Create(initial, settings, false, nil)
	if err != nil || !again.PreferSpanishAudio {
		t.Fatal("an omitted option reset the stored preference")
	}
	reloaded, err := NewStore(store.path).Job(job.ID)
	if err != nil || !reloaded.PreferSpanishAudio {
		t.Fatal("preference did not survive reload")
	}
	if _, err = store.Action(job.ID, "", "approve"); err != nil {
		t.Fatal(err)
	}
	stale, _ := store.Job(job.ID)
	no := false
	changed, err := store.Create(initial, settings, false, &no)
	if err != nil || changed.PreferSpanishAudio || changed.Status != "queued" {
		t.Fatalf("unchecking preference failed: %+v %v", changed, err)
	}
	stale.Status = "downloading"
	stale.NextSearch = time.Now().Add(time.Hour)
	if err = store.Update(stale); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Job(job.ID)
	if current.PreferSpanishAudio || current.Status != "queued" || current.NextSearch.After(time.Now()) {
		t.Fatalf("stale worker overwrote the changed preference: %+v", current)
	}
	jobs, _ := store.Jobs("alice")
	if len(jobs) != 1 {
		t.Fatal("updating preference created another request")
	}
}
func TestSpanishRequestProfileIsIndependentOfAdminAndOtherRequests(t *testing.T) {
	profile := Defaults().Movie
	profile.PreferredAudioLanguages = make([]string, 1, 4)
	profile.PreferredAudioLanguages[0] = "en"
	preferred := requestProfile(profile, Job{PreferSpanishAudio: true})
	if !slices.Equal(preferred.PreferredAudioLanguages, []string{"en", "es"}) || !slices.Equal(profile.PreferredAudioLanguages, []string{"en"}) {
		t.Fatal("effective preference changed the admin profile")
	}
	ordinary := requestProfile(profile, Job{})
	if slices.Contains(ordinary.PreferredAudioLanguages, "es") {
		t.Fatal("one request changed another request's preference")
	}
	if preferred.Destination != profile.Destination || !slices.Equal(preferred.Qualities, profile.Qualities) || !slices.Equal(preferred.AudioLanguages, profile.AudioLanguages) {
		t.Fatal("user preference changed an admin filter/destination")
	}
}
func TestLatTeamIdentityComesFromConfiguredIndexer(t *testing.T) {
	for _, tc := range []struct {
		indexer prowlarr.Indexer
		want    bool
	}{
		{prowlarr.Indexer{Name: "Renamed tracker", DefinitionName: "lat-team-api"}, true},
		{prowlarr.Indexer{Name: "Lat-Team (API)"}, true},
		{prowlarr.Indexer{Name: "Lat Team"}, true},
		{prowlarr.Indexer{Name: "LAT-TEAM"}, true},
		{prowlarr.Indexer{Name: "Not Lat-Team"}, false},
		{prowlarr.Indexer{Name: "Lat-Team", DefinitionName: "aither-api"}, false},
	} {
		if got := isLatTeam(tc.indexer); got != tc.want {
			t.Errorf("identity %+v: %v", tc.indexer, got)
		}
	}
}
func TestUserSpanishScorePrecedesOtherPreferencesButPreservesQuality(t *testing.T) {
	spanish := Candidate{Rank: 0, RequestedAudioScore: 1, LanguageScore: 1, Priority: 50}
	other := Candidate{Rank: 0, LanguageScore: 5, SubtitleFormatScore: 3, Score: 100, Priority: 0}
	if !Better(spanish, other, true) {
		t.Fatal("other preferences hid Spanish audio requested by the user")
	}
	spanish.Rank = 1
	if Better(spanish, other, true) {
		t.Fatal("user preference overrode quality order")
	}
}
