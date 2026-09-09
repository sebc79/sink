package main

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type uploadResult struct {
	OK       bool   `json:"ok"`
	Path     string `json:"path"`
	Stored   string `json:"stored"`
	Unpacked bool   `json:"unpacked"`
	Format   string `json:"format,omitempty"`
	Bytes    int64  `json:"bytes"`
	Files    int    `json:"files"`
}

func (s *Server) handleAPIUpload(w http.ResponseWriter, r *http.Request) {
	res, status, err := s.receiveUpload(w, r)
	if err != nil {
		s.writeError(w, r, status, err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleHTMLUpload(w http.ResponseWriter, r *http.Request) {
	res, status, err := s.receiveUpload(w, r)
	if err != nil {
		s.renderBrowse(w, r, browsePathAfterUpload(s, firstNonEmpty(r.FormValue("path"), r.URL.Query().Get("path"))), err.Error(), "")
		if status >= 500 {
			s.log.Error("upload", "err", err)
		}
		return
	}
	dest := res.Path
	if res.Stored == "file" {
		dest = parentRel(res.Path)
	}
	loc := "/"
	if dest != "" {
		loc = "/browse/" + urlPath(dest)
	}
	http.Redirect(w, r, loc+"?ok=uploaded", http.StatusSeeOther)
}

func (s *Server) receiveUpload(w http.ResponseWriter, r *http.Request) (*uploadResult, int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUpload)
	ct := r.Header.Get("Content-Type")
	media, _, _ := mime.ParseMediaType(ct)
	isMultipart := strings.HasPrefix(media, "multipart/")
	if isMultipart {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			if isMaxBytes(err) {
				return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("upload exceeds %d bytes", s.maxUpload)
			}
			return nil, http.StatusBadRequest, err
		}
	}

	// Query parameters always work. Form fields are used only for multipart
	// uploads — calling FormValue on a raw POST would consume the file body
	// when curl defaults to application/x-www-form-urlencoded.
	pathVal := r.URL.Query().Get("path")
	unpackVal := r.URL.Query().Get("unpack")
	if isMultipart {
		pathVal = firstNonEmpty(r.FormValue("path"), pathVal)
		unpackVal = firstNonEmpty(r.FormValue("unpack"), unpackVal)
	}
	hadTrailingSlash := strings.HasSuffix(strings.TrimSpace(pathVal), "/")

	format, unpack, err := parseUnpack(unpackVal)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if strings.TrimSpace(pathVal) == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("path is required")
	}
	if !unpack && hadTrailingSlash {
		return nil, http.StatusBadRequest, fmt.Errorf("path must name a file when not unpacking")
	}

	abs, clean, err := s.resolve(pathVal)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if clean == "" {
		if !unpack {
			return nil, http.StatusBadRequest, fmt.Errorf("path must name a file when not unpacking")
		}
		abs = s.root
	}

	tmp, err := os.CreateTemp("", "sink-upload-*")
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	n, err := s.copyUploadBody(r, tmp)
	if err != nil {
		if isMaxBytes(err) {
			return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("upload exceeds %d bytes", s.maxUpload)
		}
		return nil, http.StatusBadRequest, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, http.StatusInternalServerError, err
	}

	if unpack {
		st, err := os.Lstat(abs)
		createdDest := false
		if err == nil {
			if st.Mode()&os.ModeSymlink != 0 {
				return nil, http.StatusConflict, fmt.Errorf("destination is a symlink")
			}
			if !st.IsDir() {
				return nil, http.StatusConflict, fmt.Errorf("destination exists as a file")
			}
		} else if os.IsNotExist(err) {
			createdDest = true
		} else if isNotDir(err) {
			return nil, http.StatusConflict, fmt.Errorf("a parent of the destination is a file")
		} else {
			return nil, http.StatusInternalServerError, err
		}
		if err := os.MkdirAll(abs, 0755); err != nil {
			if isNotDir(err) {
				return nil, http.StatusConflict, fmt.Errorf("a parent of the destination is a file")
			}
			return nil, http.StatusInternalServerError, err
		}
		files, bytes, used, err := s.unpackFile(tmp, abs, format)
		if err != nil {
			if createdDest {
				_ = os.RemoveAll(abs)
			}
			status := http.StatusBadRequest
			if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrTooManyFiles) {
				status = http.StatusRequestEntityTooLarge
			}
			return nil, status, err
		}
		return &uploadResult{
			OK:       true,
			Path:     clean,
			Stored:   "directory",
			Unpacked: true,
			Format:   used,
			Bytes:    bytes,
			Files:    files,
		}, http.StatusOK, nil
	}

	st, err := os.Lstat(abs)
	if err == nil {
		if st.IsDir() {
			return nil, http.StatusConflict, fmt.Errorf("destination exists as a directory")
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, http.StatusConflict, fmt.Errorf("destination is a symlink")
		}
	} else if os.IsNotExist(err) {
		// create
	} else if isNotDir(err) {
		return nil, http.StatusConflict, fmt.Errorf("a parent of the destination is a file")
	} else {
		return nil, http.StatusInternalServerError, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		if isNotDir(err) {
			return nil, http.StatusConflict, fmt.Errorf("a parent of the destination is a file")
		}
		return nil, http.StatusInternalServerError, err
	}
	tmp.Close()
	if err := os.Rename(tmpName, abs); err != nil {
		if copyErr := copyFile(tmpName, abs); copyErr != nil {
			return nil, http.StatusInternalServerError, copyErr
		}
		os.Remove(tmpName)
	}
	return &uploadResult{
		OK:     true,
		Path:   clean,
		Stored: "file",
		Bytes:  n,
		Files:  1,
	}, http.StatusOK, nil
}

func (s *Server) copyUploadBody(r *http.Request, dst *os.File) (int64, error) {
	if r.MultipartForm != nil {
		if fhs := r.MultipartForm.File["file"]; len(fhs) > 0 {
			f, err := fhs[0].Open()
			if err != nil {
				return 0, err
			}
			defer f.Close()
			return io.Copy(dst, f)
		}
		return 0, fmt.Errorf("multipart field \"file\" is required")
	}
	ct := r.Header.Get("Content-Type")
	media, _, _ := mime.ParseMediaType(ct)
	if strings.HasPrefix(media, "multipart/") {
		return 0, fmt.Errorf("multipart field \"file\" is required")
	}
	n, err := io.Copy(dst, r.Body)
	if err != nil {
		return n, err
	}
	if n == 0 && r.ContentLength == 0 {
		// 0-byte files are allowed
	}
	return n, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func browsePathAfterUpload(s *Server, rel string) string {
	clean, err := cleanRel(rel)
	if err != nil || clean == "" {
		return ""
	}
	abs, _, err := s.resolve(clean)
	if err == nil {
		if st, err := os.Lstat(abs); err == nil && st.IsDir() {
			return clean
		}
	}
	return parentRel(clean)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func isNotDir(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOTDIR) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not a directory")
}

func isMaxBytes(err error) bool {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return true
	}
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "http: request body too large") ||
		strings.Contains(msg, "MaxBytesReader")
}
