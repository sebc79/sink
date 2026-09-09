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

var (
	kindMathInline = ast.NewNodeKind("MathInline")
	kindMathBlock  = ast.NewNodeKind("MathBlock")
)

type mathInline struct {
	ast.BaseInline
	display bool
}

func (n *mathInline) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *mathInline) Kind() ast.NodeKind { return kindMathInline }

type mathBlock struct {
	ast.BaseBlock
	bracket bool
	closed  bool
}

func (n *mathBlock) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *mathBlock) Kind() ast.NodeKind { return kindMathBlock }

func (n *mathBlock) IsRaw() bool { return true }

type mathExtender struct{}

func (e *mathExtender) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithBlockParsers(util.Prioritized(&mathBlockParser{}, 701)),
		parser.WithInlineParsers(util.Prioritized(&mathInlineParser{}, 450)),
	)
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(&mathRenderer{}, 500),
	))
}

type mathInlineParser struct{}

func (p *mathInlineParser) Trigger() []byte { return []byte{'$', '\\'} }

func (p *mathInlineParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, segment := block.PeekLine()
	if len(line) == 0 {
		return nil
	}

	var opener, closer []byte
	display := false
	switch {
	case line[0] == '$':
		opener = []byte("$")
		closer = []byte("$")
		if len(line) > 1 && line[1] == '$' {
			opener = []byte("$$")
			closer = []byte("$$")
			display = true
		}
	case line[0] == '\\' && len(line) > 1 && line[1] == '(':
		opener = []byte("\\(")
		closer = []byte("\\)")
	case line[0] == '\\' && len(line) > 1 && line[1] == '[':
		opener = []byte("\\[")
		closer = []byte("\\]")
		display = true
	default:
		return nil
	}

	if !display && len(opener) == 1 {
		if len(line) <= 1 || isMathSpace(line[1]) {
			return nil
		}
	}

	start := len(opener)
	i := start
	for i < len(line) {
		if line[i] == '\\' && i+1 < len(line) && !bytes.HasPrefix(line[i:], closer) {
			i += 2
			continue
		}
		if !bytes.HasPrefix(line[i:], closer) {
			i++
			continue
		}
		if !display && len(closer) == 1 {
			if i > start && isMathSpace(line[i-1]) {
				i++
				continue
			}
			if i+1 < len(line) && line[i+1] >= '0' && line[i+1] <= '9' {
				i++
				continue
			}
		}
		if i == start {
			return nil
		}
		node := &mathInline{display: display}
		node.AppendChild(node, ast.NewRawTextSegment(text.NewSegment(segment.Start+start, segment.Start+i)))
		block.Advance(i + len(closer))
		return node
	}
	return nil
}

type mathBlockParser struct{}

func (p *mathBlockParser) Trigger() []byte { return []byte{'$', '\\'} }

func (p *mathBlockParser) Open(_ ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || pos >= len(line) {
		return nil, parser.NoChildren
	}
	rest := line[pos:]
	bracket := false
	openerLen := 0
	switch {
	case bytes.HasPrefix(rest, []byte("$$")):
		openerLen = 2
	case bytes.HasPrefix(rest, []byte("\\[")):
		openerLen = 2
		bracket = true
	default:
		return nil, parser.NoChildren
	}

	node := &mathBlock{bracket: bracket}
	closer := []byte("$$")
	if bracket {
		closer = []byte("\\]")
	}
	after := bytes.TrimRight(rest[openerLen:], "\r\n")
	if idx := bytes.Index(after, closer); idx >= 0 && len(bytes.TrimSpace(after[idx+len(closer):])) == 0 {
		if idx > 0 {
			start := segment.Start + pos + openerLen
			node.Lines().Append(text.NewSegment(start, start+idx))
		}
		node.closed = true
		return node, parser.NoChildren
	}
	if len(bytes.TrimSpace(after)) > 0 {
		start := segment.Start + pos + openerLen
		node.Lines().Append(text.NewSegment(start, start+len(after)))
	}
	return node, parser.NoChildren
}

func (p *mathBlockParser) Continue(node ast.Node, reader text.Reader, _ parser.Context) parser.State {
	n := node.(*mathBlock)
	if n.closed {
		return parser.Close
	}
	line, segment := reader.PeekLine()
	w, pos := util.IndentWidth(line, reader.LineOffset())
	closer := []byte("$$")
	if n.bracket {
		closer = []byte("\\]")
	}
	if w < 4 && pos >= 0 && pos <= len(line) && bytes.HasPrefix(line[pos:], closer) {
		after := bytes.TrimSpace(bytes.TrimRight(line[pos+len(closer):], "\r\n"))
		if len(after) == 0 {
			newline := 0
			if len(line) > 0 && line[len(line)-1] == '\n' {
				newline = 1
			}
			reader.Advance(segment.Stop - segment.Start - newline + segment.Padding)
			return parser.Close
		}
	}
	end := segment.Stop
	if end > segment.Start && reader.Source()[end-1] == '\n' {
		end--
		if end > segment.Start && reader.Source()[end-1] == '\r' {
			end--
		}
	}
	node.Lines().Append(text.NewSegment(segment.Start, end))
	advance := segment.Stop - segment.Start
	if advance > 0 && reader.Source()[segment.Stop-1] == '\n' {
		advance--
	}
	reader.Advance(advance)
	return parser.Continue | parser.NoChildren
}

func (p *mathBlockParser) Close(ast.Node, text.Reader, parser.Context) {}

func (p *mathBlockParser) CanInterruptParagraph() bool { return true }

func (p *mathBlockParser) CanAcceptIndentedLine() bool { return false }

type mathRenderer struct{}

func (r *mathRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindMathInline, r.renderInline)
	reg.Register(kindMathBlock, r.renderBlock)
}

func (r *mathRenderer) renderInline(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*mathInline)
	class := "math-inline"
	if n.display {
		class = "math-display"
	}
	_, _ = w.WriteString(`<span class="` + class + `">`)
	writeMathText(w, source, n)
	_, _ = w.WriteString(`</span>`)
	return ast.WalkSkipChildren, nil
}

func (r *mathRenderer) renderBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<div class="math-display">`)
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		if i > 0 {
			_ = w.WriteByte('\n')
		}
		seg := lines.At(i)
		_, _ = w.Write(util.EscapeHTML(seg.Value(source)))
	}
	_, _ = w.WriteString("</div>\n")
	return ast.WalkContinue, nil
}

func writeMathText(w util.BufWriter, source []byte, n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			_, _ = w.Write(util.EscapeHTML(t.Segment.Value(source)))
		}
	}
}

func isMathSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
