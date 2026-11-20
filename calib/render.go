package calib

import (
	"image"
	"math"
)

// colour bilinear sample of a 640x360 rgb24 buffer, channel ch (0..2)
func bilC(rgb []byte, x, y float64, ch int) float64 {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x > LW-1 {
		x = LW - 1
	}
	if y > LH-1 {
		y = LH - 1
	}
	x0, y0 := int(x), int(y)
	x1, y1 := x0+1, y0+1
	if x1 > LW-1 {
		x1 = LW - 1
	}
	if y1 > LH-1 {
		y1 = LH - 1
	}
	fx, fy := x-float64(x0), y-float64(y0)
	at := func(xx, yy int) float64 { return float64(rgb[(yy*LW+xx)*3+ch]) }
	return (at(x0, y0)*(1-fx)+at(x1, y0)*fx)*(1-fy) + (at(x0, y1)*(1-fx)+at(x1, y1)*fx)*fy
}

func clampf(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// RenderRGB24 stitches the two lens colour images into the panorama, using the
// calibration in st (run Run first). The input is a 640x720 rgb24 frame (top lens
// then bottom lens). Returns the cylinder panorama (CW x CH).
func RenderFull(st *State, frame []byte) *image.RGBA {
	c := &st.cyl
	g := &st.rig
	ac := frame[:LW*LH*3]
	bc := frame[LW*LH*3 : LW*LH*3*2]
	out := image.NewRGBA(image.Rect(0, 0, c.CW, c.CH))
	band := 0.5
	if st.warp != nil {
		band = HB / 2
	}
	var r [3]float64
	var qa, qb [2]float64
	for y := 0; y < c.CH; y++ {
		seamX := -1.0
		if st.ws != nil {
			seamX = float64(st.ws.Seam[y])
		}
		for x := 0; x < c.CW; x++ {
			hx, hy, _ := warpAt(st, float64(x), float64(y))
			hx /= 2
			hy /= 2
			cylRay(c, float64(x)-hx, float64(y)-hy, &r)
			inA := toA(g, &r, 0, &qa)
			cylRay(c, float64(x)+hx, float64(y)+hy, &r)
			inB := toBrow(st, &r, float64(y)+hy, &qb)

			k := (y*c.CW + x) * 4
			out.Pix[k+3] = 255
			if !inA && !inB {
				continue
			}
			// tB: weight of lens B (0 = all A). Seam is a soft blend over 2*band px.
			tB := 0.5
			if inA && inB {
				if seamX >= 0 {
					dd := float64(x) - seamX
					if !st.aLeft {
						dd = -dd
					}
					tB = clampf(0.5 + dd/(2*band))
				}
			} else if inB {
				tB = 1
			} else {
				tB = 0
			}
			for ch := 0; ch < 3; ch++ {
				var a, b float64
				if inA {
					a = bilC(ac, qa[0], qa[1], ch)
				}
				if inB {
					b = bilC(bc, qb[0], qb[1], ch)
				}
				v := a*(1-tB) + b*tB
				if v < 0 {
					v = 0
				}
				if v > 255 {
					v = 255
				}
				out.Pix[k+ch] = byte(v + 0.5)
			}
		}
	}
	_ = math.Abs
	return out
}
