// Package compartir packs the app's whole configuration (serial, NVR user and
// password, the camaras.ini settings and the hd/orden/panoramica lists; not the
// panorama calibration) into one link for a QR code:
//
//	camaras://config?v=1&d=<base64url, no padding, of the JSON of Config>
//
// The Android app opens these links (scanned with the phone's camera) and asks
// before importing. The desktop app, the Android app and mostrar make them.
// The link holds the NVR password: show it only when the user asks for it, and
// never write it to a log.
package compartir

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	Scheme  = "camaras"
	Host    = "config"
	Version = 1
	maxData = 3000 // more than any real configuration, less than a QR code holds
)

// Config is what travels. Empty fields are left out of the link; an empty
// password means "keep the one already stored".
type Config struct {
	Serial string            `json:"sn,omitempty"`
	User   string            `json:"u,omitempty"`
	Pass   string            `json:"p,omitempty"`
	Ini    map[string]string `json:"ini,omitempty"`   // camaras.ini keys (without sn)
	HD     string            `json:"hd,omitempty"`    // hd.txt: channels in HD, "1,3"
	Orden  string            `json:"orden,omitempty"` // orden.txt: "0,2.1,2.2,1"
	Pano   map[string]int    `json:"pano,omitempty"`  // panoramica.txt: channel -> mode (3 on, 0 off)
}

// Encode gives the link for c.
func Encode(c Config) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	d := base64.RawURLEncoding.EncodeToString(b)
	if len(d) > maxData {
		return "", errors.New("la configuracion es demasiado grande para un QR")
	}
	return fmt.Sprintf("%s://%s?v=%d&d=%s", Scheme, Host, Version, d), nil
}

// Decode reads a link made by Encode.
func Decode(link string) (Config, error) {
	var c Config
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !strings.EqualFold(u.Scheme, Scheme) || !strings.EqualFold(u.Host, Host) {
		return c, errors.New("no es un enlace de configuracion de la app de camaras")
	}
	q := u.Query()
	v, err := strconv.Atoi(q.Get("v"))
	if err != nil || v < 1 {
		return c, errors.New("enlace de configuracion sin version")
	}
	if v > Version {
		return c, errors.New("la configuracion viene de una version mas nueva de la app: actualiza esta")
	}
	d := q.Get("d")
	if d == "" || len(d) > maxData {
		return c, errors.New("enlace de configuracion vacio o demasiado largo")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(d, "="))
	if err != nil {
		return c, errors.New("enlace de configuracion danado")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, errors.New("enlace de configuracion danado")
	}
	if c.Serial == "" {
		return c, errors.New("al enlace le falta el numero de serie")
	}
	return c, nil
}

// FromFolder fills the settings of a desktop settings folder: ini is camaras.ini
// already read (key=value); hd.txt, orden.txt and panoramica.txt are read from
// dir. The PTZ settings always go with their value (the desktop defaults when
// they are not set), so the other device behaves the same. Serial, user and
// password are left to the caller.
func FromFolder(dir string, ini map[string]string) Config {
	c := Config{Ini: map[string]string{}}
	for k, v := range ini {
		if k != "sn" {
			c.Ini[k] = v
		}
	}
	for k, def := range map[string]string{"ptz": "", "ptz_velocidad": "100", "ptz_invertir_pan": "0"} {
		if c.Ini[k] == "" {
			c.Ini[k] = def
		}
	}
	c.HD = list(filepath.Join(dir, "hd.txt"))
	c.Orden = list(filepath.Join(dir, "orden.txt"))
	if b, err := os.ReadFile(filepath.Join(dir, "panoramica.txt")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			ch, e1 := strconv.Atoi(f[0])
			m, e2 := strconv.Atoi(f[1])
			if e1 == nil && e2 == nil {
				if c.Pano == nil {
					c.Pano = map[string]int{}
				}
				c.Pano[strconv.Itoa(ch)] = m
			}
		}
	}
	return c
}

// list reads a comma separated file into "a,b,c" (spaces and empty items dropped).
func list(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var out []string
	for _, s := range strings.Split(strings.TrimSpace(string(b)), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

// Summary describes c for a person, with the password hidden.
func (c Config) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Numero de serie: %s\nUsuario: %s\n", c.Serial, or(c.User, "admin"))
	if c.Pass != "" {
		b.WriteString("Clave: ******** (incluida)\n")
	} else {
		b.WriteString("Clave: no incluida\n")
	}
	var keys []string
	for k := range c.Ini {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, c.Ini[k])
	}
	if c.HD != "" {
		fmt.Fprintf(&b, "hd: %s\n", c.HD)
	}
	if c.Orden != "" {
		fmt.Fprintf(&b, "orden: %s\n", c.Orden)
	}
	var chs []string
	for ch := range c.Pano {
		chs = append(chs, ch)
	}
	sort.Strings(chs)
	for _, ch := range chs {
		fmt.Fprintf(&b, "panoramica canal %s: %d\n", ch, c.Pano[ch])
	}
	return b.String()
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
