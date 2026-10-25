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
