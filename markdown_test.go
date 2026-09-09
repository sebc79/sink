package main

import (
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
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=notes/doc.md", strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("upload %d", res.StatusCode)
	}

	res, err = http.Get(ts.URL + "/view/notes/doc.md")
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
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=a.txt", strings.NewReader("plain"))
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
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
