//go:build windows

package main

import (
	"runtime"
	"syscall"
	"time"
)

// keepAwake: no sleep and no screen off while the app runs (like the old app).
// SetThreadExecutionState holds per thread, so one locked thread renews it.
func keepAwake() {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	go func() {
		runtime.LockOSThread()
		for {
			proc.Call(0x80000003) // continuous | system needed | display needed
			time.Sleep(30 * time.Second)
		}
	}()
}
