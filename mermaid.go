package main

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var kindMermaidBlock = ast.NewNodeKind("MermaidBlock")

type mermaidBlock struct {
	ast.BaseBlock
}

func (n *mermaidBlock) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *mermaidBlock) Kind() ast.NodeKind { return kindMermaidBlock }

func (n *mermaidBlock) IsRaw() bool { return true }

type mermaidExtender struct {
	found *bool
}

func (e *mermaidExtender) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithASTTransformers(
		util.Prioritized(mermaidTransformer{found: e.found}, 500),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(&mermaidRenderer{}, 500),
	))
}

type mermaidTransformer struct {
	found *bool
}

func (t mermaidTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	var blocks []*ast.FencedCodeBlock
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if fc, ok := n.(*ast.FencedCodeBlock); ok && isMermaidLang(fc.Language(source)) {
				blocks = append(blocks, fc)
			}
		}
		return ast.WalkContinue, nil
	})
	for _, fc := range blocks {
		parent := fc.Parent()
		if parent == nil {
			continue
		}
		node := &mermaidBlock{}
		node.SetLines(fc.Lines())
		parent.ReplaceChild(parent, fc, node)
		if t.found != nil {
			*t.found = true
		}
	}
}

func isMermaidLang(lang []byte) bool {
	return bytes.EqualFold(lang, []byte("mermaid"))
}

type mermaidRenderer struct{}

func (r *mermaidRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindMermaidBlock, r.render)
}

func (r *mermaidRenderer) render(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<div class="mermaid-wrap"><div class="mermaid">`)
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		_, _ = w.Write(util.EscapeHTML(seg.Value(source)))
	}
	_, _ = w.WriteString("</div></div>\n")
	return ast.WalkContinue, nil
}
