// app: the camera viewer (Mac/Windows/Linux; Android later) in Go, without the
// Dahua DLLs. Same behaviour as the old PC app (Camaras.cs):
//   - all cameras fill the screen without stretching (best layout), the split
//     two-lens camera as one wide panorama;
//   - click a camera: it goes big (the others small over its bottom edge); click
//     the big one or Esc: back to all; Esc with all: close;
//   - right click: HD on/off for that camera (saved in hd.txt);
//   - drag a camera onto another: they swap places (saved in orden.txt);
//   - big camera: wheel or +/- = smooth digital zoom, drag = move the picture;
//     on a PTZ camera, drag = joystick;
//   - each channel keeps one stream; a channel without video for 20 s restarts.
package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	p2ptunnel "dh-p2p"
	"github.com/skip2/go-qrcode"
	"nvrlib"
)

// tile is one square on screen.
type tile struct {
	key  string // like the old app: "0", "2.1" (top lens), "2.2" (bottom lens), "2.3" (panorama)
	ch   int
	half int // 0 whole picture, 1 top half, 2 bottom half, 3 panorama
	s    *stream
	name string

	img    *ebiten.Image // GPU copy of the stream's frame
	imgVer int
	rect   image.Rectangle // where it is on screen now

	// digital zoom (big view): zoom 1-4, pan -1..1 of the extra size; t* = targets
	zoom, zoomT, panX, panY, panTX, panTY float64
}

func (t *tile) units() int {
	if t.half == 3 {
		return 2
	}
	return 1
}

type game struct {
	cfg        *config
	tn         *p2ptunnel.Tunnel
	serial     string
	user, pass string
	client     *nvrlib.Client
	ptz        *ptzCtl

	mu       sync.Mutex
	tiles    []*tile
	streams  map[int]*stream
	big      *tile
	status   string // shown when there is nothing else to show
	chans    []nvrlib.Channel
	panoMsg  string
	stateVer atomic.Int64
	quit     atomic.Bool
	askPass  bool // the password screen shows
	passMsg  string
	passBuf  []rune
	passCh   chan string

	sw, sh int

	// mouse
	down, pressOK, dragging bool
	pressX, pressY          int
	dragFrom, ptzTile       *tile
	panTile                 *tile
	panX0, panY0            float64

	shotAt time.Time

	remoteText string
	remoteAt   time.Time
	remoteQR   *ebiten.Image
}

func (g *game) streamList() []*stream {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*stream
	for _, s := range g.streams {
		out = append(out, s)
	}
	return out
}

func (g *game) setStatus(s string) {
	g.mu.Lock()
	g.status = s
	g.mu.Unlock()
	log.Println(s)
	ebiten.ScheduleFrame()
}

// canPTZ: the "ptz" setting lists the channels whose camera really moves; a
// split camera can list one lens ("2.2"). The panorama has no PTZ (like the old app).
func (g *game) canPTZ(t *tile) bool {
	if t.half == 3 {
		return false
	}
	for _, s := range strings.Split(g.cfg.get("ptz", ""), ",") {
		s = strings.TrimSpace(s)
		if s != "" && (s == t.key || s == strconv.Itoa(t.ch)) {
			return true
		}
	}
	return false
}

