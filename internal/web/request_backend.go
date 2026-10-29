package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"cineroute/internal/copies"
	"cineroute/internal/library"
	"cineroute/internal/medialanguages"
	"cineroute/internal/qbittorrent"
	"cineroute/internal/requests"
	"cineroute/internal/subtitles"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

type requestBackend struct{ s *Server }

func (b requestBackend) QueueMovieSubtitles(ctx context.Context, d requests.Download) error {
	if d.MediaType != "movie" || d.Status != "complete" {
		return errors.New("only completed movie requests can enter the automatic subtitle flow")
	}
	var path string
	var size int64
	for file, bytes := range d.FileSizes {
		if !requests.IsVideo(file) || requests.IsExtra(file) {
			continue
		}
		if path != "" {
			return errors.New("automatic movie subtitles require one unambiguous feature video")
		}
		path, size = file, bytes
	}
	if path == "" || b.s.subtitles == nil {
		return errors.New("movie subtitle processing is unavailable")
	}
	return b.s.subtitles.QueueRequestedMovie(ctx, subtitles.MovieFile{DriveID: d.DriveID, Target: d.Target, Path: path, Size: size})
}

func (s *Server) requestMatches(media tmdb.Details) *mediaMatch {
	folder := library.FolderName(s.cfg.Library.FolderFormat, media.DisplayTitle(), media.Year())
	if media.MediaType == "movie" {
		return resolveMediaMatch(s.lib.FindMovie(folder), s.lib.FindMovieRemote(folder))
	}
	main, remote := s.lib.FindTV(folder), s.lib.FindTVRemote(folder)
	plain := library.SanitizeTitle(media.DisplayTitle())
	if plain != folder {
		main = append(main, s.lib.FindTV(plain)...)
		remote = append(remote, s.lib.FindTVRemote(plain)...)
	}
	return resolveMediaMatch(main, remote)
}
func profileFor(settings requests.Settings, kind string) requests.Profile {
	if kind == "tv" {
		return settings.TV
	}
	return settings.Movie
}
func downloadComplete(t qbittorrent.Torrent) bool {
	return t.Progress >= 1 || (t.CompletionOn > 0 && t.AmountLeft == 0) || slices.Contains([]string{"uploading", "stalledUP", "queuedUP", "forcedUP", "stoppedUP", "pausedUP", "checkingUP"}, t.State)
}
func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (b requestBackend) Inventory(ctx context.Context, job requests.Job, downloads []requests.Download) (requests.Inventory, error) {
	out := requests.Inventory{Episodes: map[string]requests.Existing{}}
	match := b.s.requestMatches(job.Media)
	if match == nil {
		return out, nil
	}
	if match.Conflict {
		return out, errors.New("title exists on multiple drives or in conflicting folders")
	}
	var folder *library.Folder
	if job.Target == "remote" {
		folder = match.Remote
	} else {
		folder = match.Primary
	}
	if folder == nil {
		return out, nil
	}
	files, err := library.WalkVideoFiles(folder.Path)
	if err != nil {
		return out, err
	}
	if len(files) == 0 {
		return out, nil
	}
	excluded, sizes := requests.DownloadFileIndex(downloads)
	// qBittorrent is never queried here: once a request's own download has been
	// recorded, the filesystem and CineRoute's own records are the only inputs.
	settings, err := b.s.requests.Load()
	if err != nil {
		return out, err
	}
	profile := profileFor(settings, job.Media.MediaType)
	for _, relative := range files {
		path := filepath.Join(folder.Path, relative)
		if requests.IsExtra(relative) || excluded[path] {
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() < sizes[path] {
			continue
		}
		evidence := relative
		var reportedLanguages medialanguages.Metadata
		for _, d := range downloads {
			if d.Status == "complete" && scopeDownload(d, job) && slices.Contains(d.Files, path) {
				evidence = d.Title + " " + relative
				reportedLanguages = d.Languages
				break
			}
		}
		size := info.Size()
		if job.Media.MediaType == "tv" {
			if count := len(requests.EpisodeKeys(relative)); count > 1 {
				size /= int64(count)
			}
		}
		existing := requests.AssessExisting(evidence, size, job.Media, profile, reportedLanguages)
		if job.Media.MediaType == "movie" {
			if out.Movie == nil || (existing.Conforms && !out.Movie.Conforms) || (existing.Conforms == out.Movie.Conforms && requests.Improved(existing.Quality, existing.Score, out.Movie.Quality, out.Movie.Score, profile)) {
				copy := existing
				out.Movie = &copy
			}
		} else {
			for _, key := range requests.EpisodeKeys(relative) {
				old, found := out.Episodes[key]
				if !found || (existing.Conforms && !old.Conforms) || (existing.Conforms == old.Conforms && requests.Improved(existing.Quality, existing.Score, old.Quality, old.Score, profile)) {
					out.Episodes[key] = existing
				}
			}
		}
	}
	return out, nil
}

func (b requestBackend) Submit(ctx context.Context, job requests.Job, candidate requests.Candidate, meta *torrentmeta.MetaInfo, raw []byte, episodes []string, planned func(requests.Download) error) (requests.Download, error) {
	s := b.s
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	var d requests.Download
	if s.qb == nil {
		return d, errors.New("qBittorrent is not configured")
	}
	current, err := s.requests.Job(job.ID)
	if err != nil {
		return d, err
	}
	if current.Status == "cancelled" || current.Status == "declined" {
		return d, errors.New("request was withdrawn")
	}
	pin, err := s.requests.Placement(job.Media.MediaType, job.Media.ID)
	if err != nil {
		return d, err
	}
	folder := library.FolderName(s.cfg.Library.FolderFormat, job.Media.DisplayTitle(), job.Media.Year())
	match := s.requestMatches(job.Media)
	if match != nil {
		if match.Conflict {
			return d, errors.New("conflicting library folders require review")
		}
		if pin != "" && pin != match.DriveID {
			return d, errors.New("library folder conflicts with the assigned disk")
		}
		pin = match.DriveID
		source := match.Primary
		if job.Target == "remote" && match.Remote != nil {
			source = match.Remote
		}
		if source == nil {
			source = match.Remote
		}
		if source != nil {
			folder = source.Name
		}
	}
	all, err := s.requests.Downloads()
	if err != nil {
		return d, err
	}
	reservations := map[string]int64{}
	for _, item := range all {
		if item.Status == "planned" || item.Status == "downloading" {
			reservations[item.DriveID] += int64(float64(item.Size) * (1 - item.Progress))
		}
	}
	makePlan := func(dest *Destination) error {
		if dest.ExistingConflict {
			return errors.New("conflicting folders")
		}
		if err := validateNewRequestPaths(dest.SavePath, meta.FullPaths()); err != nil {
			return err
		}
		if err := s.requests.Pin(job.Media.MediaType, job.Media.ID, dest.DriveID); err != nil {
			return err
		}
		d = requests.Download{Hash: meta.PrimaryHash(), MediaType: job.Media.MediaType, TMDBID: job.Media.ID, Target: job.Target, DriveID: dest.DriveID, SavePath: dest.SavePath, ContentPath: meta.ContentPath(dest.SavePath), TorrentData: raw, ReleaseKey: requests.ReleaseKey(candidate.Release), Title: candidate.Release.Title, Quality: candidate.Quality, Score: candidate.Score, Size: meta.Size, Episodes: episodes, Status: "planned", AddedAt: time.Now().UTC(), LastProgress: time.Now().UTC(), FileSizes: map[string]int64{}}
		for i, path := range meta.FullPaths() {
			full := filepath.Join(dest.SavePath, path)
			d.Files = append(d.Files, full)
			if !meta.Files[i].Padding {
				d.FileSizes[full] = meta.Files[i].Length
			}
		}
		return planned(d)
	}
	// A completed torrent already in the opposite library can satisfy the
	// request through a same-disk hardlink, without changing its seeding path.
	previous, err := s.qb.Torrents(ctx, url.Values{"hashes": {meta.QueryHashes()}})
	if err != nil {
		return d, err
	}
	if len(previous) > 0 {
		if len(previous) != 1 || match == nil || !downloadComplete(previous[0]) {
			return d, errors.New("this torrent is already downloading; waiting for its existing copy")
		}
		tor := previous[0]
		source := ""
		if match.Primary != nil && filepath.Clean(match.Primary.Path) == filepath.Clean(tor.SavePath) {
			source = match.Primary.Path
		}
		if match.Remote != nil && filepath.Clean(match.Remote.Path) == filepath.Clean(tor.SavePath) {
			source = match.Remote.Path
		}
		if source == "" {
			return d, errors.New("existing torrent is outside this title's configured library folders")
		}
		if err := s.verifyTorrent(ctx, &tor, meta, source, meta.ContentPath(source), false); err != nil {
			return d, err
		}
		target := s.primaryPath(job.Media.MediaType == "tv", match.DriveID, folder)
		if job.Target == "remote" {
			var ok bool
			target, ok = s.remotePath(job.Media.MediaType == "tv", match.DriveID, folder)
			if !ok {
				return d, errors.New("remote root is unavailable")
			}
		}
		d = requests.Download{Hash: meta.PrimaryHash(), MediaType: job.Media.MediaType, TMDBID: job.Media.ID, Target: job.Target, DriveID: match.DriveID, SavePath: target, ContentPath: meta.ContentPath(target), SourcePath: source, TorrentData: raw, ReleaseKey: requests.ReleaseKey(candidate.Release), Title: candidate.Release.Title, Quality: candidate.Quality, Score: candidate.Score, Size: meta.Size, Episodes: episodes, Status: "planned", AddedAt: time.Now().UTC(), LastProgress: time.Now().UTC(), FileSizes: map[string]int64{}}
		for i, path := range meta.FullPaths() {
			full := filepath.Join(target, path)
			d.Files = append(d.Files, full)
			if !meta.Files[i].Padding {
				d.FileSizes[full] = meta.Files[i].Length
			}
		}
		if err = s.requests.Pin(job.Media.MediaType, job.Media.ID, match.DriveID); err != nil {
			return d, err
		}
		if err = planned(d); err != nil {
			return d, err
		}
		return b.finishLink(ctx, d)
	}
	out, err := s.submitTorrent(ctx, submissionRequest{Bytes: raw, Filename: "request-" + job.ID + ".torrent", Meta: meta, MediaType: job.Media.MediaType, Match: job.Media.Result, Remote: job.Target == "remote", FolderName: folder, PreferredDrive: job.PreferredDrive, PinnedDrive: pin, Reservations: reservations, BeforeAdd: makePlan})
	if err != nil {
		return d, err
	}
	if out == nil || out.Result == nil {
		return d, errors.New("submission was not confirmed")
	}
	d.Hash = out.Result.Hash
	d.Status = "downloading"
	d.TorrentData = nil
	return d, nil
}

func (b requestBackend) finishLink(ctx context.Context, d requests.Download) (requests.Download, error) {
	if filepath.Clean(d.SourcePath) != filepath.Clean(d.SavePath) {
		paths := make([]string, 0, len(d.FileSizes))
		for path := range d.FileSizes {
			relative, err := filepath.Rel(d.SavePath, path)
			if err != nil {
				return d, err
			}
			paths = append(paths, relative)
		}
		if _, err := copies.LinkFiles(d.SourcePath, d.SavePath, paths); err != nil {
			return d, err
		}
	}
	for path, size := range d.FileSizes {
		info, err := os.Stat(path)
		if err != nil || info.Size() != size {
			return d, errors.New("linked video files could not be verified")
		}
	}
	d.Progress = 1
	d.Status = "complete"
	d.CompletedAt = time.Now().UTC()
	d.ObservedAt = d.CompletedAt
	d.TorrentData = nil
	return d, nil
}
func (b requestBackend) Observe(ctx context.Context, d requests.Download) (requests.Download, error) {
	if b.s.qb == nil {
		return d, errors.New("qBittorrent is not configured")
	}
	if d.Status == "planned" && d.SourcePath != "" {
		return b.finishLink(ctx, d)
	}
	if d.Status == "planned" {
		if err := b.s.ready(ctx); err != nil {
			return d, err
		}
	}
	ts, err := b.s.qb.Torrents(ctx, url.Values{"hashes": {d.Hash}})
	if err != nil {
		return d, err
	}
	if len(ts) == 0 && d.Status == "planned" {
		meta, err := torrentmeta.Parse(d.TorrentData)
		if err != nil {
			return d, err
		}
		if err = b.s.ready(ctx); err != nil {
			return d, err
		}
		b.s.allocMu.Lock()
		defer b.s.allocMu.Unlock()
		if err := validateNewRequestPaths(d.SavePath, meta.FullPaths()); err != nil {
			return d, fmt.Errorf("interrupted add needs administrator review: %w", err)
		}
		if err = os.MkdirAll(d.SavePath, 0o755); err != nil {
			return d, err
		}
		if err = b.s.qb.AddTorrent(ctx, d.TorrentData, qbittorrent.AddOptions{SavePath: d.SavePath, RootFolder: meta.RootFolder, Stopped: true, Filename: "request-recovery.torrent"}); err != nil {
			return d, err
		}
		tor, err := b.s.waitForHash(ctx, meta.QueryHashes())
		if err != nil {
			return d, err
		}
		ts = []qbittorrent.Torrent{*tor}
	}
	if len(ts) != 1 {
		d.Status = "failed"
		d.Error = "Torrent was removed from qBittorrent"
		return d, nil
	}
	tor := ts[0]
	d.TorrentState = tor.State
	d.ObservedAt = time.Now().UTC()
	d.Paused = tor.Stopped()
	if d.Status == "planned" {
		meta, err := torrentmeta.Parse(d.TorrentData)
		if err != nil {
			return d, err
		}
		if tor.Stopped() {
			settled, err := b.s.waitStopped(ctx, tor.Hash)
			if err != nil {
				return d, err
			}
			tor = *settled
		}
		if err = b.s.verifyTorrent(ctx, &tor, meta, d.SavePath, d.ContentPath, tor.Stopped()); err != nil {
			return d, err
		}
		if tor.Stopped() {
			if err = b.s.qb.Start(ctx, tor.Hash); err != nil {
				return d, err
			}
		}
		d.Paused = false
		d.Status = "downloading"
		d.TorrentData = nil
	}
	if filepath.Clean(tor.SavePath) != filepath.Clean(d.SavePath) || filepath.Clean(tor.ContentPath) != filepath.Clean(d.ContentPath) {
		return d, errors.New("request torrent was moved outside its verified destination")
	}
	if tor.State == "error" || tor.State == "missingFiles" {
		d.Status = "failed"
		d.Error = "qBittorrent reported " + tor.State
		return d, nil
	}
	progress := tor.Progress
	if downloadComplete(tor) {
		progress = 1
	}
	if progress > d.Progress {
		d.LastProgress = time.Now().UTC()
	}
	d.Progress = progress
	if progress >= 1 {
		for path, size := range d.FileSizes {
			info, err := os.Stat(path)
			if err != nil || info.Size() != size {
				return d, errors.New("download reports complete but its files are missing or incomplete")
			}
		}
		d.Status = "complete"
		if d.CompletedAt.IsZero() {
			d.CompletedAt = time.Now().UTC()
		}
	}
	return d, nil
}
func (b requestBackend) Pause(ctx context.Context, hash string) error { return b.s.qb.Stop(ctx, hash) }

// Keep the compiler checking the complete backend contract as it evolves.
var _ requests.Backend = requestBackend{}

func scopeDownload(d requests.Download, j requests.Job) bool {
	return d.MediaType == j.Media.MediaType && d.TMDBID == j.Media.ID && d.Target == j.Target
}

// Check both the initial add and crash recovery before creating any paths.
func validateNewRequestPaths(root string, paths []string) error {
	for _, name := range paths {
		full := filepath.Join(root, name)
		if !within(root, full) {
			return errors.New("torrent path escapes its destination")
		}
		if _, err := os.Lstat(full); err == nil {
			return errors.New("release would overwrite an existing file; another release is required")
		} else if !os.IsNotExist(err) {
			return err
		}
		for dir := filepath.Dir(full); within(root, dir); dir = filepath.Dir(dir) {
			info, err := os.Lstat(dir)
			if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
				return errors.New("torrent destination contains a symlink or non-directory")
			}
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if dir == root {
				break
			}
		}
	}
	return nil
}
