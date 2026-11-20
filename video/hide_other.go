//go:build !windows

package video

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
