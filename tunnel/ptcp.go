package p2ptunnel

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// parseBody walks the XML like the Rust xml-rs loop: push local names, and for
// each text node record the "/"-joined path -> text. Whitespace-only nodes are
// skipped (the device bodies are single-line, so this matches xml-rs output).
func parseBody(s string) map[string]string {
	dec := xml.NewDecoder(strings.NewReader(s))
	var stack []string
	tree := map[string]string{}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			val := string(t)
			if strings.TrimSpace(val) != "" {
				tree[strings.Join(stack, "/")] = val
			}
		}
	}
	return tree
}

// --- PTCP session (TCP-like over UDP) ---

const (
	bSync = iota
	bCommand
	bPayload
	bBind
	bStatus
	bHeartbeat
	bEmpty
	bFin
)

// body is a PTCP body ready to send: serialized bytes plus the logical length
// used for the sent/recv byte counters.
type body struct {
	data    []byte
	length  uint32
	isSync  bool
	counted bool // non-Sync, non-Empty bodies advance the packet count
}

func syncBody() body {
	return body{data: []byte{0x00, 0x03, 0x01, 0x00}, length: 4, isSync: true}
}

func heartbeatBody() body {
	return body{data: make([]byte, 12), counted: true, length: 12} // 0x13 then zeros
}

func emptyBody() body {
	return body{data: nil, length: 0}
}

func commandBody(b []byte) body {
	return body{data: b, length: uint32(len(b)), counted: true}
}

func bindBody(realm uint32, port uint32) body {
	d := make([]byte, 0, 20)
	d = append(d, 0x11, 0x00, 0x00, 0x00)
	d = binary.BigEndian.AppendUint32(d, realm)
	d = append(d, 0x00, 0x00, 0x00, 0x00)
	d = binary.BigEndian.AppendUint32(d, port)
	d = append(d, 0x7f, 0x00, 0x00, 0x01)
	return body{data: d, length: 20, counted: true}
}

func statusBody(realm uint32, status string) body {
	d := make([]byte, 0, 12+len(status))
	d = append(d, 0x12, 0x00, 0x00, 0x00)
	d = binary.BigEndian.AppendUint32(d, realm)
	d = append(d, 0x00, 0x00, 0x00, 0x00)
	d = append(d, []byte(status)...)
	return body{data: d, length: uint32(12 + len(status)), counted: true}
}

func payloadBody(realm uint32, payload []byte) body {
	header := uint32(0x10000000) | uint32(len(payload))
	d := make([]byte, 0, 12+len(payload))
	d = binary.BigEndian.AppendUint32(d, header)
	d = binary.BigEndian.AppendUint32(d, realm)
	d = binary.BigEndian.AppendUint32(d, 0)
	d = append(d, payload...)
	return body{data: d, length: uint32(12 + len(payload)), counted: true}
}

func heartbeatBodyBytes() body {
	b := heartbeatBody()
	b.data[0] = 0x13
	return b
}

type session struct {
	direct bool // straight to the device (not over the relay)
	mu     sync.Mutex
	sent   uint32 // our seq: bytes we sent
	count  uint32
	rx     rx
	tx     tx
	// stats (for DH_STATS)
	stIn, stDup, stAhead int
	statT                time.Time
	statRecv             uint32
}

// stats returns a one-line summary and resets the counters.
func (s *session) stats() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	kbs := 0.0
	if !s.statT.IsZero() {
		kbs = float64(s.rx.nxt-s.statRecv) / 1024 / time.Since(s.statT).Seconds()
	}
	s.statT, s.statRecv = time.Now(), s.rx.nxt
	held := 0
	for _, o := range s.rx.ooo {
		held += len(o.data)
	}
	out := fmt.Sprintf("%.0f KB/s, ventana %d KB, en orden %d, copias %d, adelantados %d, guardados %d bytes",
		kbs, s.rx.window()/1024, s.stIn, s.stDup, s.stAhead, held)
	s.stIn, s.stDup, s.stAhead = 0, 0, 0
	return out
}

// holeAge: kept for the log: 0 when nothing waits ahead of a hole.
func (s *session) holeAge() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rx.ooo) == 0 {
		return 0
	}
	return time.Since(s.rx.holeSince)
}

// buildSend produces the full PTCP packet for a body and advances our seq.
func (s *session) buildSend(b body) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.packetLocked(b, 0)
}

// packetLocked builds a new packet (s.mu held), keeping it for retransmission
// until acked. flags: flagReset to end the session.
func (s *session) packetLocked(b body, flags byte) []byte {
	seq := s.sent
	pkt := s.packetAt(seq, b, flags)
	s.sent += b.length
	if b.counted {
		s.count++
	}
	if b.length > 0 && !b.isSync {
		s.tx.sent(seq, b, time.Now())
	}
	return pkt
}

