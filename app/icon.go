package main

import (
	"bytes"
	"embed"
	"image"
	"image/png"

	"github.com/hajimehoshi/ebiten/v2"
)

// The app's icon (a camera), for the window and the taskbar. camaras.ico is the
// same picture for the Windows desktop shortcut.
//
//go:embed icon/*.png
var iconFS embed.FS

func setWindowIcon() {
	var imgs []image.Image
	for _, n := range []string{"icon/icon16.png", "icon/icon32.png", "icon/icon48.png", "icon/icon256.png"} {
		b, err := iconFS.ReadFile(n)
		if err != nil {
			continue
		}
		if img, err := png.Decode(bytes.NewReader(b)); err == nil {
			imgs = append(imgs, img)
		}
	}
	ebiten.SetWindowIcon(imgs)
}
