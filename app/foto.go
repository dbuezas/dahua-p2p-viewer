package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// "Mejorar foto" (phone remote): the phone takes the current picture of a camera (GET /foto?i=N), makes it 4x
// bigger with AI on its own graphics card (web/ia.js) and sends back the finished parts (POST /fotoparte).
// The TV shows the same photo full screen from the first moment (the plain picture, made big), and each part
// replaces its place as it arrives. The phone moves and zooms the photo with the finger ("foto ver cx cy z")
// and closes it ("foto 0"). The TV keeps at most fotoMax pixels on the long side (graphics card limit).
// The photo belongs to the TV, not to one page: its state (id, view, finished parts) goes to every page, so a
// page loaded again or another phone shows the same photo (GET /foto/orig, /foto/parte); a page that sees
// nobody working ("foto work", every 2 s from the page doing the AI) does the parts still missing.
// Nobody using it for fotoIdle closes it, so the TV never stays stuck on an old photo.

const (
	fotoMax  = 4096
	fotoIdle = 5 * time.Minute
)

type foto struct {
	id         int
	src        *image.RGBA // the picture as taken (for the phone)
	big        *ebiten.Image
	srcImg     *ebiten.Image // the plain picture ("Ver original")
	orig       bool          // show srcImg instead of big
	bw, bh     int     // big's size
	scale      float64 // big pixels per AI pixel (AI pixels = 4x the picture)
	cx, cy, z  float64 // view: the point in the middle of the screen (0..1) and the zoom (1 = the whole photo)
	msg        string
	parts      []fotoParte // waiting to be put in (on the drawing thread)
	done       []fotoHecha // every finished part, for pages that come later
	srcPNG     []byte
	lastUse    time.Time
	lastWork   time.Time // a page said it is doing the AI
	needsStart bool
}

type fotoHecha struct {
	ax, ay, aw, ah int // in AI pixels (4x the picture): the part's place for the pages
	x, y           int // in the TV's photo pixels
	jpg            []byte
}

type fotoParte struct {
	x, y int
	img  *image.RGBA
}


// fotoOpen takes the picture of square i and shows it. The caller does not hold g.mu.
func (g *game) fotoOpen(i int) *foto {
	src, r := g.picture(i)
	if src == nil {
		return nil
	}
	pic := shrink(src, r, 1<<30) // a copy: the stream writes its frames again
	w, h := pic.Bounds().Dx(), pic.Bounds().Dy()
	sc := 1.0
	if m := 4 * max(w, h); m > fotoMax {
		sc = float64(fotoMax) / float64(m)
	}
	var buf bytes.Buffer // PNG: no loss (the AI would make JPEG blocks bigger)
	(&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, pic)
	g.mu.Lock()
	defer g.mu.Unlock()
	// the id changes with every photo, also across restarts of the app (a page remembers which one it closed)
	f := &foto{id: int(time.Now().UnixMilli() % 1e9), src: pic, srcPNG: buf.Bytes(), scale: sc, cx: 0.5, cy: 0.5, z: 1, lastUse: time.Now(), needsStart: true,
		bw: int(float64(4*w) * sc), bh: int(float64(4*h) * sc), msg: "Mejorando la foto con IA..."}
	g.foto = f
	g.stateChanged()
	redraw()
	return f
}

