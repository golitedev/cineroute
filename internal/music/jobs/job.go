// Package jobs is CineRoute's download queue: one album at a time is downloaded
// with streamrip, verified, and moved into the library.
package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Status string

const (
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusVerifying   Status = "verifying"
	StatusImporting   Status = "importing"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusSkipped     Status = "skipped"
	StatusCancelled   Status = "cancelled"
)

// Active reports whether the job is being worked on right now.
func (s Status) Active() bool {
	return s == StatusDownloading || s == StatusVerifying || s == StatusImporting
}

// Finished reports whether the job is done (in any way).
func (s Status) Finished() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusSkipped, StatusCancelled:
		return true
	}
	return false
}

// Job is one album in the queue or history. Its JSON form is the API.
type Job struct {
	ID          string         `json:"id"`
	AlbumID     string         `json:"album_id"`
	Title       string         `json:"title"`
	Artist      string         `json:"artist"`
	Year        string         `json:"year"`
	Cover       string         `json:"cover"`
	TracksTotal int            `json:"tracks_total"`
	TracksDone  int            `json:"tracks_done"`
	Status      Status         `json:"status"`
	Message     string         `json:"message"`
	MessageCode string         `json:"message_code"`
	MessageArgs map[string]any `json:"message_args"`
	Problems    []string       `json:"problems"`
	// ProblemItems are the problems in a form the UI can translate.
	ProblemItems []ProblemItem `json:"problem_items"`
	Attempt      int           `json:"attempt"`
	MaxAttempts  int           `json:"max_attempts"`
	LibraryPath  string        `json:"library_path"`
	RequestedBy  string        `json:"requested_by"`
	CreatedAt    time.Time     `json:"created_at"`
	StartedAt    *time.Time    `json:"started_at"`
	FinishedAt   *time.Time    `json:"finished_at"`
}

func (j *Job) clone() Job {
	c := *j
	c.Problems = append([]string{}, j.Problems...)
	c.ProblemItems = append([]ProblemItem{}, j.ProblemItems...)
	c.MessageArgs = make(map[string]any, len(j.MessageArgs))
	for k, v := range j.MessageArgs {
		c.MessageArgs[k] = v
	}
	return c
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func ptr(t time.Time) *time.Time { return &t }
