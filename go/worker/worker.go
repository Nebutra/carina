// Package worker manages the execution worker pool (PRD §8.6).
// MVP: local workers only. Remote / CI / sandbox workers land in Phase 3.
package worker

import (
	"fmt"
	"sync"
	"time"

	sessionstore "github.com/Nebutra/carina/go/session-store"
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
	Type          Kind      `json:"type"` // alias of Kind for §5.4 compatibility
	Status        string    `json:"status"`
	CurrentTask   string    `json:"current_task"`
	Capabilities  []string  `json:"capabilities"`
	RegisteredAt  time.Time `json:"registered_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// capabilitiesFor returns the capability set a worker kind may exercise.
func capabilitiesFor(kind Kind) []string {
	switch kind {
	case Local:
		return []string{"FileRead", "FileWrite", "CommandExec", "PatchApply"}
	case Sandbox:
		return []string{"FileRead", "CommandExec"} // no host writes; mediated
	case CI:
		return []string{"CommandExec"}
	default: // Remote
		return []string{"CommandExec"}
	}
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
		Type:          kind,
		Status:        "idle",
		Capabilities:  capabilitiesFor(kind),
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

// Get returns a registered worker by id (used to authorize work-dispatch polls).
func (p *Pool) Get(workerID string) (*Worker, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	w, ok := p.workers[workerID]
	return w, ok
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
