// Package nvrlib talks to a Dahua NVR over JSON-RPC (login, channels, snapshot, PTZ),
// reaching it through the local end of the P2P tunnel. Replaces dhnetsdk.dll.
package nvrlib

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Client is a logged-in (or about-to-be) RPC session to one NVR.
type Client struct {
	base    string
	http    *http.Client
	id      int
	session interface{}
	user    string
	pass    string
	relog   sync.Mutex // one new login at a time
}

// New makes a client for the NVR reached at baseHost (e.g. "127.0.0.1:1580",
// the local end of a tunnel to the NVR's web/RPC port 80).
func New(baseHost, user, pass string) *Client {
	return &Client{base: "http://" + baseHost, http: &http.Client{Timeout: 15 * time.Second}, user: user, pass: pass}
}

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

// Dahua "Default" login hash: MD5(user:random:MD5(user:realm:password))
func loginHash(user, pass, realm, random string) string {
	a := md5hex(user + ":" + realm + ":" + pass)
	return md5hex(user + ":" + random + ":" + a)
}

// KeychainPassword reads the NVR password the app stored in the macOS Keychain.
func KeychainPassword(user string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-a", user, "-s", "camaras-nvr", "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

func (c *Client) callPath(path, method string, params interface{}) (map[string]interface{}, error) {
	var lastErr error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(400 * time.Millisecond)
		}
		c.id++
		req := map[string]interface{}{"method": method, "params": params, "id": c.id}
		if c.session != nil {
			req["session"] = c.session
		}
		body, _ := json.Marshal(req)
		resp, err := c.http.Post(c.base+path, "application/json", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			lastErr = fmt.Errorf("respuesta cortada/no-JSON (%d bytes)", len(raw))
			continue
		}
		return m, nil
	}
	return nil, lastErr
}

func (c *Client) call(method string, params interface{}) (map[string]interface{}, error) {
	m, err := c.callPath("/RPC2", method, params)
	if err != nil && c.relogin() {
		m, err = c.callPath("/RPC2", method, params)
	}
	return m, err
}

// relogin: the NVR closes the connection (EOF) on every call of a session it no longer knows (it timed out, or
// the NVR dropped it when the tunnel was opened again): log in again. true = logged in.
func (c *Client) relogin() bool {
	if c.session == nil || c.user == "" {
		return false
	}
	c.relog.Lock()
	defer c.relog.Unlock()
	err := c.Login()
	log.Printf("nvr: entro de nuevo al grabador: %v", map[bool]string{true: "bien", false: fmt.Sprint(err)}[err == nil])
	return err == nil
}

func (c *Client) callObj(obj float64, method string, params interface{}) (map[string]interface{}, error) {
	m, err := c.callObj1(obj, method, params)
	if err != nil && c.relogin() {
		m, err = c.callObj1(obj, method, params)
	}
	return m, err
}

func (c *Client) callObj1(obj float64, method string, params interface{}) (map[string]interface{}, error) {
	var lastErr error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(400 * time.Millisecond)
		}
		c.id++
		body, _ := json.Marshal(map[string]interface{}{"method": method, "params": params, "id": c.id, "session": c.session, "object": obj})
		resp, err := c.http.Post(c.base+"/RPC2", "application/json", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			if c.session != nil && strings.HasSuffix(err.Error(), "EOF") {
				break // the NVR no longer knows this session: trying again cannot help (callObj logs in again)
			}
			continue
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			lastErr = fmt.Errorf("respuesta cortada/no-JSON (%d bytes)", len(raw))
			continue
		}
		return m, nil
	}
	return nil, lastErr
}

// Login does the two-phase Dahua challenge/response login.
func (c *Client) Login() error {
	// a new login carries no session: the NVR closes the connection (EOF) on one it no longer knows
	c.session = nil
	m, err := c.callPath("/RPC2_Login", "global.login", map[string]interface{}{
		"userName": c.user, "password": "", "clientType": "Web3.0",
		"loginType": "Direct", "authorityType": "Default",
	})
	if err != nil {
		return err
	}
	c.session = m["session"]
	p, _ := m["params"].(map[string]interface{})
	realm, _ := p["realm"].(string)
	random, _ := p["random"].(string)
	if realm == "" || random == "" {
		return fmt.Errorf("sin desafio de login")
	}
	m, err = c.callPath("/RPC2_Login", "global.login", map[string]interface{}{
		"userName": c.user, "password": loginHash(c.user, c.pass, realm, random),
		"clientType": "Web3.0", "loginType": "Direct",
		"authorityType": "Default", "passwordType": "Default",
	})
	if err != nil {
		return err
	}
	if ok, _ := m["result"].(bool); !ok {
		return fmt.Errorf("login rechazado: %v", m["error"])
	}
	return nil
}

// Logout ends the login session (global.logout), so the NVR frees it at once
// instead of waiting for it to time out. One try, at most 1 s: it runs on exit.
func (c *Client) Logout() {
	if c.session == nil {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{"method": "global.logout", "params": nil, "id": c.id + 1, "session": c.session})
	hc := &http.Client{Timeout: time.Second, Transport: c.http.Transport}
	if resp, err := hc.Post(c.base+"/RPC2", "application/json", bytes.NewReader(body)); err == nil {
		resp.Body.Close()
	}
	c.session = nil
}

// DeviceType returns the NVR model string, best-effort.
func (c *Client) DeviceType() string {
	if m, err := c.call("magicBox.getDeviceType", nil); err == nil {
		if p, ok := m["params"].(map[string]interface{}); ok {
			if s, ok := p["type"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// Call exposes an arbitrary read-only RPC method (debug/advanced use).
func (c *Client) Call(method string, params interface{}) (map[string]interface{}, error) {
	return c.call(method, params)
}
