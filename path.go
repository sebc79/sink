package main

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	ErrPathEscape  = errors.New("path escapes tree root")
	ErrPathInvalid = errors.New("invalid path")
)

// cleanRel normalizes a tree-relative path to slash-separated form.
// The tree root is represented by an empty string.
func cleanRel(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) || !utf8.ValidString(rel) {
		return "", ErrPathInvalid
	}
	rel = strings.TrimSpace(rel)
	rel = strings.ReplaceAll(rel, `\`, "/")
	rel = strings.TrimLeft(rel, "/")
	if rel == "" || rel == "." {
		return "", nil
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return "", ErrPathEscape
	}
	cleaned := path.Clean(rel)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
		return "", ErrPathEscape
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == ".." {
			return "", ErrPathInvalid
		}
	}
	return cleaned, nil
}

func (s *Server) resolve(rel string) (abs string, clean string, err error) {
	clean, err = cleanRel(rel)
	if err != nil {
		return "", "", err
	}
	if hasIgnoredSegment(clean) {
		return "", "", ErrPathInvalid
	}
	abs = s.tree
	if clean != "" {
		abs = filepath.Join(s.tree, filepath.FromSlash(clean))
	}
	if !withinRoot(s.tree, abs) {
		return "", "", ErrPathEscape
	}
	return abs, clean, nil
}

func withinRoot(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func urlPath(rel string) string {
	if rel == "" {
		return ""
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = pathEscape(p)
	}
	return strings.Join(parts, "/")
}

func pathEscape(seg string) string {
	var b strings.Builder
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		const hex = "0123456789ABCDEF"
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xF])
	}
	return b.String()
}

func parentRel(rel string) string {
	if rel == "" {
		return ""
	}
	i := strings.LastIndex(rel, "/")
	if i < 0 {
		return ""
	}
	return rel[:i]
}

func baseRel(rel string) string {
	if rel == "" {
		return ""
	}
	return path.Base(rel)
}
