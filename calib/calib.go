package calib

// Go port of the numerical part of calib_fast.js (Calib.run, stripMatch). Everything only for the phone page
// (show, preview, slideData, rowData, makeView, stripBox, seamBox, framing, mesh, pack) is left out: none of it
// changes the result. Operations are written in the same order as the JavaScript (left to right, same
// float32 stores), so the results are the same bit for bit where possible.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
)

const LW, LH = 640, 360

var NaN = math.NaN()
var piV = math.Pi                              // a variable: Go folds constant expressions exactly, JavaScript rounds each step
var WMAX = 1 / func() float64 { return 0.2 }() // nothing nearer than 20 cm (5)

type Lens struct{ F, K float64 }
type Rig struct {
	L         Lens
	R         [9]float64
	H         [3]float64
	Pan, Tilt float64
}

// Options: the camera constants (CAM) and the fit options, with the JavaScript's "missing" semantics.
type Opts struct {
	F, K, TA, Base, Ax, Az float64
	Lam, Loose, RowLim     float64 // already with || defaults applied where the JS does (lam||0, loose||1, rowLim||0.15)
	OutW                   int
	Pos, Joint, Joint2     bool
	Warp                   bool
	RowSmooth, PairW       *float64
	CurveCost              *float64
	HasStart               bool
	StartPan, StartTilt    float64
}

type Cyl struct {
	A0, A1, Fc, H0 float64
	CW, CH         int
}

type WS struct {
	Seam []float32
	W    []float32
}

type State struct {
	A, B     []float32
	o        *Opts
	rig      Rig
	cyl      Cyl
	hasCyl   bool
	rowKnots []float64
	ws       *WS
	aLeft    bool
	warp     *Warp
	FitX     []float64
	MatchErr float64
	Matches  [][4]float64
	// Weak lists why the result may be wrong. Calibration never refuses: a person
	// or a near thing can spoil it, so it keeps the best it found and only warns.
	Weak []string
}

type calibErr struct{ msg string }

func fail(f string, a ...any) { panic(calibErr{fmt.Sprintf(f, a...)}) }

func mul(a, b [9]float64) [9]float64 {
	var r [9]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				r[i*3+j] += a[i*3+k] * b[k*3+j]
			}
		}
	}
	return r
}
func tr(a [9]float64) [9]float64 {
	return [9]float64{a[0], a[3], a[6], a[1], a[4], a[7], a[2], a[5], a[8]}
}

// Rz(roll) * Rx(pitch) * Ry(yaw)
func rot(yaw, pitch, roll float64) [9]float64 {
	cy, sy, cp, sp, cr, sr := cos(yaw), sin(yaw), cos(pitch), sin(pitch), cos(roll), sin(roll)
	return mul([9]float64{cr, -sr, 0, sr, cr, 0, 0, 0, 1}, mul([9]float64{1, 0, 0, 0, cp, -sp, 0, sp, cp}, [9]float64{cy, 0, sy, 0, 1, 0, -sy, 0, cy}))
}

func project(L Lens, x, y, z float64, q *[2]float64) bool {
	ln := math.Sqrt(x*x + y*y + z*z)
	if z < 0.05*ln {
		return false
	}
	xy := math.Sqrt(x*x + y*y)
	if xy < 1e-12 {
		q[0] = LW / 2
		q[1] = LH / 2
		return true
	}
	zl := z / ln
	if zl > 1 {
		zl = 1
	}
	t := acos(zl)
	rd := L.F * (t + L.K*t*t*t)
	q[0] = LW/2 + x/xy*rd
	q[1] = LH/2 + y/xy*rd
	return true
}

func unproject(L Lens, px, py float64, r *[3]float64) {
	u, v := px-LW/2, py-LH/2
	rd := math.Sqrt(u*u+v*v) / L.F
	if rd < 1e-9 {
		r[0], r[1], r[2] = 0, 0, 1
		return
	}
	t := rd
	for i := 0; i < 8; i++ {
		g := t + L.K*t*t*t - rd
		d := 1 + 3*L.K*t*t
		t -= g / d
	}
	sn := sin(t) / (rd * L.F)
	r[0] = u * sn
	r[1] = v * sn
	r[2] = cos(t)
}

// lens parameters for a rig: F, K, tA, base, ax, az
type RigP struct{ F, K, TA, Base, Ax, Az float64 }

func (o *Opts) rp() RigP { return RigP{o.F, o.K, o.TA, o.Base, o.Ax, o.Az} }

func makeRig(o RigP, pan, tilt float64) Rig {
	MA := rot(0, o.TA, 0)
	R := mul(rot(0, tilt, 0), mul(rot(pan, 0, 0), tr(MA)))
	ax, az := o.Ax, o.Az
	d := [3]float64{sin(ax), cos(ax) * cos(az), sin(az)}
	var h [3]float64
	for i := 0; i < 3; i++ {
		h[i] = (MA[i*3]*d[0] + MA[i*3+1]*d[1] + MA[i*3+2]*d[2]) * o.Base / 2
	}
	return Rig{L: Lens{o.F, o.K}, R: R, H: h, Pan: pan, Tilt: tilt}
}

func inside(q *[2]float64) bool { return q[0] >= 0 && q[1] >= 0 && q[0] < LW-1 && q[1] < LH-1 }
func toA(g *Rig, r *[3]float64, w float64, q *[2]float64) bool {
	return project(g.L, r[0]+g.H[0]*w, r[1]+g.H[1]*w, r[2]+g.H[2]*w, q) && inside(q)
}
func toB(g *Rig, r *[3]float64, w float64, q *[2]float64) bool {
	x, y, z, R := r[0]-g.H[0]*w, r[1]-g.H[1]*w, r[2]-g.H[2]*w, &g.R
	return project(g.L, R[0]*x+R[1]*y+R[2]*z, R[3]*x+R[4]*y+R[5]*z, R[6]*x+R[7]*y+R[8]*z, q) && inside(q)
}

func bil(img []float32, w, h int, x, y float64) float64 {
	if !(x >= 0 && y >= 0 && x < float64(w-1) && y < float64(h-1)) {
		return NaN
	}
	x0, y0 := int(x), int(y)
	fx, fy := x-float64(x0), y-float64(y0)
	k := y0*w + x0
	return (float64(img[k])*(1-fx)+float64(img[k+1])*fx)*(1-fy) + (float64(img[k+w])*(1-fx)+float64(img[k+w+1])*fx)*fy
}

func shrink(img []float32, w, h, f int) []float32 {
	w2, h2 := w/f, h/f
	o := make([]float32, w2*h2)
	ff := float64(f * f)
	for y := 0; y < h2; y++ {
		for x := 0; x < w2; x++ {
			s := 0.0
			for j := 0; j < f; j++ {
				for i := 0; i < f; i++ {
					s += float64(img[(y*f+j)*w+x*f+i])
				}
			}
			o[y*w2+x] = float32(s / ff)
		}
	}
	return o
}

func gradMag(img []float32, w, h int) []float32 {
	o := make([]float32, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			k := y*w + x
			gx := float64(img[k+1]) - float64(img[k-1])
			gy := float64(img[k+w]) - float64(img[k-w])
			o[k] = float32(math.Sqrt(gx*gx + gy*gy))
		}
	}
	return o
}

func blur3(img []float32, w, h int) []float32 {
	o := make([]float32, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			s := 0.0
			for j := -1; j <= 1; j++ {
				for i := -1; i <= 1; i++ {
					s += float64(img[(y+j)*w+x+i])
				}
			}
			o[y*w+x] = float32(s / 9)
		}
	}
	return o
}

func ncc(I, J []float32, w, h, ax, ay, bx, by, P int) float64 {
	if ax < P || ay < P || bx < P || by < P || ax >= w-P || ay >= h-P || bx >= w-P || by >= h-P {
		return -2
	}
	sa, sb, saa, sbb, sab, n := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
	for j := -P; j <= P; j++ {
		for i := -P; i <= P; i++ {
			a, b := float64(I[(ay+j)*w+ax+i]), float64(J[(by+j)*w+bx+i])
			sa += a
			sb += b
			saa += a * a
			sbb += b * b
			sab += a * b
			n++
		}
	}
	va, vb := saa/n-(sa/n)*(sa/n), sbb/n-(sb/n)*(sb/n)
	if va > 4 && vb > 4 {
		return (sab/n - sa/n*sb/n) / math.Sqrt(va*vb)
	}
	return -1
}

// JavaScript Math.round (halves go up)
func jsRound(x float64) float64 {
	r := math.Floor(x)
	if x-r >= 0.5 {
		r++
	}
	return r
}

