package main

import (
	"image/color"
	"strconv"
	"time"

	"compartir"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"github.com/skip2/go-qrcode"
)

// The configuration QR: serial, user, password and settings as a camaras://config
// link (package compartir), which the Android app imports when the phone's camera
// scans it. It holds the NVR password, so it only shows when asked for (key C,
// or the phone remote's button) and hides by itself after shareFor. The link is
// never logged.

const shareFor = 60 * time.Second

// showShare shows or hides the configuration QR. The caller holds g.mu.
func (g *game) showShare(on bool) {
	g.shareImg = nil
	if on {
		c := compartir.FromFolder(g.cfg.dir, g.cfg.kv)
		c.Serial, c.User, c.Pass = g.serial, g.user, g.pass
		link, err := compartir.Encode(c)
		if err != nil {
			g.panoMsg = err.Error()
		} else if img := qrImage(link, qrcode.Low); img != nil {
			g.shareImg = img
			g.shareUntil = time.Now().Add(shareFor)
		}
	}
	g.stateChanged()
}

// drawShare draws the configuration QR in the middle of the screen, as big as fits,
// with what it is and how long it stays. The caller holds g.mu.
func (g *game) drawShare(screen *ebiten.Image) {
	if g.shareImg == nil {
		return
	}
	left := time.Until(g.shareUntil)
	if left <= 0 {
		g.shareImg = nil
		g.stateChanged()
		return
	}
	fs := g.px(30)
	n := g.shareImg.Bounds().Dx() + 4 // with the quiet zone
	k := (g.sh - 4*fs - g.px(60)) / n
	if k < 1 {
		k = 1
	}
	qs := n * k
	x, y := (g.sw-qs)/2, (g.sh-qs)/2+fs
	// a dark band behind the texts and the code
	pad := g.px(20)
	vector.DrawFilledRect(screen, float32(x-pad-fs*8), float32(y-3*fs-pad), float32(qs+2*pad+fs*16), float32(qs+3*fs+2*pad),
		color.RGBA{0x10, 0x10, 0x10, 0xf0}, false)
	t1 := "Configuracion de las camaras: escanealo con la camara del telefono"
	t2 := "Incluye la clave del grabador. Se oculta en " + strconv.Itoa(int(left.Seconds())+1) + " s (C o Esc: ocultar ya)"
	drawText(screen, t1, (g.sw-textWidth(t1, fs, true))/2, y-3*fs+fs/4, fs, true, color.White)
	drawText(screen, t2, (g.sw-textWidth(t2, fs*7/10, false))/2, y-fs-fs/4, fs*7/10, false, color.RGBA{0xff, 0xd0, 0x40, 0xff})
	drawQR(screen, g.shareImg, k, x, y)
}
