package webdavwithpath

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/webdav"
)

type Handler struct {
	webdav.Handler
	ReadOnly bool
}

// Just copy from original webdav
func (h *Handler) stripPrefix(p string) (string, int, error) {
	if h.Prefix == "" {
		return p, http.StatusOK, nil
	}
	if r := strings.TrimPrefix(p, h.Prefix); len(r) < len(p) {
		return r, http.StatusOK, nil
	}
	return p, http.StatusNotFound, errors.New("webdav-patch: prefix mismatch")
}

func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) (status int, err error) {
	reqPath, status, err := h.stripPrefix(r.URL.Path)
	if err != nil {
		return status, err
	}
	ctx := r.Context()
	allow := "OPTIONS, LOCK, PUT, MKCOL"
	if fi, err := h.FileSystem.Stat(ctx, reqPath); err == nil {
		if fi.IsDir() {
			if h.ReadOnly {
				allow = "OPTIONS, COPY, PROPFIND"
			} else {
				allow = "OPTIONS, LOCK, DELETE, PROPPATCH, COPY, MOVE, UNLOCK, PROPFIND"
			}
		} else {
			if h.ReadOnly {
				allow = "OPTIONS, GET, HEAD, POST, PROPFIND"
			} else {
				allow = "OPTIONS, LOCK, GET, HEAD, POST, DELETE, PROPPATCH, COPY, MOVE, UNLOCK, PROPFIND, PUT, PATCH"
			}
		}
	}
	w.Header().Set("Allow", allow)
	// http://www.webdav.org/specs/rfc4918.html#dav.compliance.classes
	w.Header().Set("DAV", "1, 2, sabredav-partialupdate")
	// http://msdn.microsoft.com/en-au/library/cc250217.aspx
	w.Header().Set("MS-Author-Via", "DAV")
	return 0, nil
}

// Partial copy from original webdav
func (h *Handler) confirmLocks(r *http.Request, src, dst string) (release func(), status int, err error) {
	hdr := r.Header.Get("If")
	if hdr != "" {
		return nil, http.StatusNotImplemented, errors.New("webdav-patch: non-empty `If` header")
	}

	// An empty If header means that the client hasn't previously created locks.
	// Even if this client doesn't care about locks, we still need to check that
	// the resources aren't locked by another client, so we create temporary
	// locks that would conflict with another client's locks. These temporary
	// locks are unlocked at the end of the HTTP request.
	now, token := time.Now(), ""
	if src != "" {
		token, err = h.LockSystem.Create(now, webdav.LockDetails{
			Root:      src,
			Duration:  -1, // infiniteTimeout
			ZeroDepth: true,
		})
		if err != nil {
			if err == webdav.ErrLocked {
				return nil, http.StatusLocked, err
			}
			return nil, http.StatusInternalServerError, err
		}
	}

	return func() {
		if token != "" {
			h.LockSystem.Unlock(now, token)
		}
	}, 0, nil
}

func (h *Handler) handlePatchAppend(reqPath string, exists bool, length int64, r *http.Request) (status int, err error) {
	ctx := r.Context()
	f, err := h.FileSystem.OpenFile(ctx, reqPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return http.StatusMethodNotAllowed, err
	}
	defer f.Close()

	_, err = io.Copy(f, r.Body)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	if exists {
		return http.StatusOK, nil
	} else {
		return http.StatusCreated, nil
	}
}

