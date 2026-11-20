package calib

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCalibrate(t *testing.T) {
	dir := os.Getenv("CALIB_DIR") // folder with the camera pictures and mesh
	if dir == "" {
		t.Skip("CALIB_DIR not set")
	}
	o := buildOpts(map[string]any{}, -45)
	r := runPic(filepath.Join(dir, "hd_s.rgb"), o)
	if !r.ok {
		t.Fatalf("calibracion fallo")
	}
	t.Logf("strip match = %.4f (esperado ~0.60)", r.strip)
	if r.strip < 0.5 {
		t.Fatalf("strip muy bajo: %.4f", r.strip)
	}
}