// buildTiles makes one square per camera; a split channel gives its panorama
// (panorama on) or its two halves. The saved order is applied.
func (g *game) buildTiles(chans []nvrlib.Channel) {
	split := ints(g.cfg.get("dividir", ""))
	hd := map[int]bool{}
	for _, s := range g.cfg.list("hd.txt") {
		if n, err := strconv.Atoi(s); err == nil {
			hd[n] = true
		}
	}
	modes := g.cfg.panoModes()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.streams == nil {
		g.streams = map[int]*stream{}
	}
	var tiles []*tile
	only := ints(os.Getenv("CANALES")) // testing: CANALES=2 shows only channel 2
	for _, c := range chans {
		if !c.Enable || (len(only) > 0 && !only[c.Index]) {
			continue
		}
		isSplit := split[c.Index]
		if len(split) == 0 && len(c.Type) >= 2 && c.Type[:2] == "XM" { // no setting: the known two-lens camera
			isSplit = true
		}
		s := g.streams[c.Index]
		if s == nil {
			pano := true
			if m, ok := modes[c.Index]; ok {
				pano = m == 3
			}
			s = &stream{g: g, ch: c.Index, split: isSplit, hd: hd[c.Index], pano: pano}
			if isSplit {
				s.mesh = loadMesh3(g.cfg.path("panoramica3_" + strconv.Itoa(c.Index) + ".bin"))
				if s.mesh != nil {
					log.Printf("canal %d: hay calibracion del telefono (%dx%d)", c.Index, s.mesh.OW, s.mesh.OH)
				}
			}
			g.streams[c.Index] = s
		}
		name := c.Name
		if name == "" {
			name = c.Type
		}
		key := strconv.Itoa(c.Index)
		switch {
		case isSplit && s.pano:
			tiles = append(tiles, &tile{key: key + ".3", ch: c.Index, half: 3, s: s, name: name})
		case isSplit:
			tiles = append(tiles, &tile{key: key + ".1", ch: c.Index, half: 1, s: s, name: name})
			tiles = append(tiles, &tile{key: key + ".2", ch: c.Index, half: 2, s: s, name: name})
		default:
			tiles = append(tiles, &tile{key: key, ch: c.Index, s: s, name: name})
		}
	}
	// saved order (keys like "0", "2.1"); the panorama takes the place of its first lens
	pos := map[string]int{}
	for i, k := range g.cfg.list("orden.txt") {
		pos[k] = i
	}
	rank := func(t *tile) int {
		k := t.key
		if t.half == 3 {
			k = strconv.Itoa(t.ch) + ".1"
		}
		if p, ok := pos[k]; ok {
			return p
		}
		if p, ok := pos[strconv.Itoa(t.ch)]; ok {
			return p
		}
		return 1000
	}
	sort.SliceStable(tiles, func(a, b int) bool { return rank(tiles[a]) < rank(tiles[b]) })
	for _, t := range tiles {
		t.zoom, t.zoomT = 1, 1
	}
	g.tiles = tiles
}

func (g *game) saveOrder() {
	var keys []string
	for _, t := range g.tiles {
		if t.half == 3 { // keep both lens keys, like the old app's file
			keys = append(keys, strconv.Itoa(t.ch)+".1", strconv.Itoa(t.ch)+".2")
		} else {
			keys = append(keys, t.key)
		}
	}
	g.cfg.writeList("orden.txt", keys)
	log.Println("orden:", strings.Join(keys, ","))
}

func (g *game) saveHD() {
	var chs []string
	for ch, s := range g.streams {
		s.mu.Lock()
		if s.hd {
			chs = append(chs, strconv.Itoa(ch))
		}
		s.mu.Unlock()
	}
	sort.Strings(chs)
	g.cfg.writeList("hd.txt", chs)
}

// layout computes every square's place for the current view.
func (g *game) layout() {
	n := len(g.tiles)
	if n == 0 || g.sw == 0 {
		return
	}
	var rs []image.Rectangle
	if g.big != nil {
		bi := 0
		for i, t := range g.tiles {
			if t == g.big {
				bi = i
			}
		}
		rs = arrangeBig(n, bi, g.sw, g.sh)
	} else {
		u := make([]int, n)
		for i, t := range g.tiles {
			u[i] = t.units()
		}
		rs = arrange(u, g.sw, g.sh)
	}
	for i, t := range g.tiles {
		if i < len(rs) {
			t.rect = rs[i]
		}
	}
}

// tileAt: the small pictures lie on top of the big one, so they are checked first.
func (g *game) tileAt(x, y int) *tile {
	p := image.Pt(x, y)
	for _, t := range g.tiles {
		if t != g.big && p.In(t.rect) {
			return t
		}
	}
	if g.big != nil && p.In(g.big.rect) {
		return g.big
	}
	return nil
}

