//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// On Linux the NVR password lives in a file only this user can read
// (~/.config/camaras-go/clave, mode 600): the TV logs in by itself, so a
// keyring would stay locked and ask for a password at every start.
func passFile() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "camaras-go", "clave"), nil
}

func storedPassword(user string) (string, error) {
	p, err := passFile()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func savePassword(user, pass string) error {
	p, err := passFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(pass), 0600)
}
