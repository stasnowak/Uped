// Package store owns everything on disk under the data directory: chunked
// uploads, the shared tree of finished files, listing, deleting, zipping,
// pasted text, the free-space guard and the expiry sweeper.
//
// Layout under the data directory:
//
//	files/           the shared tree, exactly as users see it
//	.parts/<id>.part bytes received so far for an upload in progress
//	.parts/<id>.json the upload's metadata (target folder, name, size, ...)
//	meta.json        per-file metadata: uploading device and fingerprint
//
// All file access goes through an os.Root opened on the data directory, so
// no client-supplied path or planted symlink can reach outside it. Paths
// passed in are segment slices from names.SplitRel or names.CleanRel.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stasnowak/Uped/internal/names"
)

const (
	filesDir = "files"
	partsDir = ".parts"
	metaFile = "meta.json"

	// DefaultPartTTL is how long an unfinished upload may sit idle before
	// the sweeper deletes it.
	DefaultPartTTL = 24 * time.Hour

	// MaxTextBytes is the largest snippet PutText accepts.
	MaxTextBytes = 1 << 20

	emptyDirGrace    = 10 * time.Minute // an empty folder left by a delete survives this long
	progressInterval = time.Second      // at most one progress event per upload per interval
	freeCheckEvery   = 8                // re-check free space every N appends
	metaFlushDelay   = time.Second      // batch meta.json writes
	previewRunes     = 200
	previewMaxFile   = 64 << 10
	eventBuffer      = 1024
	dirPerm          = 0o750
	filePerm         = 0o640
)

// Errors returned by the store. Server handlers map them to HTTP statuses.
var (
	ErrNotFound          = errors.New("not found")
	ErrBadPath           = names.ErrBadPath
	ErrNotDir            = errors.New("a file with that name is in the way of the folder")
	ErrIsDir             = errors.New("that is a folder")
	ErrInvalid           = errors.New("invalid request")
	ErrTooLarge          = errors.New("file is larger than the server allows")
	ErrChunkTooLarge     = errors.New("chunk is larger than the upload allows")
	ErrInsufficientSpace = errors.New("not enough free disk space on the server")
	ErrIncomplete        = errors.New("upload is not complete yet")
)

// OffsetMismatchError is returned by Append when the client's offset does
// not match the bytes the server holds. Current is the server's offset; the
// client continues from there.
type OffsetMismatchError struct{ Current int64 }

func (e *OffsetMismatchError) Error() string {
	return fmt.Sprintf("offset mismatch: server has %d bytes", e.Current)
}

// Options configures a Store. Zero values select the defaults noted.
type Options struct {
	TTL         time.Duration // finished items are deleted this long after upload; 0 keeps them forever
	MinFree     int64         // uploads may not push free disk space below this many bytes
	MaxFileSize int64         // largest accepted upload in bytes; 0 means unlimited
	PartTTL     time.Duration // idle unfinished uploads are deleted after this; default DefaultPartTTL

	Now       func() time.Time      // clock; default time.Now
	FreeSpace func() (int64, error) // free bytes for uploads; default statfs on the data directory
	Logger    *slog.Logger          // default discards
}

// EventKind distinguishes the two kinds of Event.
type EventKind string

const (
	// EventChange means the contents of folder Event.Dir changed.
	EventChange EventKind = "change"
	// EventUpload carries the state of one upload in Event.Upload.
	EventUpload EventKind = "upload"
)

// Event is a notification for live updates.
type Event struct {
	Kind   EventKind
	Dir    string     // EventChange: slash-joined folder path, "" for the root
	Upload UploadInfo // EventUpload
}

// Upload states reported in UploadInfo.State.
const (
	StateActive  = "active"
	StateDone    = "done"
	StateAborted = "aborted"
)

// UploadInfo describes an upload for clients.
type UploadInfo struct {
	ID      string    `json:"id,omitempty"`
	Name    string    `json:"name"`
	Dir     string    `json:"dir"`
	Size    int64     `json:"size"`
	Offset  int64     `json:"offset"`
	Device  string    `json:"device,omitempty"`
	State   string    `json:"state"`
	Path    string    `json:"path,omitempty"` // final path once done
	Created time.Time `json:"created"`
}

