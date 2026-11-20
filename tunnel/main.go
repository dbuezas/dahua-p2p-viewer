package p2ptunnel

import (
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var debugEnabled = os.Getenv("DH_DEBUG") != ""

func debugf(format string, a ...any) {
	if debugEnabled {
		fmt.Printf("[%s] "+format, append([]any{time.Now().Format("15:04:05.000")}, a...)...)
	}
}

// Tunnel is an open P2P session that can forward several local ports.
type Tunnel struct {
	serial    string
	mu        sync.Mutex
	t         *tunnel
	listeners []net.Listener
	closed    bool
}

func newInner(serial string) (*tunnel, error) {
	var sock *udp
	var sess *session
	var err error
	for attempt := 1; attempt <= 5 && (err != nil || sock == nil); attempt++ {
		// each cloud in order (the device is registered on one only); the one that
		// works moves to the front for the next time
		for ci, c := range clouds {
			// a cloud name has many addresses (12 for easy4ipcloud), and not every
			// one answers for every device: try them in turn; the one that works
			// is tried first next time
			for _, addr := range cloudAddrs(c.Server) {
				cc := c
				cc.Server = addr
				useCloud(cc)
				sock, sess, err = p2pHandshakeRelay(serial)
				if err == nil {
					goodAddr[c.Server] = addr
					break
				}
				debugf("Handshake attempt %d on %s (%s) failed: %v\n", attempt, c.Server, addr, err)
			}
			if err == nil {
				if ci > 0 {
					clouds[0], clouds[ci] = clouds[ci], clouds[0]
				}
				break
			}
		}
		if err != nil {
			time.Sleep(3 * time.Second)
		}
	}
	if err != nil {
		return nil, err
	}
	t := &tunnel{
		sock:     sock,
		sess:     sess,
		events:   make(chan event, 256),
		stop:     make(chan struct{}),
		lastRx:   time.Now().UnixNano(),
		channels: map[uint32]chan []byte{},
		conns:    map[uint32]chan bool{},
	}
	go t.heartbeatLoop()
	go t.writerLoop()
	go t.readerLoop()
	return t, nil
}

func (tn *Tunnel) cur() *tunnel {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return tn.t
}

// watchdog re-handshakes if the device goes silent, so a relay drop doesn't kill
// the app. The local listeners stay up; new connections use the fresh session.
func (tn *Tunnel) watchdog() {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for range tk.C {
		tn.mu.Lock()
		if tn.closed {
			tn.mu.Unlock()
			return
		}
		t := tn.t
		tn.mu.Unlock()
		silent := time.Since(time.Unix(0, atomic.LoadInt64(&t.lastRx)))
		// one rule: the video must move on. No in-order data for 5 s (silence, a
		// hole never refilled, only copies, a CONN never answered) = new session.
		if silent < 5*time.Second {
			continue
		}
		fmt.Printf("tunel: sesion trabada (%v sin avanzar, hueco %v): abro otra\n",
			silent.Round(100*time.Millisecond), t.sess.holeAge().Round(100*time.Millisecond))
		// end the old session first, like the DLL: the NVR keeps one direct
		// session per client, so the new one can only go direct once it is gone
		t.goodbye()
		t.shutdown()
		nt, err := newInner(tn.serial)
		// right after a goodbye the NVR may still hold the old session for a moment,
		// so the new one cannot go direct; over the relay it often does not open
		// connections at all. Give the direct path two more tries before the relay.
		for try := 0; try < 2 && err == nil && !nt.sess.direct; try++ {
			nt.goodbye()
			nt.shutdown()
			time.Sleep(2 * time.Second)
			nt, err = newInner(tn.serial)
		}
		if err != nil {
			debugf("reconnect failed: %v\n", err)
			continue
		}
		tn.mu.Lock()
		tn.t = nt
		closed := tn.closed
		tn.mu.Unlock()
		if closed {
			nt.shutdown()
			return
		}
	}
}

// Open connects to the NVR by serial over the Dahua relay and establishes the P2P
// session. It forwards no ports yet; call Forward for each one. Relay mode is the
// only path this NVR supports. Call Close to stop.
func Open(serial string) (*Tunnel, error) {
	rand.Seed(time.Now().UnixNano())
	t, err := newInner(serial)
	if err != nil {
		return nil, err
	}
	tn := &Tunnel{serial: serial, t: t}
	go tn.watchdog()
	return tn, nil
}

// Forward listens on bindAddr:bindPort and tunnels each connection to the device's
// remotePort, all over the one P2P session.
// Direct says whether the current session goes straight to the device.
func (tn *Tunnel) Direct() bool {
	t := tn.cur()
	return t != nil && t.sess.direct
}

func (tn *Tunnel) Forward(bindAddr string, bindPort, remotePort int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindAddr, bindPort))
	if err != nil {
		return err
	}
	tn.mu.Lock()
	tn.listeners = append(tn.listeners, listener)
	tn.mu.Unlock()
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			debugf("Accepted connection from %s -> device port %d\n", client.RemoteAddr(), remotePort)
			tn.cur().handleClient(client, uint32(remotePort))
		}
	}()
	return nil
}

