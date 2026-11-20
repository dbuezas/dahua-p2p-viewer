package main

import (
	"bytes"
	"image/jpeg"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
	"web"
)

// Phone calibration (the old app's mode 3): the page runs calib.js on a fresh
// picture of the split camera and sends back a mesh. calib.js is the same file as
// the old app's (copied here as calib_phone.js).

func (g *game) serveCalibJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(web.CalibJS)
}

// serveCalibSnap: a full picture of the split camera (both lenses). Right after a
// start the video may not have come yet: wait up to 8 s.
func (g *game) serveCalibSnap(w http.ResponseWriter, r *http.Request) {
	for tries := 0; tries < 16; tries++ {
		g.mu.Lock()
		var s *stream
		for _, st := range g.streams {
			if st.split {
				s = st
			}
		}
		g.mu.Unlock()
		if s != nil {
			s.mu.Lock()
			fr := s.frame
			s.mu.Unlock()
			if fr != nil {
				var buf bytes.Buffer
				jpeg.Encode(&buf, fr.RGBA(), &jpeg.Options{Quality: 92})
				w.Header().Set("Content-Type", "image/jpeg")
				w.Write(buf.Bytes())
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	http.Error(w, "no pude sacar la foto", http.StatusServiceUnavailable)
}

// meshHead: the last phone calibration's header (the phone starts its hand
// alignment from there).
func (g *game) meshHead() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.streams {
		s.mu.Lock()
		m := s.mesh
		s.mu.Unlock()
		if m != nil {
			return m.Head
		}
	}
	return "{}"
}

// meshReceived: a mesh from the phone. It is saved where the old app keeps it
// (panoramica3_<ch>.bin) and used at once for the panorama.
func (g *game) meshReceived(data []byte) {
	m := parseMesh3(data)
	if m == nil {
		g.panoMsg = "la calibracion recibida no se puede leer"
		log.Printf("calibracion del telefono: no la puedo leer (%d bytes)", len(data))
		return
	}
	g.mu.Lock()
	var list []*stream
	for _, s := range g.streams {
		if s.split {
			list = append(list, s)
		}
	}
	g.mu.Unlock()
	for _, s := range list {
		os.WriteFile(g.cfg.path("panoramica3_"+strconv.Itoa(s.ch)+".bin"), data, 0644)
		s.mu.Lock()
		s.mesh, s.mlut, s.snapLut = m, nil, nil
		s.mu.Unlock()
		s.restart() // HD may now be allowed for the panorama
	}
	g.panoMsg = ""
	log.Printf("calibracion del telefono recibida: %dx%d, guardada y en uso", m.OW, m.OH)
	g.setPano(true)
}

// sortTiles: a stable sort of the squares.
func sortTiles(ts []*tile, less func(a, b *tile) bool) {
	sort.SliceStable(ts, func(i, j int) bool { return less(ts[i], ts[j]) })
}
