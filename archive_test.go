package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

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
