// pi-worker joins a Pi-OS daemon over TCP and offers itself as an
// execution worker (PRD §7.4, §11.5). It registers, then heartbeats until
// interrupted. Remote task execution is assigned by the daemon scheduler.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TsekaLuk/pi-os/go/rpc"
)

func main() {
	server := flag.String("server", "", "daemon TCP address, e.g. host:7777")
	name := flag.String("name", hostname(), "worker name")
	kind := flag.String("kind", "remote", "worker kind: remote|ci|sandbox")
	interval := flag.Duration("heartbeat", 10*time.Second, "heartbeat interval")
	flag.Parse()

	if *server == "" {
		fmt.Fprintln(os.Stderr, "usage: pi-worker --server <host:port> [--name N] [--kind remote|ci|sandbox]")
		os.Exit(2)
	}

	client, err := rpc.DialTCP(*server)
	if err != nil {
		log.Fatalf("pi-worker: %v", err)
	}
	defer client.Close()

	var reg struct {
		WorkerID string `json:"worker_id"`
	}
	if err := client.Call("worker.register", map[string]any{"name": *name, "kind": *kind}, &reg); err != nil {
		log.Fatalf("pi-worker: register: %v", err)
	}
	fmt.Printf("pi-worker %q (%s) joined %s as %s\n", *name, *kind, *server, reg.WorkerID)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			_ = client.Call("worker.revoke", map[string]any{"worker_id": reg.WorkerID}, nil)
			fmt.Println("\npi-worker: left the pool")
			return
		case <-ticker.C:
			if err := client.Call("worker.heartbeat", map[string]any{"worker_id": reg.WorkerID}, nil); err != nil {
				log.Printf("pi-worker: heartbeat failed: %v", err)
			}
		}
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "worker"
	}
	return h
}
