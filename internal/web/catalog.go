package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"cineroute/internal/requests"
	"cineroute/internal/tmdb"
)

func (s *Server) catalogSearch(w http.ResponseWriter, r *http.Request) {
	if s.tmdb == nil {
		writeErr(w, 503, "Movie search is not configured yet.")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("type")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page == 0 {
		page = 1
	}
	if len(query) < 2 || len(query) > 200 || page < 1 || page > 500 || (kind != "" && kind != "all" && kind != "movie" && kind != "tv") {
		writeErr(w, 400, "Enter a search between 2 and 200 characters.")
		return
	}
	settings, err := s.requests.Load()
	if err != nil {
		writeErr(w, 503, "Request settings are temporarily unavailable.")
		return
	}
	result, err := s.tmdb.SearchCatalog(r.Context(), query, kind, page)
	if err != nil {
		writeErr(w, 502, "Movie search is temporarily unavailable. Try again shortly.")
		return
	}
	filtered := []catalogMedia{}
	for _, item := range result.Results {
		if (item.MediaType == "movie" && settings.AllowMovies) || (item.MediaType == "tv" && settings.AllowTV) {
			filtered = append(filtered, catalogMedia{Result: item, InLibrary: s.catalogInLibrary(item)})
		}
	}
	writeJSON(w, 200, struct {
		tmdb.CatalogPage
		Results []catalogMedia `json:"results"`
	}{result, filtered})
}
func (s *Server) catalogDetails(w http.ResponseWriter, r *http.Request) {
	if s.tmdb == nil {
		writeErr(w, 503, "Movie search is not configured yet.")
		return
	}
	kind := r.PathValue("type")
	id, _ := strconv.Atoi(r.PathValue("id"))
	if (kind != "movie" && kind != "tv") || id <= 0 {
		writeErr(w, 400, "invalid title")
		return
	}
	details, err := s.tmdb.Details(r.Context(), kind, id)
	if err != nil || details.ID != id || details.Adult || details.DisplayTitle() == "" {
		writeErr(w, 502, "This title could not be loaded.")
		return
	}
	settings, err := s.requests.Load()
	if err != nil {
		writeErr(w, 503, "Request settings are temporarily unavailable.")
		return
	}
	a := actor(r)
	if (kind == "movie" && !settings.AllowMovies) || (kind == "tv" && !settings.AllowTV) {
		writeErr(w, 403, "Requests for this media type are disabled.")
		return
	}
	seasons := details.Seasons[:0]
	for _, season := range details.Seasons {
		if season.Number != 0 || settings.IncludeSpecials {
			seasons = append(seasons, season)
		}
	}
	details.Seasons = seasons
	writeJSON(w, 200, map[string]any{"media": details.Result, "original_language": details.OriginalLanguage, "seasons": details.Seasons, "auto_approve": settings.AutoApprove || a.Role == "admin", "digitally_available": tmdb.DigitallyAvailable(details, time.Now())})
}

type publicJob struct {
	SpanishAudio bool      `json:"spanish_audio"`
	ID           string    `json:"id"`
	Media        any       `json:"media"`
	Status       string    `json:"status"`
	Message      string    `json:"message"`
	Progress     float64   `json:"progress"`
	Seasons      []int     `json:"seasons"`
	CreatedAt    time.Time `json:"created_at"`
}

func jobView(job requests.Job) publicJob {
	return publicJob{SpanishAudio: job.SpanishAudio, ID: job.ID, Media: job.Media.Result, Status: job.Status, Message: job.Message, Progress: job.Progress, Seasons: job.Seasons, CreatedAt: job.CreatedAt}
}
func (s *Server) userRequests(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.requests.Jobs(actor(r).Name)
	if err != nil {
		writeErr(w, 503, "Your requests could not be loaded.")
		return
	}
	view := []publicJob{}
	for _, job := range jobs {
		view = append(view, jobView(job))
	}
	writeJSON(w, 200, map[string]any{"requests": view})
}
func (s *Server) createUserRequest(w http.ResponseWriter, r *http.Request) {
	if s.tmdb == nil {
		writeErr(w, 503, "Movie search is not configured yet.")
		return
	}
	var body struct {
		SpanishAudio bool   `json:"spanish_audio"`
		MediaType    string `json:"media_type"`
		TMDBID       int    `json:"tmdb_id"`
		Seasons      []int  `json:"seasons"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.TMDBID <= 0 || (body.MediaType != "movie" && body.MediaType != "tv") || len(body.Seasons) > 100 {
		writeErr(w, 400, "Choose a movie or show and its seasons.")
		return
	}
	settings, err := s.requests.Load()
	if err != nil {
		writeErr(w, 503, "Request settings are temporarily unavailable.")
		return
	}
	if (body.MediaType == "movie" && !settings.AllowMovies) || (body.MediaType == "tv" && !settings.AllowTV) {
		writeErr(w, 403, "Requests for this media type are disabled.")
		return
	}
	media, err := s.tmdb.Details(r.Context(), body.MediaType, body.TMDBID)
	if err != nil || media.ID != body.TMDBID || media.Adult || media.DisplayTitle() == "" {
		writeErr(w, 502, "This title could not be verified.")
		return
	}
	profile := profileFor(settings, body.MediaType)
	a := actor(r)
	now := time.Now().UTC()
	job := requests.Job{SpanishAudio: body.SpanishAudio, Owner: a.Name, Media: media, Target: profile.Destination, PreferredDrive: profile.NewTitleDrive, Seasons: []int{}, Episodes: []string{}, Status: "pending", Message: "Waiting for administrator approval"}
	if body.MediaType == "tv" {
		for _, number := range body.Seasons {
			found := false
			for _, season := range media.Seasons {
				if season.Number == number && (number != 0 || settings.IncludeSpecials) {
					found = true
					break
				}
			}
			if !found {
				writeErr(w, 400, "One of the selected seasons is unavailable.")
				return
			}
			if !slices.Contains(job.Seasons, number) {
				job.Seasons = append(job.Seasons, number)
			}
		}
		if len(job.Seasons) == 0 {
			writeErr(w, 400, "Select at least one season.")
			return
		}
	} else if len(body.Seasons) > 0 {
		writeErr(w, 400, "Movies do not have seasons.")
		return
	}
	if settings.AutoApprove || a.Role == "admin" {
		job.Status = "queued"
		job.Message = "Queued for a suitable release"
		job.ApprovedAt = now
		job.NextSearch = now
		if !settings.SearchOnApproval {
			job.NextSearch = now.Add(time.Duration(settings.RetryIntervalHours) * time.Hour)
		}
	}
	created, err := s.requests.Create(job, settings, a.Role == "admin")
	if errors.Is(err, requests.ErrQuota) {
		writeErr(w, 429, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 503, "Your request could not be saved.")
		return
	}
	if s.engine != nil {
		s.engine.Wake()
	}
	writeJSON(w, 201, map[string]any{"request": jobView(created)})
}
func (s *Server) cancelUserRequest(w http.ResponseWriter, r *http.Request) {
	job, err := s.requests.Action(r.PathValue("id"), actor(r).Name, "cancel")
	if errors.Is(err, requests.ErrNotFound) {
		writeErr(w, 404, "request not found")
		return
	}
	if err != nil {
		writeErr(w, 400, "Request could not be canceled.")
		return
	}
	writeJSON(w, 200, map[string]any{"request": jobView(job)})
}
func (s *Server) requestQueue(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.requests.Jobs("")
	if err != nil {
		writeErr(w, 503, "Request queue could not be loaded.")
		return
	}
	view := []any{}
	for _, job := range jobs {
		view = append(view, map[string]any{"request": jobView(job), "owner": job.Owner, "destination": job.Target, "diagnostic": job.AdminError, "next_search": job.NextSearch})
	}
	writeJSON(w, 200, map[string]any{"requests": view})
}
func (s *Server) requestAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if !slices.Contains([]string{"approve", "decline", "retry", "cancel"}, action) {
		writeErr(w, 400, "invalid action")
		return
	}
	job, err := s.requests.Action(r.PathValue("id"), "", action)
	if err != nil {
		if errors.Is(err, requests.ErrNotFound) {
			writeErr(w, 404, "request not found")
		} else {
			writeErr(w, 400, fmt.Sprint(err))
		}
		return
	}
	if action == "approve" {
		if settings, err := s.requests.Load(); err == nil && !settings.SearchOnApproval {
			job.NextSearch = time.Now().Add(time.Duration(settings.RetryIntervalHours) * time.Hour)
			_ = s.requests.Update(job)
		}
	}
	if s.engine != nil {
		s.engine.Wake()
	}
	writeJSON(w, 200, map[string]any{"request": jobView(job)})
}

// Library presence is independent of request history and quality preferences.
// Empty folders and files belonging to unfinished downloads do not count.
type catalogMedia struct {
	tmdb.Result
	InLibrary bool `json:"in_library"`
}
