module mobile

go 1.27.3

require (
	compartir v0.0.0
	dh-p2p v0.0.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	nvrlib v0.0.0
	web v0.0.0
)

require (
	golang.org/x/mobile v0.0.0-20261117202452-47699113848a // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/tools v0.53.0 // indirect
)

replace nvrlib => ../nvrlib

replace dh-p2p => ../tunnel

replace web => ../web

replace compartir => ../compartir
