package peers

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// Recorded frames from ArborSync encode_snapshot (aspv, u32be length prefix).
var (
	recordedWaiting = mustHex("00000005" + "61737076" + "00")
	recordedCurrent = mustHex(
		"0000001a" +
			"61737076" +
			"01" +
			"00000001" +
			"0008" + "6261636b75702d31" +
			"02" +
			"01" +
			"00000000" +
			"00",
	)
	recordedBusyFresh = mustHex(
		"0000001e" +
			"61737076" +
			"01" +
			"00000001" +
			"000c" + "67726f6b2d626f742d626f78" +
			"01" +
			"02" +
			"00000004" +
			"01",
	)
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestDecodeRecordedWaiting(t *testing.T) {
	t.Parallel()
	got, err := Decode(bytes.NewReader(recordedWaiting))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Waiting() {
		t.Fatalf("got %#v", got)
	}
	if _, ok := got.Get("grok-bot-box"); ok {
		t.Fatal("waiting snapshot must not name peers")
	}
}

func TestDecodeRecordedCurrent(t *testing.T) {
	t.Parallel()
	got, err := Decode(bytes.NewReader(recordedCurrent))
	if err != nil {
		t.Fatal(err)
	}
	if got.Waiting() {
		t.Fatal("expected current")
	}
	p, ok := got.Get("backup-1")
	if !ok {
		t.Fatal("missing backup-1")
	}
	want := Peer{
		Name:     "backup-1",
		Presence: Disconnected,
		Pace:     Idle,
		Depth:    0,
		Fresh:    false,
	}
	if p != want {
		t.Fatalf("got %#v want %#v", p, want)
	}
	if _, ok := got.Get("grok-bot-box"); ok {
		t.Fatal("unexpected name")
	}
}

func TestDecodeRecordedBusyFresh(t *testing.T) {
	t.Parallel()
	got, err := Decode(bytes.NewReader(recordedBusyFresh))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := got.Get("grok-bot-box")
	if !ok {
		t.Fatal("missing grok-bot-box")
	}
	want := Peer{
		Name:     "grok-bot-box",
		Presence: Connected,
		Pace:     Busy,
		Depth:    4,
		Fresh:    true,
	}
	if p != want {
		t.Fatalf("got %#v want %#v", p, want)
	}
}

func TestDecodeEncoderRoundtrip(t *testing.T) {
	t.Parallel()
	want := Current([]Peer{
		{
			Name:     "grok-bot-box",
			Presence: Connected,
			Pace:     Stuck,
			Depth:    12,
			Fresh:    true,
		},
		{
			Name:     "box-pi4-sink",
			Presence: Disconnected,
			Pace:     Idle,
			Depth:    0,
			Fresh:    false,
		},
	})
	frame := encodeSnapshot(t, want)
	got, err := Decode(bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	if got.Waiting() {
		t.Fatal("expected current")
	}
	a, ok := got.Get("grok-bot-box")
	if !ok || a != want.peers[0] {
		t.Fatalf("sender %#v", a)
	}
	b, ok := got.Get("box-pi4-sink")
	if !ok || b != want.peers[1] {
		t.Fatalf("local %#v", b)
	}
}

func TestDecodeRejectsBadFrames(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		{},
		{0, 0, 0, 3, 'a', 's', 'p'},
		append(u32be(5), []byte("asp1\x00")...),
		append(u32be(6), []byte("aspv\x00\x00")...),
		append(u32be(5), []byte("aspv\x02")...),
		truncated(recordedCurrent, 8),
	}
	for i, raw := range cases {
		if _, err := Decode(bytes.NewReader(raw)); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func TestDecodeClosed(t *testing.T) {
	t.Parallel()
	_, err := Decode(bytes.NewReader([]byte{0, 0}))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("closed")) {
		t.Fatalf("err=%v", err)
	}
}

func TestQueryUnixSocket(t *testing.T) {
	t.Parallel()
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
	frame := recordedBusyFresh
	errc := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		_, err = c.Write(frame)
		c.Close()
		errc <- err
	}()
	got, err := Query(sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	p, ok := got.Get("grok-bot-box")
	if !ok || p.Pace != Busy || !p.Fresh {
		t.Fatalf("got %#v", p)
	}
}

func TestValidSlaveID(t *testing.T) {
	t.Parallel()
	if !validSlaveID("grok-bot-box") || !validSlaveID("a.b_c:d-1") {
		t.Fatal("accepted ids rejected")
	}
	if validSlaveID("") || validSlaveID("has space") || validSlaveID("slash/no") {
		t.Fatal("bad ids accepted")
	}
}

func encodeSnapshot(t *testing.T, snap Snapshot) []byte {
	t.Helper()
	var body bytes.Buffer
	body.WriteString(viewMagic)
	if snap.waiting {
		body.WriteByte(tagWaiting)
	} else {
		body.WriteByte(tagCurrent)
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(snap.peers)))
		body.Write(n[:])
		for _, p := range snap.peers {
			name := []byte(p.Name)
			var nl [2]byte
			binary.BigEndian.PutUint16(nl[:], uint16(len(name)))
			body.Write(nl[:])
			body.Write(name)
			body.WriteByte(byte(p.Presence))
			body.WriteByte(byte(p.Pace))
			var d [4]byte
			binary.BigEndian.PutUint32(d[:], p.Depth)
			body.Write(d[:])
			if p.Fresh {
				body.WriteByte(1)
			} else {
				body.WriteByte(0)
			}
		}
	}
	payload := body.Bytes()
	out := append(u32be(uint32(len(payload))), payload...)
	return out
}

func u32be(n uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	return b[:]
}

func truncated(b []byte, n int) []byte {
	if n > len(b) {
		return b
	}
	return append([]byte(nil), b[:n]...)
}

func TestRecordedFramesMatchEncoder(t *testing.T) {
	t.Parallel()
	wait := encodeSnapshot(t, Waiting())
	if !bytes.Equal(wait, recordedWaiting) {
		t.Fatalf("waiting\n got %x\nwant %x", wait, recordedWaiting)
	}
	cur := encodeSnapshot(t, Current([]Peer{{
		Name:     "backup-1",
		Presence: Disconnected,
		Pace:     Idle,
		Depth:    0,
		Fresh:    false,
	}}))
	if !bytes.Equal(cur, recordedCurrent) {
		t.Fatalf("current\n got %x\nwant %x", cur, recordedCurrent)
	}
	busy := encodeSnapshot(t, Current([]Peer{{
		Name:     "grok-bot-box",
		Presence: Connected,
		Pace:     Busy,
		Depth:    4,
		Fresh:    true,
	}}))
	if !bytes.Equal(busy, recordedBusyFresh) {
		t.Fatalf("busy\n got %x\nwant %x", busy, recordedBusyFresh)
	}
}

var _ io.Reader = bytes.NewReader(nil)
