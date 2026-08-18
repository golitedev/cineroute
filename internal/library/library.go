// Package library scans the immediate children of movie/TV roots and matches
// canonical "Title (Year)" folders.
package library

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	Name    string
}

// MovieFolder is an immediate child of a configured movie root. It is kept
// separate from Folder so callers scanning the library can retain the folder
// name without parsing it back out of the path.
type MovieFolder = Folder

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

// Movies lists only immediate movie-root children in deterministic order.
// Unreadable roots are skipped just like FindMovie; the caller can still
// reconcile the healthy roots it can see.
func (s *Scan) Movies() []MovieFolder {
	var out []MovieFolder
	for _, d := range s.drives {
		if d.MovieRoot == "" {
			continue
		}
		ents, err := os.ReadDir(d.MovieRoot)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				out = append(out, MovieFolder{
					DriveID: d.ID,
					Path:    filepath.Join(d.MovieRoot, e.Name()),
					Name:    e.Name(),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DriveID != out[j].DriveID {
			return out[i].DriveID < out[j].DriveID
		}
		return out[i].Name < out[j].Name
	})
	return out
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
				out = append(out, Folder{DriveID: d.ID, Path: filepath.Join(root, e.Name()), Name: e.Name()})
			}
		}
	}
	return out
}

var canonicalMovieRe = regexp.MustCompile(`^(.*?)\s*\((\d{4})\)$`)

// ParseMovieFolder parses the final four-digit year suffix of a canonical
// movie folder. Numbers earlier in the title remain part of the title.
func ParseMovieFolder(name string) (title string, year int, ok bool) {
	m := canonicalMovieRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return "", 0, false
	}
	y, err := strconv.Atoi(m[2])
	if err != nil || y < 1800 || y > 2999 {
		return "", 0, false
	}
	return strings.TrimSpace(m[1]), y, true
}

var wsRe = regexp.MustCompile(`\s+`)

// Normalize canonicalizes a folder name for comparison: Unicode lowercased,
// whitespace collapsed, apostrophes unified, forbidden filename characters
// removed, dots removed at the end.
func Normalize(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '\u2019' || r == '\u2018' || r == '`' {
			r = '\''
		}
		if r == ':' || r == '/' || strings.ContainsRune("<>\"\\|?*", r) {
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
	for _, r := range "<>:\"\\|?*" {
		title = strings.ReplaceAll(title, string(r), "")
	}
	title = strings.ReplaceAll(title, "\x00", "")
	title = strings.TrimSpace(title)
	title = wsRe.ReplaceAllString(title, " ")
	if title == "." || title == ".." {
		return ""
	}
	return title
}