type nmRes struct {
	x []float64
	v float64
}

func cloneF(a []float64) []float64 { return append([]float64(nil), a...) }

func nelder(fn func([]float64) float64, x0, st []float64, iters int) nmRes {
	n := len(x0)
	pts := [][]float64{cloneF(x0)}
	for i := 0; i < n; i++ {
		p := cloneF(x0)
		p[i] += st[i]
		pts = append(pts, p)
	}
	vals := make([]float64, len(pts))
	for i, p := range pts {
		vals[i] = fn(p)
	}
	ord := make([]int, n+1)
	for it := 0; it < iters; it++ {
		for k := range ord {
			ord[k] = k
		}
		sort.SliceStable(ord, func(a, b int) bool { return vals[ord[a]] < vals[ord[b]] })
		np, nv := make([][]float64, n+1), make([]float64, n+1)
		for k, o := range ord {
			np[k] = pts[o]
			nv[k] = vals[o]
		}
		pts, vals = np, nv
		c := make([]float64, n)
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				c[j] += pts[i][j] / float64(n)
			}
		}
		at := func(t float64) []float64 {
			r := make([]float64, n)
			for k, v := range c {
				r[k] = v + t*(pts[n][k]-v)
			}
			return r
		}
		xr := at(-1)
		fr := fn(xr)
		if fr < vals[0] {
			xe := at(-2)
			fe := fn(xe)
			if fe < fr {
				pts[n], vals[n] = xe, fe
			} else {
				pts[n], vals[n] = xr, fr
			}
		} else if fr < vals[n-1] {
			pts[n], vals[n] = xr, fr
		} else {
			xc := at(0.5)
			fc := fn(xc)
			if fc < vals[n] {
				pts[n], vals[n] = xc, fc
			} else {
				for i := 1; i <= n; i++ {
					p := make([]float64, n)
					for k, v := range pts[i] {
						p[k] = pts[0][k] + 0.5*(v-pts[0][k])
					}
					pts[i] = p
					vals[i] = fn(pts[i])
				}
			}
		}
	}
	mn := math.Inf(1)
	for _, v := range vals {
		if v < mn || v != v {
			mn = v
		}
	}
	b := 0
	for i, v := range vals {
		if v == mn {
			b = i
			break
		}
	}
	return nmRes{pts[b], vals[b]}
}

type epiRes struct{ e, w, c float64 }

func epi(g *Rig, ax, ay, bx, by, lam float64) epiRes {
	var rb [3]float64
	var q [2]float64
	R := &g.R
	unproject(g.L, bx, by, &rb)
	Dpx := 2 * math.Sqrt(g.H[0]*g.H[0]+g.H[1]*g.H[1]+g.H[2]*g.H[2]) * g.L.F
	L2 := 0.0
	if lam != 0 {
		L2 = lam * Dpx * Dpx
	}
	d0 := R[0]*rb[0] + R[3]*rb[1] + R[6]*rb[2]
	d1 := R[1]*rb[0] + R[4]*rb[1] + R[7]*rb[2]
	d2 := R[2]*rb[0] + R[5]*rb[1] + R[8]*rb[2]
	b0, b1, b2 := 2*g.H[0], 2*g.H[1], 2*g.H[2]
	err := func(w float64) float64 {
		if project(g.L, b0*w+d0, b1*w+d1, b2*w+d2, &q) {
			return hypot(q[0]-ax, q[1]-ay)
		}
		return 1e4
	}
	obj := func(w float64) float64 { e := err(w); return e*e + L2*w*w }
	bw, bo := 0.0, obj(0)
	for i := 1; i <= 14; i++ {
		w := WMAX * float64(i) / 14
		ob := obj(w)
		if ob < bo {
			bo, bw = ob, w
		}
	}
	lo, hi := math.Max(0, bw-WMAX/14), math.Min(WMAX, bw+WMAX/14)
	for i := 0; i < 16; i++ {
		m1, m2 := lo+(hi-lo)*0.382, lo+(hi-lo)*0.618
		if obj(m1) < obj(m2) {
			hi = m2
		} else {
			lo = m1
		}
	}
	wm := (lo + hi) / 2
	if obj(wm) > bo {
		wm = bw
	}
	return epiRes{err(wm), wm, math.Sqrt(obj(wm))}
}

type cand struct{ v, pan, tilt float64 }

// the start of a fit: pan, tilt and maybe the lens (hasFK)
type winT struct {
	pan, tilt, F, K float64
	hasF, hasK      bool
}

type Matcher struct {
	A, B, A2, B2, A4, B4 []float32
	corners              [][3]float64
	Rs4                  int
}

func newMatcher(A, B []float32) *Matcher {
	resp := make([]float32, LW*LH)
	for y := 2; y < LH-2; y++ {
		for x := 2; x < LW-2; x++ {
			sxx, syy, sxy := 0.0, 0.0, 0.0
			for j := -1; j <= 1; j++ {
				for i := -1; i <= 1; i++ {
					kk := (y+j)*LW + x + i
					gx := float64(B[kk+1]) - float64(B[kk-1])
					gy := float64(B[kk+LW]) - float64(B[kk-LW])
					sxx += gx * gx
					syy += gy * gy
					sxy += gx * gy
				}
			}
			resp[y*LW+x] = float32(sxx*syy - sxy*sxy - 0.05*(sxx+syy)*(sxx+syy))
		}
	}
	m := &Matcher{A: A, B: B}
	mg := int(jsRound(float64(LH) * 0.05))
	for y := mg; y < LH-mg; y++ {
		for x := 12; x < LW-12; x++ {
			rv := resp[y*LW+x]
			if rv <= 0 {
				continue
			}
			top := true
			for j := -3; j <= 3 && top; j++ {
				for i := -3; i <= 3; i++ {
					if resp[(y+j)*LW+x+i] > rv {
						top = false
						break
					}
				}
			}
			if top {
				m.corners = append(m.corners, [3]float64{float64(x), float64(y), float64(rv)})
			}
		}
	}
	sort.SliceStable(m.corners, func(a, b int) bool { return m.corners[a][2] > m.corners[b][2] })
	m.A2, m.B2, m.A4, m.B4 = shrink(A, LW, LH, 2), shrink(B, LW, LH, 2), shrink(A, LW, LH, 4), shrink(B, LW, LH, 4)
	w2 := float64(LW / 2)
	Rs := jsRound(w2 * 0.09)
	m.Rs4 = int(jsRound(Rs / 2))
	return m
}

// correlation of I's patches with one fixed patch of J; nil when the patch is flat or off the picture
func nccFixed(I, J []float32, w, h, bx, by, P int) func(ax, ay int) float64 {
	if bx < P || by < P || bx >= w-P || by >= h-P {
		return nil
	}
	vals := make([]float64, 0, (2*P+1)*(2*P+1))
	s, ss := 0.0, 0.0
	for j := -P; j <= P; j++ {
		for i := -P; i <= P; i++ {
			v := float64(J[(by+j)*w+bx+i])
			vals = append(vals, v)
			s += v
			ss += v * v
		}
	}
	n := float64(len(vals))
	mb := s / n
	vb := ss/n - mb*mb
	if vb <= 4 {
		return nil
	}
	for k := range vals {
		vals[k] -= mb
	}
	return func(ax, ay int) float64 {
		if ax < P || ay < P || ax >= w-P || ay >= h-P {
			return -2
		}
		sa, saa, sab, m := 0.0, 0.0, 0.0, 0
		for j := -P; j <= P; j++ {
			row := (ay+j)*w + ax
			for i := -P; i <= P; i++ {
				a := float64(I[row+i])
				sa += a
				saa += a * a
				sab += a * vals[m]
				m++
			}
		}
		va := saa/n - (sa/n)*(sa/n)
		if va > 4 {
			return (sab / n) / math.Sqrt(va*vb)
		}
		return -1
	}
}

func iround(x float64) int { return int(jsRound(x)) }

