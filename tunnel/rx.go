package p2ptunnel

import (
	"encoding/binary"
	"sort"
	"time"
)

// The device's PTCP is the PseudoTcp of libjingle ("CPhonyTcp" in the old app's
// P2PDll.dll): a byte stream like TCP. A resent segment may be cut differently from
// the first one, so the receiver works on byte ranges, not on whole packets, and
// the "Tou" messages (0x10 data, 0x12 status, ...) are read from the joined stream.
//
// Header (24 bytes, big endian): "PTCP", seq (bytes before this one), ack (bytes we
// got in order), [12] has-extension, [13] flags (0x02 control, 0x04 reset), [14..15]
// receive window, tsval (sender's clock, ms), tsecr (echo of the peer's tsval).

const (
	rcvBuf     = 0xFFFF                // receive buffer of the DLL (and its biggest window)
	ackDelay   = 80 * time.Millisecond // a lone in-order segment is acked after 80 ms
	flagCtl    = 0x02
	flagReset  = 0x04
	extNack    = 0x01
	touHdrSize = 12
)

type oooSeg struct {
	seq  uint32
	data []byte
}

// rx is the receiving half of a session (used by the reader goroutine only, under
// session.mu).
type rx struct {
	nxt        uint32   // next byte we expect (what our ACK says)
	ooo        []oooSeg // segments that came ahead of a missing one, sorted, no overlap
	stream     []byte   // in-order bytes not yet split into Tou messages
	tsRecent   uint32   // the device's tsval to echo
	lastAckOut uint32   // ack value of the last packet we sent
	unacked    int      // in-order segments not acked yet
	ackDue     time.Time
	needAck    bool // an ACK must go out now (something came out of order)
	nack       bool // that ACK carries the list of holes
	holeSince  time.Time
	start      time.Time
}

// clock is our tsval: milliseconds since the session started.
func (r *rx) clock() uint32 {
	if r.start.IsZero() {
		r.start = time.Now()
	}
	return uint32(time.Since(r.start) / time.Millisecond)
}

// segment takes one received segment (seq, data, flags). It returns how many
// in-order bytes it added (0: a copy, or it is held ahead of a hole).
func (r *rx) segment(seq, tsval uint32, data []byte, ctl bool) int {
	n := uint32(len(data))
	// echo the device's clock of the segment that holds our last ACK point (the
	// DLL's rule: seq <= lastack < seq+len, so a pure ACK never changes it)
	if seq <= r.lastAckOut && r.lastAckOut < seq+n {
		r.tsRecent = tsval
	}
	if n == 0 {
		if seq != r.nxt {
			r.needAck = true
		}
		return 0
	}
	// cut what we already have
	if d := int32(r.nxt - seq); d > 0 {
		if uint32(d) >= n {
			r.needAck = true // a pure copy: the device missed our ACK, say it again
			return 0
		}
		data, seq = data[d:], r.nxt
		n = uint32(len(data))
	}
	if seq != r.nxt {
		// ahead of a hole: keep it if it fits in the free space, and say at once
		// (with the list of holes) what is missing
		if int32(seq-r.nxt)+int32(n) <= int32(r.window()) {
			if len(r.ooo) == 0 {
				r.holeSince = time.Now()
			}
			r.hold(seq, data)
		}
		r.needAck = true
		r.nack = len(r.ooo) > 0
		return 0
	}
	added := 0
	if !ctl {
		r.stream = append(r.stream, data...)
	}
	r.nxt += n
	added += int(n)
	// pull the held segments that now follow
	for len(r.ooo) > 0 {
		s := r.ooo[0]
		if int32(s.seq-r.nxt) > 0 {
			break
		}
		r.ooo = r.ooo[1:]
		if d := int32(r.nxt - s.seq); d < int32(len(s.data)) {
			r.stream = append(r.stream, s.data[d:]...)
			added += len(s.data) - int(d)
			r.nxt = s.seq + uint32(len(s.data))
		}
	}
	if merged := added > int(n); merged && len(r.ooo) > 0 {
		r.needAck, r.nack = true, true // a hole filled, more holes behind: tell the device now
	} else if merged {
		r.needAck = true
	}
	r.unacked++
	if r.unacked >= 2 {
		r.needAck = true // every second segment
	} else if r.ackDue.IsZero() {
		r.ackDue = time.Now().Add(ackDelay)
	}
	return added
}

