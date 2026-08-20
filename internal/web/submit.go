package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"cineroute/internal/library"
	"cineroute/internal/qbittorrent"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

type submissionRequest struct {
	Bytes              []byte
	Filename           string
	Meta               *torrentmeta.MetaInfo
	MediaType          string
	Match              tmdb.Result
	RequireExisting    bool
	UseMovieRemoteRoot bool
	UseTVRemoteRoot    bool
}

type submissionOutcome struct {
	Dest   *Destination
	Result *SubmitResult
}

// pendingReservations returns the bytes each drive has already reserved by
// intakes that are not yet submitted.
func (s *Server) pendingReservations() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int64{}
	for _, in := range s.intakes {
		if in.Status != "submitted" && in.Dest != nil && in.Dest.DriveID != "" && in.Dest.NeededBytes > 0 {
			out[in.Dest.DriveID] += in.Dest.NeededBytes
		}
	}
	return out
}

// submit runs the intake transaction under the allocation lock for every
// ready member of the intake's stack group: readiness gate, authoritative
// rescan, drive selection, folder creation, stopped add, exact verification,
// explicit start. A failure in one member does not block the others.
func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	in, ok := s.getIntake(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "intake not found")
		return
	}
	s.mu.RLock()
	if in.Match == nil {
		s.mu.RUnlock()
		writeErr(w, http.StatusBadRequest, "a TMDB match is required before submitting")
		return
	}
	key := groupKey(in)
	s.mu.RUnlock()

	s.allocMu.Lock()
	defer s.allocMu.Unlock()

	members := []*Intake{}
	for _, m := range s.groupMembers(key) {
		s.mu.RLock()
		skip := m.Status == "submitted" || m.Match == nil
		s.mu.RUnlock()
		if skip {
			continue
		}
		members = append(members, m)
	}
	if len(members) == 0 {
		s.mu.RLock()
		out := apiResponse{Intake: toJSON(in)}
		s.mu.RUnlock()
		writeJSON(w, http.StatusOK, out)
		return
	}

	failed := false
	for _, m := range members {
		s.mu.Lock()
		m.Status = "submitting"
		m.Error = ""
		m.Dest = nil
		s.mu.Unlock()
		if err := s.submitLocked(r.Context(), m); err != nil {
			s.mu.Lock()
			m.Status = "failed"
			m.Error = err.Error()
			s.mu.Unlock()
			s.pushHistory(m)
			if m.ID == in.ID {
				failed = true
			}
			continue
		}
		s.mu.Lock()
		m.Status = "submitted"
		s.mu.Unlock()
		s.pushHistory(m)
	}

	s.mu.RLock()
	out := apiResponse{Intake: toJSON(in)}
	for _, m := range s.groupMembersLocked(key) {
		out.Intakes = append(out.Intakes, toJSON(m))
	}
	s.mu.RUnlock()
	status := http.StatusOK
	if failed {
		status = http.StatusConflict
	}
	writeJSON(w, status, out)
}

func (s *Server) submitLocked(ctx context.Context, in *Intake) error {
	// Snapshot everything the transaction needs so a concurrent listing or
	// other request can never race with the long-running submit.
	s.mu.RLock()
	if in.Match == nil {
		s.mu.RUnlock()
		return errors.New("the TMDB match was reset before submitting; match the intake again")
	}
	match := *in.Match
	meta := in.Meta
	class := in.Class
	bytes := in.Bytes
	filename := in.Filename
	s.mu.RUnlock()
	outcome, err := s.submitTorrent(ctx, submissionRequest{
		Bytes:     bytes,
		Filename:  filename,
		Meta:      meta,
		MediaType: class.MediaType,
		Match:     match,
	})
	if outcome != nil {
		s.mu.Lock()
		in.Dest = outcome.Dest
		if outcome.Result != nil {
			in.Result = outcome.Result
			in.Bytes = nil
		}
		s.mu.Unlock()
	}
	return err
}

