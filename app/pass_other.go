//go:build !windows

package main

import (
	"os/exec"

	"nvrlib"
)

// storedPassword reads the NVR password from the OS secret store (macOS Keychain).
func storedPassword(user string) (string, error) {
	return nvrlib.KeychainPassword(user)
}

// savePassword keeps the NVR password in the macOS Keychain (replacing an old one).
func savePassword(user, pass string) error {
	return exec.Command("security", "add-generic-password", "-U", "-a", user, "-s", "camaras-nvr", "-w", pass).Run()
}
