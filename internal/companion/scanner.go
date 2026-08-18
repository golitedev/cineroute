package companion

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cineroute/internal/library"
)

var companionVideoExtensions = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".m4v":  true,
	".avi":  true,
	".webm": true,
}

type copyInspection struct {
	Quality         string
	Has1080pWebDL   bool
	Has1080pBluRay  bool
	JellyfinWarning string
	Error           string
}

func inspectMovieFolder(path, folderName string) copyInspection {
	videos, err := movieVideoFiles(path)
	if err != nil {
		return copyInspection{Error: fmt.Sprintf("cannot inspect movie folder: %v", err)}
	}
	if len(videos) == 0 {
		return copyInspection{Quality: "none", Error: "no video file found in movie folder; manual review required"}
	}
	var first1080p string
	inspection := copyInspection{}
	for _, name := range videos {
		lower := strings.ToLower(name)
		if !strings.Contains(lower, "1080p") {
			continue
		}
		if first1080p == "" {
			first1080p = name
		}
		if webDLRe.MatchString(name) {
			inspection.Has1080pWebDL = true
		}
		if isBluRayLike(name) {
			inspection.Has1080pBluRay = true
		}
	}
	if first1080p != "" {
		inspection.Quality = "1080p"
		inspection.JellyfinWarning = jellyfinWarning(folderName, filepath.Base(first1080p))
		return inspection
	}
	if len(videos) > 1 {
		return copyInspection{
			Quality: "multiple",
			Error:   "multiple video files found without a clear 1080p copy; manual review required",
		}
	}
	name := videos[0]
	quality := "unknown"
	lower := strings.ToLower(name)
	if strings.Contains(lower, "2160p") || strings.Contains(lower, "uhd") ||
		strings.Contains(lower, "4k") || strings.Contains(lower, "remux") {
		quality = "4k"
	}
	return copyInspection{
		Quality:         quality,
		JellyfinWarning: jellyfinWarning(folderName, filepath.Base(name)),
	}
}

// movieVideoFiles includes files kept inside an original torrent directory.
// It reads metadata only and never follows nested directory symlinks.
func movieVideoFiles(root string) ([]string, error) {
	var videos []string
	var walk func(string, string) error
	walk = func(path, relative string) error {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			relativePath := filepath.Join(relative, entry.Name())
			if entry.IsDir() {
				if err := walk(filepath.Join(path, entry.Name()), relativePath); err != nil {
					return err
				}
				continue
			}
			if companionVideoExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
				videos = append(videos, relativePath)
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return videos, nil
}

func isBluRayLike(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "remux") || strings.Contains(lower, "blu-ray") ||
		hasAny(lower, "bluray", "brrip", "bdrip")
}

// needsWebDLCompanion identifies a 1080p BluRay/remux that can still be
// upgraded with a WEB-DL companion.
func needsWebDLCompanion(inspection copyInspection) bool {
	return inspection.Quality == "1080p" && inspection.Has1080pBluRay && !inspection.Has1080pWebDL
}

// alreadyHasSuitable1080pCopy preserves the existing behavior for ordinary
// 1080p files. A WEB-DL always satisfies the target regardless of any other
// 1080p copy in the folder.
func alreadyHasSuitable1080pCopy(inspection copyInspection) bool {
	if inspection.Quality != "1080p" {
		return false
	}
	return !needsWebDLCompanion(inspection)
}

func jellyfinWarning(folderName, videoName string) string {
	if strings.HasPrefix(videoName, folderName+" -") {
		return ""
	}
	return "Original torrent filename preserved; Jellyfin may not automatically group this as another version"
}

func inspectError(movie *Movie, inspection copyInspection) {
	movie.ExistingCopy = inspection.Quality
	movie.JellyfinWarning = inspection.JellyfinWarning
	if inspection.Error != "" {
		movie.Status = StatusNeedsReview
		movie.Error = inspection.Error
	}
}

func parseMovieFolder(folder library.MovieFolder) (title string, year int, err error) {
	title, year, ok := library.ParseMovieFolder(folder.Name)
	if !ok {
		return "", 0, fmt.Errorf("folder name is not canonical; manual review required")
	}
	return title, year, nil
}