func (g *game) Update() error {
	if g.quit.Load() {
		return ebiten.Termination
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.askPass {
		g.updatePass()
		return nil
	}
	g.layout()
	mx, my := ebiten.CursorPosition()
	far := func() bool { return abs(mx-g.pressX) > 20 || abs(my-g.pressY) > 20 }

	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		g.down, g.pressX, g.pressY, g.dragging = true, mx, my, false
		t := g.tileAt(mx, my)
		g.pressOK = t != nil
		g.dragFrom, g.ptzTile, g.panTile = nil, nil, nil
		switch {
		case g.big == nil:
			g.dragFrom = t
		case t == g.big && g.canPTZ(t):
			g.ptzTile = t
		case t == g.big && t.zoomT > 1:
			g.panTile, g.panX0, g.panY0 = t, t.panTX, t.panTY
		}
	} else if g.down && ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
		if !g.dragging && far() {
			g.dragging = true
		}
		if g.dragging {
			switch {
			case g.panTile != nil: // zoomed camera: dragging moves the picture with the mouse
				t, r := g.panTile, g.panTile.rect
				t.panTX = clamp(g.panX0-float64(mx-g.pressX)/math.Max(1, float64(r.Dx())*(t.zoomT-1)/2), -1, 1)
				t.panTY = clamp(g.panY0-float64(my-g.pressY)/math.Max(1, float64(r.Dy())*(t.zoomT-1)/2), -1, 1)
			case g.ptzTile != nil: // PTZ camera: dragging = joystick
				g.ptz.drag(g.ptzTile.ch, mx-g.pressX, my-g.pressY)
			}
		}
	} else if g.down && inpututil.IsMouseButtonJustReleased(ebiten.MouseButtonLeft) {
		g.down = false
		switch {
		case g.ptzTile != nil && g.dragging:
			g.ptz.release()
		case g.panTile != nil && g.dragging:
		case g.dragFrom != nil && g.dragging:
			if to := g.tileAt(mx, my); to != nil && to != g.dragFrom {
				g.swap(g.dragFrom, to)
			}
		case g.pressOK && !g.dragging:
			g.click(g.pressX, g.pressY)
		}
		g.dragFrom, g.ptzTile, g.panTile, g.dragging = nil, nil, nil, false
	}
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonRight) {
		if t := g.tileAt(mx, my); t != nil {
			t.s.mu.Lock()
			on := !t.s.hd
			t.s.mu.Unlock()
			t.s.setHD(on)
			g.saveHD()
			log.Printf("canal %d: HD %v", t.ch, on)
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		if g.big != nil {
			g.big = nil
		} else if os.Getenv("NOQUIT") == "" {
			return ebiten.Termination
		}
	}
	// digital zoom of the big camera: wheel, +/- (one step x1.25, 1x-4x)
	if g.big != nil {
		_, wy := ebiten.Wheel()
		step := 0
		switch {
		case wy > 0 || inpututil.IsKeyJustPressed(ebiten.KeyEqual) || inpututil.IsKeyJustPressed(ebiten.KeyKPAdd):
			step = 1
		case wy < 0 || inpututil.IsKeyJustPressed(ebiten.KeyMinus) || inpututil.IsKeyJustPressed(ebiten.KeyKPSubtract):
			step = -1
		}
		if step != 0 {
			g.zoomStep(g.big, step)
		}
	}
	// smooth zoom and pan: each frame part of the way toward the target
	moving := false
	for _, t := range g.tiles {
		for _, v := range []*[2]*float64{{&t.zoom, &t.zoomT}, {&t.panX, &t.panTX}, {&t.panY, &t.panTY}} {
			cur, tgt := v[0], v[1]
			*cur += 0.25 * (*tgt - *cur)
			if math.Abs(*tgt-*cur) > 0.001 {
				moving = true
			} else {
				*cur = *tgt
			}
		}
	}
	if moving || g.dragging {
		ebiten.ScheduleFrame()
	}
	return nil
}

// click: on a camera, show it big; on the big one (or outside): back to all.
func (g *game) click(x, y int) {
	t := g.tileAt(x, y)
	if g.big != nil && (t == nil || t == g.big) {
		g.big = nil
	} else if t != nil {
		g.big = t
	}
}

func (g *game) swap(a, b *tile) {
	ia, ib := -1, -1
	for i, t := range g.tiles {
		if t == a {
			ia = i
		}
		if t == b {
			ib = i
		}
	}
	if ia < 0 || ib < 0 {
		return
	}
	g.tiles[ia], g.tiles[ib] = b, a
	g.saveOrder()
}

func (g *game) zoomStep(t *tile, dir int) {
	z := t.zoomT
	if dir > 0 {
		z *= 1.25
	} else {
		z /= 1.25
	}
	t.zoomT = clamp(z, 1, 4)
	if t.zoomT < 1.01 {
		t.zoomT, t.panTX, t.panTY = 1, 0, 0
	}
}