// Close stops all listeners (in-flight streams end on their own).
func (tn *Tunnel) Close() error {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	tn.closed = true
	for _, l := range tn.listeners {
		l.Close()
	}
	if t := tn.t; t != nil {
		// tell the device every connection is over (DISC), so it does not keep
		// a dead session around and slow down the next one
		t.goodbye()
		t.shutdown()
	}
	return nil
}

// ParsePort splits "[bind:]port:remote_port".
func ParsePort(spec string) (string, int, int) { return parsePort(spec) }

func parsePort(spec string) (string, int, int) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 2:
		bp, _ := strconv.Atoi(parts[0])
		rp, _ := strconv.Atoi(parts[1])
		return "127.0.0.1", bp, rp
	case 3:
		bp, _ := strconv.Atoi(parts[1])
		rp, _ := strconv.Atoi(parts[2])
		return parts[0], bp, rp
	default:
		panic("Invalid port specification")
	}
}

// p2pHandshakeRelay reproduces the Rust relay path in dh.rs. It returns the
// agent-connected socket and an established PTCP session.
func p2pHandshakeRelay(serial string) (*udp, *session, error) {
	var cseq uint32

	sock, err := newUDP()
	if err != nil {
		return nil, nil, err
	}

	// socket -> main server
	sock.setRemote(mainServer)

	if err := sock.dhRequest("/probe/p2psrv", "", &cseq); err != nil {
		return nil, nil, err
	}
	if _, err := sock.dhRead(); err != nil {
		return nil, nil, err
	}

	if err := sock.dhRequest("/online/p2psrv/"+serial, "", &cseq); err != nil {
		return nil, nil, err
	}
	res, err := sock.dhRead()
	if err != nil {
		return nil, nil, err
	}
	p2psrv := res.body["body/US"]

	if err := sock.dhRequest("/online/relay", "", &cseq); err != nil {
		return nil, nil, err
	}
	res, err = sock.dhRead()
	if err != nil {
		return nil, nil, err
	}
	relay := res.body["body/Address"]

	// socket2 -> p2psrv
	sock2, err := newUDP()
	if err != nil {
		return nil, nil, err
	}
	sock2.setRemote(p2psrv)
	if err := sock2.dhRequest("/probe/device/"+serial, "", &cseq); err != nil {
		return nil, nil, err
	}
	if _, err := sock2.dhRead(); err != nil {
		return nil, nil, err
	}

	// p2p-channel on socket (main server)
	cid := make([]byte, 8)
	rand.Read(cid)
	cidStr := make([]string, 8)
	for i, b := range cid {
		cidStr[i] = fmt.Sprintf("%x", b)
	}
	sock.setRemote(mainServer)
	p2pBody := fmt.Sprintf(
		"<body><Identify>%s</Identify><IpEncrpt>true</IpEncrpt><LocalAddr>127.0.0.1:%d</LocalAddr><version>5.0.0</version></body>",
		strings.Join(cidStr, " "), sock.localPort())
	if err := sock.dhRequest("/device/"+serial+"/p2p-channel", p2pBody, &cseq); err != nil {
		return nil, nil, err
	}

	// socket2 -> relay
	sock2.setRemote(relay)
	if err := sock2.dhRequest("/relay/agent", "", &cseq); err != nil {
		return nil, nil, err
	}
	res, err = sock2.dhRead()
	if err != nil {
		return nil, nil, err
	}
	token := res.body["body/Token"]
	agent := res.body["body/Agent"]

	// socket2 -> agent
	sock2.setRemote(agent)
	if err := sock2.dhRequest("/relay/start/"+token, "<body><Client>:0</Client></body>", &cseq); err != nil {
		return nil, nil, err
	}
	if _, err := sock2.dhRead(); err != nil {
		return nil, nil, err
	}

	// read p2p-channel response on socket (may be preceded by a 100)
	res, err = sock.dhReadRaw()
	if err != nil {
		return nil, nil, err
	}
	if res.code == 100 {
		res, err = sock.dhReadRaw()
		if err != nil {
			return nil, nil, err
		}
	}
	if res.code >= 400 {
		if res.code == 403 {
			fmt.Println("Device requires authentication when creating P2P channel.")
			fmt.Println("Authentication is not supported at this time.")
		}
		return nil, nil, fmt.Errorf("error response: %d %s", res.code, res.status)
	}
	devLaddr, devPub := res.body["body/LocalAddr"], res.body["body/PubAddr"]
	debugf("device addresses: local=%s public=%s\n", devLaddr, devPub)

	// First a direct path, like the old DLL app: the device already punches towards
	// this socket's public address; answer it and punch back. Round trip ~50 ms
	// instead of ~200 ms over the relay (the speed is window / round trip).
	if os.Getenv("DH_RELAY") == "" && devPub != "" {
		var devID []byte
		for _, h := range strings.Fields(res.body["body/Identify"]) {
			if v, e := strconv.ParseUint(h, 16, 8); e == nil {
				devID = append(devID, byte(v))
			}
		}
		dsess, derr := directPathDLL(sock, cid, devID, devPub, devLaddr)
		if derr == nil {
			fmt.Println("P2P: conexion directa")
			return sock, dsess, nil
		}
		fmt.Println("P2P: directa fallo (" + derr.Error() + "), uso relay")
	}

	// relay-channel on socket2 (main server), response read from the agent
	sock2.setRemote(mainServer)
	relayBody := fmt.Sprintf("<body><agentAddr>%s</agentAddr></body>", agent)
	if err := sock2.dhRequest("/device/"+serial+"/relay-channel", relayBody, &cseq); err != nil {
		return nil, nil, err
	}
	sock2.setRemote(agent)
	if _, err := sock2.dhReadRaw(); err != nil {
		return nil, nil, err
	}

	// PTCP handshake over the agent
	sess := &session{}
	if err := sock2.send(sess.buildSend(syncBody())); err != nil {
		return nil, nil, err
	}
	buf := make([]byte, 65536)
	n, err := sock2.recv(buf, 15*time.Second)
	if err != nil {
		return nil, nil, err
	}
	pkt, ok := parsePTCP(buf[:n])
	if !ok {
		return nil, nil, fmt.Errorf("invalid PTCP sync response")
	}
	sess.recvAccount(pkt)
	debugf("<<< sync reply: sent=%d recv=%d lmid=0x%08x\n", pkt.sent, pkt.recv, pkt.lmid)

	// Try a direct (hole-punched) path to the device: much shorter round trip and
	// no relay bandwidth cap. If anything fails, keep using the relay session.
	if os.Getenv("DH_DIRECT") != "" && devPub != "" {
		dsock, dsess, derr := directPath(sock, sock2, sess, cid, devPub, devLaddr)
		if derr == nil {
			fmt.Println("P2P: conexion directa")
			return dsock, dsess, nil
		}
		fmt.Println("P2P: directa fallo (" + derr.Error() + "), uso relay")
	}
	return sock2, sess, nil
}

