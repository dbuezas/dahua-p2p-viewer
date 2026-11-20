package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
)

// setupLog: started from the desktop (Windows) or the Finder (Mac) nothing receives
// the app's messages (Windows: no console at all; Mac: /dev/null). Then they go to
// a file, started fresh each time: camaras.log next to the exe on Windows,
// ~/Library/Logs/Camaras.log on the Mac. Run from a terminal or redirected (tests),
// nothing changes.
func setupLog() {
	fi, err := os.Stdout.Stat()
	if err == nil {
		if dn, err2 := os.Stat(os.DevNull); err2 != nil || !os.SameFile(fi, dn) {
			return
		}
	}
	var path string
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		path = filepath.Join(home, "Library", "Logs", "Camaras.log")
	} else {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		path = filepath.Join(filepath.Dir(exe), "camaras.log")
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
}
