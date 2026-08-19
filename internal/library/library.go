// Package library scans the immediate children of movie/TV roots and matches
// canonical "Title (Year)" folders.
package library

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Drive describes one physical drive with its primary and optional remote
// movie/TV roots. Remote roots are sibling directories on the same volume and
// are used for alternate movie copies such as 1080p companion releases.
type Drive struct {
	ID              string
	MovieRoot       string
	MovieRemoteRoot string
	TVRoot          string
	TVRemoteRoot    string
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

// conventionalRemoteRoot detects the container aliases used by the example
// deployment: /m1 -> /mr1 and /t1 -> /tr1. It only infers a root when the
// corresponding directory is mounted and readable, so older deployments
// without remote mounts keep their legacy behavior.
func conventionalRemoteRoot(primaryRoot, primaryPrefix, remotePrefix string) (string, bool) {
	clean := filepath.Clean(primaryRoot)
	base := filepath.Base(clean)
	if !strings.HasPrefix(base, primaryPrefix) {
		return "", false
	}
	suffix := strings.TrimPrefix(base, primaryPrefix)
	if suffix == "" {
		return "", false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	candidate := filepath.Join(filepath.Dir(clean), remotePrefix+suffix)
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return candidate, true
}

// MovieRemotePath returns the corresponding remote movie folder for a drive
// and canonical folder name. The folder itself does not need to exist yet;
// companion approval creates it before adding the torrent.
func (s *Scan) MovieRemotePath(driveID, folderName string) (string, bool) {
	for _, d := range s.drives {
		if d.ID != driveID {
			continue
		}
		root := d.MovieRemoteRoot
		if root == "" {
			root, _ = conventionalRemoteRoot(d.MovieRoot, "m", "mr")
		}
		if root != "" {
			return filepath.Join(root, folderName), true
		}
	}
	return "", false
}

// TVRemotePath returns the corresponding remote TV folder for a drive and
// canonical folder name. It is kept alongside MovieRemotePath so drive
// configuration remains symmetrical even though the companion workflow is
// currently movie-only.
func (s *Scan) TVRemotePath(driveID, folderName string) (string, bool) {
	for _, d := range s.drives {
		if d.ID != driveID {
			continue
		}
		root := d.TVRemoteRoot
		if root == "" {
			root, _ = conventionalRemoteRoot(d.TVRoot, "t", "tr")
		}
		if root != "" {
			return filepath.Join(root, folderName), true
		}
	}
	return "", false
}

// Movies lists only immediate movie-root children in deterministic order. It
// fails if a configured movie root cannot be read so callers do not reconcile
// a partially visible library as though the missing drive had been emptied.
func (s *Scan) Movies() ([]MovieFolder, error) {
	var out []MovieFolder
	for _, d := range s.drives {
		if d.MovieRoot == "" {
			continue
		}
		ents, err := os.ReadDir(d.MovieRoot)
		if err != nil {
			return nil, fmt.Errorf("read movie root %q (%s): %w", d.MovieRoot, d.ID, err)
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
	return out, nil
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
