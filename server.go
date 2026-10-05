package main

import (
	"context"
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
	"sync"
	"sync/atomic"
	"time"

	"sink/peers"
)

const (
	maxTreeEntries     = 10_000
	maxViewBytes       = 1 << 20
	maxHoldTimeout     = 60 * time.Second
	maxConcurrentHolds = 32
	indexRebuildMin    = 2 * time.Second
	peerCacheTTL       = 250 * time.Millisecond
	peerQueryBound     = 2 * time.Second
)

var errHoldBusy = errors.New("too many held views")

type Server struct {
	tree       string
	peerSocket string
	senderID   string
	holdFor    time.Duration
	indexEvery time.Duration
	index      *treeIndex
	queryPeers func(string) (peers.Snapshot, error)
	stopIndex  func()
	holds      chan struct{}
	indexMu    sync.Mutex
	indexBusy  bool
	lastIndex  time.Time
	rebuilds   atomic.Int64
	peerMu     sync.Mutex
	peerSnap   peers.Snapshot
	peerErr    error
	peerAt     time.Time
	log        *slog.Logger
	pages      *pageTemplates
}

type Config struct {
	TreeDir     string
	PeerSocket  string
	SenderID    string
	HoldTimeout time.Duration
	IndexRescan time.Duration
	QueryPeers  func(string) (peers.Snapshot, error)
	Logger      *slog.Logger
}

func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.TreeDir) == "" {
		return nil, fmt.Errorf("tree is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SenderID == "" {
		cfg.SenderID = "grok-bot-box"
	}
	if cfg.HoldTimeout <= 0 {
		cfg.HoldTimeout = 30 * time.Second
	}
	if cfg.HoldTimeout > maxHoldTimeout {
		cfg.HoldTimeout = maxHoldTimeout
	}
	if cfg.IndexRescan <= 0 {
		cfg.IndexRescan = 45 * time.Second
	}
	if cfg.QueryPeers == nil {
		cfg.QueryPeers = func(socket string) (peers.Snapshot, error) {
			d := peerQueryBound
			if cfg.HoldTimeout > 0 && cfg.HoldTimeout < d {
				d = cfg.HoldTimeout
			}
			return peers.QueryDeadline(socket, d)
		}
	}
	treeAbs, err := filepath.Abs(cfg.TreeDir)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(treeAbs)
	if err != nil {
		return nil, fmt.Errorf("tree: %w", err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return nil, fmt.Errorf("tree must be a directory")
	}
	pages, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		tree:       treeAbs,
		peerSocket: cfg.PeerSocket,
		senderID:   cfg.SenderID,
		holdFor:    cfg.HoldTimeout,
		indexEvery: cfg.IndexRescan,
		index:      &treeIndex{byBase: map[string][]string{}},
		queryPeers: cfg.QueryPeers,
		stopIndex:  cancel,
		holds:      make(chan struct{}, maxConcurrentHolds),
		log:        cfg.Logger,
		pages:      pages,
	}
	if err := srv.rebuildIndex(); err != nil {
		cancel()
		return nil, err
	}
	go srv.indexLoop(ctx)
	return srv, nil
}

func (s *Server) stop() {
	if s.stopIndex != nil {
		s.stopIndex()
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleBrowse)
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("GET /browse/{path...}", s.handleBrowse)
	mux.HandleFunc("GET /view/{path...}", s.handleView)
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
		w.Header().Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
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

func (s *Server) writeReadStop(w http.ResponseWriter, r *http.Request, loc located, html bool) bool {
	switch loc.kind {
	case locateInvalid:
		if html {
			http.Error(w, ErrPathInvalid.Error(), http.StatusBadRequest)
		} else {
			s.writeError(w, r, http.StatusBadRequest, ErrPathInvalid)
		}
		return true
	case locateAmbiguous:
		if html {
			s.renderAmbiguous(w, r, loc)
		} else {
			s.writeError(w, r, http.StatusConflict, fmt.Errorf("ambiguous path %s", loc.suffix))
		}
		return true
	case locateDir:
		if html {
			dest := "/"
			if loc.rel != "" {
				dest = "/browse/" + urlPath(loc.rel)
			}
			http.Redirect(w, r, dest, http.StatusSeeOther)
		} else {
			s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("path is a directory; use /api/tree or /api/archive"))
		}
		return true
	default:
		return false
	}
}

func (s *Server) writeReadErr(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, errSymlink) {
		status = http.StatusForbidden
	}
	if errors.Is(err, ErrPathEscape) || errors.Is(err, ErrPathInvalid) {
		status = http.StatusBadRequest
	}
	if r != nil && strings.HasPrefix(r.URL.Path, "/api/") {
		s.writeError(w, r, status, err)
		return
	}
	http.Error(w, err.Error(), status)
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	want, err := parseMtimeQuery(r.URL.Query().Get("mtime"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err)
		return
	}
	suffix := s.requestPath(r)
	loc, err := s.locate(suffix, r.Referer())
	if err != nil {
		s.writeReadErr(w, r, err)
		return
	}
	if s.writeReadStop(w, r, loc, false) {
		return
	}
	if !readyLocated(loc, want) {
		if !s.tryHold() {
			s.writeError(w, r, http.StatusServiceUnavailable, errHoldBusy)
			return
		}
		defer s.releaseHold()
		loc, err = s.await(r.Context(), suffix, r.Referer(), want)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			s.writeError(w, r, http.StatusNotFound, holdFailError(loc.suffix, err))
			return
		}
		if s.writeReadStop(w, r, loc, false) {
			return
		}
	}
	if loc.kind != locateHit {
		s.writeError(w, r, http.StatusNotFound, holdFailError(loc.suffix, ErrMissingAfterHold))
		return
	}
	abs, clean := loc.abs, loc.rel
	st, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			s.writeError(w, r, http.StatusNotFound, holdFailError(clean, ErrMissingAfterHold))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if st.Mode()&os.ModeSymlink != 0 {
		s.writeError(w, r, http.StatusForbidden, errSymlink)
		return
	}
	if st.IsDir() {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("path is a directory; use /api/tree or /api/archive"))
		return
	}
	if want > 0 && unixMtime(st.ModTime()) < want {
		s.writeError(w, r, http.StatusNotFound, holdFailError(clean, ErrStaleMtime))
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
	rootDev, haveDev := s.treeDev()
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
			if ignoredName(d.Name()) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if haveDev && d.IsDir() {
				if dev, ok := devOf(st); ok && dev != rootDev {
					return fs.SkipDir
				}
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
		if ignoredName(d.Name()) {
			continue
		}
		if haveDev && d.IsDir() {
			if dev, ok := devOf(st); ok && dev != rootDev {
				continue
			}
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

func (s *Server) treeDev() (uint64, bool) {
	ri, err := os.Lstat(s.tree)
	if err != nil {
		return 0, false
	}
	return devOf(ri)
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