func (M *Matcher) findMatches(g0 *Rig, maxC int) [][4]float64 {
	var cs [][5]float64
	var rb [3]float64
	var q [2]float64
	R0 := &g0.R
	for k := 0; k < len(M.corners) && len(cs) < maxC; k++ {
		c := M.corners[k]
		unproject(g0.L, c[0], c[1], &rb)
		if !project(g0.L, R0[0]*rb[0]+R0[3]*rb[1]+R0[6]*rb[2], R0[1]*rb[0]+R0[4]*rb[1]+R0[7]*rb[2], R0[2]*rb[0]+R0[5]*rb[1]+R0[8]*rb[2], &q) {
			continue
		}
		if q[0] < 20 || q[0] > LW-20 || q[1] < 20 || q[1] > LH-20 {
			continue
		}
		cs = append(cs, [5]float64{c[0], c[1], c[2], q[0], q[1]})
	}
	type one struct {
		m  [4]float64
		ok bool
	}
	res := make([]one, len(cs))
	parFor(len(cs), 8, func(lo, hi int) {
		for ci := lo; ci < hi; ci++ {
			res[ci].m, res[ci].ok = M.matchOne(cs[ci])
		}
	})
	var ms [][4]float64
	for _, r := range res {
		if r.ok {
			ms = append(ms, r.m)
		}
	}
	return ms
}

// one corner of B (x, y, response, predicted x, y in A): its place in A, if clearly found
func (M *Matcher) matchOne(c2 [5]float64) ([4]float64, bool) {
	w2, h2, w4, h4 := LW/2, LH/2, LW/4, LH/4
	Rs4 := M.Rs4
	{
		bst, second, bax, bay := -2.0, -2.0, 0, 0
		f4 := nccFixed(M.A4, M.B4, w4, h4, iround(c2[0]/4), iround(c2[1]/4), 4)
		if f4 != nil {
			px4, py4 := iround(c2[3]/4), iround(c2[4]/4)
			type t3 struct {
				v    float64
				x, y int
			}
			var tops4 []t3
			for yy := py4 - Rs4; yy <= py4+Rs4; yy++ {
				for xx := px4 - Rs4; xx <= px4+Rs4; xx++ {
					nv := f4(xx, yy)
					if nv < 0.4 {
						continue
					}
					if len(tops4) < 4 || nv > tops4[len(tops4)-1].v {
						near := -1
						for t := range tops4 {
							if iabs(tops4[t].x-xx)+iabs(tops4[t].y-yy) <= 2 {
								near = t
								break
							}
						}
						if near >= 0 {
							if nv > tops4[near].v {
								tops4[near] = t3{nv, xx, yy}
							}
						} else {
							tops4 = append(tops4, t3{nv, xx, yy})
						}
						sort.SliceStable(tops4, func(a, b int) bool { return tops4[a].v > tops4[b].v })
						if len(tops4) > 4 {
							tops4 = tops4[:4]
						}
					}
				}
			}
			var f2 func(int, int) float64
			if len(tops4) > 0 {
				f2 = nccFixed(M.A2, M.B2, w2, h2, iround(c2[0]/2), iround(c2[1]/2), 6)
			}
			if f2 != nil {
				for _, t := range tops4 {
					tb, tx, ty := -2.0, 0, 0
					for y5 := t.y*2 - 3; y5 <= t.y*2+3; y5++ {
						for x5 := t.x*2 - 3; x5 <= t.x*2+3; x5++ {
							v5 := f2(x5, y5)
							if v5 > tb {
								tb, tx, ty = v5, x5, y5
							}
						}
					}
					if tb > bst {
						if iabs(tx-bax)+iabs(ty-bay) > 2 {
							second = bst
						}
						bst, bax, bay = tb, tx, ty
					} else if tb > second && iabs(tx-bax)+iabs(ty-bay) > 2 {
						second = tb
					}
				}
			}
		}
		if bst >= 0.75 && bst-second >= 0.03 {
			fb, fx, fy := -2.0, 0, 0
			for yy := bay*2 - 3; yy <= bay*2+3; yy++ {
				for xx := bax*2 - 3; xx <= bax*2+3; xx++ {
					nv := ncc(M.A, M.B, LW, LH, xx, yy, int(c2[0]), int(c2[1]), 7)
					if nv > fb {
						fb, fx, fy = nv, xx, yy
					}
				}
			}
			if fb > 0.8 {
				return [4]float64{float64(fx), float64(fy), c2[0], c2[1]}, true
			}
		}
	}
	return [4]float64{}, false
}

func iabs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func sq(x float64) float64 { return x * x }

func toFixed(x float64, d int) string { return strconv.FormatFloat(x, 'f', d, 64) }

func sortedF(a []float64) []float64 { sort.Float64s(a); return a }

