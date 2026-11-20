package calib

// Go 1.20 lacks the built-in min/max; provide int versions for this Win7 build.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