// submitTorrent is the single qBittorrent submission transaction used by both
// normal intake and companion approval. Normal intake holds the allocation lock
// around its caller because it may select a new drive. Companion approval uses
// RequireExisting and can run concurrently because it never allocates a drive.
func (s *Server) submitTorrent(ctx context.Context, req submissionRequest) (*submissionOutcome, error) {
	if req.Meta == nil {
		return nil, errors.New("torrent metadata is missing")
	}
	if req.Filename == "" {
		req.Filename = "cineroute.torrent"
	}

	// 1. Readiness gate: versions, preferences.
	if err := s.ready(ctx); err != nil {
		return nil, err
	}

	// 2. Duplicate check against qBittorrent.
	if hashes := req.Meta.QueryHashes(); hashes != "" {
		ts, err := s.qb.Torrents(ctx, map[string][]string{"hashes": {hashes}})
		if err != nil {
			return nil, fmt.Errorf("duplicate check failed: %w", err)
		}
		if len(ts) > 0 {
			return nil, fmt.Errorf("this torrent is already in qBittorrent (%s)", ts[0].Name)
		}
	}

	// 3. Authoritative destination: fresh library scan + fresh space.
	isTV := req.MediaType == "tv"
	if req.UseMovieRemoteRoot && isTV {
		return nil, errors.New("remote companion destinations are only supported for movies")
	}
	if req.UseTVRemoteRoot && !isTV {
		return nil, errors.New("TV remote companion destinations are only supported for TV shows")
	}
	itemLabel := "movie"
	if isTV {
		itemLabel = "TV show"
	}
	folder := library.FolderName(s.cfg.Library.FolderFormat, req.Match.DisplayTitle(), req.Match.Year())
	var matches []library.Folder
	if isTV {
		matches = s.lib.FindTV(folder)
	} else {
		matches = s.lib.FindMovie(folder)
	}
	if req.RequireExisting {
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("%s folder no longer exists; rescan the library before approving the companion", itemLabel)
		case 1:
			// Use the only canonical folder below.
		default:
			return nil, fmt.Errorf("%s exists on multiple drives; resolve the duplicate folders before approving the companion", itemLabel)
		}
	}

	var savePath string
	var driveID string
	switch {
	case len(matches) == 1:
		// An existing show/movie stays on its drive regardless of free
		// space; a tight drive only produces a warning.
		savePath = matches[0].Path
		driveID = matches[0].DriveID
		if req.UseMovieRemoteRoot {
			// An omitted remote root preserves the legacy companion
			// destination for older configurations.
			if remotePath, ok := s.lib.MovieRemotePath(driveID, folder); ok {
				savePath = remotePath
			}
		} else if req.UseTVRemoteRoot {
			if remotePath, ok := s.lib.TVRemotePath(driveID, folder); ok {
				savePath = remotePath
			}
		}
	case len(matches) > 1:
		return nil, errors.New("this title exists on multiple drives; resolve the duplicates before submitting")
	default:
		if req.RequireExisting {
			return nil, fmt.Errorf("%s folder no longer exists; rescan the library before approving the companion", itemLabel)
		}
		pending := s.pendingReservations()
		sel, err := s.alloc.Select(s.cfg.Drives, pending, req.Meta.Size)
		if err != nil {
			return nil, err
		}
		driveID = sel.Drive.ID
		root := sel.Drive.TVRoot
		if !isTV {
			root = sel.Drive.MovieRoot
		}
		savePath = root + "/" + folder
	}

	dest := &Destination{
		DriveID:     driveID,
		DriveName:   driveID,
		SavePath:    savePath,
		FolderName:  folder,
		Existing:    len(matches) > 0,
		ContentPath: req.Meta.ContentPath(savePath),
		RootFolder:  req.Meta.RootFolder,
		NeededBytes: req.Meta.Size,
		EnoughSpace: true,
	}
	if st, ok := s.driveStatus(driveID); ok {
		dest.UsableSpace = st.Available
		dest.EnoughSpace = st.Available >= req.Meta.Size
		dest.Shortfall = req.Meta.Size - st.Available
		if !dest.EnoughSpace && dest.Existing {
			dest.Warnings = append(dest.Warnings, fmt.Sprintf(
				"%s has only %s free (torrent needs %s); adding anyway to keep the title on its drive",
				driveID, humanBytes(st.Available), humanBytes(req.Meta.Size)))
		}
	}
	out := &submissionOutcome{Dest: dest}

	// 4. Create only the canonical parent folder.
	if err := os.Mkdir(savePath, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return out, fmt.Errorf("creating %s: %w", savePath, err)
	}

	// 5. Add stopped, without a category or tags.
	if err := s.qb.AddTorrent(ctx, req.Bytes, qbittorrent.AddOptions{
		SavePath:   savePath,
		RootFolder: req.Meta.RootFolder,
		Stopped:    true,
		Filename:   req.Filename,
	}); err != nil {
		return out, fmt.Errorf("adding torrent to qBittorrent: %w", err)
	}

	// 6. Verify the stopped add exactly.
	tor, err := s.waitForHash(ctx, req.Meta.QueryHashes())
	if err != nil {
		return out, err
	}
	// qBittorrent may still be in a transitional state (checkingResumeData,
	// checkingDL, allocating, moving) right after the add becomes visible;
	// wait until it settles into a stopped state before verifying.
	tor, err = s.waitStopped(ctx, tor.Hash)
	if err != nil {
		return out, err
	}
	expectedContent := req.Meta.ContentPath(savePath)
	if err := s.verify(ctx, tor, req.Meta, savePath, expectedContent); err != nil {
		return out, err
	}

	// 7. Start the verified torrent and confirm it left the stopped state.
	if err := s.qb.Start(ctx, tor.Hash); err != nil {
		return out, fmt.Errorf("start request failed: %w", err)
	}
	if err := s.waitStarted(ctx, tor.Hash); err != nil {
		return out, err
	}

	out.Result = &SubmitResult{
		Hash:        tor.Hash,
		TorrentName: tor.Name,
		SavePath:    tor.SavePath,
		ContentPath: tor.ContentPath,
		DriveID:     driveID,
		RootFolder:  req.Meta.RootFolder,
		Files:       len(req.Meta.Files),
		SubmittedAt: time.Now(),
	}
	return out, nil
}