// Run: the whole calibration (Calib.run without the pictures for the page).
func Run(st *State) {
	o := st.o
	A, B := st.A, st.B
	D := 2 * piV / 360
	lam := o.Lam
	const f4 = 4
	w4, h4 := LW/f4, LH/f4
	type lvlT struct{ gA, gB []float32 }
	var lvl [2]lvlT
	for li, nb := range []int{6, 1} {
		a, b := gradMag(shrink(A, LW, LH, f4), w4, h4), gradMag(shrink(B, LW, LH, f4), w4, h4)
		for i := 0; i < nb; i++ {
			a, b = blur3(a, w4, h4), blur3(b, w4, h4)
		}
		lvl[li] = lvlT{a, b}
	}
	var rays [][3]float64
	var pix []int
	var r [3]float64
	var q [2]float64
	L0 := Lens{o.F, o.K}
	for y := 3; y < h4-3; y++ {
		for x := 3; x < w4-3; x++ {
			unproject(L0, float64(x*f4+2), float64(y*f4+2), &r)
			rays = append(rays, r)
			pix = append(pix, y*w4+x)
		}
	}
	corrOf := func(sa, sb, saa, sbb, sab, n float64) float64 {
		if n < 150 {
			return -1
		}
		va, vb := saa/n-(sa/n)*(sa/n), sbb/n-(sb/n)*(sb/n)
		if va > 0 && vb > 0 {
			return (sab/n - sa/n*sb/n) / math.Sqrt(va*vb) * math.Sqrt(n/400)
		}
		return -1
	}
	rough := func(pan, tilt float64, L int) float64 {
		g := makeRig(o.rp(), pan, tilt)
		R := &g.R
		ga, gb := lvl[L].gA, lvl[L].gB
		sa, sb, saa, sbb, sab, n := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
		for i := range rays {
			v := &rays[i]
			if !project(g.L, R[0]*v[0]+R[1]*v[1]+R[2]*v[2], R[3]*v[0]+R[4]*v[1]+R[5]*v[2], R[6]*v[0]+R[7]*v[1]+R[8]*v[2], &q) {
				continue
			}
			b := bil(gb, w4, h4, q[0]/f4-0.5, q[1]/f4-0.5)
			if b != b || q[0] < 12 || q[1] < 12 || q[0] > LW-12 || q[1] > LH-12 {
				continue
			}
			a := float64(ga[pix[i]])
			sa += a
			sb += b
			saa += a * a
			sbb += b * b
			sab += a * b
			n++
		}
		return corrOf(sa, sb, saa, sbb, sab, n)
	}
	var cands, tops []cand
	if o.HasStart {
		for pd2 := -10; pd2 <= 10; pd2++ {
			for td2 := -10; td2 <= 10; td2++ {
				pp, tt := o.StartPan+float64(pd2)*D, o.StartTilt+float64(td2)*D
				cands = append(cands, cand{rough(pp, tt, 0), pp, tt})
			}
		}
		sort.SliceStable(cands, func(a, b int) bool { return cands[a].v > cands[b].v })
		tops = append(tops, cand{0, o.StartPan, o.StartTilt})
		for _, c := range cands {
			if len(tops) < 5 && farAll(tops, c, 3*D) {
				tops = append(tops, c)
			}
		}
	} else {
		best := cand{-2, 0, 0}
		for pd := -100; pd <= 100; pd += 2 {
			for td := -36; td <= 36; td += 2 {
				v := rough(float64(pd)*D, float64(td)*D, 0)
				cands = append(cands, cand{v, float64(pd) * D, float64(td) * D})
				if v > best.v {
					best = cands[len(cands)-1]
				}
			}
		}
		sort.SliceStable(cands, func(a, b int) bool { return cands[a].v > cands[b].v })
		for _, c := range cands {
			if len(tops) < 10 && farAll(tops, c, 5*D) {
				tops = append(tops, c)
			}
		}
	}
	for ti0, c0 := range tops {
		b0 := cand{rough(c0.pan, c0.tilt, 1), c0.pan, c0.tilt}
		for _, s := range []float64{2, 1, 0.5, 0.25} {
			bb := b0
			for pi := -2; pi <= 2; pi++ {
				for ti := -2; ti <= 2; ti++ {
					pan, tilt := bb.pan+float64(pi)*s*D, bb.tilt+float64(ti)*s*D
					vv := rough(pan, tilt, 1)
					if vv > b0.v {
						b0 = cand{vv, pan, tilt}
					}
				}
			}
		}
		tops[ti0] = b0
	}
	sort.SliceStable(tops, func(a, b int) bool { return tops[a].v > tops[b].v })
	if !o.HasStart {
		var t2 []cand
		for _, t := range tops {
			if t.v >= 0.5 {
				t2 = append(t2, t)
			}
		}
		if len(t2) > 0 {
			tops = t2
		} else if len(tops) > 0 {
			st.Weak = append(st.Weak, "las dos lentes casi no se parecen")
			tops = tops[:1]
		}
	}
	if len(tops) == 0 {
		fail("no hay ninguna alineacion para probar")
	}

	// -- 2. shared corners --
	M := newMatcher(A, B)

	// -- 3. fine fit --
	rigOf := func(xv []float64) Rig {
		p := o.rp()
		if o.Joint {
			p.F, p.K = xv[2], xv[3]
		}
		return makeRig(p, xv[0], xv[1])
	}
	type fitRes struct {
		g    Rig
		x    []float64
		good int
		med  float64
	}
	fitRig := func(start winT, ms [][4]float64, iters int) fitRes {
		cost := func(xv []float64) float64 {
			gg := rigOf(xv)
			es := make([]float64, len(ms))
			parFor(len(ms), 16, func(lo, hi int) {
				for i := lo; i < hi; i++ {
					m := ms[i]
					es[i] = epi(&gg, m[0], m[1], m[2], m[3], lam).c
				}
			})
			sort.Float64s(es)
			use := int(math.Floor(float64(len(es)) * 0.9))
			if use < 6 {
				use = 6
			}
			e := 0.0
			for i2 := 0; i2 < use; i2++ {
				ei := esAt(es, i2)
				if ei < 2 {
					e += ei * ei
				} else {
					e += 4*ei - 4
				}
			}
			e /= float64(use)
			if o.Joint {
				e += sq((xv[2]-o.F)/8) + sq((xv[3]-o.K)/0.02)
			}
			return e
		}
		x0 := []float64{start.pan, start.tilt}
		st0 := []float64{1 * D, 0.5 * D}
		if o.Joint {
			F := o.F
			if start.hasF && start.F != 0 {
				F = start.F
			}
			K := o.K
			if start.hasK {
				K = start.K
			}
			x0 = append(x0, F, K)
			st0 = append(st0, 8, 0.02)
		}
		it := iters
		if o.Joint {
			it *= 2
		}
		f := nelder(cost, x0, st0, it)
		gg := rigOf(f.x)
		es := make([]float64, len(ms))
		for i, m := range ms {
			es[i] = epi(&gg, m[0], m[1], m[2], m[3], 0).e
		}
		sort.Float64s(es)
		good := 0
		for _, e := range es {
			if e < 2.5 {
				good++
			}
		}
		return fitRes{gg, f.x, good, esAt(es, len(es)>>1)}
	}
	rigX := func(x []float64) Rig {
		p := o.rp()
		p.F, p.K = x[2], x[3]
		p.Ax, p.Az = 0, 0
		if len(x) > 4 {
			p.Ax = x[4]
		}
		if len(x) > 5 {
			p.Az = x[5]
		}
		return makeRig(p, x[0], x[1])
	}
	rayCache := map[string][]float64{}
	var rayKeys []string
	raysFor := func(F, K float64) []float64 {
		key := toFixed(F, 1) + "/" + toFixed(K, 4)
		if rs, ok := rayCache[key]; ok {
			return rs
		}
		rs := make([]float64, 0, len(rays)*3)
		L0 := Lens{F, K}
		var rr [3]float64
		for yy := 3; yy < h4-3; yy++ {
			for xx := 3; xx < w4-3; xx++ {
				unproject(L0, float64(xx*f4+2), float64(yy*f4+2), &rr)
				rs = append(rs, rr[0], rr[1], rr[2])
			}
		}
		rayCache[key] = rs
		rayKeys = append(rayKeys, key)
		if len(rayKeys) > 40 {
			delete(rayCache, rayKeys[0])
			rayKeys = rayKeys[1:]
		}
		return rs
	}
	roughX := func(x []float64, L int) float64 {
		g := rigX(x)
		rs := raysFor(x[2], x[3])
		R := &g.R
		ga, gb := lvl[L].gA, lvl[L].gB
		sa, sb, saa, sbb, sab, n := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
		for i, m := 0, 0; i < len(rs); i, m = i+3, m+1 {
			if !project(g.L, R[0]*rs[i]+R[1]*rs[i+1]+R[2]*rs[i+2], R[3]*rs[i]+R[4]*rs[i+1]+R[5]*rs[i+2], R[6]*rs[i]+R[7]*rs[i+1]+R[8]*rs[i+2], &q) {
				continue
			}
			bv := bil(gb, w4, h4, q[0]/f4-0.5, q[1]/f4-0.5)
			if bv != bv || q[0] < 12 || q[1] < 12 || q[0] > LW-12 || q[1] > LH-12 {
				continue
			}
			av := float64(ga[pix[m]])
			sa += av
			sb += bv
			saa += av * av
			sbb += bv * bv
			sab += av * bv
			n++
		}
		return corrOf(sa, sb, saa, sbb, sab, n)
	}
	p0 := tops[0]
	if o.HasStart {
		p0 = cand{0, o.StartPan, o.StartTilt}
	}
	cur := []float64{p0.pan, p0.tilt, o.F, o.K}
	// 1. spread tries
	curV := roughX(cur, 0)
	halton := func(i int, b int) float64 {
		f, r := 1.0, 0.0
		for i > 0 {
			f /= float64(b)
			r += f * float64(i%b)
			i = i / b
		}
		return r
	}
	var seeds [][]float64
	for i := 0; i < len(tops) && i < 6; i++ {
		seeds = append(seeds, []float64{tops[i].pan, tops[i].tilt, o.F, o.K})
	}
	for hi := 1; hi <= 70; hi++ {
		seeds = append(seeds, []float64{p0.pan + (2*halton(hi, 2)-1)*8*D, p0.tilt + (2*halton(hi, 3)-1)*5*D, o.F * (1 + (2*halton(hi, 5)-1)*0.04), o.K + (2*halton(hi, 7)-1)*0.06})
	}
	for _, x := range seeds {
		v := roughX(x, 0)
		if v > curV {
			cur, curV = x, v
		}
	}
	// 2. descent on the sharper pictures
	curV = roughX(cur, 1)
	for _, stp := range [][4]float64{{1, 0.6, 8, 0.02}, {0.5, 0.3, 4, 0.01}, {0.25, 0.15, 2, 0.005}, {0.12, 0.08, 1, 0.0025}} {
		for it := 0; it < 30; it++ {
			improved := false
			for pi := 0; pi < 4; pi++ {
				for sg := -1; sg <= 1; sg += 2 {
					y2 := cloneF(cur)
					m := 1.0
					if pi < 2 {
						m = D
					}
					y2[pi] += float64(sg) * stp[pi] * m
					v2 := roughX(y2, 1)
					if v2 > curV {
						cur, curV, improved = y2, v2, true
					}
				}
			}
			if !improved {
				break
			}
		}
	}
	// 3. shared pairs
	g0 := rigX(cur)
	win := winT{pan: cur[0], tilt: cur[1], F: cur[2], K: cur[3], hasF: true, hasK: true}
	matches := M.findMatches(&g0, 800)
	fr2 := fitRes{}
	if len(matches) >= 8 {
		fr2 = fitRig(win, matches, 80)
	}
	if fr2.good < 15 {
		for i := 0; i < len(tops) && i < 8; i++ {
			t := tops[i]
			gt := makeRig(o.rp(), t.pan, t.tilt)
			ms := M.findMatches(&gt, 250)
			f := fitRes{}
			tw := winT{pan: t.pan, tilt: t.tilt}
			if len(ms) >= 8 {
				f = fitRig(tw, ms, 80)
			}
			if f.good > fr2.good {
				fr2, win = f, tw
			}
		}
		if fr2.good == 0 {
			st.Weak = append(st.Weak, "no encontre puntos en comun entre las dos lentes")
		} else {
			g0 = makeRig(o.rp(), win.pan, win.tilt)
			matches = M.findMatches(&g0, 800)
		}
		if len(matches) < 8 {
			st.Weak = append(st.Weak, fmt.Sprintf("muy pocos puntos en comun (%d)", len(matches)))
		}
	}
	// 4. fine fit on the pairs, in rounds
	pairCost := func(x []float64, ms [][4]float64) float64 {
		gg := rigX(x)
		es := make([]float64, len(ms))
		parFor(len(ms), 16, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				m := ms[i]
				es[i] = epi(&gg, m[0], m[1], m[2], m[3], lam).c
			}
		})
		sort.Float64s(es)
		// with few pairs use all of them; with none the lens priors alone hold the fit
		use := int(math.Floor(float64(len(es)) * 0.9))
		if use < 6 {
			use = 6
		}
		if use > len(es) {
			use = len(es)
		}
		e := 0.0
		for i2 := 0; i2 < use; i2++ {
			ei := esAt(es, i2)
			if ei < 2 {
				e += ei * ei
			} else {
				e += 4*ei - 4
			}
		}
		pf := o.Loose
		x4, x5 := 0.0, 0.0
		if len(x) > 4 {
			x4 = x[4]
		}
		if len(x) > 5 {
			x5 = x[5]
		}
		pos := 0.0
		if o.Pos {
			pos = sq(x4/(0.15*pf)) + sq(x5/(0.15*pf))
		}
		if use > 0 {
			e /= float64(use)
		}
		return e + sq((x[2]-o.F)/(20*pf)) + sq((x[3]-o.K)/(0.05*pf)) + pos
	}
	part := func(x []float64, ms [][4]float64, which string) nmRes {
		var idx [2]int
		var stp []float64
		switch which {
		case "dir":
			idx, stp = [2]int{0, 1}, []float64{0.5 * D, 0.3 * D}
		case "lens":
			idx, stp = [2]int{2, 3}, []float64{6, 0.02}
		default:
			idx, stp = [2]int{4, 5}, []float64{0.05, 0.05}
		}
		f := nelder(func(v []float64) float64 {
			y := cloneF(x)
			y[idx[0]], y[idx[1]] = v[0], v[1]
			return pairCost(y, ms)
		}, []float64{x[idx[0]], x[idx[1]]}, stp, 60)
		y2 := cloneF(x)
		y2[idx[0]], y2[idx[1]] = f.x[0], f.x[1]
		return nmRes{y2, f.v}
	}
	xF := o.F
	if win.hasF && win.F != 0 {
		xF = win.F
	}
	xK := o.K
	if win.hasK {
		xK = win.K
	}
	xb := []float64{win.pan, win.tilt, xF, xK, 0, 0}
	vb := pairCost(xb, matches)
	for round := 1; round <= 5; round++ {
		if round > 1 {
			gx := rigX(xb)
			ms2 := M.findMatches(&gx, 800)
			if len(ms2) >= 8 {
				matches = ms2
				vb = pairCost(xb, matches)
			}
		}
		before := vb
		if pd := part(xb, matches, "dir"); pd.v < vb {
			xb, vb = pd.x, pd.v
		}
		if pl := part(xb, matches, "lens"); pl.v < vb {
			xb, vb = pl.x, pl.v
		}
		if o.Pos {
			if pp := part(xb, matches, "pos"); pp.v < vb {
				xb, vb = pp.x, pp.v
			}
		}
		if round > 1 && vb > before*0.99 {
			break
		}
	}
	if o.Joint2 {
		st.rig = rigX(xb)
		cylinder(st)
		st.rowKnots = nil
		msJ := matches
		xb = jointDescent(st, xb, rigX, D, func(v []float64) float64 { return pairCost(v[:6], msJ) })
	}
	gF := rigX(xb)
	esF := make([]float64, len(matches))
	for i, m := range matches {
		esF[i] = epi(&gF, m[0], m[1], m[2], m[3], 0).e
	}
	sort.Float64s(esF)
	st.FitX = xb
	good := 0
	for _, e := range esF {
		if e < 2.5 {
			good++
		}
	}
	med := 99.0
	if len(esF) > 0 {
		med = esF[len(esF)>>1]
	}
	st.rig = gF
	st.MatchErr = med
	st.Matches = matches
	cylinder(st)
	if good < 15 || med > 4 {
		st.Weak = append(st.Weak, fmt.Sprintf("las lentes encajan mal (error tipico %.1f px, %d puntos buenos)", med, good))
	}
	// -- 4. distances --
	cylinder(st)
	seam(st, false)
	if stripMatch(st, true) < stripMatch(st, false) {
		seam(st, true)
	}
	if o.Warp {
		seamWarp(st)
	}
}

