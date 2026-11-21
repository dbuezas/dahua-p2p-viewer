package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// config reads the same settings files as the old PC app, so both share them:
// camaras.ini (key=value), hd.txt, orden.txt and panoramica.txt (plus this app's own
// tamanos.txt). They live next to the exe when a camaras.ini is there (the PC), else in the user's config folder.
type config struct {
	dir string
	kv  map[string]string
}

func loadConfig() *config {
	c := &config{kv: map[string]string{}}
	var cands []string
	if d := os.Getenv("CAMARAS_DIR"); d != "" {
		cands = append(cands, d)
	}
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		cands = append(cands, wd)
	}
	for _, d := range cands {
		if _, err := os.Stat(filepath.Join(d, "camaras.ini")); err == nil {
			c.dir = d
			break
		}
	}
	if c.dir == "" {
		if ud, err := os.UserConfigDir(); err == nil {
			c.dir = filepath.Join(ud, "camaras-go")
		} else {
			c.dir = "."
		}
		os.MkdirAll(c.dir, 0700)
	}
	if f, err := os.Open(filepath.Join(c.dir, "camaras.ini")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if l == "" || strings.HasPrefix(l, ";") || strings.HasPrefix(l, "#") {
				continue
			}
			if i := strings.Index(l, "="); i > 0 {
				c.kv[strings.TrimSpace(l[:i])] = strings.TrimSpace(l[i+1:])
			}
		}
		f.Close()
	}
	return c
}

func (c *config) get(k, def string) string {
	if v, ok := c.kv[k]; ok && v != "" {
		return v
	}
	return def
}

func (c *config) getInt(k string, def int) int {
	if v, err := strconv.Atoi(c.get(k, "")); err == nil {
		return v
	}
	return def
}

func (c *config) path(name string) string { return filepath.Join(c.dir, name) }

// list reads a comma separated file (hd.txt, orden.txt).
func (c *config) list(name string) []string {
	b, err := os.ReadFile(c.path(name))
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range strings.Split(strings.TrimSpace(string(b)), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (c *config) writeList(name string, items []string) {
	os.WriteFile(c.path(name), []byte(strings.Join(items, ",")), 0644)
}

// ints parses a comma separated setting into numbers (e.g. dividir=2).
func ints(s string) map[int]bool {
	out := map[int]bool{}
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out[n] = true
		}
	}
	return out
}

// panoModes reads panoramica.txt: "channel mode" per line (mode 3 = panorama on, 0 = off).
func (c *config) panoModes() map[int]int {
	out := map[int]int{}
	b, err := os.ReadFile(c.path("panoramica.txt"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		ch, e1 := strconv.Atoi(f[0])
		m, e2 := strconv.Atoi(f[1])
		if e1 == nil && e2 == nil {
			out[ch] = m
		}
	}
	return out
}

func (c *config) writePanoModes(m map[int]int) {
	var b strings.Builder
	for ch, mode := range m {
		b.WriteString(strconv.Itoa(ch) + " " + strconv.Itoa(mode) + "\n")
	}
	os.WriteFile(c.path("panoramica.txt"), []byte(b.String()), 0644)
}

// tamanos.txt: "channel sub pixels" per line, each camera's picture size as last seen
// (sub 0 = HD, 1 = the small stream), so the next start can pick the decoding threads.
var srcPixMu sync.Mutex

func (c *config) srcPixels(ch, sub int) int {
	srcPixMu.Lock()
	defer srcPixMu.Unlock()
	return c.readSrcPixels()[[2]int{ch, sub}]
}

func (c *config) setSrcPixels(ch, sub, pix int) {
	srcPixMu.Lock()
	defer srcPixMu.Unlock()
	m := c.readSrcPixels()
	m[[2]int{ch, sub}] = pix
	var b strings.Builder
	for k, v := range m {
		fmt.Fprintf(&b, "%d %d %d\n", k[0], k[1], v)
	}
	os.WriteFile(c.path("tamanos.txt"), []byte(b.String()), 0644)
}

func (c *config) readSrcPixels() map[[2]int]int {
	out := map[[2]int]int{}
	b, _ := os.ReadFile(c.path("tamanos.txt"))
	for _, line := range strings.Split(string(b), "\n") {
		var ch, sub, pix int
		if n, _ := fmt.Sscanf(line, "%d %d %d", &ch, &sub, &pix); n == 3 {
			out[[2]int{ch, sub}] = pix
		}
	}
	return out
}
