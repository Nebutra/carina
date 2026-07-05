// Package daemon hosts the long-running Pi-OS control plane: it wires the
// session store, scheduler, worker pool, and model router behind the
// JSON-RPC server, and mediates every side effect through the Rust
// Capability Kernel (pi-kernel-service) and the Zig native toolchain.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TsekaLuk/pi-os/go/kernel"
	modelrouter "github.com/TsekaLuk/pi-os/go/model-router"
	"github.com/TsekaLuk/pi-os/go/rpc"
	"github.com/TsekaLuk/pi-os/go/scheduler"
	sessionstore "github.com/TsekaLuk/pi-os/go/session-store"
	"github.com/TsekaLuk/pi-os/go/toolchain"
	"github.com/TsekaLuk/pi-os/go/worker"
)

const Version = "0.5.0"

// Options configures external binaries and storage.
type Options struct {
	StateDir  string // session metadata, event logs, snapshots
	KernelBin string // pi-kernel-service path ("" = auto-discover)
	ToolsDir  string // zig tools directory ("" = auto-discover)
	PolicyDir string // enterprise org-policy directory ("" = none)
	Offline   bool   // disable network model providers (PRD §5: offline mode)
}

type pendingCommand struct {
	sessionID string
	taskID    string
	argv      []string
}

type Daemon struct {
	store   *sessionstore.Store
	sched   *scheduler.Scheduler
	pool    *worker.Pool
	router  *modelrouter.Router
	server  *rpc.Server
	kern    *kernel.Service
	tools   *toolchain.Toolchain
	events  *Bus
	started time.Time

	org        *kernel.OrgPolicy // enterprise policy (nil when unconfigured)
	stateDir   string
	socketPath string
	reasoner   Reasoner // agent "thinking" engine (nil => mock loop)

	mu          sync.Mutex
	pendingCmds map[string]pendingCommand // decision_id -> command awaiting approval
}

func New(opts Options) (*Daemon, error) {
	if opts.StateDir == "" {
		opts.StateDir = ".pi-os-state"
	}
	store, err := sessionstore.Open(opts.StateDir)
	if err != nil {
		return nil, err
	}
	tools := toolchain.New(opts.ToolsDir)
	// The kernel delegates patch writes to pi-patch-native, so it needs the
	// same tools directory (PRD §4.4).
	kern, err := kernel.Start(opts.KernelBin, opts.StateDir, tools.Dir())
	if err != nil {
		return nil, fmt.Errorf("daemon: cannot start capability kernel: %w", err)
	}
	d := &Daemon{
		store:       store,
		sched:       scheduler.New(),
		pool:        worker.NewPool(),
		router:      modelrouter.New(),
		server:      rpc.NewServer(),
		kern:        kern,
		tools:       tools,
		events:      NewBus(),
		org:         loadOrgPolicy(opts.PolicyDir),
		stateDir:    opts.StateDir,
		started:     time.Now().UTC(),
		pendingCmds: make(map[string]pendingCommand),
	}
	d.registerMethods()
	registerProviders(d.router, opts.Offline)
	// Best-effort: wire the claude CLI reasoner if available and not offline.
	if !opts.Offline {
		if r, err := newClaudeCLIReasoner(); err == nil {
			d.reasoner = r
		}
	}
	d.recover()
	return d, nil
}

// SetReasoner overrides the agent reasoning engine (used by tests).
func (d *Daemon) SetReasoner(r Reasoner) { d.reasoner = r }

// recover re-initializes any sessions that were active when a previous
// daemon exited (PRD §17.3: daemon crash recovery). The event logs already
// persist; here we restore the in-kernel session context so the session can
// continue to be queried and used.
func (d *Daemon) recover() {
	recovered := 0
	for _, sess := range d.store.Recoverable() {
		if err := d.kern.InitSessionWithPolicy(sess.SessionID, sess.WorkspaceRoot, sess.PermissionProfile, d.org); err != nil {
			continue
		}
		recovered++
	}
	if recovered > 0 {
		fmt.Printf("pi-daemon: recovered %d session(s)\n", recovered)
	}
}

