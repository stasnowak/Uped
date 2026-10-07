package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stasnowak/Uped/internal/store"
)

const testChunk = 1024

const iPhoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1"

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type env struct {
	t    *testing.T
	dir  string
	st   *store.Store
	srv  *Server
	ts   *httptest.Server
	logs *syncBuffer
}

type envOpts struct {
	store  store.Options
	server Options
}

func newEnv(t *testing.T, mods ...func(*envOpts)) *env {
	t.Helper()
	o := envOpts{
		store:  store.Options{TTL: 7 * 24 * time.Hour},
		server: Options{Version: "test", ChunkSize: testChunk},
	}
	for _, m := range mods {
		m(&o)
	}
	e := &env{t: t, dir: t.TempDir(), logs: &syncBuffer{}}
	logger := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o.store.Logger = logger
	st, err := store.Open(e.dir, o.store)
	if err != nil {
		t.Fatal(err)
	}
	e.st = st
	t.Cleanup(func() { _ = st.Close() })

	o.server.Store = st
	o.server.Logger = logger
	if o.server.Static == nil {
		o.server.Static = fstest.MapFS{
			"index.html": {Data: []byte("<!doctype html><title>uped</title>")},
			"app.js":     {Data: []byte("console.log('uped')")},
			"style.css":  {Data: []byte("body{}")},
		}
	}
	srv, err := New(o.server)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	e.ts = httptest.NewServer(srv)
	t.Cleanup(e.ts.Close)
	t.Cleanup(srv.Close) // runs first, so live streams end before ts.Close waits for them
	return e
}

// do sends a request and returns the response with its body read.
func (e *env) do(method, path string, body io.Reader, hdr ...string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return resp, b
}

var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       10 * time.Second,
}

// call sends JSON (unless body is nil) and decodes a JSON response.
func (e *env) call(method, path string, body any, hdr ...string) (*http.Response, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	resp, b := e.do(method, path, r, append([]string{"Content-Type", "application/json"}, hdr...)...)
	out := map[string]any{}
	if len(b) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(b, &out); err != nil {
			e.t.Fatalf("%s %s: bad JSON %q: %v", method, path, b, err)
		}
	}
	return resp, out
}

func expect(t *testing.T, resp *http.Response, code int, what string) {
	t.Helper()
	if resp.StatusCode != code {
		t.Fatalf("%s: status %d, want %d", what, resp.StatusCode, code)
	}
}

// upload sends data through the API in testChunk pieces and returns the
// final path.
func (e *env) upload(dir, name string, data []byte, fingerprint string, hdr ...string) string {
	e.t.Helper()
	resp, info := e.call("POST", "/api/uploads", map[string]any{"name": name, "dir": dir, "size": len(data), "fingerprint": fingerprint}, hdr...)
	expect(e.t, resp, http.StatusCreated, "create upload")
	id := info["id"].(string)
	for off := 0; off < len(data); off += testChunk {
		end := min(off+testChunk, len(data))
		resp, _ := e.do("PUT", "/api/uploads/"+id+"?offset="+itoa(off), bytes.NewReader(data[off:end]), hdr...)
		expect(e.t, resp, http.StatusNoContent, "chunk")
		if got := resp.Header.Get("Upload-Offset"); got != itoa(end) {
			e.t.Fatalf("Upload-Offset = %s, want %d", got, end)
		}
	}
	resp, done := e.call("POST", "/api/uploads/"+id+"/finish", nil, hdr...)
	expect(e.t, resp, http.StatusOK, "finish")
	return done["path"].(string)
}

func itoa(n int) string { return strconv.Itoa(n) }

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13 + i/97)
	}
	return b
}

func TestStaticAndConfig(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do("GET", "/", nil)
	expect(t, resp, 200, "GET /")
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(body), "<title>uped</title>") {
		t.Fatalf("index: %s %q", resp.Header.Get("Content-Type"), body)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("index headers: etag %q cache %q", etag, resp.Header.Get("Cache-Control"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("index CSP = %q", csp)
	}
	resp, _ = e.do("GET", "/", nil, "If-None-Match", etag)
	expect(t, resp, http.StatusNotModified, "revalidate index")

	resp, body = e.do("GET", "/static/app.js", nil)
	expect(t, resp, 200, "app.js")
	if !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || string(body) != "console.log('uped')" {
		t.Fatalf("app.js: %s %q", resp.Header.Get("Content-Type"), body)
	}
	for _, p := range []string{"/static/index.html", "/static/", "/static/nope.js", "/nope"} {
		if resp, _ := e.do("GET", p, nil); resp.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}
	if resp, _ := e.do("GET", "/favicon.ico", nil); resp.StatusCode != 204 {
		t.Errorf("favicon = %d", resp.StatusCode)
	}
	if resp, body := e.do("GET", "/healthz", nil); resp.StatusCode != 200 || string(body) != "ok\n" {
		t.Errorf("healthz = %d %q", resp.StatusCode, body)
	}
	if resp, _ := e.do("POST", "/", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d", resp.StatusCode)
	}

	resp, cfg := e.call("GET", "/api/config", nil)
	expect(t, resp, 200, "config")
	if cfg["version"] != "test" || cfg["chunkSize"] != float64(testChunk) || cfg["ttlSeconds"] != float64(604800) || cfg["maxTextBytes"] != float64(store.MaxTextBytes) {
		t.Fatalf("config = %v", cfg)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("New without a store succeeded")
	}
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := New(Options{Store: st, Static: fstest.MapFS{"app.js": {}}}); err == nil {
		t.Error("New without index.html succeeded")
	}
}
