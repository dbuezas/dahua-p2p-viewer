// tput: measures how fast the tunnel carries one RTSP stream. It opens the tunnel,
// lets ffmpeg copy the stream without decoding for N seconds, and prints the
// tunnel's receiver stats (DH_STATS) and ffmpeg's byte count.
//
//	tput <serial> <channel 1-based> <subtype 0|1> <seconds>
//
// The NVR password comes from NVR_PASS (it is never printed).
package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"time"

	p2ptunnel "dh-p2p"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Println("uso: tput <serial> <canal> <subtype> <segundos>")
		return
	}
	ch, _ := strconv.Atoi(os.Args[2])
	sub, _ := strconv.Atoi(os.Args[3])
	secs, _ := strconv.Atoi(os.Args[4])
	os.Setenv("DH_STATS", "1")
	tn, err := p2ptunnel.Open(os.Args[1])
	if err != nil {
		fmt.Println("tunel:", err)
		return
	}
	if err := tn.Forward("127.0.0.1", 1555, 554); err != nil {
		fmt.Println(err)
		return
	}
	u := fmt.Sprintf("rtsp://admin:%s@127.0.0.1:1555/cam/realmonitor?channel=%d&subtype=%d",
		url.QueryEscape(os.Getenv("NVR_PASS")), ch, sub)
	t0 := time.Now()
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-rtsp_transport", "tcp",
		"-i", u, "-t", strconv.Itoa(secs), "-c", "copy", "-f", "null", "-")
	out, _ := cmd.CombinedOutput()
	_ = out // may hold the URL: not printed
	fmt.Printf("ffmpeg termino en %.0fs\n", time.Since(t0).Seconds())
	time.Sleep(time.Second)
}