// readPTCP reads the next PTCP packet on u (skipping anything else) and accounts it.
func readPTCP(u *udp, sess *session, timeout time.Duration) (*ptcpPacket, error) {
	buf := make([]byte, 65536)
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, fmt.Errorf("timeout")
		}
		n, err := u.recv(buf, left)
		if err != nil {
			return nil, err
		}
		pkt, ok := parsePTCP(append([]byte(nil), buf[:n]...))
		if !ok {
			continue
		}
		logPkt("<<<H", buf[:n])
		sess.recvAccount(pkt)
		if len(pkt.rawBody) > 0 { // ack, or the device keeps resending
			u.send(sess.buildSend(emptyBody()))
		}
		return pkt, nil
	}
}

// readAnswer skips empties and heartbeats (the device sends keep-alives) and
// returns the first real answer.
func readAnswer(u *udp, sess *session, timeout time.Duration) (*ptcpPacket, error) {
	deadline := time.Now().Add(timeout)
	for {
		p, err := readPTCP(u, sess, time.Until(deadline))
		if err != nil {
			return nil, err
		}
		if p.kind != bEmpty && p.kind != bHeartbeat {
			return p, nil
		}
	}
}

// addrBytes encodes "a.b.c.d:port" as port(2, big endian) + ip(4), every byte inverted.
func addrBytes(addr string) ([]byte, error) {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil || ua.IP.To4() == nil {
		return nil, fmt.Errorf("direccion invalida %q", addr)
	}
	b := []byte{byte(ua.Port >> 8), byte(ua.Port)}
	b = append(b, ua.IP.To4()...)
	for i := range b {
		b[i] = ^b[i]
	}
	return b, nil
}

