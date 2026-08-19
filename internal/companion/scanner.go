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
	Files           []string
	Quality         string
	Has1080pWebDL   bool
	Has1080pBluRay  bool
	JellyfinWarning string
	Error           string
}

// inspectRemoteMovieFolder inspects the optional sibling folder used for a
// second copy. A missing folder is expected: it is created when a companion
// torrent is approved. An existing but empty remote folder is also valid.
func inspectRemoteMovieFolder(path, folderName string) copyInspection {
	if path == "" {
		return copyInspection{}
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return copyInspection{}
		}
		return copyInspection{Error: fmt.Sprintf("cannot inspect remote movie folder: %v", err)}
	}
	inspection := inspectMovieFolder(path, folderName)
	if inspection.Quality == "none" && inspection.Error != "" {
		// A remote folder is a destination for a copy, so it is valid for it
		// to exist before its first video is added.
		inspection.Error = ""
	}
	return inspection
}

func inspectMovieCopies(path, remotePath, folderName string) (copyInspection, copyInspection) {
	return inspectMovieFolder(path, folderName), inspectRemoteMovieFolder(remotePath, folderName)
}

func hasSuitableMovieCopy(main, remote copyInspection) bool {
	return alreadyHasSuitable1080pCopy(main) || alreadyHasSuitable1080pCopy(remote)
}

func movieInspectionError(main, remote copyInspection) string {
	if main.Error != "" {
		return main.Error
	}
	return remote.Error
}

func movieInspectionWarnings(main, remote copyInspection) string {
	if main.JellyfinWarning == "" {
		if remote.JellyfinWarning == "" {
			return ""
		}
		return "Remote copy: " + remote.JellyfinWarning
	}
	if remote.JellyfinWarning == "" {
		return main.JellyfinWarning
	}
	return main.JellyfinWarning + "; Remote copy: " + remote.JellyfinWarning
}

func updateMovieInspection(movie *Movie, path, remotePath, folderName string) (copyInspection, copyInspection) {
	main, remote := inspectMovieCopies(path, remotePath, folderName)
	movie.RemotePath = remotePath
	movie.ExistingCopy = main.Quality
	movie.ExistingFiles = movieVideoPaths(path, main.Files)
	movie.ExistingFileSizes = movieVideoSizes(path, main.Files)
	movie.RemoteCopy = remote.Quality
	movie.RemoteFiles = movieVideoPaths(remotePath, remote.Files)
	movie.RemoteFileSizes = movieVideoSizes(remotePath, remote.Files)
	movie.JellyfinWarning = movieInspectionWarnings(main, remote)
	return main, remote
}

func inspectMovieFolder(path, folderName string) copyInspection {
	videos, err := movieVideoFiles(path)
	if err != nil {
		return copyInspection{Error: fmt.Sprintf("cannot inspect movie folder: %v", err)}
	}
	inspection := copyInspection{Files: append([]string(nil), videos...)}
	if len(videos) == 0 {
		inspection.Quality = "none"
		inspection.Error = "no video file found in movie folder; manual review required"
		return inspection
	}
	var first1080p string
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
		inspection.Quality = "multiple"
		inspection.Error = "multiple video files found without a clear 1080p copy; manual review required"
		return inspection
	}
	name := videos[0]
	quality := "unknown"
	lower := strings.ToLower(name)
	if strings.Contains(lower, "2160p") || strings.Contains(lower, "uhd") ||
		strings.Contains(lower, "4k") || strings.Contains(lower, "remux") {
		quality = "4k"
	}
	return copyInspection{
		Files:           inspection.Files,
		Quality:         quality,
		JellyfinWarning: jellyfinWarning(folderName, filepath.Base(name)),
	}
}

func movieVideoPaths(root string, relative []string) []string {
	paths := make([]string, 0, len(relative))
	for _, name := range relative {
		paths = append(paths, filepath.Join(root, name))
	}
	return paths
}

func movieVideoSizes(root string, relative []string) map[string]int64 {
	sizes := make(map[string]int64, len(relative))
	for _, name := range relative {
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sizes[path] = info.Size()
	}
	if len(sizes) == 0 {
		return nil
	}
	return sizes
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
	movie.ExistingFiles = movieVideoPaths(movie.Path, inspection.Files)
	movie.ExistingFileSizes = movieVideoSizes(movie.Path, inspection.Files)
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