// source returns the GPU picture of a square and the part of it to show.
func (g *game) source(t *tile) (*ebiten.Image, image.Rectangle) {
	s := t.s
	s.mu.Lock()
	fr, pf, ver, m := s.frame, s.pframe, s.ver, s.mesh
	s.mu.Unlock()
	if t.half != 3 {
		if fr == nil {
			return nil, image.Rectangle{}
		}
		img := s.gpu.rgbOf(fr, ver) // converted on the graphics card
		r := img.Bounds()
		switch t.half {
		case 1:
			r.Max.Y = r.Min.Y + r.Dy()/2
		case 2:
			r.Min.Y = r.Min.Y + r.Dy()/2
		}
		return img, r
	}
	if pf == nil && fr != nil && m != nil {
		if img := s.gpu.panoOf(fr, ver, m); img != nil {
			return img, img.Bounds()
		}
	}
	src := pf
	if src == nil {
		return nil, image.Rectangle{}
	}
	b := src.Bounds()
	if ver != t.imgVer || t.img == nil {
		if t.img == nil || t.img.Bounds().Dx() != b.Dx() || t.img.Bounds().Dy() != b.Dy() {
			t.img = ebiten.NewImage(b.Dx(), b.Dy())
		}
		t.img.WritePixels(src.Pix)
		t.imgVer = ver
	}
	r := t.img.Bounds()
	switch t.half {
	case 1:
		r.Max.Y = r.Min.Y + r.Dy()/2
	case 2:
		r.Min.Y = r.Min.Y + r.Dy()/2
	}
	return t.img, r
}

func (g *game) drawTile(screen *ebiten.Image, t *tile) {
	r := t.rect.Inset(1) // a thin dark line between squares, like the old app
	if r.Empty() {
		return
	}
	dst := screen.SubImage(r).(*ebiten.Image) // the square cuts the zoomed picture
	dst.Fill(color.Black)
	img, sr := g.source(t)
	if img == nil {
		drawText(screen, t.name+"  (conectando...)", r.Min.X+g.px(12), r.Min.Y+g.px(10), g.px(20), false, color.RGBA{0xc0, 0xc0, 0xc0, 0xff})
		return
	}
	// a whole camera fills its square (like the old app's video panels); a half
	// or the panorama keeps its shape and fills the square as much as it can
	iw, ih := float64(sr.Dx()), float64(sr.Dy())
	sx, sy := float64(r.Dx())/iw, float64(r.Dy())/ih
	if t.half != 0 {
		m := math.Min(sx, sy)
		sx, sy = m, m
	}
	z := t.zoom
	zw, zh := iw*sx*z, ih*sy*z
	cx, cy := float64(r.Min.X)+float64(r.Dx())/2, float64(r.Min.Y)+float64(r.Dy())/2
	x := cx - zw/2 - t.panX*math.Max(0, zw-float64(r.Dx()))/2
	y := cy - zh/2 - t.panY*math.Max(0, zh-float64(r.Dy()))/2
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(sx*z, sy*z)
	op.GeoM.Translate(x, y)
	op.Filter = ebiten.FilterLinear
	dst.DrawImage(img.SubImage(sr).(*ebiten.Image), op)
	t.s.mu.Lock()
	hd := t.s.hd // what was chosen: the tag shows at once, the HD video follows
	weak := len(t.s.weak) > 0 && t.half == 3
	t.s.mu.Unlock()
	if hd { // HD tag (red, white bold letters), smaller on the small pictures
		fs, x0, y0 := g.px(38), r.Min.X+g.px(30), r.Min.Y+g.px(24)
		if t != g.big && g.big != nil { // the small pictures over the big one
			fs, x0, y0 = g.px(14), r.Min.X+g.px(6), r.Min.Y+g.px(5)
		}
		padX, h := fs*4/10, fs*145/100
		tw := textWidth("HD", fs, true)
		vector.DrawFilledRect(screen, float32(x0), float32(y0), float32(tw+2*padX), float32(h), color.RGBA{0xd5, 0x10, 0x18, 0xff}, false)
		drawText(screen, "HD", x0+padX, y0+(h-fs)/2-fs/12, fs, true, color.White)
	}
	if weak && t == g.big {
		drawText(screen, "panoramica con poca confianza", r.Min.X+g.px(12), r.Max.Y-g.px(34), g.px(18), false, color.RGBA{0xff, 0xd0, 0x40, 0xff})
	}
}