// fotoCommand: "foto 0" closes; "foto ver cx cy z" moves the view; "foto msg text..." sets the message;
// "foto orig 1|0" shows the plain picture or the AI one.
// The caller holds g.mu.
func (g *game) fotoCommand(p []string) {
	f := g.foto
	if f == nil || len(p) < 2 {
		return
	}
	f.lastUse = time.Now()
	switch p[1] {
	case "0":
		g.fotoClose()
	case "ver":
		defer g.stateChanged() // the other pages follow the view
		if len(p) >= 5 {
			cx, e1 := strconv.ParseFloat(p[2], 64)
			cy, e2 := strconv.ParseFloat(p[3], 64)
			z, e3 := strconv.ParseFloat(p[4], 64)
			if e1 == nil && e2 == nil && e3 == nil {
				f.cx, f.cy, f.z = clamp(cx, 0, 1), clamp(cy, 0, 1), clamp(z, 1, 16)
			}
		}
	case "work":
		f.lastWork = time.Now()
		return
	case "orig":
		f.orig = len(p) > 2 && p[2] == "1"
		g.stateChanged()
	case "msg":
		f.msg = ""
		for _, s := range p[2:] {
			if f.msg != "" {
				f.msg += " "
			}
			f.msg += s
		}
		g.stateChanged()
	}
	redraw()
}

// fotoClose: the caller holds g.mu.
func (g *game) fotoClose() {
	if g.foto == nil {
		return
	}
	if g.foto.big != nil {
		g.foto.big.Deallocate()
		g.foto.srcImg.Deallocate()
	}
	g.foto = nil
	g.stateChanged()
	redraw()
}

