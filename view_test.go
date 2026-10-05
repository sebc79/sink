package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sink/peers"
)

func TestViewTreeBrowseAndResolve(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "knowledge")
	storage := t.TempDir()
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRel(t, tree, "projects/alpha/notes/keep.md", "alpha-keep")
	writeRel(t, tree, "projects/beta/notes/keep.md", "beta-keep")
	writeRel(t, tree, "projects/gamma/unique.md", "unique-body")
	writeRel(t, storage, "only-storage/note.txt", "stored-body")
	s := newTreeServer(t, tree, storage)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != 200 {
		t.Fatalf("browse %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, ">storage<") {
		t.Fatalf("crumb: %s", body)
	}
	if strings.Contains(body, "/browse/projects") {
		t.Fatalf("browse listed tree: %s", body)
	}
	if !strings.Contains(body, "/browse/only-storage") {
		t.Fatalf("storage listing: %s", body)
	}
	if res.Header.Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("referrer %q", res.Header.Get("Referrer-Policy"))
	}

	res, err = http.Get(ts.URL + "/view/projects/gamma/unique.md")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, res)
	if res.StatusCode != 200 || !strings.Contains(body, "unique-body") {
		t.Fatalf("exact %d %s", res.StatusCode, body)
	}

	res, err = http.Get(ts.URL + "/view/only-storage/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, res)
	if res.StatusCode != 200 || !strings.Contains(body, "stored-body") {
		t.Fatalf("storage %d %s", res.StatusCode, body)
	}

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err = noFollow.Get(ts.URL + "/view/unique.md")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("basename status %d", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/view/projects/gamma/unique.md" {
		t.Fatalf("location %q", loc)
	}

	res, err = http.Get(ts.URL + "/view/keep.md")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, res)
	if res.StatusCode != 200 || !strings.Contains(body, "projects/alpha/notes/keep.md") || !strings.Contains(body, "projects/beta/notes/keep.md") {
		t.Fatalf("ambiguous %d %s", res.StatusCode, body)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/view/keep.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Referer", ts.URL+"/view/projects/beta/notes/keep.md")
	res, err = noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/view/projects/beta/notes/keep.md" {
		t.Fatalf("referer %d %q", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestViewHoldFileAppears(t *testing.T) {
	tree := t.TempDir()
	storage := t.TempDir()
	s := newTreeServer(t, tree, storage)
	s.holdFor = 4 * time.Second
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	go func() {
		time.Sleep(250 * time.Millisecond)
		abs := filepath.Join(tree, "projects", "hold-note.md")
		_ = os.MkdirAll(filepath.Dir(abs), 0o755)
		_ = os.WriteFile(abs, []byte("fresh-note-body"), 0o644)
	}()
	client := &http.Client{Timeout: 6 * time.Second}
	res, err := client.Get(ts.URL + "/view/projects/hold-note.md")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != 200 || !strings.Contains(body, "fresh-note-body") {
		t.Fatalf("hold %d %s", res.StatusCode, body)
	}
}

func TestViewHoldRefusesStaleMtime(t *testing.T) {
	tree := t.TempDir()
	storage := t.TempDir()
	writeRel(t, tree, "projects/old.md", "STALE-BYTES-NOT-SERVED")
	path := filepath.Join(tree, "projects", "old.md")
	old := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	s := newTreeServer(t, tree, storage)
	s.holdFor = 400 * time.Millisecond
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	client := &http.Client{Timeout: 2 * time.Second}
	want := strconv.FormatInt(old.Unix()+100, 10)
	res, err := client.Get(ts.URL + "/view/projects/old.md?mtime=" + want)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "STALE-BYTES-NOT-SERVED") {
		t.Fatalf("served stale bytes: %s", body)
	}
	if !strings.Contains(body, "projects/old.md") || !strings.Contains(body, ErrStaleMtime.Error()) {
		t.Fatalf("body %s", body)
	}

	res, err = client.Get(ts.URL + "/api/file/projects/old.md?mtime=" + want)
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, res)
	if res.StatusCode != http.StatusNotFound || strings.Contains(body, "STALE-BYTES-NOT-SERVED") {
		t.Fatalf("api %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "projects/old.md") || !strings.Contains(body, ErrStaleMtime.Error()) {
		t.Fatalf("api body %s", body)
	}
}

func TestViewHoldDeadSenderRefusesStale(t *testing.T) {
	tree := t.TempDir()
	storage := t.TempDir()
	writeRel(t, tree, "projects/old.md", "STALE-BYTES-NOT-SERVED")
	path := filepath.Join(tree, "projects", "old.md")
	old := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	s := newTreeServer(t, tree, storage)
	s.holdFor = 5 * time.Second
	s.peerSocket = "peers.sock"
	s.queryPeers = func(string) (peers.Snapshot, error) {
		return peers.Current([]peers.Peer{{
			Name:     s.senderID,
			Presence: peers.Disconnected,
			Pace:     peers.Idle,
		}}), nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := &http.Client{Timeout: 2 * time.Second}
	want := strconv.FormatInt(old.Unix()+50, 10)
	res, err := client.Get(ts.URL + "/view/projects/old.md?mtime=" + want)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != http.StatusNotFound || strings.Contains(body, "STALE-BYTES-NOT-SERVED") {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, ErrStaleMtime.Error()) {
		t.Fatalf("body %s", body)
	}
}

func TestViewServesFreshMtime(t *testing.T) {
	tree := t.TempDir()
	storage := t.TempDir()
	writeRel(t, tree, "projects/fresh.md", "fresh-bytes")
	path := filepath.Join(tree, "projects", "fresh.md")
	when := time.Unix(1_700_000_100, 0)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	s := newTreeServer(t, tree, storage)
	s.holdFor = 5 * time.Second
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := &http.Client{Timeout: 2 * time.Second}
	q := strconv.FormatInt(when.Unix(), 10)

	res, err := client.Get(ts.URL + "/view/projects/fresh.md?mtime=" + q)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != 200 || !strings.Contains(body, "fresh-bytes") {
		t.Fatalf("view %d %s", res.StatusCode, body)
	}

	res, err = client.Get(ts.URL + "/api/file/projects/fresh.md?mtime=" + q)
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, res)
	if res.StatusCode != 200 || body != "fresh-bytes" {
		t.Fatalf("file %d %q", res.StatusCode, body)
	}
}

func TestViewRejectsMidPathSymlink(t *testing.T) {
	tree := t.TempDir()
	storage := t.TempDir()
	outside := t.TempDir()
	writeRel(t, outside, "nested/secret.md", "OUTSIDE-SECRET")
	if err := os.Symlink(filepath.Join(outside, "nested"), filepath.Join(tree, "linked")); err != nil {
		t.Fatal(err)
	}
	s := newTreeServer(t, tree, storage)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	res, err := http.Get(ts.URL + "/view/linked/secret.md")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "OUTSIDE-SECRET") {
		t.Fatalf("escaped tree: %s", body)
	}
}

