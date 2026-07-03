// Package worker manages the execution worker pool (PRD §8.6).
// MVP: local workers only. Remote / CI / sandbox workers land in Phase 3.
package worker

import (
	"fmt"
	"sync"
	"time"

	sessionstore "github.com/TsekaLuk/pi-os/go/session-store"
)

type Kind string

const (
	Local   Kind = "local"
	Remote  Kind = "remote"
	CI      Kind = "ci"
	Sandbox Kind = "sandbox"
)

type Worker struct {
	WorkerID      string    `json:"worker_id"`
	Name          string    `json:"name"`
	Kind          Kind      `json:"kind"`
	RegisteredAt  time.Time `json:"registered_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

type Pool struct {
	mu      sync.RWMutex
	workers map[string]*Worker
}

func NewPool() *Pool {
	return &Pool{workers: make(map[string]*Worker)}
}

func (p *Pool) Register(name string, kind Kind) *Worker {
	now := time.Now().UTC()
	w := &Worker{
		WorkerID:      sessionstore.NewID("wrk"),
		Name:          name,
		Kind:          kind,
		RegisteredAt:  now,
		LastHeartbeat: now,
	}
	p.mu.Lock()
	p.workers[w.WorkerID] = w
	p.mu.Unlock()
	return w
}

func (p *Pool) Heartbeat(workerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.workers[workerID]
	if !ok {
		return fmt.Errorf("worker: unknown worker %s", workerID)
	}
	updated := *w
	updated.LastHeartbeat = time.Now().UTC()
	p.workers[workerID] = &updated
	return nil
}

func (p *Pool) Revoke(workerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.workers[workerID]; !ok {
		return fmt.Errorf("worker: unknown worker %s", workerID)
	}
	delete(p.workers, workerID)
	return nil
}

func (p *Pool) List() []*Worker {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Worker, 0, len(p.workers))
	for _, w := range p.workers {
		out = append(out, w)
	}
	return out
}
