package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSniff(t *testing.T) {
	t.Parallel()
	z := makeZip(t, map[string]string{"a.txt": "hi"})
	got, err := sniffBytes(z)
	if err != nil || got != "zip" {
		t.Fatalf("zip sniff=%q err=%v", got, err)
	}
	tg := makeTarGz(t, map[string]string{"a.txt": "hi"})
	got, err = sniffBytes(tg)
	if err != nil || got != "tar.gz" {
		t.Fatalf("targz sniff=%q err=%v", got, err)
	}
	if _, err := sniffBytes([]byte("hello")); err == nil {
		t.Fatal("expected not archive")
	}
}

func TestUnpackZipAndTarGz(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	dest := filepath.Join(s.root, "out")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}

	zf := writeTemp(t, makeZip(t, map[string]string{
		"a.txt":   "alpha",
		"b/c.txt": "gamma",
	}))
	files, _, used, err := s.unpackFile(zf, dest, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if used != "zip" || files != 2 {
		t.Fatalf("used=%s files=%d", used, files)
	}
	assertFile(t, filepath.Join(dest, "a.txt"), "alpha")
	assertFile(t, filepath.Join(dest, "b", "c.txt"), "gamma")

	dest2 := filepath.Join(s.root, "out2")
	if err := os.MkdirAll(dest2, 0755); err != nil {
		t.Fatal(err)
	}
	tf := writeTemp(t, makeTarGz(t, map[string]string{
		"x.txt": "xyz",
	}))
	_, _, used, err = s.unpackFile(tf, dest2, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if used != "tar.gz" {
		t.Fatalf("used=%s", used)
	}
	assertFile(t, filepath.Join(dest2, "x.txt"), "xyz")
}

func TestZipSlipRejected(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	dest := filepath.Join(s.root, "safe")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "nope")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f := writeTemp(t, buf.Bytes())
	if _, _, _, err := s.unpackFile(f, dest, "zip"); err == nil {
		t.Fatal("expected zip slip error")
	}
	if _, err := os.Stat(filepath.Join(s.root, "escape.txt")); err == nil {
		t.Fatal("escaped file was written")
	}
}

func TestTarSlipRejected(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	dest := filepath.Join(s.root, "safe")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("nope")
	hdr := &tar.Header{Name: "../escape.txt", Mode: 0644, Size: int64(len(body))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f := writeTemp(t, buf.Bytes())
	if _, _, _, err := s.unpackFile(f, dest, "tar"); err == nil {
		t.Fatal("expected tar slip error")
	}
}

func TestPackRoundTrip(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	dir := filepath.Join(s.root, "pkg")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "n.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := packArchive(&buf, dir, "pkg", "zip"); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "pkg/sub/n.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing packed file, got %#v", names(zr))
	}
}

func names(zr *zip.Reader) []string {
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTemp(t *testing.T, data []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "arc-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("%s=%q want %q", path, b, want)
	}
}
