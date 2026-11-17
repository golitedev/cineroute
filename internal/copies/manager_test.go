package copies

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cineroute/internal/library"
)

func TestFailedScanPreservesInventoryAndLinkRevalidatesDeletedTitle(t *testing.T) {
	primary, remote := t.TempDir(), t.TempDir()
	folder := filepath.Join(primary, "Movie (2024)")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(folder, "Movie.mkv")
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(library.NewScan([]library.Drive{{ID: "disk1", MovieRoot: primary, MovieRemoteRoot: remote}}), "movie")
	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := m.View("").Items[0].ID
	if err := os.Remove(remote); err != nil {
		t.Fatal(err)
	}
	if err := m.Scan(context.Background()); err == nil {
		t.Fatal("scan accepted an unavailable remote root")
	}
	if view := m.View(id); len(view.Items) != 1 || view.Open == nil || view.StateError == "" {
		t.Fatalf("failed scan discarded the previous inventory: %+v", view)
	}
	if err := os.Mkdir(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(video); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Hardlink(context.Background(), id); err == nil {
		t.Fatal("hardlink accepted a stale title with no video")
	}
	if _, err := os.Stat(filepath.Join(remote, filepath.Base(folder))); !os.IsNotExist(err) {
		t.Fatalf("stale hardlink created a remote folder: %v", err)
	}
}

func TestAnimeInventoryIsPrimaryOnlyAndHasNoHardlinks(t *testing.T) {
	anime := t.TempDir()
	folder := filepath.Join(anime, "Frieren (2023)")
	if err := os.MkdirAll(filepath.Join(folder, "Season 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(folder, "Season 1", "Frieren.S01E01.mkv")
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(anime, "Empty (2024)")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(library.NewScan([]library.Drive{{ID: "disk1", AnimeRoot: anime}}), "anime")
	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := m.View("")
	if view.MediaType != "anime" || len(view.Items) != 1 {
		t.Fatalf("anime view = %+v", view)
	}
	item := view.Items[0]
	if item.FolderName != "Frieren (2023)" || item.DriveID != "disk1" || item.Status != "main_only" || item.RemotePath != "" {
		t.Fatalf("anime item = %+v", item)
	}
	if len(item.ExistingFiles) != 1 || item.ExistingFiles[0] != video {
		t.Fatalf("anime files = %+v", item.ExistingFiles)
	}
	if _, err := m.Hardlink(context.Background(), item.ID); err == nil {
		t.Fatal("anime hardlink was accepted")
	}
}

func TestInventoryAddedAtUsesEarliestPopulatedFolder(t *testing.T) {
	for _, kind := range []string{"movie", "tv", "anime"} {
		t.Run(kind, func(t *testing.T) {
			main, remote := t.TempDir(), t.TempDir()
			name := "Title (2024)"
			mainFolder, remoteFolder := filepath.Join(main, name), filepath.Join(remote, name)
			for _, folder := range []string{mainFolder, remoteFolder} {
				if err := os.Mkdir(folder, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// An empty main directory must not determine a remote-only title's date.
			videoFolder := remoteFolder
			if kind == "anime" {
				videoFolder = mainFolder
			}
			if err := os.WriteFile(filepath.Join(videoFolder, "video.mkv"), []byte("video"), 0o644); err != nil {
				t.Fatal(err)
			}
			drive := library.Drive{ID: "disk1", MovieRoot: main, MovieRemoteRoot: remote, TVRoot: main, TVRemoteRoot: remote, AnimeRoot: main}
			m := NewManager(library.NewScan([]library.Drive{drive}), kind)
			if err := m.Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			want, err := folderAddedAt(videoFolder)
			if err != nil {
				t.Fatal(err)
			}
			item := m.View("").Items[0]
			if item.AddedAt.IsZero() || !item.AddedAt.Equal(want) {
				t.Fatalf("added_at = %v, want %v", item.AddedAt, want)
			}
			if kind != "anime" {
				if _, err := m.Hardlink(context.Background(), item.ID); err != nil {
					t.Fatal(err)
				}
				mainAt, err := folderAddedAt(mainFolder)
				if err != nil {
					t.Fatal(err)
				}
				if mainAt.Before(want) {
					want = mainAt
				}
			}
			if err := m.Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := m.View("").Items[0].AddedAt; !got.Equal(want) {
				t.Fatalf("added_at after rescan = %v, want %v", got, want)
			}
		})
	}
}