// serveFoto: GET /foto?i=N takes the picture of square N and shows it; answers {"id":...}.
func (g *game) serveFoto(w http.ResponseWriter, r *http.Request) {
	i, _ := strconv.Atoi(r.URL.Query().Get("i"))
	f := g.fotoOpen(i)
	if f == nil {
		http.Error(w, "sin imagen", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"id":` + strconv.Itoa(f.id) + `}`))
}

// serveFotoOrig: GET /foto/orig?id= the picture as taken (PNG).
func (g *game) serveFotoOrig(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	g.mu.Lock()
	f := g.foto
	g.mu.Unlock()
	if f == nil || f.id != id {
		http.Error(w, "foto cerrada", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(f.srcPNG)
}

// serveFotoHecha: GET /foto/parte?id=&n= finished part n (JPEG, the TV's size); X-Lugar: ax,ay,aw,ah.
func (g *game) serveFotoHecha(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, _ := strconv.Atoi(q.Get("id"))
	n, _ := strconv.Atoi(q.Get("n"))
	g.mu.Lock()
	f := g.foto
	var h fotoHecha
	ok := f != nil && f.id == id && n >= 0 && n < len(f.done)
	if ok {
		h = f.done[n]
	}
	g.mu.Unlock()
	if !ok {
		http.Error(w, "no hay", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Lugar", fmt.Sprintf("%d,%d,%d,%d", h.ax, h.ay, h.aw, h.ah))
	w.Write(h.jpg)
}

// fotoStateJSON: the photo for the pages' state. The caller holds g.mu.
func (g *game) fotoStateJSON() any {
	f := g.foto
	if f == nil {
		return nil
	}
	parts := make([]string, len(f.done))
	for i, h := range f.done {
		parts[i] = fmt.Sprintf("%d,%d", h.ax, h.ay)
	}
	return map[string]any{"id": f.id, "w": f.src.Bounds().Dx(), "h": f.src.Bounds().Dy(), "s": f.scale,
		"cx": f.cx, "cy": f.cy, "z": f.z, "orig": f.orig, "msg": f.msg, "parts": parts,
		"work": time.Since(f.lastWork) < 5*time.Second}
}

// serveFotoParte: POST /fotoparte?id=&x=&y=&ax=&ay=&aw=&ah= with a JPEG: a finished part, at (x, y) in the TV's
// photo pixels and (ax, ay, aw, ah) in AI pixels.
func (g *game) serveFotoParte(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, _ := strconv.Atoi(q.Get("id"))
	x, _ := strconv.Atoi(q.Get("x"))
	y, _ := strconv.Atoi(q.Get("y"))
	var a [4]int
	for i, k := range []string{"ax", "ay", "aw", "ah"} {
		a[i], _ = strconv.Atoi(q.Get(k))
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.foto
	if f == nil || f.id != id {
		http.Error(w, "foto cerrada", http.StatusGone)
		return
	}
	for _, h := range f.done {
		if h.ax == a[0] && h.ay == a[1] { // two pages did the same part
			return
		}
	}
	f.lastUse, f.lastWork = time.Now(), time.Now()
	f.parts = append(f.parts, fotoParte{x, y, rgba})
	f.done = append(f.done, fotoHecha{a[0], a[1], a[2], a[3], x, y, b})
	g.stateChanged()
}

// drawFoto: the photo full screen, with the phone's view. The caller holds g.mu.
func (g *game) drawFoto(screen *ebiten.Image) {
	f := g.foto
	if f == nil {
		return
	}
	if time.Since(f.lastUse) > fotoIdle {
		g.fotoClose()
		return
	}
	if f.needsStart { // the plain picture, made big, until the AI parts arrive
		f.needsStart = false
		f.big = ebiten.NewImage(f.bw, f.bh)
		op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
		op.GeoM.Scale(float64(f.bw)/float64(f.src.Bounds().Dx()), float64(f.bh)/float64(f.src.Bounds().Dy()))
		f.srcImg = ebiten.NewImageFromImage(f.src)
		f.big.DrawImage(f.srcImg, op)
	}
	for _, p := range f.parts {
		rr := p.img.Bounds().Add(image.Pt(p.x, p.y)).Intersect(f.big.Bounds())
		if rr.Empty() {
			continue
		}
		sub := p.img.SubImage(rr.Sub(image.Pt(p.x, p.y))).(*image.RGBA)
		f.big.SubImage(rr).(*ebiten.Image).WritePixels(packRGBA(sub))
	}
	f.parts = nil
	screen.Fill(color.Black)
	fit := min(float64(g.sw)/float64(f.bw), float64(g.sh)/float64(f.bh))
	k := fit * f.z
	op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
	if k >= 1.5 { // past 1.5 screen pixels per photo pixel, sharp squares look better than blur
		op.Filter = ebiten.FilterNearest
	}
	// no black border once the photo is bigger than the screen (the phone's screen has another shape)
	keep := func(v, size, view float64) float64 {
		half := view / 2 / (size * k)
		if half >= 0.5 {
			return 0.5
		}
		return clamp(v, half, 1-half)
	}
	cx, cy := keep(f.cx, float64(f.bw), float64(g.sw)), keep(f.cy, float64(f.bh), float64(g.sh))
	img := f.big
	if f.orig {
		img = f.srcImg
	}
	s := float64(f.bw) / float64(img.Bounds().Dx()) // big pixels per pixel of img
	op.GeoM.Translate(-cx*float64(img.Bounds().Dx()), -cy*float64(img.Bounds().Dy()))
	op.GeoM.Scale(k*s, k*s)
	op.GeoM.Translate(float64(g.sw)/2, float64(g.sh)/2)
	if f.orig {
		op.Filter = ebiten.FilterLinear
	}
	screen.DrawImage(img, op)
	if f.msg != "" {
		fs := g.px(26)
		pad := g.px(10)
		w := textWidth(f.msg, fs, false)
		x, y := (g.sw-w)/2, g.sh-fs-2*pad-g.px(20)
		vector.DrawFilledRect(screen, float32(x-pad), float32(y-pad), float32(w+2*pad), float32(fs+2*pad), color.RGBA{0x10, 0x10, 0x10, 0xd0}, false)
		drawText(screen, f.msg, x, y-fs/10, fs, false, color.RGBA{0xff, 0xd0, 0x40, 0xff})
	}
}

// packRGBA: the pixels of an RGBA sub-image, rows packed (WritePixels wants exactly w*h*4 bytes).
func packRGBA(m *image.RGBA) []byte {
	b := m.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		o := m.PixOffset(b.Min.X, y)
		out = append(out, m.Pix[o:o+b.Dx()*4]...)
	}
	return out
}
