package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed templates/*.html
var embedded embed.FS

type pageTemplates struct {
	page *template.Template
}

func loadTemplates() (*pageTemplates, error) {
	t, err := template.New("page.html").Funcs(template.FuncMap{
		"urlPath":   urlPath,
		"humanSize": humanSize,
		"fmtTime":   fmtTime,
		"join":      pathJoin,
	}).ParseFS(embedded, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &pageTemplates{page: t}, nil
}

func pathJoin(a, b string) string {
	if a == "" {
		return b
	}
	return a + "/" + b
}

type crumb struct {
	Name string
	Path string
}

type treeNode struct {
	Name     string
	Path     string
	IsDir    bool
	Open     bool
	Current  bool
	Children []treeNode
}

type fileView struct {
	Name        string
	Path        string
	Size        int64
	ModTime     time.Time
	Content     string
	HTML        template.HTML
	IsText      bool
	IsImage     bool
	IsMarkdown  bool
	ViewMode    string
	Truncated   bool
	HexPreview  string
	ContentType string
}

type pageData struct {
	Title          string
	RelPath        string
	IsRoot         bool
	IsFile         bool
	Breadcrumb     []crumb
	Entries        []Entry
	Tree           []treeNode
	File           *fileView
	Error          string
	Parent         string
	IsAmbiguous    bool
	Matches        []string
	MatchTruncated bool
	Missing        bool
	RawQuery       string
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	s.renderBrowse(w, r, s.requestPath(r))
}

func (s *Server) renderBrowse(w http.ResponseWriter, r *http.Request, rel string) {
	abs, clean, err := s.resolve(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st.Mode()&os.ModeSymlink != 0 {
		http.Error(w, "symlinks are not served", http.StatusForbidden)
		return
	}
	if !st.IsDir() {
		http.Redirect(w, r, "/view/"+urlPath(clean), http.StatusSeeOther)
		return
	}

	entries, _, err := s.listEntries(abs, clean, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := pageData{
		Title:      s.browseTitle(clean),
		RelPath:    clean,
		IsRoot:     clean == "",
		Breadcrumb: s.breadcrumbs(clean),
		Entries:    entries,
		Tree:       s.buildTree(clean),
		Parent:     parentRel(clean),
	}
	s.renderPage(w, data)
}

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	suffix := s.requestPath(r)
	want, err := parseMtimeQuery(r.URL.Query().Get("mtime"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	loc, err := s.locate(suffix, r.Referer())
	if err != nil {
		s.writeReadErr(w, r, err)
		return
	}
	if s.writeReadStop(w, r, loc, true) {
		return
	}
	if loc.kind == locateHit && loc.rel != loc.suffix {
		http.Redirect(w, r, withQuery("/view/"+urlPath(loc.rel), r.URL.RawQuery), http.StatusFound)
		return
	}
	if !readyLocated(loc, want) {
		if !s.tryHold() {
			http.Error(w, errHoldBusy.Error(), http.StatusServiceUnavailable)
			return
		}
		defer s.releaseHold()
		loc, err = s.await(r.Context(), suffix, r.Referer(), want)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			s.renderMissing(w, loc, err)
			return
		}
		if s.writeReadStop(w, r, loc, true) {
			return
		}
		if loc.kind == locateHit && loc.rel != loc.suffix {
			http.Redirect(w, r, withQuery("/view/"+urlPath(loc.rel), r.URL.RawQuery), http.StatusFound)
			return
		}
	}
	if loc.kind != locateHit {
		s.renderMissing(w, loc, ErrMissingAfterHold)
		return
	}
	s.renderLocatedFile(w, r, loc)
}

func withQuery(path, rawQuery string) string {
	if rawQuery == "" {
		return path
	}
	return path + "?" + rawQuery
}

func (s *Server) renderLocatedFile(w http.ResponseWriter, r *http.Request, loc located) {
	st, err := os.Lstat(loc.abs)
	if err != nil {
		if os.IsNotExist(err) {
			s.renderMissing(w, loc, ErrMissingAfterHold)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st.Mode()&os.ModeSymlink != 0 {
		http.Error(w, errSymlink.Error(), http.StatusForbidden)
		return
	}
	if st.IsDir() {
		dest := "/"
		if loc.rel != "" {
			dest = "/browse/" + urlPath(loc.rel)
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	fv, err := s.readFileView(loc.abs, loc.rel, st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.EqualFold(r.URL.Query().Get("mode"), "raw") {
		fv.ViewMode = "raw"
	} else {
		fv.ViewMode = "preview"
	}
	data := pageData{
		Title:      fv.Name,
		RelPath:    loc.rel,
		IsFile:     true,
		Breadcrumb: s.breadcrumbs(loc.rel),
		Tree:       s.buildTree(parentRel(loc.rel)),
		File:       fv,
		Parent:     parentRel(loc.rel),
	}
	s.renderPage(w, data)
}

func (s *Server) renderAmbiguous(w http.ResponseWriter, r *http.Request, loc located) {
	data := pageData{
		Title:          "Matches",
		RelPath:        loc.suffix,
		IsAmbiguous:    true,
		Matches:        loc.matches,
		MatchTruncated: loc.truncated,
		RawQuery:       r.URL.RawQuery,
		Breadcrumb:     s.breadcrumbs(""),
		Tree:           s.buildTree(""),
	}
	s.renderPage(w, data)
}

func (s *Server) renderMissing(w http.ResponseWriter, loc located, err error) {
	suffix := loc.suffix
	data := pageData{
		Title:      "Not found",
		RelPath:    suffix,
		Missing:    true,
		Error:      holdFailError(suffix, err).Error(),
		Breadcrumb: s.breadcrumbs(parentRel(suffix)),
		Tree:       s.buildTree(parentRel(suffix)),
	}
	s.renderPageCode(w, http.StatusNotFound, data)
}

func (s *Server) renderPage(w http.ResponseWriter, data pageData) {
	s.renderPageCode(w, http.StatusOK, data)
}

func (s *Server) renderPageCode(w http.ResponseWriter, code int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	csp := "default-src 'self'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-ancestors 'none'"
	if data.File != nil && data.File.IsMarkdown {
		csp = "default-src 'self'; img-src 'self' data: https:; style-src 'unsafe-inline' https://cdn.jsdelivr.net; script-src 'unsafe-inline' https://cdn.jsdelivr.net; font-src https://cdn.jsdelivr.net; frame-ancestors 'none'"
	}
	w.Header().Set("Content-Security-Policy", csp)
	var buf bytes.Buffer
	if err := s.pages.page.ExecuteTemplate(&buf, "page.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) readFileView(abs, clean string, st os.FileInfo) (*fileView, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	limited := io.LimitReader(f, maxViewBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	truncated := int64(len(body)) > maxViewBytes
	if truncated {
		body = body[:maxViewBytes]
	}
	ct := contentType(clean, body)
	fv := &fileView{
		Name:        filepath.Base(abs),
		Path:        clean,
		Size:        st.Size(),
		ModTime:     st.ModTime().UTC(),
		Truncated:   truncated,
		ContentType: ct,
	}
	if isImage(ct) && !isUnsafeContent(ct) {
		fv.IsImage = true
		return fv, nil
	}
	if isTextContent(ct, body) {
		fv.IsText = true
		fv.Content = string(body)
		if isMarkdownName(clean) {
			fv.IsMarkdown = true
			html, err := renderMarkdown(body, clean)
			if err != nil {
				return nil, err
			}
			fv.HTML = html
		}
		return fv, nil
	}
	preview := body
	if len(preview) > 256 {
		preview = preview[:256]
	}
	fv.HexPreview = hex.Dump(preview)
	return fv, nil
}

func isImage(ct string) bool {
	ct = strings.ToLower(strings.Split(ct, ";")[0])
	return strings.HasPrefix(strings.TrimSpace(ct), "image/")
}

func isTextContent(ct string, body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	for _, b := range body {
		if b == 0 {
			return false
		}
	}
	ct = strings.ToLower(strings.Split(ct, ";")[0])
	ct = strings.TrimSpace(ct)
	switch {
	case strings.HasPrefix(ct, "text/"):
		return true
	case ct == "application/json", ct == "application/xml", ct == "application/javascript",
		ct == "text/javascript", ct == "image/svg+xml":
		return true
	}
	return strings.HasPrefix(http.DetectContentType(body), "text/")
}

func (s *Server) rootLabel() string {
	base := filepath.Base(s.tree)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "/"
	}
	return base
}

func (s *Server) breadcrumbs(rel string) []crumb {
	out := []crumb{{Name: s.rootLabel(), Path: ""}}
	if rel == "" {
		return out
	}
	parts := strings.Split(rel, "/")
	cur := ""
	for _, p := range parts {
		if cur == "" {
			cur = p
		} else {
			cur = cur + "/" + p
		}
		out = append(out, crumb{Name: p, Path: cur})
	}
	return out
}

func (s *Server) browseTitle(rel string) string {
	if rel == "" {
		return s.breadcrumbs("")[0].Name
	}
	return rel
}

func (s *Server) buildTree(current string) []treeNode {
	const maxNodes = 1500
	var nodes int
	rootDev, haveDev := s.treeDev()
	var walk func(abs, rel string) []treeNode
	walk = func(abs, rel string) []treeNode {
		ents, err := os.ReadDir(abs)
		if err != nil {
			return nil
		}
		var dirs []treeNode
		for _, d := range ents {
			if nodes >= maxNodes {
				break
			}
			if ignoredName(d.Name()) {
				continue
			}
			st, err := d.Info()
			if err != nil || st.Mode()&os.ModeSymlink != 0 || !d.IsDir() {
				continue
			}
			if haveDev {
				if dev, ok := devOf(st); ok && dev != rootDev {
					continue
				}
			}
			child := d.Name()
			if rel != "" {
				child = rel + "/" + child
			}
			nodes++
			n := treeNode{
				Name:    d.Name(),
				Path:    child,
				IsDir:   true,
				Open:    current == child || strings.HasPrefix(current+"/", child+"/"),
				Current: current == child,
			}
			if n.Open {
				n.Children = walk(filepath.Join(abs, d.Name()), child)
			}
			dirs = append(dirs, n)
		}
		return dirs
	}
	return walk(s.tree, "")
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	val := float64(n) / float64(div)
	units := []string{"KB", "MB", "GB", "TB"}
	prec := 1
	if val >= 10 {
		prec = 0
	}
	return strconv.FormatFloat(val, 'f', prec, 64) + " " + units[exp]
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04")
}
