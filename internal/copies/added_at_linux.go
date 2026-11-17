package copies

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Prefer directory creation time: media file timestamps often predate import.
// Filesystems without birth time support fall back to directory modification time.
func folderAddedAt(path string) (time.Time, error) {
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stat); err == nil && stat.Mask&unix.STATX_BTIME != 0 {
		return time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)).UTC(), nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime().UTC(), nil
}
