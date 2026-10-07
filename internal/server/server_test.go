package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	static := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>uped</title>")},
	}
	ts := httptest.NewServer(New(Options{Version: "test", Static: static}))
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, method, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t)
	resp, body := get(t, http.MethodGet, ts.URL+"/healthz")
	if resp.StatusCode != http.StatusOK || body != "ok\n" {
		t.Fatalf("GET /healthz = %d %q, want 200 \"ok\\n\"", resp.StatusCode, body)
	}
}

func TestIndex(t *testing.T) {
	ts := newTestServer(t)
	resp, body := get(t, http.MethodGet, ts.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(body, "<title>uped</title>") {
		t.Errorf("body = %q, want the embedded index page", body)
	}
}

func TestIndexHead(t *testing.T) {
	ts := newTestServer(t)
	resp, body := get(t, http.MethodHead, ts.URL+"/")
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("HEAD / = %d with %d body bytes, want 200 and no body", resp.StatusCode, len(body))
	}
}

func TestUnknownPathAndMethod(t *testing.T) {
	ts := newTestServer(t)
	if resp, _ := get(t, http.MethodGet, ts.URL+"/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", resp.StatusCode)
	}
	if resp, _ := get(t, http.MethodPost, ts.URL+"/"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", resp.StatusCode)
	}
}
