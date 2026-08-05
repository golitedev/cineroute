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
)

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
	if in.Match == nil {
		writeErr(w, http.StatusBadRequest, "a TMDB match is required before submitting")
		return
	}

	s.allocMu.Lock()
	defer s.allocMu.Unlock()

	members := []*Intake{}
	for _, m := range s.groupMembers(groupKey(in)) {
		if m.Status == "submitted" || m.Match == nil {
			continue
		}
		members = append(members, m)
	}
	if len(members) == 0 {
		writeJSON(w, http.StatusOK, apiResponse{Intake: toJSON(in)})
		return
	}

	failed := false
	for _, m := range members {
		m.Status = "submitting"
		m.Error = ""
		m.Dest = nil
		if err := s.submitLocked(r.Context(), m); err != nil {
			m.Status = "failed"
			m.Error = err.Error()
			s.pushHistory(m)
			if m.ID == in.ID {
				failed = true
			}
			continue
		}
		m.Status = "submitted"
		s.pushHistory(m)
	}

	out := apiResponse{Intake: toJSON(in)}
	for _, m := range s.groupMembers(groupKey(in)) {
		out.Intakes = append(out.Intakes, toJSON(m))
	}
	status := http.StatusOK
	if failed {
		status = http.StatusConflict
	}
	writeJSON(w, status, out)
}

func (s *Server) submitLocked(ctx context.Context, in *Intake) error {
	// 1. Readiness gate: versions, preferences, categories.
	if err := s.ready(ctx); err != nil {
		return err
	}

	// 2. Duplicate check against qBittorrent.
	if hashes := in.Meta.QueryHashes(); hashes != "" {
		ts, err := s.qb.Torrents(ctx, map[string][]string{"hashes": {hashes}})
		if err != nil {
			return fmt.Errorf("duplicate check failed: %w", err)
		}
		if len(ts) > 0 {
			return fmt.Errorf("this torrent is already in qBittorrent (%s)", ts[0].Name)
		}
	}

	// 3. Authoritative destination: fresh library scan + fresh space.
	isTV := in.Class.MediaType == "tv"
	folder := library.FolderName(s.cfg.Library.FolderFormat, in.Match.DisplayTitle(), in.Match.Year())
	var matches []library.Folder
	if isTV {
		matches = s.lib.FindTV(folder)
	} else {
		matches = s.lib.FindMovie(folder)
	}

	var savePath string
	var driveID string
	switch {
	case len(matches) == 1:
		// An existing show/movie stays on its drive regardless of free
		// space; a tight drive only produces a warning.
		savePath = matches[0].Path
		driveID = matches[0].DriveID
	case len(matches) > 1:
		return errors.New("this title exists on multiple drives; resolve the duplicates before submitting")
	default:
		pending := s.pendingReservations()
		sel, err := s.alloc.Select(s.cfg.Drives, pending, in.Meta.Size)
		if err != nil {
			return err
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
		ContentPath: in.Meta.ContentPath(savePath),
		RootFolder:  in.Meta.RootFolder,
		NeededBytes: in.Meta.Size,
		EnoughSpace: true,
	}
	if st, ok := s.driveStatus(driveID); ok {
		dest.UsableSpace = st.Available
		dest.EnoughSpace = st.Available >= in.Meta.Size
		dest.Shortfall = in.Meta.Size - st.Available
		if !dest.EnoughSpace && dest.Existing {
			dest.Warnings = append(dest.Warnings, fmt.Sprintf(
				"%s has only %s free (torrent needs %s); adding anyway to keep the title on its drive",
				driveID, humanBytes(st.Available), humanBytes(in.Meta.Size)))
		}
	}
	in.Dest = dest

	// 4. Create only the canonical parent folder.
	if err := os.Mkdir(savePath, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("creating %s: %w", savePath, err)
	}

	// 5. Add stopped with a unique tag.
	tag := "cineroute-" + in.ID
	cat := s.cfg.CategoryFor(in.Class.MediaType)
	tags := "cineroute," + tag
	if in.Match != nil {
		tags += fmt.Sprintf(",tmdb-%d", in.Match.ID)
	}
	if driveID != "" {
		tags += "," + driveID
	}
	if err := s.qb.AddTorrent(ctx, in.Bytes, qbittorrent.AddOptions{
		SavePath:   savePath,
		Category:   cat,
		Tags:       tags,
		RootFolder: in.Meta.RootFolder,
		Stopped:    true,
		Filename:   in.Filename,
	}); err != nil {
		return fmt.Errorf("adding torrent to qBittorrent: %w", err)
	}

	// 6. Verify the stopped add exactly.
	tor, err := s.waitForTag(ctx, tag)
	if err != nil {
		return err
	}
	// qBittorrent may still be in a transitional state (checkingResumeData,
	// checkingDL, allocating, moving) right after the add becomes visible;
	// wait until it settles into a stopped state before verifying.
	tor, err = s.waitStopped(ctx, tor.Hash)
	if err != nil {
		return err
	}
	expectedContent := in.Meta.ContentPath(savePath)
	if err := s.verify(ctx, tor, in, savePath, expectedContent, cat, tag); err != nil {
		return err
	}

	// 7. Start the verified torrent and confirm it left the stopped state.
	if err := s.qb.Start(ctx, tor.Hash); err != nil {
		return fmt.Errorf("start request failed: %w", err)
	}
	if err := s.waitStarted(ctx, tor.Hash); err != nil {
		return err
	}

	in.Result = &SubmitResult{
		Hash:        tor.Hash,
		TorrentName: tor.Name,
		SavePath:    tor.SavePath,
		ContentPath: tor.ContentPath,
		Category:    tor.Category,
		DriveID:     driveID,
		RootFolder:  in.Meta.RootFolder,
		Files:       len(in.Meta.Files),
		SubmittedAt: time.Now(),
	}
	return nil
}

// ready checks qBittorrent versions, preferences and categories.
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
	for _, cat := range []string{s.cfg.QBittorrent.MovieCategory, s.cfg.QBittorrent.TVCategory} {
		if cat == "" {
			continue
		}
		cats, err := s.qb.Categories(ctx)
		if err != nil {
			return err
		}
		if c, exists := cats[cat]; exists {
			if strings.TrimSpace(c.SavePath) != "" {
				return fmt.Errorf("category %s has a save path set; it must be empty", cat)
			}
			continue
		}
		if err := s.qb.EnsureCategory(ctx, cat); err != nil {
			return fmt.Errorf("creating category %s: %w", cat, err)
		}
	}
	return nil
}

