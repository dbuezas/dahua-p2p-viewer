// nvr: a thin CLI over the nvrlib package (login, channels, snapshot, PTZ read),
// reaching the NVR through the local end of the P2P tunnel.
package main

import (
	"flag"
	"fmt"
	"os"

	"nvrlib"
)

func main() {
	rpc := flag.String("rpc", "127.0.0.1:1580", "local end of the tunnel to the NVR web/RPC port (80)")
	user := flag.String("user", "admin", "NVR user")
	snap := flag.Int("snap", -1, "if >=0, save a JPEG snapshot of this channel and exit")
	ptzStatus := flag.Int("ptzstatus", -1, "if >=0, read PTZ position of this channel and exit")
	flag.Parse()

	pass, err := nvrlib.KeychainPassword(*user)
	if err != nil || pass == "" {
		fmt.Println("no pude leer la clave del Llavero:", err)
		os.Exit(1)
	}
	c := nvrlib.New(*rpc, *user, pass)
	if err := c.Login(); err != nil {
		fmt.Println("fallo login:", err)
		os.Exit(1)
	}
	fmt.Println("login OK, modelo:", c.DeviceType())

	if *snap >= 0 {
		b, err := c.SnapshotBytes(*rpc, *snap)
		if err != nil {
			fmt.Println("fallo snapshot:", err)
			os.Exit(1)
		}
		f := fmt.Sprintf("snap_ch%d.jpg", *snap)
		os.WriteFile(f, b, 0644)
		fmt.Printf("snapshot %s (%d bytes)\n", f, len(b))
		return
	}

	if *ptzStatus >= 0 {
		obj, err := c.PTZInstance(*ptzStatus)
		if err != nil {
			fmt.Println("fallo ptz instance:", err)
			os.Exit(1)
		}
		pan, tilt, zoom, err := c.PTZStatus(obj)
		if err != nil {
			fmt.Println("fallo ptz getStatus:", err)
			os.Exit(1)
		}
		fmt.Printf("PTZ canal %d: pan=%.1f tilt=%.1f zoom=%.1f\n", *ptzStatus, pan, tilt, zoom)
		return
	}

	cams, err := c.ListCameras()
	if err != nil {
		fmt.Println("fallo listar camaras:", err)
		os.Exit(1)
	}
	fmt.Printf("canales: %d\n", len(cams))
	n := 0
	for _, ch := range cams {
		if !ch.Enable {
			continue
		}
		fmt.Printf("  canal %d: %q  %s  (%s)\n", ch.Index, ch.Name, ch.Address, ch.Type)
		n++
	}
	fmt.Printf("activos: %d\n", n)
}
