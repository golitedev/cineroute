package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const stateVersion = 1

const (
	StatusPending      = "pending"
	StatusSearching    = "searching"
	StatusReview       = "review"
	StatusNoMatch      = "no_match"
	StatusAlready1080p = "already_has_1080p"
	StatusNeedsReview  = "needs_review"
	StatusSkipped      = "skipped"
	StatusSubmitting   = "submitting"
	StatusComplete     = "complete"
	StatusError        = "error"
)

func isLiveWorkflowStatus(status string) bool {
	switch status {
	case StatusSearching, StatusReview, StatusSubmitting:
		return true
	default:
		return false
	}
}

// Movie is the durable workflow record for one canonical companion folder.
// Search results live in the companion store separately: Prowlarr download
// URLs can contain the API key and are short-lived, so they are never written.
type Movie struct {
	ID                string           `json:"id"`
	DriveID           string           `json:"drive_id"`
	Path              string           `json:"path"`
	RemotePath        string           `json:"remote_path,omitempty"`
	FolderName        string           `json:"folder_name"`
	Title             string           `json:"title"`
	Year              int              `json:"year"`
	TmdbID            int              `json:"tmdb_id"`
	Status            string           `json:"status"`
	Error             string           `json:"error,omitempty"`
	QBHash            string           `json:"qb_hash,omitempty"`
	ExistingCopy      string           `json:"existing_copy,omitempty"`
	ExistingFiles     []string         `json:"existing_files,omitempty"`
	ExistingFileSizes map[string]int64 `json:"existing_file_sizes,omitempty"`
	RemoteCopy        string           `json:"remote_copy,omitempty"`
	RemoteFiles       []string         `json:"remote_files,omitempty"`
	RemoteFileSizes   map[string]int64 `json:"remote_file_sizes,omitempty"`
	TVApprovedPacks   []string         `json:"tv_approved_packs,omitempty"`
	JellyfinWarning   string           `json:"jellyfin_warning,omitempty"`
	Missing           bool             `json:"missing,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	AddedAt           *time.Time       `json:"added_at,omitempty"`
}

type stateFile struct {
	Version               int      `json:"version"`
	Movies                []*Movie `json:"movies"`
	SearchIntervalSeconds int      `json:"search_interval_seconds,omitempty"`
	SearchBatchSize       int      `json:"search_batch_size,omitempty"`
}

type BatchStatus struct {
	Running  bool   `json:"running"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Canceled bool   `json:"canceled,omitempty"`
	Error    string `json:"error,omitempty"`
}

func movieID(driveID, folderName string) string {
	h := sha256.Sum256([]byte(driveID + "\x00" + folderName))
	return "c_" + hex.EncodeToString(h[:])[:16]
}

func tvCompanionStatePath(configuredPath string) string {
	ext := filepath.Ext(configuredPath)
	if strings.EqualFold(ext, ".json") || strings.EqualFold(ext, ".db") {
		return strings.TrimSuffix(configuredPath, ext) + "-tv" + ext
	}
	return configuredPath + "-tv"
}

func loadState(path string) (stateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return stateFile{Version: stateVersion}, nil
		}
		return stateFile{}, fmt.Errorf("read companion state: %w", err)
	}
	var st stateFile
	if err := json.Unmarshal(data, &st); err != nil {
		return stateFile{}, fmt.Errorf("parse companion state: %w", err)
	}
	if st.Version != 0 && st.Version != stateVersion {
		return stateFile{}, fmt.Errorf("unsupported companion state version %d", st.Version)
	}
	if st.Version == 0 {
		st.Version = stateVersion
	}
	for _, movie := range st.Movies {
		if movie == nil {
			return stateFile{}, fmt.Errorf("companion state contains a null movie")
		}
		if movie.ID == "" {
			return stateFile{}, fmt.Errorf("companion state contains a movie without an id")
		}
	}
	sort.SliceStable(st.Movies, func(i, j int) bool {
		return st.Movies[i].ID < st.Movies[j].ID
	})
	return st, nil
}

// normalizeLoadedState repairs interrupted workflow states after a restart.
// A review record is retained when its candidates were stored successfully.
// A submitting record needs an explicit error because qBittorrent may have
// accepted it before CineRoute stopped.
func normalizeLoadedState(st *stateFile, searches map[string]searchState) bool {
	changed := false
	for _, movie := range st.Movies {
		switch movie.Status {
		case StatusSearching:
			movie.Status = StatusPending
			movie.Error = ""
			delete(searches, movie.ID)
			changed = true
		case StatusReview:
			if _, ok := searches[movie.ID]; !ok {
				movie.Status = StatusPending
				movie.Error = ""
				changed = true
			}
		case StatusSubmitting:
			movie.Status = StatusError
			movie.Error = "previous companion submission was interrupted; search and approve again after checking qBittorrent"
			changed = true
		case StatusNeedsReview, StatusError:
			if legacyTMDBError(movie.Error) {
				movie.Status = StatusPending
				movie.Error = ""
				changed = true
			}
		}
	}
	return changed
}

func legacyTMDBError(message string) bool {
	return strings.Contains(strings.ToLower(message), "tmdb")
}

func saveState(path string, st stateFile) error {
	if path == "" {
		return fmt.Errorf("companion state path is empty")
	}
	st.Version = stateVersion
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create companion state directory: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode companion state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".companions-*.tmp")
	if err != nil {
		return fmt.Errorf("create companion state temporary file: %w", err)
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write companion state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync companion state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close companion state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace companion state: %w", err)
	}
	removeTemp = false
	return nil
}

func cloneMovie(movie *Movie) *Movie {
	if movie == nil {
		return nil
	}
	copy := *movie
	copy.ExistingFiles = append([]string(nil), movie.ExistingFiles...)
	copy.RemoteFiles = append([]string(nil), movie.RemoteFiles...)
	copy.TVApprovedPacks = append([]string(nil), movie.TVApprovedPacks...)
	if movie.ExistingFileSizes != nil {
		copy.ExistingFileSizes = make(map[string]int64, len(movie.ExistingFileSizes))
		for path, size := range movie.ExistingFileSizes {
			copy.ExistingFileSizes[path] = size
		}
	}
	if movie.RemoteFileSizes != nil {
		copy.RemoteFileSizes = make(map[string]int64, len(movie.RemoteFileSizes))
		for path, size := range movie.RemoteFileSizes {
			copy.RemoteFileSizes[path] = size
		}
	}
	return &copy
}

func cloneMovies(movies []*Movie) []*Movie {
	out := make([]*Movie, 0, len(movies))
	for _, movie := range movies {
		out = append(out, cloneMovie(movie))
	}
	return out
}
