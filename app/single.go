package main

import "net"

var instanceLock net.Listener

// singleInstance: only one copy runs (like the old app). A local port works as
// the lock on every system and is freed by the OS when the process ends.
func singleInstance() bool {
	l, err := net.Listen("tcp", "127.0.0.1:1578")
	if err != nil {
		return false
	}
	instanceLock = l
	return true
}