// stunLike builds the hole-punch packets the DLL sends: STUN with every byte
// inverted. resp=false: binding request (0x0001), true: success (0x0101). trans is
// the 12-byte transaction id; tie the ICE tie-breaker (the inverted Identify);
// addr the inverted address bytes (port, IPv4).
func stunLike(resp bool, trans, tie, addr []byte) []byte {
	var p []byte
	if resp {
		p = append(p, 0xfe, 0xfe, 0xff, 0xe7, 0xde, 0xed, 0x5b, 0xbd)
	} else {
		p = append(p, 0xff, 0xfe, 0xff, 0xe7, 0xde, 0xed, 0x5b, 0xbd)
	}
	p = append(p, trans...)
	if resp {
		p = append(p, 0x7f, 0xd6, 0xff, 0xf7) // ICE-CONTROLLED
	} else {
		p = append(p, 0x7f, 0xd5, 0xff, 0xf7) // ICE-CONTROLLING
	}
	p = append(p, tie...)
	p = append(p, 0xff, 0xfb, 0xff, 0xf7, 0xff, 0xfe)
	return append(p, addr...)
}

// directPathDLL opens the direct path the way the old DLL app does (seen in a
// capture): binding requests to the device's public address and a few ports
// next to it, success answers to the device's own requests, then a new PTCP
// session on the port that answered. No sign, no relay.
func directPathDLL(sock *udp, cid, devID []byte, devPub, devLaddr string) (*session, error) {
	pub, err := net.ResolveUDPAddr("udp4", devPub)
	if err != nil {
		return nil, err
	}
	tie := make([]byte, len(cid))
	for i, b := range cid {
		tie[i] = ^b
	}
	lB, err := addrBytes(devLaddr)
	if err != nil {
		lB, _ = addrBytes(devPub)
	}
	var targets []*net.UDPAddr
	for _, d := range []int{0, 1, 2, 7, 9} {
		if pt := pub.Port + d; pt < 65536 {
			targets = append(targets, &net.UDPAddr{IP: pub.IP, Port: pt})
		}
	}
	buf := make([]byte, 65536)
	var peer *net.UDPAddr
	gotResp := false
	deadline := time.Now().Add(4 * time.Second)
	nextSend := time.Now()
	for time.Now().Before(deadline) && !(gotResp && peer != nil) {
		if time.Now().After(nextSend) {
			for _, t := range targets {
				trans := make([]byte, 12)
				rand.Read(trans)
				ab, _ := addrBytes(t.String())
				sock.conn.WriteToUDP(stunLike(false, trans, tie, ab), t)
			}
			nextSend = time.Now().Add(300 * time.Millisecond)
		}
		sock.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, from, err := sock.conn.ReadFromUDP(buf)
		if err != nil || !from.IP.Equal(pub.IP) || n < 20 {
			continue
		}
		switch {
		case buf[0] == 0xff && buf[1] == 0xfe: // the device asks: answer it
			sock.conn.WriteToUDP(stunLike(true, buf[8:20], tie, lB), from)
			peer = from
			debugf("direct: request from device %v\n", from)
		case buf[0] == 0xfe && buf[1] == 0xfe: // it answered ours
			gotResp = true
			peer = from
			debugf("direct: answer from device %v\n", from)
		}
	}
	sock.conn.SetReadDeadline(time.Time{})
	if peer == nil {
		return nil, fmt.Errorf("el NVR no contesto directo")
	}
	sock.remote = peer
	sess := &session{direct: true}
	for try := 0; try < 3; try++ {
		if err := sock.send(sess.buildSend(syncBody())); err != nil {
			return nil, err
		}
		for {
			p, err := readPTCP(sock, sess, 2*time.Second)
			if err != nil {
				break
			}
			if p.kind == bSync {
				debugf("direct: PTCP sync ok\n")
				return sess, nil
			}
		}
	}
	return nil, fmt.Errorf("sin sync directo")
}

