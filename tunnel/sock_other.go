//go:build !windows

package p2ptunnel

import "net"

// noConnReset is only needed on Windows.
func noConnReset(c *net.UDPConn) {}
