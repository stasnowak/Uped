package server

import (
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/stasnowak/Uped/internal/names"
	"github.com/stasnowak/Uped/internal/store"
)

// GET /api/list?path=<folder>
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	dir, err := names.SplitRel(r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	l, err := s.store.List(dir)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// POST /api/text {dir, text}
func (s *Server) handleText(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir  string `json:"dir"`
		Text string `json:"text"`
	}
	// JSON escaping can grow text up to six times (\u0000), so allow for it
	// here and let the store enforce MaxTextBytes on the decoded text.
	if err := readJSON(w, r, 8*store.MaxTextBytes, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	dir, err := names.CleanRel(req.Dir)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rel, err := s.store.PutText(dir, req.Text, device(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": rel})
}

// DELETE /api/items?path=<file or folder>
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	rel, err := names.SplitRel(r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(rel) == 0 {
		s.fail(w, r, badRequest("path is required; deleting everything at once is not allowed"))
		return
	}
	if err := s.store.Delete(rel); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /d/<path>: download one file, with Range, HEAD and conditional
// requests handled by http.ServeContent.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	rel, err := names.SplitRel(r.PathValue("path"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f, fi, err := s.store.OpenFile(rel)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer f.Close()

	name := rel[len(rel)-1]
	h := w.Header()
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", contentDisposition(name))
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	// Uploaded HTML must never run as a page on this origin.
	h.Set("Content-Security-Policy", "sandbox")

	iw := newIdleWriter(w, s.opts.IdleTimeout)
	defer iw.reset()
	http.ServeContent(iw, r, name, fi.ModTime(), f)
}

// GET /api/zip?path=<folder>: the folder (or everything) as a streamed zip.
func (s *Server) handleZip(w http.ResponseWriter, r *http.Request) {
	dir, err := names.SplitRel(r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.StatDir(dir); err != nil {
		s.fail(w, r, err)
		return
	}
	base := "all"
	if len(dir) > 0 {
		base = dir[len(dir)-1]
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", contentDisposition("uped-"+base+"-"+time.Now().Format("20060102-1504")+".zip"))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")

	iw := newIdleWriter(w, s.opts.IdleTimeout)
	defer iw.reset()
	iw.WriteHeader(http.StatusOK)
	if err := s.store.WriteZip(iw, dir, iw.Flush); err != nil {
		s.log.Info("zip download stopped", "path", names.Join(dir), "ip", clientIP(r), "err", err)
		// Abort the connection instead of ending the chunked response
		// cleanly, so the client sees a failed download rather than a
		// truncated zip that looks complete.
		panic(http.ErrAbortHandler)
	}
}

// contentDisposition builds an attachment header with an ASCII fallback
// name and the exact UTF-8 name in RFC 5987 encoding.
// mime.FormatMediaType is not used because it omits the fallback.
func contentDisposition(name string) string {
	var fallback strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
		case r > 0x7e || r == '"' || r == '\\' || r == '%':
			fallback.WriteByte('_')
		default:
			fallback.WriteRune(r)
		}
	}
	fb := fallback.String()
	if ext := path.Ext(fb); strings.Trim(strings.TrimSuffix(fb, ext), "_. ") == "" {
		fb = "download" + ext // nothing readable survived in ASCII
	}
	return `attachment; filename="` + fb + `"; filename*=UTF-8''` + encodeRFC5987(name)
}

func encodeRFC5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// isAttrChar reports whether c may appear unencoded in an RFC 5987 value.
func isAttrChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