func esAt(es []float64, i int) float64 {
	if i < len(es) {
		return es[i]
	}
	return NaN
}

func farAll(tops []cand, c cand, lim float64) bool {
	for _, t := range tops {
		if !(math.Abs(t.pan-c.pan) > lim || math.Abs(t.tilt-c.tilt) > lim) {
			return false
		}
	}
	return true
}

// Cylinder around A's view: angle a0..a1 (both lenses), heights (any picture at all). Canvas CW wide.
func cylinder(st *State) {
	g := &st.rig
	var r [3]float64
	var angs []float64
	R := &g.R
	for _, x := range []float64{0, LW - 1} {
		unproject(g.L, x, LH/2, &r)
		angs = append(angs, atan2(r[0], r[2]))
		d := [3]float64{R[0]*r[0] + R[3]*r[1] + R[6]*r[2], R[1]*r[0] + R[4]*r[1] + R[7]*r[2], R[2]*r[0] + R[5]*r[1] + R[8]*r[2]}
		angs = append(angs, atan2(d[0], d[2]))
	}
	sort.Float64s(angs)
	c := Cyl{A0: angs[0], A1: angs[3], CW: st.o.OutW}
	c.Fc = float64(c.CW) / (c.A1 - c.A0)
	hs := float64(LH) / 2 / g.L.F * 1.8
	c.H0 = -hs
	c.CH = int(jsRound(2 * hs * c.Fc))
	st.cyl = c
	st.hasCyl = true
}

func cylRay(c *Cyl, x, y float64, out *[3]float64) {
	ang := c.A0 + (x+0.5)/c.Fc
	hg := c.H0 + (y+0.5)/c.Fc
	n := math.Sqrt(1 + hg*hg)
	out[0] = sin(ang) / n
	out[1] = hg / n
	out[2] = cos(ang) / n
}

func rowW(st *State, y float64) float64 {
	k := st.rowKnots
	n := len(k)
	f := math.Max(0, math.Min(float64(n)-1.0001, y/float64(st.cyl.CH-1)*float64(n-1)))
	i := int(f)
	return k[i] + (k[i+1]-k[i])*(f-float64(i))
}

// OSDBox: where the camera draws its clock on lens A (fractions of the lens picture: x0, y0, x1, y1); the seam
// never cuts it. See seam().
var OSDBox = [4]float64{0.60, 0.035, 1.0, 0.125}

// OSDBoxB: where the camera draws its name ("Device1") on lens B, bottom left. Measured: sub stream 0-0.10 x
// 0.93-0.99, main (HD) stream 0.013-0.07 x 0.93-0.95; their union with a margin. The seam never shows it.
var OSDBoxB = [4]float64{0, 0.90, 0.12, 1.0}

