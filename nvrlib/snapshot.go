package nvrlib

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

func h(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) }

func field(hdr, key string) string {
	re := regexp.MustCompile(key + `="?([^",]+)"?`)
	m := re.FindStringSubmatch(hdr)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// SnapshotBytes returns a JPEG snapshot of a channel over HTTP Digest auth.
// baseHost is the tunnel's local end (e.g. "127.0.0.1:1580"), channel is 1-indexed.
func (c *Client) SnapshotBytes(baseHost string, channel int) ([]byte, error) {
	url := fmt.Sprintf("http://%s/cgi-bin/snapshot.cgi?channel=%d", baseHost, channel)
	// Reuse the client's kept-alive connection (the same one the RPC login
	// opened), so each snapshot does NOT open a new P2P tunnel connection.
	// Opening many tunnel connections is slow and unreliable; reusing one is fast.
	cl := c.http
	// Per-snapshot deadline: some channels (the PTZ dome) answer with an endless
	// MJPEG stream instead of one image, so we cap the read and take the first frame.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	req1, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	r1, err := cl.Do(req1)
	if err != nil {
		return nil, err
	}
	if r1.StatusCode == 200 {
		defer r1.Body.Close()
		return readFirstJPEG(r1.Body)
	}
	wa := r1.Header.Get("WWW-Authenticate")
	r1.Body.Close()
	if !strings.HasPrefix(wa, "Digest") {
		return nil, fmt.Errorf("no es Digest: %q", wa)
	}
	realm, nonce, qop := field(wa, "realm"), field(wa, "nonce"), field(wa, "qop")
	cb := make([]byte, 8)
	rand.Read(cb)
	cnonce := hex.EncodeToString(cb)
	nc := "00000001"
	uri := url[strings.Index(url, "/cgi-bin"):]
	ha1 := h(c.user + ":" + realm + ":" + c.pass)
	ha2 := h("GET:" + uri)
	resp := h(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	auth := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=%s, nc=%s, cnonce="%s", response="%s"`,
		c.user, realm, nonce, uri, qop, nc, cnonce, resp)
	req2, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req2.Header.Set("Authorization", auth)
	r2, err := cl.Do(req2)
	if err != nil {
		return nil, err
	}
	defer r2.Body.Close()
	if r2.StatusCode != 200 {
		return nil, fmt.Errorf("snapshot HTTP %d", r2.StatusCode)
	}
	return readFirstJPEG(r2.Body)
}

// readFirstJPEG reads until it has one complete JPEG (SOI 0xFFD8 .. EOI 0xFFD9),
// then returns it, even if the server keeps streaming more frames afterwards.
func readFirstJPEG(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	const limit = 8 << 20 // 8 MB safety cap
	buf := make([]byte, 0, 128<<10)
	tmp := make([]byte, 32<<10)
	for len(buf) < limit {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if s, e := jpegBounds(buf); e > 0 {
				return buf[s:e], nil
			}
		}
		if err != nil {
			if s, e := jpegBounds(buf); e > 0 {
				return buf[s:e], nil
			}
			return nil, err
		}
	}
	return nil, fmt.Errorf("snapshot sin fin de JPEG (%d bytes)", len(buf))
}

// jpegBounds returns the start and end+1 of the first JPEG in b, or (0,0).
func jpegBounds(b []byte) (int, int) {
	s := bytes.Index(b, []byte{0xFF, 0xD8})
	if s < 0 {
		return 0, 0
	}
	e := bytes.Index(b[s+2:], []byte{0xFF, 0xD9})
	if e < 0 {
		return 0, 0
	}
	return s, s + 2 + e + 2
}