func (h *Handler) handlePatchBytes(reqPath string, exists bool, bytes string, length int64, r *http.Request) (status int, err error) {
	parts := strings.Split(bytes, "-")
	if len(parts) != 2 {
		return http.StatusBadRequest, errors.New("webdav-patch: invalid bytes in X-Update-Range")
	}

	ctx := r.Context()
	f, err := h.FileSystem.OpenFile(ctx, reqPath, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return http.StatusMethodNotAllowed, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return http.StatusInternalServerError, errors.New("webdav-patch: can't stat file")
	}
	size := fi.Size()

	var start, end int64
	// Parse end
	if len(parts[1]) > 0 {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return http.StatusRequestedRangeNotSatisfiable, err
		}
	}
	// Parse start
	if len(parts[0]) > 0 {
		// bytes=A-B
		start, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return http.StatusRequestedRangeNotSatisfiable, err
		}
		// bytes=N-
		if len(parts[1]) == 0 {
			end = start + length - 1
		}
	} else { // bytes=-N
		if len(parts[1]) == 0 {
			return http.StatusRequestedRangeNotSatisfiable, errors.New("webdav-patch: empty bytes in X-Update-Range")
		}
		start = size - end
		end = start + length - 1
	}

	// There is no information anywhere about what to do in this case.
	// And it’s not clear why we need to specify the end position if we have the length of the content.
	// I decided to throw an error if the numbers diverge.
	if end-start != length-1 {
		return http.StatusBadRequest, errors.New("webdav-patch: empty bytes in X-Update-Range")
	}
	if start < 0 {
		return http.StatusBadRequest, errors.New("webdav-patch: X-Update-Range start < 0")
	}

	f.Seek(start, io.SeekStart)
	_, err = io.Copy(f, r.Body)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	if exists {
		return http.StatusOK, nil
	} else {
		return http.StatusCreated, nil
	}
}

// https://sabre.io/dav/http-patch/
func (h *Handler) handlePatch(w http.ResponseWriter, r *http.Request) (status int, err error) {
	reqPath, status, err := h.stripPrefix(r.URL.Path)
	if err != nil {
		return status, err
	}
	release, status, err := h.confirmLocks(r, reqPath, "")
	if err != nil {
		return status, err
	}
	defer release()

	_, err = h.FileSystem.Stat(r.Context(), reqPath)
	var exists bool
	if err == nil {
		exists = true
	}
	if err == os.ErrNotExist {
		exists = false
	} else {
		return http.StatusInternalServerError, err
	}

	ifMatch := r.Header.Get("If-Match")
	if ifMatch != "" {
		if ifMatch != "*" {
			return http.StatusNotImplemented, errors.New("webdav-patch: only `If-Match: *` supported")
		}
		if !exists {
			return http.StatusPreconditionFailed, nil
		}
	}

	ifNoneMatch := r.Header.Get("If-None-Match")
	if ifNoneMatch != "" {
		if ifNoneMatch != "*" {
			return http.StatusNotImplemented, errors.New("webdav-patch: only `If-None-Match: *` supported")
		}
		if exists {
			return http.StatusPreconditionFailed, nil
		}
	}

	contentType := r.Header.Get("Content-Type")
	if contentType != "application/x-sabredav-partialupdate" {
		return http.StatusUnsupportedMediaType, errors.New("webdav-patch: content-type must be application/x-sabredav-partialupdate")
	}

	contentLength := r.Header.Get("Content-Length")
	length, err := strconv.ParseInt(contentLength, 10, 64)
	if err != nil {
		return http.StatusLengthRequired, err
	}

	updateRange := r.Header.Get("X-Update-Range")
	bytes, has := strings.CutPrefix(updateRange, "bytes=")
	if has {
		return h.handlePatchBytes(reqPath, exists, bytes, length, r)
	}
	if updateRange == "append" {
		return h.handlePatchAppend(reqPath, exists, length, r)
	}
	return http.StatusBadRequest, errors.New("webdav-patch: X-Update-Range must be `bytes=` or `append`")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pass := false
	if h.FileSystem == nil {
		pass = true
	}
	if h.LockSystem == nil {
		pass = true
	}
	if r.Method != "PATCH" && r.Method != "OPTIONS" {
		pass = true
	}
	if !pass {
		var err error
		status := http.StatusBadRequest
		switch r.Method {
		case "OPTIONS":
			status, err = h.handleOptions(w, r)
		case "PATCH":
			status, err = h.handlePatch(w, r)
		}

		w.WriteHeader(status)
		if status != http.StatusNoContent {
			w.Write([]byte(webdav.StatusText(status)))
		}
		if h.Logger != nil {
			h.Logger(r, err)
		}

	} else {
		h.Handler.ServeHTTP(w, r)
	}

}
