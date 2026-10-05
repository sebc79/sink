package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxMatchList = 50

type treeIndex struct {
	mu     sync.RWMutex
	files  []string
	byBase map[string][]string
}

func (idx *treeIndex) replace(files []string, byBase map[string][]string) {
	if idx == nil {
		return
	}
	if byBase == nil {
		byBase = map[string][]string{}
	}
	idx.mu.Lock()
	idx.files = files
	idx.byBase = byBase
	idx.mu.Unlock()
}

func (idx *treeIndex) list() []string {
	if idx == nil {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]string, len(idx.files))
	copy(out, idx.files)
	return out
}

func (idx *treeIndex) lookup(suffix string) []string {
	if idx == nil || suffix == "" {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if !strings.Contains(suffix, "/") {
		src := idx.byBase[suffix]
		out := make([]string, len(src))
		copy(out, src)
		return out
	}
	var out []string
	for _, f := range idx.files {
		if f == suffix || strings.HasSuffix(f, "/"+suffix) {
			out = append(out, f)
		}
	}
	return out
}

func (s *Server) rebuildIndex() error {
	if s.tree == "" {
		return nil
	}
	files, byBase, err := scanTree(s.tree)
	if err != nil {
		return err
	}
	s.index.replace(files, byBase)
	s.indexMu.Lock()
	s.lastIndex = time.Now()
	s.indexMu.Unlock()
	return nil
}

func (s *Server) maybeRebuildIndex() {
	if s.tree == "" {
		return
	}
	s.indexMu.Lock()
	if s.indexBusy || (!s.lastIndex.IsZero() && time.Since(s.lastIndex) < indexRebuildMin) {
		s.indexMu.Unlock()
		return
	}
	s.indexBusy = true
	s.indexMu.Unlock()
	s.rebuilds.Add(1)
	err := s.rebuildIndex()
	s.indexMu.Lock()
	s.indexBusy = false
	s.indexMu.Unlock()
	if err != nil && s.log != nil {
		s.log.Info("index", "err", err)
	}
}

func (s *Server) indexLoop(ctx context.Context) {
	t := time.NewTicker(s.indexEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.rebuildIndex(); err != nil && s.log != nil {
				s.log.Info("index", "err", err)
			}
		}
	}
}

func ignoredName(name string) bool {
	return name == ".git" || name == ".arborsync-tmp"
}

func hasIgnoredSegment(rel string) bool {
	if rel == "" {
		return false
	}
	for _, seg := range splitSlash(rel) {
		if ignoredName(seg) {
			return true
		}
	}
	return false
}

func splitSlash(rel string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(rel); i++ {
		if i == len(rel) || rel[i] == '/' {
			if i > start {
				out = append(out, rel[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func devOf(info os.FileInfo) (uint64, bool) {
	if info == nil || info.Sys() == nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

func scanTree(root string) ([]string, map[string][]string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, nil, err
	}
	rootDev, rootDevOK := devOf(rootInfo)
	var files []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		name := d.Name()
		if ignoredName(name) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// A different device is another mount. The checkout stops at the tree root.
			if rootDevOK {
				info, infoErr := d.Info()
				if infoErr == nil {
					if dev, ok := devOf(info); ok && dev != rootDev {
						return fs.SkipDir
					}
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if hasIgnoredSegment(rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(files)
	byBase := make(map[string][]string, len(files))
	for _, f := range files {
		base := f
		if i := strings.LastIndex(f, "/"); i >= 0 {
			base = f[i+1:]
		}
		byBase[base] = append(byBase[base], f)
	}
	return files, byBase, nil
}
