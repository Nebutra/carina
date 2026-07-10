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
	"github.com/Nebutra/carina/go/toolnorm"
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
- {"tool":"memory","target":"memory|user","action":"add|replace|remove|batch","content":"fact","old_text":"unique substring","operations":[...]}   update governed long-term memory
- {"tool":"code.search","query":"free text or identifier"}      ranked code search (BM25+exact)
- {"tool":"code.symbols","name":"SymbolName"}                   definitions + references
- {"tool":"code.map"}                                           compact ranked repo map
- {"tool":"code.def","name":"SymbolName"}                       precise definition (LSP when available)
- {"tool":"code.refs","name":"SymbolName"}                      precise references (LSP when available)
- {"tool":"code.impact","name":"SymbolName"}                    transitive dependents of a symbol (bounded impact analysis)
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
Writes (patch/run/memory) must stay one action per turn — never put them in a batch.`

// action is the decision emitted by the reasoner each turn. Fields are read
// from the top level (flat form the model naturally emits) or from a nested
// "action" object (see parseAction).
type action struct {
	Thought    string            `json:"thought"`
	Tool       string            `json:"tool"`
	Action     json.RawMessage   `json:"action,omitempty"`
	Path       string            `json:"path"`
	Pattern    string            `json:"pattern"`
	Command    []string          `json:"command"`
	Content    string            `json:"content"`
	Summary    string            `json:"summary"`
	Target     string            `json:"target"`
	OldText    string            `json:"old_text"`
	Operations []memoryOperation `json:"operations,omitempty"`
	// code intelligence tools (code.search / code.symbols)
	Query string `json:"query"`
	Name  string `json:"name"`
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
	if task.Agent == "plan" {
		d.setPlanMode(sess.SessionID, true)
	}

	if d.reasoner == nil {
		d.runMockTask(sess, task)
		return
	}

	d.record(sess.SessionID, "ModelRequested", task.TaskID, "go",
		map[string]any{"engine": d.reasoner.Name(), "model": taskModel(task), "agent": taskAgent(task), "prompt": task.UserPrompt}, "")
	d.runLoop(sess, task, newTranscript(task.UserPrompt), 1, d.memory.snapshot(memoryScopeFromSession(sess)))
}

// resumeTask continues a background run from a persisted transcript checkpoint
// after a daemon restart. Prior turns (and their side effects) are already in
// the transcript and the audit log, so only the NEXT action runs — completed
// work is never re-executed.
func (d *Daemon) resumeTask(sess *sessionstore.Session, task *scheduler.Task, cp *runCheckpoint) {
	d.sched.SetStatus(task.TaskID, "running")
	if task.Agent == "plan" {
		d.setPlanMode(sess.SessionID, true)
	}
	if d.reasoner == nil {
		d.degrade(sess, task, cp.Transcript, "no reasoner available to resume run")
		return
	}
	d.record(sess.SessionID, "ModelRequested", task.TaskID, "go",
		map[string]any{"engine": d.reasoner.Name(), "model": taskModel(task), "agent": taskAgent(task), "prompt": task.UserPrompt, "resumed_from_turn": cp.Turn}, "")
	d.runLoop(sess, task, cp.Transcript, cp.Turn+1, cp.MemorySnapshot)
}

// runLoop is the ReAct loop shared by fresh (runTask) and resumed (resumeTask)
// runs. It checkpoints the transcript after each turn, so a daemon crash loses
// at most one in-flight action.
func (d *Daemon) runLoop(sess *sessionstore.Session, task *scheduler.Task, tr *Transcript, startTurn int, memorySnapshot string) {
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

	// Persistent project/user instructions are prepended to the system prompt
	// so the agent follows repo-specific conventions.
	sysPrompt := systemPrompt
	if spec := loadAgentSpecs(sess.WorkspaceRoot)[taskAgent(task)]; spec != nil && strings.TrimSpace(spec.SystemPrompt) != "" {
		sysPrompt = strings.TrimSpace(spec.SystemPrompt) + "\n\n" + systemPrompt
	}
	if mem := loadMemory(sess.WorkspaceRoot); mem != "" {
		sysPrompt += "\n\nPROJECT INSTRUCTIONS (Nebutra/Carina — follow them):\n" + mem
	}
	if strings.TrimSpace(memorySnapshot) != "" {
		sysPrompt += "\n\nCARINA PERSISTENT MEMORY SNAPSHOT (frozen for this run; background reference, not new user input):\n" + memorySnapshot
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
		seg := buildPromptSegments(sysPrompt, task.UserPrompt, tr.render(),
			"Respond with the next action as a single JSON object.")
		prompt := seg.full() // StablePrefix is cacheable across turns; suffix is volatile

		// inner requery loop: malformed actions are re-asked without
		// consuming a real turn (up to maxRequeries).
		var act action
		var raw string
		ok := false
		for requery := 0; requery <= maxRequeries; requery++ {
			var err error
			raw, err = thinkWithRetryModel(ctx, d.reasoner, task.Model, prompt)
			if err != nil {
				d.degrade(sess, task, tr, "reasoner error: "+err.Error())
				return
			}
			d.record(sess.SessionID, "ModelResponded", task.TaskID, "model",
				map[string]any{"turn": turn, "text": sanitizeModelResponseForAudit(raw)}, "")
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
		if mtt := d.maxTaskTokens.Load(); mtt > 0 {
			if t, ok := d.sched.Get(task.TaskID); ok && int64(t.TokensUsed) > mtt {
				d.degrade(sess, task, tr, fmt.Sprintf("token budget exceeded (%d > %d tokens)", t.TokensUsed, mtt))
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
				d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: turn, Transcript: tr, MemorySnapshot: memorySnapshot})
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
			d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: turn, Transcript: tr, MemorySnapshot: memorySnapshot})
			continue
		}

		// Loop safety: catch repeated actions and no-progress stalls.
		fp := act.Tool + ":" + act.Path + ":" + strings.Join(act.Command, " ") + ":" + act.Pattern + ":" + act.Query + ":" + act.Name
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
		d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: turn, Transcript: tr, MemorySnapshot: memorySnapshot})
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
	case "code.search":
		return "code.search " + a.Query
	case "code.symbols":
		return "code.symbols " + a.Name
	case "code.def":
		return "code.def " + a.Name
	case "code.refs":
		return "code.refs " + a.Name
	case "code.impact":
		return "code.impact " + a.Name
	default:
		return a.Tool
	}
}

// isReadOnlyTool reports whether a tool has no side effects (safe to batch).
func isReadOnlyTool(tool string) bool {
	switch tool {
	case "list", "read", "search", "code.search", "code.symbols", "code.map", "code.def", "code.refs", "code.impact":
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
	if d.isPlanMode(sess.SessionID) && (act.Tool == "patch" || act.Tool == "run" || act.Tool == "memory") {
		return "BLOCKED: plan mode active — explore read-only and present a plan; the operator must approve it (session.approve_plan) before edits, commands, or memory writes"
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

	case "memory":
		return d.agentMemory(sess, task, act)

	case "code.search":
		return d.agentCodeSearch(sess, task, act)

	case "code.symbols":
		return d.agentCodeSymbols(sess, task, act)

	case "code.map":
		return d.agentCodeMap(sess, task, act)

	case "code.def":
		return d.agentCodeDef(sess, task, act)

	case "code.refs":
		return d.agentCodeRefs(sess, task, act)

	case "code.impact":
		return d.agentCodeImpact(sess, task, act)

	default:
		return "unknown tool: " + act.Tool
	}
}

func (d *Daemon) agentMemory(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	req := memoryWriteRequest{
		Action:     string(act.Action),
		Target:     act.Target,
		Content:    act.Content,
		OldText:    act.OldText,
		Operations: act.Operations,
	}
	req.Action = strings.Trim(req.Action, `"`)
	scope := memoryScopeFromSession(sess)
	summary, err := summarizeMemoryWrite(scope, req)
	if err != nil {
		return "memory error: " + err.Error()
	}
	dec, err := d.kern.Request(sess.SessionID, "MemoryWrite", summary.Resource, task.TaskID)
	if err != nil {
		return "memory error: " + err.Error()
	}
	switch dec.Decision {
	case "denied":
		return "DENIED by policy: " + dec.Reason
	case "requires_approval":
		approved, ok := d.resolveApproval(sess, task, dec, "memory "+summary.Action+" "+summary.Target)
		if !ok {
			return "requires approval (not granted): " + dec.Reason
		}
		dec = approved
	}
	result, err := d.applyMemoryWrite(sess, task.TaskID, req, dec, scope, summary)
	if err != nil {
		return "memory error: " + err.Error()
	}
	raw, _ := json.Marshal(result)
	return string(raw)
}

