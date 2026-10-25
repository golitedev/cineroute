package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cineroute/internal/medialanguages"
	"cineroute/internal/requests"
)

// Explicit admin views keep recovery torrent data, download URLs and file lists
// out of responses. The public listener never registers this endpoint.
type requestHistoryDownload struct {
	Languages           medialanguages.Metadata `json:"languages"`
	LanguageScore       int                     `json:"language_score"`
	SubtitleFormatScore int                     `json:"subtitle_format_score"`
	Hash                string                  `json:"hash"`
	RequestID           string                  `json:"request_id,omitempty"`
	RequestedBy         string                  `json:"requested_by,omitempty"`
	Title               string                  `json:"title"`
	Tracker             string                  `json:"tracker"`
	TrackerURL          string                  `json:"tracker_url,omitempty"`
	Quality             string                  `json:"quality"`
	Drive               string                  `json:"drive"`
	Status              string                  `json:"status"`
	TorrentState        string                  `json:"torrent_state,omitempty"`
	Progress            float64                 `json:"progress"`
	Size                int64                   `json:"size"`
	Episodes            []string                `json:"episodes"`
	AddedAt             time.Time               `json:"added_at"`
	CompletedAt         time.Time               `json:"completed_at"`
	FailedAt            time.Time               `json:"failed_at"`
	LastProgress        time.Time               `json:"last_progress"`
	ObservedAt          time.Time               `json:"observed_at"`
	Error               string                  `json:"error,omitempty"`
}
type requestHistoryEntry struct {
	Request     publicJob                `json:"request"`
	Owner       string                   `json:"owner"`
	Destination string                   `json:"destination"`
	Status      string                   `json:"status"`
	Diagnostic  string                   `json:"diagnostic,omitempty"`
	NextSearch  time.Time                `json:"next_search"`
	Downloads   []requestHistoryDownload `json:"downloads"`
}

// Only tracker details links supplied by Prowlarr are used, never its download
// proxy URL or GUID. Reject links carrying credentials instead of publishing them.
func trackerPageURL(raw string) string {
	if len(raw) > 4096 || strings.ContainsAny(raw, "\r\n\t") {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	for key := range query {
		key = strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
		if strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "secret") || key == "auth" {
			return ""
		}
	}
	return u.String()
}
func historyDownloadState(d requests.Download, settings requests.Settings, now time.Time) string {
	if d.Status == "complete" || d.Status == "failed" || d.Status == "cancelled" {
		return d.Status
	}
	if d.Error != "" {
		return "error"
	}
	if d.Paused {
		return "paused"
	}
	switch d.TorrentState {
	case "queuedDL":
		return "queued"
	case "checkingDL", "checkingResumeData", "checkingUP":
		return "checking"
	case "metaDL", "forcedMetaDL":
		return "metadata"
	case "stalledDL":
		return "stalled"
	case "error", "missingFiles":
		return "error"
	}
	if d.Status == "planned" {
		return "planned"
	}
	if settings.StalledMinutes > 0 && !d.LastProgress.IsZero() && now.Sub(d.LastProgress) > time.Duration(settings.StalledMinutes)*time.Minute {
		return "stalled"
	}
	return d.Status
}
func historyRequestState(job requests.Job, downloads []requestHistoryDownload) string {
	if job.Status != "downloading" {
		return job.Status
	}
	// Historical failed attempts must not hide a successful replacement. Only
	// current transfers influence the request's current state.
	for _, state := range []string{"error", "stalled", "paused", "downloading", "checking", "metadata", "queued", "planned"} {
		for _, d := range downloads {
			if d.Status == state {
				return state
			}
		}
	}
	return job.Status
}

func (s *Server) requestHistory(w http.ResponseWriter, r *http.Request) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 100000 {
			writeErr(w, 400, "Invalid history page.")
			return
		}
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	state := r.URL.Query().Get("status")
	if len(query) > 200 {
		writeErr(w, 400, "History search is too long.")
		return
	}
	valid := map[string]bool{"": true, "all": true, "pending": true, "queued": true, "searching": true, "planned": true, "downloading": true, "stalled": true, "paused": true, "checking": true, "metadata": true, "waiting": true, "monitoring": true, "error": true, "complete": true, "cancelled": true, "declined": true}
	if !valid[state] {
		writeErr(w, 400, "Invalid history status.")
		return
	}
	jobs, err := s.requests.Jobs("")
	if err != nil {
		writeErr(w, 503, "Request history could not be loaded.")
		return
	}
	downloads, err := s.requests.Downloads()
	if err != nil {
		writeErr(w, 503, "Download history could not be loaded.")
		return
	}
	settings, err := s.requests.Load()
	if err != nil {
		writeErr(w, 503, "Request settings could not be loaded.")
		return
	}
	now := time.Now().UTC()
	// Downloads are shared by requests for the same title and destination. Keep
	// their actual initiator distinct from each requesting user, including legacy
	// records whose initiator and tracker page were never captured.
	type scope struct {
		kind   string
		id     int
		target string
	}
	byTitle := map[scope][]requestHistoryDownload{}
	for _, d := range downloads {
		entry := requestHistoryDownload{Hash: d.Hash, RequestID: d.RequestID, RequestedBy: d.RequestedBy, Title: d.Title, Tracker: d.Indexer, TrackerURL: trackerPageURL(d.InfoURL), Quality: d.Quality, Drive: d.DriveID, Status: historyDownloadState(d, settings, now), TorrentState: d.TorrentState, Progress: d.Progress, Size: d.Size, Episodes: d.Episodes, AddedAt: d.AddedAt, CompletedAt: d.CompletedAt, FailedAt: d.FailedAt, LastProgress: d.LastProgress, ObservedAt: d.ObservedAt, Error: d.Error}
		entry.Languages, entry.LanguageScore = d.Languages, d.LanguageScore
		entry.SubtitleFormatScore = d.SubtitleFormatScore
		key := scope{d.MediaType, d.TMDBID, d.Target}
		byTitle[key] = append(byTitle[key], entry)
	}
	for key := range byTitle {
		sort.SliceStable(byTitle[key], func(i, j int) bool { return byTitle[key][i].AddedAt.After(byTitle[key][j].AddedAt) })
	}
	counts := map[string]int{}
	entries := []requestHistoryEntry{}
	for _, job := range jobs {
		related := byTitle[scope{job.Media.MediaType, job.Media.ID, job.Target}]
		if related == nil {
			related = []requestHistoryDownload{}
		}
		status := historyRequestState(job, related)
		counts[status]++
		if state != "" && state != "all" && status != state {
			continue
		}
		if query != "" {
			haystack := job.Media.DisplayTitle() + " " + job.Owner + " " + job.Target + " " + job.Message + " " + job.AdminError
			for _, d := range related {
				haystack += " " + d.Title + " " + d.Tracker + " " + d.RequestedBy + " " + d.Hash + " " + d.Drive
			}
			if !strings.Contains(strings.ToLower(haystack), query) {
				continue
			}
		}
		entries = append(entries, requestHistoryEntry{Request: jobView(job), Owner: job.Owner, Destination: job.Target, Status: status, Diagnostic: job.AdminError, NextSearch: job.NextSearch, Downloads: related})
	}
	total := len(entries)
	const pageSize = 50
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * pageSize
	end := min(start+pageSize, total)
	writeJSON(w, 200, map[string]any{"entries": entries[start:end], "page": page, "total_pages": pages, "total": total, "counts": counts, "as_of": now})
}
