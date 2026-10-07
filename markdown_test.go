package main

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderMarkdownBasics(t *testing.T) {
	html, err := renderMarkdown([]byte("# Hello\n\n**bold** and `code`\n\n- item\n"), "doc.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{"<h1", "Hello", "<strong>bold</strong>", "<code>code</code>", "<li>"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
}

func TestRenderMarkdownEscapesHTML(t *testing.T) {
	html, err := renderMarkdown([]byte("ok <script>alert(1)</script>"), "x.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if strings.Contains(s, "<script>") {
		t.Fatalf("raw HTML leaked: %s", s)
	}
}

func TestRenderMarkdownMath(t *testing.T) {
	src := "The area is $A = \\pi r^2$.\n\n$$\nE = mc^2\n$$\n"
	html, err := renderMarkdown([]byte(src), "m.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if !strings.Contains(s, `class="math-inline"`) {
		t.Fatalf("missing inline math: %s", s)
	}
	if !strings.Contains(s, `class="math-display"`) {
		t.Fatalf("missing display math: %s", s)
	}
	if !strings.Contains(s, `A = \pi r^2`) {
		t.Fatalf("inline tex missing: %s", s)
	}
	if !strings.Contains(s, `E = mc^2`) {
		t.Fatalf("block tex missing: %s", s)
	}
}

func TestRenderMarkdownMathUnderscore(t *testing.T) {
	html, err := renderMarkdown([]byte("see $a_b + c$ please"), "m.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if strings.Contains(s, "<em>") {
		t.Fatalf("underscore became emphasis: %s", s)
	}
	if !strings.Contains(s, "a_b + c") {
		t.Fatalf("tex missing: %s", s)
	}
}

func TestRenderMarkdownSameLineDisplay(t *testing.T) {
	html, err := renderMarkdown([]byte("$$E = mc^2$$\n"), "m.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if !strings.Contains(s, `class="math-display"`) {
		t.Fatalf("missing display math: %s", s)
	}
	if !strings.Contains(s, "E = mc^2") {
		t.Fatalf("tex missing: %s", s)
	}
}

func TestRenderMarkdownParenDelims(t *testing.T) {
	html, err := renderMarkdown([]byte("inline \\(x^2\\) done\n\n\\[\ny\n\\]\n"), "m.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if !strings.Contains(s, `class="math-inline"`) || !strings.Contains(s, "x^2") {
		t.Fatalf("paren inline: %s", s)
	}
	if !strings.Contains(s, `class="math-display"`) {
		t.Fatalf("bracket block: %s", s)
	}
}

func TestRenderMarkdownMathAsterisk(t *testing.T) {
	html, err := renderMarkdown([]byte("see $a^*=x-b^*$ ok"), "m.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if strings.Contains(s, "<em>") {
		t.Fatalf("asterisk became emphasis: %s", s)
	}
	if !strings.Contains(s, "a^*=x-b^*") {
		t.Fatalf("tex missing: %s", s)
	}
}

func TestRenderMarkdownAlignedBlock(t *testing.T) {
	src := "$$\n\\begin{aligned}\na &= b \\\\\nc &= d\n\\end{aligned}\n$$\n"
	html, err := renderMarkdown([]byte(src), "m.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if !strings.Contains(s, `class="math-display"`) {
		t.Fatalf("missing display: %s", s)
	}
	if !strings.Contains(s, `\begin{aligned}`) || !strings.Contains(s, `a &amp;= b`) {
		t.Fatalf("aligned tex missing: %s", s)
	}
}

func TestRenderMarkdownDropsJavascriptURL(t *testing.T) {
	html, err := renderMarkdown([]byte("[x](javascript:alert(1))"), "m.md")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(html))
	if strings.Contains(s, "javascript:") {
		t.Fatalf("javascript url survived: %s", html)
	}
}

func TestCurrencyNotMath(t *testing.T) {
	html, err := renderMarkdown([]byte("It costs $20 and $30 today."), "m.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if strings.Contains(s, "math-inline") {
		t.Fatalf("currency parsed as math: %s", s)
	}
}

