package main

import (
	"errors"
	"image/color"
	"log"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// errBadPass: the NVR said no to the password (never retried by itself: too many
// tries can lock the NVR account).
var errBadPass = errors.New("el NVR no acepto la clave")

// askPassword shows the password screen (like the old app's first-run window) and
// waits until the user types it and presses Enter. The password is saved
// encrypted for this user (Windows: DPAPI in the old app's file; Mac: Keychain).
func (g *game) askPassword(msg string) {
	g.mu.Lock()
	g.askPass, g.passMsg, g.passBuf = true, msg, nil
	if g.passCh == nil {
		g.passCh = make(chan string, 1)
	}
	ch := g.passCh
	g.mu.Unlock()
	redraw()
	p := <-ch
	if err := savePassword(g.user, p); err != nil {
		log.Printf("no pude guardar la clave: %v", err)
	}
	g.mu.Lock()
	g.pass = p
	g.mu.Unlock()
}

// updatePass handles the keys while the password screen shows (g.mu held).
func (g *game) updatePass() {
	g.passBuf = append(g.passBuf, ebiten.AppendInputChars(nil)...)
	if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) && len(g.passBuf) > 0 {
		g.passBuf = g.passBuf[:len(g.passBuf)-1]
	}
	if (inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter)) && len(g.passBuf) > 0 {
		p := string(g.passBuf)
		g.askPass, g.passBuf = false, nil
		g.status = "Conectando con el grabador..."
		g.passCh <- p
	}
	redraw() // keep reading keys while the screen shows
}

// drawPass draws the password screen: the question, dots for the typed letters.
func (g *game) drawPass(screen *ebiten.Image) {
	screen.Fill(color.RGBA{0x10, 0x10, 0x18, 0xff})
	fs := g.px(34)
	y := g.sh/2 - fs*3
	for _, l := range strings.Split(g.passMsg, "\n") {
		w := textWidth(l, fs, false)
		drawText(screen, l, (g.sw-w)/2, y, fs, false, color.White)
		y += fs * 13 / 10
	}
	dots := strings.Repeat("●", len(g.passBuf)) + "_"
	w := textWidth(dots, fs, true)
	drawText(screen, dots, (g.sw-w)/2, y+fs, fs, true, color.RGBA{0xff, 0xd0, 0x40, 0xff})
	hint := "Escribí la clave y apretá Enter"
	hs := g.px(22)
	drawText(screen, hint, (g.sw-textWidth(hint, hs, false))/2, y+fs*4, hs, false, color.RGBA{0xa0, 0xa0, 0xa0, 0xff})
}
