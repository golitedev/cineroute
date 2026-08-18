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
