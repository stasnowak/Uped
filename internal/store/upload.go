package store

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stasnowak/Uped/internal/names"
)

// uploadMeta is persisted in .parts/<id>.json when an upload is reserved.
// The offset is not stored: it is the size of .parts/<id>.part, and the
// part's mtime records the last activity.
type uploadMeta struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"` // reserved final name, already deduplicated
	Dir         []string  `json:"dir"`  // target folder segments under files/
	Size        int64     `json:"size"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Device      string    `json:"device,omitempty"`
	Created     time.Time `json:"created"`
}

type upload struct {
	uploadMeta
	offset atomic.Int64 // bytes in the part file

	mu        sync.Mutex // serialises Append and Finish for this upload
	lastEvent time.Time  // guarded by mu
	gone      bool       // finished or aborted; guarded by Store.mu
}

func (u *upload) info(state string) UploadInfo {
	return UploadInfo{
		ID:      u.ID,
		Name:    u.Name,
		Dir:     names.Join(u.Dir),
		Size:    u.Size,
		Offset:  u.offset.Load(),
		Device:  u.Device,
		State:   state,
		Created: u.Created,
	}
}

// Reserve starts an upload of size bytes named name into folder dir.
//
// If an active upload with the same fingerprint, size and folder exists, it
// is returned so the client resumes it. If a finished file with that
// fingerprint and size is already in the folder, an UploadInfo with
// State == StateDone and Path set is returned and nothing needs sending.
// Otherwise the name is deduplicated case-insensitively against the folder
// and other active uploads, and a new active upload is returned.
func (s *Store) Reserve(dir []string, name string, size int64, fingerprint, device string) (UploadInfo, error) {
	if size < 0 || len(fingerprint) > 4096 || len(device) > 200 {
		return UploadInfo{}, ErrInvalid
	}
	if s.opts.MaxFileSize > 0 && size > s.opts.MaxFileSize {
		return UploadInfo{}, ErrTooLarge
	}
	if err := checkSegs(dir); err != nil {
		return UploadInfo{}, err
	}
	name = names.Sanitize(name)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return UploadInfo{}, ErrNotFound
	}

	if fingerprint != "" {
		for _, u := range s.uploads {
			if u.Fingerprint == fingerprint && u.Size == size && equalFoldSegs(u.Dir, dir) {
				return u.info(StateActive), nil
			}
		}
		if done, ok := s.findFinishedLocked(dir, fingerprint, size); ok {
			return done, nil
		}
	}

	resolved, err := s.resolveDirLocked(dir)
	if err != nil {
		return UploadInfo{}, err
	}
	if s.insufficientLocked(size) {
		return UploadInfo{}, ErrInsufficientSpace
	}
	taken, err := s.takenLocked(resolved, "")
	if err != nil {
		return UploadInfo{}, err
	}
	name = names.Dedupe(func(n string) bool { return taken[strings.ToLower(n)] }, name)

	u := &upload{uploadMeta: uploadMeta{
		ID:          newID(),
		Name:        name,
		Dir:         resolved,
		Size:        size,
		Fingerprint: fingerprint,
		Device:      device,
		Created:     s.opts.Now().UTC(),
	}}
	if err := s.createPartLocked(u); err != nil {
		return UploadInfo{}, err
	}
	s.uploads[u.ID] = u
	info := u.info(StateActive)
	s.emit(Event{Kind: EventUpload, Upload: info})
	return info, nil
}

// findFinishedLocked looks for a finished file in dir uploaded with the same
// fingerprint and still of the same size.
func (s *Store) findFinishedLocked(dir []string, fingerprint string, size int64) (UploadInfo, bool) {
	for key, m := range s.meta {
		if m.Fingerprint != fingerprint {
			continue
		}
		segs, err := names.SplitRel(key)
		if err != nil || len(segs) == 0 || !equalFoldSegs(segs[:len(segs)-1], dir) {
			continue
		}
		fi, err := s.root.Lstat(filesPath(segs))
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != size {
			continue
		}
		return UploadInfo{
			Name:    segs[len(segs)-1],
			Dir:     names.Join(segs[:len(segs)-1]),
			Size:    size,
			Offset:  size,
			Device:  m.Device,
			State:   StateDone,
			Path:    key,
			Created: fi.ModTime().UTC(),
		}, true
	}
	return UploadInfo{}, false
}

func (s *Store) createPartLocked(u *upload) error {
	data, err := json.Marshal(u.uploadMeta)
	if err != nil {
		return err
	}
	f, err := s.root.OpenFile(partPath(u.ID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	f, err = s.root.OpenFile(sidecarPath(u.ID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err == nil {
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		_ = s.root.Remove(partPath(u.ID))
		_ = s.root.Remove(sidecarPath(u.ID))
		return err
	}
	return nil
}

// get returns the active upload id.
func (s *Store) get(id string) (*upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok || u.gone {
		return nil, ErrNotFound
	}
	return u, nil
}

func (s *Store) isGone(u *upload) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return u.gone
}

// Upload returns the current state of active upload id.
func (s *Store) Upload(id string) (UploadInfo, error) {
	u, err := s.get(id)
	if err != nil {
		return UploadInfo{}, err
	}
	return u.info(StateActive), nil
}

// Offset returns how many bytes of upload id the server holds.
func (s *Store) Offset(id string) (int64, error) {
	u, err := s.get(id)
	if err != nil {
		return 0, err
	}
	return u.offset.Load(), nil
}

// Append writes up to limit bytes from r to upload id, which must currently
// hold exactly offset bytes; otherwise an *OffsetMismatchError carries the
// server's offset. It never writes past the declared size. If r still has
// data after the allowed bytes, the bytes that fit are kept and
// ErrChunkTooLarge is returned. On a read error (a dropped connection) the
// bytes received are kept too, so the client resumes from the new offset.
// limit <= 0 means no limit beyond the declared size.
func (s *Store) Append(id string, offset int64, r io.Reader, limit int64) (int64, error) {
	u, err := s.get(id)
	if err != nil {
		return 0, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if s.isGone(u) {
		return 0, ErrNotFound
	}
	cur := u.offset.Load()
	if offset != cur {
		return cur, &OffsetMismatchError{Current: cur}
	}
	if s.appends.Add(1)%freeCheckEvery == 0 {
		s.mu.Lock()
		short := s.insufficientLocked(0)
		s.mu.Unlock()
		if short {
			return cur, ErrInsufficientSpace
		}
	}

	allowed := u.Size - cur
	if limit > 0 && limit < allowed {
		allowed = limit
	}
	f, err := s.root.OpenFile(partPath(id), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return cur, mapErr(err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(r, allowed))
	closeErr := f.Close()
	if fi, err := s.root.Stat(partPath(id)); err == nil {
		u.offset.Store(fi.Size())
	} else {
		u.offset.Add(n)
	}
	now := u.offset.Load()

	if s.isGone(u) {
		return now, ErrNotFound
	}
	if now == u.Size || time.Since(u.lastEvent) >= progressInterval {
		u.lastEvent = time.Now()
		s.emit(Event{Kind: EventUpload, Upload: u.info(StateActive)})
	}
	switch {
	case copyErr != nil:
		return now, copyErr
	case closeErr != nil:
		return now, closeErr
	case n == allowed:
		var probe [1]byte
		if m, _ := io.ReadFull(r, probe[:]); m > 0 {
			return now, ErrChunkTooLarge
		}
	}
	return now, nil
}

// Finish moves a fully received upload into the shared tree and returns its
// final state, with Path set. If its reserved name was taken meanwhile, the
// name is deduplicated again.
func (s *Store) Finish(id string) (UploadInfo, error) {
	u, err := s.get(id)
	if err != nil {
		return UploadInfo{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	fi, err := s.root.Stat(partPath(id))
	if err != nil {
		return UploadInfo{}, mapErr(err)
	}
	if fi.Size() != u.Size {
		return UploadInfo{}, ErrIncomplete
	}
	f, err := s.root.OpenFile(partPath(id), os.O_RDWR, 0)
	if err != nil {
		return UploadInfo{}, mapErr(err)
	}
	syncErr := f.Sync()
	if err := errors.Join(syncErr, f.Close()); err != nil {
		return UploadInfo{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if u.gone {
		return UploadInfo{}, ErrNotFound
	}
	dir, err := s.resolveDirLocked(u.Dir)
	if err != nil {
		return UploadInfo{}, err
	}
	taken, err := s.takenLocked(dir, id)
	if err != nil {
		return UploadInfo{}, err
	}
	name := u.Name
	if taken[strings.ToLower(name)] {
		name = names.Dedupe(func(n string) bool { return taken[strings.ToLower(n)] }, name)
	}
	if err := s.moveIntoLocked(dir, id+".part", name); err != nil {
		return UploadInfo{}, mapErr(err)
	}
	_ = s.root.Remove(sidecarPath(id))
	u.gone = true
	delete(s.uploads, id)

	rel := names.Join(append(append([]string{}, dir...), name))
	s.meta[rel] = metaEntry{Device: u.Device, Fingerprint: u.Fingerprint}
	s.markMetaDirtyLocked()

	info := u.info(StateDone)
	info.Name, info.Dir, info.Path = name, names.Join(dir), rel
	s.emit(Event{Kind: EventUpload, Upload: info})
	s.emitChange(dir)
	return info, nil
}

// Abort cancels upload id and deletes what was received.
func (s *Store) Abort(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok || u.gone {
		return ErrNotFound
	}
	s.abortLocked(u)
	return nil
}

func (s *Store) abortLocked(u *upload) {
	u.gone = true
	delete(s.uploads, u.ID)
	_ = s.root.Remove(partPath(u.ID))
	_ = s.root.Remove(sidecarPath(u.ID))
	s.emit(Event{Kind: EventUpload, Upload: u.info(StateAborted)})
}

// loadUploads rebuilds active uploads from .parts after a restart and
// deletes halves of pairs whose other half is missing.
func (s *Store) loadUploads() {
	entries, err := s.readDir(partsDir)
	if err != nil {
		s.log.Warn("cannot read unfinished uploads", "err", err)
		return
	}
	parts := map[string]bool{}
	sidecars := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if id, ok := strings.CutSuffix(name, ".part"); ok && validID(id) {
			parts[id] = true
		} else if id, ok := strings.CutSuffix(name, ".json"); ok && validID(id) {
			sidecars[id] = true
		}
	}
	for id := range sidecars {
		u, err := s.readSidecar(id)
		if err != nil || !parts[id] {
			s.log.Warn("discarding unfinished upload", "id", id, "err", err)
			_ = s.root.Remove(sidecarPath(id))
			_ = s.root.Remove(partPath(id))
			continue
		}
		fi, err := s.root.Stat(partPath(id))
		if err != nil {
			continue
		}
		u.offset.Store(fi.Size())
		s.uploads[id] = u
		delete(parts, id)
	}
	for id := range parts {
		_ = s.root.Remove(partPath(id))
	}
}

func (s *Store) readSidecar(id string) (*upload, error) {
	f, err := s.root.Open(sidecarPath(id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return nil, err
	}
	u := &upload{}
	if err := json.Unmarshal(data, &u.uploadMeta); err != nil {
		return nil, err
	}
	if u.ID != id || u.Size < 0 || checkSegs(u.Dir) != nil || names.Sanitize(u.Name) != u.Name {
		return nil, fs.ErrInvalid
	}
	return u, nil
}
