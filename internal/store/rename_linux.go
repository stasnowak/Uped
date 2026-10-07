package store

import (
	"os"
	"path"
	"syscall"
)

// rename moves fromDir/fromName to toDir/toName (root-relative) with
// renameat(2) on directory handles opened through the root. Both names are
// single segments, so the kernel never resolves a path that could leave the
// data directory, even if a symlink is planted concurrently.
func (s *Store) rename(fromDir, fromName, toDir, toName string) error {
	src, err := s.root.Open(fromDir)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := s.root.Open(toDir)
	if err != nil {
		return err
	}
	defer dst.Close()
	if err := syscall.Renameat(int(src.Fd()), fromName, int(dst.Fd()), toName); err != nil {
		return &os.LinkError{Op: "renameat", Old: path.Join(fromDir, fromName), New: path.Join(toDir, toName), Err: err}
	}
	return nil
}
