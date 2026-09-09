package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{
		StorageDir: t.TempDir(),
		MaxUpload:  8 << 20,
		MaxExtract: 8 << 20,
		MaxFiles:   1000,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUploadRawCurlDefaultContentType(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=curl.txt", strings.NewReader("from-curl"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	got, err := os.ReadFile(s.root + "/curl.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-curl" {
		t.Fatalf("got %q", got)
	}
}

func TestUploadRawAndGetFile(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=docs/notes.txt", strings.NewReader("hello sink"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	var out uploadResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Path != "docs/notes.txt" || out.Stored != "file" {
		t.Fatalf("result %+v", out)
	}

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

func TestUploadMultipartUnpackZip(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	body, ctype := multipartFile(t, "vendor/lib", "auto", "lib.zip", makeZip(t, map[string]string{
		"mod.go":    "package mod",
		"sub/x.txt": "x",
	}))
	res, err := http.Post(ts.URL+"/api/upload", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, b)
	}
	got, err := http.Get(ts.URL + "/api/file/vendor/lib/mod.go")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	b, _ := io.ReadAll(got.Body)
	if string(b) != "package mod" {
		t.Fatalf("got %q", b)
	}

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

func TestUploadPathTraversalRejected(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=../../etc/passwd", strings.NewReader("x"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestFileDirConflict(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	put := func(path, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path="+path, strings.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := put("docs/a.txt", "one")
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("first %d", res.StatusCode)
	}
	res = put("docs/a.txt/extra", "two")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("nested under file: %d %s", res.StatusCode, b)
	}
}

func TestArchiveDownload(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=docs/a.txt", strings.NewReader("aaa"))
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()

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

func TestSkillRewritesBaseURL(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/skill")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
		t.Fatalf("content-type %s", ct)
	}
	b, _ := io.ReadAll(res.Body)
	body := string(b)
	if strings.Contains(body, "{{BASE_URL}}") {
		t.Fatal("placeholder not replaced")
	}
	if !strings.Contains(body, ts.URL+"/api/upload") {
		t.Fatalf("missing base url in skill:\n%s", body)
	}
	if !strings.Contains(body, "unpack") {
		t.Fatal("skill missing unpack docs")
	}
}

func TestBrowseAndViewHTML(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=readme.md", strings.NewReader("# hello"))
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()

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
	if !strings.Contains(html, `action="/upload"`) {
		t.Fatal("missing upload form")
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

func TestHTMLUploadForm(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	body, ctype := multipartFile(t, "form.txt", "none", "form.txt", []byte("from-form"))
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Post(ts.URL+"/upload", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, b)
	}
	got, err := os.ReadFile(s.root + "/form.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-form" {
		t.Fatalf("got %q", got)
	}
}

func TestUnpackNonArchiveFails(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=d&unpack=auto", strings.NewReader("not an archive"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestTrailingSlashFileRejected(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?path=dir/", strings.NewReader("x"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func multipartFile(t *testing.T, path, unpack, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("path", path); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("unpack", unpack); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}
