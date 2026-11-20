package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"calib"
	"video"
)

// stream is one NVR channel's live video, always running, shared by all the
// squares that show it (a split channel feeds its two halves or its panorama).
type stream struct {
	g     *game
	ch    int  // 0-based NVR channel (RTSP channel = ch+1)
	split bool // two cameras stacked top + bottom

	mu        sync.Mutex
	hd        bool          // main stream instead of the sub stream
	playingHD bool          // the stream now playing is the main one
	queue     []qframe      // decoded frames waiting for their turn on screen
	arrivals  []time.Time   // when the last frames came (to know the camera's pace)
	gapMax    time.Duration // longest time between two frames since the last stats line
	shown     time.Time     // when the last frame went on screen
	gaps      int           // frames that came more than 300 ms after the one before
	pano      bool          // split channel shown as one panorama
	frame     *video.Frame  // latest frame on screen (whole picture, YUV)
	pframe    *image.RGBA   // latest panorama (pano mode)
	ver       int           // bumped on each new frame
	last      time.Time     // when the last frame came
	dec       *video.Decoder
	w, h      int
	lut       *calib.Lut
	gpu       gpuState // textures on the graphics card (only used from Draw)
	snapLut   *meshLut // the panorama on the processor, for snapshots only
	mesh      *mesh3   // the phone's calibration, when there is one (used first)
	mlut      *meshLut // the mesh turned into a lookup for the current stream size
	weak      []string // why the panorama may be wrong (calibration warnings)
	calibAt   time.Time
	nfr       int
	fps       float64
	tfr       time.Time
}

// rtspURL builds the stream address; it is never logged (it holds the password).
func (s *stream) rtspURL(sub int) string {
	return fmt.Sprintf("rtsp://%s:%s@127.0.0.1:1554/cam/realmonitor?channel=%d&subtype=%d",
		s.g.user, url.QueryEscape(s.g.pass), s.ch+1, sub)
}

func (s *stream) run() {
	go s.present()
	saveTo := os.Getenv("SAVEFRAME")
	// a long look before the video starts: ffmpeg stops looking as soon as it has
	// found the video, so it only costs time on a camera that is late (the IMOU)
	slow := true
	for {
		s.mu.Lock()
		hd, pano := s.hd, s.pano
		s.mu.Unlock()
		s.mu.Lock()
		haveMesh := s.mesh != nil
		s.mu.Unlock()
		sub := 1
		// HD (main stream). A panorama in HD needs the phone's mesh: the one worked
		// out here from the picture needs the 640x720 sub stream.
		if hd && !(s.split && pano && !haveMesh) {
			sub = 0
		}
		maxW := 0
		if sub == 0 && s.split && pano {
			maxW = 1152 // the panorama is 1400 wide from two lenses: half the HD size is plenty
		} else if sub == 0 {
			s.g.mu.Lock()
			maxW = s.g.sw // never decode wider than the screen
			s.g.mu.Unlock()
		}
		d, err := video.OpenScaled(s.rtspURL(sub), true, slow, maxW)
		if err != nil {
			log.Printf("canal %d: %v", s.ch, err)
			if !slow {
				slow = true
				time.Sleep(500 * time.Millisecond)
			} else {
				time.Sleep(3 * time.Second)
			}
			continue
		}
		log.Printf("canal %d: video %dx%d (%s) %s", s.ch, d.W, d.H, map[int]string{0: "HD", 1: "baja"}[sub], d.Info)
		s.mu.Lock()
		s.dec, s.last, s.w, s.h = d, time.Now(), d.W, d.H
		s.playingHD = sub == 0
		s.tfr, s.nfr = time.Now(), 0
		s.mu.Unlock()
		saveN := 0
		for f := range d.Frames {
			s.mu.Lock()
			pano, m := s.pano, s.mesh
			s.mu.Unlock()
			// a split camera as a panorama: with the phone's mesh the graphics card
			// makes it (gpu.go); without one it is worked out here from the frame
			var pf *image.RGBA
			if s.split && pano && m == nil && f.W == 640 && f.H == 720 {
				img := f.RGBA()
				if s.lut == nil {
					s.calibrate(img)
				}
				if s.lut != nil {
					pf = image.NewRGBA(image.Rect(0, 0, s.lut.W, s.lut.H))
					s.lut.RenderRGBA(img.Pix, pf)
				}
			}
			s.push(f, pf)
			if saveTo != "" {
				if saveN++; saveN%15 == 0 {
					var out image.Image = pf
					if pf == nil {
						out = f.RGBA()
					}
					if fh, e := os.Create(fmt.Sprintf("%s_ch%d.jpg", saveTo, s.ch)); e == nil {
						jpeg.Encode(fh, out, &jpeg.Options{Quality: 80})
						fh.Close()
					}
				}
			}
		}
		s.mu.Lock()
		s.dec = nil
		s.mu.Unlock()
		time.Sleep(time.Second) // stream ended (or was restarted): open it again
	}
}

// calibrate works the panorama out from one frame (never refuses; doubtful
// results carry warnings).
func (s *stream) calibrate(img *image.RGBA) {
	t0 := time.Now()
	st, err := calib.CalibrateRGB24(rgbaToRGB24(img.Pix), -45, nil)
	if err != nil {
		log.Printf("canal %d: panoramica: %v", s.ch, err)
		return
	}
	s.lut = calib.BuildLut(st)
	s.weak = st.Weak
	s.calibAt = time.Now()
	log.Printf("canal %d: panoramica calibrada en %.1fs", s.ch, time.Since(t0).Seconds())
	if len(st.Weak) > 0 {
		log.Printf("canal %d: OJO, panoramica con poca confianza: %v", s.ch, st.Weak)
	}
}

