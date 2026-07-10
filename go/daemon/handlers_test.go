package daemon_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/rpc"
)

// TestDaemonHandlerSurface exercises the breadth of RPC handlers so the
// control-plane coverage reflects the full API (PRD §15).
func TestDaemonHandlerSurface(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	stateDir := t.TempDir()
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "a.go"), []byte("package p\n// TODO x\n"), 0o600)

	d, err := daemon.New(daemon.Options{StateDir: stateDir, KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)
	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	must := func(method string, params map[string]any) {
		if err := c.Call(method, params, nil); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}

	// daemon-level
	must("daemon.status", map[string]any{})
	must("daemon.metrics", map[string]any{})
	must("context.status", map[string]any{})
	must("context.doctor", map[string]any{})
	must("context.stats", map[string]any{})
	var methods struct {
		Methods []struct {
			Method       string `json:"method"`
			Scope        string `json:"scope"`
			Remote       bool   `json:"remote"`
			DynamicScope bool   `json:"dynamic_scope"`
		} `json:"methods"`
	}
	if err := c.Call("gateway.methods", map[string]any{}, &methods); err != nil {
		t.Fatal(err)
	}
	seenStatus := false
	seenSubmit := false
	seenPatchPropose := false
	seenMemoryWrite := false
	seenMemorySearch := false
	seenMemoryStatus := false
	seenScheduleCreate := false
	seenScheduleList := false
	seenSchedulePause := false
	seenScheduleResume := false
	seenScheduleDelete := false
	seenSessionResume := false
	seenContextStatus := false
	seenContextStats := false
	for _, m := range methods.Methods {
		switch m.Method {
		case "daemon.status":
			seenStatus = m.Scope == "read" && m.Remote
		case "task.submit":
			seenSubmit = m.Scope == "write" && !m.Remote
		case "session.resume":
			seenSessionResume = m.Scope == "write" && !m.Remote
		case "context.status":
			seenContextStatus = m.Scope == "read" && !m.Remote
		case "context.stats":
			seenContextStats = m.Scope == "read" && !m.Remote
		case "workspace.patch.propose":
			seenPatchPropose = m.Scope == "write" && m.DynamicScope
		case "memory.status":
			seenMemoryStatus = m.Scope == "read" && !m.Remote
		case "memory.search":
			seenMemorySearch = m.Scope == "read" && !m.Remote
		case "memory.write":
			seenMemoryWrite = m.Scope == "write" && !m.Remote
		case "schedule.create":
			seenScheduleCreate = m.Scope == "write" && !m.Remote
		case "schedule.list":
			seenScheduleList = m.Scope == "read" && !m.Remote
		case "schedule.pause":
			seenSchedulePause = m.Scope == "write" && !m.Remote
		case "schedule.resume":
			seenScheduleResume = m.Scope == "write" && !m.Remote
		case "schedule.delete":
			seenScheduleDelete = m.Scope == "write" && !m.Remote
		}
	}
	if !seenStatus || !seenSubmit || !seenSessionResume || !seenContextStatus || !seenContextStats || !seenPatchPropose || !seenMemoryStatus || !seenMemorySearch || !seenMemoryWrite || !seenScheduleCreate || !seenScheduleList || !seenSchedulePause || !seenScheduleResume || !seenScheduleDelete {
		t.Fatalf("gateway.methods missing expected descriptors: status=%v submit=%v resume=%v context_status=%v context_stats=%v patch=%v memory_status=%v memory_search=%v memory_write=%v schedule_create=%v schedule_list=%v schedule_pause=%v schedule_resume=%v schedule_delete=%v",
			seenStatus, seenSubmit, seenSessionResume, seenContextStatus, seenContextStats, seenPatchPropose, seenMemoryStatus, seenMemorySearch, seenMemoryWrite, seenScheduleCreate, seenScheduleList, seenSchedulePause, seenScheduleResume, seenScheduleDelete)
	}
	var hello struct {
		Role     string   `json:"role"`
		Scopes   []string `json:"scopes"`
		Features []string `json:"features"`
	}
	if err := c.Call("gateway.hello", map[string]any{"role": "operator", "scopes": []string{"read", "admin", "worker"}}, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Role != "operator" || len(hello.Scopes) != 2 || hello.Scopes[0] != "read" || hello.Scopes[1] != "admin" {
		t.Fatalf("unexpected gateway.hello negotiation: %+v", hello)
	}
	var resolved struct {
		Method       string `json:"method"`
		Scope        string `json:"scope"`
		DynamicScope bool   `json:"dynamic_scope"`
	}
	assertScope := func(method string, params map[string]any, wantScope string, wantDynamic bool) {
		t.Helper()
		if err := c.Call("gateway.resolve_scope", map[string]any{
			"method": method,
			"params": params,
		}, &resolved); err != nil {
			t.Fatal(err)
		}
		if resolved.Scope != wantScope || resolved.DynamicScope != wantDynamic {
			t.Fatalf("%s should resolve to scope=%s dynamic=%v: %+v", method, wantScope, wantDynamic, resolved)
		}
	}
	assertScope("workspace.patch.propose",
		map[string]any{"files": []map[string]any{{"path": "a.go", "new_content": "package p\n"}}},
		"write", true)
	assertScope("workspace.patch.propose",
		map[string]any{"files": []map[string]any{{"path": "../escape.go", "new_content": "x"}}},
		"admin", true)

	// worker lifecycle
	var reg struct {
		WorkerID string `json:"worker_id"`
	}
	if err := c.Call("worker.register", map[string]any{"name": "w", "kind": "ci"}, &reg); err != nil {
		t.Fatal(err)
	}
	must("worker.heartbeat", map[string]any{"worker_id": reg.WorkerID})
	must("worker.list", map[string]any{})
	must("worker.revoke", map[string]any{"worker_id": reg.WorkerID})

	// session + workspace
	var sess struct {
		SessionID string `json:"session_id"`
	}
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "safe-edit"}, &sess); err != nil {
		t.Fatal(err)
	}
	sid := sess.SessionID
	insideDir := filepath.Join(ws, "nested")
	if err := os.Mkdir(insideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	assertScope("session.add_dir", map[string]any{"session_id": sid, "path": insideDir}, "write", true)
	assertScope("session.add_dir", map[string]any{"session_id": sid, "path": t.TempDir()}, "admin", true)
	assertScope("workspace.trust", map[string]any{"root": ws, "trusted": false}, "write", true)
	assertScope("workspace.trust", map[string]any{"root": ws, "trusted": true}, "admin", true)
	assertScope("task.action.deny", map[string]any{"session_id": sid, "decision_id": "dec_test"}, "write", true)
	assertScope("task.action.deny", map[string]any{"session_id": sid, "decision_id": "dec_test", "approver": "alice"}, "admin", true)
	assertScope("task.action.approve", map[string]any{"session_id": sid, "decision_id": "dec_test"}, "admin", false)
	must("session.get", map[string]any{"session_id": sid})
	must("session.list", map[string]any{})
	must("session.pause", map[string]any{"session_id": sid})
	must("session.resume", map[string]any{"session_id": sid})
	must("workspace.tree", map[string]any{"session_id": sid})
	must("workspace.search", map[string]any{"session_id": sid, "pattern": "TODO"})
	must("workspace.file.get", map[string]any{"session_id": sid, "path": "a.go"})
	must("profile.describe", map[string]any{"session_id": sid})
	var memoryWrite struct {
		Decision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"decision"`
	}
	if err := c.Call("memory.write", map[string]any{"session_id": sid, "target": "memory", "action": "add", "content": "Use focused tests before release checks."}, &memoryWrite); err != nil {
		t.Fatal(err)
	}
	if memoryWrite.Decision.Decision == "requires_approval" {
		must("task.action.approve", map[string]any{"session_id": sid, "decision_id": memoryWrite.Decision.DecisionID})
	}
	var memoryList struct {
		Entries []string `json:"entries"`
	}
	if err := c.Call("memory.list", map[string]any{"session_id": sid, "target": "memory"}, &memoryList); err != nil {
		t.Fatal(err)
	}
	if len(memoryList.Entries) != 1 || !strings.Contains(memoryList.Entries[0], "focused tests") {
		t.Fatalf("unexpected memory.list result: %+v", memoryList)
	}
	var memoryContext struct {
		Context string `json:"context"`
	}
	if err := c.Call("memory.context", map[string]any{"session_id": sid}, &memoryContext); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(memoryContext.Context, "<memory-context>") {
		t.Fatalf("memory.context should be fenced: %+v", memoryContext)
	}
	var memoryStatus struct {
		SemanticProvider struct {
			Enabled  bool   `json:"enabled"`
			Provider string `json:"provider"`
		} `json:"semantic_provider"`
		NebutraCloudSync struct {
			SyncMode string `json:"sync_mode"`
		} `json:"nebutra_cloud_sync"`
	}
	if err := c.Call("memory.status", map[string]any{"session_id": sid}, &memoryStatus); err != nil {
		t.Fatal(err)
	}
	if memoryStatus.NebutraCloudSync.SyncMode != "off" {
		t.Fatalf("unexpected memory.status boundary: %+v", memoryStatus)
	}
	if memoryStatus.SemanticProvider.Enabled {
		if memoryStatus.SemanticProvider.Provider != "byok-embeddings" {
			t.Fatalf("enabled semantic memory provider must be BYOK-scoped: %+v", memoryStatus)
		}
	} else if memoryStatus.SemanticProvider.Provider != "local-only" {
		t.Fatalf("disabled semantic memory provider should stay local-only: %+v", memoryStatus)
	}

	// patches: propose carries the PatchApply gate decision; apply requires
	// it to be approved first (governed flow).
	var patch struct {
		PatchID       string `json:"patch_id"`
		ApplyDecision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"apply_decision"`
	}
	if err := c.Call("workspace.patch.propose", map[string]any{
		"session_id": sid, "reason": "t", "files": []map[string]any{{"path": "a.go", "new_content": "package p\n"}},
	}, &patch); err != nil {
		t.Fatal(err)
	}
	must("workspace.patch.list", map[string]any{"session_id": sid})
	must("workspace.patch.show", map[string]any{"session_id": sid, "patch_id": patch.PatchID})
	if err := c.Call("workspace.patch.apply", map[string]any{"session_id": sid, "patch_id": patch.PatchID}, nil); err == nil {
		t.Fatal("workspace.patch.apply must refuse before the gate decision is approved")
	}
	if patch.ApplyDecision.Decision != "requires_approval" {
		t.Fatalf("propose should gate apply as requires_approval, got %+v", patch.ApplyDecision)
	}
	must("task.action.approve", map[string]any{"session_id": sid, "decision_id": patch.ApplyDecision.DecisionID})
	must("workspace.patch.apply", map[string]any{"session_id": sid, "patch_id": patch.PatchID})
	must("workspace.patch.rollback", map[string]any{"session_id": sid, "patch_id": patch.PatchID})

	// task lifecycle
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := c.Call("task.submit", map[string]any{"session_id": sid, "prompt": "hi"}, &task); err != nil {
		t.Fatal(err)
	}
	must("task.status", map[string]any{"task_id": task.TaskID})
	must("task.cancel", map[string]any{"task_id": task.TaskID})

	// secrets + audit
	must("secret.grant", map[string]any{"session_id": sid, "name": "K", "value": "v"})
	must("secret.request", map[string]any{"session_id": sid, "name": "K"})
	must("session.items", map[string]any{"session_id": sid})
	must("audit.report", map[string]any{"session_id": sid})
	must("audit.export", map[string]any{"session_id": sid})
	must("audit.verify", map[string]any{"session_id": sid})

	// command approval flow: local move -> approve -> executes without network.
	if err := os.WriteFile(filepath.Join(ws, "approve-src.txt"), []byte("move me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var exec struct {
		Decision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"decision"`
	}
	if err := c.Call("command.exec", map[string]any{"session_id": sid, "argv": []string{"mv", "approve-src.txt", "approve-dst.txt"}}, &exec); err != nil {
		t.Fatal(err)
	}
	if exec.Decision.Decision == "requires_approval" {
		must("task.action.approve", map[string]any{"session_id": sid, "decision_id": exec.Decision.DecisionID})
	}
	// deny path
	if err := os.WriteFile(filepath.Join(ws, "deny-src.txt"), []byte("do not move\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var exec2 struct {
		Decision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"decision"`
	}
	c.Call("command.exec", map[string]any{"session_id": sid, "argv": []string{"mv", "deny-src.txt", "deny-dst.txt"}}, &exec2)
	if exec2.Decision.DecisionID != "" {
		must("task.action.deny", map[string]any{"session_id": sid, "decision_id": exec2.Decision.DecisionID, "reason": "no"})
	}

	must("session.close", map[string]any{"session_id": sid})

	// Error paths: unknown session / missing params.
	if err := c.Call("session.get", map[string]any{"session_id": "sess_missing"}, nil); err == nil {
		t.Fatal("unknown session should error")
	}
	if err := c.Call("session.create", map[string]any{"profile": "safe-edit"}, nil); err == nil {
		t.Fatal("missing workspace_root should error")
	}
	if err := c.Call("command.exec", map[string]any{"session_id": "sess_missing", "argv": []string{"ls"}}, nil); err == nil {
		t.Fatal("exec on unknown session should error")
	}
}

func TestMemoryWriteApprovalFlow(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	stateDir := t.TempDir()
	policyDir := filepath.Join(stateDir, "policy")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "bundle.toml"), []byte(`
name = "memory-review"
require_approval = ["MemoryWrite"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.New(daemon.Options{
		StateDir: stateDir, KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), PolicyDir: policyDir, Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)
	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var sess struct {
		SessionID string `json:"session_id"`
	}
	if err := c.Call("session.create", map[string]any{"workspace_root": t.TempDir(), "profile": "safe-edit"}, &sess); err != nil {
		t.Fatal(err)
	}
	var write struct {
		Decision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"decision"`
		Result *struct {
			Success bool `json:"success"`
		} `json:"result"`
	}
	if err := c.Call("memory.write", map[string]any{
		"session_id": sess.SessionID,
		"target":     "memory",
		"action":     "add",
		"content":    "Run focused memory approval tests before release.",
	}, &write); err != nil {
		t.Fatal(err)
	}
	if write.Decision.Decision != "requires_approval" || write.Decision.DecisionID == "" || write.Result != nil {
		t.Fatalf("expected pending memory approval, got %+v", write)
	}
	var before struct {
		Entries []string `json:"entries"`
	}
	if err := c.Call("memory.list", map[string]any{"session_id": sess.SessionID, "target": "memory"}, &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Entries) != 0 {
		t.Fatalf("pending memory write must not apply before approval: %+v", before)
	}
	var approved struct {
		Decision struct {
			Decision string `json:"decision"`
		} `json:"decision"`
		Result struct {
			Success       bool   `json:"success"`
			ContentSHA256 string `json:"content_sha256"`
		} `json:"result"`
	}
	if err := c.Call("task.action.approve", map[string]any{
		"session_id":  sess.SessionID,
		"decision_id": write.Decision.DecisionID,
		"approver":    "operator",
	}, &approved); err != nil {
		t.Fatal(err)
	}
	if approved.Decision.Decision != "allowed" || !approved.Result.Success || approved.Result.ContentSHA256 == "" {
		t.Fatalf("approved memory write should apply with hash metadata: %+v", approved)
	}
	var after struct {
		Entries []string `json:"entries"`
	}
	if err := c.Call("memory.list", map[string]any{"session_id": sess.SessionID, "target": "memory"}, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != 1 || !strings.Contains(after.Entries[0], "focused memory approval") {
		t.Fatalf("approved memory write did not persist: %+v", after)
	}

	var deniedWrite struct {
		Decision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"decision"`
	}
	if err := c.Call("memory.write", map[string]any{
		"session_id": sess.SessionID,
		"target":     "user",
		"action":     "add",
		"content":    "This denied user memory must not persist.",
	}, &deniedWrite); err != nil {
		t.Fatal(err)
	}
	if deniedWrite.Decision.Decision != "requires_approval" || deniedWrite.Decision.DecisionID == "" {
		t.Fatalf("expected pending deny candidate, got %+v", deniedWrite)
	}
	var denied struct {
		Decision string `json:"decision"`
	}
	if err := c.Call("task.action.deny", map[string]any{
		"session_id":  sess.SessionID,
		"decision_id": deniedWrite.Decision.DecisionID,
		"reason":      "not useful",
	}, &denied); err != nil {
		t.Fatal(err)
	}
	if denied.Decision != "denied" {
		t.Fatalf("expected denied memory write decision, got %+v", denied)
	}
	var userMemory struct {
		Entries []string `json:"entries"`
	}
	if err := c.Call("memory.list", map[string]any{"session_id": sess.SessionID, "target": "user"}, &userMemory); err != nil {
		t.Fatal(err)
	}
	if len(userMemory.Entries) != 0 {
		t.Fatalf("denied memory write should not persist: %+v", userMemory)
	}
}

func TestDaemonGatewayTokenIssueConfigured(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	keyFile := filepath.Join(t.TempDir(), "gateway-token.key")
	if err := os.WriteFile(keyFile, []byte("01234567890123456789012345678901\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.New(daemon.Options{
		StateDir:                   t.TempDir(),
		KernelBin:                  kernelBin,
		ToolsDir:                   filepath.Join(repoRoot, "zig/zig-out/bin"),
		GatewayTokenSigningKeyFile: keyFile,
		GatewayTokenMaxTTLSeconds:  120,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)
	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out struct {
		Token  string `json:"token"`
		Claims struct {
			Role      string   `json:"role"`
			Scopes    []string `json:"scopes"`
			Transport string   `json:"transport"`
		} `json:"claims"`
	}
	if err := c.Call("gateway.token.issue", map[string]any{
		"subject": "ws-probe", "role": "operator", "scopes": []string{"read", "admin", "worker"}, "ttl_seconds": 60, "transport": "ws",
	}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Token, "gw1.") || out.Claims.Role != "operator" || out.Claims.Transport != "ws" {
		t.Fatalf("unexpected token issue result: %+v", out)
	}
	if len(out.Claims.Scopes) != 2 || out.Claims.Scopes[0] != "read" || out.Claims.Scopes[1] != "admin" {
		t.Fatalf("unexpected token scopes: %+v", out.Claims.Scopes)
	}
	if err := c.Call("gateway.token.issue", map[string]any{"role": "operator", "scopes": []string{"read"}, "ttl_seconds": 600, "transport": "ws"}, nil); err == nil {
		t.Fatal("over-max gateway token ttl should fail")
	}
	if err := c.Call("gateway.token.issue", map[string]any{"role": "operator", "ttl_seconds": 60, "transport": "ws"}, nil); err == nil {
		t.Fatal("missing gateway token scopes should fail")
	}
}

func TestDaemonGatewayTokenIssueDisabledByDefault(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	d, err := daemon.New(daemon.Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)
	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("gateway.token.issue", map[string]any{"role": "operator", "scopes": []string{"read"}}, nil); err == nil {
		t.Fatal("gateway.token.issue should be unavailable without signing key config")
	}
}

func TestDaemonGatewayTokenSigningKeyRequiresPrivateFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "gateway-token.key")
	if err := os.WriteFile(keyFile, []byte("01234567890123456789012345678901\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := daemon.New(daemon.Options{StateDir: t.TempDir(), GatewayTokenSigningKeyFile: keyFile})
	if err == nil || !strings.Contains(err.Error(), "must not be group/world readable") {
		t.Fatalf("expected private key-file permission error, got %v", err)
	}
}

// TestDaemonEventStream covers the streaming subscription handler and the
// event bus fan-out (PRD §8.6).
func TestDaemonEventStream(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	d, err := daemon.New(daemon.Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)

	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var sess struct {
		SessionID string `json:"session_id"`
	}
	c.Call("session.create", map[string]any{"workspace_root": t.TempDir(), "profile": "safe-edit"}, &sess)

	sub, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := sub.Call("session.events.stream", map[string]any{"session_id": sess.SessionID}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 4)
	go func() {
		for {
			m, _, err := sub.ReadNotification()
			if err != nil {
				return
			}
			got <- m
		}
	}()
	c.Call("task.submit", map[string]any{"session_id": sess.SessionID, "prompt": "go"}, &struct{}{})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no event streamed")
	}
}
