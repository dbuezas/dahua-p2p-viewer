package compartir

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fake() Config {
	return Config{
		Serial: "FAKESERIAL0123456",
		User:   "admin",
		Pass:   "n0t-a-real p@ss/&=?",
		Ini:    map[string]string{"ptz": "2.2", "dividir": "2", "ptz_velocidad": "40", "ptz_invertir_pan": "1", "colchon_ms": "1500"},
		HD:     "1,3",
		Orden:  "0,2.1,2.2,1",
		Pano:   map[string]int{"2": 3},
	}
}

func TestRoundTrip(t *testing.T) {
	c := fake()
	link, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "camaras://config?v=1&d="
	if !strings.HasPrefix(link, prefix) {
		t.Fatalf("link %q", link)
	}
	if strings.ContainsAny(link[len(prefix):], "+/=&?") {
		t.Fatalf("data not url-safe: %q", link)
	}
	got, err := Decode(link)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("got %+v\nwant %+v", got, c)
	}
	if len(link) > 600 {
		t.Errorf("link is %d long, the QR gets big", len(link))
	}
}

func TestDecodeErrors(t *testing.T) {
	good, _ := Encode(fake())
	noSerial, _ := Encode(Config{User: "x"})
	for _, l := range []string{
		"",
		"https://example.com/?v=1&d=e30",
		"camaras://otra?v=1&d=e30",
		"camaras://config?d=e30",
		"camaras://config?v=2&d=e30",
		"camaras://config?v=1&d=%%%",
		"camaras://config?v=1&d=bm90IGpzb24",
		noSerial,
		strings.Replace(good, "v=1", "v=x", 1),
	} {
		if _, err := Decode(l); err == nil {
			t.Errorf("%q: no error", l)
		}
	}
}

func TestSummaryHidesPassword(t *testing.T) {
	c := fake()
	if s := c.Summary(); strings.Contains(s, c.Pass) || !strings.Contains(s, c.Serial) {
		t.Fatalf("summary %q", s)
	}
}

func TestFromFolder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hd.txt"), []byte(" 1, 3 ,\n"), 0644)
	os.WriteFile(filepath.Join(dir, "orden.txt"), []byte("0,2.1,2.2,1"), 0644)
	os.WriteFile(filepath.Join(dir, "panoramica.txt"), []byte("2 3\n5 0\nbad\n"), 0644)
	c := FromFolder(dir, map[string]string{"sn": "FAKESERIAL0123456", "dividir": "2", "ptz_velocidad": "40"})
	want := Config{
		Ini:   map[string]string{"dividir": "2", "ptz": "", "ptz_velocidad": "40", "ptz_invertir_pan": "0"},
		HD:    "1,3",
		Orden: "0,2.1,2.2,1",
		Pano:  map[string]int{"2": 3, "5": 0},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}
