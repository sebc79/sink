package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxUpload  int64 = 512 << 20
	defaultMaxExtract int64 = 1 << 30
	defaultMaxFiles         = 100_000
	maxTreeEntries          = 10_000
	maxViewBytes            = 1 << 20
)

type Server struct {
	root       string
	maxUpload  int64
	maxExtract int64
	maxFiles   int
	log        *slog.Logger
	pages      *pageTemplates
	skill      string
}

type Config struct {
	StorageDir string
	MaxUpload  int64
	MaxExtract int64
	MaxFiles   int
	Logger     *slog.Logger
}

func New(cfg Config) (*Server, error) {
	if cfg.StorageDir == "" {
		cfg.StorageDir = "storage"
	}
	if cfg.MaxUpload <= 0 {
		cfg.MaxUpload = defaultMaxUpload
	}
	if cfg.MaxExtract <= 0 {
		cfg.MaxExtract = defaultMaxExtract
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = defaultMaxFiles
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	abs, err := filepath.Abs(cfg.StorageDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	pages, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	skill, err := fs.ReadFile(embedded, "skill.md")
	if err != nil {
		return nil, err
	}
	return &Server{
		root:       abs,
		maxUpload:  cfg.MaxUpload,
		maxExtract: cfg.MaxExtract,
		maxFiles:   cfg.MaxFiles,
		log:        cfg.Logger,
		pages:      pages,
		skill:      string(skill),
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleBrowse)
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("GET /browse/{path...}", s.handleBrowse)
	mux.HandleFunc("GET /view/{path...}", s.handleView)
	mux.HandleFunc("POST /upload", s.handleHTMLUpload)
	mux.HandleFunc("POST /flush", s.handleHTMLFlush)
	mux.HandleFunc("GET /skill", s.handleSkill)
	mux.HandleFunc("GET /skill.md", s.handleSkill)
	mux.HandleFunc("GET /SKILL.md", s.handleSkill)
	mux.HandleFunc("POST /api/upload", s.handleAPIUpload)
	mux.HandleFunc("POST /api/flush", s.handleAPIFlush)
	mux.HandleFunc("GET /api/file/{path...}", s.handleGetFile)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/tree/{path...}", s.handleTree)
	mux.HandleFunc("GET /api/archive", s.handleArchive)
	mux.HandleFunc("GET /api/archive/{path...}", s.handleArchive)
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(sw, r)
		s.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytes,
			"dur", time.Since(start),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (s *Server) requestPath(r *http.Request) string {
	p := r.PathValue("path")
	if p == "" {
		p = r.URL.Query().Get("path")
	}
	return p
}

func (s *Server) handleSkill(w http.ResponseWriter, r *http.Request) {
	body := strings.ReplaceAll(s.skill, "{{BASE_URL}}", baseURL(r))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, body)
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	abs, clean, err := s.resolve(s.requestPath(r))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if clean == "" {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("path is a directory; use /api/tree or /api/archive"))
		return
	}
	st, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			s.writeError(w, r, http.StatusNotFound, fmt.Errorf("not found"))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if st.Mode()&os.ModeSymlink != 0 {
		s.writeError(w, r, http.StatusForbidden, fmt.Errorf("symlinks are not served"))
		return
	}
	if st.IsDir() {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("path is a directory; use /api/tree or /api/archive"))
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	ct := contentType(clean, head)
	download := queryTruthy(r, "download")
	inline := queryTruthy(r, "inline")
	unsafe := isUnsafeContent(ct)
	filename := filepath.Base(abs)
	switch {
	case download || unsafe || !inline:
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	default:
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, filename))
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	http.ServeContent(w, r, filename, st.ModTime(), f)
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	abs, clean, err := s.resolve(s.requestPath(r))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err)
		return
	}
	st, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			s.writeError(w, r, http.StatusNotFound, fmt.Errorf("not found"))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if st.Mode()&os.ModeSymlink != 0 {
		s.writeError(w, r, http.StatusForbidden, fmt.Errorf("symlinks are not served"))
		return
	}
	size := st.Size()
	if st.IsDir() {
		size = 0
	}
	out := treeResponse{
		OK:      true,
		Path:    clean,
		Type:    entryType(st),
		Size:    size,
		ModTime: st.ModTime().UTC(),
	}
	if !st.IsDir() {
		s.writeJSON(w, http.StatusOK, out)
		return
	}
	recursive := queryTruthy(r, "recursive")
	entries, truncated, err := s.listEntries(abs, clean, recursive)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	out.Entries = entries
	out.Truncated = truncated
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	abs, clean, err := s.resolve(s.requestPath(r))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if _, err := os.Lstat(abs); err != nil {
		if os.IsNotExist(err) {
			s.writeError(w, r, http.StatusNotFound, fmt.Errorf("not found"))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "zip"
	}
	switch canonicalFormat(format) {
	case "zip", "tar", "tar.gz":
	default:
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("format must be zip, tar, or tar.gz"))
		return
	}
	name := archiveFilename(clean, format)
	w.Header().Set("Content-Type", mimeForArchive(format))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	if err := packArchive(w, abs, clean, format); err != nil {
		s.log.Error("archive", "err", err, "path", clean)
	}
}

