package p2ptunnel

import (
	"encoding/binary"
	"time"
)

// The sending half, like the DLL's PseudoTcp sender: every segment we send stays
// in a queue until the device's ACK covers it, and is sent again when the device
// lists it as a hole (NACK), after 3 duplicate ACKs, or when the retransmission
// timer (RTO, from the measured round trip) runs out.

type txSeg struct {
	seq    uint32
	b      body
	sentAt time.Time
	xmits  int
}

type tx struct {
	queue  []txSeg // sent, not acked yet, in order
	una    uint32  // oldest byte not acked
	srtt   time.Duration
	rttvar time.Duration
	rto    time.Duration
	rtoAt  time.Time // when the oldest segment is due again (zero: nothing in flight)
	dups   int
}

func (t *tx) init() {
	if t.rto == 0 {
		t.rto = time.Second // the DLL's first RTO
	}
}

// sent records a segment that just went out for the first time.
func (t *tx) sent(seq uint32, b body, now time.Time) {
	t.init()
	t.queue = append(t.queue, txSeg{seq: seq, b: b, sentAt: now, xmits: 1})
	if t.rtoAt.IsZero() {
		t.rtoAt = now.Add(t.rto)
	}
}

// acked handles the device's ACK value, its echo of our clock (ms) and whether the
// packet was a pure ACK. It returns the segments to send again now.
func (t *tx) acked(ack, tsecr, nowMs uint32, pure bool, now time.Time) []txSeg {
	t.init()
	if len(t.queue) == 0 {
		t.una, t.dups = ack, 0
		return nil
	}
	if int32(ack-t.una) > 0 { // new data acked
		t.una, t.dups = ack, 0
		for len(t.queue) > 0 && int32(t.queue[0].seq+t.queue[0].b.length-ack) <= 0 {
			t.queue = t.queue[1:]
		}
		// round trip from the echoed clock (the DLL: srtt=(7*srtt+rtt)/8, rttvar=(3*rttvar+|rtt-srtt|)/4,
		// RTO = srtt + max(1 ms, 4*rttvar), kept in 250 ms .. 60 s)
		if tsecr != 0 && int32(nowMs-tsecr) >= 0 {
			rtt := time.Duration(nowMs-tsecr) * time.Millisecond
			if t.srtt == 0 {
				t.srtt, t.rttvar = rtt, rtt/2
			} else {
				d := rtt - t.srtt
				if d < 0 {
					d = -d
				}
				t.rttvar = (3*t.rttvar + d) / 4
				t.srtt = (7*t.srtt + rtt) / 8
			}
			v := 4 * t.rttvar
			if v < time.Millisecond {
				v = time.Millisecond
			}
			t.rto = t.srtt + v
			if t.rto < 250*time.Millisecond {
				t.rto = 250 * time.Millisecond
			}
			if t.rto > 60*time.Second {
				t.rto = 60 * time.Second
			}
		}
		t.rtoAt = time.Time{}
		if len(t.queue) > 0 {
			t.rtoAt = now.Add(t.rto)
		}
		return nil
	}
	if pure && ack == t.una {
		if t.dups++; t.dups == 3 { // fast retransmit of the first segment
			return t.resend([]int{0}, now)
		}
	}
	return nil
}

// nacked: the device listed holes (base + lo/hi pairs); send again every queued
// segment that overlaps one (not more often than once per min(srtt, 200 ms)).
func (t *tx) nacked(holes [][2]uint32, now time.Time) []txSeg {
	gap := 200 * time.Millisecond
	if t.srtt > 0 && t.srtt < gap {
		gap = t.srtt
	}
	var idx []int
	for i, s := range t.queue {
		end := s.seq + s.b.length
		for _, h := range holes {
			if int32(s.seq-(h[1]+1)) < 0 && int32(end-h[0]) > 0 && now.Sub(s.sentAt) >= gap {
				idx = append(idx, i)
				break
			}
		}
	}
	return t.resend(idx, now)
}

// due: the timer ran out: send the first segment again and double the RTO.
func (t *tx) due(now time.Time) []txSeg {
	if t.rtoAt.IsZero() || now.Before(t.rtoAt) || len(t.queue) == 0 {
		return nil
	}
	if t.rto *= 2; t.rto > 60*time.Second {
		t.rto = 60 * time.Second
	}
	t.rtoAt = now.Add(t.rto)
	return t.resend([]int{0}, now)
}

func (t *tx) resend(idx []int, now time.Time) []txSeg {
	var out []txSeg
	for _, i := range idx {
		t.queue[i].sentAt = now
		t.queue[i].xmits++
		out = append(out, t.queue[i])
	}
	return out
}

// parseNack reads the hole list of a device packet's extension (see rx.nackBlock).
func parseNack(ext []byte) [][2]uint32 {
	if len(ext) < 2 || ext[0] != 0 {
		return nil
	}
	n := int(ext[1])
	if len(ext) < 2+n || n < 4 {
		return nil
	}
	v := ext[2 : 2+n]
	base := binary.LittleEndian.Uint32(v[0:4])
	var holes [][2]uint32
	for i := 4; i+4 <= len(v); i += 4 {
		e := binary.LittleEndian.Uint32(v[i:])
		holes = append(holes, [2]uint32{base + e&0xFFFF, base + e>>16})
	}
	return holes
}
