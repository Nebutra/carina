package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	modelrouter "github.com/TsekaLuk/pi-os/go/model-router"
	"github.com/TsekaLuk/pi-os/go/kernel"
	"github.com/TsekaLuk/pi-os/go/scheduler"
	sessionstore "github.com/TsekaLuk/pi-os/go/session-store"
)

const (
	maxAgentTurns = 14
	maxRequeries  = 3
)

// systemPrompt instructs the reasoner to act as a coding agent that can only
// affect the world through pi-os tools, one JSON action at a time.
const systemPrompt = `You are a coding agent running inside the pi-os runtime.
You CANNOT touch the system directly. You act only by emitting ONE tool action
per turn as a single JSON object, and pi-os executes it through its security
kernel, returning an observation.

Available tools:
- {"tool":"list"}                              list the workspace file tree
- {"tool":"read","path":"rel/path"}            read a file
- {"tool":"search","pattern":"text"}           search the workspace
- {"tool":"run","command":["prog","arg"]}      run a command (sandboxed; risky commands are denied)
- {"tool":"patch","path":"rel/path","content":"FULL new file content"}   propose+apply an edit (transactional, rollbackable)
- {"tool":"done","summary":"what you did / found"}   finish the task

Rules:
- Reply with ONLY the JSON object for the next action. No prose, no markdown fences.
- Think step by step across turns: read/search first, then act.
- Use "patch" to change files (never shell for edits). Provide the COMPLETE new file content.
- When the task is complete, use "done" with a clear summary.`

// action is the decision emitted by the reasoner each turn. Fields are read
// from the top level (flat form the model naturally emits) or from a nested
// "action" object (see parseAction).
type action struct {
	Thought string   `json:"thought"`
	Tool    string   `json:"tool"`
	Path    string   `json:"path"`
	Pattern string   `json:"pattern"`
	Command []string `json:"command"`
	Content string   `json:"content"`
	Summary string   `json:"summary"`
}

// runTask drives one agent task to completion (PRD §18). Every side effect is
// mediated by the Rust capability kernel and executed by the Zig toolchain;
// the reasoner only decides. If no reasoner is configured, it falls back to
// the mock single-shot loop so the runtime still works offline.
func (d *Daemon) runTask(sess *sessionstore.Session, task *scheduler.Task) {
	d.sched.SetStatus(task.TaskID, "running")

	if d.reasoner == nil {
		d.runMockTask(sess, task)
		return
	}

	d.record(sess.SessionID, "ModelRequested", task.TaskID, "go",
		map[string]any{"engine": d.reasoner.Name(), "prompt": task.UserPrompt}, "")

	ctx := context.Background()
	tr := newTranscript(task.UserPrompt)
	guard := newLoopGuard()
	// A cheap summarizer for compaction: reuse the reasoner on the head.
	summarize := func(head string) (string, error) {
		return thinkWithRetry(ctx, d.reasoner,
			"Summarize this agent transcript in <=200 words, keeping: the task, decisions made, "+
				"patches applied (ids), unresolved errors. Drop raw tool output.\n\n"+head)
	}

	for turn := 1; turn <= maxAgentTurns; turn++ {
		if t, ok := d.sched.Get(task.TaskID); ok && t.Status == "cancelled" {
			return
		}

		// Bound the model view (audit log keeps everything).
		tr.compact(summarize)
		prompt := fmt.Sprintf("%s\n\nTASK: %s\n\nTRANSCRIPT:\n%s\nRespond with the next action as a single JSON object.",
			systemPrompt, task.UserPrompt, tr.render())

		// inner requery loop: malformed actions are re-asked without
		// consuming a real turn (up to maxRequeries).
		var act action
		var raw string
		ok := false
		for requery := 0; requery <= maxRequeries; requery++ {
			var err error
			raw, err = thinkWithRetry(ctx, d.reasoner, prompt)
			if err != nil {
				d.degrade(sess, task, tr, "reasoner error: "+err.Error())
				return
			}
			d.record(sess.SessionID, "ModelResponded", task.TaskID, "model",
				map[string]any{"turn": turn, "text": truncate(raw, 400)}, "")
			a, perr := parseAction(raw)
			if perr == nil {
				act, ok = a, true
				break
			}
			prompt = fmt.Sprintf("%s\n\nYour last reply was not a valid action JSON (%s). "+
				"Reply with ONE JSON object like {\"tool\":\"read\",\"path\":\"...\"}.", prompt, perr.Error())
		}
		if !ok {
			d.degrade(sess, task, tr, "model kept emitting invalid actions")
			return
		}

		if act.Tool == "done" {
			d.finish(sess, task, act.Summary)
			return
		}

		// Loop safety: catch repeated actions and no-progress stalls.
		fp := act.Tool + ":" + act.Path + ":" + strings.Join(act.Command, " ") + ":" + act.Pattern
		if guard.repeated(act.Tool, fp) {
			tr.addTurn(Turn{Thought: act.Thought, Tool: act.Tool,
				ActionBrief: briefAction(&act),
				Obs:         Observation{Content: "You have repeated this exact action several times with no new result. Change approach, or use {\"tool\":\"done\"} if finished."}})
			continue
		}
		if guard.stalled() {
			tr.addTurn(Turn{Tool: "system",
				ActionBrief: "loop-guard",
				Obs:         Observation{Content: "Many turns with no edit. Either make a concrete change with the patch tool, or finish with done."}})
			guard.madeProgress() // reset so we give one more chance, then degrade
		}

		obs := d.executeAction(sess, task, &act)
		pinned := act.Tool == "run" || act.Tool == "patch" // keep test/patch results
		if act.Tool == "patch" && strings.Contains(obs, "applied") {
			guard.madeProgress()
		} else {
			guard.tick()
		}
		tr.addTurn(Turn{Thought: act.Thought, Tool: act.Tool,
			ActionBrief: briefAction(&act), Obs: Observation{Content: obs, Pinned: pinned}})
	}

	d.degrade(sess, task, tr, "reached max turns without done")
}

