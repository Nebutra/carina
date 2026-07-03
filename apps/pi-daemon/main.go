// pi-daemon is the Pi-OS control-plane entrypoint (PRD §7.2). It hosts the
// Rust capability kernel as a child process and serves JSON-RPC on a unix
// socket (and optionally TCP for remote workers).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/TsekaLuk/pi-os/go/daemon"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("pi-daemon: %v", err)
	}
	defaultDir := filepath.Join(home, ".pi-os")

	stateDir := flag.String("state", filepath.Join(defaultDir, "state"), "session/event storage directory")
	socket := flag.String("socket", filepath.Join(defaultDir, "daemon.sock"), "unix socket path")
	tcp := flag.String("tcp", "", "optional TCP listen address for remote workers, e.g. :7777")
	kernelBin := flag.String("kernel", "", "pi-kernel-service path (default: auto-discover)")
	toolsDir := flag.String("tools", "", "zig native tools directory (default: auto-discover)")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*socket), 0o700); err != nil {
		log.Fatalf("pi-daemon: %v", err)
	}

	d, err := daemon.New(daemon.Options{StateDir: *stateDir, KernelBin: *kernelBin, ToolsDir: *toolsDir})
	if err != nil {
		log.Fatalf("pi-daemon: %v", err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("\npi-daemon: shutting down")
		_ = d.Close()
		_ = os.Remove(*socket)
		os.Exit(0)
	}()

	if *tcp != "" {
		go func() {
			fmt.Printf("pi-daemon: also listening on tcp %s\n", *tcp)
			if err := d.RunTCP(*tcp); err != nil {
				log.Printf("pi-daemon: tcp: %v", err)
			}
		}()
	}

	fmt.Printf("pi-daemon %s listening on %s\n", daemon.Version, *socket)
	if err := d.Run(*socket); err != nil {
		log.Fatalf("pi-daemon: %v", err)
	}
}
