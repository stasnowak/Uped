// Package server holds the HTTP handlers for uped: the embedded web page,
// the upload and download API, and the live event stream.
//
// Phase 0 scaffold: only the index page and /healthz exist. The API is added
// in Phase 2 of plans/uped-home-file-drop.md.
package server

import (
	"io/fs"
	"net/http"
)

// Options configures the handler returned by New.
type Options struct {
	Version string // shown in /api/config and the page footer
	Static  fs.FS  // web assets; must contain index.html at its root
}

// New returns the root HTTP handler.
func New(opts Options) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFileFS(w, r, opts.Static, "index.html")
	})

	return mux
}
