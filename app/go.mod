module app

go 1.27.3

require (
	calib v0.0.0
	compartir v0.0.0
	dh-p2p v0.0.0
	github.com/hajimehoshi/ebiten/v2 v2.10.6
	nvrlib v0.0.0
	web v0.0.0
	video v0.0.0
)

require (
	github.com/ebitengine/gomobile v0.0.0-20260820040257-d11f821a26a6 // indirect
	github.com/ebitengine/hideconsole v1.0.0 // indirect
	github.com/ebitengine/purego v0.11.3 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	golang.org/x/image v0.45.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace nvrlib => ../nvrlib

replace dh-p2p => ../tunnel

replace video => ../video

replace calib => ../calib

replace compartir => ../compartir

replace web => ../web
