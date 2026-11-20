package main

import (
	"bufio"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
)

// The RTSP auth relay: ffmpeg connects to it with no user or password in its
// command line (where any local user could read them, e.g. with ps), and the
// relay answers the NVR's login challenge (Digest, or Basic) on the way. The video
// itself ($-framed interleaved data) passes through untouched.

// startRTSPRelay listens on a free local port and relays every connection to
// upstream (the tunnel's RTSP port); it returns "127.0.0.1:port".
func startRTSPRelay(upstream string, creds func() (user, pass string)) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, p := creds()
			go relayRTSP(c, upstream, u, p)
		}
	}()
	return ln.Addr().String(), nil
}

// rtspMsg is one RTSP request or response: the start line, the header lines and the body.
type rtspMsg struct {
	start   string
	headers []string
	body    []byte
}

func (m *rtspMsg) header(name string) string {
	for _, h := range m.headers {
		if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (m *rtspMsg) bytes() []byte {
	var b strings.Builder
	b.WriteString(m.start + "\r\n")
	for _, h := range m.headers {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\n")
	return append([]byte(b.String()), m.body...)
}

// readRTSP reads either one $-framed interleaved packet (returned raw) or one RTSP message.
func readRTSP(r *bufio.Reader) (raw []byte, m *rtspMsg, err error) {
	first, err := r.Peek(1)
	if err != nil {
		return nil, nil, err
	}
	if first[0] == '$' {
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return nil, nil, err
		}
		n := int(hdr[2])<<8 | int(hdr[3])
		buf := make([]byte, 4+n)
		copy(buf, hdr)
		_, err := io.ReadFull(r, buf[4:])
		return buf, nil, err
	}
	m = &rtspMsg{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if m.start == "" {
			m.start = line
			continue
		}
		if line == "" {
			break
		}
		m.headers = append(m.headers, line)
	}
	if n, _ := strconv.Atoi(m.header("Content-Length")); n > 0 {
		m.body = make([]byte, n)
		if _, err := io.ReadFull(r, m.body); err != nil {
			return nil, nil, err
		}
	}
	return nil, m, nil
}

// rtspLogin keeps the NVR's last challenge and signs requests with it.
type rtspLogin struct {
	user, pass                string
	scheme, realm, nonce, qop string
	opaque                    string
	nc                        int
}

func (a *rtspLogin) setChallenge(resp *rtspMsg) bool {
	ok := false
	for _, h := range resp.headers {
		k, v, _ := strings.Cut(h, ":")
		if !strings.EqualFold(strings.TrimSpace(k), "WWW-Authenticate") {
			continue
		}
		v = strings.TrimSpace(v)
		scheme, params, _ := strings.Cut(v, " ")
		switch {
		case strings.EqualFold(scheme, "Digest"):
			p := parseAuthParams(params)
			a.scheme, a.realm, a.nonce, a.opaque, a.nc = "Digest", p["realm"], p["nonce"], p["opaque"], 0
			a.qop = ""
			for _, q := range strings.Split(p["qop"], ",") {
				if strings.TrimSpace(q) == "auth" {
					a.qop = "auth"
				}
			}
			return true // Digest wins over Basic
		case strings.EqualFold(scheme, "Basic") && !ok:
			a.scheme, ok = "Basic", true
		}
	}
	return ok
}

func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		s = strings.TrimLeft(s, " ,")
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var v string
		if strings.HasPrefix(rest, "\"") {
			end := strings.Index(rest[1:], "\"")
			if end < 0 {
				v, s = rest[1:], ""
			} else {
				v, s = rest[1:1+end], rest[2+end:]
			}
		} else {
			v, s, _ = strings.Cut(rest, ",")
		}
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

func md5hex(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

// sign puts the Authorization header on a request (replacing any other).
func (a *rtspLogin) sign(m *rtspMsg) {
	var hs []string
	for _, h := range m.headers {
		if k, _, _ := strings.Cut(h, ":"); !strings.EqualFold(strings.TrimSpace(k), "Authorization") {
			hs = append(hs, h)
		}
	}
	m.headers = hs
	method, rest, _ := strings.Cut(m.start, " ")
	uri, _, _ := strings.Cut(rest, " ")
	switch a.scheme {
	case "Basic":
		m.headers = append(m.headers, "Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(a.user+":"+a.pass)))
	case "Digest":
		ha1 := md5hex(a.user + ":" + a.realm + ":" + a.pass)
		ha2 := md5hex(method + ":" + uri)
		h := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s"`, a.user, a.realm, a.nonce, uri)
		if a.qop == "auth" {
			a.nc++
			nc := fmt.Sprintf("%08x", a.nc)
			cb := make([]byte, 8)
			rand.Read(cb)
			cnonce := hex.EncodeToString(cb)
			h += fmt.Sprintf(`, response="%s", qop=auth, nc=%s, cnonce="%s"`, md5hex(ha1+":"+a.nonce+":"+nc+":"+cnonce+":auth:"+ha2), nc, cnonce)
		} else {
			h += fmt.Sprintf(`, response="%s"`, md5hex(ha1+":"+a.nonce+":"+ha2))
		}
		if a.opaque != "" {
			h += fmt.Sprintf(`, opaque="%s"`, a.opaque)
		}
		m.headers = append(m.headers, "Authorization: "+h)
	}
}

// relayRTSP relays one ffmpeg connection: requests get signed; a 401 answer is
// not passed on, the request goes again with the login (once per challenge).
func relayRTSP(c net.Conn, upstream, user, pass string) {
	defer c.Close()
	up, err := net.Dial("tcp", upstream)
	if err != nil {
		log.Printf("rtsp: no pude abrir el tunel: %v", err)
		return
	}
	defer up.Close()
	var (
		mu      sync.Mutex
		upMu    sync.Mutex // writes to the NVR come from both directions
		login   = &rtspLogin{user: user, pass: pass}
		last    *rtspMsg // the last request, to send again after a 401
		retried bool
	)
	writeUp := func(b []byte) error { upMu.Lock(); defer upMu.Unlock(); _, err := up.Write(b); return err }
	done := make(chan struct{}, 2)
	go func() { // ffmpeg -> NVR
		defer func() { done <- struct{}{} }()
		r := bufio.NewReaderSize(c, 64<<10)
		for {
			raw, m, err := readRTSP(r)
			if err != nil {
				return
			}
			if raw != nil {
				if writeUp(raw) != nil {
					return
				}
				continue
			}
			mu.Lock()
			last, retried = m, false
			if login.scheme != "" {
				login.sign(m)
			}
			b := m.bytes()
			mu.Unlock()
			if writeUp(b) != nil {
				return
			}
		}
	}()
	go func() { // NVR -> ffmpeg
		defer func() { done <- struct{}{} }()
		r := bufio.NewReaderSize(up, 256<<10)
		for {
			raw, m, err := readRTSP(r)
			if err != nil {
				return
			}
			if raw != nil {
				if _, err := c.Write(raw); err != nil {
					return
				}
				continue
			}
			if strings.Contains(m.start, " 401 ") {
				mu.Lock()
				again := last != nil && !retried && login.setChallenge(m)
				var b []byte
				if again {
					retried = true
					login.sign(last)
					b = last.bytes()
				}
				mu.Unlock()
				if again {
					if writeUp(b) != nil {
						return
					}
					continue // ffmpeg never sees this 401
				}
			}
			if _, err := c.Write(m.bytes()); err != nil {
				return
			}
		}
	}()
	<-done
}