// hold keeps an early segment (merged with the ones already held).
func (r *rx) hold(seq uint32, data []byte) {
	cp := append([]byte(nil), data...)
	r.ooo = append(r.ooo, oooSeg{seq, cp})
	sort.Slice(r.ooo, func(i, j int) bool { return int32(r.ooo[i].seq-r.ooo[j].seq) < 0 })
	// drop segments fully inside the previous one
	out := r.ooo[:1]
	for _, s := range r.ooo[1:] {
		last := &out[len(out)-1]
		end := last.seq + uint32(len(last.data))
		if int32(s.seq+uint32(len(s.data))-end) <= 0 {
			continue
		}
		out = append(out, s)
	}
	r.ooo = out
}

// ackNow tells whether an ACK should go out now (and resets the delayed-ack state
// if so).
func (r *rx) ackNow() bool {
	if r.needAck || (!r.ackDue.IsZero() && time.Now().After(r.ackDue)) {
		r.needAck, r.unacked, r.ackDue = false, 0, time.Time{}
		return true
	}
	return false
}

// window is the free receive space we advertise (we hand bytes on at once, so it
// is the whole buffer minus what waits to be split into messages).
func (r *rx) window() uint32 {
	used := len(r.stream)
	if used > rcvBuf {
		return 0
	}
	return uint32(rcvBuf - used)
}

// touMsg is one message of the stream.
type touMsg struct {
	kind    int
	realm   uint32
	status  string
	payload []byte
}

// messages splits the complete Tou messages off the in-order stream.
func (r *rx) messages() []touMsg {
	var out []touMsg
	for len(r.stream) >= touHdrSize {
		b := r.stream
		size := touHdrSize
		m := touMsg{realm: binary.BigEndian.Uint32(b[4:8])}
		switch b[0] {
		case 0x10: // data: 12 + length (low 24 bits of the first word)
			size += int(binary.BigEndian.Uint32(b[0:4]) & 0x00FFFFFF)
			m.kind = bPayload
		case 0x11: // connect request
			size = 20
			m.kind = bBind
		case 0x12: // status: 4 letters (CONN, DISC, SINV)
			size = 16
			m.kind = bStatus
		case 0x13: // keepalive
			m.kind = bHeartbeat
		case 0x14: // the device ends the channel
			m.kind = bFin
		default:
			m.kind = bCommand
		}
		if len(b) < size {
			break
		}
		switch m.kind {
		case bPayload:
			m.payload = append([]byte(nil), b[touHdrSize:size]...)
		case bStatus:
			m.status = string(b[touHdrSize:size])
		}
		out = append(out, m)
		r.stream = b[size:]
	}
	if len(r.stream) == 0 {
		r.stream = nil // let the old backing array go
	}
	return out
}

// nackBlock builds the extension the DLL sends with an immediate ACK: after the
// 24-byte header, L (bytes that follow), type 0, len (bytes of the value), base =
// next expected byte, then one entry per hole: lo16 = start, hi16 = end (inclusive),
// both relative to base. base and entries are little endian (the DLL writes them
// without byte swapping). Example for base 0x1000 and held [0x1500,0x1900),
// [0x2000,0x2400): 0E 00 0C | 00 10 00 00 | 00 00 FF 04 | 00 09 FF 0F.
func (r *rx) nackBlock() []byte {
	if len(r.ooo) == 0 {
		return nil
	}
	val := binary.LittleEndian.AppendUint32(nil, r.nxt)
	prevEnd := r.nxt
	for _, s := range r.ooo {
		if int32(s.seq-prevEnd) > 0 {
			if 3+len(val)+4 > 0xFF {
				break
			}
			lo, hi := prevEnd-r.nxt, s.seq-r.nxt-1
			val = binary.LittleEndian.AppendUint32(val, lo&0xFFFF|(hi&0xFFFF)<<16)
		}
		if end := s.seq + uint32(len(s.data)); int32(end-prevEnd) > 0 {
			prevEnd = end
		}
	}
	return append([]byte{byte(len(val) + 2), 0, byte(len(val))}, val...)
}
