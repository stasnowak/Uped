package server

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// recorder captures the status and size of a response for logging.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer (Flush,
// deadlines).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// logRequests logs one line per request. Routine traffic (chunks, listings,
// static files, the event stream) logs at Debug; uploads started or
// finished, downloads, zips, deletes and failures log at Info or above.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400:
			level = slog.LevelWarn
		case routine(r):
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"ms", time.Since(start).Milliseconds(),
			"ip", clientIP(r),
		)
	})
}

func routine(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPut, r.Method == http.MethodHead:
		return true
	case r.Method != http.MethodGet:
		return false
	case p == "/", p == "/healthz", p == "/favicon.ico", p == "/api/config", p == "/api/list", p == "/api/events":
		return true
	}
	return strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/api/uploads/")
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// idleWriter cuts a response when the client stops reading for d: each
// Write first moves the connection's write deadline d into the future.
// Call reset when the handler is done, because net/http does not clear write
// deadlines between keep-alive requests when Server.WriteTimeout is zero.
type idleWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
	d  time.Duration
}

func newIdleWriter(w http.ResponseWriter, d time.Duration) *idleWriter {
	return &idleWriter{ResponseWriter: w, rc: http.NewResponseController(w), d: d}
}

func (w *idleWriter) Write(b []byte) (int, error) {
	_ = w.rc.SetWriteDeadline(time.Now().Add(w.d))
	return w.ResponseWriter.Write(b)
}

func (w *idleWriter) Flush() { _ = w.rc.Flush() }

func (w *idleWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *idleWriter) reset() { _ = w.rc.SetWriteDeadline(time.Time{}) }

// idleReader cuts a request body that stops arriving for d. The server sets
// a fresh read deadline for the next request's headers, so no reset is
// needed.
type idleReader struct {
	r  io.Reader
	rc *http.ResponseController
	d  time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	_ = r.rc.SetReadDeadline(time.Now().Add(r.d))
	return r.r.Read(p)
}
