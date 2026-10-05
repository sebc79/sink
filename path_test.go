package main

import (
	"path/filepath"
	"testing"
)

func TestCleanRel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr error
	}{
		{"", "", nil},
		{".", "", nil},
		{"docs/notes.txt", "docs/notes.txt", nil},
		{"/docs/notes.txt", "docs/notes.txt", nil},
		{"docs/../notes.txt", "notes.txt", nil},
		{"docs/foo/..", "docs", nil},
		{"..", "", ErrPathEscape},
		{"../etc/passwd", "", ErrPathEscape},
		{"foo/../../etc", "", ErrPathEscape},
		{`C:\windows`, "", ErrPathEscape},
		{"foo\\bar", "foo/bar", nil},
		{"foo//bar", "foo/bar", nil},
		{"a/b/c/", "a/b/c", nil},
	}
	for _, tc := range cases {
		got, err := cleanRel(tc.in)
		if tc.wantErr != nil {
			if err == nil {
				t.Fatalf("cleanRel(%q) err=nil, want %v", tc.in, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("cleanRel(%q) unexpected err %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("cleanRel(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveContainment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := &Server{root: dir}
	abs, clean, err := s.resolve("a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if clean != "a/b.txt" {
		t.Fatalf("clean=%q", clean)
	}
	want := filepath.Join(dir, "a", "b.txt")
	if abs != want {
		t.Fatalf("abs=%q want %q", abs, want)
	}
	if _, _, err := s.resolve("../outside"); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()
	if got := humanSize(500); got != "500 B" {
		t.Fatalf("got %q", got)
	}
	if got := humanSize(2048); got != "2.0 KB" {
		t.Fatalf("got %q", got)
	}
}