type treeResponse struct {
	OK        bool      `json:"ok"`
	Path      string    `json:"path"`
	Type      string    `json:"type"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	Entries   []Entry   `json:"entries,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
}

type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

func (s *Server) listEntries(abs, rel string, recursive bool) ([]Entry, bool, error) {
	var out []Entry
	truncated := false
	if recursive {
		err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == abs {
				return nil
			}
			st, err := d.Info()
			if err != nil {
				return err
			}
			if st.Mode()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			relPath, err := filepath.Rel(abs, p)
			if err != nil {
				return err
			}
			child := filepath.ToSlash(relPath)
			if rel != "" {
				child = rel + "/" + child
			}
			out = append(out, makeEntry(st, child))
			if len(out) >= maxTreeEntries {
				truncated = true
				return fs.SkipAll
			}
			return nil
		})
		sortEntries(out)
		return out, truncated, err
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, false, err
	}
	for _, d := range ents {
		st, err := d.Info()
		if err != nil {
			continue
		}
		if st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		child := d.Name()
		if rel != "" {
			child = rel + "/" + child
		}
		out = append(out, makeEntry(st, child))
		if len(out) >= maxTreeEntries {
			truncated = true
			break
		}
	}
	sortEntries(out)
	return out, truncated, nil
}

func makeEntry(st os.FileInfo, rel string) Entry {
	size := st.Size()
	if st.IsDir() {
		size = 0
	}
	return Entry{
		Name:    filepath.Base(rel),
		Path:    rel,
		Type:    entryType(st),
		Size:    size,
		ModTime: st.ModTime().UTC(),
	}
}

func entryType(st os.FileInfo) string {
	if st.IsDir() {
		return "directory"
	}
	return "file"
}

func sortEntries(ents []Entry) {
	sort.Slice(ents, func(i, j int) bool {
		if ents[i].Type != ents[j].Type {
			return ents[i].Type == "directory"
		}
		return strings.ToLower(ents[i].Path) < strings.ToLower(ents[j].Path)
	})
}

type errorBody struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	msg := err.Error()
	if errors.Is(err, ErrPathEscape) || errors.Is(err, ErrPathInvalid) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrTooManyFiles) {
		status = http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, ErrArchiveSlip) || errors.Is(err, ErrNotArchive) || errors.Is(err, ErrBadFormat) {
		if status == http.StatusInternalServerError {
			status = http.StatusBadRequest
		}
	}
	if r != nil && strings.HasPrefix(r.URL.Path, "/api/") {
		s.writeJSON(w, status, errorBody{OK: false, Error: msg})
		return
	}
	http.Error(w, msg, status)
}

func queryTruthy(r *http.Request, key string) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func baseURL(r *http.Request) string {
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	if p := firstHeader(r.Header.Get("X-Forwarded-Proto")); p != "" {
		proto = p
	}
	host := r.Host
	if h := firstHeader(r.Header.Get("X-Forwarded-Host")); h != "" {
		host = h
	}
	return proto + "://" + host
}

func firstHeader(v string) string {
	if v == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(v, ",")[0])
}

func contentType(name string, head []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ct := mimeByExt(ext); ct != "" {
		return ct
	}
	ct := http.DetectContentType(head)
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

func mimeByExt(ext string) string {
	switch ext {
	case ".txt", ".md", ".markdown", ".csv", ".log", ".go", ".py", ".rs", ".js", ".ts", ".css", ".html", ".json", ".xml", ".yml", ".yaml", ".toml", ".sh", ".c", ".h", ".cpp", ".java", ".rb", ".php":
		if ext == ".html" {
			return "text/html; charset=utf-8"
		}
		if ext == ".css" {
			return "text/css; charset=utf-8"
		}
		if ext == ".js" {
			return "text/javascript; charset=utf-8"
		}
		if ext == ".json" {
			return "application/json; charset=utf-8"
		}
		if ext == ".xml" {
			return "application/xml; charset=utf-8"
		}
		return "text/plain; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".gz":
		return "application/gzip"
	case ".tar":
		return "application/x-tar"
	}
	return ""
}

func isUnsafeContent(ct string) bool {
	ct = strings.ToLower(strings.Split(ct, ";")[0])
	switch strings.TrimSpace(ct) {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml":
		return true
	}
	return false
}