// Run blocks serving JSON-RPC on the unix socket.
func (d *Daemon) Run(socketPath string) error {
	d.socketPath = socketPath
	// A local execution worker and a sandbox worker are always available
	// (PRD §5.4).
	d.pool.Register("local", worker.Local)
	d.pool.Register("sandbox", worker.Sandbox)
	return d.server.ListenUnix(socketPath)
}

// RunTCP additionally serves on a TCP address (remote workers/clients).
func (d *Daemon) RunTCP(addr string) error {
	return d.server.ListenTCP(addr)
}

func (d *Daemon) Close() error {
	_ = d.server.Close()
	return d.kern.Close()
}

// Kernel exposes the capability kernel to the agent loop.
func (d *Daemon) Kernel() *kernel.Service { return d.kern }

// Tools exposes the native toolchain to the agent loop.
func (d *Daemon) Tools() *toolchain.Toolchain { return d.tools }

// Router exposes the model router.
func (d *Daemon) Router() *modelrouter.Router { return d.router }

func (d *Daemon) registerMethods() {
	d.server.Register("daemon.status", d.handleStatus)
	d.server.Register("daemon.metrics", d.handleMetrics)

	d.server.Register("session.create", d.handleSessionCreate)
	d.server.Register("session.get", d.handleSessionGet)
	d.server.Register("session.list", d.handleSessionList)
	d.server.Register("session.close", d.handleSessionClose)
	d.server.Register("session.replay", d.handleSessionReplay)

	d.server.Register("task.submit", d.handleTaskSubmit)
	d.server.Register("task.status", d.handleTaskStatus)
	d.server.Register("task.cancel", d.handleTaskCancel)
	d.server.Register("task.action.approve", d.handleApprove)
	d.server.Register("task.action.deny", d.handleDeny)

	d.server.Register("workspace.tree", d.handleWorkspaceTree)
	d.server.Register("workspace.search", d.handleWorkspaceSearch)
	d.server.Register("workspace.file.get", d.handleFileGet)
	d.server.Register("workspace.patch.propose", d.handlePatchPropose)
	d.server.Register("workspace.patch.apply", d.handlePatchApply)
	d.server.Register("workspace.patch.rollback", d.handlePatchRollback)
	d.server.Register("workspace.patch.list", d.handlePatchList)
	d.server.Register("workspace.patch.show", d.handlePatchShow)

	d.server.Register("command.exec", d.handleCommandExec)
	d.server.Register("audit.report", d.handleAuditReport)
	d.server.Register("audit.export", d.handleAuditExport)
	d.server.Register("audit.verify", d.handleAuditVerify)
	d.server.Register("profile.describe", d.handleProfileDescribe)
	d.server.Register("secret.grant", d.handleSecretGrant)
	d.server.Register("secret.request", d.handleSecretRequest)
	d.server.Register("plugin.inspect", d.handlePluginInspect)
	d.server.Register("plugin.run", d.handlePluginRun)

	d.server.RegisterStream("session.events.stream", d.handleEventStream)

	d.server.Register("worker.register", d.handleWorkerRegister)
	d.server.Register("worker.heartbeat", d.handleWorkerHeartbeat)
	d.server.Register("worker.list", d.handleWorkerList)
	d.server.Register("worker.revoke", d.handleWorkerRevoke)
}

// ---- daemon ---------------------------------------------------------------

func (d *Daemon) handleStatus(_ json.RawMessage) (any, error) {
	return map[string]any{
		"version":         Version,
		"pid":             os.Getpid(),
		"uptime_seconds":  int(time.Since(d.started).Seconds()),
		"active_sessions": len(d.store.List()),
		"sessions":        len(d.store.List()),
		"queued_tasks":    d.sched.CountByStatus()["queued"],
		"tasks":           d.sched.Count(),
		"active_workers":  len(d.pool.List()),
		"workers":         len(d.pool.List()),
		"tools":           d.tools.Available(),
		"rpc_endpoint":    d.socketPath,
		"event_log_path":  filepath.Join(d.stateDir, "events"),
	}, nil
}

