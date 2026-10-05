package main

import (
	"errors"
	"testing"

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
	if classifySender(peers.Snapshot{}, "grok-bot-box", errors.New("dial")) != senderDead {
		t.Fatal("query error is dead")
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

