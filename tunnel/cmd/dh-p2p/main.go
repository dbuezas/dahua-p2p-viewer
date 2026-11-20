// dh-p2p: CLI wrapper over the p2ptunnel library (same behavior as before).
package main

import (
	"fmt"
	"os"

	p2ptunnel "dh-p2p"
)

func main() {
	portSpec := "127.0.0.1:1554:554"
	var serial string
	relay := false
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-r", "--relay":
			relay = true
		case "-p", "--port":
			i++
			if i < len(args) {
				portSpec = args[i]
			}
		default:
			serial = args[i]
		}
	}
	_ = relay
	if serial == "" {
		fmt.Fprintln(os.Stderr, "usage: dh-p2p [--relay] [-p [bind:]port:remote_port] <SERIAL>")
		os.Exit(2)
	}
	bindAddr, bindPort, remotePort := p2ptunnel.ParsePort(portSpec)
	tn, err := p2ptunnel.Open(serial)
	if err != nil {
		panic(err)
	}
	defer tn.Close()
	if err := tn.Forward(bindAddr, bindPort, remotePort); err != nil {
		panic(err)
	}
	fmt.Println("Ready to connect!")
	if remotePort == 554 {
		fmt.Printf("RTSP URL: rtsp://127.0.0.1:%d/cam/realmonitor?channel=1&subtype=0\n", bindPort)
	}
	chBlock := make(chan struct{})
	<-chBlock
}
