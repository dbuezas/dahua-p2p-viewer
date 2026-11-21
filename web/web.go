// Package web holds what the phone sees: the remote-control page (with the
// panorama calibration, calib.js, which runs on the phone) and a small WebSocket
// server. The TV app serves it to the phone's browser; the Android app serves the
// calibration part to its own WebView.
package web

import (
	"bufio"
	"crypto/sha1"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

//go:embed remote.html
var Page []byte

//go:embed calib.js
var CalibJS []byte

// IAJS runs Real-ESRGAN (realesr-general-x4v3, BSD-3, github.com/xinntao/Real-ESRGAN) on the phone's
// graphics card; IAModel is its weights as float16, in layer order (see ia.js).
//
//go:embed ia.js
var IAJS []byte

//go:embed ia.bin
var IAModel []byte

// Conn is one WebSocket client.
type Conn struct {
	mu    sync.Mutex
	c     io.WriteCloser
	alive bool
}

// Send writes one message (op 1 text, 2 binary).
func (k *Conn) Send(op byte, data []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	head := []byte{0x80 | op}
	switch n := len(data); {
	case n < 126:
		head = append(head, byte(n))
	case n < 65536:
		head = append(head, 126, byte(n>>8), byte(n))
	default:
		head = append(head, 127)
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	if d, ok := k.c.(interface{ SetWriteDeadline(time.Time) error }); ok {
		d.SetWriteDeadline(time.Now().Add(10 * time.Second))
	}
	_, err := k.c.Write(append(head, data...))
	return err
}

// Alive tells whether the client is still connected.
func (k *Conn) Alive() bool { k.mu.Lock(); defer k.mu.Unlock(); return k.alive }

// ServeWS upgrades the request (RFC 6455, by hand: no extra package), runs
// sender in its own goroutine, and calls onText / onBinary for each message
// until the client goes away.
func ServeWS(w http.ResponseWriter, r *http.Request, sender func(*Conn), onText func(string), onBinary func([]byte)) {
	key := r.Header.Get("Sec-WebSocket-Key")
	hj, ok := w.(http.Hijacker)
	if key == "" || !ok {
		http.Error(w, "websocket", http.StatusBadRequest)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(h[:]))
	rw.Flush()
	k := &Conn{c: conn, alive: true}
	stop := func() { k.mu.Lock(); k.alive = false; k.mu.Unlock() }
	if sender != nil {
		go func() { sender(k); stop(); conn.Close() }()
	}
	br := bufio.NewReader(rw)
	var parts []byte
	var partOp byte
	for k.Alive() {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		var hd [2]byte
		if _, err := io.ReadFull(br, hd[:]); err != nil {
			break
		}
		op, fin := hd[0]&0x0f, hd[0]&0x80 != 0
		n := uint64(hd[1] & 0x7f)
		if n == 126 {
			var b [2]byte
			io.ReadFull(br, b[:])
			n = uint64(binary.BigEndian.Uint16(b[:]))
		} else if n == 127 {
			var b [8]byte
			io.ReadFull(br, b[:])
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > 16<<20 {
			break
		}
		var mask [4]byte
		if hd[1]&0x80 != 0 {
			io.ReadFull(br, mask[:])
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(br, data); err != nil {
			break
		}
		if hd[1]&0x80 != 0 {
			for i := range data {
				data[i] ^= mask[i%4]
			}
		}
		switch op {
		case 8: // close
			stop()
			continue
		case 9: // ping -> pong
			k.Send(10, data)
			continue
		}
		if op != 0 {
			partOp, parts = op, parts[:0]
		}
		parts = append(parts, data...)
		if !fin {
			continue
		}
		switch partOp {
		case 1:
			if onText != nil {
				onText(string(parts))
			}
		case 2:
			if onBinary != nil {
				onBinary(append([]byte(nil), parts...))
			}
		}
	}
	stop()
}
