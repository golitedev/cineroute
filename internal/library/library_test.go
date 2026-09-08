package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMovieFolderUsesFinalYearSuffix(t *testing.T) {
	tests := []struct {
		name  string
		title string
		year  int
	}{
		{"Dune (2021)", "Dune", 2021},
		{"Blade Runner 2049 (2017)", "Blade Runner 2049", 2017},
		{"2001 A Space Odyssey (1968)", "2001 A Space Odyssey", 1968},
		{"1917 (2019)", "1917", 2019},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, year, ok := ParseMovieFolder(tt.name)
			if !ok || title != tt.title || year != tt.year {
				t.Fatalf("ParseMovieFolder(%q) = %q, %d, %v", tt.name, title, year, ok)
			}
		})
	}
}

func TestParseMovieFolderRejectsNonCanonicalNames(t *testing.T) {
	for _, name := range []string{"random", "Movie 2021", "Movie (abcd)", "(2021)"} {
		if title, year, ok := ParseMovieFolder(name); ok {
			t.Fatalf("ParseMovieFolder(%q) = %q, %d, true", name, title, year)
		}
	}
}

func TestMoviesFailsWhenConfiguredRootCannotBeRead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "movie-root")
	if err := os.WriteFile(root, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewScan([]Drive{{ID: "hdd3", MovieRoot: root}}).Movies()
	if err == nil || !strings.Contains(err.Error(), "hdd3") {
		t.Fatalf("expected root-read error, got %v", err)
	}
}

func TestTVShowsScanOnlyPrimaryRoots(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "t1")
	remote := filepath.Join(base, "tr1")
	if err := os.MkdirAll(filepath.Join(primary, "Breaking Bad (2008)"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(remote, "Remote Only (2024)"), 0o755); err != nil {
		t.Fatal(err)
	}

	shows, err := NewScan([]Drive{{ID: "hdd1", TVRoot: primary, TVRemoteRoot: remote}}).TVShows()
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 1 || shows[0].Name != "Breaking Bad (2008)" || shows[0].Path != filepath.Join(primary, "Breaking Bad (2008)") {
		t.Fatalf("TV shows = %+v, want only the primary-root show", shows)
	}
}

func TestCompanionFoldersIncludeRemoteOnlyMoviesAndTVShows(t *testing.T) {
	base := t.TempDir()
	movies := filepath.Join(base, "movies")
	moviesRemote := filepath.Join(base, "movies-remote")
	tv := filepath.Join(base, "tv")
	tvRemote := filepath.Join(base, "tv-remote")
	for _, path := range []string{
		filepath.Join(movies, "Primary Movie (2020)"),
		filepath.Join(moviesRemote, "Remote Movie (2021)"),
		filepath.Join(tv, "Primary Show (2022)"),
		filepath.Join(tvRemote, "Remote Show (2023)"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scan := NewScan([]Drive{{ID: "hdd1", MovieRoot: movies, MovieRemoteRoot: moviesRemote, TVRoot: tv, TVRemoteRoot: tvRemote}})

	movieFolders, err := scan.CompanionMovies()
	if err != nil {
		t.Fatal(err)
	}
	if len(movieFolders) != 2 || movieFolders[1].Name != "Remote Movie (2021)" || movieFolders[1].Path != filepath.Join(movies, "Remote Movie (2021)") {
		t.Fatalf("companion movies = %+v", movieFolders)
	}
	showFolders, err := scan.CompanionTVShows()
	if err != nil {
		t.Fatal(err)
	}
	if len(showFolders) != 2 || showFolders[1].Name != "Remote Show (2023)" || showFolders[1].Path != filepath.Join(tv, "Remote Show (2023)") {
		t.Fatalf("companion TV shows = %+v", showFolders)
	}
}

func TestRemotePathsInferMountedConventionalRoots(t *testing.T) {
	base := t.TempDir()
	mainRoot := filepath.Join(base, "m1")
	remoteRoot := filepath.Join(base, "mr1")
	tvRoot := filepath.Join(base, "t1")
	tvRemoteRoot := filepath.Join(base, "tr1")
	for _, root := range []string{remoteRoot, tvRemoteRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scan := NewScan([]Drive{{ID: "hdd1", MovieRoot: mainRoot, TVRoot: tvRoot}})

	moviePath, ok := scan.MovieRemotePath("hdd1", "Movie (2024)")
	if !ok || moviePath != filepath.Join(remoteRoot, "Movie (2024)") {
		t.Fatalf("movie remote path = %q, %v", moviePath, ok)
	}
	tvPath, ok := scan.TVRemotePath("hdd1", "Show (2024)")
	if !ok || tvPath != filepath.Join(tvRemoteRoot, "Show (2024)") {
		t.Fatalf("TV remote path = %q, %v", tvPath, ok)
	}
}
