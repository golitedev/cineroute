// Package library scans the immediate children of movie/TV roots and matches
// canonical "Title (Year)" folders.
package library

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Drive describes one physical drive with its movie and TV roots.
type Drive struct {
	ID        string
	MovieRoot string
	TVRoot    string
}

type Folder struct {
	DriveID string
	Path    string
}

// Scan holds the roots to search. ReadDir is called per lookup so results are
// always fresh; there is no cached index.
type Scan struct {
	drives []Drive
}

func NewScan(drives []Drive) *Scan {
	return &Scan{drives: drives}
}

func (s *Scan) FindMovie(canonical string) []Folder {
	return s.find(canonical, func(d Drive) string { return d.MovieRoot })
}

func (s *Scan) FindTV(canonical string) []Folder {
	return s.find(canonical, func(d Drive) string { return d.TVRoot })
}

func (s *Scan) find(canonical string, rootOf func(Drive) string) []Folder {
	var out []Folder
	want := Normalize(canonical)
	for _, d := range s.drives {
		root := rootOf(d)
		if root == "" {
			continue
		}
		ents, err := os.ReadDir(root)
		if err != nil {
			continue // unhealthy root; drive status is reported elsewhere
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			if Normalize(e.Name()) == want {
				out = append(out, Folder{DriveID: d.ID, Path: filepath.Join(root, e.Name())})
			}
		}
	}
	return out
}

var wsRe = regexp.MustCompile(`\s+`)

// Normalize canonicalizes a folder name for comparison: Unicode lowercased,
// whitespace collapsed, apostrophes unified, colons removed, dots removed at
// the end.
func Normalize(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '\u2019' || r == '\u2018' || r == '`' {
			r = '\''
		}
		if r == ':' {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	s := strings.TrimSpace(b.String())
	s = wsRe.ReplaceAllString(s, " ")
	s = strings.TrimRight(s, ".")
	return s
}

// FolderName builds the canonical parent folder, e.g. "Lost (2004)".
func FolderName(format, title string, year int) string {
	title = SanitizeTitle(title)
	if year > 0 {
		return strings.ReplaceAll(strings.ReplaceAll(format, "{title}", title), "{year}", strconv.Itoa(year))
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(format, "{title}", title), "({year})", ""))
}

// SanitizeTitle makes a title safe as a single Linux path component. It only
// removes characters that cannot appear in a path.
func SanitizeTitle(title string) string {
	title = strings.ReplaceAll(title, "/", "-")
	title = strings.ReplaceAll(title, ":", "")
	title = strings.ReplaceAll(title, "\x00", "")
	title = strings.TrimSpace(title)
	title = wsRe.ReplaceAllString(title, " ")
	if title == "." || title == ".." {
		return ""
	}
	return title
}
