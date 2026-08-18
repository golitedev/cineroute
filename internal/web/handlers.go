package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"time"

	"cineroute/internal/allocator"
	"cineroute/internal/classifier"
	"cineroute/internal/library"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

type apiResponse struct {
	Intake  *intakeJSON   `json:"intake,omitempty"`
	Intakes []*intakeJSON `json:"intakes"`
	Error   string        `json:"error,omitempty"`
	Status  string        `json:"status,omitempty"`
}

type intakeJSON struct {
	ID          string        `json:"id"`
	Group       string        `json:"group"`
	CreatedAt   time.Time     `json:"created_at"`
	Filename    string        `json:"filename"`
	TorrentName string        `json:"torrent_name"`
	Size        int64         `json:"size"`
	Kind        string        `json:"kind"`
	RootFolder  bool          `json:"root_folder"`
	RootName    string        `json:"root_name"`
	Files       int           `json:"files"`
	InfoHashV1  string        `json:"info_hash_v1"`
	InfoHashV2  string        `json:"info_hash_v2,omitempty"`
	MediaType   string        `json:"media_type"`
	Title       string        `json:"title"`
	Year        int           `json:"year"`
	Season      int           `json:"season"`
	Confidence  string        `json:"confidence"`
	TMDB        []tmdb.Result `json:"tmdb"`
	TMDBError   string        `json:"tmdb_error"`
	Match       *tmdb.Result  `json:"match"`
	Dest        *Destination  `json:"dest"`
	Status      string        `json:"status"`
	Error       string        `json:"error"`
	Result      *SubmitResult `json:"result"`
}

func toJSON(in *Intake) *intakeJSON {
	return &intakeJSON{
		ID:          in.ID,
		Group:       groupKey(in),
		CreatedAt:   in.CreatedAt,
		Filename:    in.Filename,
		TorrentName: in.Meta.Name,
		Size:        in.Meta.Size,
		Kind:        string(in.Meta.Kind),
		RootFolder:  in.Meta.RootFolder,
		RootName:    in.Meta.RootName,
		Files:       len(in.Meta.Files),
		InfoHashV1:  in.Meta.InfoHashV1,
		InfoHashV2:  in.Meta.InfoHashV2,
		MediaType:   in.Class.MediaType,
		Title:       in.Class.Title,
		Year:        in.Class.Year,
		Season:      in.Class.Season,
		Confidence:  in.Class.Confidence,
		TMDB:        in.TMDBResults,
		TMDBError:   in.TMDBError,
		Match:       in.Match,
		Dest:        in.Dest,
		Status:      in.Status,
		Error:       in.Error,
		Result:      in.Result,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiResponse{Error: msg})
}

func (s *Server) pageIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.page.Execute(w, nil)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// storageJSON totals the plain free space of all drives, with no movie/tv
// distinction: each drive is a single volume, so its free bytes count once.
type storageJSON struct {
	Free    int64  `json:"free"`
	Healthy bool   `json:"healthy"`
	Err     string `json:"err"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := struct {
		TMDB            string      `json:"tmdb"`
		QBittorrent     string      `json:"qbittorrent"`
		Prowlarr        string      `json:"prowlarr"`
		ProwlarrIndexer string      `json:"prowlarr_indexer,omitempty"`
		QBVersion       string      `json:"qb_version"`
		QBWebAPI        string      `json:"qb_webapi"`
		Preallocate     string      `json:"preallocate"`
		TempPath        string      `json:"temp_path"`
		Storage         storageJSON `json:"storage"`
		Auth            bool        `json:"auth"`
	}{TMDB: "not configured", QBittorrent: "not checked", Prowlarr: "not configured"}
	if s.tmdb != nil {
		out.TMDB = "configured"
	}
	if s.qb != nil {
		ctx := r.Context()
		ver, err1 := s.qb.AppVersion(ctx)
		api, err2 := s.qb.WebAPIVersion(ctx)
		prefs, err3 := s.qb.Preferences(ctx)
		switch {
		case err1 != nil:
			out.QBittorrent = "unreachable: " + err1.Error()
		case err2 != nil:
			out.QBittorrent = "unreachable: " + err2.Error()
		default:
			out.QBittorrent = "ok"
			out.QBVersion = ver
			out.QBWebAPI = api
			if err3 == nil {
				if prefs.PreallocateAll {
					out.Preallocate = "enabled (blocking submissions)"
				} else {
					out.Preallocate = "disabled"
				}
				if prefs.TempPathEnabled {
					out.TempPath = "enabled (blocking submissions)"
				} else {
					out.TempPath = "disabled"
				}
			}
		}
	}
	if s.companions != nil {
		out.Prowlarr, out.ProwlarrIndexer = s.companions.ProwlarrStatus(r.Context())
	}
	storage := storageJSON{Healthy: true}
	for _, st := range s.alloc.Statuses(s.cfg.Drives) {
		storage.Free += st.Available
		if !st.Healthy {
			storage.Healthy = false
			if storage.Err == "" {
				storage.Err = st.Err
			}
		}
	}
	out.Storage = storage
	out.Auth = s.cfg.AuthPassword != ""
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listCompanions(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	openID := r.URL.Query().Get("open")
	writeJSON(w, http.StatusOK, s.companions.View(openID))
}

func (s *Server) updateCompanionSettings(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	var body struct {
		SearchIntervalSeconds *int `json:"search_interval_seconds"`
		SearchBatchSize       *int `json:"search_batch_size"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.SearchIntervalSeconds == nil && body.SearchBatchSize == nil {
		writeErr(w, http.StatusBadRequest, "at least one companion setting is required")
		return
	}
	if err := s.companions.SetSearchSettings(body.SearchIntervalSeconds, body.SearchBatchSize); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.companions.View(""))
}

