//go:build windows

package p2ptunnel

import (
	"net"
	"syscall"
	"unsafe"
)

// noConnReset turns off Windows' SIO_UDP_CONNRESET behaviour. By default, after
// an ICMP "port unreachable" every later read on the UDP socket fails at once
// (WSAECONNRESET), which makes a read loop spin at full CPU and drop data.
func noConnReset(c *net.UDPConn) {
	const sioUDPConnReset = syscall.IOC_IN | syscall.IOC_VENDOR | 12
	rc, err := c.SyscallConn()
	if err != nil {
		return
	}
	rc.Control(func(fd uintptr) {
		flag := uint32(0)
		var ret uint32
		syscall.WSAIoctl(syscall.Handle(fd), sioUDPConnReset,
			(*byte)(unsafe.Pointer(&flag)), uint32(unsafe.Sizeof(flag)), nil, 0, &ret, nil, 0)
	})
}
