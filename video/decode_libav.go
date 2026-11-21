//go:build libav

// ffmpeg's libraries inside the app (build with -tags libav; needs the libavformat,
// libavcodec, libavutil and libswscale development files and cgo). The same Decoder
// API as the subprocess (decode.go), plus what a separate process cannot do:
//
//   - a live view never decodes stale video: packets wait in a queue between the
//     network and the decoder, and when more than skipAfter of video waits there
//     (a burst after a stall, or a decoder slower than the camera) the decoder jumps
//     to the newest key frame in the queue. Frames between key frames need the ones
//     before them, so the jump can only land on a key frame.
//   - no pipe: frames go straight from the decoder into the app's memory.
//   - no separate ffmpeg program to install.
//
// A crash inside ffmpeg ends the whole app (with the subprocess only that camera's
// ffmpeg stops); correr.sh on the TV PC opens it again.
package video

/*
#cgo pkg-config: libavformat libavcodec libavutil libswscale
#include <stdlib.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libswscale/swscale.h>

// The interrupt flag lives in C memory: ffmpeg calls this from inside its network
// reads, and a non-zero flag makes them give up (Close, or the start timeout).
static int interrupted(void *p) { return *(volatile int *)p; }
static void set_interrupt(AVFormatContext *f, int *flag) {
	f->interrupt_callback.callback = interrupted;
	f->interrupt_callback.opaque = flag;
}
static AVStream *stream_at(AVFormatContext *f, int i) { return f->streams[i]; }
static int err_again(void) { return AVERROR(EAGAIN); }
static int err_eof(void) { return AVERROR_EOF; }
static int64_t nopts(void) { return AV_NOPTS_VALUE; }
// scale into the app's YUV 4:2:0 layout: W x H luma rows, then H/2 rows that each
// hold a U line and a V line of W/2 bytes (planes interleaved by row, stride W)
static int scale_into(struct SwsContext *sws, AVFrame *f, uint8_t *dst, int w, int h) {
	uint8_t *d[4] = {dst, dst + w * h, dst + w * h + w / 2, NULL};
	int ls[4] = {w, w, w, 0};
	return sws_scale(sws, (const uint8_t *const *)f->data, f->linesize, 0, f->height, d, ls);
}
*/
import "C"

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// skipAfter: more video than this waiting for the decoder = jump to the newest key
// frame (the app shows ~1.2 s behind real time; it jumps by itself past 3.6 s).
const skipAfter = 1.5

// Decoder streams frames from an input (RTSP URL or file path).
type Decoder struct {
	Frames chan *Frame
	W, H   int
	SrcW   int // the camera's picture size, before any shrinking (0 = unknown)
	SrcH   int
	Info   string // the input video (codec, size, rate) and the decoding threads

	stop *C.int // the interrupt flag (C memory)
	fmt  *C.AVFormatContext
	cod  *C.AVCodecContext
	sws  *C.struct_SwsContext
	si   C.int   // video stream index
	tb   float64 // seconds per pts tick
	maxW int
	name string // for the log: "channel=N" from the address

	mu       sync.Mutex
	reading  bool // the reader goroutine runs (free waits for it)
	freed    bool // everything released (Close must not touch the flag any more)
	timedOut atomic.Bool
	queue    []*C.AVPacket // read, not decoded yet
	readEnd  bool          // the reader stopped (end, error or Close)
	wake     chan struct{} // the reader added packets
}

var (
	netOnce sync.Once
	noPTS   = C.nopts()
	chanRe  = regexp.MustCompile(`channel=\d+`)
)

