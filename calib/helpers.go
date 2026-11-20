package calib

// gocal [options json] : all test pictures (8 at a time), the strip match and typical error per picture, the mean
// and the time; the same output as evalpar.ts. The json overrides camera constants and options, like evalpar's
// first argument (e.g. '{"F":670,"loose":2}'). Env vars as aligncheck.ts: CAM (json), ST, POS, LOOSE, ROWLIM,
// ROWSM, PAIRW, CURVE.

import (
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

var allPics = []struct {
	name string
	sp   float64
}{{"hd_s", -45}, {"vieja", -45}, {"nueva", -45}, {"vivo", -45}, {"vivo2", -45}, {"vivo3", -45}, {"sin1", -45}, {"sin2", -45}, {"hoy3_s", -45}, {"mismo1_s", 0}, {"noche", -45}, {"arriba", 0}}

// JavaScript's unary + on an env string (empty or missing: NaN, then "|| default" in the caller)
func jsNum(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return NaN
	}
	return v
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0 && x == x
	case string:
		return x != ""
	case nil:
		return false
	}
	return true
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case nil:
		return 0
	case string:
		return jsNum(x)
	}
	return NaN
}

// the options as aligncheck.ts + calib run() build them
func buildOpts(over map[string]any, sp float64) *Opts {
	m := map[string]any{"F": 671.58, "K": -0.2496, "tA": -0.02703, "base": 0.09, "ax": 0.0, "az": 0.0,
		"lam": 0.05, "joint": true, "pos": true, "loose": 1.0, "outW": 1400.0}
	stv := -3.0
	if s := os.Getenv("ST"); s != "" {
		stv = jsNum(s)
	}
	m["pos"] = os.Getenv("POS") != "0"
	lo := jsNum(os.Getenv("LOOSE"))
	if !(lo != 0 && lo == lo) {
		lo = 1
	}
	m["loose"] = lo
	rl := jsNum(os.Getenv("ROWLIM"))
	if !(rl != 0 && rl == rl) {
		rl = 0.15
	}
	m["rowLim"] = rl
	if s := os.Getenv("ROWSM"); s != "" {
		m["rowSmooth"] = jsNum(s)
	}
	if s := os.Getenv("PAIRW"); s != "" {
		m["pairW"] = jsNum(s)
	}
	if s := os.Getenv("CURVE"); s != "" {
		m["curveCost"] = jsNum(s)
	}
	for k, v := range over {
		m[k] = v
	}
	o := &Opts{F: num(m["F"]), K: num(m["K"]), TA: num(m["tA"]), Base: num(m["base"]), Ax: num(m["ax"]), Az: num(m["az"]),
		Pos: truthy(m["pos"]), Joint: truthy(m["joint"]), Joint2: m["joint2"] != false, Warp: m["warp"] != false,
		HasStart: true, StartPan: sp / 57.3, StartTilt: stv / 57.3}
	if !truthy(m["ax"]) {
		o.Ax = 0
	}
	if !truthy(m["az"]) {
		o.Az = 0
	}
	o.Lam = 0
	if truthy(m["lam"]) {
		o.Lam = num(m["lam"])
	}
	o.Loose = 1
	if truthy(m["loose"]) {
		o.Loose = num(m["loose"])
	}
	o.RowLim = 0.15
	if truthy(m["rowLim"]) {
		o.RowLim = num(m["rowLim"])
	}
	o.OutW = 1400
	if truthy(m["outW"]) {
		o.OutW = int(num(m["outW"]))
	}
	ptr := func(k string) *float64 {
		if v, ok := m[k]; ok && v != nil {
			x := num(v)
			return &x
		}
		return nil
	}
	o.RowSmooth, o.PairW, o.CurveCost = ptr("rowSmooth"), ptr("pairW"), ptr("curveCost")
	return o
}

func load(path string) (A, B []float32, err error) {
	rgb, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if len(rgb) != 640*720*3 {
		return nil, nil, fmt.Errorf("%s: expected a 640x720 rgb24 frame", path)
	}
	lens := func(top bool) []float32 {
		g := make([]float32, LW*LH)
		off := 0
		if !top {
			off = LH
		}
		for y := 0; y < LH; y++ {
			for x := 0; x < LW; x++ {
				k := ((y+off)*LW + x) * 3
				s0, s1, s2 := float64(rgb[k]), float64(rgb[k+1]), float64(rgb[k+2])
				g[y*LW+x] = float32((0.299*s0 + 0.587*s1 + 0.114*s2) / 1)
			}
		}
		return g
	}
	return lens(true), lens(false), nil
}

type result struct {
	ok         bool
	strip, err float64
	msg        string
	secs       float64
	st         *State
}

func runPic(path string, o *Opts) (res result) {
	t0 := time.Now()
	defer func() {
		if e := recover(); e != nil {
			if ce, ok := e.(calibErr); ok {
				res = result{msg: ce.msg}
			} else {
				res = result{msg: fmt.Sprint(e) + "\n" + string(debug.Stack())}
			}
		}
		res.secs = time.Since(t0).Seconds()
	}()
	A, B, err := load(path)
	if err != nil {
		return result{msg: err.Error()}
	}
	st := &State{A: A, B: B, o: o}
	Run(st)
	return result{ok: true, strip: stripMatch(st, true), err: st.MatchErr, st: st}
}
