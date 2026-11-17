//go:build !linux

package copies

import (
	"os"
	"time"
)

func folderAddedAt(path string) (time.Time, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime().UTC(), nil
}