func (g *game) Draw(screen *ebiten.Image) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.askPass {
		g.drawPass(screen)
		g.maybeShot(screen)
		return
	}
	screen.Fill(color.Black)
	if len(g.tiles) == 0 {
		g.drawStatus(screen)
		return
	}
	g.layout()
	if g.big != nil {
		g.drawTile(screen, g.big)
	}
	for _, t := range g.tiles {
		if t != g.big {
			g.drawTile(screen, t)
		}
	}
	// drag and drop: a yellow frame on the target
	if g.dragging && g.dragFrom != nil {
		mx, my := ebiten.CursorPosition()
		if to := g.tileAt(mx, my); to != nil && to != g.dragFrom {
			r := to.rect
			vector.StrokeRect(screen, float32(r.Min.X)+3, float32(r.Min.Y)+3, float32(r.Dx())-6, float32(r.Dy())-6, 6, color.RGBA{0xff, 0xff, 0x00, 0xff}, false)
		}
	}
	g.drawRemoteAddr(screen)
	g.maybeShot(screen)
}

// drawRemoteAddr: the phone remote's address, small, in the bottom right corner,
// and its QR code in the bottom left (point the phone's camera at it), like the old app.
func (g *game) drawRemoteAddr(screen *ebiten.Image) {
	if g.remoteText == "" || time.Since(g.remoteAt) > 30*time.Second { // the address can change (DHCP)
		g.remoteAt = time.Now()
		if urls := g.remoteURLs(); len(urls) > 0 && urls[0] != g.remoteText {
			g.remoteText = urls[0]
			if q, err := qrcode.New(urls[0], qrcode.Medium); err == nil {
				q.DisableBorder = true
				// one pixel per module; drawn at a whole-number scale with no smoothing,
				// so every module is a sharp square on the real screen pixels
				bm := q.Bitmap()
				n := len(bm)
				img := image.NewRGBA(image.Rect(0, 0, n, n))
				for yy := 0; yy < n; yy++ {
					for xx := 0; xx < n; xx++ {
						c := color.RGBA{0xff, 0xff, 0xff, 0xff}
						if bm[yy][xx] {
							c = color.RGBA{0, 0, 0, 0xff}
						}
						img.SetRGBA(xx, yy, c)
					}
				}
				g.remoteQR = ebiten.NewImageFromImage(img)
			}
		}
	}
	if g.remoteQR != nil {
		n := g.remoteQR.Bounds().Dx()
		k := g.px(80) / n // whole screen pixels per module
		if k < 1 {
			k = 1
		}
		qs := n * k
		qz := 2 * k // white quiet zone (2 modules)
		x, y := g.px(6), g.sh-g.px(6)-qs-2*qz
		vector.DrawFilledRect(screen, float32(x), float32(y), float32(qs+2*qz), float32(qs+2*qz), color.White, false)
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Scale(float64(k), float64(k))
		op.GeoM.Translate(float64(x+qz), float64(y+qz))
		op.Filter = ebiten.FilterNearest
		screen.DrawImage(g.remoteQR, op)
	}
	if g.remoteText != "" {
		fs := g.px(19)
		w := textWidth(g.remoteText, fs, false)
		pad := g.px(6)
		x, y := g.sw-w-2*pad-g.px(8), g.sh-fs-2*pad-g.px(6)
		vector.DrawFilledRect(screen, float32(x), float32(y), float32(w+2*pad), float32(fs+2*pad), color.RGBA{0x20, 0x20, 0x20, 0xc0}, false)
		drawText(screen, g.remoteText, x+pad, y+pad-fs/10, fs, false, color.RGBA{0xe8, 0xe8, 0xe8, 0xff})
	}
}

// drawStatus shows the connection state in big letters (no cameras yet).
func (g *game) drawStatus(screen *ebiten.Image) {
	msg := g.status
	if msg == "" {
		msg = "Conectando..."
	}
	lines := strings.Split(msg, "\n")
	fs := g.px(44)
	y := g.sh/2 - len(lines)*fs*13/10/2
	for _, l := range lines {
		drawText(screen, l, (g.sw-textWidth(l, fs, false))/2, y, fs, false, color.White)
		y += fs * 13 / 10
	}
}

