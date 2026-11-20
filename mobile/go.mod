module mobile

go 1.27.3

require (
	dh-p2p v0.0.0
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
