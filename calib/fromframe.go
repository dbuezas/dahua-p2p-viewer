package calib

// grayFromRGB24 splits a 640x720 rgb24 frame into the two lens grayscale buffers.
func grayFromRGB24(rgb []byte) (A, B []float32) {
	lens := func(top bool) []float32 {
		g := make([]float32, LW*LH)
		off := 0
		if !top {
			off = LH
		}
		for y := 0; y < LH; y++ {
			for x := 0; x < LW; x++ {
				k := ((y+off)*LW + x) * 3
				g[y*LW+x] = float32(0.299*float64(rgb[k]) + 0.587*float64(rgb[k+1]) + 0.114*float64(rgb[k+2]))
			}
		}
		return g
	}
	return lens(true), lens(false)
}

// CalibrateRGB24 runs the full calibration on a 640x720 rgb24 frame and returns
// the State. startPan is the rough pan guess in degrees. Panics become an error.
func CalibrateRGB24(rgb []byte, startPan float64, over map[string]any) (st *State, err error) {
	defer func() {
		if e := recover(); e != nil {
			if ce, ok := e.(calibErr); ok {
				err = &calErr{ce.msg}
			} else {
				err = &calErr{"panic en calibracion"}
			}
		}
	}()
	A, B := grayFromRGB24(rgb)
	st = &State{A: A, B: B, o: buildOpts(over, startPan)}
	Run(st)
	return st, nil
}

type calErr struct{ m string }

func (e *calErr) Error() string { return e.m }
