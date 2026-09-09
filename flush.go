package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type flushResult struct {
	OK      bool `json:"ok"`
	Flushed bool `json:"flushed"`
}

func (s *Server) handleAPIFlush(w http.ResponseWriter, r *http.Request) {
	if err := s.flushStorage(); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, flushResult{OK: true, Flushed: true})
}

func (s *Server) handleHTMLFlush(w http.ResponseWriter, r *http.Request) {
	if err := s.flushStorage(); err != nil {
		s.renderBrowse(w, r, "", err.Error(), "")
		s.log.Error("flush", "err", err)
		return
	}
	http.Redirect(w, r, "/?ok=flushed", http.StatusSeeOther)
}

// flushStorage removes every entry inside the storage root, leaving the
// directory itself in place so later uploads still have a destination.
func (s *Server) flushStorage() error {
	if err := os.MkdirAll(s.root, 0755); err != nil {
		return err
	}
	ents, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, e := range ents {
		p := filepath.Join(s.root, e.Name())
		rel, err := filepath.Rel(s.root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return ErrPathEscape
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}
