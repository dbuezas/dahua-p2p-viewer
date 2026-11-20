package calib

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestRender(t *testing.T) {
	dir := os.Getenv("CALIB_DIR") // folder with the camera pictures and mesh
	if dir == "" {
		t.Skip("CALIB_DIR not set")
	}
	path := filepath.Join(dir, "hd_s.rgb")
	o := buildOpts(map[string]any{}, -45)
	r := runPic(path, o)
	if !r.ok {
		t.Fatal("calib fallo")
	}
	frame, _ := os.ReadFile(path)
	img := RenderFull(r.st, frame)
	f, _ := os.Create("/tmp/pano.png")
	png.Encode(f, img)
	f.Close()
	t.Logf("panorama %dx%d guardada en /tmp/pano.png", img.Bounds().Dx(), img.Bounds().Dy())
}