// directPath ports the Rust reference's direct mode: ask the device (over the
// relay) for a sign, punch a UDP hole to its public address, then open a new PTCP
// session there and authenticate it with the sign.
func directPath(sock, relay *udp, rsess *session, cid []byte, devPub, devLaddr string) (*udp, *session, error) {
	// 1. the sign, over the relay session
	cmd := append([]byte{0x17}, make([]byte, 11)...)
	signReq := rsess.buildSend(commandBody(cmd))
	logPkt(">>>H", signReq)
	if err := relay.send(signReq); err != nil {
		return nil, nil, err
	}
	ans, err := readAnswer(relay, rsess, 5*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("sin firma: %v", err)
	}
	if ans.kind != bCommand || len(ans.rawBody) < 12 {
		return nil, nil, fmt.Errorf("respuesta de firma rara")
	}
	sign := append([]byte(nil), ans.rawBody[12:]...)

	// 2. hole punch towards the device's public address
	ncid := make([]byte, len(cid))
	for i, b := range cid {
		ncid[i] = ^b
	}
	pubB, err := addrBytes(devPub)
	if err != nil {
		return nil, nil, err
	}
	sock.setRemote(devPub)
	cookie := make([]byte, 4)
	trans := make([]byte, 12)
	rand.Read(cookie)
	rand.Read(trans)
	var p1 []byte
	p1 = append(p1, 0xff, 0xfe, 0xff, 0xe7)
	p1 = append(p1, cookie...)
	p1 = append(p1, trans...)
	p1 = append(p1, 0x7f, 0xd5, 0xff, 0xf7)
	p1 = append(p1, ncid...)
	p1 = append(p1, 0xff, 0xfb, 0xff, 0xf7, 0xff, 0xfe)
	p1 = append(p1, pubB...)
	buf := make([]byte, 65536)
	var n int
	for try := 0; try < 5; try++ { // UDP: resend a few times while the hole opens
		if err := sock.send(p1); err != nil {
			return nil, nil, err
		}
		n, err = sock.recv(buf, time.Second)
		if err == nil && n >= 20 {
			break
		}
	}
	if err != nil || n < 20 {
		return nil, nil, fmt.Errorf("el NVR no contesto directo")
	}
	rtrans := append([]byte(nil), buf[8:20]...)
	lB, err := addrBytes(devLaddr)
	if err != nil {
		lB = pubB
	}
	var p2 []byte
	p2 = append(p2, 0xfe, 0xfe, 0xff, 0xe7)
	p2 = append(p2, cookie...)
	p2 = append(p2, rtrans...)
	p2 = append(p2, 0x7f, 0xd6, 0xff, 0xf7)
	p2 = append(p2, ncid...)
	p2 = append(p2, 0xff, 0xfb, 0xff, 0xf7, 0xff, 0xfe)
	p2 = append(p2, lB...)
	if err := sock.send(p2); err != nil {
		return nil, nil, err
	}
	for i := 0; i < 5; i++ { // the device answers a few hole-punch packets; drain them
		if _, err := sock.recv(buf, time.Second); err != nil {
			break
		}
	}

	// 3. new PTCP session on the direct path, authenticated with the sign
	sess := &session{}
	if err := sock.send(sess.buildSend(syncBody())); err != nil {
		return nil, nil, err
	}
	var p *ptcpPacket
	for {
		p, err = readPTCP(sock, sess, 5*time.Second)
		if err != nil {
			return nil, nil, fmt.Errorf("sin sync directo: %v", err)
		}
		if p.kind == bSync {
			break
		}
	}
	auth := append(append([]byte{0x19}, make([]byte, 11)...), sign...)
	if err := sock.send(sess.buildSend(commandBody(auth))); err != nil {
		return nil, nil, err
	}
	ans, err = readAnswer(sock, sess, 5*time.Second)
	if err != nil || len(ans.rawBody) == 0 || ans.rawBody[0] != 0x1A {
		return nil, nil, fmt.Errorf("el NVR no acepto la firma")
	}
	fin := append([]byte{0x1b}, make([]byte, 11)...)
	if err := sock.send(sess.buildSend(commandBody(fin))); err != nil {
		return nil, nil, err
	}
	readPTCP(sock, sess, 3*time.Second) // an empty ack
	return sock, sess, nil
}