// maybeShot saves the screen to SHOT every 5 s (testing: screen captures cannot
// see a fullscreen app on Windows).
func (g *game) maybeShot(screen *ebiten.Image) {
	p := os.Getenv("SHOT")
	if p == "" || time.Since(g.shotAt) < 5*time.Second {
		return
	}
	g.shotAt = time.Now()
	b := screen.Bounds()
	pix := make([]byte, 4*b.Dx()*b.Dy())
	screen.ReadPixels(pix)
	img := &image.RGBA{Pix: pix, Stride: 4 * b.Dx(), Rect: image.Rect(0, 0, b.Dx(), b.Dy())}
	go func() {
		if f, err := os.Create(p); err == nil {
			jpeg.Encode(f, img, &jpeg.Options{Quality: 75})
			f.Close()
		}
	}()
}

// Layout: draw in real screen pixels; with Windows zoom (e.g. 175%) the picture
// would otherwise be drawn small and stretched (blurry), like the old app avoided.
func (g *game) Layout(w, h int) (int, int) {
	f := ebiten.DeviceScaleFactor()
	if f < 1 {
		f = 1
	}
	w, h = int(float64(w)*f), int(float64(h)*f)
	g.mu.Lock()
	g.sw, g.sh = w, h
	g.mu.Unlock()
	return w, h
}

// px: a size in pixels for a 1080-line screen, scaled to this screen.
func (g *game) px(n float64) int {
	v := int(n * float64(g.sh) / 1080)
	if v < 1 {
		v = 1
	}
	return v
}

// connect opens the tunnel, logs in and starts the streams; on failure it tries
// again (2 s, doubling up to 15 s), showing what happens on screen.
func (g *game) connect(serial string) {
	wait := 2 * time.Second
	for try := 1; ; try++ {
		err := g.connectOnce(serial)
		if err == nil {
			return
		}
		if errors.Is(err, errBadPass) {
			g.askPassword("El NVR no aceptó la clave.\nEscribila de nuevo (la misma que en DMSS):")
			wait = 2 * time.Second
			continue
		}
		g.setStatus(fmt.Sprintf("No hay conexion con las camaras (intento %d): %v\n\nVuelvo a probar en %d s.", try, err, int(wait.Seconds())))
		time.Sleep(wait)
		if wait *= 2; wait > 15*time.Second {
			wait = 15 * time.Second
		}
	}
}

func (g *game) connectOnce(serial string) error {
	g.setStatus("Conectando con el grabador...")
	tn, err := p2ptunnel.Open(serial)
	if err != nil {
		return fmt.Errorf("tunel: %v", err)
	}
	if err := tn.Forward("127.0.0.1", 1580, 80); err != nil {
		tn.Close()
		return err
	}
	g.tn = tn
	g.serial = serial
	if err := tn.Forward("127.0.0.1", 1554, 554); err != nil {
		tn.Close()
		return err
	}
	log.Println("tunel abierto (puertos 1580->80, 1554->554)")
	c := nvrlib.New("127.0.0.1:1580", g.user, g.pass)
	var lerr error
	for i := 0; i < 5; i++ {
		if lerr = c.Login(); lerr == nil || strings.Contains(lerr.Error(), "rechazado") {
			break // a refused password is never retried (the NVR locks the account)
		}
		time.Sleep(3 * time.Second)
	}
	if lerr != nil {
		tn.Close()
		if strings.Contains(lerr.Error(), "rechazado") {
			log.Printf("login: %v", lerr)
			return fmt.Errorf("%w: %v", errBadPass, lerr)
		}
		return fmt.Errorf("login: %v", lerr)
	}
	log.Println("login OK, modelo:", c.DeviceType())
	chans, err := c.ListCameras()
	if err != nil {
		tn.Close()
		return fmt.Errorf("lista de camaras: %v", err)
	}
	g.client = c
	g.mu.Lock()
	g.chans = chans
	g.mu.Unlock()
	g.buildTiles(chans)
	g.startRemote()
	log.Printf("camaras: %d cuadros", len(g.tiles))
	// start every channel's stream, a little apart so the tunnel is not flooded
	for _, s := range g.streamList() {
		go s.run()
		// apart, so the first pictures of every camera do not come in one burst
		// (on the TV PC's Wi-Fi a burst loses packets)
		time.Sleep(1500 * time.Millisecond)
	}
	go g.watchdog()
	go g.statsLog()
	return nil
}

