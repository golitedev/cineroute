package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"cineroute/internal/classifier"
	"cineroute/internal/library"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

type apiResponse struct {
	Intake *intakeJSON `json:"intake,omitempty"`
	Error  string      `json:"error,omitempty"`
	Status string      `json:"status,omitempty"`
}

type intakeJSON struct {
	ID          string        `json:"id"`
	Filename    string        `json:"filename"`
	TorrentName string        `json:"torrent_name"`
	Size        int64         `json:"size"`
	Kind        string        `json:"kind"`
	RootFolder  bool          `json:"root_folder"`
	RootName    string        `json:"root_name"`
	Files       int           `json:"files"`
	InfoHashV1  string        `json:"info_hash_v1"`
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
		Filename:    in.Filename,
		TorrentName: in.Meta.Name,
		Size:        in.Meta.Size,
		Kind:        string(in.Meta.Kind),
		RootFolder:  in.Meta.RootFolder,
		RootName:    in.Meta.RootName,
		Files:       len(in.Meta.Files),
		InfoHashV1:  in.Meta.InfoHashV1,
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
	ID         string `json:"id"`
	Root       string `json:"root"`
	Total      int64  `json:"total"`
	Available  int64  `json:"available"`
	Reserve    int64  `json:"reserve"`
	Incomplete int64  `json:"incomplete"`
	Usable     int64  `json:"usable"`
	Healthy    bool   `json:"healthy"`
	Err        string `json:"err"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := struct {
		TMDB        string            `json:"tmdb"`
		QBittorrent string            `json:"qbittorrent"`
		QBVersion   string            `json:"qb_version"`
		QBWebAPI    string            `json:"qb_webapi"`
		Preallocate string            `json:"preallocate"`
		TempPath    string            `json:"temp_path"`
		Drives      []driveStatusJSON `json:"drives"`
		Auth        bool              `json:"auth"`
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
	for _, st := range s.alloc.Statuses(r.Context(), s.cfg.Drives) {
		out.Drives = append(out.Drives, driveStatusJSON{
			ID: st.ID, Root: st.MovieRoot, Total: st.Total, Available: st.Available,
			Reserve: st.Reserve, Incomplete: st.Incomplete, Usable: st.Usable,
			Healthy: st.Healthy, Err: st.Err,
		})
	}
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

// upload accepts the multipart .torrent upload, parses it, classifies it and
// runs an initial TMDB search.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(s.cfg.MaxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "upload too large or malformed")
		return
	}
	file, header, err := r.FormFile("torrents")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing torrent file")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, s.cfg.MaxUploadBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to read upload")
		return
	}
	if int64(len(data)) > s.cfg.MaxUploadBytes {
		writeErr(w, http.StatusBadRequest, "torrent exceeds maximum upload size")
		return
	}
	meta, err := torrentmeta.Parse(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid torrent: "+err.Error())
		return
	}

	cls := classifier.Classify(meta.Name, meta.RelPaths())
	in := &Intake{
		ID:        newID(),
		CreatedAt: time.Now(),
		Filename:  header.Filename,
		Bytes:     data,
		Meta:      meta,
		Class: classifierResult{
			MediaType: cls.MediaType, Title: cls.Title, Year: cls.Year,
			Season: cls.Season, Confidence: cls.Confidence,
		},
		Status: "parsed",
	}
	s.searchTMDB(in)
	s.storeIntake(in)
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

// search re-runs the TMDB search with a manual query.
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
	in.SearchQuery = body.Query
	in.SearchYear = body.Year
	in.Match = nil
	in.Dest = nil
	in.Error = ""
	s.searchTMDBWith(in, body.Query, body.Year)
	writeJSON(w, http.StatusOK, apiResponse{Intake: toJSON(in)})
}

func (s *Server) searchTMDB(in *Intake) {
	s.searchTMDBWith(in, in.Class.Title, in.Class.Year)
}

func (s *Server) searchTMDBWith(in *Intake, query string, year int) {
	in.TMDBResults = nil
	in.TMDBError = ""
	if s.tmdb == nil {
		in.TMDBError = "TMDB is not configured (set tmdb.api_key or CINEROUTE_TMDB_API_KEY)"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var results []tmdb.Result
	var err error
	if in.Class.MediaType == "tv" {
		results, err = s.tmdb.SearchTV(ctx, query, year)
	} else {
		results, err = s.tmdb.SearchMovie(ctx, query, year)
	}
	if err != nil {
		in.TMDBError = err.Error()
		return
	}
	in.TMDBResults = results
}

// match selects a TMDB result and computes the destination preview.
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
	var found *tmdb.Result
	for i := range in.TMDBResults {
		if in.TMDBResults[i].ID == body.TMDBID {
			found = &in.TMDBResults[i]
			break
		}
	}
	if found == nil {
		writeErr(w, http.StatusBadRequest, "tmdb result not found")
		return
	}
	in.Match = found
	in.Dest = nil
	in.Error = ""
	dest, warn := s.planDestination(in)
	in.Dest = dest
	if warn != "" {
		in.Error = warn
	}
	writeJSON(w, http.StatusOK, apiResponse{Intake: toJSON(in)})
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
	sel, err := s.alloc.Select(context.Background(), s.cfg.Drives, pending, in.Meta.Size, "")
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
