package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/stasnowak/Uped/internal/names"
	"github.com/stasnowak/Uped/internal/store"
)

func device(r *http.Request) string { return names.DeviceLabel(r.UserAgent()) }

func setOffset(w http.ResponseWriter, info store.UploadInfo) {
	w.Header().Set("Upload-Offset", strconv.FormatInt(info.Offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(info.Size, 10))
}

// POST /api/uploads {name, dir, size, fingerprint}
//
// 201 with an active upload to send chunks to (new or resumed), or 200 with
// state "done" and a path when this exact file is already on the server.
func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Dir         string `json:"dir"`
		Size        *int64 `json:"size"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := readJSON(w, r, 64<<10, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.Size == nil {
		s.fail(w, r, badRequest("size is required"))
		return
	}
	dir, err := names.CleanRel(req.Dir)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	info, err := s.store.Reserve(dir, req.Name, *req.Size, req.Fingerprint, device(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if info.State == store.StateDone {
		s.log.Info("upload skipped, file already on server", "path", info.Path, "ip", clientIP(r))
		writeJSON(w, http.StatusOK, info)
		return
	}
	w.Header().Set("Location", "/api/uploads/"+info.ID)
	setOffset(w, info)
	writeJSON(w, http.StatusCreated, info)
}

// GET or HEAD /api/uploads/{id}: the server's offset, in the Upload-Offset
// header and the JSON body.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	info, err := s.store.Upload(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, uploadErr(err))
		return
	}
	setOffset(w, info)
	writeJSON(w, http.StatusOK, info)
}

// PUT /api/uploads/{id}?offset=N with one chunk as the body.
//
// 204 with the new Upload-Offset. On any failure after bytes were written,
// Upload-Offset still says where to continue.
func (s *Server) handleChunk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	off, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || off < 0 {
		s.fail(w, r, badRequest("offset query parameter is required"))
		return
	}
	if r.ContentLength > s.opts.ChunkSize {
		s.fail(w, r, store.ErrChunkTooLarge)
		return
	}
	body := &idleReader{
		r:  http.MaxBytesReader(w, r.Body, s.opts.ChunkSize+1),
		rc: http.NewResponseController(w),
		d:  s.opts.IdleTimeout,
	}
	n, err := s.store.Append(id, off, body, s.opts.ChunkSize)
	if err == nil {
		w.Header().Set("Upload-Offset", strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var mm *store.OffsetMismatchError
	var re *requestError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, errUploadGone)
		return
	case errors.As(err, &mm):
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(n, 10))
	if code, _ := status(err); code == http.StatusInternalServerError && !isDiskError(err) && !errors.As(err, &re) {
		// Reading the request failed: the client went away or stalled.
		err = &requestError{status: http.StatusBadRequest, msg: "upload interrupted; continue from Upload-Offset"}
	}
	s.fail(w, r, err)
}

// POST /api/uploads/{id}/finish moves a complete upload into place.
func (s *Server) handleFinish(w http.ResponseWriter, r *http.Request) {
	info, err := s.store.Finish(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, uploadErr(err))
		return
	}
	secs := time.Since(info.Created).Seconds()
	s.log.Info("upload finished",
		"path", info.Path,
		"size", info.Size,
		"device", info.Device,
		"ip", clientIP(r),
		"seconds", int64(secs),
	)
	writeJSON(w, http.StatusOK, info)
}

// DELETE /api/uploads/{id} cancels an upload.
func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Abort(r.PathValue("id")); err != nil {
		s.fail(w, r, uploadErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func uploadErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errUploadGone
	}
	return err
}
