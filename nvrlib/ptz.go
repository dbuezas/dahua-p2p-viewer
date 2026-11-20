package nvrlib

import "fmt"

// PTZInstance binds a PTZ object to a channel (0-indexed). Returns an object id.
func (c *Client) PTZInstance(channel int) (float64, error) {
	m, err := c.call("ptz.factory.instance", map[string]interface{}{"channel": channel})
	if err != nil {
		return 0, err
	}
	if b, isBool := m["result"].(bool); isBool && !b {
		return 0, fmt.Errorf("ptz.factory.instance rechazado: %v", m["error"])
	}
	id, ok := m["result"].(float64)
	if !ok || id == 0 {
		return 0, fmt.Errorf("sin id de objeto: %v", m["result"])
	}
	return id, nil
}

// PTZMove starts (or stops) a continuous move. code: Up/Down/Left/Right/ZoomTele/ZoomWide/...
func (c *Client) PTZMove(obj float64, code string, speed int, stop bool) error {
	method := "ptz.start"
	if stop {
		method = "ptz.stop"
	}
	m, err := c.callObj(obj, method, map[string]interface{}{"code": code, "arg1": 0, "arg2": speed, "arg3": 0})
	if err != nil {
		return err
	}
	if ok, _ := m["result"].(bool); !ok {
		return fmt.Errorf("%s rechazado: %v", method, m["error"])
	}
	return nil
}

// PTZAbsolute moves to an exact (pan,tilt,zoom).
func (c *Client) PTZAbsolute(obj float64, pan, tilt, zoom float64) error {
	m, err := c.callObj(obj, "ptz.start", map[string]interface{}{"code": "PositionABS", "arg1": pan, "arg2": tilt, "arg3": zoom, "arg4": 0})
	if err != nil {
		return err
	}
	if ok, _ := m["result"].(bool); !ok {
		return fmt.Errorf("PositionABS rechazado: %v", m["error"])
	}
	return nil
}

// PTZStatus reads the current pan/tilt/zoom.
func (c *Client) PTZStatus(obj float64) (pan, tilt, zoom float64, err error) {
	m, err := c.callObj(obj, "ptz.getStatus", nil)
	if err != nil {
		return
	}
	p, _ := m["params"].(map[string]interface{})
	st, _ := p["status"].(map[string]interface{})
	pos, _ := st["Postion"].([]interface{})
	if pos == nil {
		pos, _ = st["Position"].([]interface{})
	}
	if len(pos) >= 3 {
		pan, _ = pos[0].(float64)
		tilt, _ = pos[1].(float64)
		zoom, _ = pos[2].(float64)
	}
	return
}

// PTZMove2 starts (or stops) a move with both speeds: arg1 = vertical speed
// (used by diagonals), arg2 = horizontal / main speed (1-8).
// Codes: Up Down Left Right LeftUp RightUp LeftDown RightDown ZoomTele ZoomWide.
func (c *Client) PTZMove2(obj float64, code string, arg1, arg2 int, stop bool) error {
	method := "ptz.start"
	if stop {
		method = "ptz.stop"
	}
	m, err := c.callObj(obj, method, map[string]interface{}{"code": code, "arg1": arg1, "arg2": arg2, "arg3": 0})
	if err != nil {
		return err
	}
	if ok, _ := m["result"].(bool); !ok {
		return fmt.Errorf("%s rechazado: %v", method, m["error"])
	}
	return nil
}

// PTZMoveContinuously moves at a smooth speed: pan and tilt from -1 to 1 (any
// fraction, so a slow move is still continuous), stopping by itself after
// timeout seconds unless sent again. Not every camera knows it (then: an error).
func (c *Client) PTZMoveContinuously(obj float64, pan, tilt float64, timeout int) error {
	m, err := c.callObj(obj, "ptz.moveContinuously", map[string]interface{}{
		"speed": []float64{pan, tilt, 0}, "timeout": timeout})
	if err != nil {
		return err
	}
	if ok, _ := m["result"].(bool); !ok {
		return fmt.Errorf("ptz.moveContinuously rechazado: %v", m["error"])
	}
	return nil
}

// PTZStopMove ends a PTZMoveContinuously move.
func (c *Client) PTZStopMove(obj float64) error {
	m, err := c.callObj(obj, "ptz.stopMove", nil)
	if err != nil {
		return err
	}
	if ok, _ := m["result"].(bool); !ok {
		return fmt.Errorf("ptz.stopMove rechazado: %v", m["error"])
	}
	return nil
}
