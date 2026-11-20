package main

import (
	"encoding/binary"
	"image"
	"math"
	"os"
	"regexp"
	"strconv"
)

// mesh3 is the panorama the phone works out (calib.js), the same file the old app
// keeps (panoramica3_<ch>.bin): output OW x OH; nodes every Step output pixels
// (NX x NY), each with lens A (x, y) and lens B (x, y) as parts of the lens
// picture's width and height (NaN: behind the lens); seam per output row (part of
// the width, -1: none); ALeft: lens A shows on the left of the seam. Lens A is the
// top half of the split picture, lens B the bottom half. Being relative, it works
// at any stream size (also HD).
type mesh3 struct {
	OW, OH, NX, NY, Step, Band int
	ALeft                      bool
	M, Seam                    []float32
	Head                       string
}

var meshNum = regexp.MustCompile(`"([A-Za-z]+)":(-?[0-9.]+)`)

func parseMesh3(b []byte) *mesh3 {
	if len(b) < 6 || b[0] != 'P' || b[1] != '3' {
		return nil
	}
	hl := int(binary.LittleEndian.Uint32(b[2:6]))
	if 6+hl > len(b) {
		return nil
	}
	head := string(b[6 : 6+hl])
	nums := map[string]int{}
	for _, m := range meshNum.FindAllStringSubmatch(head, -1) {
		if f, err := strconv.ParseFloat(m[2], 64); err == nil {
			if _, seen := nums[m[1]]; !seen {
				nums[m[1]] = int(f)
			}
		}
	}
	r := &mesh3{OW: nums["OW"], OH: nums["OH"], NX: nums["NX"], NY: nums["NY"], Step: nums["step"],
		Band: nums["band"], Head: head}
	r.ALeft = regexp.MustCompile(`"aLeft":true`).MatchString(head)
	if r.Band < 0 {
		r.Band = 0
	}
	if r.OW < 2 || r.OH < 2 || r.NX < 2 || r.NY < 2 || r.Step < 1 {
		return nil
	}
	off := 6 + hl
	off += (4 - off%4) % 4
	nm := r.NX * r.NY * 4
	if len(b) < off+(nm+r.OH)*4 {
		return nil
	}
	r.M = make([]float32, nm)
	for i := range r.M {
		r.M[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[off+i*4:]))
	}
	o2 := off + nm*4
	r.Seam = make([]float32, r.OH)
	for i := range r.Seam {
		r.Seam[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[o2+i*4:]))
	}
	return r
}

func loadMesh3(path string) *mesh3 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseMesh3(b)
}

// at: lens coordinates (parts of width / height) at output pixel (x, y); k = 0
// for lens A, 2 for lens B. False when behind the lens.
func (m *mesh3) at(x, y float64, k int) (float64, float64, bool) {
	gx := math.Max(0, math.Min(float64(m.NX)-1.0001, x/float64(m.Step)))
	gy := math.Max(0, math.Min(float64(m.NY)-1.0001, y/float64(m.Step)))
	x0, y0 := int(gx), int(gy)
	fx, fy := gx-float64(x0), gy-float64(y0)
	i00 := (y0*m.NX+x0)*4 + k
	i10, i01 := i00+4, i00+m.NX*4
	i11 := i01 + 4
	M := m.M
	u := (float64(M[i00])*(1-fx)+float64(M[i10])*fx)*(1-fy) + (float64(M[i01])*(1-fx)+float64(M[i11])*fx)*fy
	v := (float64(M[i00+1])*(1-fx)+float64(M[i10+1])*fx)*(1-fy) + (float64(M[i01+1])*(1-fx)+float64(M[i11+1])*fx)*fy
	return u, v, !math.IsNaN(u) && !math.IsNaN(v)
}

// meshLut: for every output pixel, which pixel of each lens to take and how much
// of lens B (a soft seam over the mesh's band). Built once per stream size, then
// each frame is a fast lookup.
type meshLut struct {
	W, H       int
	srcW, srcH int
	ia, ib     []int32   // byte offset into the RGBA frame, -1 = none
	wb         []uint8   // weight of lens B, 0-255
	seamX      []float32 // seam per output row, in output pixels (-1: none)
	aLeft      bool
	band       float64 // half width of the wide (colour) blend, output pixels
}

