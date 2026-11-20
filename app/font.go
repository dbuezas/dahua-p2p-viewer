package main

import (
	"image/color"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

// Real (smooth) fonts instead of Ebiten's debug font, like the old app's Windows text.
var (
	fontMu    sync.Mutex
	fontCache = map[[2]int]font.Face{}
	fRegular  *opentype.Font
	fBold     *opentype.Font
)

func face(size int, bold bool) font.Face {
	fontMu.Lock()
	defer fontMu.Unlock()
	key := [2]int{size, 0}
	if bold {
		key[1] = 1
	}
	if f, ok := fontCache[key]; ok {
		return f
	}
	if fRegular == nil {
		fRegular, _ = opentype.Parse(goregular.TTF)
		fBold, _ = opentype.Parse(gobold.TTF)
	}
	src := fRegular
	if bold {
		src = fBold
	}
	f, err := opentype.NewFace(src, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	fontCache[key] = f
	return f
}

// drawText draws s with its top-left corner at (x, y).
func drawText(dst *ebiten.Image, s string, x, y, size int, bold bool, clr color.Color) {
	f := face(size, bold)
	if f == nil {
		return
	}
	text.Draw(dst, s, f, x, y+f.Metrics().Ascent.Ceil(), clr)
}

// textWidth: the width of s in pixels.
func textWidth(s string, size int, bold bool) int {
	f := face(size, bold)
	if f == nil {
		return 0
	}
	return font.MeasureString(f, s).Ceil()
}