// --- tunnel data plane ---

type eventKind int

const (
	evHeartbeat eventKind = iota
	evConnect
	evDisconnect
	evData
)

type event struct {
	kind       eventKind
	realm      uint32
	data       []byte
	remotePort uint32
}

type tunnel struct {
	sock   *udp
	sess   *session
	events chan event
	stop   chan struct{}
	lastRx int64 // unixnano of the last in-order packet from the device (atomic)

	sendMu sync.Mutex // serializes build+send so packets leave in sequence order

	mu       sync.Mutex
	channels map[uint32]chan []byte
	conns    map[uint32]chan bool
}

// sendBody builds a packet for b and sends it atomically, so two goroutines
// (the writer and the reader's ACKs) can never interleave build and send and
// put packets on the wire out of sequence order (which makes the device retransmit).
func (t *tunnel) sendBody(b body) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	pkt := t.sess.buildSend(b)
	logPkt(">>>W", pkt)
	return t.sock.send(pkt)
}

// sendRaw sends ready packets (segments sent again).
func (t *tunnel) sendRaw(pkts [][]byte) {
	if len(pkts) == 0 {
		return
	}
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	for _, p := range pkts {
		logPkt(">>>R", p)
		t.sock.send(p)
	}
}

func (t *tunnel) heartbeatLoop() {
	tk := time.NewTicker(5 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-tk.C:
			select {
			case t.events <- event{kind: evHeartbeat}:
			case <-t.stop:
				return
			}
		}
	}
}

