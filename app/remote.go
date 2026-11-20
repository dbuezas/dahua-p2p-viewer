package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"time"
	"web"
)

// The phone remote: the same page and protocol as the old PC app, so the phone
// works the same. One WebSocket carries everything: the state (JSON) when it
// changes and every 3 s, one camera picture per second in turn, and the phone's
// commands ("big i", "hd i 0|1", "order k,k", "ptz i dx dy", "ptzstop", "zoom i 1|-1",
// "pano 0|3", "compartir 0|1"). Only the local network may connect.

func (g *game) startRemote() {
	port := g.webPort()
	if port <= 0 {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(web.Page)
	})
	mux.HandleFunc("/ws", g.serveWS)
	if os.Getenv("PPROF") != "" { // profiling, for measurements only
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	}
	mux.HandleFunc("/snap", func(w http.ResponseWriter, r *http.Request) {
		i, _ := strconv.Atoi(r.URL.Query().Get("i"))
		if b := g.snapshot(i, 480); b != nil {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/calib.js", g.serveCalibJS)
	mux.HandleFunc("/calibsnap", g.serveCalibSnap)
	mux.HandleFunc("/calibprev", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(g.meshHead()))
	})
	// plain HTTP actions (the page uses the WebSocket; these stay for old bookmarks)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/big":
			g.command("big " + q.Get("i"))
		case "/api/all":
			g.command("big -1")
		case "/api/hd":
			g.command("hd " + q.Get("i") + " " + q.Get("on"))
		case "/api/order":
			g.command("order " + q.Get("o"))
		case "/api/ptzpos": // tests: read (or with p,t,z set) the PTZ position of channel ch
			ch, _ := strconv.Atoi(q.Get("ch"))
			obj, ok := g.ptz.object(ch)
			if !ok || g.client == nil {
				http.Error(w, "sin ptz", 500)
				return
			}
			if q.Get("p") != "" {
				pp, _ := strconv.ParseFloat(q.Get("p"), 64)
				tt, _ := strconv.ParseFloat(q.Get("t"), 64)
				zz, _ := strconv.ParseFloat(q.Get("z"), 64)
				if err := g.client.PTZAbsolute(obj, pp, tt, zz); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				time.Sleep(3 * time.Second)
			}
			pp, tt, zz, err := g.client.PTZStatus(obj)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			fmt.Fprintf(w, "%g %g %g\n", pp, tt, zz)
			return
		case "/api/quit": // close the app cleanly (used by the tests instead of killing it)
			g.quit.Store(true)
			redraw()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(g.stateJSON()))
	})
	srv := &http.Server{Handler: localOnly(mux)}
	go func() {
		for tries := 0; ; tries++ { // right after a restart the old copy may still hold the port
			l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
			if err != nil {
				if tries == 0 || tries%30 == 0 {
					log.Printf("web: no pude abrir el puerto %d: %v", port, err)
				}
				time.Sleep(2 * time.Second)
				continue
			}
			log.Printf("web: control remoto en %s", strings.Join(g.remoteURLs(), " "))
			srv.Serve(l)
			return
		}
	}()
}

func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if ip == nil || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
			http.Error(w, "solo red local", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// remoteURLs: the addresses a phone on the same network can open.
func (g *game) remoteURLs() []string {
	port := g.webPort()
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.To4() == nil || !ipn.IP.IsPrivate() {
			continue
		}
		out = append(out, fmt.Sprintf("http://%s:%d", ipn.IP, port))
	}
	return out
}

// stateJSON: what the phone page shows (same fields as the old app).
func (g *game) stateJSON() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	type camJ struct {
		K   string `json:"k"`
		HD  bool   `json:"hd"`
		PTZ bool   `json:"ptz"`
	}
	st := struct {
		Big       int    `json:"big"`
		Split     bool   `json:"split"`
		Pano      int    `json:"pano"`
		PanoReady bool   `json:"panoReady"`
		Msg       string `json:"msg"`
		Share     bool   `json:"share"` // the configuration QR is on the TV
		Cams      []camJ `json:"cams"`
	}{Big: -1, Msg: g.panoMsg, Share: g.shareImg != nil, Cams: []camJ{}}
	for i, t := range g.tiles {
		if t == g.big {
			st.Big = i
		}
		t.s.mu.Lock()
		hd := t.s.hd
		if t.s.split {
			st.Split = true
			st.PanoReady = true // this app works the panorama out by itself
			if t.s.pano {
				st.Pano = 3
			}
		}
		t.s.mu.Unlock()
		st.Cams = append(st.Cams, camJ{K: t.key, HD: hd, PTZ: g.canPTZ(t)})
	}
	b, _ := json.Marshal(st)
	return string(b)
}

