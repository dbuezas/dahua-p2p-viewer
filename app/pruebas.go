package main

import (
	"bufio"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Test buttons on the phone remote, for comparing settings by eye on the TV. Optional: only when the folder of
// camaras.ini has pruebas.txt, one row per test:
//
//	Calidad | Normal=calidad normal | Sin filtro=calidad sinfiltro
//
// The phone only picks a row and an option ("prueba row option"); the command comes from this file, never from
// the phone. The last option picked per row is kept in pruebas.estado (a command may restart this app).

type prueba struct {
	title string
	names []string
	cmds  []string
	sel   int // -1 = none picked yet
}

func (g *game) loadPruebas() {
	f, err := os.Open(filepath.Join(g.cfg.dir, "pruebas.txt"))
	if err != nil {
		return
	}
	defer f.Close()
	picked := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(g.cfg.dir, "pruebas.estado")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, "="); ok {
				picked[k] = v
			}
		}
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		parts := strings.Split(l, "|")
		p := prueba{title: strings.TrimSpace(parts[0]), sel: -1}
		for _, o := range parts[1:] {
			if n, c, ok := strings.Cut(o, "="); ok {
				p.names = append(p.names, strings.TrimSpace(n))
				p.cmds = append(p.cmds, strings.TrimSpace(c))
				if picked[p.title] == strings.TrimSpace(n) {
					p.sel = len(p.names) - 1
				}
			}
		}
		if len(p.names) > 0 {
			g.pruebas = append(g.pruebas, p)
		}
	}
}

// runPrueba runs option j of row i. The caller holds g.mu.
func (g *game) runPrueba(i, j int) {
	if i < 0 || i >= len(g.pruebas) || j < 0 || j >= len(g.pruebas[i].names) {
		return
	}
	p := &g.pruebas[i]
	p.sel = j
	var b strings.Builder
	for _, q := range g.pruebas {
		if q.sel >= 0 {
			b.WriteString(q.title + "=" + q.names[q.sel] + "\n")
		}
	}
	os.WriteFile(filepath.Join(g.cfg.dir, "pruebas.estado"), []byte(b.String()), 0o644)
	log.Printf("prueba: %s = %s", p.title, p.names[j])
	c := exec.Command("sh", "-c", p.cmds[j])
	if err := c.Start(); err != nil {
		log.Printf("prueba: %v", err)
		return
	}
	go c.Wait()
	g.stateChanged()
}

// pruebasJSON: for the pages' state. The caller holds g.mu.
func (g *game) pruebasJSON() any {
	if len(g.pruebas) == 0 {
		return nil
	}
	out := []map[string]any{}
	for _, p := range g.pruebas {
		out = append(out, map[string]any{"t": p.title, "o": p.names, "sel": p.sel})
	}
	return out
}
