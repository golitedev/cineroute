package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cineroute/internal/config"
	"cineroute/internal/library"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

func TestCompanionSubmissionUsesMovieRemoteFolder(t *testing.T) {
	srv, fake, _, roots := newTestServer(t)
	mainFolder := filepath.Join(roots["/m3"], "Apollo 13 (1995)")
	remoteRoot := filepath.Join(t.TempDir(), "movies-remote")
	if err := os.MkdirAll(mainFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	srv.cfg.Drives = []config.Drive{{ID: "hdd3", MovieRoot: roots["/m3"], MovieRemoteRoot: remoteRoot, TVRoot: roots["/t3"]}}
	srv.lib = library.NewScan([]library.Drive{{ID: "hdd3", MovieRoot: roots["/m3"], MovieRemoteRoot: remoteRoot, TVRoot: roots["/t3"]}})

	raw := singleFileTorrent("Apollo.13.1995.1080p.WEB-DL.mkv", 100)
	meta, err := torrentmeta.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := srv.submitTorrent(context.Background(), submissionRequest{
		Bytes: raw, Filename: "companion.torrent", Meta: meta, MediaType: "movie",
		Match:           tmdb.Result{ID: 568, Title: "Apollo 13", ReleaseDate: "1995-01-01"},
		RequireExisting: true, UseMovieRemoteRoot: true,
	})
	if err != nil {
		t.Fatalf("remote companion submission failed: %v", err)
	}
	want := filepath.Join(remoteRoot, "Apollo 13 (1995)")
	if out == nil || out.Dest == nil || out.Dest.SavePath != want || out.Dest.DriveID != "hdd3" {
		t.Fatalf("companion destination = %+v, want %s on hdd3", out, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("remote companion folder was not created: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.added) != 1 || fake.added[0].savepath != want {
		t.Fatalf("qBittorrent save path = %+v, want %s", fake.added, want)
	}
}

func TestCompanionSubmissionRequiresExistingMovieFolder(t *testing.T) {
	srv, fake, _, roots := newTestServer(t)
	existing := filepath.Join(roots["/m3"], "Toy Story (1995)")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}

	raw := singleFileTorrent("Toy.Story.1995.1080p.WEB-DL.mkv", 100)
	meta, err := torrentmeta.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := srv.submitTorrent(context.Background(), submissionRequest{
		Bytes: raw, Filename: "companion.torrent", Meta: meta, MediaType: "movie",
		Match:              tmdb.Result{ID: 862, Title: "Toy Story", ReleaseDate: "1995-01-01"},
		RequireExisting:    true,
		UseMovieRemoteRoot: true,
	})
	if err != nil {
		t.Fatalf("companion submission failed: %v", err)
	}
	if out == nil || out.Dest == nil || out.Dest.SavePath != existing || out.Dest.DriveID != "hdd3" {
		t.Fatalf("companion left its existing drive: %+v", out)
	}

	missingRaw := singleFileTorrent("No.Folder.2020.1080p.WEB-DL.mkv", 100)
	missingMeta, err := torrentmeta.Parse(missingRaw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.submitTorrent(context.Background(), submissionRequest{
		Bytes: missingRaw, Filename: "companion-missing.torrent", Meta: missingMeta, MediaType: "movie",
		Match:              tmdb.Result{ID: 999, Title: "No Folder", ReleaseDate: "2020-01-01"},
		RequireExisting:    true,
		UseMovieRemoteRoot: true,
	})
	if err == nil || !strings.Contains(err.Error(), "folder no longer exists") {
		t.Fatalf("expected missing-folder rejection, got %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.added) != 1 {
		t.Fatalf("missing companion must not be added, got %d adds", len(fake.added))
	}
}
