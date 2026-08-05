package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
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

type driveStatusJSON struct {
	ID          string `json:"id"`
	Root        string `json:"root"`
	Total       int64  `json:"total"`
	Available   int64  `json:"available"`
	TVTotal     int64  `json:"tv_total"`
	TVAvailable int64  `json:"tv_available"`
	Reserve     int64  `json:"reserve"`
	Incomplete  int64  `json:"incomplete"`
	Usable      int64  `json:"usable"`
	TVUsable    int64  `json:"tv_usable"`
	Healthy     bool   `json:"healthy"`
	Err         string `json:"err"`
}

// aggregateStatusJSON totals usable space across all drives for one media
// type. Each drive is measured via the movie root for movies and via the tv
// root for tv, so the two aggregates reflect their own volumes.
type aggregateStatusJSON struct {
	Usable  int64 `json:"usable"`
	Healthy bool  `json:"healthy"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := struct {
		TMDB        string              `json:"tmdb"`
		QBittorrent string              `json:"qbittorrent"`
		QBVersion   string              `json:"qb_version"`
		QBWebAPI    string              `json:"qb_webapi"`
		Preallocate string              `json:"preallocate"`
		TempPath    string              `json:"temp_path"`
		Drives      []driveStatusJSON   `json:"drives"`
		Movies      aggregateStatusJSON `json:"movies"`
		TV          aggregateStatusJSON `json:"tv"`
		Auth        bool                `json:"auth"`
	}{TMDB: "not configured", QBittorrent: "not checked"}
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
	movies := aggregateStatusJSON{Healthy: true}
	tv := aggregateStatusJSON{Healthy: true}
	for _, st := range s.alloc.Statuses(r.Context(), s.cfg.Drives) {
		out.Drives = append(out.Drives, driveStatusJSON{
			ID: st.ID, Root: st.MovieRoot, Total: st.Total, Available: st.Available,
			TVTotal: st.TVTotal, TVAvailable: st.TVAvailable,
			Reserve: st.Reserve, Incomplete: st.Incomplete,
			Usable: st.Usable, TVUsable: st.TVUsable,
			Healthy: st.Healthy, Err: st.Err,
		})
		movies.Usable += st.Usable
		tv.Usable += st.TVUsable
		if !st.Healthy {
			movies.Healthy = false
			tv.Healthy = false
		}
	}
	out.Movies = movies
	out.TV = tv
	out.Auth = s.cfg.AuthPassword != ""
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) historyHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return in, nil
}

// listIntakes reports every active intake, oldest first, so the frontend can
// render the whole stack (grouped by show) after any action.
func (s *Server) listIntakes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.MediaType != "movie" && body.MediaType != "tv" {
		writeErr(w, http.StatusBadRequest, "media_type must be movie or tv")
		return
	}
	in.Class.MediaType = body.MediaType
	in.Match = nil
	in.Dest = nil
	in.Error = ""
	s.searchTMDB(in)
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	body.Query = strings.TrimSpace(body.Query)
	if body.Query == "" {
		writeErr(w, http.StatusBadRequest, "query must not be empty")
		return
	}
	for _, m := range s.groupMembers(groupKey(in)) {
		if m.Status == "submitted" {
			continue
		}
		m.SearchQuery = body.Query
		m.SearchYear = body.Year
		m.Match = nil
		m.Dest = nil
		m.Error = ""
		s.searchTMDBWith(m, body.Query, body.Year)
	}
	writeJSON(w, http.StatusOK, apiResponse{Intake: toJSON(in)})
}

func (s *Server) searchTMDB(in *Intake) {
	s.searchTMDBWith(in, in.Class.Title, in.Class.Year)
}

// searchTMDBWith queries TMDB with a fallback chain so a wrong guessed year
// or a year that is part of the title ("Blade Runner 2049") still finds the
// right title: (1) query+year, (2) alternate title without year,
// (3) query without year.
func (s *Server) searchTMDBWith(in *Intake, query string, year int) {
	in.TMDBResults = nil
	in.TMDBError = ""
	if s.tmdb == nil {
		in.TMDBError = "TMDB is not configured (set tmdb.api_key or CINEROUTE_TMDB_API_KEY)"
		return
	}
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
		var results []tmdb.Result
		var err error
		if in.Class.MediaType == "tv" {
			results, err = s.tmdb.SearchTV(ctx, a.q, a.y)
		} else {
			results, err = s.tmdb.SearchMovie(ctx, a.q, a.y)
		}
		if err != nil {
			in.TMDBError = err.Error()
			return
		}
		if len(results) > 0 {
			in.TMDBResults = results
			return
		}
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if findResult(in.TMDBResults, body.TMDBID) == nil {
		writeErr(w, http.StatusBadRequest, "tmdb result not found")
		return
	}
	for _, m := range s.groupMembers(groupKey(in)) {
		if m.Status == "submitted" {
			continue
		}
		found := findResult(m.TMDBResults, body.TMDBID)
		if found == nil {
			continue
		}
		m.Match = found
		m.Dest = nil
		m.Error = ""
		dest, warn := s.planDestination(m)
		m.Dest = dest
		if warn != "" {
			m.Error = warn
		}
	}
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
		if st, ok := s.driveStatus(context.Background(), m.DriveID); ok {
			d.UsableSpace = st.Usable
			d.EnoughSpace = st.Usable >= in.Meta.Size
			d.Shortfall = in.Meta.Size - st.Usable
			if !d.EnoughSpace {
				d.Warnings = append(d.Warnings, fmt.Sprintf(
					"%s has only %s usable (torrent needs %s); adding anyway to keep the title on its drive",
					m.DriveID, humanBytes(st.Usable), humanBytes(in.Meta.Size)))
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

	// New title: choose the drive with the most usable space.
	pending := s.pendingReservations()
	sel, err := s.alloc.Select(context.Background(), s.cfg.Drives, pending, in.Meta.Size)
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
	d.UsableSpace = sel.Status.Usable
	d.EnoughSpace = d.UsableSpace >= in.Meta.Size
	d.Shortfall = in.Meta.Size - d.UsableSpace
	return d, ""
}

// driveStatus reports the current status of one drive.
func (s *Server) driveStatus(ctx context.Context, id string) (allocator.DriveStatus, bool) {
	for _, st := range s.alloc.Statuses(ctx, s.cfg.Drives) {
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
