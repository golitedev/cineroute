package tmdb

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDigitallyAvailableRequiresDigitalMovieDatesAndAiredTV(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, raw, kind string
		want            bool
	}{
		{"digital past", `{"release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2020-01-01T00:00:00.000Z"}]}]}}`, "movie", true},
		{"digital future", `{"release_date":"2020-01-01","status":"Released","release_dates":{"results":[{"release_dates":[{"type":3,"release_date":"2020-01-01"},{"type":4,"release_date":"2099-01-01"}]}]}}`, "movie", false},
		{"physical only", `{"release_dates":{"results":[{"release_dates":[{"type":5,"release_date":"2020-01-01"}]}]}}`, "movie", false},
		{"released status alone", `{"status":"Released","release_date":"2020-01-01"}`, "movie", false},
		{"later today", `{"release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2026-10-25T23:00:00Z"}]}]}}`, "movie", false},
		{"bad date", `{"release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2020-invalid"}]}]}}`, "movie", false},
		{"aired series", `{"first_air_date":"2020-01-01"}`, "tv", true},
		{"future series", `{"first_air_date":"2099-01-01"}`, "tv", false},
		{"unknown series", `{}`, "tv", false},
		{"regular episode aired", `{"last_episode_to_air":{"season_number":1,"episode_number":1,"air_date":"2020-01-01"}}`, "tv", true},
		{"only unaired specials", `{"last_episode_to_air":{"season_number":0,"episode_number":1,"air_date":"2020-01-01"}}`, "tv", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d Details
			if err := json.Unmarshal([]byte(tc.raw), &d); err != nil {
				t.Fatal(err)
			}
			d.MediaType = tc.kind
			if got := DigitallyAvailable(d, now); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
