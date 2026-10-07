package store

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/stasnowak/Uped/internal/names"
)

// metaEntry is what the store remembers about a finished file beyond what
// the filesystem records. It is cosmetic or an optimisation: losing
// meta.json loses device labels and re-upload detection, nothing else.
type metaEntry struct {
	Device      string `json:"device,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// loadMeta reads meta.json and drops entries whose file is gone.
func (s *Store) loadMeta() {
	f, err := s.root.Open(metaFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		s.log.Warn("cannot read metadata, starting without it", "err", err)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err == nil {
		err = json.Unmarshal(data, &s.meta)
	}
	if err != nil {
		s.log.Warn("metadata file is unreadable, starting without it", "err", err)
		s.meta = map[string]metaEntry{}
		s.metaDirty = true
		return
	}
	for key := range s.meta {
		segs, err := names.SplitRel(key)
		if err != nil || len(segs) == 0 || names.Join(segs) != key {
			delete(s.meta, key)
			s.metaDirty = true
			continue
		}
		if fi, err := s.root.Lstat(filesPath(segs)); err != nil || !fi.Mode().IsRegular() {
			delete(s.meta, key)
			s.metaDirty = true
		}
	}
}

// markMetaDirtyLocked schedules a meta.json write within metaFlushDelay, so
// a folder of thousands of files costs a handful of writes, not thousands.
func (s *Store) markMetaDirtyLocked() {
	s.metaDirty = true
	if s.metaTimer != nil || s.closed {
		return
	}
	s.metaTimer = time.AfterFunc(metaFlushDelay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.metaTimer = nil
		if s.closed {
			return
		}
		if err := s.saveMetaLocked(); err != nil {
			s.log.Error("cannot write metadata", "err", err)
		}
	})
}

// saveMetaLocked writes meta.json atomically: temp file, fsync, rename.
func (s *Store) saveMetaLocked() error {
	if !s.metaDirty {
		return nil
	}
	data, err := json.Marshal(s.meta)
	if err != nil {
		return err
	}
	tmp := metaFile + ".tmp"
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = s.rename(".", tmp, ".", metaFile)
	}
	if err != nil {
		_ = s.root.Remove(tmp)
		return err
	}
	s.metaDirty = false
	return nil
}

// dropMetaLocked forgets key and, when it is a folder, everything under it.
func (s *Store) dropMetaLocked(key string) {
	for k := range s.meta {
		if k == key || strings.HasPrefix(k, key+"/") {
			delete(s.meta, k)
			s.metaDirty = true
		}
	}
	if s.metaDirty {
		s.markMetaDirtyLocked()
	}
}

// FlushMeta writes pending metadata now instead of after the batching delay.
func (s *Store) FlushMeta() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveMetaLocked()
}
