// Package video decodes an RTSP stream (or a file) to frames, for the desktop
// build. Two decoders give the same Decoder API: ffmpeg as a subprocess (decode.go,
// the default) and ffmpeg's libraries inside the app (decode_libav.go, built with
// -tags libav). Android uses MediaCodec instead (same Frames channel).
package video

import (
	"image"
	"regexp"
)

// credRe matches the user:password part of a URL, so errors never show it.
var credRe = regexp.MustCompile(`//[^/@\s]*@`)

// srcSizeRe finds the picture size in ffmpeg's first "Video:" line ("..., 2304x2592, ...").
var srcSizeRe = regexp.MustCompile(`Video:[^|]*?, (\d{2,5})x(\d{2,5})`)

// redact hides URL credentials in a message.
func redact(s string) string { return credRe.ReplaceAllString(s, "//***@") }

// Open starts decoding. For RTSP use rtsp:// ... ; tcp forces TCP transport.
func Open(input string, rtspTCP bool) (*Decoder, error) {
	return OpenProbe(input, rtspTCP, false)
}

// OpenProbe: long = read more of the stream before starting (some cameras only
// show their size later).
func OpenProbe(input string, rtspTCP, long bool) (*Decoder, error) {
	return OpenScaled(input, rtspTCP, long, 0, 1)
}

// Frame is one decoded picture: YUV 4:2:0 laid out like ffmpeg's pgmyuv (W x H
// luma rows, then H/2 rows that each hold a U line and a V line of W/2 bytes),
// limited range, BT.601.
type Frame struct {
	W, H int
	Pix  []byte
	PTS  float64 // time in the video, seconds; -1 = unknown
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