func (d *Daemon) handleMetrics(_ json.RawMessage) (any, error) {
	return map[string]any{
		"version":         Version,
		"uptime_seconds":  int(time.Since(d.started).Seconds()),
		"tasks_by_status": d.sched.CountByStatus(),
		"model_usage":     d.router.UsageByProvider(),
		"subscribers":     d.events.SubscriberCount(),
	}, nil
}

// ---- sessions -------------------------------------------------------------

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
	if _, err := os.Stat(p.WorkspaceRoot); err != nil {
		return nil, fmt.Errorf("workspace_root: %w", err)
	}
	sess, err := d.store.CreateSession(p.WorkspaceRoot, p.Profile)
	if err != nil {
		return nil, err
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, sess.WorkspaceRoot, sess.PermissionProfile, d.org); err != nil {
		return nil, fmt.Errorf("kernel session init: %w", err)
	}
	return sess, nil
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
	d.record(id, "SessionClosed", "", "go", map[string]any{"reason": "client request"}, "")
	return sess, nil
}

func (d *Daemon) handleSessionReplay(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.ReadEvents(id)
}

// ---- tasks ----------------------------------------------------------------

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
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
		map[string]any{"task_id": task.TaskID, "user_prompt": task.UserPrompt}, "")

	go d.runTask(sess, task)
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

// ---- approvals ------------------------------------------------------------

