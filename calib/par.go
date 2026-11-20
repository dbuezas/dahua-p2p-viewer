package calib

import (
	"runtime"
	"sync"
)

// parFor runs fn over [0, n) in chunks on all cores. Only for work whose items are independent: each item writes
// its own result, and any sums are made afterwards in the original order (so the numbers stay bit-identical).
func parFor(n, minChunk int, fn func(lo, hi int)) {
	P := runtime.GOMAXPROCS(0)
	ch := (n + 2*P - 1) / (2 * P)
	if ch < minChunk {
		ch = minChunk
	}
	if ch >= n {
		fn(0, n)
		return
	}
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += ch {
		hi := min(n, lo+ch)
		wg.Add(1)
		go func(lo, hi int) { defer wg.Done(); fn(lo, hi) }(lo, hi)
	}
	wg.Wait()
}
