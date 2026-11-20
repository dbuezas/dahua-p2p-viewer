// mostrar prints everything another phone or computer needs to watch the cameras:
// the NVR serial, user, password and the app settings, plus two QR codes (all of it,
// and the serial alone, which DMSS can scan to add the recorder).
//
//	cd mostrar && go run .            (macOS: the password comes from the Keychain)
//	go run . -sin-clave               (everything but the password)
//
// It reads the same settings folder as the desktop app. The password is shown on
// screen: run it only where nobody else is looking.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

func main() {
	sinClave := flag.Bool("sin-clave", false, "do not show the password")
	flag.Parse()

	dir := settingsDir()
	kv := readIni(filepath.Join(dir, "camaras.ini"))
	sn := kv["sn"]
	if sn == "" {
		sn = firstLine(filepath.Join(dir, "sn.txt"))
	}
	user := os.Getenv("NVR_USER")
	if user == "" {
		user = "admin"
	}
	pass := "(oculta)"
	if !*sinClave {
		pass = password(user, dir)
	}

	fmt.Println("Carpeta de ajustes:", dir)
	fmt.Println()
	fmt.Println("Numero de serie: ", or(sn, "(no encontrado)"))
	fmt.Println("Usuario:         ", user)
	fmt.Println("Clave:           ", or(pass, "(no encontrada)"))
	fmt.Println()

	lines := []string{"sn=" + sn, "usuario=" + user, "clave=" + pass}
	var keys []string
	for k := range kv {
		if k != "sn" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		fmt.Println("Ajustes (camaras.ini):")
		for _, k := range keys {
			fmt.Printf("  %s=%s\n", k, kv[k])
			lines = append(lines, k+"="+kv[k])
		}
	}
	for _, f := range []string{"hd.txt", "orden.txt", "panoramica.txt"} {
		if v := firstLine(filepath.Join(dir, f)); v != "" {
			fmt.Printf("  %s: %s\n", f, v)
			lines = append(lines, strings.TrimSuffix(f, ".txt")+"="+v)
		}
	}
	fmt.Println()
	fmt.Println("Para el otro telefono: en la app de camaras, Ajustes -> numero de serie y clave.")
	fmt.Println("La calibracion de la panoramica no viaja: se hace de nuevo en ese telefono.")

	qr("QR con todo (escanealo con la camara del otro telefono y copia los datos):", strings.Join(lines, "\n"))
	if sn != "" {
		qr("QR con solo el numero de serie (DMSS: + -> Escanear):", sn)
	}
}

func settingsDir() string {
	if d := os.Getenv("CAMARAS_DIR"); d != "" {
		return d
	}
	if ud, err := os.UserConfigDir(); err == nil {
		return filepath.Join(ud, "camaras-go")
	}
	return "."
}

func password(user, dir string) string {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("security", "find-generic-password", "-a", user, "-s", "camaras-nvr", "-w").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		return ""
	}
	return firstLine(filepath.Join(filepath.Dir(dir), "camaras-go", "clave")) // Linux: ~/.config/camaras-go/clave
}

func readIni(path string) map[string]string {
	kv := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return kv
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, ";") || strings.HasPrefix(l, "#") {
			continue
		}
		if i := strings.Index(l, "="); i > 0 {
			kv[strings.TrimSpace(l[:i])] = strings.TrimSpace(l[i+1:])
		}
	}
	return kv
}

func firstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func qr(title, text string) {
	q, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		fmt.Println("QR:", err)
		return
	}
	fmt.Println()
	fmt.Println(title)
	fmt.Print(q.ToSmallString(false))
}
