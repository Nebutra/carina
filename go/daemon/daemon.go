// Package daemon hosts the long-running Pi-OS control plane: it wires the
// session store, scheduler, worker pool, and model router behind the
// JSON-RPC server (PRD §8.6). The CLI is a client of this daemon.
package daemon

import (
	"encoding/json"
	"fmt"
	"time"

	modelrouter "github.com/TsekaLuk/pi-os/go/model-router"
	"github.com/TsekaLuk/pi-os/go/rpc"
	"github.com/TsekaLuk/pi-os/go/scheduler"
	sessionstore "github.com/TsekaLuk/pi-os/go/session-store"
	"github.com/TsekaLuk/pi-os/go/worker"
)

const Version = "0.1.0-phase0"

type Daemon struct {
	store   *sessionstore.Store
	sched   *scheduler.Scheduler
	pool    *worker.Pool
	router  *modelrouter.Router
	server  *rpc.Server
	started time.Time
}

func New(stateDir string) (*Daemon, error) {
	store, err := sessionstore.Open(stateDir)
	if err != nil {
		return nil, err
	}
	d := &Daemon{
		store:   store,
		sched:   scheduler.New(),
		pool:    worker.NewPool(),
		router:  modelrouter.New(),
		server:  rpc.NewServer(),
		started: time.Now().UTC(),
	}
	d.registerMethods()
	return d, nil
}

// Run blocks serving JSON-RPC on the unix socket.
func (d *Daemon) Run(socketPath string) error {
	d.pool.Register("local", worker.Local)
	return d.server.ListenUnix(socketPath)
}

func (d *Daemon) Close() error { return d.server.Close() }

func (d *Daemon) registerMethods() {
	d.server.Register("daemon.status", d.handleStatus)
	d.server.Register("session.create", d.handleSessionCreate)
	d.server.Register("session.get", d.handleSessionGet)
	d.server.Register("session.list", d.handleSessionList)
	d.server.Register("session.close", d.handleSessionClose)
	d.server.Register("session.replay", d.handleSessionReplay)
	d.server.Register("task.submit", d.handleTaskSubmit)
	d.server.Register("task.status", d.handleTaskStatus)
	d.server.Register("task.cancel", d.handleTaskCancel)
}

func (d *Daemon) handleStatus(_ json.RawMessage) (any, error) {
	return map[string]any{
		"version":        Version,
		"uptime_seconds": int(time.Since(d.started).Seconds()),
		"sessions":       len(d.store.List()),
		"tasks":          d.sched.Count(),
		"workers":        len(d.pool.List()),
	}, nil
}

func (d *Daemon) handleSessionCreate(params json.RawMessage) (any, error) {
	var p struct {
		WorkspaceRoot string `json:"workspace_root"`
		Profile       string `json:"profile"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace_root is required")
	}
	return d.store.CreateSession(p.WorkspaceRoot, p.Profile)
}

func (d *Daemon) handleSessionGet(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	return sess, nil
}

func (d *Daemon) handleSessionList(_ json.RawMessage) (any, error) {
	return d.store.List(), nil
}

func (d *Daemon) handleSessionClose(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	sess, err := d.store.SetStatus(id, "closed")
	if err != nil {
		return nil, err
	}
	if err := d.store.AppendEvent(sessionstore.Event{
		SessionID: id,
		Type:      "SessionClosed",
		Payload:   json.RawMessage(`{"reason":"client request"}`),
	}); err != nil {
		return nil, err
	}
	return sess, nil
}

func (d *Daemon) handleSessionReplay(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.store.ReadEvents(id)
}

func (d *Daemon) handleTaskSubmit(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Prompt    string `json:"prompt"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	if sess.Status != "active" {
		return nil, fmt.Errorf("session %s is %s, not active", p.SessionID, sess.Status)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, p.Prompt)
	payload, _ := json.Marshal(map[string]string{"task_id": task.TaskID, "user_prompt": task.UserPrompt})
	if err := d.store.AppendEvent(sessionstore.Event{
		SessionID: sess.SessionID,
		TaskID:    task.TaskID,
		Type:      "TaskCreated",
		Payload:   payload,
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (d *Daemon) handleTaskStatus(params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	task, ok := d.sched.Get(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("unknown task %s", p.TaskID)
	}
	return task, nil
}

func (d *Daemon) handleTaskCancel(params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return d.sched.Cancel(p.TaskID)
}

func sessionID(params json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}
	return p.SessionID, nil
}
