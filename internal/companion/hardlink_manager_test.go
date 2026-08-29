package companion

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cineroute/internal/library"
)

func TestHardlinkManagerRelinksRenamedMovieAndPreservesExtraFiles(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "movies")
	remote := filepath.Join(base, "movies-remote")
	tvPrimary := filepath.Join(base, "tv")
	tvRemote := filepath.Join(base, "tv-remote")
	for _, dir := range []string{primary, remote, tvPrimary, tvRemote} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	oldSource := filepath.Join(primary, "Old Title (2020)")
	oldRemote := filepath.Join(remote, "Old Title (2020)")
	if err := os.MkdirAll(filepath.Join(oldSource, "extras"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(oldSource, "Old Title.mkv"),
		filepath.Join(oldSource, "extras", "poster.jpg"),
	}
	for _, file := range files {
		if err := os.WriteFile(file, []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hardlinkTree(oldSource, oldRemote); err != nil {
		t.Fatal(err)
	}
	newSource := filepath.Join(primary, "New Title (2020)")
	if err := os.Rename(oldSource, newSource); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(newSource, "extras"), filepath.Join(newSource, "artwork")); err != nil {
		t.Fatal(err)
	}
	// Simulate an earlier additive relink: the current path exists, but the
	// old hardlinked path is still present beside it.
	duplicateTarget := filepath.Join(oldRemote, "artwork", "poster.jpg")
	if err := os.MkdirAll(filepath.Dir(duplicateTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(newSource, "artwork", "poster.jpg"), duplicateTarget); err != nil {
		t.Fatal(err)
	}

	showSource := filepath.Join(tvPrimary, "Example Show (2021)")
	showRemote := filepath.Join(tvRemote, "Example Show (2021)")
	if err := os.MkdirAll(showSource, 0o755); err != nil {
		t.Fatal(err)
	}
	showFile := filepath.Join(showSource, "Example.Show.S01E01.mkv")
	if err := os.WriteFile(showFile, []byte("episode"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hardlinkTree(showSource, showRemote); err != nil {
		t.Fatal(err)
	}

	scan := library.NewScan([]library.Drive{{
		ID:              "hdd1",
		MovieRoot:       primary,
		MovieRemoteRoot: remote,
		TVRoot:          tvPrimary,
		TVRemoteRoot:    tvRemote,
	}})
	manager := NewHardlinkManager(scan)
	view, err := manager.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Stats.Total != 2 || view.Stats.Movies != 1 || view.Stats.TVShows != 1 || view.Stats.LinkedFiles != 4 {
		t.Fatalf("stats = %+v, want two folders, one movie, one TV, four remote links including the duplicate", view.Stats)
	}

	var renamed *HardlinkItem
	for _, item := range view.Items {
		if item.MediaType == "movie" {
			renamed = item
			break
		}
	}
	if renamed == nil || renamed.Status != "needs_relink" || renamed.SourceFolder != "New Title (2020)" || renamed.RemoteFolder != "Old Title (2020)" || renamed.PathsMatch {
		t.Fatalf("renamed item = %+v, want needs_relink with the current source name", renamed)
	}

	result, refreshed, err := manager.Relink(context.Background(), renamed.ID)
	if err != nil {
		t.Fatal(err)
	}
	newRemote := filepath.Join(remote, "New Title (2020)")
	if result.DestinationPath != newRemote {
		t.Fatalf("relink destination = %q, want %q", result.DestinationPath, newRemote)
	}
	if result.RemovedDuplicateFiles != 1 {
		t.Fatalf("relink removed duplicates = %d, want 1", result.RemovedDuplicateFiles)
	}
	if _, err := os.Stat(oldRemote); !os.IsNotExist(err) {
		t.Fatalf("old remote folder still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newRemote, "extras")); !os.IsNotExist(err) {
		t.Fatalf("old nested remote folder still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newRemote, "artwork", "poster.jpg")); err != nil {
		t.Fatalf("renamed nested remote link is missing: %v", err)
	}
	if len(refreshed.Items) != 2 {
		t.Fatalf("refreshed items = %d, want 2", len(refreshed.Items))
	}
	var relinked *HardlinkItem
	for _, item := range refreshed.Items {
		if item.MediaType == "movie" {
			relinked = item
			break
		}
	}
	if relinked == nil || relinked.Status != "healthy" || !relinked.NameMatches {
		t.Fatalf("relinked item = %+v, want healthy matching names", relinked)
	}

	extra := filepath.Join(newRemote, "downloaded-by-qbittorrent.mkv")
	if err := os.WriteFile(extra, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, refreshed, err := manager.Remove(context.Background(), relinked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removed.RemovedFiles != 2 || removed.RemainingFiles != 1 {
		t.Fatalf("remove result = %+v, want two removed and one extra remaining", removed)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatalf("unrelated remote file was removed: %v", err)
	}
	if len(refreshed.Items) != 1 || refreshed.Items[0].MediaType != "tv" {
		t.Fatalf("refreshed items after remove = %+v, want only TV hardlink", refreshed.Items)
	}
}
