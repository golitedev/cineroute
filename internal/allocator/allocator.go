// Package allocator selects the physical drive for a new title based on the
// most usable free space, accounting for configured reserves and qBittorrent's
// committed incomplete bytes on each drive.
package allocator

import (
	"context"
	"fmt"
	"syscall"

	"cineroute/internal/config"
	"cineroute/internal/qbittorrent"
)

type DriveStatus struct {
	ID         string
	MovieRoot  string
	TVRoot     string
	Total      int64
	Available  int64
	Reserve    int64
	Incomplete int64
	Usable     int64
	Healthy    bool
	Err        string
}

type Selection struct {
	Drive  config.Drive
	Status DriveStatus
}

type Allocator struct {
	qb *qbittorrent.Client
}

func New(qb *qbittorrent.Client) *Allocator {
	return &Allocator{qb: qb}
}

func statfs(path string) (*syscall.Statfs_t, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// torrentSnapshot fetches the full qBittorrent torrent list once per
// Statuses/Select invocation. On error it returns a nil slice so callers can
// treat incomplete bytes as 0 for every drive (the previous lenient behavior
// where the per-drive fetch error was ignored).
func (a *Allocator) torrentSnapshot(ctx context.Context) ([]qbittorrent.Torrent, error) {
	if a.qb == nil {
		return nil, nil
	}
	ts, err := a.qb.Torrents(ctx, nil)
	if err != nil {
		return nil, err
	}
	return ts, nil
}

// driveStatus computes the status of one drive from a shared torrent snapshot
// (so the full torrent list is only fetched once per Statuses/Select) and a
// per-drive statfs.
func (a *Allocator) driveStatus(d config.Drive, incomplete int64) DriveStatus {
	st := DriveStatus{
		ID:        d.ID,
		MovieRoot: d.MovieRoot,
		TVRoot:    d.TVRoot,
		Reserve:   d.ReserveBytes,
	}
	if d.MovieRoot == "" || d.TVRoot == "" {
		st.Err = "configured roots are empty"
		return st
	}
	fs, err := statfs(d.MovieRoot)
	if err != nil {
		st.Err = fmt.Sprintf("statfs: %v", err)
		return st
	}
	st.Total = int64(fs.Blocks) * fs.Bsize
	st.Available = int64(fs.Bavail) * fs.Bsize
	st.Incomplete = incomplete
	st.Usable = st.Available - st.Reserve - st.Incomplete
	if st.Usable < 0 {
		st.Usable = 0
	}
	st.Healthy = true
	return st
}

// Statuses reports the current state of every drive.
func (a *Allocator) Statuses(ctx context.Context, drives []config.Drive) []DriveStatus {
	ts, _ := a.torrentSnapshot(ctx) // lenient: a failed fetch counts as 0 incomplete
	out := make([]DriveStatus, 0, len(drives))
	for _, d := range drives {
		inc := qbittorrent.IncompleteBytesUnder(ts, d.MovieRoot, d.TVRoot)
		out = append(out, a.driveStatus(d, inc))
	}
	return out
}

// Select picks the drive with the most usable space for a new title.
// pending is the bytes already reserved by other intakes.
func (a *Allocator) Select(ctx context.Context, drives []config.Drive, pending map[string]int64, need int64) (Selection, error) {
	ts, _ := a.torrentSnapshot(ctx) // lenient: a failed fetch counts as 0 incomplete
	var best *Selection
	var bestUsable int64
	for _, d := range drives {
		inc := qbittorrent.IncompleteBytesUnder(ts, d.MovieRoot, d.TVRoot)
		st := a.driveStatus(d, inc)
		if !st.Healthy {
			continue
		}
		usable := st.Usable - pending[d.ID]
		if usable < 0 {
			usable = 0
		}
		if usable >= need && (best == nil || usable > bestUsable) {
			sel := Selection{Drive: d, Status: st}
			best = &sel
			bestUsable = usable
		}
	}
	if best == nil {
		return Selection{}, fmt.Errorf("no drive has enough usable space for %d bytes", need)
	}
	return *best, nil
}