func seam(st *State, flat bool) {
	c, g := &st.cyl, &st.rig
	const S = 2
	w, h := c.CW/S, c.CH/S
	KW := c.Fc / S * math.Sqrt(4*(g.H[0]*g.H[0]+g.H[1]*g.H[1]+g.H[2]*g.H[2]))
	const BAND = 8
	nb := (h + BAND - 1) / BAND
	hw := make([]float32, h)
	bandW := make([]float64, nb)
	for i := range bandW {
		bandW[i] = NaN
	}
	var rq [3]float64
	var qa0, qb0 [2]float64
	wLo, wHi, wStep := -10/KW, math.Min(WMAX, 70/KW), 0.5/KW
	for bi := 0; bi < nb && !flat && st.rowKnots == nil; bi++ {
		var rays2 [][3]float64
		var av2 []float64
		for y := bi * BAND; y < min(h, (bi+1)*BAND); y++ {
			for x := 0; x < w; x += 2 {
				cylRay(c, float64(x*S+S/2), float64(y*S+S/2), &rq)
				if !toA(g, &rq, 0, &qa0) || !toB(g, &rq, 0, &qb0) {
					continue
				}
				rays2 = append(rays2, rq)
				av2 = append(av2, bil(st.A, LW, LH, qa0[0], qa0[1]))
			}
		}
		if len(rays2) < 150 {
			continue
		}
		type sc2 struct{ w, s float64 }
		var scores []sc2
		bestS := -2.0
		for wv := wLo; wv <= wHi; wv += wStep {
			sa, sb, saa, sbb, sab, n := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
			for i := range rays2 {
				if !toB(g, &rays2[i], 2*wv, &qb0) {
					continue
				}
				bv := bil(st.B, LW, LH, qb0[0], qb0[1])
				if bv != bv {
					continue
				}
				a3 := av2[i]
				sa += a3
				sb += bv
				saa += a3 * a3
				sbb += bv * bv
				sab += a3 * bv
				n++
			}
			sc := -2.0
			if n > float64(len(rays2))*0.6 {
				va, vb := saa/n-(sa/n)*(sa/n), sbb/n-(sb/n)*(sb/n)
				if va > 1 && vb > 1 {
					sc = (sab/n - sa/n*sb/n) / math.Sqrt(va*vb)
				}
			}
			scores = append(scores, sc2{wv, sc})
			if sc > bestS {
				bestS = sc
			}
		}
		var pick *sc2
		for i := range scores {
			if scores[i].s >= bestS-0.01 && (pick == nil || math.Abs(scores[i].w) < math.Abs(pick.w)) {
				pick = &scores[i]
			}
		}
		if pick != nil && bestS > 0.3 {
			bandW[bi] = pick.w
		}
	}
	for y := 0; y < h; y++ {
		bb0 := y / BAND
		best, bd := NaN, 1e9
		for bi := 0; bi < nb; bi++ {
			if bandW[bi] == bandW[bi] && math.Abs(float64(bi-bb0)) < bd {
				bd = math.Abs(float64(bi - bb0))
				best = bandW[bi]
			}
		}
		if best == best {
			hw[y] = float32(best)
		} else {
			hw[y] = 0
		}
	}
	LIM := st.o.RowLim
	if st.rowKnots != nil && !flat {
		hwK := make([]float32, h)
		for y := 0; y < h; y++ {
			hwK[y] = float32(rowW(st, float64(y*S+S/2)))
		}
		hw = hwK
	}
	hs := make([]float32, h)
	if st.rowKnots != nil && !flat {
		hs = hw
	} else {
		for y := 0; y < h; y++ {
			var nn []float64
			for j := max(0, y-4); j <= min(h-1, y+4); j++ {
				nn = append(nn, float64(hw[j]))
			}
			sort.Float64s(nn)
			hs[y] = float32(nn[len(nn)>>1])
		}
	}
	SM := 0.0
	if st.rowKnots == nil {
		SM = 40
		if st.o.RowSmooth != nil {
			SM = *st.o.RowSmooth
		}
	}
	if SM > 0 {
		hs2 := make([]float32, h)
		for y := 0; y < h; y++ {
			sw, sv := 0.0, 0.0
			fy := float64(y)
			for j := math.Max(0, fy-2*SM); j <= math.Min(float64(h-1), fy+2*SM); j++ {
				wg := exp(-0.5 * sq((j-fy)/math.Max(1, SM)))
				sw += wg
				hj := NaN
				if j == math.Floor(j) {
					hj = float64(hs[int(j)])
				}
				sv += wg * hj
			}
			hs2[y] = float32(sv / sw)
		}
		hs = hs2
	}
	limBase := LIM
	if st.rowKnots != nil {
		limBase = 100
	}
	lim := limBase / KW
	hwA := append([]float32(nil), hs...)
	for itp := 0; itp < 200; itp++ {
		for y := 0; y < h; y++ {
			hwA[y] = float32(0.7*float64(hwA[y]) + 0.3*float64(hs[y]))
		}
		for y := 1; y < h; y++ {
			hwA[y] = float32(math.Max(float64(hwA[y-1])-lim, math.Min(float64(hwA[y-1])+lim, float64(hwA[y]))))
		}
		for y := h - 2; y >= 0; y-- {
			hwA[y] = float32(math.Max(float64(hwA[y+1])-lim, math.Min(float64(hwA[y+1])+lim, float64(hwA[y]))))
		}
	}
	// both lenses exactly as they will be drawn
	la, lb, ok := make([]float32, w*h), make([]float32, w*h), make([]bool, w*h)
	var r [3]float64
	var qa, qb [2]float64
	// which lens is left: where only A shows, on average (does not depend on the seam)
	sA, nA := 0.0, 0
	for y := 0; y < c.CH; y += 8 {
		for x := 0; x < c.CW; x += 8 {
			cylRay(c, float64(x), float64(y), &r)
			if toA(g, &r, 0, &qa) && !toB(g, &r, 0, &qb) {
				sA += float64(x)
				nA++
			}
		}
	}
	st.aLeft = nA == 0 || sA/float64(nA) < float64(c.CW)/2
	// the camera's clock (top right of lens A, drawn by the camera) is never cut by the seam: every point of lens
	// A in OSDBox (lens A fractions x0, y0, x1, y1) stays on A's side, osdGap cells away (details blend over +-3 px).
	// Measured on this camera: sub stream 0.62-0.98 x 0.05-0.11, main (HD) stream 0.80-0.995 x 0.06-0.09; the box
	// is their union with a margin, so one rule holds in both qualities. (Same as web/calib.js.)
	osd, osdB := OSDBox, OSDBoxB
	const osdGap = 4
	segLo, segHi := make([]int, h), make([]int, h)
	for y := range segHi {
		segHi[y] = w - 1
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			k := y*w + x
			cylRay(c, float64(x*S+S/2), float64(y*S+S/2), &r)
			if toA(g, &r, 0, &qa) && toB(g, &r, 2*float64(hwA[y]), &qb) {
				ok[k] = true
				la[k] = float32(bil(st.A, LW, LH, qa[0], qa[1]))
				lb[k] = float32(bil(st.B, LW, LH, qb[0], qb[1]))
				// lens B's own label is never shown either: those points stay on A's side
				if qb[0] >= osdB[0]*LW && qb[0] <= osdB[2]*LW && qb[1] >= osdB[1]*LH && qb[1] <= osdB[3]*LH {
					if st.aLeft {
						segLo[y] = max(segLo[y], x+osdGap)
					} else {
						segHi[y] = min(segHi[y], x-osdGap)
					}
				}
			}
			if toA(g, &r, 0, &qa) && qa[0] >= osd[0]*LW && qa[0] <= osd[2]*LW && qa[1] >= osd[1]*LH && qa[1] <= osd[3]*LH {
				if st.aLeft {
					segLo[y] = max(segLo[y], x+osdGap)
				} else {
					segHi[y] = min(segHi[y], x-osdGap)
				}
			}
		}
	}
	// a label's edge can fall between two rows of this half-size grid: each limit also holds one row above and below
	lo0, hi0 := append([]int(nil), segLo...), append([]int(nil), segHi...)
	for y := 0; y < h; y++ {
		for j := max(0, y-1); j <= min(h-1, y+1); j++ {
			segLo[y] = max(segLo[y], lo0[j])
			segHi[y] = min(segHi[y], hi0[j])
		}
	}
	// a row with no room left: the seam goes to the last place it can (the far end of the shared part)
	for y := 0; y < h; y++ {
		xl, xr := -1, -1
		for x := 1; x < w-1; x++ {
			if ok[y*w+x] && ok[y*w+x-1] && ok[y*w+x+1] {
				if xl < 0 {
					xl = x
				}
				xr = x
			}
		}
		if xr >= 0 && segLo[y] > xr {
			segLo[y] = xr
		}
		if xl >= 0 && segHi[y] < xl {
			segHi[y] = xl
		}
	}
	// The path goes down one row at a time, usually at most one cell sideways per row, but up to move cells at a
	// cost: the shared part can narrow fast near the bottom and the clock can push the seam to one end, and the path
	// must follow it instead of losing its way. (Same as web/calib.js.)
	const move = 12
	ea, eb := gradMag(la, w, h), gradMag(lb, w, h)
	INF := 1e18
	prev := make([]float64, w)
	for i := range prev {
		prev[i] = INF
	}
	from := make([]int32, w*h)
	for i := range from {
		from[i] = -1
	}
	rowOk := make([]bool, h)
	for y := 0; y < h; y++ {
		cur := make([]float64, w)
		for i := range cur {
			cur[i] = INF
		}
		// the best end so far (to go on from, when no cell of the last row is near enough)
		pbv, pbx := INF, -1
		for x := 0; x < w; x++ {
			if prev[x] < pbv {
				pbv, pbx = prev[x], x
			}
		}
		for x := 1; x < w-1; x++ {
			k2 := y*w + x
			if !ok[k2] || !ok[k2-1] || !ok[k2+1] || x < segLo[y] || x > segHi[y] {
				continue
			}
			Dv := math.Abs(float64(ea[k2])-float64(eb[k2])) + 0.15*math.Abs(float64(la[k2])-float64(lb[k2])) + 0.5
			if pbx < 0 { // the first row with room: the path starts here
				cur[x] = Dv
				continue
			}
			bv, bx := INF, -1
			for d := -move; d <= move; d++ {
				xx := x + d
				if xx < 0 || xx >= w || prev[xx] >= INF {
					continue
				}
				v := prev[xx]
				if d < -1 || d > 1 {
					v += 4 * (math.Abs(float64(d)) - 1)
				}
				if v < bv {
					bv, bx = v, xx
				}
			}
			if bx < 0 { // nothing near: go on from the best end of the last row
				bv, bx = pbv+50, pbx
			}
			cur[x] = bv + Dv
			from[k2] = int32(bx)
		}
		for x := 0; x < w; x++ {
			if cur[x] < INF {
				rowOk[y] = true
				break
			}
		}
		if !rowOk[y] { // a row with no room: the path keeps its place
			for x := 0; x < w; x++ {
				cur[x] = prev[x]
				if prev[x] < INF {
					from[y*w+x] = int32(x)
				}
			}
		}
		prev = cur
	}
	bx2, bvv := -1, INF
	for x := 0; x < w; x++ {
		if prev[x] < bvv {
			bvv, bx2 = prev[x], x
		}
	}
	seamC, Ws, hx := make([]float32, c.CH), make([]float32, c.CH), make([]float32, h)
	if bx2 >= 0 { // back up the path; above where it started there is no shared part (no seam there)
		for y := h - 1; y >= 0; y-- {
			hx[y] = float32(bx2)
			if bx2 >= 0 {
				bx2 = int(from[y*w+bx2])
			}
		}
	} else {
		for i := range hx {
			hx[i] = -1
		}
	}
	for y := 0; y < c.CH; y++ {
		yh := min(h-1, y/S)
		if hx[yh] < 0 {
			seamC[y] = -1
		} else {
			seamC[y] = hx[yh]*S + S/2
		}
		Ws[y] = hwA[yh]
	}
	st.ws = &WS{Seam: seamC, W: Ws}
}