func (d *Daemon) handleApprove(params json.RawMessage) (any, error) {
	var p struct {
		SessionID  string `json:"session_id"`
		DecisionID string `json:"decision_id"`
		Approver   string `json:"approver"`
		Role       string `json:"role"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Approver == "" {
		p.Approver = "user"
	}
	decision, err := d.kern.ApproveWithRole(p.SessionID, p.DecisionID, p.Approver, p.Role)
	if err != nil {
		return nil, err
	}
	// A role-rejected approval does not execute the pending command.
	if decision.Decision != "allowed" {
		return map[string]any{"decision": decision}, nil
	}

	// If the approval unblocks a queued command, execute it now.
	d.mu.Lock()
	pending, ok := d.pendingCmds[p.DecisionID]
	delete(d.pendingCmds, p.DecisionID)
	d.mu.Unlock()
	if ok {
		result, err := d.executeCommand(pending.sessionID, pending.taskID, pending.argv, decision)
		if err != nil {
			return nil, err
		}
		return map[string]any{"decision": decision, "result": result}, nil
	}
	return map[string]any{"decision": decision}, nil
}

func (d *Daemon) handleDeny(params json.RawMessage) (any, error) {
	var p struct {
		SessionID  string `json:"session_id"`
		DecisionID string `json:"decision_id"`
		Approver   string `json:"approver"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Approver == "" {
		p.Approver = "user"
	}
	d.mu.Lock()
	delete(d.pendingCmds, p.DecisionID)
	d.mu.Unlock()
	return d.kern.Deny(p.SessionID, p.DecisionID, p.Approver, p.Reason)
}

// ---- workspace ------------------------------------------------------------

func (d *Daemon) handleWorkspaceTree(params json.RawMessage) (any, error) {
	sess, _, err := d.session(params)
	if err != nil {
		return nil, err
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	return d.tools.Scan(sess.WorkspaceRoot)
}

func (d *Daemon) handleWorkspaceSearch(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Pattern   string `json:"pattern"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	return d.tools.Grep(p.Pattern, sess.WorkspaceRoot)
}

func (d *Daemon) handleFileGet(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	abs := p.Path
	if !strings.HasPrefix(abs, "/") {
		abs = sess.WorkspaceRoot + "/" + p.Path
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", abs, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	d.record(sess.SessionID, "FileRead", "", "go",
		map[string]any{"path": abs, "bytes": len(content)}, decision.DecisionID)
	return map[string]any{"content": string(content), "hash": hex.EncodeToString(sum[:])}, nil
}

// ---- patches --------------------------------------------------------------

func (d *Daemon) handlePatchPropose(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string               `json:"session_id"`
		TaskID    string               `json:"task_id"`
		Reason    string               `json:"reason"`
		Files     []kernel.FileChange  `json:"files"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return d.kern.PatchPropose(p.SessionID, p.TaskID, p.Reason, p.Files)
}

func (d *Daemon) handlePatchApply(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		PatchID   string `json:"patch_id"`
		Approver  string `json:"approver"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Approver == "" {
		p.Approver = "user"
	}
	return d.kern.PatchApply(p.SessionID, p.PatchID, p.Approver)
}

func (d *Daemon) handlePatchRollback(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		PatchID   string `json:"patch_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return d.kern.PatchRollback(p.SessionID, p.PatchID)
}

func (d *Daemon) handlePatchList(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.PatchList(id)
}

func (d *Daemon) handlePatchShow(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		PatchID   string `json:"patch_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return d.kern.PatchShow(p.SessionID, p.PatchID)
}

// ---- command execution ------------------------------------------------------

func (d *Daemon) handleCommandExec(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string   `json:"session_id"`
		TaskID    string   `json:"task_id"`
		Argv      []string `json:"argv"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if len(p.Argv) == 0 {
		return nil, fmt.Errorf("argv is required")
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	command := strings.Join(p.Argv, " ")
	decision, err := d.kern.Request(sess.SessionID, "CommandExec", command, p.TaskID)
	if err != nil {
		return nil, err
	}
	switch decision.Decision {
	case "denied":
		return map[string]any{"decision": decision}, nil
	case "requires_approval":
		d.mu.Lock()
		d.pendingCmds[decision.DecisionID] = pendingCommand{sessionID: sess.SessionID, taskID: p.TaskID, argv: p.Argv}
		d.mu.Unlock()
		return map[string]any{"decision": decision}, nil
	}
	result, err := d.executeCommand(sess.SessionID, p.TaskID, p.Argv, decision)
	if err != nil {
		return nil, err
	}
	return map[string]any{"decision": decision, "result": result}, nil
}

func (d *Daemon) executeCommand(sessionID, taskID string, argv []string, decision *kernel.Decision) (*toolchain.CommandResult, error) {
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", sessionID)
	}
	command := strings.Join(argv, " ")
	risk, _ := d.kern.ClassifyCommand(command)
	// The command is executed by the Zig pi-run tool, so its lifecycle
	// events are attributed to the Zig actor. Package-manager mutations are
	// flagged so lockfile changes are auditable (PRD §13.7).
	started := map[string]any{"command": command, "cwd": sess.WorkspaceRoot, "risk_level": risk}
	if mutatesPackages(command) {
		started["package_mutation"] = true
	}
	d.record(sessionID, "CommandStarted", taskID, "zig", started, decision.DecisionID)

	result, err := d.tools.Run(argv, sess.WorkspaceRoot, 2*time.Minute)
	if err != nil {
		d.record(sessionID, "CommandExited", taskID, "zig", map[string]any{"exit_code": -1, "error": err.Error()}, "")
		return nil, err
	}
	output := result.Stdout
	if len(output) > 100 {
		output = output[:100]
	}
	// Redact any known secret values before the output enters the log.
	chunk := strings.Join(output, "\n")
	if redacted, err := d.kern.Redact(sessionID, chunk); err == nil {
		chunk = redacted
	}
	d.record(sessionID, "CommandOutput", taskID, "zig", map[string]any{"stream": "stdout", "chunk": chunk}, "")
	d.record(sessionID, "CommandExited", taskID, "zig",
		map[string]any{"exit_code": result.ExitCode, "duration_ms": result.DurationMs}, "")
	return result, nil
}

// ---- audit / events ---------------------------------------------------------

func (d *Daemon) handleAuditReport(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.AuditReport(id)
}

func (d *Daemon) handleAuditExport(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.AuditExport(id)
}

func (d *Daemon) handleAuditVerify(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.AuditVerify(id)
}

func (d *Daemon) handleProfileDescribe(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.kern.ProfileDescribe(id)
}

func (d *Daemon) handleSecretGrant(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
		Value     string `json:"value"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	handle, err := d.kern.GrantSecret(p.SessionID, p.Name, p.Value)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": p.Name, "handle": handle}, nil
}

func (d *Daemon) handleSecretRequest(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	decision, handle, err := d.kern.RequestSecret(p.SessionID, p.Name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"decision": decision, "handle": handle}, nil
}

func (d *Daemon) handlePluginInspect(params json.RawMessage) (any, error) {
	var p struct {
		ManifestTOML string `json:"manifest_toml"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return d.kern.PluginInspect(p.ManifestTOML)
}

func (d *Daemon) handlePluginRun(params json.RawMessage) (any, error) {
	var p struct {
		SessionID       string `json:"session_id"`
		ManifestTOML    string `json:"manifest_toml"`
		WasmBase64      string `json:"wasm_base64"`
		SignatureBase64 string `json:"signature_base64"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	return d.kern.PluginRun(p.SessionID, p.ManifestTOML, p.WasmBase64, p.SignatureBase64)
}

func (d *Daemon) handleEventStream(params json.RawMessage, sub *rpc.Subscription) error {
	id, err := sessionID(params)
	if err != nil {
		return err
	}
	d.events.Subscribe(id, sub)
	return nil
}

// record appends an event through the kernel (single audit writer) and
// fans it out to live subscribers. actor tags the language layer that
// produced the effect (go/rust/zig/model/user) so the audit trail shows
// the Go → Rust → Zig control flow (PRD §4.1).
func (d *Daemon) record(sessionID, eventType, taskID, actor string, payload map[string]any, decisionID string) {
	_ = d.kern.RecordEvent(sessionID, eventType, taskID, actor, payload, decisionID)
	d.events.Publish(sessionID, map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
		"type":       eventType,
		"actor":      actor,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"payload":    payload,
	})
}

// ---- workers ----------------------------------------------------------------

func (d *Daemon) handleWorkerRegister(params json.RawMessage) (any, error) {
	var p struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Kind == "" {
		p.Kind = "remote"
	}
	return d.pool.Register(p.Name, worker.Kind(p.Kind)), nil
}

func (d *Daemon) handleWorkerHeartbeat(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := d.pool.Heartbeat(p.WorkerID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (d *Daemon) handleWorkerList(_ json.RawMessage) (any, error) {
	return d.pool.List(), nil
}

func (d *Daemon) handleWorkerRevoke(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := d.pool.Revoke(p.WorkerID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (d *Daemon) session(params json.RawMessage) (*sessionstore.Session, string, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, "", err
	}
	sess, ok := d.store.Get(id)
	if !ok {
		return nil, "", fmt.Errorf("unknown session %s", id)
	}
	return sess, id, nil
}

// mutatesPackages reports whether a command installs/updates dependencies
// and therefore likely changes a lockfile (PRD §13.7).
func mutatesPackages(command string) bool {
	prefixes := []string{
		"npm install", "npm i ", "npm ci", "npm uninstall", "npm update",
		"pnpm add", "pnpm install", "pnpm remove", "yarn add", "yarn install", "yarn remove",
		"pip install", "pip uninstall", "poetry add", "poetry remove",
		"cargo add", "cargo install", "cargo remove", "go get", "bundle add",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(command, p) {
			return true
		}
	}
	// Direct lockfile edits.
	for _, lock := range []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "Cargo.lock", "go.sum", "poetry.lock"} {
		if strings.Contains(command, lock) {
			return true
		}
	}
	return false
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
