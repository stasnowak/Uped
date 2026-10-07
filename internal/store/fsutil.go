package store

import (
	"errors"
	"io/fs"
	"path"
	"strings"
)

// readDir lists a root-relative directory.
func (s *Store) readDir(p string) ([]fs.DirEntry, error) {
	f, err := s.root.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// mkdirAll creates the folders segs inside files/, one level at a time.
func (s *Store) mkdirAll(segs []string) error {
	for i := range segs {
		p := filesPath(segs[:i+1])
		err := s.root.Mkdir(p, dirPerm)
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrExist) {
			return mapErr(err)
		}
		fi, err := s.root.Lstat(p)
		if err != nil {
			return mapErr(err)
		}
		if !fi.IsDir() {
			return ErrNotDir
		}
	}
	return nil
}

// removeAll deletes a root-relative path recursively without following
// symlinks: a symlink is removed itself, never what it points to.
func (s *Store) removeAll(p string) error {
	fi, err := s.root.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		entries, err := s.readDir(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if err := s.removeAll(path.Join(p, e.Name())); err != nil {
				return err
			}
		}
	}
	if err := s.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// resolveDirLocked maps a target folder onto existing folders, matching
// names case-insensitively so "Trip" and "trip" do not become two folders
// that collide when unzipped on Windows or macOS. Segments that do not exist
// yet are returned as given. A file or symlink in the way gives ErrNotDir.
func (s *Store) resolveDirLocked(dir []string) ([]string, error) {
	out := make([]string, 0, len(dir))
	missing := false
	for _, seg := range dir {
		if missing {
			out = append(out, seg)
			continue
		}
		entries, err := s.readDir(filesPath(out))
		if errors.Is(err, fs.ErrNotExist) {
			missing = true
			out = append(out, seg)
			continue
		}
		if err != nil {
			return nil, mapErr(err)
		}
		var match fs.DirEntry
		for _, e := range entries {
			if e.Name() == seg {
				match = e
				break
			}
		}
		if match == nil {
			for _, e := range entries {
				if strings.EqualFold(e.Name(), seg) {
					match = e
					break
				}
			}
		}
		switch {
		case match == nil:
			missing = true
			out = append(out, seg)
		case !match.IsDir():
			return nil, ErrNotDir
		default:
			out = append(out, match.Name())
		}
	}
	return out, nil
}

// takenLocked returns the lowercased names already used in dir, by files on
// disk or by active uploads other than exclude.
func (s *Store) takenLocked(dir []string, exclude string) (map[string]bool, error) {
	taken := map[string]bool{}
	entries, err := s.readDir(filesPath(dir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, mapErr(err)
	}
	for _, e := range entries {
		taken[strings.ToLower(e.Name())] = true
	}
	for id, u := range s.uploads {
		if id != exclude && equalFoldSegs(u.Dir, dir) {
			taken[strings.ToLower(u.Name)] = true
		}
	}
	return taken, nil
}

func equalFoldSegs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

// hasPrefixSegs reports whether p starts with prefix, segment by segment.
func hasPrefixSegs(p, prefix []string) bool {
	if len(prefix) > len(p) {
		return false
	}
	for i := range prefix {
		if p[i] != prefix[i] {
			return false
		}
	}
	return true
}

// moveIntoLocked renames .parts/from to name inside folder dir, creating the
// folder first. If the sweeper removes the folder between creating it and
// the rename (it had just emptied out), the folder is recreated and the
// rename retried once.
func (s *Store) moveIntoLocked(dir []string, from, name string) error {
	if err := s.mkdirAll(dir); err != nil {
		return err
	}
	err := s.rename(partsDir, from, filesPath(dir), name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := s.mkdirAll(dir); err != nil {
			return err
		}
		err = s.rename(partsDir, from, filesPath(dir), name)
	}
	return err
}
