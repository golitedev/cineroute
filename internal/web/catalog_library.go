package web

import (
	"os"
	"path/filepath"

	"cineroute/internal/library"
	"cineroute/internal/requests"
	"cineroute/internal/tmdb"
)

// A snapshot contains presence by normalized library folder name, not TMDB IDs,
// so uncached searches and alternate result pages never cause filesystem reads.
type catalogLibrary map[string]bool

func catalogLibraryKey(kind, folder string) string { return kind + "\x00" + library.Normalize(folder) }

func (s *Server) scanCatalogLibrary() catalogLibrary {
	out := catalogLibrary{}
	downloads, err := s.requests.Downloads()
	if err != nil {
		return out
	}
	excluded, sizes := requests.DownloadFileIndex(downloads)
	seen := map[string]bool{}
	for _, drive := range s.lib.Drives() {
		movieRemote, _ := s.lib.MovieRemotePath(drive.ID, "")
		tvRemote, _ := s.lib.TVRemotePath(drive.ID, "")
		for _, tree := range []struct{ kind, root string }{
			{"movie", drive.MovieRoot}, {"movie", movieRemote}, {"tv", drive.TVRoot}, {"tv", tvRemote},
		} {
			if tree.root == "" || seen[tree.kind+"\x00"+tree.root] {
				continue
			}
			seen[tree.kind+"\x00"+tree.root] = true
			entries, err := os.ReadDir(tree.root)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				folder := filepath.Join(tree.root, entry.Name())
				files, err := library.WalkVideoFiles(folder)
				if err != nil {
					continue
				}
				for _, file := range files {
					path := filepath.Join(folder, file)
					if excluded[path] || requests.IsExtra(file) {
						continue
					}
					if tree.kind == "tv" && len(requests.EpisodeKeys(file)) == 0 {
						continue
					}
					info, err := os.Stat(path)
					if err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() >= sizes[path] {
						out[catalogLibraryKey(tree.kind, entry.Name())] = true
						break
					}
				}
			}
		}
	}
	return out
}

func (s *Server) catalogInLibrary(media tmdb.Result) bool {
	index := s.catalogLibrary.Get()
	folder := library.FolderName(s.cfg.Library.FolderFormat, media.DisplayTitle(), media.Year())
	if index[catalogLibraryKey(media.MediaType, folder)] {
		return true
	}
	return media.MediaType == "tv" && index[catalogLibraryKey("tv", library.SanitizeTitle(media.DisplayTitle()))]
}
