// Package allocator selects the physical drive for a new title based on the
// most free space, and reports the plain statfs free bytes of every drive.
package allocator

import (
	"fmt"
	"syscall"

	"cineroute/internal/config"
)

type DriveStatus struct {
	ID        string
	MovieRoot string
	TVRoot    string
	Total     int64
	Available int64
	Healthy   bool
	Err       string
}

type Selection struct {
	Drive  config.Drive
	Status DriveStatus
}

type Allocator struct{}

func New() *Allocator {
	return &Allocator{}
}

func statfs(path string) (*syscall.Statfs_t, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// driveStatus reports the plain free space of one drive. The movie root and
// the tv root of a drive sit on the same volume, so one statfs on the movie
// root yields the free space of the whole drive.
func (a *Allocator) driveStatus(d config.Drive) DriveStatus {
	st := DriveStatus{
		ID:        d.ID,
		MovieRoot: d.MovieRoot,
		TVRoot:    d.TVRoot,
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
	st.Healthy = true
	return st
}

// Statuses reports the plain free space of every drive.
func (a *Allocator) Statuses(drives []config.Drive) []DriveStatus {
	out := make([]DriveStatus, 0, len(drives))
	for _, d := range drives {
		out = append(out, a.driveStatus(d))
	}
	return out
}

// Select picks the drive with the most free space for a new title.
// pending is the bytes already reserved by other intakes.
func (a *Allocator) Select(drives []config.Drive, pending map[string]int64, need int64) (Selection, error) {
	var best *Selection
	var bestFree int64
	for _, d := range drives {
		st := a.driveStatus(d)
		if !st.Healthy {
			continue
		}
		free := st.Available - pending[d.ID]
		if free < 0 {
			free = 0
		}
		if free >= need && (best == nil || free > bestFree) {
			sel := Selection{Drive: d, Status: st}
			best = &sel
			bestFree = free
		}
	}
	if best == nil {
		return Selection{}, fmt.Errorf("no drive has enough free space for %d bytes", need)
	}
	return *best, nil
}
