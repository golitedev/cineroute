package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"cineroute/internal/music/jobs"
)

var musicID = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

func musicOK(w http.ResponseWriter, value any) {
	writeJSON(w, 200, map[string]any{"success": true, "data": value})
}
func (s *Server) musicReady(w http.ResponseWriter) bool {
	if s.music == nil || s.music.Jobs == nil {
		writeErr(w, 503, "Music is not configured yet.")
		return false
	}
	return true
}
func (s *Server) musicInfo(w http.ResponseWriter, r *http.Request) {
	musicOK(w, map[string]any{"enabled": s.cfg.Music.Enabled, "qobuz_ok": s.music != nil, "qobuz_error": "", "qobuz_error_code": ""})
}
func (s *Server) musicCatalog(w http.ResponseWriter, r *http.Request) {
	if !s.musicReady(w) {
		return
	}
	q := r.URL.Query()
	offset, err := strconv.Atoi(q.Get("offset"))
	if q.Get("offset") == "" {
		offset = 0
		err = nil
	}
	if err != nil || offset < 0 || offset > 100000 {
		writeErr(w, 400, "Invalid catalog offset.")
		return
	}
	var result json.RawMessage
	switch r.PathValue("action") {
	case "get-music":
		query := strings.TrimSpace(q.Get("q"))
		if len(query) < 2 || len(query) > 200 || offset > 1000 {
			writeErr(w, 400, "Enter a search between 2 and 200 characters.")
			return
		}
		result, err = s.music.Catalog.Search(r.Context(), query, 10, offset)
	case "get-album":
		id := q.Get("album_id")
		if !musicID.MatchString(id) {
			writeErr(w, 400, "Invalid album.")
			return
		}
		result, err = s.music.Catalog.AlbumRaw(r.Context(), id)
	case "get-artist":
		id := q.Get("artist_id")
		if !musicID.MatchString(id) {
			writeErr(w, 400, "Invalid artist.")
			return
		}
		result, err = s.music.Catalog.ArtistPage(r.Context(), id)
		if err == nil {
			musicOK(w, map[string]any{"artist": result})
			return
		}
	case "get-releases":
		id := q.Get("artist_id")
		kind := q.Get("release_type")
		if kind == "" {
			kind = "album"
		}
		if !musicID.MatchString(id) || !map[string]bool{"album": true, "live": true, "compilation": true, "epSingle": true, "download": true}[kind] {
			writeErr(w, 400, "Invalid artist release search.")
			return
		}
		limit := 10
		if q.Get("limit") != "" {
			limit, err = strconv.Atoi(q.Get("limit"))
			if err != nil || limit < 1 || limit > 100 {
				writeErr(w, 400, "Invalid page size.")
				return
			}
		}
		result, err = s.music.Catalog.ArtistReleases(r.Context(), id, kind, limit, offset, 1000)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeErr(w, 502, "Music search is temporarily unavailable. Check the Qobuz account configuration.")
		return
	}
	musicOK(w, result)
}
func publicMusicJob(job jobs.Job) jobs.Job { job.LibraryPath = ""; return job }
func (s *Server) musicJobs(w http.ResponseWriter, r *http.Request) {
	result := []jobs.Job{}
	if s.music != nil {
		for _, job := range s.music.Jobs.List() {
			if job.RequestedBy == actor(r).Name {
				result = append(result, publicMusicJob(job))
			}
		}
	}
	musicOK(w, map[string]any{"jobs": result})
}
func (s *Server) musicAdd(w http.ResponseWriter, r *http.Request) {
	if !s.musicReady(w) {
		return
	}
	var body struct {
		AlbumID string `json:"album_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || !musicID.MatchString(body.AlbumID) {
		writeErr(w, 400, "Choose a valid album.")
		return
	}
	// Bound each user's outstanding queue before the catalog lookup.
	count := 0
	for _, j := range s.music.Jobs.List() {
		if j.RequestedBy == actor(r).Name && !j.Status.Finished() {
			count++
			if j.AlbumID == body.AlbumID {
				musicOK(w, publicMusicJob(j))
				return
			}
		}
	}
	if count >= 50 {
		writeErr(w, 429, "You already have 50 music requests in progress.")
		return
	}
	job, err := s.music.Jobs.Enqueue(r.Context(), body.AlbumID, actor(r).Name, 50)
	if errors.Is(err, jobs.ErrQueueFull) {
		writeErr(w, 429, "You already have 50 music requests in progress.")
		return
	}
	if err != nil {
		writeErr(w, 422, "This release could not be queued. It may be unavailable on your Qobuz account.")
		return
	}
	musicOK(w, publicMusicJob(job))
}
func (s *Server) musicAction(w http.ResponseWriter, r *http.Request) {
	if !s.musicReady(w) {
		return
	}
	id := r.PathValue("id")
	job, err := s.music.Jobs.Get(id)
	if err != nil || job.RequestedBy != actor(r).Name {
		writeErr(w, 404, "Request not found.")
		return
	}
	switch r.PathValue("action") {
	case "cancel":
		job, err = s.music.Jobs.Cancel(id)
	case "retry":
		job, err = s.music.Jobs.Retry(id, 50)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeErr(w, 409, "This request cannot be changed in its current state.")
		return
	}
	musicOK(w, publicMusicJob(job))
}
func (s *Server) musicRemove(w http.ResponseWriter, r *http.Request) {
	if !s.musicReady(w) {
		return
	}
	job, err := s.music.Jobs.Get(r.PathValue("id"))
	if err != nil || job.RequestedBy != actor(r).Name {
		writeErr(w, 404, "Request not found.")
		return
	}
	if err := s.music.Jobs.Remove(job.ID); err != nil {
		writeErr(w, 409, "Only finished music requests can be dismissed.")
		return
	}
	musicOK(w, nil)
}
