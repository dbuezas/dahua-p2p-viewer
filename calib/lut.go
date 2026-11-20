package calib

import "image"

// Lut is a precomputed panorama mapping: built once from the calibration, then
// applied to each video frame cheaply (nearest-neighbour + seam blend). This is
// what keeps live panorama fast, like the PC app's mesh.
type Lut struct {
	W, H       int
	x0, y0     int     // crop offset inside the full cylinder
	aIdx, bIdx []int32 // nearest lens-A / lens-B pixel (byte offset in rgb), -1 = none
	tB         []uint8 // weight of lens B, 0..255
}

// BuildLut computes the mapping (and a framing crop that drops the black bands).
func BuildLut(st *State) *Lut {
	c := &st.cyl
	g := &st.rig
	CW, CH := c.CW, c.CH
	band := 0.5
	if st.warp != nil {
		band = HB / 2
	}
	// full-cylinder maps first
	fa := make([]int32, CW*CH)
	fb := make([]int32, CW*CH)
	ft := make([]uint8, CW*CH)
	top := make([]int, CW)
	bot := make([]int, CW)
	for x := 0; x < CW; x++ {
		top[x] = -1
		bot[x] = -1
	}
	var r [3]float64
	var qa, qb [2]float64
	for y := 0; y < CH; y++ {
		seamX := -1.0
		if st.ws != nil {
			seamX = float64(st.ws.Seam[y])
		}
		for x := 0; x < CW; x++ {
			hx, hy, _ := warpAt(st, float64(x), float64(y))
			hx /= 2
			hy /= 2
			cylRay(c, float64(x)-hx, float64(y)-hy, &r)
			inA := toA(g, &r, 0, &qa)
			cylRay(c, float64(x)+hx, float64(y)+hy, &r)
			inB := toBrow(st, &r, float64(y)+hy, &qb)
			k := y*CW + x
			fa[k], fb[k] = -1, -1
			if inA {
				xi, yi := int(qa[0]+0.5), int(qa[1]+0.5)
				if xi >= 0 && xi < LW && yi >= 0 && yi < LH {
					fa[k] = int32(yi*LW + xi)
				}
			}
			if inB {
				xi, yi := int(qb[0]+0.5), int(qb[1]+0.5)
				if xi >= 0 && xi < LW && yi >= 0 && yi < LH {
					fb[k] = int32(yi*LW + xi)
				}
			}
			tB := 0.5
			if fa[k] >= 0 && fb[k] >= 0 {
				if seamX >= 0 {
					dd := float64(x) - seamX
					if !st.aLeft {
						dd = -dd
					}
					tB = clampf(0.5 + dd/(2*band))
				}
			} else if fb[k] >= 0 {
				tB = 1
			} else if fa[k] >= 0 {
				tB = 0
			}
			ft[k] = uint8(tB*255 + 0.5)
			if fa[k] >= 0 || fb[k] >= 0 {
				if top[x] < 0 {
					top[x] = y
				}
				bot[x] = y
			}
		}
	}
	// framing: inner rectangle fully covered (drops the black bands)
	x0, x1 := 0, CW-1
	for x0 < CW && top[x0] < 0 {
		x0++
	}
	for x1 > x0 && top[x1] < 0 {
		x1--
	}
	iT, iB := 0, CH-1
	for x := x0; x <= x1; x++ {
		if top[x] > iT {
			iT = top[x]
		}
		if bot[x] >= 0 && bot[x] < iB {
			iB = bot[x]
		}
	}
	w, h := x1-x0+1, iB-iT+1
	if w < 2 || h < 2 {
		x0, iT, w, h = 0, 0, CW, CH
	}
	l := &Lut{W: w, H: h, x0: x0, y0: iT, aIdx: make([]int32, w*h), bIdx: make([]int32, w*h), tB: make([]uint8, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src := (y+iT)*CW + (x + x0)
			dst := y*w + x
			l.aIdx[dst] = fa[src]
			l.bIdx[dst] = fb[src]
			l.tB[dst] = ft[src]
		}
	}
	return l
}

// render is the shared fast path. aOff/bOff are the byte offsets of each lens
// half in the frame; bpp is bytes per pixel (3 for rgb24, 4 for rgba).
func (l *Lut) render(frame []byte, aOff, bOff, bpp int, out *image.RGBA) {
	for i := 0; i < l.W*l.H; i++ {
		ai, bi, t := int(l.aIdx[i]), int(l.bIdx[i]), int(l.tB[i])
		k := i * 4
		out.Pix[k+3] = 255
		if ai < 0 && bi < 0 {
			out.Pix[k], out.Pix[k+1], out.Pix[k+2] = 0, 0, 0
			continue
		}
		pa := aOff + ai*bpp
		pb := bOff + bi*bpp
		for ch := 0; ch < 3; ch++ {
			var a, b int
			if ai >= 0 {
				a = int(frame[pa+ch])
			}
			if bi >= 0 {
				b = int(frame[pb+ch])
			}
			out.Pix[k+ch] = byte((a*(255-t) + b*t) / 255)
		}
	}
}

// RenderRGB24 applies the LUT to a 640x720 rgb24 frame (top lens, then bottom).
func (l *Lut) RenderRGB24(frame []byte, out *image.RGBA) {
	l.render(frame, 0, LW*LH*3, 3, out)
}

// RenderRGBA applies the LUT to a 640x720 rgba frame (top lens, then bottom).
func (l *Lut) RenderRGBA(frame []byte, out *image.RGBA) {
	l.render(frame, 0, LW*LH*4, 4, out)
}
