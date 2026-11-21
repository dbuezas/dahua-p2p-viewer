package main

import (
	"log"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/hajimehoshi/ebiten/v2"

	"video"
)

// The graphics card does the per-pixel work: the YUV -> RGB conversion of every
// frame, and the panorama from the phone's mesh (the same math as mesh.go's
// meshLut.render + twoBand, which stays as the reference). The processor only
// copies the decoder's bytes up (1.5 bytes per pixel).

// yuvKage: the frame's bytes are uploaded as an RGBA texture W/4 wide and H*3/2
// high (4 bytes per texel); this picks the byte of each plane and converts
// (limited range, BT.601, as ffmpeg's own conversion did).
const yuvKage = `//kage:unit pixels
package main

var Size vec2
var Sharp float // 0 = off; 0..1 = AMD CAS sharpening of the brightness (CAMARAS_NITIDEZ)

func byteAt(idx float, row float) float {
	col := floor(idx / 4)
	t := imageSrc0UnsafeAt(imageSrc0Origin() + vec2(col+0.5, row+0.5))
	c := idx - col*4
	if c < 0.5 {
		return t.r
	}
	if c < 1.5 {
		return t.g
	}
	if c < 2.5 {
		return t.b
	}
	return t.a
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := floor(dstPos.xy - imageDstOrigin())
	w := Size.x
	h := Size.y
	y := byteAt(p.x, p.y)
	if Sharp > 0 {
		// CAS (AMD FidelityFX contrast adaptive sharpening) on the brightness, cross
		// of 4 neighbours: strong where the local contrast is low, weak near hard
		// edges (no halos)
		n := byteAt(p.x, max(p.y-1, 0))
		so := byteAt(p.x, min(p.y+1, h-1))
		we := byteAt(max(p.x-1, 0), p.y)
		ea := byteAt(min(p.x+1, w-1), p.y)
		mn := min(min(min(n, so), min(we, ea)), y)
		mx := max(max(max(n, so), max(we, ea)), y)
		amp := sqrt(clamp(min(mn, 1-mx)/max(mx, 1.0/255.0), 0, 1))
		k := -amp / mix(8.0, 5.0, Sharp)
		y = clamp((y+k*(n+so+we+ea))/(1+4*k), 0, 1)
	}
	crow := h + floor(p.y/2)
	u := byteAt(floor(p.x/2), crow)
	v := byteAt(w/2+floor(p.x/2), crow)
	Y := (y - 16.0/255.0) * 1.164
	U := u - 128.0/255.0
	V := v - 128.0/255.0
	r := clamp(Y+1.596*V, 0, 1)
	g := clamp(Y-0.392*U-0.813*V, 0, 1)
	b := clamp(Y+2.017*U, 0, 1)
	return vec4(r, g, b, 1)
}
`

