// carina-daemon is the Carina control-plane entrypoint (PRD §7.2). It hosts the
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

	"github.com/Nebutra/carina/go/daemon"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}
	defaultDir := filepath.Join(home, ".carina")

	stateDir := flag.String("state", filepath.Join(defaultDir, "state"), "session/event storage directory")
	socket := flag.String("socket", filepath.Join(defaultDir, "daemon.sock"), "unix socket path")
	tcp := flag.String("tcp", "", "optional TCP listen address for remote workers, e.g. :7777")
	kernelBin := flag.String("kernel", "", "carina-kernel-service path (default: auto-discover)")
	toolsDir := flag.String("tools", "", "zig native tools directory (default: auto-discover)")
	policyDir := flag.String("policy", filepath.Join(defaultDir, "policy"), "enterprise org-policy directory")
	offline := flag.Bool("offline", false, "offline mode: disable network model providers")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*socket), 0o700); err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	d, err := daemon.New(daemon.Options{
		StateDir:  *stateDir,
		KernelBin: *kernelBin,
		ToolsDir:  *toolsDir,
		PolicyDir: *policyDir,
		Offline:   *offline,
	})
	if err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("\ncarina-daemon: shutting down")
		_ = d.Close()
		_ = os.Remove(*socket)
		os.Exit(0)
	}()

	if *tcp != "" {
		go func() {
			fmt.Printf("carina-daemon: also listening on tcp %s\n", *tcp)
			if err := d.RunTCP(*tcp); err != nil {
				log.Printf("carina-daemon: tcp: %v", err)
			}
		}()
	}

	fmt.Printf("carina-daemon %s listening on %s\n", daemon.Version, *socket)
	if err := d.Run(*socket); err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}
}