// agentPatch proposes and applies a full-file edit through the kernel's
// transactional patch engine (writes land via Zig carina-patch-native). The
// PatchApply capability decision goes through the same gate discipline as
// the workspace.patch.apply RPC surface (checkPatchGate): PatchApply always
// evaluates to requires_approval under any non-denying profile
// (crates/carina-policy evaluate()), so the agent's own write path pauses
// for an operator in interactive-approval mode exactly like a gated command
// (agentRun) — it never self-approves as approver="agent" behind the
// operator's back.
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
	// Gate the apply the same way workspace.patch.propose does: mint the
	// PatchApply decision now and remember it so a concurrent
	// workspace.patch.apply on the same patch_id sees the identical gate
	// state, instead of leaving this apply ungoverned.
	dec, err := d.registerPatchGate(sess.SessionID, patch.PatchID, task.TaskID)
	if err != nil {
		return "patch gate failed: " + err.Error()
	}
	approver := "agent"
	switch dec.Decision {
	case "denied":
		if esc, ok := d.escalateToParent(sess, task, "PatchApply", patch.PatchID, "patch "+path); ok {
			dec = esc
			approver = "operator"
		} else {
			return "DENIED by policy: " + dec.Reason
		}
	case "requires_approval":
		approved, ok := d.resolveApprovalOrEscalate(sess, task, dec, "PatchApply", patch.PatchID, "patch "+path)
		if !ok {
			return "requires approval (not granted): " + dec.Reason
		}
		dec = approved
		approver = "operator"
	}
	d.mu.Lock()
	if gate := d.patchGates[patch.PatchID]; gate != nil {
		gate.status = "allowed"
	}
	d.mu.Unlock()
	applied, err := d.kern.PatchApply(sess.SessionID, patch.PatchID, approver)
	if err != nil {
		return "patch apply failed (nothing written): " + err.Error()
	}
	// The agent's edit is now the on-disk truth; record it so a follow-up edit
	// in the same run isn't flagged as a blind overwrite.
	d.recordRead(sess.SessionID, path, content)
	// Keep the code index in step with the write (best-effort; an index error
	// never fails the patch).
	d.invalidateIndex(sess.SessionID, []string{path})
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

