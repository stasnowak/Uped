package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPathEscape plants a symlink inside files/ that points outside the data
// directory and feeds the store raw traversal segments. Nothing may read,
// list, write, zip, expire or delete anything outside, and the store must
// answer with an error rather than a crash.
func TestPathEscape(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(secret, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, f.path("evil")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, f.path("evil-file")); err != nil {
		t.Fatal(err)
	}

	reject := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: accepted, want an error", name)
			return
		}
		t.Logf("rejected %-40s -> %v", name, err)
	}

	_, err := f.s.List([]string{"evil"})
	reject("List(evil)", err)
	_, _, err = f.s.OpenFile([]string{"evil", "secret.txt"})
	reject("OpenFile(evil/secret.txt)", err)
	_, _, err = f.s.OpenFile([]string{"evil-file"})
	reject("OpenFile(evil-file)", err)
	reject("Delete(evil/secret.txt)", f.s.Delete([]string{"evil", "secret.txt"}))
	_, err = f.s.Reserve([]string{"evil"}, "x.txt", 1, "", "")
	reject("Reserve(evil/x.txt)", err)
	_, err = f.s.PutText([]string{"evil"}, "hello", "")
	reject("PutText(evil)", err)
	reject("WriteZip(evil)", f.s.WriteZip(&bytes.Buffer{}, []string{"evil"}, nil))

	for _, segs := range [][]string{{".."}, {"..", "x"}, {"a", "..", ".."}, {"a/b"}, {""}, {"."}, {"x\x00"}} {
		name := "raw " + strings.Join(segs, "|")
		_, err := f.s.List(segs)
		if !errors.Is(err, ErrBadPath) {
			t.Errorf("List(%q) err = %v, want ErrBadPath", segs, err)
		}
		reject("List "+name, err)
		reject("Delete "+name, f.s.Delete(segs))
		_, _, err = f.s.OpenFile(segs)
		reject("OpenFile "+name, err)
		_, err = f.s.Reserve(segs, "x", 1, "", "")
		reject("Reserve "+name, err)
	}

	// The root listing and zip must not show or follow the links.
	l, err := f.s.List(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Entries {
		t.Errorf("listing shows %q", e.Name)
	}
	var buf bytes.Buffer
	if err := f.s.WriteZip(&buf, nil, nil); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("top secret")) {
		t.Error("zip contains the outside file")
	}

	// Sweeping must not expire the ancient outside file through the link.
	f.s.Sweep(time.Now())
	// Deleting the link removes the link itself, never the target.
	if err := f.s.Delete([]string{"evil"}); err != nil {
		t.Fatalf("Delete(evil) = %v, want the symlink itself removed", err)
	}
	if b, err := os.ReadFile(secret); err != nil || string(b) != "top secret" {
		t.Fatalf("outside file damaged: %q, %v", b, err)
	}
	if f.exists("evil") {
		t.Error("symlink still present after delete")
	}
}