// finish marks a task completed with the model's summary.
func (d *Daemon) finish(sess *sessionstore.Session, task *scheduler.Task, summary string) {
	d.sched.SetStatus(task.TaskID, "completed")
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
		map[string]any{"status": "completed", "summary": summary}, "")
}

// degrade ends a task that couldn't reach done, but does so gracefully:
// it reports partial progress (applied patches survive and are rollbackable)
// rather than a bare failure (the SWE-agent "autosubmit" idea).
func (d *Daemon) degrade(sess *sessionstore.Session, task *scheduler.Task, tr *Transcript, reason string) {
	patches, _ := d.kern.PatchList(sess.SessionID)
	applied := make([]string, 0, len(patches))
	for _, p := range patches {
		if p.Status == "applied" || p.Status == "committed" {
			applied = append(applied, p.PatchID)
		}
	}
	d.sched.SetStatus(task.TaskID, "degraded")
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
		"status": "degraded", "reason": reason,
		"turns": len(tr.Turns), "applied_patches": applied,
	}, "")
}

func briefAction(a *action) string {
	switch a.Tool {
	case "read", "patch":
		return a.Tool + " " + a.Path
	case "search":
		return "search " + a.Pattern
	case "run":
		return "run [" + strings.Join(a.Command, " ") + "]"
	default:
		return a.Tool
	}
}

// executeAction runs one tool action through the kernel + toolchain and
// returns the observation to feed back to the reasoner.
func (d *Daemon) executeAction(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	switch act.Tool {
	case "list":
		dec, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, task.TaskID)
		if err != nil || dec.Decision != "allowed" {
			return "DENIED: cannot read workspace"
		}
		files, err := d.tools.Scan(sess.WorkspaceRoot)
		if err != nil {
			return "error: " + err.Error()
		}
		d.record(sess.SessionID, "FileRead", task.TaskID, "zig",
			map[string]any{"resource": sess.WorkspaceRoot, "bytes": len(files)}, dec.DecisionID)
		var b strings.Builder
		for i, f := range files {
			if i >= 200 {
				break
			}
			fmt.Fprintf(&b, "%s (%d bytes, %s)\n", f.Path, f.Size, f.Language)
		}
		return b.String()

	case "read":
		abs := resolveIn(sess.WorkspaceRoot, act.Path)
		dec, err := d.kern.Request(sess.SessionID, "FileRead", abs, task.TaskID)
		if err != nil {
			return "error: " + err.Error()
		}
		if dec.Decision != "allowed" {
			return "DENIED: " + dec.Reason
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			return "error: " + err.Error()
		}
		d.record(sess.SessionID, "FileRead", task.TaskID, "go",
			map[string]any{"path": abs, "bytes": len(content)}, dec.DecisionID)
		return string(content)

	case "search":
		dec, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, task.TaskID)
		if err != nil || dec.Decision != "allowed" {
			return "DENIED: cannot search workspace"
		}
		matches, err := d.tools.Grep(act.Pattern, sess.WorkspaceRoot)
		if err != nil {
			return "error: " + err.Error()
		}
		d.record(sess.SessionID, "FileRead", task.TaskID, "zig",
			map[string]any{"resource": sess.WorkspaceRoot, "pattern": act.Pattern, "matches": len(matches)}, dec.DecisionID)
		if len(matches) == 0 {
			return "no matches"
		}
		var b strings.Builder
		for i, m := range matches {
			if i >= 50 {
				break
			}
			fmt.Fprintf(&b, "%s:%d: %s\n", m.File, m.Line, m.Text)
		}
		return b.String()

	case "run":
		if len(act.Command) == 0 {
			return "error: empty command"
		}
		return d.agentRun(sess, task, act.Command)

	case "patch":
		return d.agentPatch(sess, task, act.Path, act.Content)

	default:
		return "unknown tool: " + act.Tool
	}
}