func TestRenderMarkdownRelativeURLs(t *testing.T) {
	src := "![x](pic.png)\n\n[n](notes.md#sec)\n\n[abs](https://example.com/a)\n"
	html, err := renderMarkdown([]byte(src), "docs/readme.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if !strings.Contains(s, `/api/file/docs/pic.png?inline=1`) {
		t.Fatalf("image rewrite: %s", s)
	}
	if !strings.Contains(s, `/view/docs/notes.md#sec`) {
		t.Fatalf("link rewrite: %s", s)
	}
	if !strings.Contains(s, `https://example.com/a`) {
		t.Fatalf("absolute link: %s", s)
	}
}

func TestMarkdownViewHTML(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	body := "# Title\n\nSee $E=mc^2$ and:\n\n$$\n\\int_0^1 x dx\n$$\n"
	writeRel(t, s.tree, "notes/doc.md", body)

	res, err := http.Get(ts.URL + "/view/notes/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	html := string(b)
	if res.StatusCode != 200 {
		t.Fatalf("status %d %s", res.StatusCode, html)
	}
	if !strings.Contains(html, `id="md-preview"`) || !strings.Contains(html, "<h1") {
		t.Fatalf("missing preview: %s", html)
	}
	if !strings.Contains(html, `id="md-raw"`) || !strings.Contains(html, "# Title") {
		t.Fatalf("missing raw: %s", html)
	}
	if !strings.Contains(html, `hidden`) {
		t.Fatal("one of the panes should be hidden")
	}
	if !strings.Contains(html, `data-md-mode="preview"`) || !strings.Contains(html, `data-md-mode="raw"`) {
		t.Fatal("missing mode switch")
	}
	if !strings.Contains(html, "katex.min.js") || !strings.Contains(html, "katex.min.css") {
		t.Fatal("missing katex assets")
	}
	if !strings.Contains(html, `class="math-inline"`) || !strings.Contains(html, `class="math-display"`) {
		t.Fatalf("missing math spans: %s", html)
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "cdn.jsdelivr.net") {
		t.Fatalf("csp %s", res.Header.Get("Content-Security-Policy"))
	}

	res, err = http.Get(ts.URL + "/view/notes/doc.md?mode=raw")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ = io.ReadAll(res.Body)
	html = string(b)
	if !strings.Contains(html, `id="md-preview" hidden`) {
		t.Fatalf("raw mode should hide preview: %s", html)
	}
}

func TestNonMarkdownViewHasNoKatex(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	writeRel(t, s.tree, "a.txt", "plain")
	res, err := http.Get(ts.URL + "/view/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	html := string(b)
	if strings.Contains(html, "katex.min.js") || strings.Contains(html, "katex.min.css") {
		t.Fatal("katex loaded for plain text")
	}
}

func TestRenderMarkdownTreePaths(t *testing.T) {
	src := "" +
		"See projects/alpha/notes/keep.md for the list.\n\n" +
		"Open `projects/alpha/extra/keep.md` now.\n\n" +
		"Path /home/box/knowledge/projects/gamma/unique.md end.\n\n" +
		"[n](projects/alpha/notes/keep.md)\n\n" +
		"![p](/home/box/knowledge/projects/gamma/pic.png)\n\n" +
		"[v](/view/already.md)\n\n" +
		"[h](https://example.com/projects/x)\n\n" +
		"```\nprojects/secret.md\n```\n"
	html, err := renderMarkdown([]byte(src), "docs/readme.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{
		`/view/projects/alpha/notes/keep.md`,
		`/view/projects/alpha/extra/keep.md`,
		`/view/projects/gamma/unique.md`,
		`/api/file/projects/gamma/pic.png?inline=1`,
		`/view/already.md`,
		`https://example.com/projects/x`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "/view/docs/projects/") {
		t.Fatalf("joined projects onto the current directory: %s", s)
	}
	if strings.Contains(s, "/view/projects/secret.md") {
		t.Fatalf("rewrote a fenced path: %s", s)
	}
	if strings.Contains(s, "/home/box/knowledge/projects/gamma/pic.png") {
		t.Fatalf("left the image path absolute: %s", s)
	}
}

func TestRelFileMention(t *testing.T) {
	ok := []string{
		"2026-09-08-derender-market-research.md",
		"originals/2026-09-08-derender-market-verdict.md",
		"INDEX.md",
		"notes.markdown",
		"readme.TXT",
		"projects/alpha/notes/keep.md",
	}
	for _, raw := range ok {
		if got := relFileMention(raw); got != raw {
			t.Fatalf("relFileMention(%q)=%q", raw, got)
		}
	}
	skip := []string{
		"",
		"code",
		"ls -la",
		"https://example.com/a.md",
		"/abs/file.md",
		"../escape.md",
		"a/../b.md",
		"git status",
		"foo.bar",
		"INDEX.md ",
		".md",
	}
	for _, raw := range skip {
		if got := relFileMention(raw); got != "" {
			t.Fatalf("relFileMention(%q)=%q, want empty", raw, got)
		}
	}
}

func TestRenderMarkdownRelFiles(t *testing.T) {
	src := "sources: `2026-09-08-derender-market-research.md` and " +
		"`originals/2026-09-08-derender-market-verdict.md` and " +
		"`missing.md` and `../escape.md` and `INDEX.md` and `ls -la`.\n"
	exists := map[string]bool{
		"projects/cv/notes/2026-09-08-derender-market-research.md":          true,
		"projects/cv/notes/originals/2026-09-08-derender-market-verdict.md": true,
		"INDEX.md": true,
	}
	html, _, err := renderViewMarkdown([]byte(src), "projects/cv/notes/2026-09-18-florence-journal-hebdo-fr.md", func(rel string) bool {
		return exists[rel]
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	sib := `/view/projects/cv/notes/2026-09-08-derender-market-research.md`
	sub := `/view/projects/cv/notes/originals/2026-09-08-derender-market-verdict.md`
	if !strings.Contains(s, `<a href="`+sib+`"><code>2026-09-08-derender-market-research.md</code></a>`) {
		t.Fatalf("sibling link: %s", s)
	}
	if !strings.Contains(s, `<a href="`+sub+`"><code>originals/2026-09-08-derender-market-verdict.md</code></a>`) {
		t.Fatalf("subdir link: %s", s)
	}
	if strings.Contains(s, `/view/projects/cv/notes/missing.md`) {
		t.Fatalf("linked a missing file: %s", s)
	}
	if strings.Contains(s, `../escape.md</code></a>`) || strings.Contains(s, `/view/projects/cv/escape.md`) {
		t.Fatalf("linked a parent escape: %s", s)
	}
	if strings.Contains(s, `/view/INDEX.md`) {
		t.Fatalf("linked a tree-root INDEX.md: %s", s)
	}
	if strings.Contains(s, `<code>ls -la</code></a>`) {
		t.Fatalf("linked a shell command: %s", s)
	}
	if !strings.Contains(s, `<code>missing.md</code>`) || !strings.Contains(s, `<code>INDEX.md</code>`) {
		t.Fatalf("missing spans should stay code: %s", s)
	}
}

func TestRenderMarkdownRelFileKeepsTreeAbsoluteBacktick(t *testing.T) {
	src := "Open `projects/alpha/extra/keep.md` now.\n"
	html, _, err := renderViewMarkdown([]byte(src), "projects/cv/notes/journal.md", func(string) bool {
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	if !strings.Contains(s, `/view/projects/alpha/extra/keep.md`) {
		t.Fatalf("tree-absolute backtick: %s", s)
	}
	if strings.Contains(s, `/view/projects/cv/notes/projects/`) {
		t.Fatalf("joined a tree-absolute path onto the viewed directory: %s", s)
	}
}

func TestMarkdownViewRelFileLinks(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	body := "sources: `2026-09-08-derender-market-research.md` and " +
		"`originals/2026-09-08-derender-market-verdict.md` and " +
		"`missing.md` and `../escape.md` and `INDEX.md`.\n"
	writeRel(t, s.tree, "projects/cv/notes/2026-09-18-florence-journal-hebdo-fr.md", body)
	writeRel(t, s.tree, "projects/cv/notes/2026-09-08-derender-market-research.md", "sib")
	writeRel(t, s.tree, "projects/cv/notes/originals/2026-09-08-derender-market-verdict.md", "sub")
	writeRel(t, s.tree, "INDEX.md", "root-index")

	res, err := http.Get(ts.URL + "/view/projects/cv/notes/2026-09-18-florence-journal-hebdo-fr.md")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	html := string(b)
	if res.StatusCode != 200 {
		t.Fatalf("status %d %s", res.StatusCode, html)
	}
	if !strings.Contains(html, `/view/projects/cv/notes/2026-09-08-derender-market-research.md`) {
		t.Fatalf("sibling: %s", html)
	}
	if !strings.Contains(html, `/view/projects/cv/notes/originals/2026-09-08-derender-market-verdict.md`) {
		t.Fatalf("subdir: %s", html)
	}
	if strings.Contains(html, `/view/projects/cv/notes/missing.md`) {
		t.Fatalf("missing file linked: %s", html)
	}
	if strings.Contains(html, `/view/projects/cv/escape.md`) {
		t.Fatalf("parent escape linked: %s", html)
	}
	if strings.Contains(html, `href="/view/INDEX.md"`) {
		t.Fatalf("global INDEX.md linked: %s", html)
	}

	res, err = http.Get(ts.URL + "/view/projects/cv/notes/2026-09-18-florence-journal-hebdo-fr.md?mode=raw")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ = io.ReadAll(res.Body)
	html = string(b)
	if !strings.Contains(html, `id="md-preview" hidden`) {
		t.Fatalf("raw mode should hide preview: %s", html)
	}
	rawStart := strings.Index(html, `id="md-raw"`)
	if rawStart < 0 {
		t.Fatalf("missing raw pane: %s", html)
	}
	raw := html[rawStart:]
	if !strings.Contains(raw, "`2026-09-08-derender-market-research.md`") {
		t.Fatalf("raw pane lost backticks: %s", raw)
	}
}

func mermaidDivContent(t *testing.T, rendered string) string {
	t.Helper()
	const open = `<div class="mermaid">`
	i := strings.Index(rendered, open)
	if i < 0 {
		t.Fatalf("missing mermaid div: %s", rendered)
	}
	rest := rendered[i+len(open):]
	j := strings.Index(rest, "</div>")
	if j < 0 {
		t.Fatalf("unclosed mermaid div: %s", rendered)
	}
	return html.UnescapeString(rest[:j])
}

func TestRenderMarkdownMermaidPreservesSource(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{
			name: "mindmap",
			source: "" +
				"mindmap\n" +
				"  Root\n" +
				"    Origins\n" +
				"      History\n" +
				"    Research\n" +
				"      On effectiveness\n",
		},
		{
			name:   "flowchart",
			source: "graph TD\n  A-->B\n  B-->C\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md := "```mermaid\n" + tc.source + "```\n"
			rendered, mermaid, err := renderViewMarkdown([]byte(md), "d.md", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !mermaid {
				t.Fatal("expected a mermaid block")
			}
			got := mermaidDivContent(t, string(rendered))
			if got != tc.source {
				t.Fatalf("mermaid source mismatch\nwant %q\ngot  %q", tc.source, got)
			}
		})
	}
}

func TestRenderMarkdownMermaid(t *testing.T) {
	src := "```mermaid\ngraph TD\n  A-->B\n```\n\n```js\nconst x = 1;\n```\n"
	html, mermaid, err := renderViewMarkdown([]byte(src), "d.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !mermaid {
		t.Fatal("expected a mermaid block")
	}
	s := string(html)
	if !strings.Contains(s, `class="mermaid-wrap"`) || !strings.Contains(s, `class="mermaid"`) {
		t.Fatalf("missing mermaid wrap: %s", s)
	}
	if !strings.Contains(s, "graph TD") || !strings.Contains(s, "A--&gt;B") {
		t.Fatalf("mermaid source missing or not escaped: %s", s)
	}
	if !strings.Contains(s, `class="language-js"`) || !strings.Contains(s, "const x = 1;") {
		t.Fatalf("js fence changed: %s", s)
	}
	if strings.Contains(s, `class="language-mermaid"`) {
		t.Fatalf("mermaid stayed a code fence: %s", s)
	}
}

func TestRenderMarkdownMermaidEscapes(t *testing.T) {
	src := "```mermaid\ngraph TD\n  A[\"<script>alert(1)</script>\"] --> B\n```\n"
	html, mermaid, err := renderViewMarkdown([]byte(src), "d.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !mermaid {
		t.Fatal("expected a mermaid block")
	}
	s := string(html)
	if strings.Contains(s, "<script>alert(1)</script>") {
		t.Fatalf("raw HTML leaked: %s", s)
	}
	if !strings.Contains(s, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("source not escaped: %s", s)
	}
}

func TestRenderMarkdownMermaidIgnoresOtherFences(t *testing.T) {
	src := "```\nmermaid\n```\n\n```go\nfunc main() {}\n```\n\n`mermaid`\n"
	html, mermaid, err := renderViewMarkdown([]byte(src), "d.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	if mermaid {
		t.Fatal("plain fences and inline code should not count as mermaid")
	}
	s := string(html)
	if strings.Contains(s, `class="mermaid"`) {
		t.Fatalf("non-mermaid fence became a diagram: %s", s)
	}
	if !strings.Contains(s, `class="language-go"`) {
		t.Fatalf("go fence missing: %s", s)
	}
}

func TestMarkdownViewMermaidAssets(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	writeRel(t, s.tree, "notes/flow.md", "# Flow\n\n```mermaid\ngraph LR\n  A-->B\n```\n")
	writeRel(t, s.tree, "notes/plain.md", "# Hi\n\n```js\n1\n```\n")

	res, err := http.Get(ts.URL + "/view/notes/flow.md")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	html := string(b)
	if res.StatusCode != 200 {
		t.Fatalf("status %d %s", res.StatusCode, html)
	}
	if !strings.Contains(html, mermaidScriptSrc) {
		t.Fatalf("missing mermaid script: %s", html)
	}
	if !strings.Contains(html, `class="mermaid"`) || !strings.Contains(html, "graph LR") {
		t.Fatalf("missing mermaid source: %s", html)
	}
	if !strings.Contains(html, "securityLevel") || !strings.Contains(html, `"strict"`) {
		t.Fatalf("missing strict mermaid config: %s", html)
	}

	res, err = http.Get(ts.URL + "/view/notes/flow.md?mode=raw")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ = io.ReadAll(res.Body)
	raw := string(b)
	if strings.Contains(raw, mermaidScriptSrc) {
		t.Fatalf("mermaid script loaded in raw mode: %s", raw)
	}
	if !strings.Contains(raw, `id="md-preview" hidden`) {
		t.Fatalf("raw mode should hide preview: %s", raw)
	}

	res, err = http.Get(ts.URL + "/view/notes/plain.md")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ = io.ReadAll(res.Body)
	plain := string(b)
	if strings.Contains(plain, "mermaid.min.js") || strings.Contains(plain, `class="mermaid"`) {
		t.Fatalf("mermaid loaded without a mermaid fence: %s", plain)
	}
}

func TestNonMarkdownViewHasNoMermaid(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	writeRel(t, s.tree, "a.txt", "```mermaid\ngraph TD\n  A-->B\n```\n")
	res, err := http.Get(ts.URL + "/view/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	html := string(b)
	if strings.Contains(html, mermaidScriptSrc) || strings.Contains(html, "mermaid.min.js") {
		t.Fatal("mermaid loaded for plain text")
	}
	if strings.Contains(html, `class="mermaid"`) {
		t.Fatal("plain text rendered mermaid HTML")
	}
}

const mermaidScriptSrc = `src="https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js"`