// command runs a phone command. Commands say the wanted result, so a repeated one
// changes nothing more.
func (g *game) command(cmd string) {
	p := strings.Fields(cmd)
	if len(p) == 0 {
		return
	}
	i := -1
	if len(p) > 1 {
		if n, err := strconv.Atoi(p[1]); err == nil {
			i = n
		}
	}
	g.mu.Lock()
	var t *tile
	if i >= 0 && i < len(g.tiles) {
		t = g.tiles[i]
	}
	g.mu.Unlock()
	switch p[0] {
	case "big":
		g.mu.Lock()
		g.big = t
		g.mu.Unlock()
	case "hd":
		if t != nil && len(p) > 2 {
			t.s.setHD(p[2] == "1")
			g.mu.Lock()
			g.saveHD()
			g.mu.Unlock()
		}
	case "order":
		if len(p) > 1 {
			g.setOrder(strings.Split(p[1], ","))
		}
	case "ptz":
		if t == nil || len(p) < 4 {
			return
		}
		dx, _ := strconv.Atoi(p[2])
		dy, _ := strconv.Atoi(p[3])
		if g.canPTZ(t) {
			g.ptz.drag(t.ch, dx*2, dy*2)
		} else { // a camera without PTZ: the joystick moves the zoomed picture
			g.mu.Lock()
			if t == g.big && t.zoomT > 1 {
				// a speed, not a jump: the TV moves a little every frame (the phone
				// sends every 150 ms; jumps that often looked jerky)
				k := 0.5 * 0.004 / t.zoomT / 0.15 // half the old speed: the user found it too fast
				t.panVX, t.panVY = float64(dx)*k, float64(dy)*k
				t.panVEnd = time.Now().Add(400 * time.Millisecond)
			}
			g.mu.Unlock()
		}
	case "ptzstop":
		g.ptz.release()
		g.mu.Lock()
		for _, t := range g.tiles {
			t.panVX, t.panVY = 0, 0
		}
		g.mu.Unlock()
	case "zoom":
		if t != nil && len(p) > 2 {
			g.mu.Lock()
			if len(p) > 3 && p[3] == "s" { // held: small steps
				f := 1.04
				if p[2] != "1" {
					f = 1 / f
				}
				t.zoomT = clamp(t.zoomT*f, 1, 4)
			} else {
				dir := 1
				if p[2] != "1" {
					dir = -1
				}
				g.zoomStep(t, dir)
			}
			g.mu.Unlock()
		}
	case "compartir": // the configuration QR on the TV (share.go): "compartir 1" shows it, "compartir 0" hides it
		g.mu.Lock()
		g.showShare(len(p) > 1 && p[1] == "1")
		g.mu.Unlock()
	case "pano":
		if len(p) > 1 {
			g.setPano(p[1] != "0")
		}
	case "ptzraw":
		if len(p) > 3 {
			ch, _ := strconv.Atoi(p[1])
			sp, _ := strconv.Atoi(p[3])
			code, _ := strconv.Atoi(p[2])
			codes := map[int]string{0: "Up", 1: "Down", 2: "Left", 3: "Right", 4: "ZoomTele", 5: "ZoomWide",
				0x20: "LeftUp", 0x21: "RightUp", 0x22: "LeftDown", 0x23: "RightDown"}
			if c, ok := codes[code]; ok {
				a1 := 0
				if code >= 0x20 {
					a1 = sp
				}
				g.ptz.start(ch, c, a1, sp)
			}
		}
	}
	g.stateChanged()
}

// setOrder puts the squares in the given order (keys like "0", "2.1"); unknown
// keys are ignored, missing squares go last.
func (g *game) setOrder(keys []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pos := map[string]int{}
	for i, k := range keys {
		pos[k] = i
	}
	old := append([]*tile(nil), g.tiles...)
	rank := func(t *tile, idx int) int {
		if p, ok := pos[t.key]; ok {
			return p
		}
		if t.half == 3 {
			if p, ok := pos[strconv.Itoa(t.ch)+".1"]; ok {
				return p
			}
		}
		return 1000 + idx
	}
	idx := map[*tile]int{}
	for i, t := range old {
		idx[t] = i
	}
	sortTiles(g.tiles, func(a, b *tile) bool { return rank(a, idx[a]) < rank(b, idx[b]) })
	g.saveOrder()
}

