package requests

import (
	"cineroute/internal/medialanguages"
	"cineroute/internal/prowlarr"
	"cineroute/internal/tmdb"
	"path/filepath"
	"testing"
)

func TestSpanishAudioRequiresReportedAudioForMoviesAndTV(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		job := Job{SpanishAudio: true, Media: tmdb.Details{Result: tmdb.Result{ID: 1, Title: "Example", Name: "Example", MediaType: kind, ReleaseDate: "2024-01-01"}}}
		s := Defaults()
		s.Movie.AllowUnknownLanguage = true
		s.TV.AllowUnknownLanguage = true
		p := ProfileForJob(s, job)
		title := "Example.2024.1080p.WEB-DL.x264.Spanish.Subs"
		if kind == "tv" {
			title = "Example.S01.1080p.WEB-DL.x264.Spanish.Subs"
		}
		for _, metadata := range []medialanguages.Metadata{{}, {Audio: []string{"en"}, Subtitles: []string{"es"}}, {Audio: []string{"es", "en"}}} {
			r := prowlarr.Release{Title: title, Languages: metadata}
			_, err := Evaluate(r, job.Media, p, 0, 1)
			want := len(metadata.Audio) > 0 && metadata.Audio[0] == "es"
			if (err == nil) != want {
				t.Fatalf("%s accepted=%v metadata=%+v err=%v", kind, err == nil, metadata, err)
			}
		}
		job.SpanishAudio = false
		if ProfileForJob(s, job).RequireSpanishAudio {
			t.Fatal("normal routing kept strict requirement")
		}
	}
}

func TestSpanishTrackerSettingsPersistAndOnlyExpandSpanishRequests(t *testing.T) {
	s := Defaults()
	s.Trackers = []Tracker{{ID: 1, Name: "normal", Priority: 10}}
	s.SpanishTrackers = []Tracker{{ID: 2, Name: "Spanish", Priority: 1}}
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	s, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	normal := s.TrackersForJob(Job{})
	spanish := s.TrackersForJob(Job{SpanishAudio: true})
	if len(normal) != 1 || normal[0].ID != 1 || len(spanish) != 2 || spanish[0].ID != 2 {
		t.Fatalf("wrong tracker selection: %v %v", normal, spanish)
	}
	s.SpanishTrackers = append(s.SpanishTrackers, s.SpanishTrackers[0])
	if s.Validate() == nil {
		t.Fatal("duplicate Spanish tracker accepted")
	}
}

func TestSpanishAudioCanBeRelaxedWithoutDuplicateOrStaleWorkerOverwrite(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	settings := Defaults()
	job, err := store.Create(Job{Owner: "alice", SpanishAudio: true, Media: tmdb.Details{Result: tmdb.Result{ID: 1, MediaType: "movie"}}, Target: "remote", Status: "waiting"}, settings, false)
	if err != nil {
		t.Fatal(err)
	}
	changed := job
	changed.SpanishAudio = false
	updated, err := store.Create(changed, settings, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != job.ID || updated.SpanishAudio || updated.Status != "queued" {
		t.Fatalf("not updated: %+v", updated)
	}
	if err := store.Update(job); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Job(job.ID)
	if current.SpanishAudio {
		t.Fatal("stale worker restored Spanish requirement")
	}
}
