package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cineroute/internal/tmdb"
)

func TestLegacyMonitoringAndAudioFlagsDoNotChangeSelectedSeasonCoverage(t *testing.T) {
	// A legacy job followed new seasons and requested only episodes airing after
	// its creation. Both flags are now ignored: only its explicit season remains.
	var job Job
	raw := `{"id":"legacy","media":{"id":202,"media_type":"tv","seasons":[{"season_number":1,"air_date":"2020-01-01"},{"season_number":2,"air_date":"2020-02-01"}]},"seasons":[1],"created_at":"2019-01-01T00:00:00Z","follow_future":true,"only_future":true,"prefer_spanish_audio":true}`
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatal(err)
	}
	job.CreatedAt = time.Now()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/202/season/1" {
			t.Errorf("unselected season requested: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"episodes":[{"episode_number":1,"season_number":1,"air_date":"2020-01-01"},{"episode_number":2,"season_number":1,"air_date":"2099-01-01"}]}`)
	}))
	defer upstream.Close()
	client := tmdb.New("key", "en-US", time.Second)
	client.SetBaseURL(upstream.URL)
	engine := &Engine{TMDB: client}
	wanted, err := engine.wantedEpisodes(context.Background(), &job, Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 1 || !wanted[EpisodeKey(1, 1)] || !reflect.DeepEqual(job.Seasons, []int{1}) {
		t.Fatalf("legacy flags changed coverage: %v %v", wanted, job.Seasons)
	}
	serialized, _ := json.Marshal(job)
	for _, field := range []string{"follow_future", "only_future", "prefer_spanish_audio"} {
		if strings.Contains(string(serialized), field) {
			t.Fatalf("legacy flag %s persisted", field)
		}
	}
}
func TestLegacyTVMonitorSettingIsIgnored(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	original := Defaults()
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(original)
	var data map[string]any
	json.Unmarshal(raw, &data)
	data["tv_monitor"] = "all"
	raw, _ = json.Marshal(data)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE request_settings SET payload=? WHERE id=1", string(raw))
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || !reflect.DeepEqual(loaded, original) {
		t.Fatalf("legacy monitoring changed settings: %+v %v", loaded, err)
	}
}
func TestLegacySeasonlessRequestWaitsForManualSelection(t *testing.T) {
	engine := &Engine{}
	job := Job{Media: tmdb.Details{Result: tmdb.Result{ID: 202, MediaType: "tv"}}}
	downloads := []Download{}
	if err := engine.process(context.Background(), &job, Defaults(), &downloads); err != nil {
		t.Fatal(err)
	}
	if job.Status != "waiting" || !strings.Contains(job.Message, "Select a season") {
		t.Fatalf("seasonless legacy request did not wait: %+v", job)
	}
}
