// pi-daemon is the Pi-OS control-plane entrypoint (PRD §7.2).
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
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*socket), 0o700); err != nil {
		log.Fatalf("pi-daemon: %v", err)
	}

	d, err := daemon.New(*stateDir)
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

	fmt.Printf("pi-daemon %s listening on %s\n", daemon.Version, *socket)
	if err := d.Run(*socket); err != nil {
		log.Fatalf("pi-daemon: %v", err)
	}
}
