package camaras

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"web"
)

// StartCalib serves the panorama calibration to the app's WebView: the same page
// and calib.js the TV app gives the phone's browser. The picture is the NVR's
// snapshot of the split camera (channel ch, 0-based); the finished mesh is saved
// to meshPath (the "P3" file the app draws with) and MeshVersion goes up. It
// returns the page's address.
func (s *Session) StartCalib(ch int, meshPath string) (string, error) {
	s.mu.Lock()
	if s.calib != nil {
		s.calib.Close()
	}
	s.mu.Unlock()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(web.Page)
	})
	mux.HandleFunc("/calib.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(web.CalibJS)
	})
	mux.HandleFunc("/calibsnap", func(w http.ResponseWriter, r *http.Request) {
		jpg, err := s.c.SnapshotBytes(fmt.Sprintf("127.0.0.1:%d", httpPort), ch+1)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpg)
	})
	mux.HandleFunc("/calibprev", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(meshHead(meshPath)))
	})
	state := fmt.Sprintf(`{"big":-1,"split":true,"pano":3,"panoReady":true,"msg":"","cams":[{"k":"%d.3","hd":false,"ptz":false}]}`, ch)
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		web.ServeWS(w, r, func(k *web.Conn) {
			for k.Alive() {
				if k.Send(1, []byte(state)) != nil {
					return
				}
				time.Sleep(time.Second)
			}
		}, nil, func(mesh []byte) {
			if len(mesh) > 6 && mesh[0] == 'P' && mesh[1] == '3' && os.WriteFile(meshPath, mesh, 0o600) == nil {
				s.mu.Lock()
				s.meshVer++
				s.mu.Unlock()
			}
		})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	s.mu.Lock()
	s.calib = srv
	s.mu.Unlock()
	return fmt.Sprintf("http://%s/", ln.Addr()), nil
}

// MeshVersion goes up each time a calibration is saved.
func (s *Session) MeshVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meshVer
}

// StopCalib ends the calibration page.
func (s *Session) StopCalib() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calib != nil {
		s.calib.Close()
		s.calib = nil
	}
}

// meshHead: the JSON head of a saved mesh (the page starts from it), or "{}".
func meshHead(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 6 || b[0] != 'P' || b[1] != '3' {
		return "{}"
	}
	hl := int(binary.LittleEndian.Uint32(b[2:6]))
	if 6+hl > len(b) {
		return "{}"
	}
	return string(b[6 : 6+hl])
}
