// Package video decodes an RTSP stream (or a file) to frames, for the desktop
// build, using ffmpeg as a subprocess. Android will use MediaCodec instead
// (same Frames channel), selected by build tags later.
package video

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// credRe matches the user:password part of a URL, so errors never show it.
var credRe = regexp.MustCompile(`//[^/@\s]*@`)

// redact hides URL credentials in a message.
func redact(s string) string { return credRe.ReplaceAllString(s, "//***@") }

// Decoder streams frames from an input (RTSP URL or file path).
type Decoder struct {
	cmd    *exec.Cmd
	pts    chan float64 // each frame's time in the video (s), from ffmpeg's showinfo lines
	reap   sync.Once    // Wait exactly once, whoever sees the end first
	Frames chan *Frame
	W, H   int
	Info   string // ffmpeg's lines about the input video (codec) and hardware decoding
}

// probe returns the video width and height via ffprobe, reading its STDOUT
// (reliable across platforms, unlike ffmpeg's block-buffered stderr). Over the
// P2P tunnel only TCP works, so force RTSP-over-TCP (the UDP default hangs).
// Open starts decoding. For RTSP use rtsp:// ... ; tcp forces TCP transport.
// One ffmpeg process and ONE connection per stream (no separate ffprobe): every
// frame comes as a PAM image, whose small text header carries its size.
func Open(input string, rtspTCP bool) (*Decoder, error) {
	return OpenProbe(input, rtspTCP, false)
}

// OpenProbe: long = read more of the stream before starting (some cameras only
// show their size later).
func OpenProbe(input string, rtspTCP, long bool) (*Decoder, error) {
	return OpenScaled(input, rtspTCP, long, 0)
}

// OpenScaled: like OpenProbe; maxW > 0 makes ffmpeg shrink wider pictures to that
// width (less work and memory when the full size is not needed).
func OpenScaled(input string, rtspTCP, long bool, maxW int) (*Decoder, error) {
	size, dur := "1000000", "2000000"
	if long {
		size, dur = "5000000", "8000000"
	}
	// info level (no progress lines): the stream's codec line and any warning about
	// hardware decoding end up in the error tail, logged once at start
	args := []string{"-nostdin", "-hide_banner", "-nostats", "-loglevel", "info", "-probesize", size, "-analyzeduration", dur}
	if rtspTCP {
		args = append(args, "-rtsp_transport", "tcp")
	}
	// -vsync passthrough: only the camera's real frames (no repeated ones to keep a
	// fixed rate: they waste CPU and would hide a stalled stream from the watchdog)
	// one decoding thread per camera: with the default (one per CPU thread) frame
	// threading cost 35-40% more CPU on the TV PC (i7 Q720) for the same video and
	// holds frames back; one thread still decodes the biggest stream (2592x1520
	// HEVC) 2.5x faster than real time there, and each camera has its own process
	// (FFMPEG_THREADS overrides it, for measurements; 0 = ffmpeg's default)
	threads := "1"
	if v := os.Getenv("FFMPEG_THREADS"); v != "" {
		threads = v
	}
	args = append(args, "-threads", threads)
	// quality trades, for comparing by eye (off by default): FFMPEG_SKIP_LOOP=1 skips
	// the deblocking filter (~5% of a core on the HD panorama, slightly blockier)
	if os.Getenv("FFMPEG_SKIP_LOOP") == "1" {
		args = append(args, "-skip_loop_filter", "all")
	}
	args = append(args, "-i", input, "-an", "-vsync", "passthrough")
	// YUV 4:2:0 as it comes from the camera (1.5 bytes per pixel, no colour
	// conversion here: the graphics card does it). Width a multiple of 8 (the
	// graphics card reads 4 bytes at a time), never wider than maxW.
	mw := maxW
	if mw <= 0 {
		mw = 1 << 16
	}
	// showinfo: one stderr line per frame with its time in the video (pts_time), so
	// the frames can be shown at the camera's real pace, like a video player does
	// (checksum=0: showinfo otherwise sums every picture, which the app never uses:
	// 6% of a core on the 2304x2592 panorama; older ffmpeg builds lack the option)
	args = append(args, "-vf", fmt.Sprintf("scale='trunc(min(%d,iw)/8)*8':-2:flags=%s,%s", mw, scaleFlags(), showinfoFilter()),
		"-f", "image2pipe", "-c:v", "pgmyuv", "-pix_fmt", "yuv420p", "-")
	cmd := exec.Command("ffmpeg", args...)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &lastLine{}
	pts := make(chan float64, 512)
	cmd.Stderr = &stderrSplit{tail: tail, pts: pts}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(stdout, 1<<20)
	type hdr struct {
		w, h int
		err  error
	}
	got := make(chan hdr, 1)
	go func() { w, h, err := readPGMHeader(br); got <- hdr{w, h, err} }()
	var first hdr
	select {
	case first = <-got:
	case <-time.After(25 * time.Second):
		cmd.Process.Kill()
		<-got
		cmd.Wait()
		return nil, fmt.Errorf("ffmpeg: sin video en 25 s")
	}
	if first.err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("ffmpeg: %v %s", first.err, redact(tail.get()))
	}
	d := &Decoder{cmd: cmd, pts: pts, Frames: make(chan *Frame, 2), W: first.w, H: first.h, Info: tail.find("Video:", "Failed", "failed", "hwaccel")}
	go d.read(br)
	return d, nil
}

