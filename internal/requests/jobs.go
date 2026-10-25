package requests

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"cineroute/internal/medialanguages"
	"cineroute/internal/tmdb"
)

var ErrQuota = errors.New("request limit reached for this period")
var ErrNotFound = errors.New("request not found")

type Job struct {
	ID             string       `json:"id"`
	Owner          string       `json:"owner"`
	Media          tmdb.Details `json:"media"`
	Target         string       `json:"target"`
	PreferredDrive string       `json:"preferred_drive"`
	Seasons        []int        `json:"seasons"`
	FollowFuture   bool         `json:"follow_future"`
	OnlyFuture     bool         `json:"only_future"`
	Episodes       []string     `json:"episodes"`
	Status         string       `json:"status"`
	Message        string       `json:"message"`
	AdminError     string       `json:"admin_error"`
	Progress       float64      `json:"progress"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	ApprovedAt     time.Time    `json:"approved_at"`
	NextSearch     time.Time    `json:"next_search"`
	RetryAt        time.Time    `json:"retry_at"`
}

type Download struct {
	Languages           medialanguages.Metadata `json:"languages"`
	LanguageScore       int                     `json:"language_score"`
	SubtitleFormatScore int                     `json:"subtitle_format_score"`
	RequestID           string                  `json:"request_id,omitempty"`
	RequestedBy         string                  `json:"requested_by,omitempty"`
	IndexerID           int                     `json:"indexer_id,omitempty"`
	Indexer             string                  `json:"indexer,omitempty"`
	InfoURL             string                  `json:"info_url,omitempty"`
	TorrentState        string                  `json:"torrent_state,omitempty"`
	ObservedAt          time.Time               `json:"observed_at"`
	CompletedAt         time.Time               `json:"completed_at"`
	Hash                string                  `json:"hash"`
	MediaType           string                  `json:"media_type"`
	TMDBID              int                     `json:"tmdb_id"`
	Target              string                  `json:"target"`
	DriveID             string                  `json:"drive_id"`
	SavePath            string                  `json:"save_path"`
	ContentPath         string                  `json:"content_path"`
	Files               []string                `json:"files"`
	FileSizes           map[string]int64        `json:"file_sizes"`
	SourcePath          string                  `json:"source_path,omitempty"`
	TorrentData         []byte                  `json:"torrent_data,omitempty"`
	ReleaseKey          string                  `json:"release_key"`
	Title               string                  `json:"title"`
	Quality             string                  `json:"quality"`
	Score               int                     `json:"score"`
	Size                int64                   `json:"size"`
	Episodes            []string                `json:"episodes"`
	Status              string                  `json:"status"`
	Progress            float64                 `json:"progress"`
	Paused              bool                    `json:"paused"`
	AddedAt             time.Time               `json:"added_at"`
	LastProgress        time.Time               `json:"last_progress"`
	Error               string                  `json:"error"`
	FailedAt            time.Time               `json:"failed_at"`
}

const jobSchema = `
CREATE TABLE IF NOT EXISTS media_requests(id TEXT PRIMARY KEY,owner TEXT NOT NULL,media_type TEXT NOT NULL,tmdb_id INTEGER NOT NULL,target TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL,data TEXT NOT NULL,UNIQUE(owner,media_type,tmdb_id,target));
CREATE INDEX IF NOT EXISTS request_owner_date ON media_requests(owner,media_type,created_at);
CREATE TABLE IF NOT EXISTS request_downloads(hash TEXT NOT NULL,media_type TEXT NOT NULL,tmdb_id INTEGER NOT NULL,target TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(media_type,tmdb_id,target,hash));
CREATE TABLE IF NOT EXISTS request_placements(media_type TEXT NOT NULL,tmdb_id INTEGER NOT NULL,drive_id TEXT NOT NULL,PRIMARY KEY(media_type,tmdb_id));
CREATE TABLE IF NOT EXISTS request_worker_lease(id INTEGER PRIMARY KEY CHECK(id=1),owner TEXT NOT NULL,expires INTEGER NOT NULL);
`

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func terminal(status string) bool { return status == "cancelled" || status == "declined" }

func (s *Store) Create(job Job, settings Settings, admin bool) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return Job{}, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	job.Owner = strings.ToLower(job.Owner)
	now := time.Now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	var previous string
	err = tx.QueryRow("SELECT data FROM media_requests WHERE owner=? AND media_type=? AND tmdb_id=? AND target=?", job.Owner, job.Media.MediaType, job.Media.ID, job.Target).Scan(&previous)
	if err == nil {
		var old Job
		if err = json.Unmarshal([]byte(previous), &old); err != nil {
			return Job{}, err
		}
		if !terminal(old.Status) {
			changed := false
			for _, season := range job.Seasons {
				if !slices.Contains(old.Seasons, season) {
					old.Seasons = append(old.Seasons, season)
					changed = true
				}
			}
			if job.FollowFuture && !old.FollowFuture {
				old.FollowFuture = true
				changed = true
			}
			if !changed {
				return old, nil
			}
			job = old
			job.UpdatedAt = now
			job.NextSearch = now
			if old.Status != "pending" {
				job.Status = "queued"
			}
		} else {
			job.ID = old.ID
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, err
	}
	if job.ID == "" {
		job.ID = NewID()
	}
	if job.CreatedAt.Equal(now) && !admin {
		limit := settings.MovieLimit
		if job.Media.MediaType == "tv" {
			limit = settings.TVLimit
		}
		if limit > 0 {
			var count int
			err = tx.QueryRow("SELECT COUNT(*) FROM media_requests WHERE owner=? AND media_type=? AND created_at>=?", job.Owner, job.Media.MediaType, now.AddDate(0, 0, -settings.LimitDays).Format(time.RFC3339Nano)).Scan(&count)
			if err != nil {
				return Job{}, err
			}
			if count >= limit {
				return Job{}, ErrQuota
			}
		}
	}
	if err = saveJob(tx, job); err != nil {
		return Job{}, err
	}
	return job, tx.Commit()
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

func saveJob(db execer, job Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO media_requests(id,owner,media_type,tmdb_id,target,status,created_at,data) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,data=excluded.data,created_at=excluded.created_at", job.ID, job.Owner, job.Media.MediaType, job.Media.ID, job.Target, job.Status, job.CreatedAt.Format(time.RFC3339Nano), string(data))
	return err
}
func (s *Store) Jobs(owner string) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	query := "SELECT data FROM media_requests"
	args := []any{}
	if owner != "" {
		query += " WHERE owner=?"
		args = append(args, strings.ToLower(owner))
	}
	query += " ORDER BY created_at DESC"
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		var data string
		var job Job
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
func (s *Store) Job(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return Job{}, err
	}
	defer db.Close()
	var data string
	err = db.QueryRow("SELECT data FROM media_requests WHERE id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	var job Job
	err = json.Unmarshal([]byte(data), &job)
	return job, err
}

// Update never resurrects a request canceled or declined while a worker ran.
func (s *Store) Update(job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()
	var data string
	if err = db.QueryRow("SELECT data FROM media_requests WHERE id=?", job.ID).Scan(&data); err != nil {
		return err
	}
	var current Job
	if err = json.Unmarshal([]byte(data), &current); err != nil {
		return err
	}
	if terminal(current.Status) && !terminal(job.Status) {
		return nil
	}
	for _, season := range current.Seasons {
		if !slices.Contains(job.Seasons, season) {
			job.Seasons = append(job.Seasons, season)
		}
	}
	job.FollowFuture = job.FollowFuture || current.FollowFuture
	if current.RetryAt.After(job.RetryAt) {
		job.RetryAt = current.RetryAt
	}
	if current.UpdatedAt.After(job.UpdatedAt) && current.Status == "queued" {
		job.NextSearch = current.NextSearch
		job.Status = "queued"
	}
	job.UpdatedAt = time.Now().UTC()
	return saveJob(db, job)
}
func (s *Store) Action(id, owner, action string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return Job{}, err
	}
	defer db.Close()
	var data string
	err = db.QueryRow("SELECT data FROM media_requests WHERE id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	var job Job
	if err = json.Unmarshal([]byte(data), &job); err != nil {
		return Job{}, err
	}
	if owner != "" && !strings.EqualFold(owner, job.Owner) {
		return Job{}, ErrNotFound
	}
	now := time.Now().UTC()
	switch action {
	case "cancel":
		job.Status = "cancelled"
		job.Message = "Request canceled"
	case "decline":
		job.Status = "declined"
		job.Message = "Request declined"
	case "approve":
		if job.Status != "pending" {
			return Job{}, fmt.Errorf("only pending requests can be approved")
		}
		job.ApprovedAt = now
		job.Status = "queued"
	case "retry":
		if job.Status == "pending" || terminal(job.Status) {
			return Job{}, fmt.Errorf("approve this request before retrying")
		}
		job.Status = "queued"
		job.RetryAt = now
	default:
		return Job{}, fmt.Errorf("invalid request action")
	}
	job.NextSearch = now
	job.UpdatedAt = now
	job.AdminError = ""
	return job, saveJob(db, job)
}
func (s *Store) Downloads() ([]Download, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT data FROM request_downloads")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Download{}
	for rows.Next() {
		var data string
		var d Download
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) SaveDownload(d Download) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO request_downloads(hash,media_type,tmdb_id,target,data) VALUES(?,?,?,?,?) ON CONFLICT(media_type,tmdb_id,target,hash) DO UPDATE SET data=excluded.data", d.Hash, d.MediaType, d.TMDBID, d.Target, string(data))
	return err
}
func (s *Store) Placement(kind string, id int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return "", err
	}
	defer db.Close()
	var drive string
	err = db.QueryRow("SELECT drive_id FROM request_placements WHERE media_type=? AND tmdb_id=?", kind, id).Scan(&drive)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return drive, err
}
func (s *Store) Pin(kind string, id int, drive string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("INSERT INTO request_placements(media_type,tmdb_id,drive_id) VALUES(?,?,?) ON CONFLICT(media_type,tmdb_id) DO NOTHING", kind, id, drive)
	if err != nil {
		return err
	}
	var actual string
	err = db.QueryRow("SELECT drive_id FROM request_placements WHERE media_type=? AND tmdb_id=?", kind, id).Scan(&actual)
	if err == nil && actual != drive {
		return fmt.Errorf("title is assigned to a different disk; review its library folders")
	}
	return err
}
func (s *Store) Lease(owner string, release bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()
	if release {
		_, err = db.Exec("DELETE FROM request_worker_lease WHERE id=1 AND owner=?", owner)
		return true, err
	}
	_, err = db.Exec("INSERT INTO request_worker_lease(id,owner,expires) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE request_worker_lease.expires<? OR request_worker_lease.owner=?", owner, time.Now().Add(2*time.Minute).Unix(), time.Now().Unix(), owner)
	if err != nil {
		return false, err
	}
	var active string
	err = db.QueryRow("SELECT owner FROM request_worker_lease WHERE id=1").Scan(&active)
	return active == owner, err
}
