//go:build windows

package video

import (
	"os/exec"
	"syscall"
)

// hideWindow: ffmpeg is a console program; without this Windows opens a black
// window for each one.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