func logPkt(dir string, pkt []byte) {
	if !debugEnabled || len(pkt) < 24 {
		return
	}
	p, _ := parsePTCP(pkt)
	n := len(p.rawBody)
	if n > 12 {
		n = 12
	}
	debugf("%s sent=%d recv=%d pid=0x%08x lmid=0x%08x rmid=0x%08x kind=%d blen=%d head=%x\n",
		dir, p.sent, p.recv, p.pid, p.lmid, p.rmid, p.kind, len(p.rawBody), p.rawBody[:n])
}

func (t *tunnel) writerLoop() {
	for {
		var ev event
		select {
		case <-t.stop:
			return
		case ev = <-t.events:
		}
		var b body
		switch ev.kind {
		case evHeartbeat:
			b = heartbeatBodyBytes()
		case evConnect:
			b = bindBody(ev.realm, ev.remotePort)
		case evDisconnect:
			b = statusBody(ev.realm, "DISC")
		case evData:
			b = payloadBody(ev.realm, ev.data)
		}
		if err := t.sendBody(b); err != nil {
			debugf("writer send error: %v\n", err)
		}
	}
}

func (t *tunnel) readerLoop() {
	buf := make([]byte, 65536)
	recvErrs := 0
	statsOn := os.Getenv("DH_STATS") != ""
	lastStats := time.Now()
	for {
		select {
		case <-t.stop:
			return
		default:
		}
		if statsOn && time.Since(lastStats) > 5*time.Second {
			fmt.Printf("[%s] tunel: %s\n", time.Now().Format("15:04:05"), t.sess.stats())
			lastStats = time.Now()
		}
		// a lone in-order segment is acked after 80 ms (like the DLL's delayed ACK);
		// our own segments are sent again when the timer runs out
		if t.sess.ackDue() {
			t.sendBody(emptyBody())
		}
		t.sendRaw(t.sess.resends(nil))
		n, err := t.sock.recv(buf, 20*time.Millisecond)
		if err != nil {
			// a read timeout is normal; any other error must not spin the CPU
			if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				recvErrs++
				if recvErrs == 1 || recvErrs%1000 == 0 {
					fmt.Printf("tunel: error leyendo UDP (%d veces): %v\n", recvErrs, err)
				}
				time.Sleep(5 * time.Millisecond)
			}
			continue
		}
		pkt, ok := parsePTCP(append([]byte(nil), buf[:n]...))
		if !ok {
			continue
		}
		logPkt("<<<R", buf[:n])
		if pkt.flags&flagReset != 0 {
			fmt.Println("tunel: el NVR cerro la sesion (reset)")
			atomic.StoreInt64(&t.lastRx, 0) // the watchdog opens a new one
			continue
		}
		t.sendRaw(t.sess.resends(pkt)) // the device's ACK / hole list for what we sent
		msgs := t.sess.recvAccount(pkt)
		for _, m := range msgs {
			if m.kind == bPayload {
				// progress = video data, not keepalives or copies: a session where
				// the video does not move on gets replaced
				atomic.StoreInt64(&t.lastRx, time.Now().UnixNano())
				break
			}
		}
		// out of order, a copy, every 2nd segment: ACK now (like the DLL)
		if t.sess.ackDue() {
			if err := t.sendBody(emptyBody()); err != nil {
				debugf("ack send error: %v\n", err)
			}
		}
		for _, m := range msgs {
			t.process(m)
		}
	}
}

// process handles one in-order message from the device.
func (t *tunnel) process(pkt touMsg) {
	switch pkt.kind {
	case bStatus:
		if pkt.status == "DISC" {
			// the device closed this connection (e.g. the NVR's web server drops an
			// idle keep-alive): close our side too, so the next request opens a new one
			t.mu.Lock()
			ch := t.channels[pkt.realm]
			delete(t.channels, pkt.realm)
			t.mu.Unlock()
			if ch != nil {
				func() { defer func() { recover() }(); close(ch) }()
			}
			debugf("device DISC realm %08x\n", pkt.realm)
		}
		if pkt.status == "CONN" {
			t.mu.Lock()
			ch := t.conns[pkt.realm]
			delete(t.conns, pkt.realm)
			t.mu.Unlock()
			if ch != nil {
				select {
				case ch <- true:
				default:
				}
			}
		}
	case bPayload:
		t.mu.Lock()
		ch := t.channels[pkt.realm]
		t.mu.Unlock()
		if ch != nil {
			// A payload for an already-closed realm is dropped, not fatal.
			func() {
				defer func() { recover() }()
				ch <- pkt.payload
			}()
		}
	}
}