// panoKage: the panorama. Images: 0 = the split frame (RGB, both lenses stacked),
// 1 = the mesh nodes (2 texels per node: lens A x,y and lens B x,y as 16-bit
// numbers), 2 = the seam per output row. Like meshLut: each output pixel takes
// lens A or B (or a soft mix over the band around the seam), and near the seam
// colours blend over the wide band while details blend over a narrow one.
const panoKage = `//kage:unit pixels
package main

var Src vec2   // frame size
var Out vec2   // panorama size drawn
var OSize vec2 // mesh output size
var Grid vec3  // NX, NY, Step
var Band float // half width of the colour blend, in drawn pixels
var ALeft float

func n16(t vec4, hi bool) float {
	if hi {
		return t.r*255*256 + t.g*255
	}
	return t.b*255*256 + t.a*255
}

// node returns lens k (0 = A, 1 = B) of mesh node (ix, iy): x, y and validity.
func node(ix float, iy float, k float) vec3 {
	t := imageSrc1UnsafeAt(imageSrc0Origin() + vec2(ix*2+k+0.5, iy+0.5))
	x := n16(t, true)
	y := n16(t, false)
	if x > 65534.5 || y > 65534.5 {
		return vec3(0, 0, 0)
	}
	return vec3(x/65534*3-1, y/65534*3-1, 1)
}

// meshAt: lens coordinates (parts of the lens size) at mesh output point o.
func meshAt(o vec2, k float) vec3 {
	gx := clamp(o.x/Grid.z, 0, Grid.x-1.0001)
	gy := clamp(o.y/Grid.z, 0, Grid.y-1.0001)
	x0 := floor(gx)
	y0 := floor(gy)
	fx := gx - x0
	fy := gy - y0
	a := node(x0, y0, k)
	b := node(x0+1, y0, k)
	c := node(x0, y0+1, k)
	d := node(x0+1, y0+1, k)
	if a.z*b.z*c.z*d.z < 0.5 {
		return vec3(0, 0, 0)
	}
	p := (a.xy*(1-fx)+b.xy*fx)*(1-fy) + (c.xy*(1-fx)+d.xy*fx)*fy
	return vec3(p, 1)
}

// lens: the frame colour at lens coordinates (alpha 0 = behind the lens).
func lens(m vec3, bottom bool) vec4 {
	if m.z < 0.5 {
		return vec4(0)
	}
	lh := floor(Src.y / 2)
	x := floor(m.x * Src.x)
	y := floor(m.y * lh)
	if x < 0 || x >= Src.x || y < 0 || y >= lh {
		return vec4(0)
	}
	if bottom {
		y += lh
	}
	c := imageSrc0UnsafeAt(imageSrc0Origin() + vec2(x+0.5, y+0.5))
	return vec4(c.rgb, 1)
}

// pair: lens A and B colours at drawn pixel x of row oy (a missing one takes the other).
func pair(px float, oy float, sx float) (vec4, vec4) {
	o := vec2((px+0.5)*sx, oy)
	a := lens(meshAt(o, 0), false)
	b := lens(meshAt(o, 1), true)
	if a.a < 0.5 {
		a = b
	}
	if b.a < 0.5 {
		b = a
	}
	return a, b
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := floor(dstPos.xy - imageDstOrigin())
	sx := OSize.x / Out.x
	sy := OSize.y / Out.y
	oy := (p.y + 0.5) * sy
	row := min(OSize.y-1, floor(oy))
	st := imageSrc2UnsafeAt(imageSrc0Origin() + vec2(row+0.5, 0.5))
	sv := st.r*255*256 + st.g*255
	hasSeam := sv < 65534.5
	seam := sv/65534*3 - 1
	o := vec2((p.x+0.5)*sx, oy)
	a := lens(meshAt(o, 0), false)
	b := lens(meshAt(o, 1), true)
	if a.a < 0.5 && b.a < 0.5 {
		return vec4(0, 0, 0, 1)
	}
	w := 0.5
	seamX := 0.0
	if hasSeam {
		seamX = seam * OSize.x / sx
		d := ((o.x - seam*OSize.x) / sx) / Band
		if ALeft > 0.5 {
			w = 0.5 + d
		} else {
			w = 0.5 - d
		}
	}
	if a.a < 0.5 {
		w = 1
	}
	if b.a < 0.5 {
		w = 0
	}
	w = clamp(w, 0, 1)
	out := a.rgb*(1-w) + b.rgb*w
	// two bands at the seam: colours over the wide band, details over a narrow one
	x0 := max(floor(seamX-Band), 0)
	x1 := min(floor(seamX+Band), Out.x)
	if hasSeam && p.x >= x0 && p.x < x1 && x1-x0 >= 12 {
		A, B := pair(p.x, oy, sx)
		sa := vec3(0)
		sb := vec3(0)
		cnt := 0.0
		for i := -6; i <= 6; i++ {
			q := p.x + float(i)
			if q >= x0 && q < x1 {
				qa, qb := pair(q, oy, sx)
				sa += qa.rgb
				sb += qb.rgb
				cnt += 1
			}
		}
		ba := sa / cnt
		bb := sb / cnt
		sgn := 1.0
		if ALeft < 0.5 {
			sgn = -1.0
		}
		dd := p.x + 0.5 - seamX
		wW := clamp(0.5+sgn*dd/Band, 0, 1)
		wN := clamp(0.5+sgn*dd/6, 0, 1)
		out = ba*(1-wW) + bb*wW + (A.rgb-ba)*(1-wN) + (B.rgb-bb)*wN
	}
	return vec4(clamp(out, 0, 1), 1)
}
`

var yuvShader, panoShader *ebiten.Shader

func shaders() bool {
	if yuvShader != nil {
		return panoShader != nil
	}
	var err error
	if yuvShader, err = ebiten.NewShader([]byte(yuvKage)); err != nil {
		log.Fatalf("shader yuv: %v", err)
	}
	if panoShader, err = ebiten.NewShader([]byte(panoKage)); err != nil {
		log.Printf("shader panoramica: %v", err)
	}
	return panoShader != nil
}

// gpuState is a stream's textures (used only from Draw).
type gpuState struct {
	ver       int
	pack, rgb *ebiten.Image
	pano      *ebiten.Image
	panoVer   int
	mesh      *mesh3
	meshTex   *ebiten.Image
	seamTex   *ebiten.Image
}

