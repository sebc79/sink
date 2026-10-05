package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"sink/peers"
)

var (
	ErrHoldTimeout      = errors.New("hold timed out")
	ErrSenderDead       = errors.New("sending slave is disconnected or stuck")
	ErrSenderWaiting    = errors.New("peer directory is not ready")
	ErrPeerQuery        = errors.New("peer socket query failed")
	ErrStaleMtime       = errors.New("checkout mtime is behind the requested value")
	ErrMissingAfterHold = errors.New("file did not appear")
)

type holdAction int

const (
	holdServe holdAction = iota
	holdWait
	holdFail
)

type holdVerdict struct {
	action holdAction
	err    error
}

func decideHold(present bool, diskMtime, wantMtime int64, sender senderStatus, timedOut bool) holdVerdict {
	if present && (wantMtime == 0 || diskMtime >= wantMtime) {
		return holdVerdict{action: holdServe}
	}
	if sender == senderDead {
		return holdVerdict{action: holdFail, err: failReason(present, wantMtime, diskMtime, ErrSenderDead)}
	}
	if timedOut {
		if sender == senderWaiting {
			return holdVerdict{action: holdFail, err: ErrSenderWaiting}
		}
		if present && wantMtime > 0 {
			return holdVerdict{action: holdFail, err: ErrStaleMtime}
		}
		return holdVerdict{action: holdFail, err: ErrHoldTimeout}
	}
	return holdVerdict{action: holdWait}
}

func failReason(present bool, wantMtime, diskMtime int64, fallback error) error {
	if present && wantMtime > 0 && diskMtime < wantMtime {
		return ErrStaleMtime
	}
	if !present {
		return ErrMissingAfterHold
	}
	return fallback
}

type senderStatus int

const (
	senderUnknown senderStatus = iota
	senderOK
	senderDead
	senderWaiting
)

func classifySender(snap peers.Snapshot, id string, queryErr error) senderStatus {
	if queryErr != nil {
		return senderDead
	}
	if snap.Waiting() {
		return senderWaiting
	}
	p, ok := snap.Get(id)
	if !ok {
		return senderUnknown
	}
	if p.Presence == peers.Disconnected || p.Pace == peers.Stuck {
		return senderDead
	}
	return senderOK
}

func unixMtime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

const holdPoll = 200 * time.Millisecond

func parseMtimeQuery(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid mtime")
	}
	return n, nil
}

func holdFailError(suffix string, err error) error {
	if suffix == "" || err == nil {
		return err
	}
	return fmt.Errorf("%s: %w", suffix, err)
}

func (s *Server) await(ctx context.Context, suffix, referer string, want int64) (located, error) {
	deadline := time.Now().Add(s.holdFor)
	var last located
	for {
		loc, err := s.pollLocate(suffix, referer)
		if err != nil {
			return loc, err
		}
		last = loc
		switch loc.kind {
		case locateAmbiguous, locateDir, locateInvalid:
			return loc, nil
		}
		present, disk := holdPresence(loc, want)
		timedOut := !time.Now().Before(deadline)
		verdict := decideHold(present, disk, want, s.currentSender(), timedOut)
		switch verdict.action {
		case holdServe:
			loc, err = s.locate(suffix, referer)
			if err != nil {
				return loc, err
			}
			if readyLocated(loc, want) {
				return loc, nil
			}
		case holdFail:
			if loc.suffix == "" {
				loc.suffix = suffix
			}
			return loc, verdict.err
		}
		wait := holdPoll
		if remain := time.Until(deadline); remain < wait {
			if remain < 0 {
				remain = 0
			}
			wait = remain
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Server) pollLocate(suffix, referer string) (located, error) {
	loc, err := s.locate(suffix, referer)
	if err != nil || loc.kind != locateMiss || s.tree == "" {
		return loc, err
	}
	if err := s.rebuildIndex(); err != nil && s.log != nil {
		s.log.Info("index", "err", err)
	}
	return s.locate(suffix, referer)
}

func holdPresence(loc located, want int64) (bool, int64) {
	if loc.kind != locateHit {
		return false, 0
	}
	// A storage copy is not the checkout an mtime query is waiting on.
	if !loc.fromTree && want > 0 {
		return false, 0
	}
	st, err := os.Lstat(loc.abs)
	if err != nil || !st.Mode().IsRegular() {
		return false, 0
	}
	return true, unixMtime(st.ModTime())
}

func readyLocated(loc located, want int64) bool {
	present, disk := holdPresence(loc, want)
	return present && (want == 0 || disk >= want)
}

func (s *Server) currentSender() senderStatus {
	if s.peerSocket == "" || s.queryPeers == nil {
		return senderUnknown
	}
	snap, err := s.queryPeers(s.peerSocket)
	return classifySender(snap, s.senderID, err)
}
