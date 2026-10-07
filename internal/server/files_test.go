package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stasnowak/Uped/internal/store"
)

func TestDownloadRangeHeadConditional(t *testing.T) {
	e := newEnv(t)
	data := pattern(2000)
	p := e.upload("dir", "data.bin", data, "")

	resp, body := e.do("GET", "/d/"+p, nil, "Range", "bytes=10-19")
	expect(t, resp, http.StatusPartialContent, "range")
	if !bytes.Equal(body, data[10:20]) || resp.Header.Get("Content-Range") != "bytes 10-19/2000" {
		t.Fatalf("range: %q %s", body, resp.Header.Get("Content-Range"))
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("unknown extension Content-Type = %q", resp.Header.Get("Content-Type"))
	}

	resp, body = e.do("HEAD", "/d/"+p, nil)
	expect(t, resp, 200, "HEAD")
	if len(body) != 0 || resp.ContentLength != 2000 || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD: %d body bytes, length %d, ranges %q", len(body), resp.ContentLength, resp.Header.Get("Accept-Ranges"))
	}

	resp, _ = e.do("GET", "/d/"+p, nil, "If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	expect(t, resp, http.StatusNotModified, "conditional")

	for path, code := range map[string]int{"/d/dir": 400, "/d/": 400, "/d/missing.txt": 404, "/d/dir/missing": 404} {
		if resp, _ := e.do("GET", path, nil); resp.StatusCode != code {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, code)
		}
	}
}

func zipNames(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(data)
	}
	return out
}

func TestZip(t *testing.T) {
	e := newEnv(t)
	e.upload("trip/day1", "a.jpg", []byte("aaa"), "")
	e.upload("trip", "b.jpg", []byte("bb"), "")
	e.upload("", "top.txt", []byte("t"), "")

	resp, body := e.do("GET", "/api/zip?path=trip", nil)
	expect(t, resp, 200, "folder zip")
	_, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if resp.Header.Get("Content-Type") != "application/zip" || !strings.HasPrefix(params["filename"], "uped-trip-") || !strings.HasSuffix(params["filename"], ".zip") {
		t.Fatalf("zip headers: %v", resp.Header)
	}
	got := zipNames(t, body)
	if got["trip/day1/a.jpg"] != "aaa" || got["trip/b.jpg"] != "bb" || len(got) != 4 {
		t.Fatalf("folder zip entries: %v", got)
	}

	resp, body = e.do("GET", "/api/zip", nil)
	expect(t, resp, 200, "zip all")
	_, params, _ = mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if !strings.HasPrefix(params["filename"], "uped-all-") || zipNames(t, body)["top.txt"] != "t" {
		t.Fatalf("zip all: %v", params)
	}

	for path, code := range map[string]int{"/api/zip?path=nope": 404, "/api/zip?path=top.txt": 409, "/api/zip?path=../x": 400} {
		resp, body := e.do("GET", path, nil)
		if resp.StatusCode != code || !strings.Contains(string(body), `"error"`) {
			t.Errorf("GET %s = %d %q, want %d with a JSON error", path, resp.StatusCode, body, code)
		}
	}
}

func TestTextListDelete(t *testing.T) {
	e := newEnv(t)
	resp, out := e.call("POST", "/api/text", map[string]string{"dir": "notes", "text": "Shopping list\nmilk"}, "User-Agent", iPhoneUA)
	expect(t, resp, http.StatusCreated, "text")
	if out["path"] != "notes/shopping-list.txt" {
		t.Fatalf("text path = %v", out)
	}
	resp, list := e.call("GET", "/api/list?path=notes", nil)
	expect(t, resp, 200, "list notes")
	entry := list["entries"].([]any)[0].(map[string]any)
	if entry["preview"] != "Shopping list\nmilk" || entry["device"] != "iPhone Safari" {
		t.Fatalf("text entry = %v", entry)
	}

	resp, _ = e.call("POST", "/api/text", map[string]string{"text": "  "})
	expect(t, resp, 400, "blank text")
	resp, _ = e.call("POST", "/api/text", map[string]string{"text": strings.Repeat("x", store.MaxTextBytes+1)})
	expect(t, resp, 413, "huge text")

	for path, code := range map[string]int{"/api/list?path=..": 400, "/api/list?path=nope": 404, "/api/list?path=notes/shopping-list.txt": 409} {
		if resp, _ := e.do("GET", path, nil); resp.StatusCode != code {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, code)
		}
	}

	resp, _ = e.do("DELETE", "/api/items?path=notes/shopping-list.txt", nil)
	expect(t, resp, 204, "delete file")
	resp, _ = e.do("DELETE", "/api/items?path=notes/shopping-list.txt", nil)
	expect(t, resp, 404, "delete again")
	resp, _ = e.do("DELETE", "/api/items?path=", nil)
	expect(t, resp, 400, "delete root")
	resp, _ = e.do("DELETE", "/api/items?path=notes", nil)
	expect(t, resp, 204, "delete folder")
	resp, list = e.call("GET", "/api/list", nil)
	expect(t, resp, 200, "list root")
	if n := len(list["entries"].([]any)); n != 0 {
		t.Fatalf("root still has %d entries", n)
	}
}

func TestContentDisposition(t *testing.T) {
	for _, name := range []string{"plain.txt", "żółw 🐢.txt", `quote".txt`, "100% done;x=1.txt", "日本語", "a\\b"} {
		h := contentDisposition(name)
		disp, params, err := mime.ParseMediaType(h)
		if err != nil || disp != "attachment" || params["filename"] != name {
			t.Errorf("contentDisposition(%q) = %q -> %v %v", name, h, params, err)
		}
		fallback := h[strings.Index(h, `filename="`)+10:]
		fallback = fallback[:strings.Index(fallback, `"`)]
		for _, r := range fallback {
			if r > 0x7e || r == '%' || r == '\\' {
				t.Errorf("fallback for %q has %q", name, fallback)
			}
		}
	}
	if h := contentDisposition("日本語.pdf"); !strings.Contains(h, `filename="download.pdf"`) {
		t.Errorf("all-non-ASCII fallback = %q", h)
	}
}

func TestErrorStatusMapping(t *testing.T) {
	cases := map[error]int{
		store.ErrNotFound:                        404,
		store.ErrBadPath:                         400,
		store.ErrInvalid:                         400,
		store.ErrIsDir:                           400,
		store.ErrNotDir:                          409,
		store.ErrIncomplete:                      409,
		&store.OffsetMismatchError{}:             409,
		store.ErrTooLarge:                        413,
		store.ErrChunkTooLarge:                   413,
		store.ErrInsufficientSpace:               507,
		fmt.Errorf("wrapped: %w", errUploadGone): 404,
		io.ErrUnexpectedEOF:                      500,
	}
	for err, want := range cases {
		if got, msg := status(err); got != want || msg == "" {
			t.Errorf("status(%v) = %d %q, want %d", err, got, msg, want)
		}
	}
	if _, msg := status(io.ErrUnexpectedEOF); msg != "internal server error" {
		t.Errorf("500 leaks detail: %q", msg)
	}
}
