//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// On Windows the old app saved the NVR password at %APPDATA%\camaras\nvr.bin,
// encrypted with DPAPI for the current user. We read and decrypt it the same
// way, so the password never travels on the command line.
var (
	crypt32       = syscall.NewLazyDLL("crypt32.dll")
	kernel32dll   = syscall.NewLazyDLL("kernel32.dll")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procLocalFree = kernel32dll.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func storedPassword(user string) (string, error) {
	_ = user
	pf := filepath.Join(os.Getenv("APPDATA"), "camaras", "nvr.bin")
	enc, err := os.ReadFile(pf)
	if err != nil {
		return "", err
	}
	var in, out dataBlob
	in.cbData = uint32(len(enc))
	if len(enc) > 0 {
		in.pbData = &enc[0]
	}
	r, _, e := procUnprotect.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return "", fmt.Errorf("CryptUnprotectData: %v", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	b := make([]byte, out.cbData)
	copy(b, unsafe.Slice(out.pbData, out.cbData))
	return string(b), nil
}

// savePassword encrypts the password with DPAPI for this Windows user and writes
// it where the old app keeps it (%APPDATA%\camaras\nvr.bin), so both apps share it.
func savePassword(user, pass string) error {
	_ = user
	pf := filepath.Join(os.Getenv("APPDATA"), "camaras", "nvr.bin")
	if err := os.MkdirAll(filepath.Dir(pf), 0o700); err != nil {
		return err
	}
	b := []byte(pass)
	var in, out dataBlob
	in.cbData = uint32(len(b))
	if len(b) > 0 {
		in.pbData = &b[0]
	}
	r, _, e := procProtect.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return fmt.Errorf("CryptProtectData: %v", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	enc := make([]byte, out.cbData)
	copy(enc, unsafe.Slice(out.pbData, out.cbData))
	return os.WriteFile(pf, enc, 0o600)
}
