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