// Frame is one decoded picture: YUV 4:2:0 laid out like ffmpeg's pgmyuv (W x H
// luma rows, then H/2 rows that each hold a U line and a V line of W/2 bytes),
// limited range, BT.601.
type Frame struct {
	W, H int
	Pix  []byte
	PTS  float64 // time in the video, seconds; -1 = unknown
}

// readPGMHeader reads one pgmyuv header ("P5\n<w> <h>\n255\n"); h is 3/2 of the
// picture height.
func readPGMHeader(r *bufio.Reader) (int, int, error) {
	var nums []int
	for len(nums) < 3 {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, 0, err
		}
		for _, f := range strings.Fields(line) {
			if f == "P5" {
				continue
			}
			n, err := strconv.Atoi(f)
			if err != nil {
				return 0, 0, fmt.Errorf("encabezado PGM raro: %q", line)
			}
			nums = append(nums, n)
		}
	}
	w, h := nums[0], nums[1]*2/3
	if w <= 0 || h <= 0 || w%4 != 0 {
		return 0, 0, fmt.Errorf("PGM de tamaño raro %dx%d", w, h)
	}
	return w, h, nil
}

// read: the first header was already read by Open; then frame, header, frame...
func (d *Decoder) read(r *bufio.Reader) {
	defer close(d.Frames)
	defer d.wait() // ffmpeg ended by itself: collect its exit, or it stays a zombie process
	w, h := d.W, d.H
	for {
		buf := make([]byte, w*h*3/2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		select {
		case d.Frames <- &Frame{W: w, H: h, Pix: buf, PTS: d.nextPTS()}:
		default: // drop a frame if the UI is behind, so we stay live
		}
		nw, nh, err := readPGMHeader(r)
		if err != nil {
			return
		}
		w, h = nw, nh
	}
}

// RGBA converts the frame on the processor (only for snapshots and the
// calibration; the screen converts on the graphics card).
func (f *Frame) RGBA() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, f.W, f.H))
	cw := f.W / 2
	for y := 0; y < f.H; y++ {
		crow := f.Pix[f.W*f.H+(y/2)*f.W:]
		for x := 0; x < f.W; x++ {
			Y := (float32(f.Pix[y*f.W+x]) - 16) * 1.164
			U := float32(crow[x/2]) - 128
			V := float32(crow[cw+x/2]) - 128
			o := (y*f.W + x) * 4
			img.Pix[o] = clamp8(Y + 1.596*V)
			img.Pix[o+1] = clamp8(Y - 0.392*U - 0.813*V)
			img.Pix[o+2] = clamp8(Y + 2.017*U)
			img.Pix[o+3] = 255
		}
	}
	return img
}

