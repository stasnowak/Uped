package store

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestList(t *testing.T) {
	f := newFixture(t)
	base := f.now.Add(-time.Hour)
	f.write("old.bin", "12345", base.Add(-2*time.Hour))
	f.write("note.txt", "hello\nworld", base)
	f.write("trip/a.jpg", "aaa", base.Add(-3*time.Hour))
	f.write("trip/day1/b.jpg", "bbbbb", base.Add(-30*time.Minute))
	if err := os.Mkdir(f.path("empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.touch("empty", base.Add(-5*time.Hour))
	if err := os.Symlink("/etc", f.path("link")); err != nil {
		t.Fatal(err)
	}
	f.s.meta["note.txt"] = metaEntry{Device: "iPhone Safari"}
	up := f.reserve([]string{"trip", "day2"}, "c.jpg", 10, "")

	l, err := f.s.List(nil)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, e := range l.Entries {
		order = append(order, e.Name)
	}
	if got := strings.Join(order, ","); got != "note.txt,trip,old.bin,empty" {
		t.Fatalf("order = %s, want newest first and no symlink", got)
	}
	note, trip, empty := l.Entries[0], l.Entries[1], l.Entries[3]
	if trip.Type != "dir" || trip.Items != 2 || trip.Size != 8 || !trip.Modified.Equal(base.Add(-30*time.Minute)) {
		t.Errorf("trip = %+v", trip)
	}
	if trip.ExpiresAt == nil || !trip.ExpiresAt.Equal(base.Add(-30*time.Minute).Add(7*24*time.Hour)) {
		t.Errorf("trip expiry = %v", trip.ExpiresAt)
	}
	if note.Type != "file" || note.Size != 11 || note.Preview != "hello\nworld" || note.Device != "iPhone Safari" {
		t.Errorf("note = %+v", note)
	}
	if empty.Items != 0 || empty.ExpiresAt != nil {
		t.Errorf("empty folder = %+v", empty)
	}
	if len(l.Uploads) != 1 || l.Uploads[0].ID != up.ID || l.Uploads[0].Dir != "trip/day2" {
		t.Errorf("uploads = %+v, want the one heading into trip/day2", l.Uploads)
	}
	if l.Free != 1<<40 || l.Path != "" {
		t.Errorf("free = %d path = %q", l.Free, l.Path)
	}

	sub, err := f.s.List([]string{"trip"})
	if err != nil || sub.Path != "trip" || len(sub.Entries) != 2 || len(sub.Uploads) != 1 {
		t.Fatalf("List(trip) = %+v, %v", sub, err)
	}
	day1, _ := f.s.List([]string{"trip", "day1"})
	if len(day1.Uploads) != 0 {
		t.Errorf("upload into day2 listed under day1")
	}

	if _, err := f.s.List([]string{"nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing folder err = %v", err)
	}
	if _, err := f.s.List([]string{"old.bin"}); !errors.Is(err, ErrNotDir) {
		t.Errorf("list a file err = %v", err)
	}
	f.setFree(0, errors.New("unknown"))
	if l, _ := f.s.List(nil); l.Free != -1 {
		t.Errorf("unknown free = %d, want -1", l.Free)
	}
}

func TestListWithoutTTL(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.TTL = 0 })
	f.write("a.txt", "x", f.now)
	l, _ := f.s.List(nil)
	if l.Entries[0].ExpiresAt != nil {
		t.Fatalf("expiresAt = %v with TTL off", l.Entries[0].ExpiresAt)
	}
}