func toBrow(st *State, r *[3]float64, cy float64, q *[2]float64) bool {
	w := 0.0
	if st.ws != nil {
		W := st.ws.W
		w = float64(W[int(math.Max(0, math.Min(float64(len(W)-1), jsRound(cy))))])
	}
	return toB(&st.rig, r, 2*w, q)
}

func jointDescent(st *State, x0 []float64, rigX func([]float64) Rig, D float64, pairs func([]float64) float64) []float64 {
	c := &st.cyl
	g0 := rigX(x0)
	var r [3]float64
	var qa, qb [2]float64
	var pts []float64
	for y := 0; y < c.CH; y += 4 {
		for x := 0; x < c.CW; x += 4 {
			cylRay(c, float64(x), float64(y), &r)
			if toA(&g0, &r, 0, &qa) && toB(&g0, &r, 0, &qb) {
				pts = append(pts, r[0], r[1], r[2], float64(y))
			}
		}
	}
	const NK = 8
	KWf := c.Fc * 2 * math.Sqrt(g0.H[0]*g0.H[0]+g0.H[1]*g0.H[1]+g0.H[2]*g0.H[2])
	o := st.o
	LAMC := 0.0004
	if o.CurveCost != nil {
		LAMC = *o.CurveCost
	}
	pairW := 0.01
	if o.PairW != nil {
		pairW = *o.PairW
	}
	CH1 := float64(c.CH - 1)
	np := len(pts) / 4
	pa, pb, pv := make([]float64, np), make([]float64, np), make([]bool, np)
	score := func(v []float64) float64 {
		g := rigX(v)
		parFor(np, 512, func(lo, hi int) {
			var rr [3]float64
			var qa, qb [2]float64
			for p := lo; p < hi; p++ {
				i := p * 4
				pv[p] = false
				rr[0], rr[1], rr[2] = pts[i], pts[i+1], pts[i+2]
				yy := pts[i+3]
				if !toA(&g, &rr, 0, &qa) {
					continue
				}
				f := yy / CH1 * (NK - 1)
				k := min(NK-2, int(f))
				w := v[6+k] + (v[7+k]-v[6+k])*(f-float64(k))
				if !toB(&g, &rr, 2*w, &qb) {
					continue
				}
				a, b := bil(st.A, LW, LH, qa[0], qa[1]), bil(st.B, LW, LH, qb[0], qb[1])
				if a != a || b != b {
					continue
				}
				pa[p], pb[p], pv[p] = a, b, true
			}
		})
		sa, sb, saa, sbb, sab, n := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
		for p := 0; p < np; p++ {
			if !pv[p] {
				continue
			}
			a, b := pa[p], pb[p]
			sa += a
			sb += b
			saa += a * a
			sbb += b * b
			sab += a * b
			n++
		}
		if n < 500 {
			return -2
		}
		m := (sab/n - sa/n*sb/n) / math.Sqrt((saa/n-(sa/n)*(sa/n))*(sbb/n-(sb/n)*(sb/n)))
		bend := 0.0
		for j := 1; j < NK-1; j++ {
			d2 := (v[6+j-1] - 2*v[6+j] + v[6+j+1]) * KWf
			bend += d2 * d2
		}
		pf := o.Loose
		prior := sq((v[2]-o.F)/(20*pf)) + sq((v[3]-o.K)/(0.05*pf)) + sq(v[4]/(0.15*pf)) + sq(v[5]/(0.15*pf))
		pc := 0.0
		if pairs != nil {
			pc = pairs(v)
		}
		return m - LAMC*bend - 0.002*prior - pairW*pc
	}
	v := cloneF(x0[:6])
	for k := 0; k < NK; k++ {
		v = append(v, 0)
	}
	best := score(v)
	step0 := []float64{0.15 * D, 0.1 * D, 3, 0.008, 0.02, 0.02}
	for k := 0; k < NK; k++ {
		step0 = append(step0, 3/KWf)
	}
	for _, sc := range []float64{1, 0.5, 0.25, 0.12} {
		for pass := 0; pass < 8; pass++ {
			improved := false
			for p := 0; p < len(v); p++ {
				for sg := -1; sg <= 1; sg += 2 {
					t := cloneF(v)
					t[p] += float64(sg) * step0[p] * sc
					sv := score(t)
					if sv > best {
						best, v, improved = sv, t, true
					}
				}
			}
			if !improved {
				break
			}
		}
	}
	st.rowKnots = cloneF(v[6:])
	return cloneF(v[:6])
}

// -- 6b. small bending near the seam (does not change the strip match; kept so the run does the same work) --
const HB = 32

type Warp struct {
	X0, NX, NY, Step int
	Dx, Dy           []float32
	Measured         int
}

