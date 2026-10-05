package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func newTreeServer(t *testing.T, tree string) *Server {
	t.Helper()
	s, err := New(Config{
		TreeDir: tree,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stop)
	return s
}

func writeRel(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocatePaths(t *testing.T) {
	tree := t.TempDir()
	writeRel(t, tree, "projects/alpha/notes/keep.md", "alpha-keep")
	writeRel(t, tree, "projects/alpha/extra/keep.md", "alpha-extra")
	writeRel(t, tree, "projects/beta/notes/keep.md", "beta-keep")
	writeRel(t, tree, "projects/gamma/unique.md", "unique")
	writeRel(t, tree, "knowledge/plain.md", "plain")
	writeRel(t, tree, "readme.md", "readme")
	writeRel(t, tree, ".git/config", "git")
	writeRel(t, tree, ".arborsync-tmp/scratch.md", "tmp")
	writeRel(t, tree, "nested/.git/hidden.md", "hidden")
	writeRel(t, tree, "nested/.arborsync-tmp/hidden.md", "hidden2")

	outside := t.TempDir()
	writeRel(t, outside, "secret.txt", "secret")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(tree, "escape.md")); err != nil {
		t.Fatal(err)
	}
	writeRel(t, outside, "nested/secret.md", "dir-secret")
	if err := os.Symlink(filepath.Join(outside, "nested"), filepath.Join(tree, "linked")); err != nil {
		t.Fatal(err)
	}

	s := newTreeServer(t, tree)

	for _, f := range s.index.list() {
		if hasIgnoredSegment(f) || f == "escape.md" || len(f) >= 6 && f[:6] == "linked" {
			t.Fatalf("indexed %s", f)
		}
	}

	alphaRef := "http://box/view/projects/alpha/other.md"
	betaRef := "http://box/view/projects/beta/other.md"
	scratchRef := "http://box/view/scratch/foo.md"

	cases := []struct {
		name      string
		in        string
		referer   string
		kind      locateKind
		rel       string
		matches   []string
		truncated bool
		symlink   bool
	}{
		{name: "exact", in: "projects/gamma/unique.md", kind: locateHit, rel: "projects/gamma/unique.md"},
		{name: "strip home", in: "home/box/knowledge/projects/gamma/unique.md", kind: locateHit, rel: "projects/gamma/unique.md"},
		{name: "strip knowledge exact file", in: "knowledge/plain.md", kind: locateHit, rel: "knowledge/plain.md"},
		{name: "strip knowledge prefix", in: "knowledge/projects/gamma/unique.md", kind: locateHit, rel: "projects/gamma/unique.md"},
		{name: "strip bot", in: "bot/knowledge/projects/gamma/unique.md", kind: locateHit, rel: "projects/gamma/unique.md"},
		{name: "basename", in: "unique.md", kind: locateHit, rel: "projects/gamma/unique.md"},
		{name: "trailing", in: "gamma/unique.md", kind: locateHit, rel: "projects/gamma/unique.md"},
		{name: "dir", in: "projects/alpha", kind: locateDir, rel: "projects/alpha"},
		{name: "dotdot", in: "../etc/passwd", kind: locateInvalid},
		{name: "git", in: ".git/config", kind: locateInvalid},
		{name: "tmp", in: "nested/.arborsync-tmp/hidden.md", kind: locateInvalid},
		{name: "symlink", in: "escape.md", symlink: true},
		{name: "mid symlink", in: "linked/secret.md", symlink: true},
		{name: "nested keep", in: "projects/alpha/notes/keep.md", kind: locateHit, rel: "projects/alpha/notes/keep.md"},
		{name: "ambiguous", in: "keep.md", kind: locateAmbiguous, matches: []string{
			"projects/alpha/extra/keep.md",
			"projects/alpha/notes/keep.md",
			"projects/beta/notes/keep.md",
		}},
		{name: "referer one", in: "keep.md", referer: betaRef, kind: locateHit, rel: "projects/beta/notes/keep.md"},
		{name: "referer many", in: "keep.md", referer: alphaRef, kind: locateAmbiguous, matches: []string{
			"projects/alpha/extra/keep.md",
			"projects/alpha/notes/keep.md",
		}},
		{name: "referer miss falls back", in: "keep.md", referer: scratchRef, kind: locateAmbiguous, matches: []string{
			"projects/alpha/extra/keep.md",
			"projects/alpha/notes/keep.md",
			"projects/beta/notes/keep.md",
		}},
		{name: "miss", in: "no/such.md", kind: locateMiss},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := s.locate(tc.in, tc.referer)
			if tc.symlink {
				if err == nil || err != errSymlink {
					t.Fatalf("err=%v loc=%#v", err, loc)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if loc.kind != tc.kind {
				t.Fatalf("kind=%v want %v loc=%#v", loc.kind, tc.kind, loc)
			}
			if tc.rel != "" && loc.rel != tc.rel {
				t.Fatalf("rel=%q want %q", loc.rel, tc.rel)
			}
			if tc.matches != nil && !reflect.DeepEqual(loc.matches, tc.matches) {
				t.Fatalf("matches=%v want %v", loc.matches, tc.matches)
			}
			if loc.truncated != tc.truncated {
				t.Fatalf("truncated=%v", loc.truncated)
			}
			if tc.kind == locateHit {
				if !withinRoot(tree, loc.abs) {
					t.Fatalf("abs %s escaped tree", loc.abs)
				}
			}
			if tc.name == "nested keep" {
				body, err := os.ReadFile(loc.abs)
				if err != nil {
					t.Fatal(err)
				}
				if string(body) != "alpha-keep" {
					t.Fatalf("body %q", body)
				}
			}
		})
	}
}

func TestLocateMatchCap(t *testing.T) {
	tree := t.TempDir()
	for i := 0; i < maxMatchList+1; i++ {
		writeRel(t, tree, filepath.ToSlash(filepath.Join("d"+two(i), "dup.md")), "x")
	}
	s := newTreeServer(t, tree)
	loc, err := s.locate("dup.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if loc.kind != locateAmbiguous || !loc.truncated || len(loc.matches) != maxMatchList {
		t.Fatalf("kind=%v trunc=%v n=%d", loc.kind, loc.truncated, len(loc.matches))
	}
	if loc.matches[0] != "d00/dup.md" {
		t.Fatalf("first %s", loc.matches[0])
	}
	for _, m := range loc.matches {
		if m == "d50/dup.md" {
			t.Fatal("kept match past the cap")
		}
	}
}

func TestNewRequiresTree(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := New(Config{Logger: log})
	if err == nil {
		t.Fatal("missing tree")
	}
	_, err = New(Config{
		TreeDir: filepath.Join(t.TempDir(), "missing"),
		Logger:  log,
	})
	if err == nil {
		t.Fatal("missing tree path")
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = New(Config{TreeDir: file, Logger: log})
	if err == nil {
		t.Fatal("file as tree")
	}
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	_, err = New(Config{TreeDir: link, Logger: log})
	if err == nil {
		t.Fatal("symlink as tree")
	}
}

func TestResolveStaysOnTree(t *testing.T) {
	tree := t.TempDir()
	s := newTreeServer(t, tree)
	abs, clean, err := s.resolve("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if clean != "a.txt" {
		t.Fatalf("clean %s", clean)
	}
	if !withinRoot(tree, abs) {
		t.Fatalf("abs %s", abs)
	}
	if _, _, err := s.resolve(".git/config"); err != ErrPathInvalid {
		t.Fatalf("ignored path err=%v", err)
	}
}

func two(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