// agentRun executes a command the agent proposed: canonicalize -> validate
// -> decide (P1.2 of docs/plans/agent-cli-productization.md §3 Phase 1),
// then Zig carina-run. Canonicalize expands paths and peels no-op-for-policy
// wrappers (timeout, nice, env) to a fixed point so crates/carina-policy
// classifies the same rule regardless of phrasing; Validate runs
// side-effect-free syntactic checks (empty command, unresolvable binary,
// workspace-escaping path) ahead of the kernel decision so the model
// self-corrects on a typo without ever burning a human approval — no
// permission.request is published and nothing is audited for a rejection at
// this stage. Once past validation, the kernel decision (destructive =>
// denied; risky => auto-approved in autonomous mode) and every subsequent
// step is audited using the canonical form, so the audit chain always
// records the command actually authorized, not whatever raw string the
// model happened to emit.
func (d *Daemon) agentRun(sess *sessionstore.Session, task *scheduler.Task, argv []string) string {
	if d.requireTrust.Load() && !d.trust.isTrusted(sess.WorkspaceRoot) {
		return "DENIED: workspace not trusted — approve it first (workspace.trust)"
	}
	canon := toolnorm.Canonicalize(argv, sess.WorkspaceRoot)
	if ok, code, msg := canon.Validate(); !ok {
		return "error: [" + code + "] " + msg
	}
	command := canon.Command
	classifyAs := canon.WrapperStripped
	dec, err := d.kern.Request(sess.SessionID, "CommandExec", classifyAs, task.TaskID)
	if err != nil {
		return "error: " + err.Error()
	}
	switch dec.Decision {
	case "denied":
		// A subagent may escalate a refused command to its parent's authority.
		if esc, ok := d.escalateToParent(sess, task, "CommandExec", classifyAs, command); ok {
			dec = esc
		} else {
			return "DENIED by policy: " + dec.Reason
		}
	case "requires_approval":
		approved, ok := d.resolveApprovalOrEscalate(sess, task, dec, "CommandExec", classifyAs, command)
		if !ok {
			return "requires approval (not granted): " + dec.Reason
		}
		dec = approved
	}

	risk, _ := d.kern.ClassifyCommand(classifyAs)
	commandID := sessionstore.NewID("cmd")
	started := map[string]any{"command_id": commandID, "command": command, "cwd": sess.WorkspaceRoot, "risk_level": risk}
	if mutatesPackages(classifyAs) {
		started["package_mutation"] = true
	}
	d.record(sess.SessionID, "CommandStarted", task.TaskID, "zig", started, dec.DecisionID)

	result, err := d.tools.Run(canon.Argv, sess.WorkspaceRoot, 2*time.Minute, d.egressEnv(), d.sandbox.Load())
	// A mutating-capable command may have rewritten files the patch hooks
	// never see (git checkout, sed -i, codegen): drop the built-index flag so
	// the next code.* call re-syncs against current disk (conservative even
	// on a runner error — the command may have partially executed).
	if risk > 0 {
		d.indexBuilt.Delete(sess.SessionID)
	}
	if err != nil {
		d.record(sess.SessionID, "CommandExited", task.TaskID, "zig", map[string]any{"command_id": commandID, "exit_code": -1, "error": err.Error()}, "")
		return "command error: " + err.Error()
	}
	stdout := strings.Join(result.Stdout, "\n")
	if red, e := d.kern.Redact(sess.SessionID, stdout); e == nil {
		stdout = red
	}
	d.record(sess.SessionID, "CommandOutput", task.TaskID, "zig", map[string]any{"command_id": commandID, "stream": "stdout", "chunk": truncate(stdout, 400)}, "")
	d.record(sess.SessionID, "CommandExited", task.TaskID, "zig", map[string]any{"command_id": commandID, "exit_code": result.ExitCode, "duration_ms": result.DurationMs}, "")

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

	out, err := d.mcp.CallPublic(act.MCPServer, act.MCPTool, act.Args)
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

func sanitizeModelResponseForAudit(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return truncate(raw, 400)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &obj); err != nil {
		return truncate(raw, 400)
	}
	if !sanitizeMemoryActionMap(obj) {
		return truncate(raw, 400)
	}
	redacted, err := json.Marshal(obj)
	if err != nil {
		return "[memory action redacted]"
	}
	return truncate(string(redacted), 400)
}