func TestPreview(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("ż", 300) // 600 bytes, read buffer ends mid-character
	f.write("long.txt", long, time.Time{})
	f.write("big.txt", strings.Repeat("x", previewMaxFile+1), time.Time{})
	f.write("data.bin", "binary", time.Time{})
	f.write("bad.TXT", "ok\xff", time.Time{})
	l, _ := f.s.List(nil)
	got := map[string]string{}
	for _, e := range l.Entries {
		got[e.Name] = e.Preview
	}
	if p := got["long.txt"]; p != strings.Repeat("ż", previewRunes) {
		t.Errorf("long preview has %d runes, valid=%v", utf8.RuneCountInString(p), utf8.ValidString(p))
	}
	if got["big.txt"] != "" || got["data.bin"] != "" {
		t.Errorf("unexpected previews: big=%d bin=%q", len(got["big.txt"]), got["data.bin"])
	}
	if p := got["bad.TXT"]; !utf8.ValidString(p) || !strings.HasPrefix(p, "ok") {
		t.Errorf("invalid UTF-8 preview = %q", p)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	p := f.upload([]string{"trip", "day1"}, "a.jpg", []byte("a"), 10)
	f.upload([]string{"trip"}, "b.jpg", []byte("b"), 10)
	f.upload(nil, "keep.txt", []byte("k"), 10)
	pending := f.reserve([]string{"trip", "day2"}, "c.jpg", 5, "")
	drain(f.s)

	if err := f.s.Delete([]string{"trip", "day1", "a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if f.exists(p) || !f.exists("trip/day1") {
		t.Fatalf("file delete: file exists=%v, folder exists=%v", f.exists(p), f.exists("trip/day1"))
	}
	if err := f.s.Delete([]string{"trip"}); err != nil {
		t.Fatal(err)
	}
	if f.exists("trip") || !f.exists("keep.txt") {
		t.Fatal("folder delete removed the wrong things")
	}
	if _, err := f.s.Offset(pending.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upload into deleted folder still active: %v", err)
	}
	for k := range f.s.meta {
		if strings.HasPrefix(k, "trip") {
			t.Errorf("meta for %s survived delete", k)
		}
	}
	events := drain(f.s)
	sawAbort, sawRootChange := false, false
	for _, e := range events {
		sawAbort = sawAbort || (e.Kind == EventUpload && e.Upload.State == StateAborted)
		sawRootChange = sawRootChange || (e.Kind == EventChange && e.Dir == "")
	}
	if !sawAbort || !sawRootChange {
		t.Fatalf("events after delete = %+v", events)
	}

	if err := f.s.Delete(nil); !errors.Is(err, ErrBadPath) {
		t.Errorf("delete root err = %v", err)
	}
	if err := f.s.Delete([]string{"nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing err = %v", err)
	}
}

func TestPutText(t *testing.T) {
	f := newFixture(t)
	p, err := f.s.PutText(nil, "Shopping list\nmilk", "Android Chrome")
	if err != nil || p != "shopping-list.txt" || f.read(p) != "Shopping list\nmilk" {
		t.Fatalf("PutText = %q, %v", p, err)
	}
	p2, err := f.s.PutText([]string{"notes"}, "Shopping list\neggs", "")
	if err != nil || p2 != "notes/shopping-list.txt" {
		t.Fatalf("PutText into new folder = %q, %v", p2, err)
	}
	p3, _ := f.s.PutText(nil, "shopping list", "")
	if p3 != "shopping-list (1).txt" {
		t.Fatalf("duplicate snippet name = %q", p3)
	}
	l, _ := f.s.List(nil)
	for _, e := range l.Entries {
		if e.Name == "shopping-list.txt" && e.Device != "Android Chrome" {
			t.Errorf("device = %q", e.Device)
		}
	}
	if _, err := f.s.PutText(nil, "  \n ", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank text err = %v", err)
	}
	if _, err := f.s.PutText(nil, strings.Repeat("x", MaxTextBytes+1), ""); !errors.Is(err, ErrTooLarge) {
		t.Errorf("huge text err = %v", err)
	}
	f.setFree(10, nil)
	f.s.opts.MinFree = 100
	if _, err := f.s.PutText(nil, "hi", ""); !errors.Is(err, ErrInsufficientSpace) {
		t.Errorf("full disk err = %v", err)
	}
	for _, name := range f.partFiles() {
		t.Errorf("temp file left in .parts: %s", name)
	}
}

func TestOpenFile(t *testing.T) {
	f := newFixture(t)
	f.write("dir/a.txt", "abc", time.Time{})
	file, fi, err := f.s.OpenFile([]string{"dir", "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(file)
	file.Close()
	if string(b) != "abc" || fi.Size() != 3 {
		t.Fatalf("OpenFile read %q size %d", b, fi.Size())
	}
	if _, _, err := f.s.OpenFile([]string{"dir"}); !errors.Is(err, ErrIsDir) {
		t.Errorf("open folder err = %v", err)
	}
	if _, _, err := f.s.OpenFile(nil); !errors.Is(err, ErrIsDir) {
		t.Errorf("open root err = %v", err)
	}
	if _, _, err := f.s.OpenFile([]string{"nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("open missing err = %v", err)
	}
}

func readZip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	out := map[string]string{}
	for _, zf := range zr.File {
		if zf.Method != zip.Store {
			t.Errorf("%s method = %d, want Store", zf.Name, zf.Method)
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[zf.Name] = string(data)
	}
	return out
}

func keys(m map[string]string) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func TestWriteZip(t *testing.T) {
	f := newFixture(t)
	f.write("top.txt", "top", time.Time{})
	f.write("trip/a.jpg", "aaa", time.Time{})
	f.write("trip/day1/ż.jpg", "zzz", time.Time{})
	if err := os.Mkdir(f.path("trip/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", f.path("trip/link")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	flushes := 0
	if err := f.s.WriteZip(&buf, nil, func() { flushes++ }); err != nil {
		t.Fatal(err)
	}
	all := readZip(t, buf.Bytes())
	if got := keys(all); got != "top.txt,trip/,trip/a.jpg,trip/day1/,trip/day1/ż.jpg,trip/empty/" {
		t.Fatalf("root zip entries = %s", got)
	}
	if all["trip/day1/ż.jpg"] != "zzz" || flushes != 3 {
		t.Fatalf("content %q, flushes %d", all["trip/day1/ż.jpg"], flushes)
	}

	buf.Reset()
	if err := f.s.WriteZip(&buf, []string{"trip"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := keys(readZip(t, buf.Bytes())); got != "trip/,trip/a.jpg,trip/day1/,trip/day1/ż.jpg,trip/empty/" {
		t.Fatalf("folder zip entries = %s", got)
	}

	buf.Reset()
	if err := f.s.WriteZip(&buf, []string{"trip", "empty"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := keys(readZip(t, buf.Bytes())); got != "empty/" {
		t.Fatalf("empty folder zip = %s", got)
	}

	if err := f.s.WriteZip(io.Discard, []string{"nope"}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("zip missing err = %v", err)
	}
	if err := f.s.WriteZip(io.Discard, []string{"top.txt"}, nil); !errors.Is(err, ErrNotDir) {
		t.Errorf("zip file err = %v", err)
	}
	if err := f.s.WriteZip(failingWriter{}, nil, nil); err == nil {
		t.Errorf("zip to a failing writer returned no error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("client went away") }

func TestMetaPersistence(t *testing.T) {
	f := newFixture(t)
	f.upload(nil, "a.txt", []byte("a"), 10)
	f.upload(nil, "b.txt", []byte("b"), 10)
	f.reopen()
	if f.s.meta["a.txt"].Device != "Test Device" {
		t.Fatalf("device lost across reopen: %+v", f.s.meta)
	}

	if err := os.Remove(f.path("b.txt")); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	if _, ok := f.s.meta["b.txt"]; ok {
		t.Fatal("meta for a deleted file survived reopen")
	}

	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dir+"/"+metaFile, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.open()
	if len(f.s.meta) != 0 {
		t.Fatalf("corrupt meta.json loaded: %+v", f.s.meta)
	}
	if err := f.s.FlushMeta(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f.dir + "/" + metaFile); string(b) != "{}" {
		t.Fatalf("meta.json after recovery = %q", b)
	}
}

func TestMetaWritesAreBatched(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 20; i++ {
		f.upload(nil, "f.txt", []byte("x"), 10)
	}
	if _, err := os.Stat(f.dir + "/" + metaFile); !os.IsNotExist(err) {
		t.Fatalf("meta.json written before the batching delay: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(f.dir + "/" + metaFile); err == nil && strings.Count(string(b), "Test Device") == 20 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("meta.json was not written within 5s")
}