func (s *Server) scanCompanions(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	if err := s.companions.Scan(r.Context()); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.companions.View(""))
}

func (s *Server) searchMissingCompanions(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	if err := s.companions.StartSearchMissing(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "batch": s.companions.View("").Batch})
}

func (s *Server) cancelCompanionSearch(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	if err := s.companions.CancelSearchMissing(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "canceling", "batch": s.companions.View("").Batch})
}

func (s *Server) searchCompanion(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	id := r.PathValue("id")
	if _, err := s.companions.SearchOne(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.companions.View(id))
}

func (s *Server) skipCompanion(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	if err := s.companions.Skip(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.companions.View(r.PathValue("id")))
}

func (s *Server) approveCompanion(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	var body struct {
		Guid string `json:"guid"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	movie, candidate, data, err := s.companions.PrepareSelected(r.Context(), r.PathValue("id"), body.Guid)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	meta, err := torrentmeta.Parse(data)
	if err != nil {
		err = fmt.Errorf("selected Prowlarr torrent is invalid: %w", err)
		s.companions.MarkError(movie.ID, err)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.qb == nil {
		err = errors.New("qBittorrent is not configured")
		s.companions.MarkError(movie.ID, err)
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	match := tmdb.Result{
		ID:          movie.TmdbID,
		Title:       movie.Title,
		ReleaseDate: fmt.Sprintf("%04d-01-01", movie.Year),
	}
	s.allocMu.Lock()
	outcome, err := s.submitTorrentLocked(r.Context(), submissionRequest{
		Bytes:           data,
		Filename:        "companion-" + movie.ID + ".torrent",
		Meta:            meta,
		MediaType:       "movie",
		Match:           match,
		RequireExisting: true,
	})
	s.allocMu.Unlock()
	if err != nil {
		s.companions.MarkError(movie.ID, err)
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if outcome == nil || outcome.Result == nil {
		err = errors.New("qBittorrent submission returned no result")
		s.companions.MarkError(movie.ID, err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.companions.MarkComplete(movie.ID, outcome.Result.Hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "torrent was submitted, but companion state could not be saved: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"movie":     movie,
		"candidate": candidate,
		"result":    outcome.Result,
		"view":      s.companions.View(movie.ID),
	})
}

func (s *Server) searchIntakeCompanion(w http.ResponseWriter, r *http.Request) {
	if s.companions == nil {
		writeErr(w, http.StatusServiceUnavailable, "companion subsystem is unavailable")
		return
	}
	in, ok := s.getIntake(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "intake not found")
		return
	}
	s.mu.RLock()
	if in.Class.MediaType != "movie" || in.Status != "submitted" || in.Match == nil || in.Result == nil || in.Dest == nil {
		s.mu.RUnlock()
		writeErr(w, http.StatusConflict, "only a successfully submitted movie can search for a companion")
		return
	}
	driveID := in.Result.DriveID
	path := in.Result.SavePath
	folder := in.Dest.FolderName
	match := *in.Match
	s.mu.RUnlock()
	id, err := s.companions.UpsertMovie(driveID, path, folder, match.DisplayTitle(), match.Year(), match.ID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if _, err := s.companions.SearchOne(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.companions.View(id))
}

func (s *Server) historyHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*intakeJSON, 0, len(s.recent))
	for _, in := range s.recent {
		out = append(out, toJSON(in))
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": out})
}

// upload accepts one or more .torrent files, parsing, classifying and
// running the initial TMDB search on each. TV intakes of the same show are
// stacked into one group; every movie is its own group.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(s.cfg.MaxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "upload too large or malformed")
		return
	}
	files := r.MultipartForm.File["torrents"]
	if len(files) == 0 {
		writeErr(w, http.StatusBadRequest, "missing torrent file")
		return
	}
	parsed := make([]*Intake, 0, len(files))
	for _, fh := range files {
		in, err := s.ingestFile(fh)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid torrent: "+err.Error())
			return
		}
		parsed = append(parsed, in)
	}
	created := make([]*intakeJSON, 0, len(parsed))
	for _, in := range parsed {
		s.storeIntake(in)
		created = append(created, toJSON(in))
	}
	writeJSON(w, http.StatusOK, apiResponse{Intake: created[0], Intakes: created})
}

func (s *Server) ingestFile(fh *multipart.FileHeader) (*Intake, error) {
	file, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, s.cfg.MaxUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read upload: %w", err)
	}
	if int64(len(data)) > s.cfg.MaxUploadBytes {
		return nil, fmt.Errorf("%s exceeds maximum upload size", fh.Filename)
	}
	meta, err := torrentmeta.Parse(data)
	if err != nil {
		return nil, err
	}

	cls := classifier.Classify(meta.Name, meta.RelPaths())
	in := &Intake{
		ID:        newID(),
		CreatedAt: time.Now(),
		Filename:  fh.Filename,
		Bytes:     data,
		Meta:      meta,
		Class: classifierResult{
			MediaType: cls.MediaType, Title: cls.Title, AltTitle: cls.AltTitle,
			Year: cls.Year, Season: cls.Season, Confidence: cls.Confidence,
		},
		Status: "parsed",
	}
	s.searchTMDB(in)
	s.autoConfirm(in)
	return in, nil
}

// listIntakes reports every active intake, oldest first, so the frontend can
// render the whole stack (grouped by show) after any action.
func (s *Server) listIntakes(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := make([]*Intake, 0, len(s.intakes))
	for _, in := range s.intakes {
		all = append(all, in)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	out := make([]*intakeJSON, 0, len(all))
	for _, in := range all {
		out = append(out, toJSON(in))
	}
	writeJSON(w, http.StatusOK, apiResponse{Intakes: out})
}

// deleteIntake removes one intake from the stack. Intakes that are currently
// being pushed to qBittorrent cannot be removed; submitted intakes can, since
// the torrent already lives in qBittorrent.
func (s *Server) deleteIntake(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Serialize against a running submit so an intake is never deleted while
	// its torrent is being added to qBittorrent.
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intakes[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "intake not found")
		return
	}
	if in.Status == "submitting" {
		writeErr(w, http.StatusConflict, "cannot delete an intake while it is being submitted")
		return
	}
	in.Bytes = nil
	delete(s.intakes, id)
	writeJSON(w, http.StatusOK, apiResponse{Intake: toJSON(in)})
}

// setType re-classifies the intake as movie or tv and re-runs the TMDB search.
func (s *Server) setType(w http.ResponseWriter, r *http.Request) {
	in, ok := s.getIntake(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "intake not found")
		return
	}
	var body struct {
		MediaType string `json:"media_type"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.MediaType != "movie" && body.MediaType != "tv" {
		writeErr(w, http.StatusBadRequest, "media_type must be movie or tv")
		return
	}
	s.mu.Lock()
	if in.Status == "submitted" || in.Status == "submitting" {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "cannot change a submitted intake")
		return
	}
	in.Class.MediaType = body.MediaType
	in.Match = nil
	in.Dest = nil
	in.Error = ""
	s.mu.Unlock()
	s.searchTMDB(in)
	s.autoConfirm(in)
	writeJSON(w, http.StatusOK, apiResponse{Intake: toJSON(in)})
}

