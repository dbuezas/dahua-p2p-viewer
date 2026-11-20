module app

go 1.20

require github.com/hajimehoshi/ebiten/v2 v2.7.10

require (
	calib v0.0.0
	dh-p2p v0.0.0
	nvrlib v0.0.0
	web v0.0.0
	video v0.0.0
)

require (
	github.com/ebitengine/gomobile v0.0.0-20240518074828-e86332849895 // indirect
	github.com/ebitengine/hideconsole v1.0.0 // indirect
	github.com/ebitengine/purego v0.7.0 // indirect
	github.com/jezek/xgb v1.1.1 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	golang.org/x/image v0.18.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.20.0 // indirect
)

replace nvrlib => ../nvrlib

replace dh-p2p => ../tunnel

replace video => ../video

replace calib => ../calib

replace web => ../web
