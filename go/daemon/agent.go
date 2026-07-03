package daemon

import (
	"context"
	"strings"
	"time"

	modelrouter "github.com/TsekaLuk/pi-os/go/model-router"
	"github.com/TsekaLuk/pi-os/go/scheduler"
	sessionstore "github.com/TsekaLuk/pi-os/go/session-store"
)

// runTask drives one agent turn (PRD §18 MVP loop). It is intentionally
// small: scan the workspace, call the model, and record the full trace to
// the event log. Real tool-calling orchestration is provided by the Agent
// Surface (TypeScript) over RPC; this in-daemon loop keeps `pi run` useful
// standalone and exercises the Go→Rust→Zig path end to end.
func (d *Daemon) runTask(sess *sessionstore.Session, task *scheduler.Task) {
	d.sched.SetStatus(task.TaskID, "running")

	// 1. Read the workspace (FileRead capability → Zig pi-scan).
	decision, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, task.TaskID)
	if err == nil && decision.Decision == "allowed" {
		if files, err := d.tools.Scan(sess.WorkspaceRoot); err == nil {
			d.record(sess.SessionID, "FileRead", task.TaskID,
				map[string]any{"resource": sess.WorkspaceRoot, "bytes": len(files)}, decision.DecisionID)
		}
	}

	// 2. Call the model router.
	d.record(sess.SessionID, "ModelRequested", task.TaskID,
		map[string]any{"prompt": task.UserPrompt}, "")
	resp, err := d.router.Complete(context.Background(), modelrouter.Request{
		Model:  "default",
		Prompt: task.UserPrompt,
	})
	if err != nil {
		d.sched.SetStatus(task.TaskID, "failed")
		d.record(sess.SessionID, "ModelResponded", task.TaskID,
			map[string]any{"error": err.Error()}, "")
		return
	}
	d.record(sess.SessionID, "ModelResponded", task.TaskID, map[string]any{
		"provider": resp.Provider, "model": resp.Model,
		"output_tokens": resp.OutputTokens, "text": truncate(resp.Text, 500),
	}, "")

	d.sched.SetStatus(task.TaskID, "completed")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// registerProviders installs the default model providers. The mock
// provider keeps the runtime fully functional offline (PRD §17 DX: first
// task in 5 minutes); real providers are added when API keys are present.
func registerProviders(router *modelrouter.Router) {
	router.RegisterProvider(NewAnthropicProviderFromEnv())
	router.RegisterProvider(modelrouter.NewMockProvider())
	_ = time.Now
}
