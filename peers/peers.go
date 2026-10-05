package peers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
	"unicode/utf8"
)

const (
	maxFrame     = 1 << 20
	queryBound   = 2 * time.Second
	minPeerBytes = 10
	viewMagic    = "aspv"
	tagWaiting   = 0
	tagCurrent   = 1
	presenceConn = 1
	presenceDisc = 2
	paceIdle     = 1
	paceBusy     = 2
	paceStuck    = 3
)

var (
	ErrClosed   = errors.New("peer socket closed before a full snapshot")
	ErrBadFrame = errors.New("peer socket snapshot was malformed")
)

type Presence int

const (
	Connected    Presence = presenceConn
	Disconnected Presence = presenceDisc
)

type Pace int

const (
	Idle  Pace = paceIdle
	Busy  Pace = paceBusy
	Stuck Pace = paceStuck
)

type Peer struct {
	Name     string
	Presence Presence
	Pace     Pace
	Depth    uint32
	Fresh    bool
}

// Snapshot is one peer-socket view. Waiting means the slave has no directory yet.
// Current with an empty slice means the directory arrived and named nobody else.
type Snapshot struct {
	waiting bool
	peers   []Peer
}

func Waiting() Snapshot {
	return Snapshot{waiting: true}
}

func Current(peers []Peer) Snapshot {
	return Snapshot{peers: peers}
}

func (s Snapshot) Waiting() bool { return s.waiting }

func (s Snapshot) Peers() []Peer {
	if s.waiting {
		return nil
	}
	return s.peers
}

func (s Snapshot) Get(name string) (Peer, bool) {
	if s.waiting {
		return Peer{}, false
	}
	for _, p := range s.peers {
		if p.Name == name {
			return p, true
		}
	}
	return Peer{}, false
}

func Query(socket string) (Snapshot, error) {
	return QueryDeadline(socket, queryBound)
}

func QueryDeadline(socket string, d time.Duration) (Snapshot, error) {
	if d <= 0 {
		d = queryBound
	}
	c, err := net.DialTimeout("unix", socket, d)
	if err != nil {
		return Snapshot{}, err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(d)); err != nil {
		return Snapshot{}, err
	}
	return Decode(c)
}

func Decode(r io.Reader) (Snapshot, error) {
	var lenBuf [4]byte
	if err := readExact(r, lenBuf[:]); err != nil {
		return Snapshot{}, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > maxFrame || n < uint32(len(viewMagic)) {
		return Snapshot{}, ErrBadFrame
	}
	payload := make([]byte, n)
	if err := readExact(r, payload); err != nil {
		return Snapshot{}, err
	}
	if string(payload[:len(viewMagic)]) != viewMagic {
		return Snapshot{}, ErrBadFrame
	}
	return decodeView(payload[len(viewMagic):])
}

func decodeView(body []byte) (Snapshot, error) {
	if len(body) == 0 {
		return Snapshot{}, ErrBadFrame
	}
	tag, rest := body[0], body[1:]
	switch tag {
	case tagWaiting:
		if len(rest) != 0 {
			return Snapshot{}, ErrBadFrame
		}
		return Waiting(), nil
	case tagCurrent:
		if len(rest) < 4 {
			return Snapshot{}, ErrBadFrame
		}
		count := int(binary.BigEndian.Uint32(rest[:4]))
		remain := len(rest) - 4
		if count < 0 || count > remain/minPeerBytes {
			return Snapshot{}, ErrBadFrame
		}
		pos := 4
		peers := make([]Peer, 0, count)
		for i := 0; i < count; i++ {
			p, n, err := decodePeer(rest[pos:])
			if err != nil {
				return Snapshot{}, err
			}
			peers = append(peers, p)
			pos += n
		}
		if pos != len(rest) {
			return Snapshot{}, ErrBadFrame
		}
		return Current(peers), nil
	default:
		return Snapshot{}, ErrBadFrame
	}
}

func decodePeer(buf []byte) (Peer, int, error) {
	if len(buf) < 2 {
		return Peer{}, 0, ErrBadFrame
	}
	nameLen := int(binary.BigEndian.Uint16(buf[:2]))
	end := 2 + nameLen
	if len(buf) < end+7 {
		return Peer{}, 0, ErrBadFrame
	}
	name := string(buf[2:end])
	if !utf8.ValidString(name) || !validSlaveID(name) {
		return Peer{}, 0, ErrBadFrame
	}
	presence, ok := presenceFrom(buf[end])
	if !ok {
		return Peer{}, 0, ErrBadFrame
	}
	pace, ok := paceFrom(buf[end+1])
	if !ok {
		return Peer{}, 0, ErrBadFrame
	}
	depth := binary.BigEndian.Uint32(buf[end+2 : end+6])
	var fresh bool
	switch buf[end+6] {
	case 0:
		fresh = false
	case 1:
		fresh = true
	default:
		return Peer{}, 0, ErrBadFrame
	}
	return Peer{
		Name:     name,
		Presence: presence,
		Pace:     pace,
		Depth:    depth,
		Fresh:    fresh,
	}, end + 7, nil
}

func presenceFrom(tag byte) (Presence, bool) {
	switch tag {
	case presenceConn:
		return Connected, true
	case presenceDisc:
		return Disconnected, true
	default:
		return 0, false
	}
}

func paceFrom(tag byte) (Pace, bool) {
	switch tag {
	case paceIdle:
		return Idle, true
	case paceBusy:
		return Busy, true
	case paceStuck:
		return Stuck, true
	default:
		return 0, false
	}
}

func validSlaveID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func readExact(r io.Reader, buf []byte) error {
	_, err := io.ReadFull(r, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w", ErrClosed)
	}
	return err
}