func seamWarp(st *State) {
	c, ws := &st.cyl, st.ws
	const R, P, step = 8, 4, 6
	var r [3]float64
	var q [2]float64
	st.warp = nil
	if ws == nil {
		return
	}
	lo, hi := 1e9, -1.0
	for y := 0; y < c.CH; y++ {
		if ws.Seam[y] >= 0 {
			lo = math.Min(lo, float64(ws.Seam[y]))
			hi = math.Max(hi, float64(ws.Seam[y]))
		}
	}
	if hi < 0 {
		return
	}
	X0 := int(math.Max(0, math.Floor(lo-HB-R-P-2)))
	X1 := int(math.Min(float64(c.CW-1), math.Ceil(hi+HB+R+P+2)))
	BW, H := X1-X0+1, c.CH
	A, B := make([]float32, BW*H), make([]float32, BW*H)
	nan32 := float32(NaN)
	for i := range A {
		A[i], B[i] = nan32, nan32
	}
	for y := 0; y < H; y++ {
		sy := float64(ws.Seam[y])
		if sy < 0 {
			continue
		}
		for x := int(math.Max(float64(X0), math.Floor(sy-HB-R-P-1))); x <= int(math.Min(float64(X1), math.Ceil(sy+HB+R+P+1))); x++ {
			cylRay(c, float64(x), float64(y), &r)
			k := y*BW + x - X0
			if toA(&st.rig, &r, 0, &q) {
				A[k] = float32(bil(st.A, LW, LH, q[0], q[1]))
			}
			if toBrow(st, &r, float64(y), &q) {
				B[k] = float32(bil(st.B, LW, LH, q[0], q[1]))
			}
		}
	}
	NX, NY := BW/step+1, H/step+1
	N := NX * NY
	mdx, mdy := make([]float32, N), make([]float32, N)
	has, out := make([]bool, N), make([]bool, N)
	pa := make([]float64, 0, (2*P+1)*(2*P+1))
	for gy := 0; gy < NY; gy++ {
		for gx := 0; gx < NX; gx++ {
			cx, cy, gi := X0+gx*step, gy*step, gy*NX+gx
			sc := float64(ws.Seam[min(H-1, cy)])
			if sc < 0 || math.Abs(float64(cx)-sc) > HB {
				out[gi] = true
				continue
			}
			if cy < P+R || cy >= H-P-R || cx-X0 < P+R || X1-cx < P+R {
				continue
			}
			pa = pa[:0]
			sa, saa, okp := 0.0, 0.0, true
			for j := -P; j <= P && okp; j++ {
				for i := -P; i <= P; i++ {
					av := float64(A[(cy+j)*BW+cx-X0+i])
					if av != av {
						okp = false
						break
					}
					pa = append(pa, av)
					sa += av
					saa += av * av
				}
			}
			if !okp {
				continue
			}
			n := float64(len(pa))
			va := saa/n - (sa/n)*(sa/n)
			if va < 25 {
				continue
			}
			best, second, bx, by := -2.0, -2.0, 0, 0
			for dy := -R; dy <= R; dy++ {
				for dx := -R; dx <= R; dx++ {
					sb, sbb, sab, m, okb := 0.0, 0.0, 0.0, 0, true
					for j := -P; j <= P && okb; j++ {
						for i := -P; i <= P; i++ {
							bv := float64(B[(cy+dy+j)*BW+cx+dx-X0+i])
							if bv != bv {
								okb = false
								break
							}
							aa := pa[m]
							m++
							sb += bv
							sbb += bv * bv
							sab += aa * bv
						}
					}
					if !okb {
						continue
					}
					vb := sbb/n - (sb/n)*(sb/n)
					if vb < 1 {
						continue
					}
					v := (sab/n - sa/n*sb/n) / math.Sqrt(va*vb)
					if v > best {
						if iabs(dx-bx)+iabs(dy-by) > 2 {
							second = best
						}
						best, bx, by = v, dx, dy
					} else if v > second && iabs(dx-bx)+iabs(dy-by) > 2 {
						second = v
					}
				}
			}
			if best > 0.75 && best-second > 0.02 && iabs(bx) < R && iabs(by) < R {
				mdx[gi], mdy[gi], has[gi] = float32(bx), float32(by), true
			}
		}
	}
	keep := append([]bool(nil), has...)
	for gy := 0; gy < NY; gy++ {
		for gx := 0; gx < NX; gx++ {
			gi := gy*NX + gx
			if !has[gi] {
				continue
			}
			var lx, ly []float64
			for j := -2; j <= 2; j++ {
				for i := -2; i <= 2; i++ {
					xx, yy := gx+i, gy+j
					if xx < 0 || yy < 0 || xx >= NX || yy >= NY || !has[yy*NX+xx] {
						continue
					}
					lx = append(lx, float64(mdx[yy*NX+xx]))
					ly = append(ly, float64(mdy[yy*NX+xx]))
				}
			}
			if len(lx) < 4 {
				keep[gi] = false
				continue
			}
			sort.Float64s(lx)
			sort.Float64s(ly)
			if math.Abs(float64(mdx[gi])-lx[len(lx)>>1]) > 2.5 || math.Abs(float64(mdy[gi])-ly[len(ly)>>1]) > 2.5 {
				keep[gi] = false
			}
		}
	}
	fx, fy := make([]float32, N), make([]float32, N)
	for it := 0; it < 600; it++ {
		for gy := 0; gy < NY; gy++ {
			for gx := 0; gx < NX; gx++ {
				gi := gy*NX + gx
				if out[gi] {
					fx[gi], fy[gi] = 0, 0
					continue
				}
				s1, s2, cn := 0.0, 0.0, 0.0
				if gx > 0 {
					s1 += float64(fx[gi-1])
					s2 += float64(fy[gi-1])
					cn++
				}
				if gx < NX-1 {
					s1 += float64(fx[gi+1])
					s2 += float64(fy[gi+1])
					cn++
				}
				if gy > 0 {
					s1 += float64(fx[gi-NX])
					s2 += float64(fy[gi-NX])
					cn++
				}
				if gy < NY-1 {
					s1 += float64(fx[gi+NX])
					s2 += float64(fy[gi+NX])
					cn++
				}
				if keep[gi] {
					s1 += cn * float64(mdx[gi])
					s2 += cn * float64(mdy[gi])
					cn *= 2
				}
				fx[gi], fy[gi] = float32(s1/cn), float32(s2/cn)
			}
		}
	}
	lim := step * 0.25
	for pass := 0; pass < 50; pass++ {
		chg := false
		for gy := 0; gy < NY; gy++ {
			for gx := 0; gx < NX; gx++ {
				gi := gy*NX + gx
				for _, h2 := range [2]int{func() int {
					if gx+1 < NX {
						return gi + 1
					}
					return -1
				}(), func() int {
					if gy+1 < NY {
						return gi + NX
					}
					return -1
				}()} {
					if h2 < 0 {
						continue
					}
					for _, f := range [2][]float32{fx, fy} {
						d := float64(f[h2]) - float64(f[gi])
						if math.Abs(d) > lim {
							sg := 1.0
							if d < 0 {
								sg = -1
							}
							fix := (math.Abs(d) - lim) / 2 * sg
							f[gi] = float32(float64(f[gi]) + fix)
							f[h2] = float32(float64(f[h2]) - fix)
							chg = true
						}
					}
				}
			}
		}
		if !chg {
			break
		}
	}
	kc := 0
	for _, k := range keep {
		if k {
			kc++
		}
	}
	st.warp = &Warp{X0: X0, NX: NX, NY: NY, Step: step, Dx: fx, Dy: fy, Measured: kc}
}

// the bending at work-canvas point (x, y), faded by the distance to the seam: dx, dy (B minus A); ok false: none
func warpAt(st *State, x, y float64) (float64, float64, bool) {
	wp := st.warp
	if wp == nil {
		return 0, 0, false
	}
	sy := float64(st.ws.Seam[int(math.Max(0, math.Min(float64(len(st.ws.Seam)-1), jsRound(y))))])
	if sy < 0 {
		return 0, 0, false
	}
	d := math.Abs(x - sy)
	var f float64
	if d <= HB/2 {
		f = 1
	} else if d >= HB {
		f = 0
	} else {
		f = 1 - (d-HB/2)/(HB/2)
	}
	if f <= 0 {
		return 0, 0, false
	}
	gx := math.Max(0, math.Min(float64(wp.NX)-1.001, (x-float64(wp.X0))/float64(wp.Step)))
	gy := math.Max(0, math.Min(float64(wp.NY)-1.001, y/float64(wp.Step)))
	x0, y0 := int(gx), int(gy)
	ax, ay := gx-float64(x0), gy-float64(y0)
	k := y0*wp.NX + x0
	bl := func(F []float32) float64 {
		return (float64(F[k])*(1-ax)+float64(F[k+1])*ax)*(1-ay) + (float64(F[k+wp.NX])*(1-ax)+float64(F[k+wp.NX+1])*ax)*ay
	}
	f = f * f * (3 - 2*f)
	return bl(wp.Dx) * f, bl(wp.Dy) * f, true
}

// How well both lenses match over the shared part (correlation of brightness), B with or without the per-row distances.
func stripMatch(st *State, useW bool) float64 {
	c, g := &st.cyl, &st.rig
	var r [3]float64
	var qa, qb [2]float64
	sa, sb, saa, sbb, sab, n := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
	for y := 0; y < c.CH; y += 2 {
		for x := 0; x < c.CW; x += 2 {
			cylRay(c, float64(x), float64(y), &r)
			if !toA(g, &r, 0, &qa) {
				continue
			}
			var ok bool
			if useW {
				ok = toBrow(st, &r, float64(y), &qb)
			} else {
				ok = toB(g, &r, 0, &qb)
			}
			if !ok {
				continue
			}
			a, b := bil(st.A, LW, LH, qa[0], qa[1]), bil(st.B, LW, LH, qb[0], qb[1])
			if a != a || b != b {
				continue
			}
			sa += a
			sb += b
			saa += a * a
			sbb += b * b
			sab += a * b
			n++
		}
	}
	return (sab/n - sa/n*sb/n) / math.Sqrt((saa/n-(sa/n)*(sa/n))*(sbb/n-(sb/n)*(sb/n)))
}