func buildMeshLut(m *mesh3, srcW, srcH, maxW int) *meshLut {
	W, H := m.OW, m.OH
	if maxW > 0 && W > maxW {
		H = H * maxW / W
		W = maxW
	}
	sx, sy := float64(m.OW)/float64(W), float64(m.OH)/float64(H)
	lh := srcH / 2
	l := &meshLut{W: W, H: H, srcW: srcW, srcH: srcH,
		ia: make([]int32, W*H), ib: make([]int32, W*H), wb: make([]uint8, W*H)}
	band := math.Max(2, float64(m.Band)/sx)
	l.band, l.aLeft = band, m.ALeft
	l.seamX = make([]float32, H)
	px := func(u, v float64, top bool) int32 {
		x, y := int(u*float64(srcW)), int(v*float64(lh))
		if x < 0 || x >= srcW || y < 0 || y >= lh {
			return -1
		}
		if !top {
			y += lh
		}
		return int32((y*srcW + x) * 4)
	}
	for y := 0; y < H; y++ {
		oy := (float64(y) + 0.5) * sy
		row := int(math.Min(float64(m.OH-1), oy))
		seam := float64(m.Seam[row])
		l.seamX[y] = -1
		if seam >= 0 {
			l.seamX[y] = float32(seam * float64(m.OW) / sx)
		}
		for x := 0; x < W; x++ {
			ox := (float64(x) + 0.5) * sx
			i := y*W + x
			l.ia[i], l.ib[i] = -1, -1
			if u, v, ok := m.at(ox, oy, 0); ok {
				l.ia[i] = px(u, v, true)
			}
			if u, v, ok := m.at(ox, oy, 2); ok {
				l.ib[i] = px(u, v, false)
			}
			// how much of lens B: 0 on lens A's side of the seam, 1 on B's, soft over the band
			w := 0.5
			if seam >= 0 {
				d := ((ox - seam*float64(m.OW)) / sx) / band // >0 right of the seam
				if m.ALeft {
					w = 0.5 + d
				} else {
					w = 0.5 - d
				}
			}
			switch {
			case l.ia[i] < 0:
				w = 1
			case l.ib[i] < 0:
				w = 0
			}
			l.wb[i] = uint8(math.Max(0, math.Min(1, w)) * 255)
		}
	}
	return l
}

func (l *meshLut) render(src []byte, dst *image.RGBA) {
	d := dst.Pix
	for i := range l.ia {
		a, b, w := l.ia[i], l.ib[i], int32(l.wb[i])
		o := i * 4
		switch {
		case a < 0 && b < 0:
			d[o], d[o+1], d[o+2] = 0, 0, 0
		case w == 0 || b < 0:
			d[o], d[o+1], d[o+2] = src[a], src[a+1], src[a+2]
		case w == 255 || a < 0:
			d[o], d[o+1], d[o+2] = src[b], src[b+1], src[b+2]
		default:
			wa := 255 - w
			d[o] = uint8((int32(src[a])*wa + int32(src[b])*w) / 255)
			d[o+1] = uint8((int32(src[a+1])*wa + int32(src[b+1])*w) / 255)
			d[o+2] = uint8((int32(src[a+2])*wa + int32(src[b+2])*w) / 255)
		}
		d[o+3] = 255
	}
	l.twoBand(src, dst)
}

// twoBand redoes the strip around each row's seam like the old app: colours and
// light blend over the wide band, details (picture minus its blur) over a narrow
// one, so there is no colour edge and no double picture.
func (l *meshLut) twoBand(src []byte, dst *image.RGBA) {
	const narrow, rad = 6.0, 6
	sgn := 1.0
	if !l.aLeft {
		sgn = -1
	}
	n := int(2*l.band) + 2
	A, B := make([]float64, n*3), make([]float64, n*3)
	pa, pb := make([]float64, (n+1)*3), make([]float64, (n+1)*3)
	for y := 0; y < l.H; y++ {
		sx := float64(l.seamX[y])
		if sx < 0 {
			continue
		}
		x0, x1 := int(sx-l.band), int(sx+l.band)
		if x0 < 0 {
			x0 = 0
		}
		if x1 > l.W {
			x1 = l.W
		}
		w := x1 - x0
		if w < 2*rad {
			continue
		}
		for k := 0; k < w; k++ {
			i := y*l.W + x0 + k
			a, b := l.ia[i], l.ib[i]
			if a < 0 {
				a = b
			}
			if b < 0 {
				b = a
			}
			for c := 0; c < 3; c++ {
				if a >= 0 {
					A[k*3+c], B[k*3+c] = float64(src[int(a)+c]), float64(src[int(b)+c])
				} else {
					A[k*3+c], B[k*3+c] = 0, 0
				}
			}
		}
		for c := 0; c < 3; c++ { // prefix sums for the horizontal box blur
			pa[c], pb[c] = 0, 0
		}
		for k := 0; k < w; k++ {
			for c := 0; c < 3; c++ {
				pa[(k+1)*3+c] = pa[k*3+c] + A[k*3+c]
				pb[(k+1)*3+c] = pb[k*3+c] + B[k*3+c]
			}
		}
		row := dst.Pix[y*dst.Stride:]
		for k := 0; k < w; k++ {
			lo, hi := k-rad, k+rad+1
			if lo < 0 {
				lo = 0
			}
			if hi > w {
				hi = w
			}
			cnt := float64(hi - lo)
			dd := float64(x0+k) + 0.5 - sx
			wW := math.Max(0, math.Min(1, 0.5+sgn*dd/l.band))
			wN := math.Max(0, math.Min(1, 0.5+sgn*dd/narrow))
			o := (x0 + k) * 4
			for c := 0; c < 3; c++ {
				ba := (pa[hi*3+c] - pa[lo*3+c]) / cnt
				bb := (pb[hi*3+c] - pb[lo*3+c]) / cnt
				v := ba*(1-wW) + bb*wW + (A[k*3+c]-ba)*(1-wN) + (B[k*3+c]-bb)*wN
				if v < 0 {
					v = 0
				} else if v > 255 {
					v = 255
				}
				row[o+c] = uint8(v)
			}
		}
	}
}
