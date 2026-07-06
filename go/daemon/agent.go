package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/kernel"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	maxAgentTurns     = 14
	maxRequeries      = 3
	maxVerifyAttempts = 3
)

// toolsHelp is the shared tool reference used by the main agent and subagents.
const toolsHelp = `Available tools:
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

// systemPrompt instructs the reasoner to act as a coding agent that can only
// affect the world through the Nebutra runtime, one JSON action at a time.
const systemPrompt = `You are a coding agent running on the Nebutra agent runtime.
You CANNOT touch the system directly. You act only by emitting ONE tool action
per turn as a single JSON object, and the Nebutra runtime executes it through
its security kernel, returning an observation.

` + toolsHelp + `

You may also delegate to specialized subagents (isolated context, restricted
capabilities) for focused sub-tasks like recon or review:
- {"tool":"spawn","agent":"scout","task":"find all auth code"}
- {"tool":"spawn","tasks":[{"agent":"scout","task":"..."},{"agent":"reviewer","task":"..."}]}   (parallel)

For a repeatable multi-step pipeline, run a named workflow (a dependency DAG of
subagents; independent steps run in parallel, and each step's output is
available to later steps as ${step_id}). Top-level only:
- {"tool":"workflow","workflow":"review","task":"optional input, available to every step as ${input}"}

