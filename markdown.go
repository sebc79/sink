package main

import (
	"bytes"
	"html/template"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

func isMarkdownName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd", ".mdwn":
		return true
	}
	return false
}

func renderMarkdown(src []byte, relPath string) (template.HTML, error) {
	src = linkifyTreeMentions(src)
	dir := parentRel(relPath)
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Footnote,
			&mathExtender{},
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(
				util.Prioritized(mdURLTransformer{dir: dir}, 100),
			),
		),
	)
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

type mdURLTransformer struct {
	dir string
}

func (t mdURLTransformer) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Image:
			n.Destination = rewriteMarkdownDest(t.dir, n.Destination, true)
		case *ast.Link:
			n.Destination = rewriteMarkdownDest(t.dir, n.Destination, false)
		}
		return ast.WalkContinue, nil
	})
}

func rewriteMarkdownDest(dir string, dest []byte, image bool) []byte {
	s := strings.TrimSpace(string(dest))
	if s == "" || strings.HasPrefix(s, "#") {
		return dest
	}
	u, err := url.Parse(s)
	if err != nil {
		return dest
	}
	if u.Scheme != "" || u.Host != "" {
		return dest
	}
	if strings.HasPrefix(u.Path, "/view/") || strings.HasPrefix(u.Path, "/api/") {
		return dest
	}
	if rest, ok := strings.CutPrefix(u.Path, "/home/box/knowledge/"); ok {
		if out := formatMarkdownDest(rest, image, u); out != nil {
			return out
		}
		return dest
	}
	if strings.HasPrefix(u.Path, "/") {
		return dest
	}
	// projects/ is a tree path, not a link relative to the current file.
	if strings.HasPrefix(u.Path, "projects/") {
		if out := formatMarkdownDest(u.Path, image, u); out != nil {
			return out
		}
		return dest
	}
	joined := u.Path
	if dir != "" {
		joined = path.Join(dir, u.Path)
	}
	if out := formatMarkdownDest(joined, image, u); out != nil {
		return out
	}
	return dest
}

func formatMarkdownDest(rel string, image bool, u *url.URL) []byte {
	clean, err := cleanRel(rel)
	if err != nil || clean == "" || hasIgnoredSegment(clean) {
		return nil
	}
	out := "/view/" + urlPath(clean)
	if image {
		out = "/api/file/" + urlPath(clean) + "?inline=1"
	} else if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		out += "#" + url.PathEscape(u.Fragment)
	}
	return []byte(out)
}

func linkifyTreeMentions(src []byte) []byte {
	text := string(src)
	if !strings.Contains(text, "projects/") && !strings.Contains(text, "/home/box/knowledge/") {
		return src
	}
	lines := strings.Split(text, "\n")
	inFence := false
	fence := ""
	for i, line := range lines {
		trim := strings.TrimLeft(line, " \t")
		if !inFence && (strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~")) {
			inFence = true
			fence = trim[:3]
			continue
		}
		if inFence {
			if fence != "" && strings.HasPrefix(trim, fence) {
				inFence = false
			}
			continue
		}
		lines[i] = linkifyLine(line)
	}
	return []byte(strings.Join(lines, "\n"))
}

func linkifyLine(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	for i := 0; i < len(line); {
		if line[i] == '`' {
			rel := strings.IndexByte(line[i+1:], '`')
			if rel < 0 {
				b.WriteString(line[i:])
				break
			}
			end := i + 1 + rel
			inner := line[i+1 : end]
			if link, ok := markdownPathLink(inner); ok {
				b.WriteString(link)
			} else {
				b.WriteString(line[i : end+1])
			}
			i = end + 1
			continue
		}
		if line[i] == '!' && i+1 < len(line) && line[i+1] == '[' {
			if end, ok := skipMDLink(line, i+1); ok {
				b.WriteString(line[i:end])
				i = end
				continue
			}
		}
		if line[i] == '[' {
			if end, ok := skipMDLink(line, i); ok {
				b.WriteString(line[i:end])
				i = end
				continue
			}
		}
		if i == 0 || pathBoundary(line[i-1]) {
			if raw, n, ok := matchTreePath(line[i:]); ok {
				if link, ok := markdownPathLink(raw); ok {
					b.WriteString(link)
					i += n
					continue
				}
			}
		}
		b.WriteByte(line[i])
		i++
	}
	return b.String()
}

func pathBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '(', '"', '\'', '<':
		return true
	default:
		return false
	}
}

func skipMDLink(line string, i int) (int, bool) {
	if i >= len(line) || line[i] != '[' {
		return 0, false
	}
	rel := strings.Index(line[i:], "](")
	if rel < 0 {
		return 0, false
	}
	dest := i + rel + 2
	paren := strings.IndexByte(line[dest:], ')')
	if paren < 0 {
		return 0, false
	}
	return dest + paren + 1, true
}

func matchTreePath(s string) (string, int, bool) {
	pref := ""
	switch {
	case strings.HasPrefix(s, "/home/box/knowledge/"):
		pref = "/home/box/knowledge/"
	case strings.HasPrefix(s, "projects/"):
		pref = "projects/"
	default:
		return "", 0, false
	}
	i := len(pref)
	if i >= len(s) || !isPathChar(s[i]) {
		return "", 0, false
	}
	for i < len(s) && isPathChar(s[i]) {
		i++
	}
	end := i
	for end > len(pref) && isTrailPunct(s[end-1]) {
		end--
	}
	if end <= len(pref) {
		return "", 0, false
	}
	return s[:end], end, true
}

func isPathChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '/' || c == '.' || c == '_' || c == '-' || c == '~'
}

func isTrailPunct(c byte) bool {
	switch c {
	case '.', ',', ';', ':', '!', '?':
		return true
	default:
		return false
	}
}

func markdownPathLink(raw string) (string, bool) {
	target := treeViewPath(raw)
	if target == "" {
		return "", false
	}
	return "[" + raw + "](" + target + ")", true
}

func treeViewPath(raw string) string {
	rel := strings.TrimPrefix(raw, "/home/box/knowledge/")
	clean, err := cleanRel(rel)
	if err != nil || clean == "" || hasIgnoredSegment(clean) {
		return ""
	}
	return "/view/" + urlPath(clean)
}
