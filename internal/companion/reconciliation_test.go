package companion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/library"
)

func TestScanPrunesDeletedAndObsoleteCompanions(t *testing.T) {
	for _, kind := range []companionKind{companionMovie, companionTV} {
		for _, deletion := range []string{"folder", "video"} {
			t.Run(string(kind)+"/"+deletion, func(t *testing.T) {
				primary, remote := t.TempDir(), t.TempDir()
				name := "Hamilton (2012)"
				folder := filepath.Join(primary, name)
				if err := os.MkdirAll(folder, 0o755); err != nil {
					t.Fatal(err)
				}
				video := filepath.Join(folder, "Hamilton.2012.S01.2160p.mkv")
				if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
					t.Fatal(err)
				}
				drive := library.Drive{ID: "disk1", MovieRoot: primary, MovieRemoteRoot: remote, TVRoot: primary, TVRemoteRoot: remote}
				cfg := config.Default()
				statePath := filepath.Join(t.TempDir(), "queue.db")
				m := newManager(cfg, library.NewScan([]library.Drive{drive}), nil, kind, statePath)
				defer m.store.db.Close()
				oldID := movieID("hdd3", name)
				m.state.Movies = []*Movie{{ID: oldID, DriveID: "hdd3", FolderName: name, Path: "/hdd3/movies/" + name, Status: StatusComplete}}
				m.searches[oldID] = searchState{Candidates: []Candidate{{Guid: "stale"}}, SearchedAt: time.Now()}
				if err := m.persistLocked(searchHistoryRecord{MovieID: oldID}); err != nil {
					t.Fatal(err)
				}
				if err := m.Scan(context.Background()); err != nil {
					t.Fatal(err)
				}
				view := m.View(oldID)
				if len(view.Movies) != 1 || view.Movies[0].DriveID != "disk1" || view.Movies[0].Path != folder || view.Open != nil {
					t.Fatalf("obsolete HDD entry survived: %+v", view)
				}
				if _, ok := m.searches[oldID]; ok {
					t.Fatal("obsolete search survived")
				}
				id := view.Movies[0].ID
				m.searches[id] = searchState{Candidates: []Candidate{{Guid: "current"}}, SearchedAt: time.Now()}
				if err := m.persistLocked(searchHistoryRecord{MovieID: id}); err != nil {
					t.Fatal(err)
				}
				if deletion == "folder" {
					if err := os.RemoveAll(folder); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Remove(video); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(folder, "poster.jpg"), nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := m.Scan(context.Background()); err != nil {
					t.Fatal(err)
				}
				if len(m.View(id).Movies) != 0 || m.View(id).Open != nil {
					t.Fatal("deleted media survived in live inventory")
				}
				reloaded := newManager(cfg, m.lib, nil, kind, statePath)
				defer reloaded.store.db.Close()
				if len(reloaded.state.Movies) != 0 || len(reloaded.searches) != 0 {
					t.Fatal("deleted media returned after restart")
				}
				var count int
				if err := m.store.db.QueryRow("SELECT COUNT(*) FROM companion_search_history").Scan(&count); err != nil || count != 0 {
					t.Fatalf("obsolete history count = %d, error = %v", count, err)
				}
			})
		}
	}
}

func TestScanRemovesMissingCompanionsInEveryStatus(t *testing.T) {
	for _, status := range []string{StatusComplete, StatusSkipped, StatusPending, StatusSearching, StatusReview, StatusSubmitting, StatusError} {
		t.Run(status, func(t *testing.T) {
			m := newTransitionManager(t, status)
			m.lib = library.NewScan([]library.Drive{{ID: "disk1", MovieRoot: t.TempDir()}})
			if err := m.Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(m.state.Movies) != 0 {
				t.Fatal("missing entry was retained")
			}
		})
	}
}

func TestIncompleteCompanionScanPreservesInventory(t *testing.T) {
	for _, failure := range []string{"missing root", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			m := newTransitionManager(t, StatusComplete)
			root := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "canceled" {
				cancel()
			} else {
				root = filepath.Join(root, "not-mounted")
			}
			m.lib = library.NewScan([]library.Drive{{ID: "disk1", MovieRoot: root}})
			m.searches["movie"] = searchState{Candidates: []Candidate{{Guid: "keep"}}}
			err := m.Scan(ctx)
			if err == nil || (failure == "canceled" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("unexpected scan error: %v", err)
			}
			if len(m.state.Movies) != 1 || m.state.Movies[0].Status != StatusComplete || len(m.searches) != 1 {
				t.Fatal("incomplete scan modified saved inventory")
			}
		})
	}
}

func TestHardlinkInventoryDropsDeletedFiles(t *testing.T) {
	primary, remote := t.TempDir(), t.TempDir()
	source := filepath.Join(primary, "Movie (2024)")
	destination := filepath.Join(remote, "Movie (2024)")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Movie.mkv"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hardlinkTree(source, destination); err != nil {
		t.Fatal(err)
	}
	m := NewHardlinkManager(library.NewScan([]library.Drive{{ID: "disk3", MovieRoot: primary, MovieRemoteRoot: remote}}))
	view, err := m.View(context.Background())
	if err != nil || len(view.Items) != 1 || view.Items[0].DriveID != "disk3" {
		t.Fatalf("initial hardlinks = %+v, error = %v", view, err)
	}
	if err := os.Remove(filepath.Join(destination, "Movie.mkv")); err != nil {
		t.Fatal(err)
	}
	view, err = m.View(context.Background())
	if err != nil || len(view.Items) != 0 || view.Stats.Total != 0 {
		t.Fatalf("deleted hardlink survived: %+v, error = %v", view, err)
	}
	if _, err := os.Stat(filepath.Join(source, "Movie.mkv")); err != nil {
		t.Fatalf("scan modified source video: %v", err)
	}
}