// search re-runs the TMDB search with a manual query for the whole group the
// intake belongs to.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	in, ok := s.getIntake(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "intake not found")
		return
	}
	var body struct {
		Query string `json:"query"`
		Year  int    `json:"year"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	body.Query = strings.TrimSpace(body.Query)
	if body.Query == "" {
		writeErr(w, http.StatusBadRequest, "query must not be empty")
		return
	}
	s.mu.RLock()
	if in.Status == "submitted" || in.Status == "submitting" {
		s.mu.RUnlock()
		writeErr(w, http.StatusConflict, "cannot re-search a submitted intake")
		return
	}
	key := groupKey(in)
	s.mu.RUnlock()
	for _, m := range s.groupMembers(key) {
		s.mu.Lock()
		if m.Status == "submitted" {
			s.mu.Unlock()
			continue
		}
		m.SearchQuery = body.Query
		m.SearchYear = body.Year
		m.Match = nil
		m.Dest = nil
		m.Error = ""
		s.mu.Unlock()
		s.searchTMDBWith(m, body.Query, body.Year)
	}
	s.autoConfirm(in)
	writeJSON(w, http.StatusOK, apiResponse{Intake: toJSON(in)})
}

func (s *Server) searchTMDB(in *Intake) {
	s.searchTMDBWith(in, in.Class.Title, in.Class.Year)
}

// searchTMDBWith queries TMDB with a fallback chain so a wrong guessed year
// or a year that is part of the title ("Blade Runner 2049") still finds the
// right title: (1) query+year, (2) alternate title without year,
// (3) query without year. Only the result assignment takes the intake lock;
// the HTTP calls run outside it.
func (s *Server) searchTMDBWith(in *Intake, query string, year int) {
	var results []tmdb.Result
	errStr := ""
	if s.tmdb == nil {
		errStr = "TMDB is not configured (set tmdb.api_key or CINEROUTE_TMDB_API_KEY)"
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		type attempt struct {
			q string
			y int
		}
		attempts := []attempt{{query, year}}
		if in.Class.AltTitle != "" && !strings.EqualFold(in.Class.AltTitle, query) {
			attempts = append(attempts, attempt{in.Class.AltTitle, 0})
		}
		if year > 0 {
			attempts = append(attempts, attempt{query, 0})
		}

		for _, a := range attempts {
			var err error
			if in.Class.MediaType == "tv" {
				results, err = s.tmdb.SearchTV(ctx, a.q, a.y)
			} else {
				results, err = s.tmdb.SearchMovie(ctx, a.q, a.y)
			}
			if err != nil {
				errStr = err.Error()
				break
			}
			if len(results) > 0 {
				break
			}
		}
	}
	s.mu.Lock()
	in.TMDBResults = results
	in.TMDBError = errStr
	s.mu.Unlock()
}

// autoConfirm matches the top TMDB result for an intake and its group, so
// the destination preview appears without a click. The user can still pick a
// different result afterwards via the match endpoint.
func (s *Server) autoConfirm(in *Intake) {
	s.mu.RLock()
	if in.Status == "submitted" || len(in.TMDBResults) == 0 || in.Match != nil {
		s.mu.RUnlock()
		return
	}
	first := in.TMDBResults[0].ID
	s.mu.RUnlock()
	s.confirmMatch(in, first)
}

// confirmMatch selects a TMDB result and computes the destination preview for
// the intake and the whole group it belongs to, so every part of the same
// show is matched in one go.
func (s *Server) confirmMatch(in *Intake, tmdbID int) {
	s.mu.RLock()
	key := groupKey(in)
	has := findResult(in.TMDBResults, tmdbID) != nil
	s.mu.RUnlock()
	if !has {
		return
	}
	seen := map[*Intake]bool{}
	for _, m := range append([]*Intake{in}, s.groupMembers(key)...) {
		if seen[m] {
			continue
		}
		seen[m] = true
		s.mu.Lock()
		if m.Status == "submitted" {
			s.mu.Unlock()
			continue
		}
		found := findResult(m.TMDBResults, tmdbID)
		if found == nil {
			s.mu.Unlock()
			continue
		}
		m.Match = found
		m.Dest = nil
		m.Error = ""
		s.mu.Unlock()
		dest, warn := s.planDestination(m)
		s.mu.Lock()
		m.Dest = dest
		if warn != "" {
			m.Error = warn
		}
		s.mu.Unlock()
	}
}

// match selects a TMDB result and computes the destination preview for the
// whole group the intake belongs to, so every part of the same show is
// matched in one click.
func (s *Server) match(w http.ResponseWriter, r *http.Request) {
	in, ok := s.getIntake(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "intake not found")
		return
	}
	var body struct {
		TMDBID int `json:"tmdb_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	s.mu.RLock()
	if in.Status == "submitted" || in.Status == "submitting" {
		s.mu.RUnlock()
		writeErr(w, http.StatusConflict, "cannot re-match a submitted intake")
		return
	}
	if findResult(in.TMDBResults, body.TMDBID) == nil {
		s.mu.RUnlock()
		writeErr(w, http.StatusBadRequest, "tmdb result not found")
		return
	}
	s.mu.RUnlock()
	s.confirmMatch(in, body.TMDBID)
	writeJSON(w, http.StatusOK, apiResponse{Intake: toJSON(in)})
}