func TestViewMtimeDoesNotServeStorage(t *testing.T) {
	tree := t.TempDir()
	storage := t.TempDir()
	writeRel(t, storage, "projects/only-store.md", "STORAGE-BYTES")
	s := newTreeServer(t, tree, storage)
	s.holdFor = 200 * time.Millisecond
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(ts.URL + "/view/projects/only-store.md?mtime=1700000100")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != http.StatusNotFound || strings.Contains(body, "STORAGE-BYTES") {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
}

func TestViewStorageBeatsFuzzyTree(t *testing.T) {
	tree := t.TempDir()
	storage := t.TempDir()
	writeRel(t, tree, "projects/x/keep.md", "tree-keep")
	writeRel(t, storage, "keep.md", "storage-keep")
	s := newTreeServer(t, tree, storage)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	res, err := http.Get(ts.URL + "/view/keep.md")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != 200 || !strings.Contains(body, "storage-keep") {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "tree-keep") {
		t.Fatalf("served fuzzy tree: %s", body)
	}
}

func TestViewHoldBusy(t *testing.T) {
	s := newTreeServer(t, t.TempDir(), t.TempDir())
	for i := 0; i < maxConcurrentHolds; i++ {
		s.holds <- struct{}{}
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	res, err := http.Get(ts.URL + "/view/missing.md")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("view status %d %s", res.StatusCode, body)
	}
	res, err = http.Get(ts.URL + "/api/file/missing.md")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, res)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("api status %d %s", res.StatusCode, body)
	}
}

func TestViewInvalidMtime(t *testing.T) {
	s := newTreeServer(t, t.TempDir(), t.TempDir())
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	res, err := http.Get(ts.URL + "/view/notes.md?mtime=nope")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
