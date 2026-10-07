//go:build linux || darwin

package store

import (
	"math"
	"syscall"
)

// diskFree returns the bytes available to unprivileged users on the
// filesystem holding dir.
func diskFree(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if free > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(free), nil
}