func findResult(results []tmdb.Result, id int) *tmdb.Result {
	for i := range results {
		if results[i].ID == id {
			return &results[i]
		}
	}
	return nil
}

// planDestination computes the canonical folder, checks the library, and
// selects a drive. It does not hold the allocation lock: the authoritative
// version is recomputed during submit.
func (s *Server) planDestination(in *Intake) (*Destination, string) {
	folder := library.FolderName(s.cfg.Library.FolderFormat, in.Match.DisplayTitle(), in.Match.Year())
	isTV := in.Class.MediaType == "tv"

	var matches []library.Folder
	if isTV {
		matches = s.lib.FindTV(folder)
	} else {
		matches = s.lib.FindMovie(folder)
	}

	d := &Destination{
		FolderName:  folder,
		RootFolder:  in.Meta.RootFolder,
		NeededBytes: in.Meta.Size,
	}

	if len(matches) == 1 {
		m := matches[0]
		d.DriveID = m.DriveID
		d.DriveName = m.DriveID
		d.Existing = true
		d.ExistingPaths = []string{m.Path}
		d.SavePath = m.Path
		d.ContentPath = in.Meta.ContentPath(m.Path)
		d.EnoughSpace = true
		// The title stays on its drive regardless of free space; a tight
		// drive only produces a warning.
		if st, ok := s.driveStatus(m.DriveID); ok {
			d.UsableSpace = st.Available
			d.EnoughSpace = st.Available >= in.Meta.Size
			d.Shortfall = in.Meta.Size - st.Available
			if !d.EnoughSpace {
				d.Warnings = append(d.Warnings, fmt.Sprintf(
					"%s has only %s free (torrent needs %s); adding anyway to keep the title on its drive",
					m.DriveID, humanBytes(st.Available), humanBytes(in.Meta.Size)))
			}
		}
		return d, ""
	}

	if len(matches) > 1 {
		d.Existing = true
		for _, m := range matches {
			d.ExistingPaths = append(d.ExistingPaths, m.Path)
		}
		d.EnoughSpace = false
		return d, "this title exists on multiple drives; resolve the duplicates before submitting"
	}

	// New title: choose the drive with the most free space.
	pending := s.pendingReservations()
	sel, err := s.alloc.Select(s.cfg.Drives, pending, in.Meta.Size)
	if err != nil {
		d.EnoughSpace = false
		d.Shortfall = in.Meta.Size
		return d, err.Error()
	}
	root := sel.Drive.TVRoot
	if !isTV {
		root = sel.Drive.MovieRoot
	}
	d.DriveID = sel.Drive.ID
	d.DriveName = sel.Drive.ID
	d.SavePath = root + "/" + folder
	d.ContentPath = in.Meta.ContentPath(d.SavePath)
	d.UsableSpace = sel.Status.Available
	d.EnoughSpace = d.UsableSpace >= in.Meta.Size
	d.Shortfall = in.Meta.Size - d.UsableSpace
	return d, ""
}

// driveStatus reports the plain free space of one drive.
func (s *Server) driveStatus(id string) (allocator.DriveStatus, bool) {
	for _, st := range s.alloc.Statuses(s.cfg.Drives) {
		if st.ID == id {
			return st, true
		}
	}
	return allocator.DriveStatus{}, false
}

func humanBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".") + " " + units[i]
}

func newID() string {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000000000000000000000000000"
	}
	id := make([]byte, 32)
	for i, v := range b {
		id[i*2] = hexdigits[v>>4]
		id[i*2+1] = hexdigits[v&0xf]
	}
	return string(id)
}
