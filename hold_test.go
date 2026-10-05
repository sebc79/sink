package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sink/peers"
)

func TestDecideHoldServesWhenPresentAndNoMtime(t *testing.T) {
	t.Parallel()
	got := decideHold(true, 100, 0, senderOK, false)
	if got.action != holdServe {
		t.Fatalf("action=%v err=%v", got.action, got.err)
	}
}

func TestDecideHoldServesWhenMtimeCaughtUp(t *testing.T) {
	t.Parallel()
	got := decideHold(true, 200, 200, senderOK, false)
	if got.action != holdServe {
		t.Fatalf("action=%v err=%v", got.action, got.err)
	}
	got = decideHold(true, 201, 200, senderOK, false)
	if got.action != holdServe {
		t.Fatalf("newer disk should serve: %#v", got)
	}
	got = decideHold(true, 200, 200, senderDead, false)
	if got.action != holdServe {
		t.Fatalf("fresh file still serves after sender dies: %#v", got)
	}
}

func TestDecideHoldWaitsWhenMissing(t *testing.T) {
	t.Parallel()
	got := decideHold(false, 0, 0, senderOK, false)
	if got.action != holdWait {
		t.Fatalf("action=%v", got.action)
	}
}

func TestDecideHoldWaitsWhenMtimeBehind(t *testing.T) {
	t.Parallel()
	got := decideHold(true, 100, 200, senderOK, false)
	if got.action != holdWait {
		t.Fatalf("action=%v", got.action)
	}
}

func TestDecideHoldFailsOnDeadSenderAndNeverServesStale(t *testing.T) {
	t.Parallel()
	got := decideHold(true, 100, 200, senderDead, false)
	if got.action != holdFail || !errors.Is(got.err, ErrStaleMtime) {
		t.Fatalf("stale+dead: %#v", got)
	}
	got = decideHold(false, 0, 200, senderDead, false)
	if got.action != holdFail || !errors.Is(got.err, ErrMissingAfterHold) {
		t.Fatalf("missing+dead: %#v", got)
	}
	got = decideHold(true, 100, 0, senderDead, false)
	if got.action != holdServe {
		t.Fatalf("no mtime query may serve existing bytes even if sender is dead: %#v", got)
	}
}

func TestDecideHoldTimeout(t *testing.T) {
	t.Parallel()
	got := decideHold(false, 0, 0, senderOK, true)
	if got.action != holdFail || !errors.Is(got.err, ErrHoldTimeout) {
		t.Fatalf("missing timeout: action=%v err=%v", got.action, got.err)
	}
	got = decideHold(true, 100, 200, senderOK, true)
	if got.action != holdFail || !errors.Is(got.err, ErrStaleMtime) {
		t.Fatalf("stale timeout: %#v", got)
	}
	got = decideHold(false, 0, 0, senderWaiting, true)
	if got.action != holdFail || !errors.Is(got.err, ErrSenderWaiting) {
		t.Fatalf("waiting timeout: %#v", got)
	}
}

func TestClassifySender(t *testing.T) {
	t.Parallel()
	if classifySender(peers.Snapshot{}, "grok-bot-box", errors.New("dial")) != senderUnknown {
		t.Fatal("query error is unknown, not dead")
	}
	if classifySender(peers.Waiting(), "grok-bot-box", nil) != senderWaiting {
		t.Fatal("waiting view")
	}
	snap := peers.Current([]peers.Peer{{
		Name:     "grok-bot-box",
		Presence: peers.Connected,
		Pace:     peers.Busy,
		Fresh:    false,
	}})
	if classifySender(snap, "grok-bot-box", nil) != senderOK {
		t.Fatal("connected busy is still ok to wait")
	}
	snap = peers.Current([]peers.Peer{{
		Name:     "grok-bot-box",
		Presence: peers.Disconnected,
		Pace:     peers.Idle,
	}})
	if classifySender(snap, "grok-bot-box", nil) != senderDead {
		t.Fatal("disconnected is dead")
	}
	snap = peers.Current([]peers.Peer{{
		Name:     "grok-bot-box",
		Presence: peers.Connected,
		Pace:     peers.Stuck,
	}})
	if classifySender(snap, "grok-bot-box", nil) != senderDead {
		t.Fatal("stuck is dead")
	}
	if classifySender(peers.Current(nil), "grok-bot-box", nil) != senderUnknown {
		t.Fatal("named sender missing from directory")
	}
}

func TestAwaitSilentPeerReturnsWithinHold(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "peers.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ln.Close()
		os.Remove(sock)
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				time.Sleep(10 * time.Second)
			}(c)
		}
	}()
	holdFor := 300 * time.Millisecond
	s, err := New(Config{
		TreeDir:     t.TempDir(),
		PeerSocket:  sock,
		HoldTimeout: holdFor,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stop)
	start := time.Now()
	_, err = s.await(context.Background(), "missing.md", "", 0)
	elapsed := time.Since(start)
	if elapsed > holdFor+800*time.Millisecond {
		t.Fatalf("elapsed %s holdFor %s err=%v", elapsed, holdFor, err)
	}
	if !errors.Is(err, ErrHoldTimeout) {
		t.Fatalf("err=%v", err)
	}
}

func TestPollLocateBoundsRebuilds(t *testing.T) {
	s := newTreeServer(t, t.TempDir())
	s.lastIndex = time.Time{}
	s.rebuilds.Store(0)
	const n = 24
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := s.pollLocate("no-such.md", ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := s.rebuilds.Load(); got != 1 {
		t.Fatalf("rebuilds=%d want 1", got)
	}
	if _, err := s.pollLocate("still-missing.md", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.rebuilds.Load(); got != 1 {
		t.Fatalf("second wave rebuilds=%d", got)
	}
}

func TestNewClampsHoldTimeout(t *testing.T) {
	t.Parallel()
	s, err := New(Config{
		TreeDir:     t.TempDir(),
		HoldTimeout: 5 * time.Minute,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stop)
	if s.holdFor != maxHoldTimeout {
		t.Fatalf("holdFor=%s", s.holdFor)
	}
}

func TestTryHoldCapsConcurrency(t *testing.T) {
	t.Parallel()
	s := &Server{holds: make(chan struct{}, 1)}
	if !s.tryHold() {
		t.Fatal("first hold")
	}
	if s.tryHold() {
		t.Fatal("cap should reject")
	}
	s.releaseHold()
	if !s.tryHold() {
		t.Fatal("after release")
	}
}
