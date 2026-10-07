// Package server holds the HTTP handlers for uped: the embedded web page,
// the upload, download and folder API, and the live event stream. See the
// HTTP API section of plans/uped-home-file-drop.md.
package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/stasnowak/Uped/internal/store"
)

// Options configures the handler returned by New.
type Options struct {
	Version   string       // shown in /api/config and the page footer
	Static    fs.FS        // web assets; index.html at the root, other files served under /static/
	Store     *store.Store // required
	ChunkSize int64        // upload chunk size advertised to browsers; default 16 MiB
	Logger    *slog.Logger // default discards

	// IdleTimeout cuts a transfer that makes no progress for this long.
	// Default 60s.
	IdleTimeout time.Duration
}

// Server is the root HTTP handler. Close it on shutdown so live event
// streams end and http.Server.Shutdown does not wait for them.
type Server struct {
	opts    Options
	store   *store.Store
	log     *slog.Logger
	hub     *hub
	static  map[string]staticFile
	handler http.Handler
}

type staticFile struct {
	body []byte
	etag string
}

// New builds the handler and starts relaying store events to live clients.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("server: Options.Store is required")
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 16 << 20
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 60 * time.Second
	}
	static, err := loadStatic(opts.Static)
	if err != nil {
		return nil, err
	}
	s := &Server{
		opts:   opts,
		store:  opts.Store,
		log:    opts.Logger,
		static: static,
	}
	s.hub = newHub(opts.Store.Events(), changeCooldown, opts.Logger)
	s.handler = s.logRequests(s.routes())
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Close ends all live event streams and stops relaying store events. Use it
// with http.Server.RegisterOnShutdown.
func (s *Server) Close() { s.hub.close() }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.serveStatic(w, r, "index.html") })
	mux.HandleFunc("GET /static/{name}", func(w http.ResponseWriter, r *http.Request) {
		if name := r.PathValue("name"); name != "index.html" {
			s.serveStatic(w, r, name)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "ok\n")
	})

	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/list", s.handleList)
	mux.HandleFunc("GET /api/events", s.handleEvents)

	mux.HandleFunc("POST /api/uploads", s.handleCreateUpload)
	mux.HandleFunc("GET /api/uploads/{id}", s.handleUploadStatus) // HEAD too
	mux.HandleFunc("PUT /api/uploads/{id}", s.handleChunk)
	mux.HandleFunc("POST /api/uploads/{id}/finish", s.handleFinish)
	mux.HandleFunc("DELETE /api/uploads/{id}", s.handleAbort)

	mux.HandleFunc("POST /api/text", s.handleText)
	mux.HandleFunc("DELETE /api/items", s.handleDelete)
	mux.HandleFunc("GET /d/{path...}", s.handleDownload)
	mux.HandleFunc("GET /api/zip", s.handleZip)
	return mux
}

// loadStatic reads every top-level asset into memory with a content hash
// for ETag revalidation (the embedded files have no modification time).
func loadStatic(fsys fs.FS) (map[string]staticFile, error) {
	if fsys == nil {
		return nil, errors.New("server: Options.Static is required")
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("server: read static assets: %w", err)
	}
	out := map[string]staticFile{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("server: read %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(body)
		out[e.Name()] = staticFile{body: body, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	}
	if _, ok := out["index.html"]; !ok {
		return nil, errors.New("server: static assets have no index.html")
	}
	return out, nil
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, name string) {
	f, ok := s.static[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("ETag", f.etag)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.body))
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	o := s.store.Options()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":      s.opts.Version,
		"chunkSize":    s.opts.ChunkSize,
		"ttlSeconds":   int64(o.TTL / time.Second),
		"maxFileSize":  o.MaxFileSize,
		"minFree":      o.MinFree,
		"maxTextBytes": store.MaxTextBytes,
	})
}
