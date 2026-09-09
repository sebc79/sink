package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIFlushRemovesTreeAndKeepsRoot(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	mustUpload(t, ts.URL, "docs/a.txt", "aaa")
	mustUpload(t, ts.URL, "docs/sub/b.txt", "bbb")
	mustUpload(t, ts.URL, "root.bin", "xyz")

	res, err := http.Post(ts.URL+"/api/flush", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, b)
	}
	var out flushResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || !out.Flushed {
		t.Fatalf("result %+v", out)
	}

	ents, err := os.ReadDir(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("storage not empty: %v", dirNames(ents))
	}
	st, err := os.Stat(s.root)
	if err != nil || !st.IsDir() {
		t.Fatalf("storage root missing: %v", err)
	}

	tree, err := http.Get(ts.URL + "/api/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Body.Close()
	var listing treeResponse
	if err := json.NewDecoder(tree.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if !listing.OK || listing.Type != "directory" || len(listing.Entries) != 0 {
		t.Fatalf("tree %+v", listing)
	}

	mustUpload(t, ts.URL, "after.txt", "ok")
	got, err := os.ReadFile(filepath.Join(s.root, "after.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
}

func TestAPIFlushEmptyIsOK(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Post(ts.URL+"/api/flush", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, b)
	}
	ents, err := os.ReadDir(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("storage not empty: %v", dirNames(ents))
	}
}

func TestHTMLFlushButtonAndRedirect(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	mustUpload(t, ts.URL, "keep/me.txt", "nope")

	home, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer home.Body.Close()
	html, _ := io.ReadAll(home.Body)
	page := string(html)
	if !strings.Contains(page, `action="/flush"`) {
		t.Fatal("missing flush form")
	}
	if !strings.Contains(page, ">Flush</button>") {
		t.Fatal("missing Flush button")
	}
	if strings.Contains(page, "confirm(") {
		t.Fatal("flush must not prompt")
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Post(ts.URL+"/flush", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, b)
	}
	loc := res.Header.Get("Location")
	if loc != "/?ok=flushed" {
		t.Fatalf("location %q", loc)
	}

	ents, err := os.ReadDir(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("storage not empty: %v", dirNames(ents))
	}

	after, err := http.Get(ts.URL + "/?ok=flushed")
	if err != nil {
		t.Fatal(err)
	}
	defer after.Body.Close()
	b, _ := io.ReadAll(after.Body)
	body := string(b)
	if !strings.Contains(body, "Storage emptied.") {
		t.Fatalf("missing notice: %s", body)
	}
	if strings.Contains(body, "keep/me.txt") || strings.Contains(body, ">me.txt<") {
		t.Fatalf("listing still has flushed file: %s", body)
	}
}

func TestSkillDocumentsFlush(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/skill")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	body := string(b)
	if !strings.Contains(body, ts.URL+"/api/flush") {
		t.Fatalf("skill missing flush api:\n%s", body)
	}
}

func mustUpload(t *testing.T, base, path, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/api/upload?path="+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload %s: %d %s", path, res.StatusCode, b)
	}
}

func dirNames(ents []os.DirEntry) []string {
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}
