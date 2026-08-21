package companion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cineroute/internal/config"
	"cineroute/internal/library"
)

func TestHardlinkPreservesMovieAndTVTrees(t *testing.T) {
	tests := []struct {
		name      string
		kind      companionKind
		mediaDir  string
		remoteDir string
		files     []string
	}{
		{
			name:      "movie",
			kind:      companionMovie,
			mediaDir:  "movies",
			remoteDir: "movies-remote",
			files:     []string{"Movie.2024.2160p.mkv", "Movie.2024.en.srt"},
		},
		{
			name:      "TV pack",
			kind:      companionTV,
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
			if tt.kind == companionTV {
				drive.TVRoot = mainRoot
				drive.TVRemoteRoot = remoteRoot
			} else {
				drive.MovieRoot = mainRoot
				drive.MovieRemoteRoot = remoteRoot
			}
			manager := &Manager{
				cfg:      config.Default(),
				lib:      library.NewScan([]library.Drive{drive}),
				kind:     tt.kind,
				state:    stateFile{Version: stateVersion, Movies: []*Movie{{ID: "item", DriveID: "hdd1", Path: source, FolderName: folderName, Status: StatusReview}}},
				searches: map[string]searchState{"item": {}},
			}

			result, err := manager.Hardlink("item")
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
			if manager.state.Movies[0].Status != StatusComplete || manager.state.Movies[0].AddedAt == nil {
				t.Fatalf("hardlinked item state = %+v", manager.state.Movies[0])
			}
			if _, ok := manager.searches["item"]; ok {
				t.Fatal("hardlink did not clear stale search candidates")
			}

			retry, err := manager.Hardlink("item")
			if err != nil {
				t.Fatal(err)
			}
			if retry.LinkedFiles != 0 || retry.ExistingFiles != len(tt.files) {
				t.Fatalf("idempotent hardlink result = %+v", retry)
			}
		})
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
