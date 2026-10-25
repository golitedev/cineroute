package copies

import (
	"cineroute/internal/library"
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
