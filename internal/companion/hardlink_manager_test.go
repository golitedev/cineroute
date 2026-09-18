package companion

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cineroute/internal/library"
)

func TestHardlinkManagerRelinksRenamedMovieAndRemoveKeepsUnrelatedFiles(t *testing.T) {
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

// TestHardlinkManagerRelinkMirrorsReplacedSubtitles covers the renamed external
// subtitle workflow: wrong-named subs were linked to the remote tree, then the
// primary library replaced them with correctly named files. Relink must add the
// new links and drop every remote file the primary folder no longer has,
// including stale links whose inode no longer exists in the primary tree and
// unrelated extras.
func TestHardlinkManagerRelinkMirrorsReplacedSubtitles(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "tv")
	remote := filepath.Join(base, "tv-remote")
	for _, dir := range []string{primary, remote} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	source := filepath.Join(primary, "Example Show (2021)")
	remoteFolder := filepath.Join(remote, "Example Show (2021)")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	episode := filepath.Join(source, "Example.Show.S01E01.mkv")
	if err := os.WriteFile(episode, []byte("episode"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrongSub := filepath.Join(source, "Example.Show.S01E01.wrong-name.srt")
	if err := os.WriteFile(wrongSub, []byte("wrong sub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hardlinkTree(source, remoteFolder); err != nil {
		t.Fatal(err)
	}

	// Replace the wrongly named subtitle with a correctly named one and add an
	// unrelated remote-only file that must not survive the relink.
	if err := os.Remove(wrongSub); err != nil {
		t.Fatal(err)
	}
	rightSub := filepath.Join(source, "Example.Show.S01E01.right-name.srt")
	if err := os.WriteFile(rightSub, []byte("right sub"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(remoteFolder, "stray-download.nfo")
	if err := os.WriteFile(extra, []byte("not in primary"), 0o644); err != nil {
		t.Fatal(err)
	}

	scan := library.NewScan([]library.Drive{{
		ID:           "hdd3",
		TVRoot:       primary,
		TVRemoteRoot: remote,
	}})
	manager := NewHardlinkManager(scan)
	view, err := manager.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Stats.Total != 1 || len(view.Items) != 1 {
		t.Fatalf("stats = %+v, want one hardlink item", view.Stats)
	}
	item := view.Items[0]
	if item.Status != "partial" || item.SourceFileCount != 2 || item.LinkedFiles != 1 || item.ExtraFileCount != 2 {
		t.Fatalf("item = %+v, want partial with one linked and two extra files", item)
	}

	result, refreshed, err := manager.Relink(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.LinkedFiles != 1 || result.RemovedDuplicateFiles != 2 {
		t.Fatalf("relink result = %+v, want one new link and two removed stale files", result)
	}

	staleRemoteSub := filepath.Join(remoteFolder, "Example.Show.S01E01.wrong-name.srt")
	if _, err := os.Lstat(staleRemoteSub); !os.IsNotExist(err) {
		t.Fatalf("stale remote subtitle still exists: %v", err)
	}
	if _, err := os.Lstat(extra); !os.IsNotExist(err) {
		t.Fatalf("remote-only extra file still exists: %v", err)
	}
	linkedRemoteSub := filepath.Join(remoteFolder, "Example.Show.S01E01.right-name.srt")
	remoteInfo, err := os.Lstat(linkedRemoteSub)
	if err != nil {
		t.Fatalf("relinked remote subtitle is missing: %v", err)
	}
	sourceInfo, err := os.Lstat(rightSub)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sourceInfo, remoteInfo) {
		t.Fatalf("remote subtitle is not hardlinked to the primary subtitle")
	}

	if len(refreshed.Items) != 1 {
		t.Fatalf("refreshed items = %d, want 1", len(refreshed.Items))
	}
	relinked := refreshed.Items[0]
	if relinked.Status != "healthy" || relinked.RemoteFileCount != 2 || relinked.ExtraFileCount != 0 {
		t.Fatalf("relinked item = %+v, want healthy with exactly the two primary files", relinked)
	}
}

// TestHardlinkManagerRelinkReplacesSameNameConflict covers a primary file that
// was replaced while keeping its old name: the stale remote link must be
// replaced with a link to the new primary file instead of failing the relink.
func TestHardlinkManagerRelinkReplacesSameNameConflict(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "movies")
	remote := filepath.Join(base, "movies-remote")
	for _, dir := range []string{primary, remote} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	source := filepath.Join(primary, "Example Movie (2022)")
	remoteFolder := filepath.Join(remote, "Example Movie (2022)")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	movie := filepath.Join(source, "movie.mkv")
	if err := os.WriteFile(movie, []byte("movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(source, "movie.en.srt")
	if err := os.WriteFile(sub, []byte("old subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hardlinkTree(source, remoteFolder); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("new subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}

	scan := library.NewScan([]library.Drive{{
		ID:              "hdd1",
		MovieRoot:       primary,
		MovieRemoteRoot: remote,
	}})
	manager := NewHardlinkManager(scan)
	view, err := manager.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Stats.Total != 1 || len(view.Items) != 1 {
		t.Fatalf("stats = %+v, want one hardlink item", view.Stats)
	}

	result, _, err := manager.Relink(context.Background(), view.Items[0].ID)
	if err != nil {
		t.Fatalf("relink: %v", err)
	}
	if result.LinkedFiles != 1 || result.RemovedDuplicateFiles != 1 {
		t.Fatalf("relink result = %+v, want one replacement link and one removed stale file", result)
	}
	remoteInfo, err := os.Lstat(filepath.Join(remoteFolder, "movie.en.srt"))
	if err != nil {
		t.Fatalf("remote subtitle is missing: %v", err)
	}
	sourceInfo, err := os.Lstat(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sourceInfo, remoteInfo) {
		t.Fatalf("remote subtitle was not replaced with a link to the new primary file")
	}
}
