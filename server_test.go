package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{
		StorageDir: t.TempDir(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGetFileFromStorage(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	writeRel(t, s.root, "docs/notes.txt", "hello sink")

	got, err := http.Get(ts.URL + "/api/file/docs/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	b, _ := io.ReadAll(got.Body)
	if got.StatusCode != 200 || string(b) != "hello sink" {
		t.Fatalf("get %d %q", got.StatusCode, b)
	}
}

func TestArchiveDownload(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	writeRel(t, s.root, "docs/a.txt", "aaa")

	res, err := http.Get(ts.URL + "/api/archive/docs?format=zip")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	b, _ := io.ReadAll(res.Body)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "docs/a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("zip entries: %v", names(zr))
	}

	res, err = http.Get(ts.URL + "/api/archive/docs?format=tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	gr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gr)
	found = false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "docs/a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("tar.gz missing docs/a.txt")
	}
}

func TestRemovedUploadEraRoutes(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	checks := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/skill"},
		{http.MethodGet, "/skill.md"},
		{http.MethodGet, "/SKILL.md"},
		{http.MethodPost, "/upload"},
		{http.MethodPost, "/flush"},
		{http.MethodPost, "/api/upload"},
		{http.MethodPost, "/api/flush"},
	}
	for _, tc := range checks {
		req, err := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s: status %d", tc.method, tc.path, res.StatusCode)
		}
	}
}

func TestBrowseAndViewHTML(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	writeRel(t, s.root, "readme.md", "# hello")

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	html := string(b)
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if !strings.Contains(html, "readme.md") {
		t.Fatalf("listing missing file: %s", html)
	}
	if strings.Contains(html, `action="/upload"`) || strings.Contains(html, ">Flush<") || strings.Contains(html, `href="/skill"`) {
		t.Fatalf("leftover upload-era UI: %s", html)
	}

	res, err = http.Get(ts.URL + "/?err=spoofed-upload-error")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ = io.ReadAll(res.Body)
	if strings.Contains(string(b), "spoofed-upload-error") {
		t.Fatal("browse still renders ?err=")
	}

	res, err = http.Get(ts.URL + "/view/readme.md")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ = io.ReadAll(res.Body)
	if !strings.Contains(string(b), "# hello") {
		t.Fatalf("view missing content: %s", b)
	}
}

func TestViewHasFullScreenButton(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	files := map[string]string{
		"doc.md":  "# Title\n\nbody\n",
		"a.txt":   "plain",
		"pic.png": "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR",
		"x.bin":   "\x00\x01\x02\x03",
	}
	for name, body := range files {
		writeRel(t, s.root, name, body)

		res, err := http.Get(ts.URL + "/view/" + name)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		html := string(b)
		if res.StatusCode != 200 {
			t.Fatalf("%s: status %d", name, res.StatusCode)
		}
		for _, want := range []string{`id="pv"`, `id="pv-fs"`, "Full screen", `class="pv-body"`} {
			if !strings.Contains(html, want) {
				t.Fatalf("%s: view missing %q", name, want)
			}
		}
	}

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(b), `id="pv-fs"`) {
		t.Fatal("full screen button should only be on view pages")
	}
}

func TestTreeListingFromStorage(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	writeRel(t, s.root, "vendor/lib/mod.go", "package mod")
	writeRel(t, s.root, "vendor/lib/sub/x.txt", "x")

	tree, err := http.Get(ts.URL + "/api/tree/vendor/lib?recursive=1")
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Body.Close()
	var listing treeResponse
	if err := json.NewDecoder(tree.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if !listing.OK || listing.Type != "directory" || len(listing.Entries) < 2 {
		t.Fatalf("tree %+v", listing)
	}
}
