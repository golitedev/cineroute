package copies

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cineroute/internal/library"
)

func TestHardlinkPreservesMovieAndTVTrees(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		mediaDir  string
		remoteDir string
		files     []string
	}{
		{
			name:      "movie",
			kind:      "movie",
			mediaDir:  "movies",
			remoteDir: "movies-remote",
			files:     []string{"Movie.2024.2160p.mkv", "Movie.2024.en.srt"},
		},
		{
			name:      "TV pack",
			kind:      "tv",
			mediaDir:  "tv",
			remoteDir: "tv-remote",
			files:     []string{"Show.S01/Show.S01E01.mkv", "Show.S01/Show.S01E02.mkv"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			mainRoot := filepath.Join(base, tt.mediaDir)
			remoteRoot := filepath.Join(base, tt.remoteDir)
			folderName := "Show (2024)"
			source := filepath.Join(mainRoot, folderName)
			for _, relative := range tt.files {
				path := filepath.Join(source, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(relative), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(remoteRoot, 0o755); err != nil {
				t.Fatal(err)
			}

			drive := library.Drive{ID: "hdd1"}
			if tt.kind == "tv" {
				drive.TVRoot = mainRoot
				drive.TVRemoteRoot = remoteRoot
			} else {
				drive.MovieRoot = mainRoot
				drive.MovieRemoteRoot = remoteRoot
			}
			manager := NewManager(library.NewScan([]library.Drive{drive}), tt.kind)
			if err := manager.Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			id := manager.View("").Items[0].ID
			result, err := manager.Hardlink(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if result.LinkedFiles != len(tt.files) || result.ExistingFiles != 0 {
				t.Fatalf("hardlink result = %+v, want %d new files", result, len(tt.files))
			}
			for _, relative := range tt.files {
				sourceInfo, err := os.Stat(filepath.Join(source, filepath.FromSlash(relative)))
				if err != nil {
					t.Fatal(err)
				}
				remoteInfo, err := os.Stat(filepath.Join(remoteRoot, folderName, filepath.FromSlash(relative)))
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(sourceInfo, remoteInfo) {
					t.Fatalf("%s was copied instead of hardlinked", relative)
				}
			}

			retry, err := manager.Hardlink(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if retry.LinkedFiles != 0 || retry.ExistingFiles != len(tt.files) {
				t.Fatalf("idempotent hardlink result = %+v", retry)
			}
		})
	}
}

func TestHardlinkRemoteOnlyMovieIntoPrimaryFolder(t *testing.T) {
	base := t.TempDir()
	mainRoot := filepath.Join(base, "movies")
	remoteRoot := filepath.Join(base, "movies-remote")
	folderName := "Remote Only (2024)"
	remoteFolder := filepath.Join(remoteRoot, folderName)
	remoteFile := filepath.Join(remoteFolder, "Remote.Only.2024.1080p.WEB-DL.mkv")
	if err := os.MkdirAll(remoteFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteFile, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainFolder := filepath.Join(mainRoot, folderName)
	manager := NewManager(library.NewScan([]library.Drive{{ID: "hdd1", MovieRoot: mainRoot, MovieRemoteRoot: remoteRoot}}), "movie")
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := manager.View("").Items[0].ID
	result, err := manager.Hardlink(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourcePath != remoteFolder || result.DestinationPath != mainFolder || result.LinkedFiles != 1 {
		t.Fatalf("reverse hardlink result = %+v", result)
	}
	mainInfo, err := os.Stat(filepath.Join(mainFolder, filepath.Base(remoteFile)))
	if err != nil {
		t.Fatal(err)
	}
	remoteInfo, err := os.Stat(remoteFile)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(mainInfo, remoteInfo) {
		t.Fatal("remote-only movie was copied instead of hardlinked")
	}
}

func TestHardlinkRejectsDestinationConflictBeforeCreatingFiles(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "movies", "Movie (2024)")
	destination := filepath.Join(base, "movies-remote", "Movie (2024)")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a-conflict.mkv"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "z-new.srt"), []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "a-conflict.mkv"), []byte("different"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := hardlinkTree(source, destination)
	if err == nil || !strings.Contains(err.Error(), "different file") {
		t.Fatalf("hardlink conflict error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "z-new.srt")); !os.IsNotExist(err) {
		t.Fatalf("preflight conflict created another destination file: %v", err)
	}
}
