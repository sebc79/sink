package main

import (
	"errors"
	"time"

	"sink/peers"
)

var (
	ErrHoldTimeout    = errors.New("hold timed out")
	ErrSenderDead     = errors.New("sending slave is disconnected or stuck")
	ErrSenderWaiting  = errors.New("peer directory is not ready")
	ErrPeerQuery      = errors.New("peer socket query failed")
	ErrStaleMtime     = errors.New("checkout mtime is behind the requested value")
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
