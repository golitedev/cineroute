package subtitles

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cineroute/internal/subtitles/opensubtitles"
)

func movieFile(t *testing.T, video string) MovieFile {
	t.Helper()
	info, err := os.Stat(video)
	if err != nil {
		t.Fatal(err)
	}
	return MovieFile{DriveID: "hdd1", Target: "remote", Path: video, Size: info.Size()}
}

func TestAutomaticMovieAddsOnlyMissingLanguagesAndLeavesOtherMoviesAlone(t *testing.T) {
	h := newTestHarness(t)
	h.manager.cfg.TargetLanguages = []string{"sv", "es", "en"}
	video := h.addRemoteMovie(t, "Movie (2019)", "Movie.2019.1080p.WEB-DL.mkv")
	original, _ := os.ReadFile(video)
	unrelated := h.addRemoteMovie(t, "Other (2019)", "Other.2019.1080p.WEB-DL.mkv")
	h.configureEmbeddedEnglish(video)
	h.os.items = []opensubtitles.Item{
		languageCandidate("Movie", 2019, "sv", "Movie.2019.1080p.WEB-DL", 701),
		languageCandidate("Movie", 2019, "es", "Movie.2019.1080p.WEB-DL", 702),
	}
	file := movieFile(t, video)
	if err := h.manager.QueueRequestedMovie(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if len(h.manager.View("").Items) != 1 {
		t.Fatal("completion scanned unrelated library movies")
	}
	// A restart must retain the queued handoff, without rescanning the library.
	m := newManager(h.manager.cfg, h.manager.lib, h.manager.store, h.prober, fakeSyncer{}, h.os)
	if !m.runNextAutomatic(context.Background()) || m.runNextAutomatic(context.Background()) {
		t.Fatal("automatic movie was lost or processed more than once")
	}
	item := m.View("").Items[0]
	if item.Status != StatusAdded || item.AutomaticPending || h.os.downloads != 2 || len(h.prober.extracted) != 1 {
		t.Fatalf("wrong automatic result: %+v, downloads=%d, extractions=%v", item, h.os.downloads, h.prober.extracted)
	}
	for _, lang := range []string{"sv", "es"} {
		if !IsValidSRTFile(subtitleOutputPath(video, lang)) {
			t.Errorf("missing %s subtitle", lang)
		}
	}
	if item.Target("en").Status != TargetPresent {
		t.Fatal("embedded English text subtitle was not preserved")
	}
	for _, query := range h.os.queries {
		if hasCSVValue(query.Languages, "en") {
			t.Fatal("searched for English even though full English text already exists")
		}
	}
	if data, _ := os.ReadFile(video); string(data) != string(original) {
		t.Fatal("subtitle work modified the seeding movie")
	}
	if _, err := os.Stat(subtitleOutputPath(unrelated, "sv")); !os.IsNotExist(err) {
		t.Fatal("processed an unrelated movie")
	}
	if err := m.QueueRequestedMovie(context.Background(), file); err != nil || m.runNextAutomatic(context.Background()) {
		t.Fatalf("duplicate completion repeated subtitle work: %v", err)
	}
}

func TestAutomaticMovieSkipsImageOnlyAndAlreadySatisfiedReleases(t *testing.T) {
	for _, mode := range []string{"image-only", "no-subtitles", "already-present"} {
		t.Run(mode, func(t *testing.T) {
			h := newTestHarness(t)
			h.manager.cfg.TargetLanguages = []string{"sv", "es", "en"}
			video := h.addRemoteMovie(t, "Movie (2019)", "Movie.2019.1080p.mkv")
			if mode == "image-only" {
				h.prober.streams[video] = MediaInfo{Streams: []EmbeddedSubtitle{{Index: 2, Codec: "hdmv_pgs_subtitle", Language: "en", Usable: false}}}
			}
			if mode == "already-present" {
				for _, lang := range []string{"sv", "es", "en"} {
					if err := os.WriteFile(subtitleOutputPath(video, lang), []byte(referenceSRT), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := h.manager.QueueRequestedMovie(context.Background(), movieFile(t, video)); err != nil {
				t.Fatal(err)
			}
			h.manager.runNextAutomatic(context.Background())
			item := h.manager.View("").Items[0]
			want := StatusNoReference
			if mode == "already-present" {
				want = StatusHasTargets
			}
			if item.Status != want || item.AutomaticPending || h.os.searches != 0 || h.os.downloads != 0 {
				t.Fatalf("unexpected work/result: %+v searches=%d downloads=%d", item, h.os.searches, h.os.downloads)
			}
		})
	}
}

func TestAutomaticMoviePathsMainLibraryAndManualWork(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Movie (2019)", "Movie.2019.1080p.mkv")
	file := movieFile(t, video)
	h.manager.batch.Running = true
	if err := h.manager.QueueRequestedMovie(context.Background(), file); err == nil {
		t.Fatal("automatic handoff interrupted manual work")
	}
	h.manager.batch.Running = false
	for _, bad := range []MovieFile{
		{DriveID: "unknown", Target: "remote", Path: video, Size: file.Size},
		{DriveID: "hdd1", Target: "main", Path: video, Size: file.Size},
		{DriveID: "hdd1", Target: "remote", Path: video, Size: file.Size + 1},
	} {
		if err := h.manager.QueueRequestedMovie(context.Background(), bad); err == nil {
			t.Fatalf("accepted wrong destination/incomplete video: %+v", bad)
		}
	}
	alias := filepath.Join(h.remote, "Linked (2019)")
	if err := os.Symlink(filepath.Dir(video), alias); err != nil {
		t.Fatal(err)
	}
	bad := file
	bad.Path = filepath.Join(alias, filepath.Base(video))
	if err := h.manager.QueueRequestedMovie(context.Background(), bad); err == nil {
		t.Fatal("followed a symlink into a movie folder")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(h.manager.lib.Drives()[0].MovieRoot, "Movie (2019)", filepath.Base(video))
	if err := os.MkdirAll(filepath.Dir(main), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(video, main); err != nil {
		t.Fatal(err)
	}
	mainFile := file
	mainFile.Target, mainFile.Path = "main", main
	for _, f := range []MovieFile{file, mainFile} {
		if err := h.manager.QueueRequestedMovie(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := h.manager.View("").Items
	if len(items) != 2 || items[0].ID == items[1].ID || !items[0].AutomaticPending || !items[1].AutomaticPending {
		t.Fatalf("main and remote work collided or was removed by the remote scan: %+v", items)
	}
}

func TestAutomaticMovieQuotaWaitIsDurableAndBounded(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Movie (2019)", "Movie.2019.1080p.WEB-DL.mkv")
	h.configureEmbeddedEnglish(video)
	h.os.items = []opensubtitles.Item{swedishCandidate()}
	h.os.remaining = h.manager.cfg.QuotaReserve
	if err := h.manager.QueueRequestedMovie(context.Background(), movieFile(t, video)); err != nil {
		t.Fatal(err)
	}
	h.manager.runNextAutomatic(context.Background())
	item := h.manager.View("").Items[0]
	if !item.AutomaticPending || time.Until(item.AutomaticRetryAt) < 50*time.Minute || h.os.downloads != 0 {
		t.Fatalf("quota reserve did not defer work: %+v", item)
	}
	m := newManager(h.manager.cfg, h.manager.lib, h.manager.store, h.prober, fakeSyncer{}, h.os)
	if m.runNextAutomatic(context.Background()) {
		t.Fatal("restart ignored the saved quota wait")
	}
}

func TestForcedTracksAreNotFullTargetSubtitles(t *testing.T) {
	states := targetStates([]string{"es", "sv", "en"}, []externalSubtitle{{Ext: ".srt", Language: "es", Forced: true}, {Ext: ".srt", Language: "sv"}}, []EmbeddedSubtitle{{Language: "en", Usable: true, Forced: true}})
	if states[0].Present || !states[1].Present || states[2].Present {
		t.Fatalf("forced-only tracks satisfied full-subtitle targets: %+v", states)
	}
	if !containsForcedLabel("es.forced") || !containsForcedLabel("en-SIGNS") || containsForcedLabel("en.sdh") {
		t.Fatal("forced and full subtitles were confused")
	}
	if !strings.Contains(targetSummary(states), "es") {
		t.Fatal("missing languages lost from status")
	}
}

func TestAutomaticWorkerResumesAfterShutdownAndDrainsPersistedWork(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Movie (2019)", "Movie.2019.1080p.WEB-DL.mkv")
	h.configureEmbeddedEnglish(video)
	candidate := swedishCandidate()
	// The restart reloads the original API payload, rather than an in-memory
	// candidate; use the complete payload a real OpenSubtitles response carries.
	candidate.Raw, _ = json.Marshal(candidate)
	h.os.items = []opensubtitles.Item{candidate}
	if err := h.manager.QueueRequestedMovie(context.Background(), movieFile(t, video)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.prober.cancelDuringExtract = cancel
	h.manager.runNextAutomatic(ctx)
	if item := h.manager.View("").Items[0]; !item.AutomaticPending || item.Status != StatusPending {
		t.Fatalf("shutdown lost the in-flight movie: %+v", item)
	}
	h.prober.cancelDuringExtract = nil
	m := newManager(h.manager.cfg, h.manager.lib, h.manager.store, h.prober, fakeSyncer{}, h.os)
	m.StartAutomatic(context.Background())
	defer m.StopAutomatic()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view := m.View("")
		if !view.Batch.Running && !view.Items[0].AutomaticPending {
			if view.Items[0].Status != StatusAdded || !IsValidSRTFile(subtitleOutputPath(video, "sv")) {
				t.Fatalf("resumed worker did not finish the movie: %+v", view.Items[0])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("automatic worker did not drain persisted work")
}