func clamp8(v float32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// Close stops ffmpeg.
func (d *Decoder) Close() {
	if d.cmd != nil && d.cmd.Process != nil {
		d.cmd.Process.Kill()
		go d.wait()
	}
}

// wait collects ffmpeg's exit once (Linux and Mac keep a zombie process until then).
func (d *Decoder) wait() { d.reap.Do(func() { d.cmd.Wait() }) }

// nextPTS: the time of the frame just read. ffmpeg writes its showinfo line before
// the frame, on another pipe, so wait a moment for it; -1 if it does not come.
func (d *Decoder) nextPTS() float64 {
	select {
	case p := <-d.pts:
		return p
	case <-time.After(300 * time.Millisecond):
		return -1
	}
}

// stderrSplit takes the per-frame showinfo lines out of ffmpeg's stderr (their
// times go to pts); every other line goes to the error tail as before.
type stderrSplit struct {
	tail *lastLine
	pts  chan float64
	part []byte
}

func (s *stderrSplit) Write(p []byte) (int, error) {
	s.part = append(s.part, p...)
	for {
		i := bytes.IndexByte(s.part, '\n')
		if i < 0 {
			break
		}
		ln := s.part[:i+1]
		if j := bytes.Index(ln, []byte("pts_time:")); j >= 0 {
			f := strings.Fields(string(ln[j+len("pts_time:"):]))
			t := -1.0
			if len(f) > 0 {
				if v, err := strconv.ParseFloat(f[0], 64); err == nil {
					t = v
				}
			}
			select {
			case s.pts <- t:
			default: // nobody reads (should not happen): never block ffmpeg
			}
		} else if !bytes.Contains(ln, []byte("Parsed_showinfo")) {
			s.tail.Write(ln)
		}
		s.part = s.part[i+1:]
	}
	if len(s.part) > 4096 { // a line that never ends: keep the tail working
		s.tail.Write(s.part)
		s.part = nil
	}
	return len(p), nil
}

// lastLine keeps the last error line ffmpeg wrote (to say why it stopped).
type lastLine struct {
	mu  sync.Mutex
	buf []byte
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.buf = append(l.buf, p...)
	if len(l.buf) > 8192 {
		l.buf = l.buf[len(l.buf)-8192:]
	}
	l.mu.Unlock()
	return len(p), nil
}

func (l *lastLine) get() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := strings.TrimSpace(string(l.buf))
	if i := strings.LastIndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	}
	return "(" + t + ")"
}

// find returns the lines (redacted, joined) that hold any of the words.
func (l *lastLine) find(words ...string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, ln := range strings.Split(string(l.buf), "\n") {
		for _, w := range words {
			if strings.Contains(ln, w) {
				out = append(out, strings.TrimSpace(redact(ln)))
				break
			}
		}
	}
	return strings.Join(out, " | ")
}

var (
	showinfoOnce sync.Once
	showinfoArg  = "showinfo"
)

// showinfoFilter is "showinfo=checksum=0" when this ffmpeg knows the option (asked once).
func showinfoFilter() string {
	showinfoOnce.Do(func() {
		cmd := exec.Command("ffmpeg", "-hide_banner", "-h", "filter=showinfo")
		hideWindow(cmd)
		if out, err := cmd.Output(); err == nil && strings.Contains(string(out), "checksum") {
			showinfoArg = "showinfo=checksum=0"
		}
	})
	return showinfoArg
}

// scaleFlags: how ffmpeg shrinks; FFMPEG_SCALE=neighbor is cheaper (~5%) but has jaggier edges
func scaleFlags() string {
	if v := os.Getenv("FFMPEG_SCALE"); v == "neighbor" || v == "bilinear" || v == "area" {
		return v
	}
	return "fast_bilinear"
}
