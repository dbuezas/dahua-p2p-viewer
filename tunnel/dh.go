package p2ptunnel

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"
)

// The P2P cloud in use (server and the official client's credentials). Clouds
// lists the ones to try, in order (SetClouds); a device answers on one only.
var mainServer = "www.easy4ipcloud.com:8800"

var username = "cba1b29e32cb17aa46b8ff9e73c7f40b"
var userkey = "996103384cdf19179e19243e959bbf8b"

// Cloud is one P2P cloud: server (host:port, port 8800 if missing) and the
// official client's username and key.
type Cloud struct{ Server, User, Key string }

var clouds = []Cloud{{"www.easy4ipcloud.com:8800", "cba1b29e32cb17aa46b8ff9e73c7f40b", "996103384cdf19179e19243e959bbf8b"}}

// SetClouds sets the clouds to try in order (like the old app's "servidores").
func SetClouds(cs []Cloud) {
	var out []Cloud
	for _, c := range cs {
		if c.Server == "" || c.User == "" || c.Key == "" {
			continue
		}
		if !strings.Contains(c.Server, ":") {
			c.Server += ":8800"
		}
		out = append(out, c)
	}
	if len(out) > 0 {
		clouds = out
	}
}

func useCloud(c Cloud) { mainServer, username, userkey = c.Server, c.User, c.Key }

// udp is an unconnected UDP socket with a mutable current remote, mirroring the
// Python helper. Keeping one socket with a fixed local port across the whole
// handshake matters: the cloud/agent reply to that local port and the
// p2p-channel request advertises it.
type udp struct {
	conn   *net.UDPConn
	remote *net.UDPAddr
}

func newUDP() (*udp, error) {
	addr, _ := net.ResolveUDPAddr("udp", "0.0.0.0:0")
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	// Windows' default UDP socket buffers are tiny (~8 KB); under a video burst
	// packets overflow and are dropped, which stalls the stream. Make them big.
	c.SetReadBuffer(4 << 20)
	c.SetWriteBuffer(1 << 20)
	noConnReset(c)
	return &udp{conn: c}, nil
}

func (u *udp) setRemote(addr string) error {
	r, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	u.remote = r
	return nil
}

func (u *udp) localPort() int {
	return u.conn.LocalAddr().(*net.UDPAddr).Port
}

func (u *udp) send(data []byte) error {
	_, err := u.conn.WriteToUDP(data, u.remote)
	return err
}

// recv reads one datagram. A zero timeout means block.
func (u *udp) recv(buf []byte, timeout time.Duration) (int, error) {
	if timeout > 0 {
		u.conn.SetReadDeadline(time.Now().Add(timeout))
		defer u.conn.SetReadDeadline(time.Time{})
	}
	n, _, err := u.conn.ReadFromUDP(buf)
	return n, err
}

type dhResponse struct {
	version string
	code    int
	status  string
	headers map[string]string
	body    map[string]string
}

func (u *udp) dhRequest(path string, body string, cseq *uint32) error {
	method := "DHGET"
	if body != "" {
		method = "DHPOST"
	}

	nonce := rand.Uint32()
	currdate := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	pwd := fmt.Sprintf("%d%sDHP2P:%s:%s", nonce, currdate, username, userkey)
	sum := sha1.Sum([]byte(pwd))
	digest := base64.StdEncoding.EncodeToString(sum[:])

	*cseq++

	req := fmt.Sprintf("%s %s HTTP/1.1\r\n"+
		"CSeq: %d\r\n"+
		"Authorization: WSSE profile=\"UsernameToken\"\r\n"+
		"X-WSSE: UsernameToken Username=\"%s\", PasswordDigest=\"%s\", Nonce=\"%d\", Created=\"%s\"\r\n\r\n%s",
		method, path, *cseq, username, digest, nonce, currdate, body)

	debugf(">>> %s\n%s\n---\n", u.remote, req)
	return u.send([]byte(req))
}

func (u *udp) dhReadRaw() (*dhResponse, error) {
	buf := make([]byte, 65536)
	n, err := u.recv(buf, 8*time.Second) // the servers answer in about 2 s
	if err != nil {
		return nil, err
	}
	raw := string(buf[:n])
	debugf("<<< %s\n%s\n---\n", u.remote, raw)
	return parseResponse(raw)
}

func (u *udp) dhRead() (*dhResponse, error) {
	res, err := u.dhReadRaw()
	if err != nil {
		return nil, err
	}
	if res.code >= 300 {
		return nil, fmt.Errorf("error response: %d %s", res.code, res.status)
	}
	return res, nil
}

func parseResponse(raw string) (*dhResponse, error) {
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	head := parts[0]
	body := ""
	if len(parts) > 1 {
		body = parts[1]
	}

	lines := strings.Split(head, "\r\n")
	statusParts := strings.SplitN(lines[0], " ", 3)
	if len(statusParts) < 2 {
		return nil, fmt.Errorf("bad status line: %q", lines[0])
	}
	res := &dhResponse{version: statusParts[0], headers: map[string]string{}}
	fmt.Sscanf(statusParts[1], "%d", &res.code)
	if len(statusParts) >= 3 {
		res.status = statusParts[2]
	}
	for _, line := range lines[1:] {
		kv := strings.SplitN(line, ": ", 2)
		if len(kv) == 2 {
			res.headers[kv[0]] = kv[1]
		}
	}
	if strings.TrimSpace(body) != "" {
		res.body = parseBody(body)
	}
	return res, nil
}

// ipToBytes encodes an "ip:port" address the way the direct hole-punch path
// expects: big-endian port, then the 4 IP octets, all bitwise-inverted.
func ipToBytes(addr string) []byte {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil
	}
	ip4 := a.IP.To4()
	out := make([]byte, 0, 6)
	out = binary.BigEndian.AppendUint16(out, uint16(a.Port))
	out = append(out, ip4...)
	for i := range out {
		out[i] = ^out[i]
	}
	return out
}