// agentPatch proposes and applies a full-file edit through the kernel's
// transactional patch engine (writes land via Zig pi-patch-native).
func (d *Daemon) agentPatch(sess *sessionstore.Session, task *scheduler.Task, path, content string) string {
	if path == "" {
		return "error: patch needs a path"
	}
	patch, err := d.kern.PatchPropose(sess.SessionID, task.TaskID, "agent edit",
		[]kernel.FileChange{{Path: path, NewContent: content}})
	if err != nil {
		return "patch propose failed: " + err.Error()
	}
	applied, err := d.kern.PatchApply(sess.SessionID, patch.PatchID, "agent")
	if err != nil {
		return "patch apply failed (nothing written): " + err.Error()
	}
	return fmt.Sprintf("patch %s applied to %s (status=%s, rollbackable)", applied.PatchID, path, applied.Status)
}

// agentRun executes a command the agent proposed: kernel decision first
// (destructive => denied; risky => auto-approved in autonomous mode), then
// Zig pi-run. Every step is audited.
func (d *Daemon) agentRun(sess *sessionstore.Session, task *scheduler.Task, argv []string) string {
	command := strings.Join(argv, " ")
	dec, err := d.kern.Request(sess.SessionID, "CommandExec", command, task.TaskID)
	if err != nil {
		return "error: " + err.Error()
	}
	switch dec.Decision {
	case "denied":
		return "DENIED by policy: " + dec.Reason
	case "requires_approval":
		approved, aerr := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, "agent", "")
		if aerr != nil || approved.Decision != "allowed" {
			return "requires approval (not granted): " + dec.Reason
		}
		dec = approved
	}

	risk, _ := d.kern.ClassifyCommand(command)
	started := map[string]any{"command": command, "cwd": sess.WorkspaceRoot, "risk_level": risk}
	if mutatesPackages(command) {
		started["package_mutation"] = true
	}
	d.record(sess.SessionID, "CommandStarted", task.TaskID, "zig", started, dec.DecisionID)

	result, err := d.tools.Run(argv, sess.WorkspaceRoot, 2*time.Minute)
	if err != nil {
		d.record(sess.SessionID, "CommandExited", task.TaskID, "zig", map[string]any{"exit_code": -1, "error": err.Error()}, "")
		return "command error: " + err.Error()
	}
	stdout := strings.Join(result.Stdout, "\n")
	if red, e := d.kern.Redact(sess.SessionID, stdout); e == nil {
		stdout = red
	}
	d.record(sess.SessionID, "CommandOutput", task.TaskID, "zig", map[string]any{"stream": "stdout", "chunk": truncate(stdout, 400)}, "")
	d.record(sess.SessionID, "CommandExited", task.TaskID, "zig", map[string]any{"exit_code": result.ExitCode, "duration_ms": result.DurationMs}, "")

	var b strings.Builder
	fmt.Fprintf(&b, "exit=%d\n%s", result.ExitCode, stdout)
	if len(result.Stderr) > 0 {
		fmt.Fprintf(&b, "\n[stderr] %s", strings.Join(result.Stderr, "\n"))
	}
	return b.String()
}

func resolveIn(root, path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return root + "/" + path
}

func parseAction(raw string) (action, error) {
	// Strip markdown fences and extract the first {...} block.
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return action{}, fmt.Errorf("no json object")
	}
	block := []byte(raw[start : end+1])
	var a action
	if err := json.Unmarshal(block, &a); err != nil {
		return action{}, err
	}
	// Accept a nested {"action": {...}} form too.
	if a.Tool == "" {
		var nested struct {
			Action action `json:"action"`
		}
		if json.Unmarshal(block, &nested) == nil && nested.Action.Tool != "" {
			a = nested.Action
		}
	}
	if a.Tool == "" {
		return action{}, fmt.Errorf("no tool in action")
	}
	return a, nil
}

// runMockTask is the offline fallback: read the workspace, ask the mock
// model, record the trace. Keeps the runtime functional without a reasoner.
func (d *Daemon) runMockTask(sess *sessionstore.Session, task *scheduler.Task) {
	decision, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, task.TaskID)
	if err == nil && decision.Decision == "allowed" {
		if files, err := d.tools.Scan(sess.WorkspaceRoot); err == nil {
			d.record(sess.SessionID, "FileRead", task.TaskID, "zig",
				map[string]any{"resource": sess.WorkspaceRoot, "bytes": len(files)}, decision.DecisionID)
		}
	}
	d.record(sess.SessionID, "ModelRequested", task.TaskID, "go",
		map[string]any{"prompt": task.UserPrompt}, "")
	resp, err := d.router.Complete(context.Background(), modelrouter.Request{Model: "default", Prompt: task.UserPrompt})
	if err != nil {
		d.sched.SetStatus(task.TaskID, "failed")
		d.record(sess.SessionID, "ModelResponded", task.TaskID, "model", map[string]any{"error": err.Error()}, "")
		return
	}
	d.record(sess.SessionID, "ModelResponded", task.TaskID, "model", map[string]any{
		"provider": resp.Provider, "model": resp.Model, "text": truncate(resp.Text, 500),
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

func registerProviders(router *modelrouter.Router, offline bool) {
	if !offline {
		router.RegisterProvider(NewAnthropicProviderFromEnv())
	}
	router.RegisterProvider(modelrouter.NewMockProvider())
	_ = time.Now
}
