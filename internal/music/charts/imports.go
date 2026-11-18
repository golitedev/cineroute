package charts

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"cineroute/internal/music/jobs"
)

type Import struct {
	JobID      string             `json:"job_id"`
	AlbumID    string             `json:"album_id"`
	Album      string             `json:"album"`
	Artist     string             `json:"artist"`
	Date       time.Time          `json:"imported_at"`
	Partial    bool               `json:"partial"`
	FolderPath string             `json:"folder_path,omitempty"`
	Sources    []jobs.ChartSource `json:"sources"`
}

func (m *Manager) importFolder(rel string) string {
	if m.importRoot == "" || !filepath.IsLocal(rel) || filepath.Clean(rel) == "." {
		return ""
	}
	return filepath.Join(m.importRoot, rel)
}

// Restore paths only from the original job. Album titles alone cannot identify
// a folder, and the retained job may outlive a folder that was moved or removed.
func (m *Manager) restoreImportPaths(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `SELECT job_id, json_extract(receipt, '$.album_id')
		FROM chart_import WHERE json_extract(receipt, '$.folder_path') IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	paths := make(map[string]string)
	for rows.Next() {
		var id, albumID string
		if err := rows.Scan(&id, &albumID); err != nil {
			return err
		}
		job, err := m.jobs.Get(id)
		if errors.Is(err, jobs.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if job.Status == jobs.StatusCompleted && job.AlbumID == albumID {
			if path := m.importFolder(job.LibraryPath); path != "" {
				paths[id] = path
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id, path := range paths {
		if _, err := m.db.ExecContext(ctx, `UPDATE chart_import SET receipt = json_set(receipt, '$.folder_path', ?)
			WHERE job_id = ?`, path, id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) recordImports(ctx context.Context) error {
	for _, job := range m.jobs.PendingChartImports() {
		finished := job.CreatedAt
		if job.FinishedAt != nil {
			finished = *job.FinishedAt
		}
		receipt := Import{JobID: job.ID, AlbumID: job.AlbumID, Album: job.Title, Artist: job.Artist,
			Date: finished, Partial: job.MessageCode == "added_incomplete", FolderPath: m.importFolder(job.LibraryPath), Sources: job.ChartSources}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if _, err := m.db.ExecContext(ctx, "INSERT OR IGNORE INTO chart_import VALUES (?, ?, ?)", job.ID, receipt.Date.Unix(), raw); err != nil {
			return err
		}
		if err := m.jobs.ConfirmChartImport(job.ID); err != nil {
			return err
		}
	}
	return nil
}

// Imports returns one page of the durable import log, newest first.
func (m *Manager) Imports(ctx context.Context, offset int) ([]Import, bool, error) {
	const pageSize = 25
	imports := make([]Import, 0, pageSize+1)
	err := m.eachImport(ctx, pageSize+1, offset, func(receipt Import) error {
		imports = append(imports, receipt)
		return nil
	})
	return imports[:min(len(imports), pageSize)], len(imports) > pageSize, err
}

func (m *Manager) eachImport(ctx context.Context, limit, offset int, visit func(Import) error) error {
	rows, err := m.db.QueryContext(ctx, "SELECT receipt FROM chart_import ORDER BY imported_at DESC, job_id DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var receipt Import
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		if err := visit(receipt); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ImportsCSV exports the full history in the same order as the admin page.
func (m *Manager) ImportsCSV(ctx context.Context) ([]byte, error) {
	var data bytes.Buffer
	csvFile := csv.NewWriter(&data)
	if err := csvFile.Write([]string{"Imported at", "Artist", "Album", "Folder name", "Folder path", "Status", "Chart sources", "Job ID"}); err != nil {
		return nil, err
	}
	err := m.eachImport(ctx, -1, 0, func(receipt Import) error {
		folder := ""
		if receipt.FolderPath != "" {
			folder = filepath.Base(receipt.FolderPath)
		}
		status := "Complete"
		if receipt.Partial {
			status = "Partial album"
		}
		sources := make([]string, 0, len(receipt.Sources))
		for _, source := range receipt.Sources {
			sources = append(sources, source.Playlist+" · "+source.Track+" · "+source.ChartDate)
		}
		fields := []string{receipt.Date.Format(time.RFC3339), receipt.Artist, receipt.Album, folder, receipt.FolderPath,
			status, strings.Join(sources, "\n"), receipt.JobID}
		for i, field := range fields {
			trimmed := strings.TrimLeftFunc(field, unicode.IsSpace)
			if strings.HasPrefix(field, "\t") || strings.HasPrefix(field, "\r") || strings.HasPrefix(field, "\n") ||
				(trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0]))) {
				fields[i] = "'" + field
			}
		}
		return csvFile.Write(fields)
	})
	if err != nil {
		return nil, err
	}
	csvFile.Flush()
	return data.Bytes(), csvFile.Error()
}