func quad(w, h int) ([]ebiten.Vertex, []uint16) {
	fw, fh := float32(w), float32(h)
	v := []ebiten.Vertex{
		{DstX: 0, DstY: 0, SrcX: 0, SrcY: 0, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: fw, DstY: 0, SrcX: fw, SrcY: 0, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: 0, DstY: fh, SrcX: 0, SrcY: fh, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: fw, DstY: fh, SrcX: fw, SrcY: fh, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
	}
	return v, []uint16{0, 1, 2, 1, 2, 3}
}

// rgbOf returns the frame as an RGB texture (converted on the graphics card once
// per new frame).
func (gs *gpuState) rgbOf(f *video.Frame, ver int) *ebiten.Image {
	shaders()
	if gs.rgb != nil && gs.ver == ver && gs.rgb.Bounds().Dx() == f.W && gs.rgb.Bounds().Dy() == f.H {
		return gs.rgb
	}
	pw, ph := f.W/4, f.H*3/2
	if gs.pack == nil || gs.pack.Bounds().Dx() != pw || gs.pack.Bounds().Dy() != ph {
		gs.pack = ebiten.NewImage(pw, ph)
		gs.rgb = ebiten.NewImage(f.W, f.H)
	}
	gs.pack.WritePixels(f.Pix)
	v, i := quad(f.W, f.H)
	op := &ebiten.DrawTrianglesShaderOptions{Blend: ebiten.BlendCopy}
	op.Images[0] = gs.pack
	op.Uniforms = map[string]any{"Size": []float32{float32(f.W), float32(f.H)}, "Sharp": sharpness()}
	gs.rgb.DrawTrianglesShader(v, i, yuvShader, op)
	gs.ver = ver
	return gs.rgb
}

// panoOf returns the panorama of a split frame, made on the graphics card from
// the phone's mesh (nil if the shader is not available).
func (gs *gpuState) panoOf(f *video.Frame, ver int, m *mesh3) *ebiten.Image {
	if !shaders() || m == nil {
		return nil
	}
	if gs.pano != nil && gs.panoVer == ver && gs.mesh == m {
		return gs.pano
	}
	rgb := gs.rgbOf(f, ver)
	W, H := m.OW, m.OH
	if W > 1600 {
		H = H * 1600 / W
		W = 1600
	}
	if gs.mesh != m || gs.meshTex == nil {
		gs.meshTex, gs.seamTex = meshTextures(m)
		gs.mesh = m
		gs.pano = nil
	}
	if gs.pano == nil || gs.pano.Bounds().Dx() != W || gs.pano.Bounds().Dy() != H {
		gs.pano = ebiten.NewImage(W, H)
	}
	sx := float64(m.OW) / float64(W)
	band := math.Max(2, float64(m.Band)/sx)
	aLeft := float32(0)
	if m.ALeft {
		aLeft = 1
	}
	v, i := quad(W, H)
	op := &ebiten.DrawTrianglesShaderOptions{Blend: ebiten.BlendCopy}
	op.Images[0] = rgb
	op.Images[1] = gs.meshTex
	op.Images[2] = gs.seamTex
	op.Uniforms = map[string]any{
		"Src":   []float32{float32(f.W), float32(f.H)},
		"Out":   []float32{float32(W), float32(H)},
		"OSize": []float32{float32(m.OW), float32(m.OH)},
		"Grid":  []float32{float32(m.NX), float32(m.NY), float32(m.Step)},
		"Band":  float32(band),
		"ALeft": aLeft,
	}
	gs.pano.DrawTrianglesShader(v, i, panoShader, op)
	gs.panoVer = ver
	return gs.pano
}

// enc16 packs a mesh value (a part of the lens size, -1 .. 2) in 16 bits; NaN or
// "none" = 65535.
func enc16(v float32) (byte, byte) {
	if math.IsNaN(float64(v)) || v < -1 || v > 2 {
		return 0xff, 0xff
	}
	n := uint16(math.Round(float64(v+1) / 3 * 65534))
	return byte(n >> 8), byte(n)
}

func meshTextures(m *mesh3) (*ebiten.Image, *ebiten.Image) {
	nodes := make([]byte, m.NX*2*m.NY*4)
	for iy := 0; iy < m.NY; iy++ {
		for ix := 0; ix < m.NX; ix++ {
			for k := 0; k < 2; k++ {
				i := (iy*m.NX + ix) * 4
				o := ((iy*m.NX*2 + ix*2 + k) * 4)
				nodes[o], nodes[o+1] = enc16(m.M[i+k*2])
				nodes[o+2], nodes[o+3] = enc16(m.M[i+k*2+1])
			}
		}
	}
	mt := ebiten.NewImage(m.NX*2, m.NY)
	mt.WritePixels(nodes)
	seam := make([]byte, m.OH*4)
	for y := 0; y < m.OH; y++ {
		v := m.Seam[y]
		if v < 0 {
			seam[y*4], seam[y*4+1] = 0xff, 0xff
		} else {
			seam[y*4], seam[y*4+1] = enc16(v)
		}
		seam[y*4+3] = 0xff
	}
	st := ebiten.NewImage(m.OH, 1)
	st.WritePixels(seam)
	return mt, st
}

var sharpOnce sync.Once
var sharpBits atomic.Uint32 // float32 bits

// sharpness: CAMARAS_NITIDEZ at the start (0 = off, up to 1); setSharpness changes it while running.
func sharpness() float32 {
	sharpOnce.Do(func() {
		if v, err := strconv.ParseFloat(os.Getenv("CAMARAS_NITIDEZ"), 64); err == nil {
			setSharpness(v)
		}
	})
	return math.Float32frombits(sharpBits.Load())
}

func setSharpness(v float64) {
	s := float32(math.Max(0, math.Min(1, v)))
	sharpBits.Store(math.Float32bits(s))
	log.Printf("nitidez: %.2f", s)
}
