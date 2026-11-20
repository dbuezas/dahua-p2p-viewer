package main

import (
	"image"
	"math"
)

// arrange places the pictures (no stretching) so that together they cover as much
// of the screen as possible; same rules as the old PC app. u[i] = how many 16:9
// cells wide picture i is (1, or 2 for a panorama). Two kinds of layouts are tried
// and the bigger one wins:
//   - a grid of equal cells, c cells wide (a 2-cell picture that does not fit at
//     the end of a row starts the next row),
//   - rows with different counts (e.g. 5 = 2 big on top + 3 smaller below), each
//     row as wide as the screen.
//
// The result is centered.
func arrange(u []int, W, H int) []image.Rectangle {
	n := len(u)
	if n == 0 {
		return nil
	}
	total, widest := 0, 1
	for _, x := range u {
		total += x
		if x > widest {
			widest = x
		}
	}
	var best []image.Rectangle
	bestArea := -1.0
	consider := func(list []image.Rectangle) {
		a := 0.0
		for _, r := range list {
			a += float64(r.Dx()) * float64(r.Dy())
		}
		if a > bestArea+1 {
			bestArea, best = a, list
		}
	}
	for c := widest; c <= total; c++ {
		row, col := make([]int, n), make([]int, n)
		rr, used := 0, 0
		for i := 0; i < n; i++ {
			if used+u[i] > c {
				rr++
				used = 0
			}
			row[i], col[i] = rr, used
			used += u[i]
		}
		rows := rr + 1
		sc := math.Min(float64(W)/float64(c)/16, float64(H)/float64(rows)/9)
		w, h := int(sc*16), int(sc*9)
		x0, y0 := (W-c*w)/2, (H-rows*h)/2
		list := make([]image.Rectangle, n)
		for i := 0; i < n; i++ {
			x, y := x0+col[i]*w, y0+row[i]*h
			list[i] = image.Rect(x, y, x+u[i]*w, y+h)
		}
		consider(list)
	}
	for r := 1; r <= n; r++ {
		baseK, extra := n/r, n%r
		counts := make([]int, r)
		for i := 0; i < r; i++ {
			counts[i] = baseK
			if i >= r-extra { // fewer (bigger) on top
				counts[i]++
			}
		}
		rowUnits := make([]int, r)
		k0 := 0
		for i := 0; i < r; i++ {
			for j := 0; j < counts[i]; j++ {
				rowUnits[i] += u[k0+j]
			}
			k0 += counts[i]
		}
		sum := 0.0
		bad := false
		for _, ru := range rowUnits {
			if ru == 0 {
				bad = true
				break
			}
			sum += float64(W) / float64(ru) * 9 / 16
		}
		if bad {
			continue
		}
		f := math.Min(1, float64(H)/sum)
		y := (float64(H) - sum*f) / 2
		list := make([]image.Rectangle, 0, n)
		k0 = 0
		for i := 0; i < r; i++ {
			cw := f * float64(W) / float64(rowUnits[i])
			h := cw * 9 / 16
			x := (float64(W) - float64(rowUnits[i])*cw) / 2
			for j := 0; j < counts[i]; j++ {
				k := k0 + j
				list = append(list, image.Rect(int(x), int(y), int(x+float64(u[k])*cw), int(y+h)))
				x += float64(u[k]) * cw
			}
			k0 += counts[i]
			y += h
		}
		consider(list)
	}
	return best
}

// arrangeBig shows one camera big (the biggest 16:9 rectangle, centered); the
// others float over its bottom edge as small pictures (10% of the screen width
// each, in a centered row), so the big one keeps its full size.
func arrangeBig(n, bigIdx, W, H int) []image.Rectangle {
	fs := math.Min(float64(W)/16, float64(H)/9)
	gw, gh := fs*16, fs*9
	x0, y0 := (float64(W)-gw)/2, (float64(H)-gh)/2
	m := n - 1
	tw := float64(W) * 0.10
	if m > 0 {
		tw = math.Min(tw, (gw-20)/float64(m))
	} else {
		tw = 0
	}
	th, gap := tw*9/16, 10.0
	tx, ty := (float64(W)-float64(m)*tw)/2, y0+gh-th-gap
	list := make([]image.Rectangle, n)
	k := 0
	for i := 0; i < n; i++ {
		if i == bigIdx {
			list[i] = image.Rect(int(x0), int(y0), int(x0+gw), int(y0+gh))
			continue
		}
		x := tx + float64(k)*tw
		list[i] = image.Rect(int(x), int(ty), int(x+tw), int(ty+th))
		k++
	}
	return list
}
