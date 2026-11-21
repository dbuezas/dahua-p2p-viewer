// Package camaras is the Go core for the Android app (bound with gomobile): the
// P2P tunnel to the NVR, the NVR's login, the camera list and the PTZ joystick,
// the same code the desktop app uses. The video itself is played by Android
// (MediaCodec through Media3/ExoPlayer) from the RTSP address given here.
package camaras

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	p2ptunnel "dh-p2p"
	"nvrlib"
)

const (
	httpPort = 21580 // local ports of the tunnel on the phone
	rtspPort = 21554

	extraBasePort  = 21600 // standalone cameras: one local RTSP port each, from here
	extraBaseIndex = 100   // and made-up camera numbers from here (never clash with the NVR's)
)

// Session is one connection to the NVR.
type Session struct {
	mu   sync.Mutex
	tn   *p2ptunnel.Tunnel
	c    *nvrlib.Client
	user string
	pass string
	joy  *nvrlib.Joystick
	cams []nvrlib.Channel

	calib   *http.Server
	meshVer int

	extras   map[int]*extraSrc   // standalone cameras (not on the NVR), by made-up number
	extraTns []*p2ptunnel.Tunnel // their tunnels, to close with the session
}

// extraSrc is one standalone camera's own local RTSP port and login.
type extraSrc struct {
	port int
	user string
	pass string
}

// SetClouds sets the P2P servers, "server|key|user" comma separated (camaras.ini
// "servidores"); empty keeps the built-in ones.
func SetClouds(list string) {
	var cs []p2ptunnel.Cloud
	for _, e := range strings.Split(list, ",") {
		if f := strings.Split(strings.TrimSpace(e), "|"); len(f) == 3 {
			cs = append(cs, p2ptunnel.Cloud{Server: f[0], Key: f[1], User: f[2]})
		}
	}
	if len(cs) > 0 {
		p2ptunnel.SetClouds(cs)
	}
}

// ErrBadPassword is in the error text when the NVR refuses the password.
const ErrBadPassword = "clave rechazada"

// Connect opens the tunnel to the NVR with this serial, logs in and reads the
// cameras. Call it off the main thread (it takes a few seconds).
func Connect(serial, user, pass string) (*Session, error) {
	// one session at a time: a new screen (the phone turned, the app reopened)
	// replaces the old one, which frees its local ports
	curMu.Lock()
	if cur != nil {
		cur.Close()
		cur = nil
	}
	curMu.Unlock()
	tn, err := p2ptunnel.Open(serial)
	if err != nil {
		return nil, fmt.Errorf("tunel: %v", err)
	}
	if err := tn.Forward("127.0.0.1", httpPort, 80); err != nil {
		tn.Close()
		return nil, err
	}
	if err := tn.Forward("127.0.0.1", rtspPort, 554); err != nil {
		tn.Close()
		return nil, err
	}
	c := nvrlib.New(fmt.Sprintf("127.0.0.1:%d", httpPort), user, pass)
	var lerr error
	for i := 0; i < 5; i++ {
		if lerr = c.Login(); lerr == nil || strings.Contains(lerr.Error(), "rechazado") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if lerr != nil {
		tn.Close()
		if strings.Contains(lerr.Error(), "rechazado") {
			return nil, fmt.Errorf("%s: %v", ErrBadPassword, lerr)
		}
		return nil, fmt.Errorf("login: %v", lerr)
	}
	cams, err := c.ListCameras()
	if err != nil {
		tn.Close()
		return nil, fmt.Errorf("camaras: %v", err)
	}
	s := &Session{tn: tn, c: c, user: user, pass: pass, joy: &nvrlib.Joystick{Client: c}, cams: cams}
	curMu.Lock()
	cur = s
	curMu.Unlock()
	return s, nil
}

// Cameras returns the cameras as JSON: [{"ch":0,"name":"...","on":true}, ...].
func (s *Session) Cameras() string {
	type cam struct {
		Ch   int    `json:"ch"`
		Name string `json:"name"`
		On   bool   `json:"on"`
	}
	var out []cam
	for _, c := range s.cams {
		name := c.Name
		if name == "" {
			name = c.Type // the NVR often has no name: the model, like the TV app shows
		}
		out = append(out, cam{c.Index, name, c.Enable})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// AddExtra adds a standalone camera (not on the NVR), reached by its own P2P
// serial. It opens the camera's tunnel and a local RTSP port, and makes the
// camera show in Cameras() with a made-up number (100, 101 ...). Call it after
// Connect and before Cameras(). An empty user means "admin".
func (s *Session) AddExtra(serial, user, pass, name string) error {
	tn, err := p2ptunnel.Open(serial)
	if err != nil {
		return fmt.Errorf("tunel: %v", err)
	}
	s.mu.Lock()
	i := len(s.extraTns)
	s.mu.Unlock()
	port := extraBasePort + i
	if err := tn.Forward("127.0.0.1", port, 554); err != nil {
		tn.Close()
		return err
	}
	if user == "" {
		user = "admin"
	}
	idx := extraBaseIndex + i
	if name == "" {
		name = fmt.Sprintf("Camara %d", i+1)
	}
	s.mu.Lock()
	if s.extras == nil {
		s.extras = map[int]*extraSrc{}
	}
	s.extras[idx] = &extraSrc{port: port, user: user, pass: pass}
	s.extraTns = append(s.extraTns, tn)
	s.cams = append(s.cams, nvrlib.Channel{Index: idx, Name: name, Type: "IMOU", Enable: true})
	s.mu.Unlock()
	return nil
}

// RTSP is the local address of a camera's video (hd: main stream, else the
// sub stream), through the tunnel.
func (s *Session) RTSP(ch int, hd bool) string {
	sub := 1
	if hd {
		sub = 0
	}
	s.mu.Lock()
	e := s.extras[ch]
	s.mu.Unlock()
	if e != nil { // a standalone camera: its own port and login, always channel 1
		return fmt.Sprintf("rtsp://%s:%s@127.0.0.1:%d/cam/realmonitor?channel=1&subtype=%d",
			url.QueryEscape(e.user), url.QueryEscape(e.pass), e.port, sub)
	}
	return fmt.Sprintf("rtsp://%s:%s@127.0.0.1:%d/cam/realmonitor?channel=%d&subtype=%d",
		url.QueryEscape(s.user), url.QueryEscape(s.pass), rtspPort, ch+1, sub)
}

// Direct tells whether the tunnel goes straight to the NVR (not over the relay).
func (s *Session) Direct() bool { return s.tn.Direct() }

// PTZDrag moves the PTZ like the desktop app's joystick: (dx, dy) pixels from
// the press point; invert turns left/right around; speedPct 100 = normal.
func (s *Session) PTZDrag(ch, dx, dy int, invert bool, speedPct int) {
	s.mu.Lock()
	s.joy.Invert, s.joy.SpeedPct = invert, speedPct
	s.mu.Unlock()
	s.joy.Drag(ch, dx, dy)
}

// PTZRelease stops the PTZ.
func (s *Session) PTZRelease() { s.joy.Release() }

// Close ends the session (the NVR is told, like the old app does).
func (s *Session) Close() {
	s.StopCalib()
	if s.c != nil {
		s.c.Logout() // free the NVR login at once (through the tunnel, so first)
	}
	for _, tn := range s.extraTns {
		tn.Close()
	}
	s.tn.Close()
}

var (
	curMu sync.Mutex
	cur   *Session
)

// SetLogFile sends the core's messages (tunnel, NVR) to a file (Android has no
// console for them). Call once at start.
func SetLogFile(path string) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
	os.Setenv("DH_STATS", "1")
}