To gather context faster, batch several READ-ONLY tools (list/read/search) to run
in parallel in one turn:
- {"actions":[{"tool":"read","path":"a.go"},{"tool":"read","path":"b.go"},{"tool":"search","pattern":"foo"}]}
Writes (patch/run) must stay one action per turn — never put them in a batch.`

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
	// spawn tool
	Agent string      `json:"agent"`
	Task  string      `json:"task"`
	Tasks []SpawnTask `json:"tasks"`
	// workflow tool
	Workflow string `json:"workflow"`
	// mcp tool
	MCPServer string         `json:"mcp_server"`
	MCPTool   string         `json:"mcp_tool"`
	Args      map[string]any `json:"args"`
	// intra-turn parallel batch of read-only actions (list/read/search)
	Actions []action `json:"actions,omitempty"`
}

// SpawnTask is one delegation in a parallel spawn.
type SpawnTask struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
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
	d.runLoop(sess, task, newTranscript(task.UserPrompt), 1)
}

// resumeTask continues a background run from a persisted transcript checkpoint
// after a daemon restart. Prior turns (and their side effects) are already in
// the transcript and the audit log, so only the NEXT action runs — completed
// work is never re-executed.
func (d *Daemon) resumeTask(sess *sessionstore.Session, task *scheduler.Task, cp *runCheckpoint) {
	d.sched.SetStatus(task.TaskID, "running")
	if d.reasoner == nil {
		d.degrade(sess, task, cp.Transcript, "no reasoner available to resume run")
		return
	}
	d.record(sess.SessionID, "ModelRequested", task.TaskID, "go",
		map[string]any{"engine": d.reasoner.Name(), "prompt": task.UserPrompt, "resumed_from_turn": cp.Turn}, "")
	d.runLoop(sess, task, cp.Transcript, cp.Turn+1)
}

// runLoop is the ReAct loop shared by fresh (runTask) and resumed (resumeTask)
// runs. It checkpoints the transcript after each turn, so a daemon crash loses
// at most one in-flight action.
func (d *Daemon) runLoop(sess *sessionstore.Session, task *scheduler.Task, tr *Transcript, startTurn int) {
	// Refresh the task so settings applied after submit (output schema, mode)
	// are visible — the scheduler replaces the row on each update.
	if t, ok := d.sched.Get(task.TaskID); ok {
		task = t
	}
	ctx := context.Background()
	guard := newLoopGuard()
	verifyAttempts := 0
	// A cheap summarizer for compaction: reuse the reasoner on the head.
	summarize := func(head string) (string, error) {
		return thinkWithRetry(ctx, d.summarizeReasoner(),
			"Summarize this agent transcript in <=200 words, keeping: the task, decisions made, "+
				"patches applied (ids), unresolved errors. Drop raw tool output.\n\n"+head)
	}

	// Persistent project/user memory (CARINA.md) is prepended to the system
	// prompt so the agent follows repo-specific conventions.
	sysPrompt := systemPrompt
	if mem := loadMemory(sess.WorkspaceRoot); mem != "" {
		sysPrompt = systemPrompt + "\n\nPROJECT MEMORY (from CARINA.md — follow it):\n" + mem
	}
	if style := loadStyle(sess.WorkspaceRoot); style != "" {
		sysPrompt = "OUTPUT STYLE (apply to your presentation):\n" + style + "\n\n" + sysPrompt
	}
	if tools := d.mcp.Tools(); len(tools) > 0 {
		var b strings.Builder
		b.WriteString("\n\nMCP TOOLS (call via {\"tool\":\"mcp\",\"mcp_server\":\"<server>\",\"mcp_tool\":\"<name>\",\"args\":{...}}):\n")
		for _, t := range tools {
			fmt.Fprintf(&b, "- mcp__%s__%s: %s\n", t.Server, t.Name, truncate(t.Description, 120))
		}
		sysPrompt += b.String()
	}

	for turn := startTurn; turn <= maxAgentTurns; turn++ {
		if t, ok := d.sched.Get(task.TaskID); ok && t.Status == "cancelled" {
			return
		}

		// Drain async steering messages at the turn boundary so a running
		// (background) agent can be redirected without a restart.
		for _, msg := range d.drainMailbox(task.TaskID) {
			tr.addTurn(Turn{Tool: "user", ActionBrief: "steer",
				Obs: Observation{Content: "USER STEERING (incorporate this now): " + msg, Pinned: true}})
			d.record(sess.SessionID, "TaskCreated", task.TaskID, "user",
				map[string]any{"status": "steered", "message": truncate(msg, 200)}, "")
		}

		// Bound the model view (audit log keeps everything).
		tr.compact(summarize)
		prompt := fmt.Sprintf("%s\n\nTASK: %s\n\nTRANSCRIPT:\n%s\nRespond with the next action as a single JSON object.",
			sysPrompt, task.UserPrompt, tr.render())

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

		// Meter token spend and enforce the per-task budget (safety brake for
		// runaway autonomous loops).
		d.sched.AddTokens(task.TaskID, estimateTokens(prompt)+estimateTokens(raw))
		if d.maxTaskTokens > 0 {
			if t, ok := d.sched.Get(task.TaskID); ok && t.TokensUsed > d.maxTaskTokens {
				d.degrade(sess, task, tr, fmt.Sprintf("token budget exceeded (%d > %d tokens)", t.TokensUsed, d.maxTaskTokens))
				return
			}
		}

		if act.Tool == "done" {
			// Goal verification: if the task carries objective success
			// criteria, check them before accepting "done" (Codex-style
			// verifiable completion vs pure model self-judgment).
			if len(task.SuccessCriteria) > 0 {
				if failed := d.checkSuccessCriteria(sess, task); len(failed) > 0 {
					verifyAttempts++
					d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
						map[string]any{"status": "goal_check_failed", "failed": failed}, "")
					if verifyAttempts > maxVerifyAttempts {
						d.degrade(sess, task, tr, "success criteria still failing after retries")
						return
					}
					tr.addTurn(Turn{Tool: "system", ActionBrief: "goal-check",
						Obs: Observation{Pinned: true, Content: "NOT done yet — these success criteria failed:\n" +
							strings.Join(failed, "\n") + "\nKeep working, then call done again."}})
					continue
				}
			}
			if len(task.OutputSchema) > 0 {
				if missing := validateOutput(act.Summary, task.OutputSchema); len(missing) > 0 {
					verifyAttempts++
					if verifyAttempts > maxVerifyAttempts {
						d.degrade(sess, task, tr, "final output never matched the required schema")
						return
					}
					tr.addTurn(Turn{Tool: "system", ActionBrief: "output-schema", Obs: Observation{Pinned: true,
						Content: "Your 'done' summary must be a JSON object containing keys: " + strings.Join(task.OutputSchema, ", ") +
							" (missing/invalid: " + strings.Join(missing, ", ") + "). Re-emit done with a valid JSON summary."}})
					continue
				}
			}
			// Independent verifier: a separate judge (fresh context) rules on the
			// done-claim before we trust it. Default-lenient (nil verifier => pass).
			if ok, reason := d.verifyDone(ctx, sess, task, act.Summary); !ok {
				verifyAttempts++
				d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
					map[string]any{"status": "verify_rejected", "reason": truncate(reason, 300)}, "")
				if verifyAttempts > maxVerifyAttempts {
					d.degrade(sess, task, tr, "independent verifier kept rejecting the done-claim: "+reason)
					return
				}
				tr.addTurn(Turn{Tool: "system", ActionBrief: "verify-rejected", Obs: Observation{Pinned: true,
					Content: "An independent verifier rejected your 'done': " + reason + "\nKeep working, then call done again."}})
				continue
			}
			d.finish(sess, task, act.Summary)
			return
		}

		// Intra-turn parallel batch: run several read-only tools concurrently and
		// fold their observations back as one turn. Writes are rejected so no
		// write races are possible.
		if len(act.Actions) > 0 {
			if bad := nonReadOnlyTools(act.Actions); len(bad) > 0 {
				tr.addTurn(Turn{Tool: "system", ActionBrief: "batch-rejected", Obs: Observation{Pinned: true,
					Content: "Parallel batches are read-only (list/read/search); these are not: " + strings.Join(bad, ", ") +
						". Run writes (patch/run) one action per turn."}})
				guard.tick()
				d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: turn, Transcript: tr})
				continue
			}
			if guard.repeated("batch", briefBatch(act.Actions)) {
				tr.addTurn(Turn{Tool: "batch", ActionBrief: briefBatch(act.Actions),
					Obs: Observation{Content: "You repeated this batch with no new result. Change approach, or use done."}})
				continue
			}
			obs := d.executeBatch(sess, task, act.Actions)
			guard.tick() // reads make no edit
			tr.addTurn(Turn{Thought: act.Thought, Tool: "batch",
				ActionBrief: briefBatch(act.Actions), Obs: Observation{Content: obs}})
			d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: turn, Transcript: tr})
			continue
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
		// Checkpoint after each completed turn so a crash can resume here.
		d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: turn, Transcript: tr})
	}

	d.degrade(sess, task, tr, "reached max turns without done")
}

// checkSuccessCriteria runs each objective criterion through the kernel +
// toolchain, returning the failures (empty = all pass). This is the "goal
// verifier" that turns model-judged done into machine-checked done.
func (d *Daemon) checkSuccessCriteria(sess *sessionstore.Session, task *scheduler.Task) []string {
	var failed []string
	for _, c := range task.SuccessCriteria {
		switch c.Kind {
		case "command_zero_exit":
			d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
				map[string]any{"status": "goal_check", "command": c.Command}, "")
			obs := d.agentRun(sess, task, strings.Fields(c.Command))
			if !strings.Contains(obs, "exit=0") {
				failed = append(failed, fmt.Sprintf("`%s` did not exit 0: %s", c.Command, truncate(obs, 200)))
			}
		case "file_exists":
			if _, err := os.Stat(resolveIn(sess.WorkspaceRoot, c.Path)); err != nil {
				failed = append(failed, "file missing: "+c.Path)
			}
		case "grep_absent":
			if matches, err := d.tools.Grep(c.Pattern, sess.WorkspaceRoot); err == nil && len(matches) > 0 {
				failed = append(failed, fmt.Sprintf("pattern still present (%d matches): %s", len(matches), c.Pattern))
			}
		default:
			// unknown check kinds are ignored (forward-compatible)
		}
	}
	return failed
}

// finish marks a task completed with the model's summary and persists the run
// record (summary + applied patches) so it stays queryable after restart.
func (d *Daemon) finish(sess *sessionstore.Session, task *scheduler.Task, summary string) {
	d.sched.SetStatus(task.TaskID, "completed")
	d.sched.SetResult(task.TaskID, summary, d.appliedPatchIDs(sess))
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
		map[string]any{"status": "completed", "summary": summary}, "")
	d.persistRun(task.TaskID)
	d.runs.deleteCheckpoint(task.TaskID)
	d.emitCompletion(sess.SessionID, task)
}

// appliedPatchIDs returns the ids of patches that landed (applied/committed) in
// a session — the rollbackable footprint of a run.
func (d *Daemon) appliedPatchIDs(sess *sessionstore.Session) []string {
	patches, _ := d.kern.PatchList(sess.SessionID)
	applied := make([]string, 0, len(patches))
	for _, p := range patches {
		if p.Status == "applied" || p.Status == "committed" {
			applied = append(applied, p.PatchID)
		}
	}
	return applied
}

// degrade ends a task that couldn't reach done, but does so gracefully:
// it reports partial progress (applied patches survive and are rollbackable)
// rather than a bare failure (the SWE-agent "autosubmit" idea).
func (d *Daemon) degrade(sess *sessionstore.Session, task *scheduler.Task, tr *Transcript, reason string) {
	applied := d.appliedPatchIDs(sess)
	d.sched.SetStatus(task.TaskID, "degraded")
	d.sched.SetResult(task.TaskID, reason, applied)
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
		"status": "degraded", "reason": reason,
		"turns": len(tr.Turns), "applied_patches": applied,
	}, "")
	d.persistRun(task.TaskID)
	d.runs.deleteCheckpoint(task.TaskID)
	d.emitCompletion(sess.SessionID, task)
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

// isReadOnlyTool reports whether a tool has no side effects (safe to batch).
func isReadOnlyTool(tool string) bool {
	switch tool {
	case "list", "read", "search":
		return true
	}
	return false
}

// nonReadOnlyTools returns the offending tool names in a batch (empty = all safe).
func nonReadOnlyTools(acts []action) []string {
	var bad []string
	for _, a := range acts {
		if !isReadOnlyTool(a.Tool) {
			bad = append(bad, a.Tool)
		}
	}
	return bad
}

// briefBatch renders a batch for the transcript, e.g. parallel[read a | search x].
func briefBatch(acts []action) string {
	parts := make([]string, len(acts))
	for i := range acts {
		parts[i] = briefAction(&acts[i])
	}
	return "parallel[" + strings.Join(parts, " | ") + "]"
}

// executeBatch runs a batch of read-only actions concurrently (one goroutine
// each, through the same kernel-gated executeAction as a single action) and
// joins the observations in emit order. Safe because every sub-action is
// side-effect-free and the kernel client serializes requests.
func (d *Daemon) executeBatch(sess *sessionstore.Session, task *scheduler.Task, acts []action) string {
	results := make([]string, len(acts))
	var wg sync.WaitGroup
	for i := range acts {
		wg.Add(1)
		go func(i int, sub action) {
			defer wg.Done()
			results[i] = d.executeAction(sess, task, &sub)
		}(i, acts[i])
	}
	wg.Wait()
	var b strings.Builder
	for i := range acts {
		fmt.Fprintf(&b, "=== [%d] %s ===\n%s\n", i, briefAction(&acts[i]), results[i])
	}
	return strings.TrimSpace(b.String())
}

// executeAction runs a tool action wrapped by lifecycle hooks: a PreToolUse
// hook that exits 2 blocks the action (its stderr is the feedback); PostToolUse
// hooks observe the result. The kernel+toolchain dispatch is dispatchAction.
func (d *Daemon) executeAction(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	if blocked, reason := d.runPreToolHooks(sess.WorkspaceRoot, act.Tool, hookPayload(act, "")); blocked {
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
			map[string]any{"status": "hook_blocked", "tool": act.Tool, "reason": reason}, "")
		return "BLOCKED by hook: " + reason
	}
	if d.isPlanMode(sess.SessionID) && (act.Tool == "patch" || act.Tool == "run") {
		return "BLOCKED: plan mode active — explore read-only and present a plan; the operator must approve it (session.approve_plan) before edits/commands"
	}
	obs := d.dispatchAction(sess, task, act)
	d.runPostToolHooks(sess.WorkspaceRoot, act.Tool, hookPayload(act, obs))
	return obs
}

// dispatchAction runs one tool action through the kernel + toolchain and
// returns the observation to feed back to the reasoner.
func (d *Daemon) dispatchAction(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
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
		d.recordRead(sess.SessionID, act.Path, string(content))
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

	case "spawn":
		return d.executeSpawn(sess, task, act)

	case "workflow":
		return d.executeWorkflow(sess, task, act)

	case "mcp":
		return d.callMCP(sess, task, act)

	default:
		return "unknown tool: " + act.Tool
	}
}

// agentPatch proposes and applies a full-file edit through the kernel's
// transactional patch engine (writes land via Zig carina-patch-native).
func (d *Daemon) agentPatch(sess *sessionstore.Session, task *scheduler.Task, path, content string) string {
	if path == "" {
		return "error: patch needs a path"
	}
	// Read-before-write: refuse to clobber a file the agent never read, or one
	// that drifted since it read it (dirty write).
	if err := d.checkWriteProvenance(sess.SessionID, path, resolveIn(sess.WorkspaceRoot, path)); err != nil {
		return "DENIED: " + err.Error()
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
	// The agent's edit is now the on-disk truth; record it so a follow-up edit
	// in the same run isn't flagged as a blind overwrite.
	d.recordRead(sess.SessionID, path, content)
	result := fmt.Sprintf("patch %s applied to %s (status=%s, rollbackable)", applied.PatchID, path, applied.Status)
	// Post-edit diagnostics: surface compile/parse errors this edit introduced,
	// so the agent can self-correct on the next turn instead of turns later.
	if diag := checkEdited(resolveIn(sess.WorkspaceRoot, path)); diag != "" {
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
			map[string]any{"status": "post_edit_diagnostics", "path": path, "diagnostics": truncate(diag, 500)}, "")
		result += "\n[diagnostics] this edit introduced errors:\n" + truncate(diag, 1000)
	}
	// Semantic (LSP) diagnostics augment the syntax probe when a language server
	// is installed — type errors and undefined symbols a parse check can't see.
	if sem := d.semanticDiagnostics(resolveIn(sess.WorkspaceRoot, path), sess.WorkspaceRoot); sem != "" {
		result += "\n[semantic] this edit has type errors:\n" + truncate(sem, 1000)
	}
	return result
}

// agentRun executes a command the agent proposed: kernel decision first
// (destructive => denied; risky => auto-approved in autonomous mode), then
// Zig carina-run. Every step is audited.
func (d *Daemon) agentRun(sess *sessionstore.Session, task *scheduler.Task, argv []string) string {
	if d.requireTrust && !d.trust.isTrusted(sess.WorkspaceRoot) {
		return "DENIED: workspace not trusted — approve it first (workspace.trust)"
	}
	command := strings.Join(argv, " ")
	dec, err := d.kern.Request(sess.SessionID, "CommandExec", command, task.TaskID)
	if err != nil {
		return "error: " + err.Error()
	}
	switch dec.Decision {
	case "denied":
		// A subagent may escalate a refused command to its parent's authority.
		if esc, ok := d.escalateToParent(sess, task, "CommandExec", command, command); ok {
			dec = esc
		} else {
			return "DENIED by policy: " + dec.Reason
		}
	case "requires_approval":
		approved, ok := d.resolveApprovalOrEscalate(sess, task, dec, "CommandExec", command, command)
		if !ok {
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

	result, err := d.tools.Run(argv, sess.WorkspaceRoot, 2*time.Minute, d.egressEnv(), d.sandbox)
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

// callMCP proxies a tool call to an external MCP server. Like every other
// effect it is gated by the capability kernel (PluginLoad) and audited, so MCP
// tools are subject to the same policy + approval as native tools; the result
// is redacted before it enters the transcript/log.
func (d *Daemon) callMCP(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	if act.MCPServer == "" || act.MCPTool == "" {
		return "error: mcp needs mcp_server and mcp_tool"
	}
	dec, err := d.kern.Request(sess.SessionID, "PluginLoad", "mcp:"+act.MCPServer+"/"+act.MCPTool, task.TaskID)
	if err != nil {
		return "error: " + err.Error()
	}
	mcpResource := "mcp:" + act.MCPServer + "/" + act.MCPTool
	switch dec.Decision {
	case "denied":
		if esc, ok := d.escalateToParent(sess, task, "PluginLoad", mcpResource, mcpResource); ok {
			dec = esc
		} else {
			return "DENIED by policy: " + dec.Reason
		}
	case "requires_approval":
		approved, ok := d.resolveApprovalOrEscalate(sess, task, dec, "PluginLoad", mcpResource, mcpResource)
		if !ok {
			return "requires approval (not granted): " + dec.Reason
		}
		dec = approved
	}
	d.record(sess.SessionID, "ToolApproved", task.TaskID, "go",
		map[string]any{"mcp_server": act.MCPServer, "mcp_tool": act.MCPTool}, dec.DecisionID)

	out, err := d.mcp.Call(act.MCPServer, act.MCPTool, act.Args)
	if err != nil {
		return "mcp error: " + err.Error()
	}
	if red, e := d.kern.Redact(sess.SessionID, out); e == nil {
		out = red
	}
	d.record(sess.SessionID, "ModelResponded", task.TaskID, "go",
		map[string]any{"mcp_server": act.MCPServer, "mcp_tool": act.MCPTool, "result": truncate(out, 300)}, "")
	return out
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
	// Batch form: {"actions":[...]} runs several read-only tools in parallel.
	// Validate structurally here; the read-only policy is enforced in runLoop.
	if len(a.Actions) > 0 {
		for i, sub := range a.Actions {
			if sub.Tool == "" {
				return action{}, fmt.Errorf("action %d in batch has no tool", i)
			}
			if len(sub.Actions) > 0 {
				return action{}, fmt.Errorf("nested batches not allowed")
			}
		}
		return a, nil
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

// estimateTokens approximates the token count of a string (~4 chars/token).
// claude-cli does not expose token counts cheaply on every call, so the budget
// governor meters with this estimate.
func estimateTokens(s string) int { return len(s)/4 + 1 }

// validateOutput returns the required keys missing from a done summary that is
// expected to be a JSON object (structured output). A summary that is not a
// JSON object counts every key as missing.
func validateOutput(summary string, keys []string) []string {
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(summary)), &obj) != nil {
		return keys
	}
	var missing []string
	for _, k := range keys {
		if _, ok := obj[k]; !ok {
			missing = append(missing, k)
		}
	}
	return missing
}

func registerProviders(router *modelrouter.Router, offline bool) {
	if !offline {
		router.RegisterProvider(NewAnthropicProviderFromEnv())
	}
	router.RegisterProvider(modelrouter.NewMockProvider())
	_ = time.Now
}
