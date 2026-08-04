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

func driveStatus(ctx context.Context, a *Allocator, d config.Drive) DriveStatus {
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
	if a.qb != nil {
		if inc, err := a.qb.IncompleteBytesUnder(ctx, d.MovieRoot, d.TVRoot); err == nil {
			st.Incomplete = inc
		}
	}
	st.Usable = st.Available - st.Reserve - st.Incomplete
	if st.Usable < 0 {
		st.Usable = 0
	}
	st.Healthy = true
	return st
}

// Statuses reports the current state of every drive.
func (a *Allocator) Statuses(ctx context.Context, drives []config.Drive) []DriveStatus {
	out := make([]DriveStatus, 0, len(drives))
	for _, d := range drives {
		out = append(out, driveStatus(ctx, a, d))
	}
	return out
}

// Select picks the drive for a new title. preferred is the drive an existing
// TV show lives on; when set, that drive is used unconditionally if it has
// enough space, and an error is returned if it does not (the show must never
// be split silently). pending is the bytes already reserved by other intakes.
func (a *Allocator) Select(ctx context.Context, drives []config.Drive, pending map[string]int64, need int64, preferred string) (Selection, error) {
	var best *Selection
	var bestUsable int64
	for _, d := range drives {
		st := driveStatus(ctx, a, d)
		if !st.Healthy {
			continue
		}
		usable := st.Usable - pending[d.ID]
		if usable < 0 {
			usable = 0
		}
		if d.ID == preferred {
			if usable < need {
				return Selection{}, fmt.Errorf(
					"%s already has this title but lacks space: need %d bytes, usable %d bytes (shortfall %d)",
					d.ID, need, usable, need-usable)
			}
			return Selection{Drive: d, Status: st}, nil
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