// shutdown stops a session and closes every client connection on it.
func (t *tunnel) shutdown() {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	t.mu.Lock()
	for realm, ch := range t.channels {
		delete(t.channels, realm)
		func() { defer func() { recover() }(); close(ch) }()
	}
	t.mu.Unlock()
}

func (t *tunnel) handleClient(client net.Conn, remotePort uint32) {
	realm := rand.Uint32()
	// Big buffer: the shared reader loop must never block delivering to one client
	// (if it blocks it stops ACKing and the device stalls the whole session).
	rx := make(chan []byte, 16384)
	connCh := make(chan bool, 1)

	t.mu.Lock()
	t.channels[realm] = rx
	t.conns[realm] = connCh
	t.mu.Unlock()

	t.events <- event{kind: evConnect, realm: realm, remotePort: remotePort}

	select {
	case <-connCh:
	case <-time.After(8 * time.Second):
		fmt.Println("Timed out waiting for device CONN")
		client.Close()
		t.mu.Lock()
		delete(t.channels, realm)
		delete(t.conns, realm)
		t.mu.Unlock()
		return
	}

	// writer: device -> client (ends when the session is closed: then the client
	// is closed too, so it reconnects through the new session at once)
	go func() {
		defer client.Close()
		total := 0
		for data := range rx {
			n, err := client.Write(data)
			total += n
			if err != nil {
				debugf("client.Write err after %d bytes: %v\n", total, err)
				return
			}
		}
		debugf("client writer exit, total=%d\n", total)
	}()

	// reader: client -> device
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := client.Read(buf)
			if err != nil {
				if err != io.EOF {
					debugf("client read: %v\n", err)
				}
				t.events <- event{kind: evDisconnect, realm: realm}
				t.mu.Lock()
				ch := t.channels[realm]
				delete(t.channels, realm)
				t.mu.Unlock()
				if ch != nil {
					close(ch)
				}
				client.Close()
				return
			}
			debugf("client->device %d bytes\n", n)
			t.events <- event{kind: evData, realm: realm, data: append([]byte(nil), buf[:n]...)}
		}
	}()
}

// goodbye ends a session the way the old DLL app does: DISC for every connection,
// FIN (0x14) for the channel, then a PTCP reset (flags 0x04), which makes the
// device's PseudoTcp drop the session at once.
func (t *tunnel) goodbye() {
	t.mu.Lock()
	var realms []uint32
	for r := range t.channels {
		realms = append(realms, r)
	}
	t.mu.Unlock()
	for _, r := range realms {
		t.sendBody(statusBody(r, "DISC"))
	}
	t.sendBody(commandBody(append([]byte{0x14}, make([]byte, 11)...)))
	t.sendMu.Lock()
	t.sess.mu.Lock()
	rst := t.sess.packetLocked(emptyBody(), flagReset)
	t.sess.mu.Unlock()
	t.sock.send(rst)
	t.sendMu.Unlock()
}

// goodAddr: per cloud name, the address that answered last time.
var goodAddr = map[string]string{}

// cloudAddrs: the addresses of a cloud ("host:port"), the last good one first,
// at most 4 (each one that does not answer costs a few seconds).
func cloudAddrs(server string) []string {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return []string{server}
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return []string{server}
	}
	var out []string
	if g := goodAddr[server]; g != "" {
		out = append(out, g)
	}
	for _, ip := range ips {
		a := net.JoinHostPort(ip, port)
		if len(out) >= 4 {
			break
		}
		if a != goodAddr[server] {
			out = append(out, a)
		}
	}
	return out
}
