package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPathTraversalViaHTTP plants a symlink to a secret outside the data
// directory and attacks every endpoint that takes a path. No response may
// contain the secret, and the secret must survive.
func TestPathTraversalViaHTTP(t *testing.T) {
	e := newEnv(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.dir, "files", "evil")); err != nil {
		t.Fatal(err)
	}
	// Something readable one level above files/ too.
	if err := os.WriteFile(filepath.Join(e.dir, "inside-data-dir.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	requests := []struct{ method, path, body string }{
		{"GET", "/d/../../etc/passwd", ""},
		{"GET", "/d/%2e%2e/%2e%2e/etc/passwd", ""},
		{"GET", "/d/..%2f..%2fetc%2fpasswd", ""},
		{"GET", "/d/..%2finside-data-dir.txt", ""},
		{"GET", "/d/%2e%2e/inside-data-dir.txt", ""},
		{"GET", "/d/evil/secret.txt", ""},
		{"GET", "/d/evil%2fsecret.txt", ""},
		{"HEAD", "/d/evil/secret.txt", ""},
		{"GET", "/api/list?path=..", ""},
		{"GET", "/api/list?path=evil", ""},
		{"GET", "/api/list?path=%2e%2e%2f%2e%2e", ""},
		{"GET", "/api/zip?path=evil", ""},
		{"GET", "/api/zip?path=../..", ""},
		{"DELETE", "/api/items?path=evil/secret.txt", ""},
		{"DELETE", "/api/items?path=../inside-data-dir.txt", ""},
		{"POST", "/api/uploads", `{"name":"x","size":1,"dir":"../.."}`},
		{"POST", "/api/uploads", `{"name":"x","size":1,"dir":"evil"}`},
		{"POST", "/api/uploads", `{"name":"../../x","size":1,"dir":"..\\.."}`},
		{"POST", "/api/text", `{"dir":"evil","text":"hi"}`},
		{"POST", "/api/text", `{"dir":"../","text":"hi"}`},
	}
	for _, rq := range requests {
		var body *bytes.Reader
		if rq.body != "" {
			body = bytes.NewReader([]byte(rq.body))
		} else {
			body = bytes.NewReader(nil)
		}
		resp, b := e.do(rq.method, rq.path, body, "Content-Type", "application/json")
		t.Logf("%-6s %-45s -> %d", rq.method, rq.path, resp.StatusCode)
		if resp.StatusCode == 200 || resp.StatusCode == 204 || resp.StatusCode == 206 {
			t.Errorf("%s %s succeeded with %d", rq.method, rq.path, resp.StatusCode)
		}
		if strings.Contains(string(b), "top secret") || strings.Contains(string(b), "root:") {
			t.Errorf("%s %s leaked file contents", rq.method, rq.path)
		}
		if resp.StatusCode == 301 {
			// Go's router cleans ".." out of the path; the cleaned target
			// must not exist on this server.
			loc := resp.Header.Get("Location")
			follow, fb := e.do("GET", loc, nil)
			t.Logf("       following redirect to %-28s -> %d", loc, follow.StatusCode)
			if follow.StatusCode != 404 || strings.Contains(string(fb), "root:") {
				t.Errorf("redirect %s -> %s gave %d", rq.path, loc, follow.StatusCode)
			}
		}
	}
	if b, err := os.ReadFile(secret); err != nil || string(b) != "top secret" {
		t.Fatalf("outside secret damaged: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "inside-data-dir.txt")); err != nil {
		t.Fatalf("file above files/ damaged: %v", err)
	}
	// Uploading a hostile name lands safely inside files/.
	p := e.upload("", "../../x", []byte("ok"), "")
	if p != "x" {
		t.Fatalf("hostile name stored as %q", p)
	}
}