// statsLog writes every channel's frame rate every 60 s.
func (g *game) statsLog() {
	for range time.Tick(60 * time.Second) {
		var parts []string
		for _, s := range g.streamList() {
			s.mu.Lock()
			parts = append(parts, fmt.Sprintf("canal %d %dx%d %.1f cuadros/s (cortes >0.3s: %d, el mas largo %.1fs)",
				s.ch, s.w, s.h, s.fps, s.gaps, s.gapMax.Seconds()))
			s.gaps, s.gapMax = 0, 0
			s.mu.Unlock()
		}
		sort.Strings(parts)
		log.Println("video:", strings.Join(parts, "; "))
	}
}

func main() {
	setupLog()
	// a bundled ffmpeg next to the app (Windows ships ffmpeg.exe there); on the Mac
	// also Homebrew's folders (an app started from the Finder does not have them)
	if exe, err := os.Executable(); err == nil {
		path := filepath.Dir(exe) + string(os.PathListSeparator) + os.Getenv("PATH")
		if runtime.GOOS == "darwin" {
			path += ":/opt/homebrew/bin:/usr/local/bin"
		}
		os.Setenv("PATH", path)
	}
	if !singleInstance() {
		log.Println("ya hay una copia abierta")
		return
	}
	cfg := loadConfig()
	serial := envOr("SERIAL", cfg.get("sn", firstFile(cfg.path("sn.txt"), "sn.txt", "../tunnel/sn.txt")))
	user := envOr("NVR_USER", "admin")
	// password: NVR_PASS, else the OS store (macOS Keychain / the old app's DPAPI blob)
	pass := os.Getenv("NVR_PASS")
	if pass == "" {
		var perr error
		if pass, perr = storedPassword(user); perr != nil || pass == "" {
			pass = "" // first run: the app asks for it on screen (see below)
		}
	}
	// P2P clouds to try, like the old app's "servidores": server|key|user, comma separated
	if sv := cfg.get("servidores", ""); sv != "" {
		var cs []p2ptunnel.Cloud
		for _, e := range strings.Split(sv, ",") {
			if f := strings.Split(strings.TrimSpace(e), "|"); len(f) == 3 {
				cs = append(cs, p2ptunnel.Cloud{Server: f[0], Key: f[1], User: f[2]})
			}
		}
		p2ptunnel.SetClouds(cs)
	}
	// memory follows what the cameras need: the heap may grow 50% above the live
	// data (Go's default is 100%), so all-HD gets what it needs and low quality stays
	// small, without a fixed number
	debug.SetGCPercent(50)
	g := &game{cfg: cfg, user: user, pass: pass}
	g.ptz = &ptzCtl{g: g}
	go func() {
		if g.pass == "" {
			g.askPassword("Clave del NVR (la misma que en DMSS):")
		}
		g.connect(serial)
	}()
	keepAwake()

	ebiten.SetWindowTitle("Camaras")
	setWindowIcon()
	ebiten.SetWindowSize(1280, 720)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	// draw only when something changed (a new frame, input, an animation)
	if os.Getenv("FPSFULL") == "" {
		ebiten.SetFPSMode(ebiten.FPSModeVsyncOffMinimum)
		go func() { // a slow heartbeat keeps status texts current
			for range time.Tick(time.Second) {
				ebiten.ScheduleFrame()
			}
		}()
	}
	if os.Getenv("WINDOWED") == "" {
		ebiten.SetFullscreen(true)
	}
	if err := ebiten.RunGameWithOptions(g, &ebiten.RunGameOptions{GraphicsLibrary: graphicsLib}); err != nil && err != ebiten.Termination {
		log.Fatal(err)
	}
	// close the P2P session cleanly (the NVR otherwise keeps it a while), then go
	if g.tn != nil {
		g.tn.Close()
		time.Sleep(300 * time.Millisecond)
	}
	os.Exit(0) // do not wait for streams
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func rgbaToRGB24(rgba []byte) []byte {
	n := len(rgba) / 4
	rgb := make([]byte, n*3)
	for i := 0; i < n; i++ {
		rgb[i*3], rgb[i*3+1], rgb[i*3+2] = rgba[i*4], rgba[i*4+1], rgba[i*4+2]
	}
	return rgb
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// firstFile returns the trimmed contents of the first path that has content.
func firstFile(paths ...string) string {
	for _, p := range paths {
		if s := readFile(p); s != "" {
			return s
		}
	}
	return ""
}