// ready checks qBittorrent versions and preferences.
func (s *Server) ready(ctx context.Context) error {
	api, err := s.qb.WebAPIVersion(ctx)
	if err != nil {
		return fmt.Errorf("qBittorrent unavailable: %w", err)
	}
	major, minor, ok := parseVersion(api)
	if !ok || major < 2 || (major == 2 && minor < 11) {
		return fmt.Errorf("unsupported qBittorrent Web API version %s (2.11+ required)", api)
	}
	prefs, err := s.qb.Preferences(ctx)
	if err != nil {
		return fmt.Errorf("qBittorrent preferences unavailable: %w", err)
	}
	if prefs.PreallocateAll {
		return errors.New("qBittorrent preallocation is enabled; disable \"Pre-allocate disk space\" before submitting")
	}
	if prefs.TempPathEnabled {
		return errors.New("qBittorrent incomplete torrent path is enabled; disable \"Keep incomplete torrents in\" before submitting")
	}
	return nil
}

// waitForHash polls the torrent list until the freshly added torrent shows
// up. The torrent is located by its info hash (v1 and/or v2) instead of a
// tag, so the add carries no tags or category.
func (s *Server) waitForHash(ctx context.Context, hashes string) (*qbittorrent.Torrent, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		ts, err := s.qb.Torrents(ctx, map[string][]string{"hashes": {hashes}})
		if err != nil {
			return nil, err
		}
		if len(ts) == 1 {
			return &ts[0], nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("qBittorrent did not confirm the add within 30s")
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// waitStopped polls until the torrent leaves transitional states and is
// stopped. On deadline it returns the last-seen torrent so the caller can
// still run verification and report the actual state.
func (s *Server) waitStopped(ctx context.Context, hash string) (*qbittorrent.Torrent, error) {
	deadline := time.Now().Add(2 * time.Minute)
	var last *qbittorrent.Torrent
	for {
		ts, err := s.qb.Torrents(ctx, url.Values{"hashes": {hash}})
		if err != nil {
			return nil, err
		}
		if len(ts) > 0 {
			t := ts[0]
			last = &t
			if len(ts) == 1 && ts[0].Stopped() {
				return last, nil
			}
		}
		if time.Now().After(deadline) {
			if last == nil {
				return nil, fmt.Errorf("torrent %s not found in qBittorrent", hash)
			}
			return last, nil
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// verify checks every invariant of the stopped torrent against the parsed
// torrent and the expected destination. The torrent is never started on
// failure.
func (s *Server) verify(ctx context.Context, tor *qbittorrent.Torrent, meta *torrentmeta.MetaInfo, wantSave, wantContent string) error {
	problems := []string{}
	if want := meta.PrimaryHash(); want != "" && !strings.EqualFold(tor.Hash, want) {
		problems = append(problems, fmt.Sprintf("hash: got %s want %s", tor.Hash, want))
	}
	if strings.TrimRight(tor.SavePath, "/") != strings.TrimRight(wantSave, "/") {
		problems = append(problems, fmt.Sprintf("save_path: got %q want %q", tor.SavePath, wantSave))
	}
	if strings.TrimRight(tor.ContentPath, "/") != strings.TrimRight(wantContent, "/") {
		problems = append(problems, fmt.Sprintf("content_path: got %q want %q", tor.ContentPath, wantContent))
	}
	if tor.Category != "" {
		problems = append(problems, fmt.Sprintf("category: got %q, want none", tor.Category))
	}
	if tor.Tags != "" {
		problems = append(problems, fmt.Sprintf("tags: got %q, want none", tor.Tags))
	}
	if tor.AutoTMM {
		problems = append(problems, "auto_tmm is true")
	}
	if tor.TotalSize != meta.Size {
		problems = append(problems, fmt.Sprintf("total_size: got %d want %d", tor.TotalSize, meta.Size))
	}
	if !tor.Stopped() {
		problems = append(problems, fmt.Sprintf("state: got %q, expected stopped", tor.State))
	}
	if len(problems) == 0 {
		if err := s.verifyFiles(ctx, tor.Hash, meta); err != nil {
			return err
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("verification failed (%s remains stopped):\n  %s", tor.Hash, strings.Join(problems, "\n  "))
	}
	return nil
}

func (s *Server) verifyFiles(ctx context.Context, hash string, meta *torrentmeta.MetaInfo) error {
	files, err := s.qb.Files(ctx, hash)
	if err != nil {
		return fmt.Errorf("reading torrent files: %w", err)
	}
	want := map[string]int64{}
	for i, p := range meta.FullPaths() {
		want[p] = meta.Files[i].Length
	}
	problems := []string{}
	gotCount := 0
	for _, f := range files {
		gotCount++
		wantLen, ok := want[f.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("unexpected file %q", f.Name))
			continue
		}
		if f.Size != wantLen {
			problems = append(problems, fmt.Sprintf("file %q size %d want %d", f.Name, f.Size, wantLen))
		}
		delete(want, f.Name)
	}
	if gotCount != len(meta.Files) {
		problems = append(problems, fmt.Sprintf("file count: got %d want %d", gotCount, len(meta.Files)))
	}
	for name := range want {
		problems = append(problems, fmt.Sprintf("missing file %q", name))
	}
	if len(problems) > 0 {
		return fmt.Errorf("file verification failed:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func (s *Server) waitStarted(ctx context.Context, hash string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		ts, err := s.qb.Torrents(ctx, map[string][]string{"hashes": {hash}})
		if err != nil {
			return err
		}
		if len(ts) == 1 && !ts[0].Stopped() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("torrent did not leave the stopped state within 30s (verify manually)")
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func parseVersion(v string) (major, minor int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minor); err != nil {
		return 0, 0, false
	}
	return major, minor, true
}