func (s *Server) waitForTag(ctx context.Context, tag string) (*qbittorrent.Torrent, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		ts, err := s.qb.Torrents(ctx, map[string][]string{"tag": {tag}})
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
func (s *Server) verify(ctx context.Context, tor *qbittorrent.Torrent, in *Intake, wantSave, wantContent, wantCat, tag string) error {
	problems := []string{}
	if want := in.Meta.PrimaryHash(); want != "" && !strings.EqualFold(tor.Hash, want) {
		problems = append(problems, fmt.Sprintf("hash: got %s want %s", tor.Hash, want))
	}
	if strings.TrimRight(tor.SavePath, "/") != strings.TrimRight(wantSave, "/") {
		problems = append(problems, fmt.Sprintf("save_path: got %q want %q", tor.SavePath, wantSave))
	}
	if strings.TrimRight(tor.ContentPath, "/") != strings.TrimRight(wantContent, "/") {
		problems = append(problems, fmt.Sprintf("content_path: got %q want %q", tor.ContentPath, wantContent))
	}
	if tor.Category != wantCat {
		problems = append(problems, fmt.Sprintf("category: got %q want %q", tor.Category, wantCat))
	}
	if tor.AutoTMM {
		problems = append(problems, "auto_tmm is true")
	}
	if tor.TotalSize != in.Meta.Size {
		problems = append(problems, fmt.Sprintf("total_size: got %d want %d", tor.TotalSize, in.Meta.Size))
	}
	if !tor.Stopped() {
		problems = append(problems, fmt.Sprintf("state: got %q, expected stopped", tor.State))
	}
	if !strings.Contains(tor.Tags, tag) {
		problems = append(problems, fmt.Sprintf("tags: got %q, missing %s", tor.Tags, tag))
	}
	if len(problems) == 0 {
		if err := s.verifyFiles(ctx, tor.Hash, in); err != nil {
			return err
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("verification failed (%s remains stopped):\n  %s", tor.Hash, strings.Join(problems, "\n  "))
	}
	return nil
}

func (s *Server) verifyFiles(ctx context.Context, hash string, in *Intake) error {
	files, err := s.qb.Files(ctx, hash)
	if err != nil {
		return fmt.Errorf("reading torrent files: %w", err)
	}
	want := map[string]int64{}
	for _, p := range in.Meta.RelPaths() {
		want[p] = 0
	}
	for _, f := range in.Meta.Files {
		want[strings.Join(f.RelPath, "/")] = f.Length
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
	if gotCount != len(in.Meta.Files) {
		problems = append(problems, fmt.Sprintf("file count: got %d want %d", gotCount, len(in.Meta.Files)))
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
