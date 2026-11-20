package main

import "nvrlib"

// ptzCtl hands the PTZ to nvrlib.Joystick (the old app's PTZ, shared with the
// Android app), with the settings of camaras.ini.
type ptzCtl struct {
	g *game
	j *nvrlib.Joystick
}

func (p *ptzCtl) joy() *nvrlib.Joystick {
	if p.j == nil || p.j.Client != p.g.client {
		p.j = &nvrlib.Joystick{Client: p.g.client}
	}
	p.j.Invert = p.g.cfg.get("ptz_invertir_pan", "0") == "1"
	p.j.SpeedPct = p.g.cfg.getInt("ptz_velocidad", 100)
	return p.j
}

func (p *ptzCtl) drag(ch, dx, dy int)                 { p.joy().Drag(ch, dx, dy) }
func (p *ptzCtl) release()                            { p.joy().Release() }
func (p *ptzCtl) object(ch int) (float64, bool)       { return p.joy().Object(ch) }
func (p *ptzCtl) start(ch int, code string, a, b int) { p.joy().Start(ch, code, a, b) }