// packetAt builds a packet for a body at a given seq, with the current ACK, window
// and clocks (also used to send a segment again).
func (s *session) packetAt(seq uint32, b body, flags byte) []byte {
	ack := s.rx.nxt
	wnd := s.rx.window()
	if b.isSync {
		flags |= flagCtl
		wnd = rcvBuf
	}
	s.rx.lastAckOut = ack
	var ext []byte
	if len(b.data) == 0 && s.rx.nack { // only an immediate pure ACK carries the holes
		ext = s.rx.nackBlock()
	}
	s.rx.nack = false
	hasExt := byte(0)
	if ext != nil {
		hasExt = extNack
	}
	pkt := make([]byte, 0, 24+len(ext)+len(b.data))
	pkt = append(pkt, 'P', 'T', 'C', 'P')
	pkt = binary.BigEndian.AppendUint32(pkt, seq)
	pkt = binary.BigEndian.AppendUint32(pkt, ack)
	pkt = append(pkt, hasExt, flags, byte(wnd>>8), byte(wnd))
	pkt = binary.BigEndian.AppendUint32(pkt, s.rx.clock())
	pkt = binary.BigEndian.AppendUint32(pkt, s.rx.tsRecent)
	pkt = append(pkt, ext...)
	pkt = append(pkt, b.data...)
	return pkt
}

// ptcpPacket is a parsed inbound packet.
type ptcpPacket struct {
	sent, recv, pid, lmid, rmid uint32
	flags                       byte
	nack                        [][2]uint32 // holes the device asks us to send again
	rawBody                     []byte
	kind                        int
	realm                       uint32
	status                      string
	payload                     []byte
}

func parsePTCP(data []byte) (*ptcpPacket, bool) {
	if len(data) < 24 || string(data[0:4]) != "PTCP" {
		return nil, false
	}
	p := &ptcpPacket{
		sent:    binary.BigEndian.Uint32(data[4:8]),
		recv:    binary.BigEndian.Uint32(data[8:12]),
		pid:     binary.BigEndian.Uint32(data[12:16]),
		flags:   data[13],
		lmid:    binary.BigEndian.Uint32(data[16:20]),
		rmid:    binary.BigEndian.Uint32(data[20:24]),
		rawBody: data[24:],
	}
	if data[12] != 0 && len(p.rawBody) > 0 { // extension block (the device's own NACK list)
		if l := int(p.rawBody[0]); 1+l <= len(p.rawBody) {
			p.nack = parseNack(p.rawBody[1 : 1+l])
			p.rawBody = p.rawBody[1+l:]
		}
	}
	b := p.rawBody
	switch {
	case len(b) == 0:
		p.kind = bEmpty
	case p.flags&flagCtl != 0 || b[0] == 0x00:
		p.kind = bSync
	case b[0] == 0x10:
		p.kind = bPayload
		if len(b) >= 12 {
			p.realm = binary.BigEndian.Uint32(b[4:8])
			p.payload = b[12:]
		}
	case b[0] == 0x11:
		p.kind = bBind
	case b[0] == 0x12:
		p.kind = bStatus
		if len(b) >= 12 {
			p.realm = binary.BigEndian.Uint32(b[4:8])
			p.status = string(b[12:])
		}
	case b[0] == 0x13:
		p.kind = bHeartbeat
	default:
		p.kind = bCommand
	}
	return p, true
}

// recvAccount takes a received packet into the byte stream and returns the Tou
// messages that are now complete and in order.
func (s *session) recvAccount(p *ptcpPacket) []touMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.rx.nxt
	held := len(s.rx.ooo)
	added := s.rx.segment(p.sent, p.lmid, p.rawBody, p.kind == bSync)
	switch {
	case added > 0:
		s.stIn++
	case len(p.rawBody) > 0 && len(s.rx.ooo) > held:
		s.stAhead++
	case len(p.rawBody) > 0 && s.rx.nxt == before:
		s.stDup++
	}
	return s.rx.messages()
}

// resends returns the packets to send again now (the device's ACK, its hole list,
// duplicate ACKs, or the retransmission timer). p may be nil (timer only).
func (s *session) resends(p *ptcpPacket) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var segs []txSeg
	if p != nil {
		segs = append(segs, s.tx.acked(p.recv, p.rmid, s.rx.clock(), len(p.rawBody) == 0, now)...)
		if len(p.nack) > 0 {
			segs = append(segs, s.tx.nacked(p.nack, now)...)
		}
	}
	segs = append(segs, s.tx.due(now)...)
	var out [][]byte
	for _, g := range segs {
		out = append(out, s.packetAt(g.seq, g.b, 0))
	}
	return out
}

// progress is how far the in-order stream got (for the watchdog).
func (s *session) progress() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rx.nxt
}

// ackDue: whether an ACK should be sent now (delayed ACK, or something out of order).
func (s *session) ackDue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rx.ackNow()
}

func envDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
