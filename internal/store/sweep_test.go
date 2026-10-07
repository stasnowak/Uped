package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSweep(t *testing.T) {
	f := newFixture(t)
	week := 7 * 24 * time.Hour
	f.write("expired.txt", "x", f.now.Add(-week-time.Minute))
	f.write("fresh.txt", "x", f.now.Add(-week+time.Minute))
	f.write("trip/day1/old.jpg", "x", f.now.Add(-week-time.Hour)) // its folders empty out in this sweep
	f.touch("trip/day1", f.now)
	f.touch("trip", f.now)
	f.write("mixed/old.jpg", "x", f.now.Add(-week-time.Hour))
	f.write("mixed/new.jpg", "x", f.now)
	if err := os.MkdirAll(f.path("left-empty-recently"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.touch("left-empty-recently", f.now.Add(-time.Minute))
	if err := os.MkdirAll(f.path("left-empty-long-ago"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.touch("left-empty-long-ago", f.now.Add(-time.Hour))
	f.s.meta["expired.txt"] = metaEntry{Device: "x"}

	stale := f.reserve(nil, "stale.bin", 10, "")
	active := f.reserve(nil, "active.bin", 10, "")
	old := f.now.Add(-25 * time.Hour)
	if err := os.Chtimes(filepath.Join(f.dir, partsDir, stale.ID+".part"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(f.dir, partsDir, active.ID+".part"), f.now, f.now); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(f.dir, partsDir, strings.Repeat("9", 32)+".txt.tmp")
	if err := os.WriteFile(stray, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stray, old, old); err != nil {
		t.Fatal(err)
	}
	drain(f.s)

	res := f.s.Sweep(f.now)
	if res != (SweepResult{Files: 3, Parts: 2, Dirs: 3}) {
		t.Fatalf("Sweep = %+v, want 3 files, 2 parts (stale upload + stray temp), 3 dirs", res)
	}
	for rel, want := range map[string]bool{
		"expired.txt": false, "fresh.txt": true, "trip": false, "mixed/new.jpg": true, "mixed/old.jpg": false,
		"left-empty-recently": true, "left-empty-long-ago": false,
	} {
		if f.exists(rel) != want {
			t.Errorf("%s exists = %v, want %v", rel, !want, want)
		}
	}
	if _, err := os.Stat(f.path("")); err != nil {
		t.Fatalf("files/ root removed: %v", err)
	}
	if _, err := f.s.Offset(stale.ID); err == nil {
		t.Error("stale upload survived")
	}
	if _, err := f.s.Offset(active.ID); err != nil {
		t.Errorf("active upload removed: %v", err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Error("stray temp file survived")
	}
	if _, ok := f.s.meta["expired.txt"]; ok {
		t.Error("meta for expired file survived")
	}
	dirs := map[string]bool{}
	for _, e := range drain(f.s) {
		if e.Kind == EventChange {
			dirs[e.Dir] = true
		}
	}
	if !dirs[""] || !dirs["mixed"] {
		t.Errorf("change events for %v, want root and mixed", dirs)
	}

	if again := f.s.Sweep(f.now); again != (SweepResult{}) {
		t.Errorf("second sweep removed %+v", again)
	}
}

func TestSweepWithoutTTLKeepsFiles(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.TTL = 0 })
	f.write("ancient.txt", "x", f.now.Add(-365*24*time.Hour))
	if res := f.s.Sweep(f.now); res.Files != 0 || !f.exists("ancient.txt") {
		t.Fatalf("Sweep with TTL off = %+v", res)
	}
}

func TestRunSweeper(t *testing.T) {
	f := newFixture(t)
	f.write("expired.txt", "x", f.now.Add(-30*24*time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.s.RunSweeper(ctx, time.Hour)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for f.exists("expired.txt") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSweeper did not stop after cancel")
	}
	if f.exists("expired.txt") {
		t.Fatal("RunSweeper did not sweep on start")
	}
}

// TestFinishSurvivesFolderRemovedMidway simulates the sweeper deleting the
// target folder between Finish creating it and moving the file in.
func TestFinishSurvivesFolderRemovedMidway(t *testing.T) {
	f := newFixture(t)
	info := f.reserve([]string{"trip"}, "a.jpg", 1, "")
	if _, err := f.s.Append(info.ID, 0, strings.NewReader("a"), 0); err != nil {
		t.Fatal(err)
	}
	f.s.mu.Lock()
	if err := f.s.mkdirAll([]string{"trip"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.path("trip")); err != nil { // the sweeper wins the race
		t.Fatal(err)
	}
	err := f.s.moveIntoLocked([]string{"trip"}, info.ID+".part", "a.jpg")
	f.s.mu.Unlock()
	if err != nil || f.read("trip/a.jpg") != "a" {
		t.Fatalf("moveIntoLocked after folder removal: %v", err)
	}
}
