package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type virtualFolder struct {
	ItemID    string   `json:"ItemId"`
	Locations []string `json:"Locations"`
}

// matchingLibraries selects the most specific containing root after path
// translation. Equal roots can belong to multiple libraries; refresh each one.
func matchingLibraries(folder string, libraries []virtualFolder) []string {
	best := -1
	ids := []string{}
	seen := map[string]bool{}
	for _, library := range libraries {
		for _, location := range library.Locations {
			if !filepath.IsAbs(location) {
				continue
			}
			root := filepath.Clean(location)
			rel, err := filepath.Rel(root, folder)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				continue
			}
			if len(root) > best {
				best = len(root)
				ids = nil
				clear(seen)
			}
			if len(root) == best && library.ItemID != "" && !seen[library.ItemID] {
				ids = append(ids, library.ItemID)
				seen[library.ItemID] = true
			}
		}
	}
	return ids
}

func (n *Notifier) request(ctx context.Context, method, endpoint string, body []byte, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(n.cfg.URL, "/")+endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("construct Jellyfin request")
	}
	req.Header.Set("Authorization", fmt.Sprintf("MediaBrowser Token=%q", n.cfg.APIKey))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("Jellyfin request failed (connection, timeout or cancellation)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Jellyfin returned HTTP %d", resp.StatusCode)
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result); err != nil {
			return fmt.Errorf("invalid Jellyfin library response")
		}
	} else {
		_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
	}
	return nil
}

func (n *Notifier) deliver(ctx context.Context, events []event) []error {
	results := make([]error, len(events))
	needsLibraries := false
	for _, e := range events {
		needsLibraries = needsLibraries || e.refreshLibrary
	}
	var libraries []virtualFolder
	var lookupErr error
	if needsLibraries {
		lookupErr = n.request(ctx, http.MethodGet, "/Library/VirtualFolders", nil, &libraries)
	}

	// Group refreshes by library ID across disks and acknowledge successful events
	// independently when another library fails.
	refreshes := map[string]error{}
	var subtitleIndexes []int
	seenFolders := map[string]bool{}
	var updates []update
	for i, e := range events {
		if !e.refreshLibrary {
			subtitleIndexes = append(subtitleIndexes, i)
			if !seenFolders[e.path] {
				seenFolders[e.path] = true
				updates = append(updates, update{e.path, "Modified"})
			}
			continue
		}
		if lookupErr != nil {
			results[i] = fmt.Errorf("discover libraries: %w", lookupErr)
			continue
		}
		ids := matchingLibraries(e.path, libraries)
		if len(ids) == 0 {
			results[i] = fmt.Errorf("no Jellyfin library contains the completed download folder; check library paths and path mappings")
			continue
		}
		for _, id := range ids {
			refreshErr, attempted := refreshes[id]
			if !attempted {
				endpoint := "/Items/" + url.PathEscape(id) + "/Refresh?metadataRefreshMode=Default&imageRefreshMode=Default&replaceAllMetadata=false&replaceAllImages=false"
				refreshErr = n.request(ctx, http.MethodPost, endpoint, nil, nil)
				refreshes[id] = refreshErr
				if refreshErr == nil {
					slog.Info("jellyfin: library refresh queued", "library_id", id)
				}
			}
			if refreshErr != nil {
				results[i] = fmt.Errorf("refresh library %s: %w", id, refreshErr)
			}
		}
	}
	if len(updates) > 0 {
		body, err := json.Marshal(struct {
			Updates []update `json:"Updates"`
		}{updates})
		if err == nil {
			err = n.request(ctx, http.MethodPost, "/Library/Media/Updated", body, nil)
		}
		for _, i := range subtitleIndexes {
			results[i] = err
		}
		if err == nil {
			slog.Info("jellyfin: folder updates reported", "folders", len(updates))
		}
	}
	return results
}