// setPano switches every split camera between its panorama and its two halves.
func (g *game) setPano(on bool) {
	g.mu.Lock()
	modes := g.cfg.panoModes()
	for ch, s := range g.streams {
		if !s.split {
			continue
		}
		s.mu.Lock()
		changed := s.pano != on
		s.pano = on
		hd := s.hd
		s.mu.Unlock()
		if changed && hd { // HD halves use the main stream, the panorama the sub stream
			s.restart()
		}
		if on {
			modes[ch] = 3
		} else {
			modes[ch] = 0
		}
	}
	g.cfg.writePanoModes(modes)
	chans := g.chans
	g.big = nil
	g.mu.Unlock()
	g.buildTiles(chans)
}

func (g *game) stateChanged() {
	g.stateVer.Add(1)
	redraw()
}

// snapshot: the current picture of square i as a small JPEG (its half for a
// split channel, the panorama for a panorama square).
func (g *game) snapshot(i, maxW int) []byte {
	g.mu.Lock()
	if i < 0 || i >= len(g.tiles) {
		g.mu.Unlock()
		return nil
	}
	t := g.tiles[i]
	g.mu.Unlock()
	t.s.mu.Lock()
	fr, pf, m := t.s.frame, t.s.pframe, t.s.mesh
	t.s.mu.Unlock()
	if fr == nil {
		return nil
	}
	src := fr.RGBA()
	if t.half == 3 {
		if pf == nil && m != nil { // the screen's panorama is on the graphics card: make one here
			if l := t.s.snapLut; l == nil || l.srcW != fr.W || l.srcH != fr.H {
				t.s.snapLut = buildMeshLut(m, fr.W, fr.H, 1600)
			}
			pf = image.NewRGBA(image.Rect(0, 0, t.s.snapLut.W, t.s.snapLut.H))
			t.s.snapLut.render(src.Pix, pf)
		}
		if pf == nil {
			return nil
		}
		src = pf
	}
	r := src.Bounds()
	switch t.half {
	case 1:
		r.Max.Y = r.Min.Y + r.Dy()/2
	case 2:
		r.Min.Y += r.Dy() / 2
	}
	small := shrink(src, r, maxW)
	var buf bytes.Buffer
	jpeg.Encode(&buf, small, &jpeg.Options{Quality: 70})
	return buf.Bytes()
}

// shrink copies the part r of src, made at most maxW wide (box average).
func shrink(src *image.RGBA, r image.Rectangle, maxW int) *image.RGBA {
	f := 1
	for r.Dx()/f > maxW {
		f++
	}
	w, h := r.Dx()/f, r.Dy()/f
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s [3]int
			for yy := 0; yy < f; yy++ {
				o := src.PixOffset(r.Min.X+x*f, r.Min.Y+y*f+yy)
				for xx := 0; xx < f; xx++ {
					s[0] += int(src.Pix[o])
					s[1] += int(src.Pix[o+1])
					s[2] += int(src.Pix[o+2])
					o += 4
				}
			}
			n := f * f
			k := out.PixOffset(x, y)
			out.Pix[k], out.Pix[k+1], out.Pix[k+2], out.Pix[k+3] = uint8(s[0]/n), uint8(s[1]/n), uint8(s[2]/n), 255
		}
	}
	return out
}

// ---- WebSocket (RFC 6455, just what the page needs) ----

func (g *game) serveWS(w http.ResponseWriter, r *http.Request) {
	// sender: state when it changes (and every 3 s), one camera picture per second in turn
	sender := func(k *web.Conn) {
		last, lastBeat, lastPic, cam := "", time.Time{}, time.Time{}, 0
		for k.Alive() {
			sj := g.stateJSON()
			if sj != last || time.Since(lastBeat) >= 3*time.Second {
				if k.Send(1, []byte(sj)) != nil {
					return
				}
				last, lastBeat = sj, time.Now()
			}
			g.mu.Lock()
			n := len(g.tiles)
			g.mu.Unlock()
			if n > 0 && time.Since(lastPic) >= time.Second {
				lastPic = time.Now()
				cam = (cam + 1) % n
				if jpg := g.snapshot(cam, 480); jpg != nil {
					if k.Send(2, append([]byte{byte(cam)}, jpg...)) != nil {
						return
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	// receiver: the phone's commands (and the calibration mesh, binary)
	web.ServeWS(w, r, sender, g.command, g.meshReceived)
}

// webPort: puerto_web in camaras.ini (default 8080); PUERTO_WEB overrides it.
func (g *game) webPort() int {
	if v, err := strconv.Atoi(os.Getenv("PUERTO_WEB")); err == nil {
		return v
	}
	return g.cfg.getInt("puerto_web", 8080)
}
