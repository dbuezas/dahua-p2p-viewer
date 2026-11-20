package calib

import (
	"image"
	"os"
	"path/filepath"
	"testing"
)

func TestRGBAPath(t *testing.T) {
	dir := os.Getenv("CALIB_DIR") // folder with the camera pictures and mesh
	if dir == "" {
		t.Skip("CALIB_DIR not set")
	}
	rgb, _ := os.ReadFile(filepath.Join(dir, "hd_s.rgb"))
	// make an RGBA version of the 640x720 frame
	rgba := make([]byte, 640*720*4)
	for i := 0; i < 640*720; i++ {
		rgba[i*4] = rgb[i*3]
		rgba[i*4+1] = rgb[i*3+1]
		rgba[i*4+2] = rgb[i*3+2]
		rgba[i*4+3] = 255
	}
	st, err := CalibrateRGB24(rgb, -45, nil)
	if err != nil {
		t.Fatal(err)
	}
	lut := BuildLut(st)
	out := image.NewRGBA(image.Rect(0, 0, lut.W, lut.H))
	lut.RenderRGBA(rgba, out)
	// sanity: center pixel not black
	cx := (lut.H/2*lut.W + lut.W/2) * 4
	if out.Pix[cx] == 0 && out.Pix[cx+1] == 0 && out.Pix[cx+2] == 0 {
		t.Fatal("centro negro: el render RGBA no funciono")
	}
	t.Logf("RGBA ok, panorama %dx%d, centro=(%d,%d,%d)", lut.W, lut.H, out.Pix[cx], out.Pix[cx+1], out.Pix[cx+2])
}
