package calib

// For rendering the panorama in the app we use Go's standard math. (The original
// gocal used the system libm through an assembly trampoline only to match the
// phone's JavaScript bit-for-bit during equivalence testing; that is not needed here.)

import "math"

func sin(x float64) float64      { return math.Sin(x) }
func cos(x float64) float64      { return math.Cos(x) }
func acos(x float64) float64     { return math.Acos(x) }
func atan2(y, x float64) float64 { return math.Atan2(y, x) }
func hypot(x, y float64) float64 { return math.Hypot(x, y) }
func exp(x float64) float64      { return math.Exp(x) }
