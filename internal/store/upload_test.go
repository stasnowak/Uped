package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestUploadRoundTrip(t *testing.T) {
	f := newFixture(t)
	data := pattern(3000)
	info := f.reserve([]string{"Photos", "2026"}, "trip.bin", int64(len(data)), "fp-1")
	if info.State != StateActive || info.Offset != 0 || info.Name != "trip.bin" || info.Dir != "Photos/2026" {
		t.Fatalf("Reserve = %+v", info)
	}
	for i := 0; i < 3; i++ {
		n, err := f.s.Append(info.ID, int64(i*1000), bytes.NewReader(data[i*1000:(i+1)*1000]), 1000)
		if err != nil || n != int64((i+1)*1000) {
			t.Fatalf("Append %d = %d, %v", i, n, err)
		}
	}
	if off, err := f.s.Offset(info.ID); err != nil || off != 3000 {
		t.Fatalf("Offset = %d, %v", off, err)
	}
	done, err := f.s.Finish(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != StateDone || done.Path != "Photos/2026/trip.bin" {
		t.Fatalf("Finish = %+v", done)
	}
	if got := f.read("Photos/2026/trip.bin"); got != string(data) {
		t.Fatalf("content differs: got %d bytes", len(got))
	}
	if parts := f.partFiles(); len(parts) != 0 {
		t.Fatalf(".parts not empty after finish: %v", parts)
	}
	if _, err := f.s.Offset(info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Offset after finish err = %v, want ErrNotFound", err)
	}

	var kinds []string
	for _, e := range drain(f.s) {
		if e.Kind == EventUpload {
			kinds = append(kinds, "upload:"+e.Upload.State)
		} else {
			kinds = append(kinds, "change:"+e.Dir)
		}
	}
	got := strings.Join(kinds, " ")
	if !strings.HasPrefix(got, "upload:active") || !strings.HasSuffix(got, "upload:done change:Photos/2026") {
		t.Fatalf("events = %s", got)
	}
}

func TestZeroByteUpload(t *testing.T) {
	f := newFixture(t)
	info := f.reserve(nil, "empty.txt", 0, "")
	if _, err := f.s.Append(info.ID, 0, strings.NewReader(""), 100); err != nil {
		t.Fatal(err)
	}
	done, err := f.s.Finish(info.ID)
	if err != nil || done.Path != "empty.txt" || f.read("empty.txt") != "" {
		t.Fatalf("Finish = %+v, %v", done, err)
	}
}

func TestOffsetMismatchAndDroppedConnection(t *testing.T) {
	f := newFixture(t)
	info := f.reserve(nil, "x.bin", 100, "")

	_, err := f.s.Append(info.ID, 10, strings.NewReader("zzzz"), 100)
	var mm *OffsetMismatchError
	if !errors.As(err, &mm) || mm.Current != 0 {
		t.Fatalf("Append at wrong offset err = %v, want mismatch with Current 0", err)
	}

	n, err := f.s.Append(info.ID, 0, brokenReader(strings.Repeat("a", 30)), 100)
	if err == nil || n != 30 {
		t.Fatalf("Append over dropped connection = %d, %v; want 30 and an error", n, err)
	}
	if off, _ := f.s.Offset(info.ID); off != 30 {
		t.Fatalf("Offset after drop = %d, want 30", off)
	}
	if _, err := f.s.Finish(info.ID); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Finish incomplete err = %v", err)
	}
	if n, err := f.s.Append(info.ID, 30, strings.NewReader(strings.Repeat("b", 70)), 100); err != nil || n != 100 {
		t.Fatalf("resume Append = %d, %v", n, err)
	}
	done, err := f.s.Finish(info.ID)
	if err != nil || f.read(done.Path) != strings.Repeat("a", 30)+strings.Repeat("b", 70) {
		t.Fatalf("resumed content wrong: %v", err)
	}
}

func TestChunkLimits(t *testing.T) {
	f := newFixture(t)
	info := f.reserve(nil, "x.bin", 1000, "")
	n, err := f.s.Append(info.ID, 0, strings.NewReader(strings.Repeat("a", 150)), 100)
	if !errors.Is(err, ErrChunkTooLarge) || n != 100 {
		t.Fatalf("over-limit chunk = %d, %v; want 100, ErrChunkTooLarge", n, err)
	}

	small := f.reserve(nil, "small.bin", 50, "")
	n, err = f.s.Append(small.ID, 0, strings.NewReader(strings.Repeat("a", 80)), 0)
	if !errors.Is(err, ErrChunkTooLarge) || n != 50 {
		t.Fatalf("past declared size = %d, %v; want 50, ErrChunkTooLarge", n, err)
	}
	if fi, _ := os.Stat(filepath.Join(f.dir, partsDir, small.ID+".part")); fi.Size() != 50 {
		t.Fatalf("part grew past declared size: %d", fi.Size())
	}
}

