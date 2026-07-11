// Package worker manages the execution worker pool (PRD §8.6).
// MVP: local workers only. Remote / CI / sandbox workers land in Phase 3.
package worker

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	sessionstore "github.com/Nebutra/carina/go/session-store"
)

type Kind string

type ProcessTreeContainment string

const (
	Local   Kind = "local"
	Remote  Kind = "remote"
	CI      Kind = "ci"
	Sandbox Kind = "sandbox"

	ContainmentNone         ProcessTreeContainment = "none"
	ContainmentUnixPgrpV1   ProcessTreeContainment = "unix_pgrp_v1"
	ContainmentWindowsJobV1 ProcessTreeContainment = "windows_job_v1"
)

type Worker struct {
	WorkerID               string                 `json:"worker_id"`
	Name                   string                 `json:"name"`
	Kind                   Kind                   `json:"kind"`
	Type                   Kind                   `json:"type"` // alias of Kind for §5.4 compatibility
	Status                 string                 `json:"status"`
	CurrentTask            string                 `json:"current_task"`
	Capabilities           []string               `json:"capabilities"`
	ProcessTreeContainment ProcessTreeContainment `json:"process_tree_containment"`
	RegisteredAt           time.Time              `json:"registered_at"`
	LastHeartbeat          time.Time              `json:"last_heartbeat"`
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
	mu             sync.RWMutex
	workers        map[string]*Worker
	credentialHash map[string][sha256.Size]byte
}

func NewPool() *Pool {
	return &Pool{
		workers:        make(map[string]*Worker),
		credentialHash: make(map[string][sha256.Size]byte),
	}
}

func (p *Pool) Register(name string, kind Kind) *Worker {
	w := newWorker(name, kind)
	p.mu.Lock()
	p.workers[w.WorkerID] = w
	p.mu.Unlock()
	return w
}

// RegisterAuthenticated registers a worker and returns its bearer credential.
// The opaque credential is returned once; Pool retains only its SHA-256 hash.
func (p *Pool) RegisterAuthenticated(name string, kind Kind) (*Worker, string, error) {
	return p.RegisterAuthenticatedWithContainment(name, kind, ContainmentNone)
}

func (p *Pool) RegisterAuthenticatedWithContainment(name string, kind Kind, containment ProcessTreeContainment) (*Worker, string, error) {
	return p.RegisterAuthenticatedWithPools(name, kind, containment, nil)
}

// RegisterAuthenticatedWithPools is RegisterAuthenticatedWithContainment plus
// self-declared "worker_pool:<tag>" capability tags (Agent Swarm design
// §4.1's affinity hint) — this is what lets a real operator run
// `carina-worker --pool gpu-heavy` and have a streaming workflow step
// declaring `"affinity":{"worker_pool":"gpu-heavy"}` actually route to it via
// scheduler.LeaseMatching's existing Supports() check, instead of that path
// only being reachable by a test mutating Worker.Capabilities directly.
// pools is assumed ALREADY VALIDATED by the caller (the RPC boundary in
// go/daemon/daemon.go's handleWorkerRegister) — this is the trusted-internal
// side of that trust boundary, not itself a sanitizer.
func (p *Pool) RegisterAuthenticatedWithPools(name string, kind Kind, containment ProcessTreeContainment, pools []string) (*Worker, string, error) {
	if !ValidProcessTreeContainment(containment) {
		return nil, "", fmt.Errorf("worker: unsupported process tree containment %q", containment)
	}
	credentialBytes := make([]byte, 32)
	if _, err := rand.Read(credentialBytes); err != nil {
		return nil, "", fmt.Errorf("worker: generate credential: %w", err)
	}
	credential := base64.RawURLEncoding.EncodeToString(credentialBytes)
	w := newWorker(name, kind)
	w.ProcessTreeContainment = containment
	for _, tag := range pools {
		w.Capabilities = append(w.Capabilities, "worker_pool:"+tag)
	}
	p.mu.Lock()
	p.workers[w.WorkerID] = w
	p.credentialHash[w.WorkerID] = sha256.Sum256([]byte(credential))
	p.mu.Unlock()
	return w, credential, nil
}

func ValidProcessTreeContainment(value ProcessTreeContainment) bool {
	switch value {
	case ContainmentNone, ContainmentUnixPgrpV1, ContainmentWindowsJobV1:
		return true
	default:
		return false
	}
}

func (w *Worker) Supports(required []string) bool {
	declared := make(map[string]bool, len(w.Capabilities))
	for _, capability := range w.Capabilities {
		declared[capability] = true
	}
	for _, capability := range required {
		switch capability {
		case "process_tree_containment":
			if w.ProcessTreeContainment == ContainmentNone {
				return false
			}
		case "process_tree_containment:unix_pgrp_v1":
			if w.ProcessTreeContainment != ContainmentUnixPgrpV1 {
				return false
			}
		case "process_tree_containment:windows_job_v1":
			if w.ProcessTreeContainment != ContainmentWindowsJobV1 {
				return false
			}
		default:
			if !declared[capability] {
				return false
			}
		}
	}
	return true
}

func newWorker(name string, kind Kind) *Worker {
	now := time.Now().UTC()
	return &Worker{
		WorkerID:               sessionstore.NewID("wrk"),
		Name:                   name,
		Kind:                   kind,
		Type:                   kind,
		Status:                 "idle",
		Capabilities:           capabilitiesFor(kind),
		ProcessTreeContainment: ContainmentNone,
		RegisteredAt:           now,
		LastHeartbeat:          now,
	}
}

// Authenticate verifies that credential belongs to workerID without revealing
// whether the worker or credential was the mismatched input.
func (p *Pool) Authenticate(workerID, credential string) bool {
	workerID = strings.TrimSpace(workerID)
	credential = strings.TrimSpace(credential)
	candidate := sha256.Sum256([]byte(credential))
	p.mu.RLock()
	expected, hasCredential := p.credentialHash[workerID]
	_, hasWorker := p.workers[workerID]
	p.mu.RUnlock()
	return workerID != "" && credential != "" && hasWorker && hasCredential &&
		subtle.ConstantTimeCompare(candidate[:], expected[:]) == 1
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
	delete(p.credentialHash, workerID)
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
