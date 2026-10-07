package store

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixture is a Store in a temp dir with a controllable clock and free space.
type fixture struct {
	t    *testing.T
	dir  string
	s    *Store
	opts Options

	mu   sync.Mutex
	now  time.Time
	free int64
	ferr error
}

func newFixture(t *testing.T, mod ...func(*Options)) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), free: 1 << 40}
	f.opts = Options{
		TTL:       7 * 24 * time.Hour,
		Now:       f.clock,
		FreeSpace: f.freeSpace,
	}
	for _, m := range mod {
		m(&f.opts)
	}
	f.open()
	return f
}

func (f *fixture) open() {
	f.t.Helper()
	s, err := Open(f.dir, f.opts)
	if err != nil {
		f.t.Fatalf("Open: %v", err)
	}
	f.s = s
	f.t.Cleanup(func() { _ = s.Close() })
}

func (f *fixture) reopen() {
	f.t.Helper()
	if err := f.s.Close(); err != nil {
		f.t.Fatalf("Close: %v", err)
	}
	f.open()
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) setFree(n int64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.free, f.ferr = n, err
}

func (f *fixture) freeSpace() (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.free, f.ferr
}

// path returns the host path of rel inside files/.
func (f *fixture) path(rel string) string {
	return filepath.Join(f.dir, filesDir, filepath.FromSlash(rel))
}

func (f *fixture) write(rel, content string, mod time.Time) {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if !mod.IsZero() {
		f.touch(rel, mod)
	}
}

func (f *fixture) touch(rel string, mod time.Time) {
	f.t.Helper()
	if err := os.Chtimes(f.path(rel), mod, mod); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(f.path(rel))
	if err != nil {
		f.t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Lstat(f.path(rel))
	return err == nil
}

func (f *fixture) reserve(dir []string, name string, size int64, fp string) UploadInfo {
	f.t.Helper()
	info, err := f.s.Reserve(dir, name, size, fp, "Test Device")
	if err != nil {
		f.t.Fatalf("Reserve(%q, %q): %v", dir, name, err)
	}
	return info
}

// upload sends data in chunks of chunk bytes and finishes, returning the
// final path.
func (f *fixture) upload(dir []string, name string, data []byte, chunk int) string {
	f.t.Helper()
	info := f.reserve(dir, name, int64(len(data)), "")
	off := int64(0)
	for off < int64(len(data)) {
		end := min(int(off)+chunk, len(data))
		n, err := f.s.Append(info.ID, off, bytes.NewReader(data[off:end]), int64(chunk))
		if err != nil {
			f.t.Fatalf("Append at %d: %v", off, err)
		}
		off = n
	}
	done, err := f.s.Finish(info.ID)
	if err != nil {
		f.t.Fatalf("Finish: %v", err)
	}
	return done.Path
}

func (f *fixture) partFiles() []string {
	f.t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.dir, partsDir))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func drain(s *Store) []Event {
	var out []Event
	for {
		select {
		case e := <-s.Events():
			out = append(out, e)
		default:
			return out
		}
	}
}

// brokenReader yields data and then fails, like a dropped connection.
func brokenReader(data string) io.Reader {
	return io.MultiReader(strings.NewReader(data), errReader{})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}
