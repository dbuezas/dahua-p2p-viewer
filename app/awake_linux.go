//go:build linux

package main

import "os/exec"

// keepAwake: the screen must not blank while the cameras show (the X screen saver
// and power saving are turned off for this session).
func keepAwake() {
	exec.Command("xset", "s", "off", "-dpms", "s", "noblank").Run()
}
