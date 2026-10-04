package subtitles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cineroute/internal/library"
)

func TestScanPrunesDeletedVideosAndLegacyPaths(t *testing.T) {
	h := newTestHarness(t)
	h.manager.lib = library.NewScan([]library.Drive{{ID: "disk1", MovieRemoteRoot: h.remote}})
	video := h.addRemoteMovie(t, "Hamilton (2012)", "Hamilton.2012.mkv")
	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	current := h.manager.View("").Items[0]
	legacy := *current
	legacy.ID = subtitleItemID("hdd3", "Hamilton (2012)/Hamilton.2012.mkv")
	legacy.DriveID = "hdd3"
	legacy.RemoteRoot = "/hdd3/movies-remote"
	legacy.VideoPath = "/hdd3/movies-remote/Hamilton (2012)/Hamilton.2012.mkv"
	if err := h.manager.store.saveItem(&legacy); err != nil {
		t.Fatal(err)
	}
	h.manager.items = append(h.manager.items, &legacy)
	h.manager.byID[legacy.ID] = &legacy
	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := h.manager.store.loadItems()
	if err != nil || len(items) != 1 || items[0].DriveID != "disk1" || items[0].VideoPath != video {
		t.Fatalf("obsolete paths survived: %+v, error = %v", items, err)
	}

	// Cached API results and attempt history must disappear with the video.
	queries := []string{
		"INSERT INTO subtitle_searches (item_id, kind, query, status, searched_at) VALUES (?, 'title', 'Hamilton', 'ok', '')",
		"INSERT INTO subtitle_candidates (item_id, file_id, rank, score, category) VALUES (?, 1, 1, 1, 'exact')",
		"INSERT INTO subtitle_features (item_id) VALUES (?)",
		"INSERT INTO subtitle_attempts (item_id, file_id, status, at) VALUES (?, 1, 'added', '')",
	}
	for _, query := range queries {
		if _, err := h.manager.store.db.Exec(query, current.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(video); err != nil {
		t.Fatal(err)
	}
	// Leave the folder and a subtitle behind: the deleted video is still gone.
	if err := os.WriteFile(filepath.Join(filepath.Dir(video), "Hamilton.2012.sv.srt"), []byte(swedishSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.manager.View("").Items) != 0 || h.manager.itemByID(current.ID) != nil {
		t.Fatal("deleted video survived in live inventory")
	}
	for _, table := range []string{"subtitle_items", "subtitle_searches", "subtitle_candidates", "subtitle_features", "subtitle_attempts"} {
		var count int
		if err := h.manager.store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s has %d obsolete rows, error = %v", table, count, err)
		}
	}
	restarted := newManager(h.manager.cfg, h.manager.lib, h.manager.store, h.prober, fakeSyncer{}, h.os)
	if err := restarted.StateError(); err != nil || len(restarted.View("").Items) != 0 {
		t.Fatalf("deleted video returned after restart: %v", err)
	}
}

type cancelScanProber struct {
	Prober
	cancel context.CancelFunc
}

func (p cancelScanProber) Probe(ctx context.Context, path string) (MediaInfo, error) {
	p.cancel()
	return p.Prober.Probe(ctx, path)
}

func TestIncompleteSubtitleScanPreservesInventory(t *testing.T) {
	for _, failure := range []string{"missing root", "unreadable root", "canceled during probe", "database failure"} {
		t.Run(failure, func(t *testing.T) {
			h := newTestHarness(t)
			video := h.addRemoteMovie(t, "Movie (2024)", "Movie.2024.mkv")
			if err := h.manager.Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			id := h.manager.View("").Items[0].ID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "missing root":
				if err := os.RemoveAll(h.remote); err != nil {
					t.Fatal(err)
				}
			case "unreadable root":
				if err := os.RemoveAll(h.remote); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(h.remote, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			case "canceled during probe":
				// Add a second video so the cached first video's presence cannot
				// hide the cancellation during fresh work.
				h.addRemoteMovie(t, "Other (2024)", "Other.2024.mkv")
				h.manager.prober = cancelScanProber{Prober: h.prober, cancel: cancel}
			case "database failure":
				if err := os.Remove(video); err != nil {
					t.Fatal(err)
				}
				if _, err := h.manager.store.db.Exec("CREATE TRIGGER reject_delete BEFORE DELETE ON subtitle_items BEGIN SELECT RAISE(ABORT, 'failed deletion'); END"); err != nil {
					t.Fatal(err)
				}
			}
			err := h.manager.Scan(ctx)
			if err == nil || (failure == "canceled during probe" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("unexpected scan error: %v", err)
			}
			items, loadErr := h.manager.store.loadItems()
			if loadErr != nil || len(items) != 1 || items[0].ID != id || len(h.manager.View("").Items) != 1 || h.manager.itemByID(id) == nil {
				t.Fatalf("incomplete scan modified inventory: %+v, error = %v", items, loadErr)
			}
		})
	}
}
