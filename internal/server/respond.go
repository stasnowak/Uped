package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"syscall"

	"github.com/stasnowak/Uped/internal/store"
)

// requestError is a 4xx with a message for the user.
type requestError struct {
	status int
	msg    string
}

func (e *requestError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &requestError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

var errUploadGone = fmt.Errorf("%w: this upload was finished, cancelled or has expired", store.ErrNotFound)

// status maps an error to an HTTP status and a message safe to show users.
func status(err error) (int, string) {
	var re *requestError
	var mm *store.OffsetMismatchError
	var mb *http.MaxBytesError
	switch {
	case errors.As(err, &re):
		return re.status, re.msg
	case errors.As(err, &mm):
		return http.StatusConflict, err.Error()
	case errors.As(err, &mb):
		return http.StatusRequestEntityTooLarge, "request body is too large"
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, store.ErrBadPath), errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrIsDir):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, store.ErrNotDir), errors.Is(err, store.ErrIncomplete):
		return http.StatusConflict, err.Error()
	case errors.Is(err, store.ErrTooLarge), errors.Is(err, store.ErrChunkTooLarge):
		return http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, store.ErrInsufficientSpace), errors.Is(err, syscall.ENOSPC):
		return http.StatusInsufficientStorage, store.ErrInsufficientSpace.Error()
	}
	return http.StatusInternalServerError, "internal server error"
}

// fail writes err as a JSON error response.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	code, msg := status(err)
	if code >= 500 && code != http.StatusInsufficientStorage {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	var mm *store.OffsetMismatchError
	if errors.As(err, &mm) {
		w.Header().Set("Upload-Offset", strconv.FormatInt(mm.Current, 10))
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON decodes a JSON body of at most max bytes into v.
func readJSON(w http.ResponseWriter, r *http.Request, max int64, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	if err := dec.Decode(v); err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			return err
		}
		return badRequest("invalid JSON body: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return badRequest("invalid JSON body: unexpected data after the object")
	}
	return nil
}

// isDiskError reports whether err came from writing to disk rather than
// from reading the request.
func isDiskError(err error) bool {
	var pe *fs.PathError
	return errors.As(err, &pe)
}
