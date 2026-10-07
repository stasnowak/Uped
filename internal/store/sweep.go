package store

import (
	"context"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/stasnowak/Uped/internal/names"
)

// SweepResult counts what one Sweep removed.
type SweepResult struct {
	Files int // finished files past their TTL
	Parts int // unfinished uploads idle longer than PartTTL, plus stray temp files
	Dirs  int // empty folders
}

// Sweep deletes expired files, stale unfinished uploads and empty folders.
// A folder is removed when it is empty and either something inside it was
// removed in this sweep or it has been untouched for emptyDirGrace. The
// files/ root itself is never removed, and symlinks are never followed.
func (s *Store) Sweep(now time.Time) SweepResult {
	var res SweepResult
	changed := map[string]bool{}

	// Collect first, delete afterwards: never mutate a tree mid-walk.
	var dirs, expired []string
	cutoff := now.Add(-s.opts.TTL)
	_ = fs.WalkDir(s.root.FS(), filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		switch {
		case d.IsDir():
			if p != filesDir {
				dirs = append(dirs, p)
			}
		case d.Type().IsRegular() && s.opts.TTL > 0:
			if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
				expired = append(expired, p)
			}
		}
		return nil
	})
	removedIn := map[string]bool{}
	var removedKeys []string
	for _, p := range expired {
		if err := s.root.Remove(p); err == nil {
			res.Files++
			removedIn[path.Dir(p)] = true
			removedKeys = append(removedKeys, strings.TrimPrefix(p, filesDir+"/"))
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- { // deepest first
		p := dirs[i]
		entries, err := s.readDir(p)
		if err != nil || len(entries) > 0 {
			continue
		}
		info, err := s.root.Lstat(p)
		if err != nil {
			continue
		}
		if !removedIn[p] && now.Sub(info.ModTime()) < emptyDirGrace {
			continue
		}
		if err := s.root.Remove(p); err == nil {
			res.Dirs++
			removedIn[path.Dir(p)] = true
			removedKeys = append(removedKeys, strings.TrimPrefix(p, filesDir+"/"))
		}
	}
	for p := range removedIn {
		changed[strings.TrimPrefix(strings.TrimPrefix(p, filesDir), "/")] = true
	}

	s.mu.Lock()
	for _, k := range removedKeys {
		s.dropMetaLocked(k)
	}
	for _, u := range s.uploads {
		info, err := s.root.Stat(partPath(u.ID))
		if err == nil && now.Sub(info.ModTime()) < s.opts.PartTTL {
			continue
		}
		s.abortLocked(u)
		res.Parts++
	}
	// Stray files in .parts (crashed text writes, metadata temp files) that
	// no active upload owns.
	if entries, err := s.readDir(partsDir); err == nil {
		for _, e := range entries {
			id, _, _ := strings.Cut(e.Name(), ".")
			if _, active := s.uploads[id]; active {
				continue
			}
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) >= s.opts.PartTTL {
				if s.root.Remove(partsDir+"/"+e.Name()) == nil {
					res.Parts++
				}
			}
		}
	}
	s.mu.Unlock()

	for dir := range changed {
		segs, err := names.SplitRel(dir)
		if err == nil {
			s.emitChange(segs)
		}
	}
	return res
}

// RunSweeper sweeps now and then every interval until ctx ends.
func (s *Store) RunSweeper(ctx context.Context, interval time.Duration) {
	sweep := func() {
		r := s.Sweep(s.opts.Now())
		if r != (SweepResult{}) {
			s.log.Info("expiry sweep", "files", r.Files, "unfinished_uploads", r.Parts, "folders", r.Dirs)
		}
	}
	sweep()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
