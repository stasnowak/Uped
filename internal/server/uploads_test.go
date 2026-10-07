package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestUploadRoundTrip(t *testing.T) {
	e := newEnv(t)
	data := pattern(3000) // three chunks: 1024 + 1024 + 952
	name := "żółw 🐢.txt"

	resp, info := e.call("POST", "/api/uploads", map[string]any{"name": name, "dir": "Photos/2026", "size": len(data)}, "User-Agent", iPhoneUA)
	expect(t, resp, http.StatusCreated, "create")
	id, _ := info["id"].(string)
	if id == "" || resp.Header.Get("Location") != "/api/uploads/"+id || resp.Header.Get("Upload-Offset") != "0" || info["device"] != "iPhone Safari" {
		t.Fatalf("create: %v headers %v", info, resp.Header)
	}
	for i, off := range []int{0, 1024, 2048} {
		end := min(off+testChunk, len(data))
		resp, _ := e.do("PUT", "/api/uploads/"+id+"?offset="+itoa(off), bytes.NewReader(data[off:end]))
		expect(t, resp, http.StatusNoContent, fmt.Sprintf("chunk %d", i))
		head, _ := e.do("HEAD", "/api/uploads/"+id, nil)
		if head.Header.Get("Upload-Offset") != itoa(end) || head.Header.Get("Upload-Length") != "3000" {
			t.Fatalf("HEAD after chunk %d: offset %s length %s", i, head.Header.Get("Upload-Offset"), head.Header.Get("Upload-Length"))
		}
	}
	resp, done := e.call("POST", "/api/uploads/"+id+"/finish", nil)
	expect(t, resp, 200, "finish")
	if done["path"] != "Photos/2026/"+name || done["state"] != "done" {
		t.Fatalf("finish = %v", done)
	}
	if !strings.Contains(e.logs.String(), "upload finished") {
		t.Error("no 'upload finished' log line")
	}

	resp, body := e.do("GET", "/d/Photos/2026/"+url.PathEscape(name), nil)
	expect(t, resp, 200, "download")
	if !bytes.Equal(body, data) {
		t.Fatalf("downloaded %d bytes differ from upload", len(body))
	}
	disp, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if err != nil || disp != "attachment" || params["filename"] != name {
		t.Fatalf("Content-Disposition %q -> %s %v %v", resp.Header.Get("Content-Disposition"), disp, params, err)
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="___w _.txt"`) {
		t.Errorf("ASCII fallback missing: %q", resp.Header.Get("Content-Disposition"))
	}
	for h, want := range map[string]string{"Content-Security-Policy": "sandbox", "X-Content-Type-Options": "nosniff", "Content-Type": "text/plain; charset=utf-8"} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}

	resp, list := e.call("GET", "/api/list?path=Photos/2026", nil)
	expect(t, resp, 200, "list")
	entries := list["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["device"] != "iPhone Safari" {
		t.Fatalf("list = %v", list)
	}
}

func TestChunkErrors(t *testing.T) {
	e := newEnv(t)
	_, info := e.call("POST", "/api/uploads", map[string]any{"name": "a.bin", "size": 5000})
	id := info["id"].(string)
	put := "/api/uploads/" + id

	resp, _ := e.do("PUT", put, strings.NewReader("x"))
	expect(t, resp, 400, "missing offset")

	resp, body := e.call("PUT", put+"?offset=7", nil)
	expect(t, resp, http.StatusConflict, "wrong offset")
	if resp.Header.Get("Upload-Offset") != "0" || !strings.Contains(fmt.Sprint(body["error"]), "offset mismatch") {
		t.Fatalf("mismatch: %v %v", resp.Header, body)
	}

	resp, _ = e.do("PUT", put+"?offset=0", bytes.NewReader(make([]byte, testChunk+1)))
	expect(t, resp, http.StatusRequestEntityTooLarge, "Content-Length over chunk size")

	// Without a Content-Length the server reads up to the chunk size, keeps
	// it, and reports the overflow.
	resp, _ = e.do("PUT", put+"?offset=0", io.MultiReader(bytes.NewReader(make([]byte, testChunk)), strings.NewReader("extra")))
	expect(t, resp, http.StatusRequestEntityTooLarge, "streamed body over chunk size")
	if resp.Header.Get("Upload-Offset") != itoa(testChunk) {
		t.Fatalf("Upload-Offset after overflow = %q", resp.Header.Get("Upload-Offset"))
	}

	resp, _ = e.call("POST", put+"/finish", nil)
	expect(t, resp, http.StatusConflict, "finish incomplete")

	resp, _ = e.do("DELETE", put, nil)
	expect(t, resp, http.StatusNoContent, "abort")
	for _, m := range []string{"HEAD", "GET", "PUT", "DELETE"} {
		resp, body := e.do(m, put+"?offset=0", nil)
		expect(t, resp, 404, m+" after abort")
		if m != "HEAD" && !strings.Contains(string(body), "finished, cancelled or has expired") {
			t.Errorf("%s 404 body = %q", m, body)
		}
	}
	resp, _ = e.call("POST", "/api/uploads/nope/finish", nil)
	expect(t, resp, 404, "finish unknown")
}

func TestCreateUploadErrors(t *testing.T) {
	e := newEnv(t, func(o *envOpts) { o.store.MaxFileSize = 1000 })
	cases := []struct {
		body string
		code int
	}{
		{`{"name":"a","size":10`, 400},
		{`{"name":"a"}`, 400},
		{`{"name":"a","size":-1}`, 400},
		{`{"name":"a","size":10} {"x":1}`, 400},
		{`{"name":"a","size":10,"dir":"../x"}`, 400},
		{`{"name":"a","size":1001}`, 413},
		{`{"name":"` + strings.Repeat("a", 70<<10) + `","size":1}`, 413},
	}
	for _, c := range cases {
		resp, body := e.do("POST", "/api/uploads", strings.NewReader(c.body), "Content-Type", "application/json")
		if resp.StatusCode != c.code || !strings.Contains(string(body), `"error"`) {
			t.Errorf("POST %.40q = %d %s, want %d with a JSON error", c.body, resp.StatusCode, body, c.code)
		}
	}

	full := newEnv(t, func(o *envOpts) { o.store.MinFree = 1 << 62 })
	resp, body := full.call("POST", "/api/uploads", map[string]any{"name": "a", "size": 1})
	expect(t, resp, http.StatusInsufficientStorage, "disk full")
	if !strings.Contains(fmt.Sprint(body["error"]), "free disk space") {
		t.Errorf("507 body = %v", body)
	}
}

func TestReDropFinishedFileIsSkipped(t *testing.T) {
	e := newEnv(t)
	p := e.upload("trip", "a.jpg", pattern(10), "fp-1")
	resp, info := e.call("POST", "/api/uploads", map[string]any{"name": "a.jpg", "dir": "trip", "size": 10, "fingerprint": "fp-1"})
	expect(t, resp, 200, "re-drop")
	if info["state"] != "done" || info["path"] != p || info["id"] != nil {
		t.Fatalf("re-drop = %v", info)
	}
}

// TestInterruptedChunkKeepsBytes sends half a chunk over a raw connection
// and hangs up, like a phone losing Wi-Fi.
func TestInterruptedChunkKeepsBytes(t *testing.T) {
	e := newEnv(t)
	_, info := e.call("POST", "/api/uploads", map[string]any{"name": "a.bin", "size": 1000})
	id := info["id"].(string)

	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "PUT /api/uploads/%s?offset=0 HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n", id)
	_, _ = conn.Write(bytes.Repeat([]byte("a"), 300))
	conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, _ := e.do("HEAD", "/api/uploads/"+id, nil)
		if resp.Header.Get("Upload-Offset") == "300" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("offset after hang-up = %s, want 300", resp.Header.Get("Upload-Offset"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, _ := e.do("PUT", "/api/uploads/"+id+"?offset=300", bytes.NewReader(bytes.Repeat([]byte("b"), 700)))
	expect(t, resp, 204, "resume")
	resp, done := e.call("POST", "/api/uploads/"+id+"/finish", nil)
	expect(t, resp, 200, "finish")
	_, body := e.do("GET", "/d/"+done["path"].(string), nil)
	if string(body) != strings.Repeat("a", 300)+strings.Repeat("b", 700) {
		t.Fatal("resumed content wrong")
	}
}

// TestStalledUploadIsCut checks the idle read deadline: a client that stops
// sending mid-chunk is disconnected and the bytes so far are kept.
func TestStalledUploadIsCut(t *testing.T) {
	e := newEnv(t, func(o *envOpts) { o.server.IdleTimeout = 200 * time.Millisecond })
	_, info := e.call("POST", "/api/uploads", map[string]any{"name": "a.bin", "size": 1000})
	id := info["id"].(string)

	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT /api/uploads/%s?offset=0 HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n", id)
	_, _ = conn.Write(bytes.Repeat([]byte("a"), 100))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response to a stalled upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 || resp.Header.Get("Upload-Offset") != "100" {
		t.Fatalf("stalled upload: %d offset %q", resp.StatusCode, resp.Header.Get("Upload-Offset"))
	}
}

// TestWriteDeadlineResetBetweenRequests makes sure an idle write deadline
// set during a download does not break the next request on the same
// keep-alive connection.
func TestWriteDeadlineResetBetweenRequests(t *testing.T) {
	e := newEnv(t, func(o *envOpts) { o.server.IdleTimeout = 150 * time.Millisecond })
	e.upload("", "a.txt", []byte("hello"), "")

	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	get := func(path string) *http.Response {
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\n\r\n", path)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("GET %s on reused connection: %v", path, err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp
	}
	expect(t, get("/d/a.txt"), 200, "download")
	time.Sleep(400 * time.Millisecond) // well past the idle deadline
	expect(t, get("/healthz"), 200, "next request on the same connection")
	expect(t, get("/api/zip"), 200, "zip")
	time.Sleep(400 * time.Millisecond)
	expect(t, get("/healthz"), 200, "request after zip on the same connection")
}
