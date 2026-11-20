package calib

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLut(t *testing.T) {
	dir := os.Getenv("CALIB_DIR") // folder with the camera pictures and mesh
	if dir == "" {
		t.Skip("CALIB_DIR not set")
	}
	path := filepath.Join(dir, "hd_s.rgb")
	r := runPic(path, buildOpts(map[string]any{}, -45))
	if !r.ok {
		t.Fatal("calib fallo")
	}
	frame, _ := os.ReadFile(path)
	lut := BuildLut(r.st)
	out := image.NewRGBA(image.Rect(0, 0, lut.W, lut.H))
	// time 100 frames
	t0 := time.Now()
	for i := 0; i < 100; i++ {
		lut.RenderRGB24(frame, out)
	}
	ms := time.Since(t0).Seconds() * 1000 / 100
	t.Logf("recorte %dx%d, %.2f ms por cuadro (%.0f fps posibles)", lut.W, lut.H, ms, 1000/ms)
	f, _ := os.Create("/tmp/pano_lut.png")
	png.Encode(f, out)
	f.Close()
}