func averr(n C.int) string {
	buf := make([]byte, 128)
	C.av_strerror(n, (*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

// OpenScaled: like OpenProbe; maxW > 0 makes ffmpeg shrink wider pictures to that
// width; threads = decoding threads (below 1 counts as 1).
func OpenScaled(input string, rtspTCP, long bool, maxW, threads int) (*Decoder, error) {
	netOnce.Do(func() {
		C.avformat_network_init()
		C.av_log_set_level(C.AV_LOG_ERROR)
	})
	d := &Decoder{Frames: make(chan *Frame, 2), wake: make(chan struct{}, 1), maxW: maxW, name: chanRe.FindString(input)}
	d.stop = (*C.int)(C.calloc(1, C.size_t(unsafe.Sizeof(C.int(0)))))
	// starting must not hang: give up after 25 s, like the subprocess
	startTimer := time.AfterFunc(25*time.Second, func() { d.timedOut.Store(true); d.Close() })
	defer startTimer.Stop()
	if err := d.openInput(input, rtspTCP, long, threads); err != nil {
		d.free()
		if d.timedOut.Load() {
			return nil, fmt.Errorf("ffmpeg: sin video en 25 s")
		}
		return nil, fmt.Errorf("ffmpeg: %s", redact(err.Error()))
	}
	d.reading = true
	go d.read()
	first := make(chan error, 1)
	go d.decode(first)
	if err := <-first; err != nil {
		d.Close()
		if d.timedOut.Load() {
			return nil, fmt.Errorf("ffmpeg: sin video en 25 s")
		}
		return nil, fmt.Errorf("ffmpeg: %s", redact(err.Error()))
	}
	return d, nil
}

func (d *Decoder) openInput(input string, rtspTCP, long bool, threads int) error {
	d.fmt = C.avformat_alloc_context()
	C.set_interrupt(d.fmt, d.stop)
	var opts *C.AVDictionary
	set := func(k, v string) {
		ck, cv := C.CString(k), C.CString(v)
		C.av_dict_set(&opts, ck, cv, 0)
		C.free(unsafe.Pointer(ck))
		C.free(unsafe.Pointer(cv))
	}
	size, dur := "1000000", "2000000"
	if long {
		size, dur = "5000000", "8000000"
	}
	set("probesize", size)
	set("analyzeduration", dur)
	if rtspTCP {
		set("rtsp_transport", "tcp")
	}
	set("timeout", "15000000") // a silent connection gives up after 15 s (the app's watchdog restarts it)
	cin := C.CString(input)
	defer C.free(unsafe.Pointer(cin))
	ret := C.avformat_open_input(&d.fmt, cin, nil, &opts)
	C.av_dict_free(&opts)
	if ret < 0 {
		d.fmt = nil // freed by avformat_open_input on failure
		return fmt.Errorf("%s (%s)", averr(ret), input)
	}
	if ret = C.avformat_find_stream_info(d.fmt, nil); ret < 0 {
		return fmt.Errorf("%s", averr(ret))
	}
	var codec *C.AVCodec
	d.si = C.av_find_best_stream(d.fmt, C.AVMEDIA_TYPE_VIDEO, -1, -1, &codec, 0)
	if d.si < 0 || codec == nil {
		return fmt.Errorf("sin video: %s", averr(d.si))
	}
	st := C.stream_at(d.fmt, d.si)
	d.tb = float64(st.time_base.num) / float64(st.time_base.den)
	// only the video packets reach the queue
	for i := C.uint(0); i < d.fmt.nb_streams; i++ {
		if C.int(i) != d.si {
			C.stream_at(d.fmt, C.int(i)).discard = C.AVDISCARD_ALL
		}
	}
	d.cod = C.avcodec_alloc_context3(codec)
	if ret = C.avcodec_parameters_to_context(d.cod, st.codecpar); ret < 0 {
		return fmt.Errorf("%s", averr(ret))
	}
	// few decoding threads, chosen by the caller (see decode.go); FFMPEG_THREADS overrides
	th := max(threads, 1)
	if v, err := strconv.Atoi(os.Getenv("FFMPEG_THREADS")); err == nil {
		th = v
	}
	d.cod.thread_count = C.int(th)
	if os.Getenv("FFMPEG_SKIP_LOOP") == "1" {
		d.cod.skip_loop_filter = C.AVDISCARD_ALL
	}
	if ret = C.avcodec_open2(d.cod, codec, nil); ret < 0 {
		return fmt.Errorf("%s", averr(ret))
	}
	d.SrcW, d.SrcH = int(st.codecpar.width), int(st.codecpar.height)
	fps := ""
	if r := st.avg_frame_rate; r.den > 0 && r.num > 0 {
		fps = fmt.Sprintf(", %.3g fps", float64(r.num)/float64(r.den))
	}
	d.Info = fmt.Sprintf("Video: %s, %dx%d%s (ffmpeg en la app, %d hilo(s))",
		C.GoString(codec.name), d.SrcW, d.SrcH, fps, th)
	return nil
}

// read moves packets from the network into the queue until the end, an error or Close.
func (d *Decoder) read() {
	for {
		pkt := C.av_packet_alloc()
		if ret := C.av_read_frame(d.fmt, pkt); ret < 0 {
			C.av_packet_free(&pkt)
			if *d.stop == 0 {
				log.Printf("video %s: la lectura termino: %s", d.name, averr(ret))
			}
			break
		}
		if pkt.stream_index != d.si {
			C.av_packet_free(&pkt)
			continue
		}
		d.mu.Lock()
		d.queue = append(d.queue, pkt)
		d.mu.Unlock()
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
	d.mu.Lock()
	d.readEnd = true
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// next takes the next packet to decode; nil when the reader has ended and the
// queue is empty. Too much video waiting: everything before the newest key frame
// is dropped, and the decoder starts again from that key frame.
func (d *Decoder) next() *C.AVPacket {
	for {
		d.mu.Lock()
		if n := len(d.queue); n > 0 {
			if span := d.span(); span > skipAfter {
				for k := n - 1; k > 0; k-- {
					if d.queue[k].flags&C.AV_PKT_FLAG_KEY != 0 {
						for _, p := range d.queue[:k] {
							C.av_packet_free(&p)
						}
						d.queue = append(d.queue[:0], d.queue[k:]...)
						C.avcodec_flush_buffers(d.cod)
						log.Printf("video %s: %.1f s esperando, salto al ultimo cuadro clave (%d paquetes sin decodificar)", d.name, span, k)
						break
					}
				}
			}
			p := d.queue[0]
			d.queue = d.queue[1:]
			d.mu.Unlock()
			return p
		}
		end := d.readEnd
		d.mu.Unlock()
		if end {
			return nil
		}
		<-d.wake
	}
}

// span: seconds of video in the queue, by the packets' times. The caller holds d.mu.
func (d *Decoder) span() float64 {
	a, b := d.queue[0].dts, d.queue[len(d.queue)-1].dts
	if a == noPTS || b == noPTS {
		a, b = d.queue[0].pts, d.queue[len(d.queue)-1].pts
	}
	if a == noPTS || b == noPTS || b < a {
		return 0
	}
	return float64(b-a) * d.tb
}

// decode turns packets into frames for the app. first gets nil after the first
// frame (W and H set) or the reason it never came.
func (d *Decoder) decode(first chan error) {
	defer d.free()
	defer close(d.Frames)
	frame := C.av_frame_alloc()
	defer C.av_frame_free(&frame)
	started := false
	fail := func(err error) {
		if !started {
			first <- err
			started = true
		}
	}
	defer fail(fmt.Errorf("fin"))
	// drain takes every frame the decoder has ready; false = stop decoding
	drain := func() bool {
		for {
			ret := C.avcodec_receive_frame(d.cod, frame)
			if ret == C.err_again() {
				return true
			}
			if ret == C.err_eof() {
				return false
			}
			if ret < 0 {
				fail(fmt.Errorf("%s", averr(ret)))
				return false
			}
			f := d.convert(frame)
			if f != nil && !started {
				d.W, d.H = f.W, f.H
				if d.SrcW == 0 {
					d.SrcW, d.SrcH = int(frame.width), int(frame.height)
				}
				first <- nil
				started = true
			}
			C.av_frame_unref(frame)
			if f == nil {
				continue
			}
			select {
			case d.Frames <- f:
			default: // drop a frame if the app is behind, so it stays live
			}
		}
	}
	for {
		pkt := d.next()
		end := pkt == nil // nil = the reader ended: drain the decoder and stop
		ret := C.avcodec_send_packet(d.cod, pkt)
		// the decoder is full: take its frames, then give it the same packet again
		// (dropping it would spoil the picture until the next key frame)
		for ret == C.err_again() {
			if !drain() {
				C.av_packet_free(&pkt)
				return
			}
			ret = C.avcodec_send_packet(d.cod, pkt)
		}
		C.av_packet_free(&pkt) // sets pkt to nil: test end, not pkt, below
		if !drain() || end {
			return
		}
	}
}

// convert shrinks the frame to at most maxW wide (width a multiple of 8, the
// graphics card reads 4 bytes at a time; height keeps the shape, even) into the
// app's YUV 4:2:0 layout, limited range, like the subprocess's scale filter.
func (d *Decoder) convert(f *C.AVFrame) *Frame {
	iw, ih := int(f.width), int(f.height)
	if iw <= 0 || ih <= 0 {
		return nil
	}
	mw := d.maxW
	if mw <= 0 {
		mw = 1 << 16
	}
	w := min(mw, iw) / 8 * 8
	h := (ih*w/iw + 1) / 2 * 2
	if w <= 0 || h <= 0 {
		return nil
	}
	d.sws = C.sws_getCachedContext(d.sws, f.width, f.height, C.enum_AVPixelFormat(f.format),
		C.int(w), C.int(h), C.AV_PIX_FMT_YUV420P, swsFlags(), nil, nil, nil)
	if d.sws == nil {
		return nil
	}
	buf := make([]byte, w*h*3/2)
	C.scale_into(d.sws, f, (*C.uint8_t)(unsafe.Pointer(&buf[0])), C.int(w), C.int(h))
	pts := -1.0
	if t := f.best_effort_timestamp; t != noPTS {
		pts = float64(t) * d.tb
	}
	return &Frame{W: w, H: h, Pix: buf, PTS: pts}
}

// swsFlags: how frames are shrunk; FFMPEG_SCALE as in decode.go
func swsFlags() C.int {
	switch os.Getenv("FFMPEG_SCALE") {
	case "neighbor":
		return C.SWS_POINT
	case "bilinear":
		return C.SWS_BILINEAR
	case "area":
		return C.SWS_AREA
	}
	return C.SWS_FAST_BILINEAR
}

// Close stops the decoder: the network read gives up, then everything is freed.
func (d *Decoder) Close() {
	d.mu.Lock()
	if !d.freed {
		*d.stop = 1
	}
	d.mu.Unlock()
}

// free releases everything once the decoder ends (after the reader has stopped).
func (d *Decoder) free() {
	d.mu.Lock()
	done := d.freed
	d.mu.Unlock()
	if done {
		return
	}
	*d.stop = 1
	for {
		d.mu.Lock()
		end := d.readEnd || !d.reading
		d.mu.Unlock()
		if end {
			break
		}
		select {
		case <-d.wake:
		case <-time.After(100 * time.Millisecond):
		}
	}
	for _, p := range d.queue {
		C.av_packet_free(&p)
	}
	d.queue = nil
	if d.sws != nil {
		C.sws_freeContext(d.sws)
		d.sws = nil
	}
	if d.cod != nil {
		C.avcodec_free_context(&d.cod)
	}
	if d.fmt != nil {
		C.avformat_close_input(&d.fmt)
	}
	d.mu.Lock()
	d.freed = true
	C.free(unsafe.Pointer(d.stop))
	d.mu.Unlock()
}