// restart stops the current video; run opens it again (with the current HD /
// panorama choice).
func (s *stream) restart() {
	s.mu.Lock()
	d := s.dec
	s.mu.Unlock()
	if d != nil {
		d.Close()
	}
}

func (s *stream) setHD(on bool) {
	s.mu.Lock()
	changed := s.hd != on
	s.hd = on
	s.mu.Unlock()
	if changed {
		s.restart()
	}
}

// watchdog, every 5 s: a channel without a new frame for 20 s gets its video
// restarted (like the old app).
func (g *game) watchdog() {
	last := time.Now()
	for range time.Tick(5 * time.Second) {
		// after the PC wakes up (a big jump of the clock) every video starts again;
		// the tunnel notices its silent session and opens a new one by itself
		if gap := time.Since(last); gap > 30*time.Second {
			log.Printf("la PC desperto (%v sin correr): reinicio los videos", gap.Round(time.Second))
			for _, s := range g.streamList() {
				s.restart()
			}
		}
		last = time.Now()
		for _, s := range g.streamList() {
			s.mu.Lock()
			stale := s.dec != nil && time.Since(s.last) > 20*time.Second
			s.mu.Unlock()
			if stale {
				log.Printf("canal %d: sin video 20 s, reinicio", s.ch)
				s.restart()
			}
		}
	}
}

// qframe is one decoded frame (and its panorama) waiting in the cushion.
type qframe struct {
	f  *video.Frame
	pf *image.RGBA
}

func (f qframe) bytes() int {
	n := 0
	if f.f != nil {
		n = len(f.f.Pix)
	}
	if f.pf != nil {
		n += len(f.pf.Pix)
	}
	return n
}

// push puts a new frame in the cushion (called when it comes out of ffmpeg).
func (s *stream) push(img *video.Frame, pf *image.RGBA) {
	now := time.Now()
	s.mu.Lock()
	s.last = now
	if pf != nil {
		// panorama: only the (small) panorama waits in the cushion; the whole
		// picture is kept just as the latest one (snapshots, calibration)
		s.frame = img
		img = nil
	}
	s.queue = append(s.queue, qframe{img, pf})
	s.arrivals = append(s.arrivals, now)
	if len(s.arrivals) > 60 {
		s.arrivals = s.arrivals[len(s.arrivals)-60:]
	}
	// never more than ~150 MB waiting (full-size HD frames are 24 MB each)
	total := 0
	for _, f := range s.queue {
		total += f.bytes()
	}
	for total > 150<<20 && len(s.queue) > 1 {
		total -= s.queue[0].bytes()
		s.queue = s.queue[1:]
	}
	s.mu.Unlock()
}

// present shows the frames at an even pace, like a video player: it keeps a
// cushion of about colchon_ms (default 1200 ms) of video, so the short waits of the
// tunnel (a lost packet holds back every camera for 0.1-1 s) do not show as
// stops and bursts. The picture is that much behind real time.
func (s *stream) present() {
	target := time.Duration(s.g.cfg.getInt("colchon_ms", 1200)) * time.Millisecond
	var next time.Time
	filling := true
	for {
		now := time.Now()
		s.mu.Lock()
		n := len(s.queue)
		// the camera's pace: frames per second over the last ~60 frames
		interval := 66 * time.Millisecond
		if k := len(s.arrivals); k >= 10 {
			if span := s.arrivals[k-1].Sub(s.arrivals[0]); span > 0 {
				interval = span / time.Duration(k-1)
			}
		}
		if n == 0 {
			s.mu.Unlock()
			if !filling && target > 0 {
				filling = true // ran dry: fill the cushion again before going on
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		lag := time.Duration(n) * interval
		if filling {
			if s.frame != nil && target > 0 && lag < target {
				s.mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				continue
			}
			filling, next = false, now
		}
		if now.Before(next) {
			s.mu.Unlock()
			time.Sleep(next.Sub(now))
			continue
		}
		f := s.queue[0]
		s.queue = s.queue[1:]
		if s.nfr > 0 {
			if gap := now.Sub(s.shown); gap > s.gapMax {
				s.gapMax = gap
			}
			if now.Sub(s.shown) > 300*time.Millisecond {
				s.gaps++
			}
		}
		if f.f != nil {
			s.frame = f.f
		}
		s.pframe, s.shown = f.pf, now
		s.ver++
		if s.nfr++; now.Sub(s.tfr) > 10*time.Second {
			s.fps = float64(s.nfr) / now.Sub(s.tfr).Seconds()
			s.nfr, s.tfr = 0, now
		}
		s.mu.Unlock()
		ebiten.ScheduleFrame()
		// keep the cushion near its size: a bit faster when too full, slower when
		// almost empty (never noticeable)
		step := interval
		switch {
		case target == 0:
			step = 0
		case lag > target*3/2:
			step = interval * 8 / 10
		case lag < target/2:
			step = interval * 12 / 10
		}
		next = next.Add(step)
		if next.Before(now.Add(-200 * time.Millisecond)) {
			next = now
		}
	}
}
