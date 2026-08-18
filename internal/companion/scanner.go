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
	JellyfinWarning string
	Error           string
}

func inspectMovieFolder(path, folderName string) copyInspection {
	entries, err := os.ReadDir(path)
	if err != nil {
		return copyInspection{Error: fmt.Sprintf("cannot inspect movie folder: %v", err)}
	}
	var videos []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if companionVideoExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			videos = append(videos, entry.Name())
		}
	}
	if len(videos) == 0 {
		return copyInspection{Quality: "none", Error: "no top-level video file found; manual review required"}
	}
	for _, name := range videos {
		if strings.Contains(strings.ToLower(name), "1080p") {
			return copyInspection{
				Quality:         "1080p",
				JellyfinWarning: jellyfinWarning(folderName, name),
			}
		}
	}
	if len(videos) > 1 {
		return copyInspection{
			Quality: "multiple",
			Error:   "multiple top-level video files found without a clear 1080p copy; manual review required",
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
		JellyfinWarning: jellyfinWarning(folderName, name),
	}
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
