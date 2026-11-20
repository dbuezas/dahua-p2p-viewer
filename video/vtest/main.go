package main

import (
	"fmt"
	"image/png"
	"os"

	"video"
)

func main() {
	d, err := video.Open(os.Args[1], false)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer d.Close()
	fmt.Printf("resolucion %dx%d\n", d.W, d.H)
	n := 0
	for img := range d.Frames {
		n++
		if n == 10 {
			f, _ := os.Create("/tmp/vframe.png")
			png.Encode(f, img)
			f.Close()
		}
	}
	fmt.Printf("cuadros decodificados: %d\n", n)
}
