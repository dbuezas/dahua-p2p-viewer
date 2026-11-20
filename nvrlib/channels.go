package nvrlib

// Channel is one camera connected to the NVR (only safe fields; no passwords).
type Channel struct {
	Index   int
	Name    string
	Address string
	Type    string
	Enable  bool
}

// ListCameras returns the NVR's channels.
func (c *Client) ListCameras() ([]Channel, error) {
	m, err := c.call("LogicDeviceManager.getCameraAll", map[string]interface{}{"name": nil})
	if err != nil {
		return nil, err
	}
	p, _ := m["params"].(map[string]interface{})
	cams, _ := p["camera"].([]interface{})
	var out []Channel
	for pos, ci := range cams {
		cm, _ := ci.(map[string]interface{})
		di, _ := cm["DeviceInfo"].(map[string]interface{})
		enable, _ := di["Enable"].(bool)
		// the list is in channel order (its "Channel" field is 0 for every camera here)
		idx := pos
		name, _ := di["Name"].(string)
		addr, _ := di["Address"].(string)
		typ, _ := di["DeviceType"].(string)
		out = append(out, Channel{Index: idx, Name: name, Address: addr, Type: typ, Enable: enable})
	}
	return out, nil
}
