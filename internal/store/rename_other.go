//go:build !linux

package store

import (
	"os"
	"path/filepath"
)

// rename is the portable fallback for development on non-Linux systems: it
// checks both folders through the root, then renames by path. Production
// builds target Linux and use renameat(2) instead.
func (s *Store) rename(fromDir, fromName, toDir, toName string) error {
	for _, d := range []string{fromDir, toDir} {
		if _, err := s.root.Stat(d); err != nil {
			return err
		}
	}
	return os.Rename(filepath.Join(s.dir, fromDir, fromName), filepath.Join(s.dir, toDir, toName))
}
