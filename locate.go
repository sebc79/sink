package main

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type locateKind int

const (
	locateInvalid locateKind = iota
	locateMiss
	locateHit
	locateAmbiguous
	locateDir
)

type located struct {
	kind      locateKind
	rel       string
	abs       string
	matches   []string
	truncated bool
	suffix    string
}

var errSymlink = errors.New("symlinks are not served")

var knowledgePrefixes = []string{
	"home/box/knowledge/",
	"knowledge/",
	"bot/knowledge/",
}

type statKind int

const (
	statMissing statKind = iota
	statFile
	statDir
	statSymlink
	statOther
)

func (s *Server) locate(suffix, referer string) (located, error) {
	cleaned, err := cleanRel(suffix)
	if err != nil || hasIgnoredSegment(cleaned) {
		return located{kind: locateInvalid, suffix: suffix}, nil
	}
	cands := exactCandidates(cleaned)
	for _, c := range cands {
		loc, err := s.statLocated(c, cleaned)
		if err != nil {
			return loc, err
		}
		if loc.kind == locateHit || loc.kind == locateDir {
			return loc, nil
		}
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		matches := applyReferer(s.index.lookup(c), referer)
		if len(matches) == 0 {
			continue
		}
		if len(matches) == 1 {
			loc, err := s.statLocated(matches[0], cleaned)
			if err != nil {
				return loc, err
			}
			if loc.kind == locateHit || loc.kind == locateDir {
				return loc, nil
			}
			continue
		}
		shown, trunc := capMatches(matches)
		return located{
			kind:      locateAmbiguous,
			suffix:    cleaned,
			matches:   shown,
			truncated: trunc,
		}, nil
	}
	return located{kind: locateMiss, suffix: cleaned}, nil
}

func exactCandidates(cleaned string) []string {
	out := []string{cleaned}
	if cleaned == "" {
		return out
	}
	seen := map[string]bool{cleaned: true}
	for _, p := range knowledgePrefixes {
		if !strings.HasPrefix(cleaned, p) {
			continue
		}
		rest, err := cleanRel(strings.TrimPrefix(cleaned, p))
		if err != nil || rest == "" || hasIgnoredSegment(rest) || seen[rest] {
			continue
		}
		seen[rest] = true
		out = append(out, rest)
	}
	return out
}

func (s *Server) statLocated(rel, suffix string) (located, error) {
	abs, _, kind, err := s.statIn(s.tree, rel)
	if err != nil {
		if errors.Is(err, ErrPathEscape) || errors.Is(err, ErrPathInvalid) {
			return located{kind: locateInvalid, suffix: suffix}, nil
		}
		return located{suffix: suffix}, err
	}
	if kind == statSymlink {
		return located{kind: locateInvalid, suffix: suffix, rel: rel, abs: abs}, errSymlink
	}
	switch kind {
	case statFile:
		return located{kind: locateHit, rel: rel, abs: abs, suffix: suffix}, nil
	case statDir:
		return located{kind: locateDir, rel: rel, abs: abs, suffix: suffix}, nil
	default:
		return located{kind: locateMiss, suffix: suffix}, nil
	}
}

func (s *Server) statIn(root, rel string) (string, os.FileInfo, statKind, error) {
	if root == "" {
		return "", nil, statMissing, nil
	}
	abs := root
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return abs, nil, statMissing, nil
		}
		return "", nil, statMissing, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return abs, info, statSymlink, nil
	}
	if rel != "" {
		for _, seg := range strings.Split(rel, "/") {
			if seg == "" || ignoredName(seg) {
				return "", nil, statMissing, ErrPathInvalid
			}
			abs = filepath.Join(abs, seg)
			if !withinRoot(root, abs) {
				return "", nil, statMissing, ErrPathEscape
			}
			info, err = os.Lstat(abs)
			if err != nil {
				if os.IsNotExist(err) {
					return abs, nil, statMissing, nil
				}
				return "", nil, statMissing, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return abs, info, statSymlink, nil
			}
		}
	}
	same, err := sameMount(root, abs, info)
	if err != nil {
		return "", nil, statMissing, err
	}
	if !same {
		return abs, info, statOther, nil
	}
	if info.IsDir() {
		return abs, info, statDir, nil
	}
	if info.Mode().IsRegular() {
		return abs, info, statFile, nil
	}
	return abs, info, statMissing, nil
}

func sameMount(root, abs string, info os.FileInfo) (bool, error) {
	ri, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	rd, rok := devOf(ri)
	id, iok := devOf(info)
	if rok && iok && rd == id {
		return true, nil
	}
	// Regular files on this host can report a different st_dev than their
	// parent directory while statfs still shows one filesystem. A real
	// mount has a different fsid, so that is the check that rejects it.
	return sameFsid(root, abs)
}

func sameFsid(a, b string) (bool, error) {
	var sa, sb syscall.Statfs_t
	if err := syscall.Statfs(a, &sa); err != nil {
		return false, err
	}
	if err := syscall.Statfs(b, &sb); err != nil {
		return false, err
	}
	return sa.Fsid == sb.Fsid && sa.Type == sb.Type, nil
}

func capMatches(in []string) ([]string, bool) {
	if len(in) <= maxMatchList {
		return in, false
	}
	out := make([]string, maxMatchList)
	copy(out, in[:maxMatchList])
	return out, true
}

func applyReferer(matches []string, referer string) []string {
	key := refererProject(referer)
	if key == "" || len(matches) == 0 {
		return matches
	}
	var filtered []string
	prefix := key + "/"
	for _, m := range matches {
		if m == key || strings.HasPrefix(m, prefix) {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) == 0 {
		return matches
	}
	return filtered
}

func refererProject(referer string) string {
	if referer == "" {
		return ""
	}
	u, err := url.Parse(referer)
	if err != nil {
		return ""
	}
	p := u.Path
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	const marker = "/view/"
	if !strings.HasPrefix(p, marker) {
		return ""
	}
	rel, err := cleanRel(strings.TrimPrefix(p, marker))
	if err != nil || rel == "" {
		return ""
	}
	if strings.HasPrefix(rel, "projects/") {
		parts := strings.SplitN(rel, "/", 3)
		if len(parts) >= 2 && parts[1] != "" {
			return "projects/" + parts[1]
		}
		return ""
	}
	seg, _, _ := strings.Cut(rel, "/")
	return seg
}
