package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	p2ptunnel "dh-p2p"
)

// Extra cameras: a camera that is NOT on the NVR, reached by its own P2P serial
// (an Imou/Dahua camera with cloud). Each one gets its own tunnel and its own RTSP
// auth relay (its own password), and shows as one more tile. It has only video
// (HD and low); no PTZ, no panorama, no name/time from the device (the camera
// burns its own clock into the picture). The NVR cameras are unchanged.
//
// The cameras are listed in camaras_extra.txt next to camaras.ini, one per line:
//
//	SERIAL usuario clave Nombre con espacios
//
// Blank lines and lines that start with # are skipped. The file holds a password,
// so it is never committed (like camaras.ini).

// extraIndexBase keeps the made-up channel numbers of the extra cameras far from
// the NVR's own (0, 1, 2 ...), so they never clash.
const extraIndexBase = 100

type extraCam struct {
	serial string
	user   string
	pass   string
	name   string
	index  int // made-up channel number (extraIndexBase + position)
	port   int // the tunnel's local RTSP port (forwarded to the camera's 554)

	mu    sync.Mutex
	tn    *p2ptunnel.Tunnel
	relay string // the RTSP auth relay address, once it is up
}

// loadExtras reads camaras_extra.txt and returns one extraCam per line (no tunnel
// opened yet). The order is the file's order.
func (g *game) loadExtras() []*extraCam {
	f, err := os.Open(g.cfg.path("camaras_extra.txt"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []*extraCam
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// SERIAL user pass name...: the name is the rest of the line (may have spaces)
		parts := strings.SplitN(line, " ", 4)
		if len(parts) < 3 {
			log.Printf("camaras_extra.txt: linea sin serial/usuario/clave: %q", firstField(line))
			continue
		}
		ec := &extraCam{
			serial: strings.TrimSpace(parts[0]),
			user:   strings.TrimSpace(parts[1]),
			pass:   strings.TrimSpace(parts[2]),
			index:  extraIndexBase + len(out),
			port:   1600 + len(out),
		}
		if len(parts) == 4 {
			ec.name = strings.TrimSpace(parts[3])
		}
		if ec.name == "" {
			ec.name = "Camara " + fmt.Sprint(ec.index-extraIndexBase+1)
		}
		out = append(out, ec)
	}
	return out
}

// relayAddr opens the camera's tunnel and RTSP relay the first time it is asked
// for, and returns the relay address. "" when the tunnel is not up yet (the
// caller, the stream's run loop, tries again later). Safe to call from several
// goroutines.
func (ec *extraCam) relayAddr(g *game) string {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	if ec.relay != "" {
		return ec.relay
	}
	tn, err := p2ptunnel.Open(ec.serial)
	if err != nil {
		log.Printf("camara %s: tunel: %v", ec.name, err)
		return ""
	}
	if err := tn.Forward("127.0.0.1", ec.port, 554); err != nil {
		tn.Close()
		log.Printf("camara %s: forward: %v", ec.name, err)
		return ""
	}
	addr, err := startRTSPRelay(fmt.Sprintf("127.0.0.1:%d", ec.port), func() (string, string) {
		return ec.user, ec.pass
	})
	if err != nil {
		log.Printf("camara %s: relevo RTSP: %v", ec.name, err)
		addr = fmt.Sprintf("127.0.0.1:%d", ec.port)
	}
	ec.tn = tn
	ec.relay = addr
	log.Printf("camara suelta %s: tunel abierto (puerto %d->554)", ec.name, ec.port)
	return ec.relay
}

// firstField returns the first word of a line (to log a bad line without its password).
func firstField(line string) string {
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return line[:i] + " ..."
	}
	return line
}
