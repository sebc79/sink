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
	if u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/") {
		return dest
	}
	joined := u.Path
	if dir != "" {
		joined = path.Join(dir, u.Path)
	}
	clean, err := cleanRel(joined)
	if err != nil {
		return dest
	}
	out := "/view/" + urlPath(clean)
	if image {
		out = "/api/file/" + urlPath(clean) + "?inline=1"
	}
	if u.Fragment != "" {
		out += "#" + url.PathEscape(u.Fragment)
	}
	return []byte(out)
}