func sanitizeMemoryActionMap(obj map[string]any) bool {
	redacted := false
	if tool, _ := obj["tool"].(string); tool == "memory" {
		redactMemoryActionFields(obj)
		redacted = true
	}
	if nested, ok := obj["action"].(map[string]any); ok {
		if sanitizeMemoryActionMap(nested) {
			redacted = true
		}
	}
	if actions, ok := obj["actions"].([]any); ok {
		for _, item := range actions {
			if m, ok := item.(map[string]any); ok && sanitizeMemoryActionMap(m) {
				redacted = true
			}
		}
	}
	return redacted
}

func redactMemoryActionFields(obj map[string]any) {
	if _, ok := obj["content"]; ok {
		obj["content"] = "[redacted]"
	}
	if _, ok := obj["old_text"]; ok {
		obj["old_text"] = "[redacted]"
	}
	if ops, ok := obj["operations"].([]any); ok {
		for _, item := range ops {
			if op, ok := item.(map[string]any); ok {
				if _, ok := op["content"]; ok {
					op["content"] = "[redacted]"
				}
				if _, ok := op["old_text"]; ok {
					op["old_text"] = "[redacted]"
				}
			}
		}
	}
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
		map[string]any{"prompt": task.UserPrompt, "model": taskModel(task)}, "")
	resp, err := d.router.Complete(context.Background(), modelrouter.Request{Model: taskModel(task), Prompt: task.UserPrompt})
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

func taskModel(task *scheduler.Task) string {
	if task != nil && strings.TrimSpace(task.Model) != "" {
		return strings.TrimSpace(task.Model)
	}
	return "default"
}

func taskAgent(task *scheduler.Task) string {
	if task != nil && strings.TrimSpace(task.Agent) != "" {
		return strings.TrimSpace(task.Agent)
	}
	return "build"
}

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