func TestConcurrentAppendsSameOffset(t *testing.T) {
	f := newFixture(t)
	info := f.reserve(nil, "race.bin", 1000, "")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, mismatch := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.s.Append(info.ID, 0, strings.NewReader(strings.Repeat("c", 500)), 500)
			mu.Lock()
			defer mu.Unlock()
			var mm *OffsetMismatchError
			switch {
			case err == nil:
				ok++
			case errors.As(err, &mm):
				mismatch++
			default:
				t.Errorf("unexpected error %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || mismatch != 7 {
		t.Fatalf("ok=%d mismatch=%d, want exactly one winner", ok, mismatch)
	}
	if off, _ := f.s.Offset(info.ID); off != 500 {
		t.Fatalf("offset = %d, want 500", off)
	}
}

func TestDuplicateNames(t *testing.T) {
	f := newFixture(t)
	f.write("photo.jpg", "old", f.now)
	a := f.reserve(nil, "photo.jpg", 1, "")
	b := f.reserve(nil, "photo.jpg", 1, "")
	c := f.reserve(nil, "PHOTO.JPG", 1, "")
	if a.Name != "photo (1).jpg" || b.Name != "photo (2).jpg" || c.Name != "PHOTO (3).JPG" {
		t.Fatalf("names = %q %q %q", a.Name, b.Name, c.Name)
	}
	if got := f.reserve(nil, "../../evil\x00.sh", 1, "").Name; got != "evil.sh" {
		t.Fatalf("unsanitised name reserved: %q", got)
	}
}

func TestFinishRedupesWhenNameTaken(t *testing.T) {
	f := newFixture(t)
	info := f.reserve(nil, "x.txt", 2, "")
	f.write("X.TXT", "someone else", f.now) // appears out of band after the reservation
	if _, err := f.s.Append(info.ID, 0, strings.NewReader("hi"), 0); err != nil {
		t.Fatal(err)
	}
	done, err := f.s.Finish(info.ID)
	if err != nil || done.Path != "x (1).txt" || f.read("X.TXT") != "someone else" {
		t.Fatalf("Finish = %+v, %v; want x (1).txt and the other file untouched", done, err)
	}
}

func TestFolderMergesCaseInsensitively(t *testing.T) {
	f := newFixture(t)
	f.upload([]string{"Trip"}, "a.jpg", []byte("a"), 10)
	p := f.upload([]string{"trip", "Day1"}, "b.jpg", []byte("b"), 10)
	if p != "Trip/Day1/b.jpg" {
		t.Fatalf("path = %q, want it inside the existing Trip folder", p)
	}
	if entries, _ := os.ReadDir(f.path("")); len(entries) != 1 {
		t.Fatalf("root has %d entries, want one folder", len(entries))
	}
}

func TestFileInTheWayOfFolder(t *testing.T) {
	f := newFixture(t)
	f.write("trip", "a file", f.now)
	if _, err := f.s.Reserve([]string{"trip"}, "a.jpg", 1, "", ""); !errors.Is(err, ErrNotDir) {
		t.Fatalf("Reserve into a file err = %v, want ErrNotDir", err)
	}
}

func TestResumeAndSkipByFingerprint(t *testing.T) {
	f := newFixture(t)
	a := f.reserve([]string{"d"}, "v.mp4", 10, "fp")
	if _, err := f.s.Append(a.ID, 0, strings.NewReader("12345"), 0); err != nil {
		t.Fatal(err)
	}
	again := f.reserve([]string{"d"}, "v.mp4", 10, "fp")
	if again.ID != a.ID || again.Offset != 5 {
		t.Fatalf("re-reserve = %+v, want same id at offset 5", again)
	}
	if other := f.reserve([]string{"d"}, "v.mp4", 11, "fp"); other.ID == a.ID {
		t.Fatal("different size resumed the same upload")
	}
	if _, err := f.s.Append(a.ID, 5, strings.NewReader("67890"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Finish(a.ID); err != nil {
		t.Fatal(err)
	}
	done := f.reserve([]string{"D"}, "v.mp4", 10, "fp")
	if done.State != StateDone || done.Path != "d/v.mp4" || done.ID != "" {
		t.Fatalf("re-drop of finished file = %+v, want done at d/v.mp4", done)
	}
	if fresh := f.reserve([]string{"elsewhere"}, "v.mp4", 10, "fp"); fresh.State != StateActive {
		t.Fatalf("same file into another folder = %+v, want a new upload", fresh)
	}
}

func TestReopenResumesAndCleansParts(t *testing.T) {
	f := newFixture(t)
	info := f.reserve([]string{"a"}, "big.bin", 10, "fp")
	if _, err := f.s.Append(info.ID, 0, strings.NewReader("hello"), 0); err != nil {
		t.Fatal(err)
	}
	strayPart := strings.Repeat("ab", 16)
	strayJSON := strings.Repeat("cd", 16)
	badJSON := strings.Repeat("ef", 16)
	for name, content := range map[string]string{
		strayPart + ".part": "orphan",
		strayJSON + ".json": `{"id":"` + strayJSON + `","name":"x","dir":[],"size":1}`,
		badJSON + ".json":   "{not json",
		badJSON + ".part":   "",
	} {
		if err := os.WriteFile(filepath.Join(f.dir, partsDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.reopen()

	if off, err := f.s.Offset(info.ID); err != nil || off != 5 {
		t.Fatalf("Offset after reopen = %d, %v; want 5", off, err)
	}
	parts := strings.Join(f.partFiles(), " ")
	for _, gone := range []string{strayPart, strayJSON, badJSON} {
		if strings.Contains(parts, gone) {
			t.Errorf("stray %s survived reopen: %s", gone, parts)
		}
	}
	if _, err := f.s.Append(info.ID, 5, strings.NewReader("world"), 0); err != nil {
		t.Fatal(err)
	}
	done, err := f.s.Finish(info.ID)
	if err != nil || f.read(done.Path) != "helloworld" {
		t.Fatalf("finish after reopen: %+v, %v", done, err)
	}
}

func TestSizeAndSpaceLimits(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MaxFileSize = 100; o.MinFree = 100 })
	if _, err := f.s.Reserve(nil, "x", 101, "", ""); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over max size err = %v", err)
	}
	if _, err := f.s.Reserve(nil, "x", -1, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative size err = %v", err)
	}

	f.setFree(250, nil)
	a := f.reserve(nil, "a", 100, "") // 250 - 100 = 150 >= 100
	if _, err := f.s.Reserve(nil, "b", 100, "", ""); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("second reservation should count the first one's pending bytes, err = %v", err)
	}
	f.setFree(0, errors.New("statfs broke"))
	if _, err := f.s.Reserve(nil, "c", 100, "", ""); err != nil {
		t.Fatalf("unknown free space should not block uploads, err = %v", err)
	}

	// The periodic re-check inside Append notices the disk filling up.
	f.setFree(50, nil)
	var err error
	for i := 0; i < freeCheckEvery && err == nil; i++ {
		_, err = f.s.Append(a.ID, int64(i), strings.NewReader("x"), 1)
	}
	if !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("Append on a full disk err = %v, want ErrInsufficientSpace within %d appends", err, freeCheckEvery)
	}
}

func TestAbort(t *testing.T) {
	f := newFixture(t)
	info := f.reserve(nil, "x", 10, "")
	if err := f.s.Abort(info.ID); err != nil {
		t.Fatal(err)
	}
	if parts := f.partFiles(); len(parts) != 0 {
		t.Fatalf("parts left after abort: %v", parts)
	}
	if _, err := f.s.Append(info.ID, 0, strings.NewReader("x"), 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Append after abort err = %v", err)
	}
	if err := f.s.Abort(info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Abort err = %v", err)
	}
	if _, err := f.s.Finish("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Finish unknown err = %v", err)
	}
	if _, err := f.s.Upload(info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Upload after abort err = %v", err)
	}
}

func TestUploadInfo(t *testing.T) {
	f := newFixture(t)
	info := f.reserve([]string{"a"}, "x", 10, "")
	got, err := f.s.Upload(info.ID)
	if err != nil || got.ID != info.ID || got.Dir != "a" || got.Device != "Test Device" || got.Created != f.now {
		t.Fatalf("Upload = %+v, %v", got, err)
	}
}
