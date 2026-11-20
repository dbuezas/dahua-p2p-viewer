package camaras

import (
	"encoding/json"
	"errors"

	"compartir"

	"github.com/skip2/go-qrcode"
)

// The configuration link (package compartir, the same as the desktop app's QR):
// camaras://config?v=1&d=... The Java side works with its JSON, whose fields are
// "sn", "u", "p" (password; empty = keep the stored one), "ini" (camaras.ini
// keys), "hd", "orden" and "pano" (channel -> mode). None of this is logged.

// ConfigURI makes the link from the configuration's JSON.
func ConfigURI(js string) (string, error) {
	var c compartir.Config
	if err := json.Unmarshal([]byte(js), &c); err != nil {
		return "", err
	}
	return compartir.Encode(c)
}

// ConfigFromURI reads a link and gives the configuration's JSON.
func ConfigFromURI(uri string) (string, error) {
	c, err := compartir.Decode(uri)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	return string(b), err
}

// ConfigSummary describes a link's configuration for a person (password hidden).
func ConfigSummary(uri string) (string, error) {
	c, err := compartir.Decode(uri)
	if err != nil {
		return "", err
	}
	return c.Summary(), nil
}

// QRModules gives the QR code of text as n*n bytes, row by row, 1 = black module
// (no quiet zone): Android draws it with sharp pixels.
func QRModules(text string) ([]byte, error) {
	q, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return nil, err
	}
	q.DisableBorder = true
	bm := q.Bitmap()
	n := len(bm)
	if n == 0 {
		return nil, errors.New("qr vacio")
	}
	out := make([]byte, 0, n*n)
	for _, row := range bm {
		for _, b := range row {
			if b {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
		}
	}
	return out, nil
}