// Store is safe for concurrent use.
type Store struct {
	dir    string
	root   *os.Root
	opts   Options
	log    *slog.Logger
	events chan Event

	appends atomic.Int64 // counts Append calls for the periodic free-space check

	mu        sync.Mutex
	uploads   map[string]*upload
	meta      map[string]metaEntry // key: slash-joined path under files/
	metaDirty bool
	metaTimer *time.Timer
	closed    bool
}

// Open opens or creates a store in dataDir, resuming unfinished uploads.
func Open(dataDir string, opts Options) (*Store, error) {
	if opts.PartTTL <= 0 {
		opts.PartTTL = DefaultPartTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FreeSpace == nil {
		opts.FreeSpace = func() (int64, error) { return diskFree(dataDir) }
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := os.MkdirAll(dataDir, dirPerm); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open data directory: %w", err)
	}
	s := &Store{
		dir:     dataDir,
		root:    root,
		opts:    opts,
		log:     opts.Logger,
		events:  make(chan Event, eventBuffer),
		uploads: map[string]*upload{},
		meta:    map[string]metaEntry{},
	}
	for _, d := range []string{filesDir, partsDir} {
		if err := root.Mkdir(d, dirPerm); err != nil && !errors.Is(err, fs.ErrExist) {
			root.Close()
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
		if fi, err := root.Lstat(d); err != nil || !fi.IsDir() {
			root.Close()
			return nil, fmt.Errorf("%s in the data directory is not a directory", d)
		}
	}
	s.loadMeta()
	s.loadUploads()
	return s, nil
}

// Close writes pending metadata and releases the data directory.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.metaTimer != nil {
		s.metaTimer.Stop()
		s.metaTimer = nil
	}
	err := s.saveMetaLocked()
	s.mu.Unlock()
	return errors.Join(err, s.root.Close())
}

// Events returns the channel of change and upload notifications. Events are
// dropped rather than blocking the store when the channel is full.
func (s *Store) Events() <-chan Event { return s.events }

// Options returns the effective options.
func (s *Store) Options() Options { return s.opts }

// Free returns the free bytes reported for the data directory.
func (s *Store) Free() (int64, error) { return s.opts.FreeSpace() }

func (s *Store) emit(e Event) {
	select {
	case s.events <- e:
	default:
		s.log.Warn("event dropped: no reader keeping up", "kind", e.Kind)
	}
}

func (s *Store) emitChange(dir []string) {
	s.emit(Event{Kind: EventChange, Dir: names.Join(dir)})
}

// checkSegs rejects segment slices that did not come from names.SplitRel or
// names.CleanRel.
func checkSegs(segs []string) error {
	if len(segs) > names.MaxDepth {
		return ErrBadPath
	}
	for _, s := range segs {
		if s == "" || names.Sanitize(s) != s {
			return ErrBadPath
		}
	}
	return nil
}

// filesPath is the root-relative path of segs inside files/.
func filesPath(segs []string) string {
	return path.Join(append([]string{filesDir}, segs...)...)
}

func partPath(id string) string    { return partsDir + "/" + id + ".part" }
func sidecarPath(id string) string { return partsDir + "/" + id + ".json" }

// mapErr converts filesystem errors on client paths into store errors.
func mapErr(err error) error {
	var pe *fs.PathError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.As(err, &pe) && pe.Err != nil && pe.Err.Error() == "path escapes from parent":
		return ErrBadPath
	}
	return err
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b[:])
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// insufficientLocked reports whether accepting extra more bytes would push
// free space below MinFree, counting bytes still owed to active uploads.
// A free-space probe that fails disables the guard rather than blocking.
func (s *Store) insufficientLocked(extra int64) bool {
	free, err := s.opts.FreeSpace()
	if err != nil {
		s.log.Warn("free space unknown, disk guard skipped", "err", err)
		return false
	}
	need := extra + s.pendingLocked()
	return free-need < s.opts.MinFree
}

// pendingLocked is the number of bytes active uploads still have to send.
func (s *Store) pendingLocked() int64 {
	var n int64
	for _, u := range s.uploads {
		if rem := u.Size - u.offset.Load(); rem > 0 {
			n += rem
		}
	}
	return n
}
