package nvrlib

import (
	"log"
	"math"
	"sync"
	"time"
)

// Joystick is the old app's PTZ (Camaras.cs PtzSend/PtzStart/PtzStop/PtzDrag), with
// the same commands and numbers. Every order goes to ONE worker queue, in order,
// so a stop never reaches the NVR after the next start (and the screen never waits
// for the network).
type Joystick struct {
	Client   *Client
	Invert   bool // ptz_invertir_pan=1: left/right turned around
	SpeedPct int  // ptz_velocidad (percent, 100 = normal)

	mu sync.Mutex

	q chan func()

	// the move now running (PtzStart)
	on     bool
	ch     int
	code   string
	p1, p2 int

	obj   float64 // PTZ object of objCh (0 = not made yet)
	objCh int

	pulseRun   bool
	pulseCh    int
	pulseCode  string
	pulseOnDur time.Duration
}

// codes by drag sector: right, up-right, up, up-left, left, down-left, down,
// down-right (the old app's 3, 0x21, 0, 0x20, 2, 0x22, 1, 0x23)
var ptzCodes = []string{"Right", "RightUp", "Up", "LeftUp", "Left", "LeftDown", "Down", "RightDown"}

func (p *Joystick) send(f func()) {
	p.mu.Lock()
	if p.q == nil {
		p.q = make(chan func(), 256)
		go func(q chan func()) {
			for f := range q {
				f()
			}
		}(p.q)
	}
	q := p.q
	p.mu.Unlock()
	q <- f
}

// Object returns the NVR's PTZ object for a channel (made once).
func (p *Joystick) Object(ch int) (float64, bool) {
	p.mu.Lock()
	if p.obj != 0 && p.objCh == ch {
		o := p.obj
		p.mu.Unlock()
		return o, true
	}
	p.mu.Unlock()
	c := p.Client
	if c == nil {
		return 0, false
	}
	o, err := c.PTZInstance(ch)
	if err != nil {
		log.Printf("ptz canal %d: %v", ch, err)
		return 0, false
	}
	p.mu.Lock()
	p.obj, p.objCh = o, ch
	p.mu.Unlock()
	return o, true
}

func (p *Joystick) control(ch int, code string, p1, p2 int, stop bool) {
	obj, ok := p.Object(ch)
	if !ok {
		return
	}
	if err := p.Client.PTZMove2(obj, code, p1, p2, stop); err != nil {
		log.Printf("ptz: %v", err)
	}
}

// Start: PtzStart (nothing if the same move already runs; else stop the old one first).
func (p *Joystick) Start(ch int, code string, p1, p2 int) {
	p.mu.Lock()
	if p.on && ch == p.ch && code == p.code && p1 == p.p1 && p2 == p.p2 {
		p.mu.Unlock()
		return
	}
	running := p.on
	p.mu.Unlock()
	if running {
		p.stopMove()
	}
	p.mu.Lock()
	p.on, p.ch, p.code, p.p1, p.p2 = true, ch, code, p1, p2
	p.mu.Unlock()
	p.send(func() {
		p.control(ch, code, p1, p2, false)
		log.Printf("ptz canal %d %s vel %d/%d", ch, code, p1, p2)
	})
}

// stopMove: PtzStop.
func (p *Joystick) stopMove() {
	p.pulseOff()
	p.mu.Lock()
	if !p.on {
		p.mu.Unlock()
		return
	}
	p.on = false
	ch, code, p1, p2 := p.ch, p.code, p.p1, p.p2
	p.mu.Unlock()
	p.send(func() { p.control(ch, code, p1, p2, true) })
}

// Release ends the drag.
func (p *Joystick) Release() { p.stopMove() }

// Drag: the old app's PtzDrag. A drag of (dx, dy) pixels from the press point: 8 directions,
// speed 1-8 grows with the distance. ptz_invertir_pan=1 turns left/right around;
// ptz_velocidad=50 = half speed (percent).
func (p *Joystick) Drag(ch, dx, dy int) {
	if p.Invert {
		dx = -dx
	}
	ang := math.Atan2(float64(-dy), float64(dx)) * 180 / math.Pi // 0 = right, 90 = up
	sector := int(math.Round(math.Mod(ang+360, 360)/45)) % 8
	speed0 := int(math.Sqrt(float64(dx*dx+dy*dy)) / 40)
	if speed0 < 1 {
		speed0 = 1
	}
	if speed0 > 8 {
		speed0 = 8
	}
	want := float64(speed0) * float64(p.speedPct()) / 100 // may be below 1, the slowest real speed
	speed := int(math.Round(want))
	if speed < 1 {
		speed = 1
	}
	code := ptzCodes[sector]
	diag := sector%2 == 1
	if want >= 1 {
		p.pulseOff()
		p1 := 0
		if diag {
			p1 = speed
		}
		p.Start(ch, code, p1, speed)
		return
	}
	// slower than speed 1: move at speed 1 in short pulses (on for want x 400 ms of
	// every 400 ms, at least 60 ms)
	p.mu.Lock()
	running := p.on
	p.mu.Unlock()
	if running {
		p.stopMove()
	}
	p.mu.Lock()
	p.pulseCh, p.pulseCode = ch, code
	p.pulseOnDur = time.Duration(math.Max(60, want*400)) * time.Millisecond
	startLoop := !p.pulseRun
	p.pulseRun = true
	p.mu.Unlock()
	if startLoop {
		go p.pulseLoop()
		log.Printf("ptz canal %d pulsos de %v cada 400 ms", ch, p.pulseOnDur)
	}
}

func (p *Joystick) pulseOff() {
	p.mu.Lock()
	p.pulseRun = false
	p.mu.Unlock()
}

// pulseLoop: the old app's pulse thread (start, wait "on", stop, wait the rest of
// the 400 ms); the orders go through the same ordered queue.
func (p *Joystick) pulseLoop() {
	for {
		p.mu.Lock()
		run, ch, code, on := p.pulseRun, p.pulseCh, p.pulseCode, p.pulseOnDur
		p.mu.Unlock()
		if !run {
			return
		}
		p1 := 0
		for i, c := range ptzCodes {
			if c == code && i%2 == 1 {
				p1 = 1
			}
		}
		t0 := time.Now()
		p.send(func() { p.control(ch, code, p1, 1, false) })
		time.Sleep(on)
		p.send(func() { p.control(ch, code, p1, 1, true) })
		time.Sleep(time.Until(t0.Add(400 * time.Millisecond)))
	}
}

func (p *Joystick) speedPct() int {
	if p.SpeedPct <= 0 {
		return 100
	}
	return p.SpeedPct
}
