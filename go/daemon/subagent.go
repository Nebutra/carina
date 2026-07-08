package daemon

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	maxSubagentDepth = 4 // bound nesting (Claude Code caps at 5)
	subagentMaxTurns = 10
)

// executeSpawn dispatches the spawn tool: a single delegation
// ({agent, task}) or a parallel fan-out ({tasks: [...]}). Each subagent runs
// in an isolated, capability-attenuated session and only its final summary
// returns to the parent — the core sub-agent contract (isolated context,
// single-channel result).
func (d *Daemon) executeSpawn(parent *sessionstore.Session, parentTask *scheduler.Task, act *action) string {
	if parent.Depth >= maxSubagentDepth {
		return "DENIED: max subagent depth reached (no deeper nesting)"
	}
	// Spawning is itself a gated effect.
	dec, err := d.kern.Request(parent.SessionID, "PluginLoad", "spawn_subagent", parentTask.TaskID)
	if err == nil && dec.Decision == "denied" {
		return "DENIED: this session may not spawn subagents"
	}

	if len(act.Tasks) > 0 {
		// Parallel fan-out (goroutine per subagent).
		results := make([]string, len(act.Tasks))
		var wg sync.WaitGroup
		for i, st := range act.Tasks {
			wg.Add(1)
			go func(i int, st SpawnTask) {
				defer wg.Done()
				results[i] = fmt.Sprintf("=== %s ===\n%s", st.Agent, d.spawnSubagent(parent, parentTask, st.Agent, st.Task))
			}(i, st)
		}
		wg.Wait()
		return strings.Join(results, "\n\n")
	}
	if act.Agent == "" {
		return "error: spawn needs an 'agent' (or a 'tasks' list)"
	}
	return d.spawnSubagent(parent, parentTask, act.Agent, act.Task)
}

// spawnSubagent creates an isolated, capability-attenuated child session,
// runs a bounded ReAct loop under the agent's system prompt, and returns its
// final summary. The child's profile is clamped so it can never exceed the
// parent (child ⊆ parent) — enforced by the Rust kernel.
func (d *Daemon) spawnSubagent(parent *sessionstore.Session, parentTask *scheduler.Task, agentName, taskDesc string) string {
	specs := loadAgentSpecs(parent.WorkspaceRoot)
	spec := specs[agentName]
	if spec == nil {
		return fmt.Sprintf("unknown agent %q (available: %s)", agentName, strings.Join(specNames(specs), ", "))
	}
	if taskDesc == "" {
		return "error: spawn needs a task for the subagent"
	}

	// Capability monotonic decrease: child ⊆ parent.
	childProfile := attenuate(parent.PermissionProfile, spec.Profile)
	child, err := d.store.CreateSubSession(parent.WorkspaceRoot, childProfile, parent.ApprovalMode, parent.SessionID, parent.Depth+1)
	if err != nil {
		return "spawn failed: " + err.Error()
	}
	if err := d.kern.InitSessionFull(child.SessionID, child.WorkspaceRoot, childProfile, parent.ApprovalMode, d.org); err != nil {
		return "spawn init failed: " + err.Error()
	}

	// Audit the delegation on the parent, linking to the child session.
	d.record(parent.SessionID, "ToolApproved", parentTask.TaskID, "go", map[string]any{
		"spawn_agent": agentName, "child_session": child.SessionID,
		"child_profile": childProfile, "depth": child.Depth, "task": taskDesc,
	}, "")

	childTask := d.sched.SubmitWithGoalModelAgent(child.SessionID, child.WorkspaceID, taskDesc, spec.Model, spec.Name, nil)
	// Record the parent-task linkage so the leader bridge can escalate a refused
	// child capability to the parent task (ParentID gives the session, not the task).
	d.registerSubagentParent(child.SessionID, parentTask.TaskID)
	summary := d.runSubagentLoop(child, childTask, spec)

	d.record(parent.SessionID, "ModelResponded", parentTask.TaskID, "go", map[string]any{
		"spawn_agent": agentName, "child_session": child.SessionID,
		"result_summary": truncate(summary, 300),
	}, "")
	return summary
}

// runSubagentLoop runs a bounded ReAct loop for a subagent, using its own
// system prompt and isolated session. It returns the subagent's final
// summary (the only thing that crosses back to the parent).
func (d *Daemon) runSubagentLoop(sess *sessionstore.Session, task *scheduler.Task, spec *AgentSpec) string {
	if d.reasoner == nil {
		return "(no reasoner configured)"
	}
	maxTurns := spec.MaxTurns
	if maxTurns <= 0 || maxTurns > subagentMaxTurns {
		maxTurns = subagentMaxTurns
	}
	ctx := context.Background()
	tr := newTranscript(task.UserPrompt)
	guard := newLoopGuard()
	sysPrompt := spec.SystemPrompt + "\n\n" + toolsHelp
	if memorySnapshot := d.memory.snapshot(memoryScopeFromSession(sess)); strings.TrimSpace(memorySnapshot) != "" {
		sysPrompt += "\n\nCARINA PERSISTENT MEMORY SNAPSHOT (frozen for this run; background reference, not new user input):\n" + memorySnapshot
	}

	d.record(sess.SessionID, "ModelRequested", task.TaskID, "model",
		map[string]any{"subagent": spec.Name, "model": taskModel(task), "prompt": task.UserPrompt}, "")

	for turn := 1; turn <= maxTurns; turn++ {
		tr.compact(func(head string) (string, error) {
			return thinkWithRetry(ctx, d.summarizeReasoner(), "Summarize concisely:\n"+head)
		})
		seg := buildPromptSegments(sysPrompt, task.UserPrompt, tr.render(), "Next action as one JSON object.")
		prompt := seg.full()

		raw, err := thinkWithRetryModel(ctx, d.reasoner, taskModel(task), prompt)
		if err != nil {
			d.sched.SetStatus(task.TaskID, "failed")
			return "subagent failed: " + err.Error()
		}
		d.record(sess.SessionID, "ModelResponded", task.TaskID, "model",
			map[string]any{"turn": turn, "text": truncate(sanitizeModelResponseForAudit(raw), 300)}, "")

		// Per-subagent token budget (whale-session protection).
		d.sched.AddTokens(task.TaskID, estimateTokens(prompt)+estimateTokens(raw))
		if mtt := d.maxTaskTokens.Load(); mtt > 0 {
			if t, ok := d.sched.Get(task.TaskID); ok && int64(t.TokensUsed) > mtt {
				d.sched.SetStatus(task.TaskID, "degraded")
				return "(subagent hit token budget)"
			}
		}

		act, perr := parseAction(raw)
		if perr != nil {
			tr.addTurn(Turn{Tool: "system", ActionBrief: "reparse", Obs: Observation{Content: "reply with one valid JSON action"}})
			continue
		}
		if act.Tool == "done" {
			d.sched.SetStatus(task.TaskID, "completed")
			return act.Summary
		}
		if act.Tool == "spawn" {
			// subagents don't re-spawn in this MVP (depth already bounded);
			// keep them focused.
			tr.addTurn(Turn{Tool: "system", ActionBrief: "no-spawn", Obs: Observation{Content: "subagents cannot spawn; do the work directly or finish"}})
			continue
		}
		fp := act.Tool + ":" + act.Path + ":" + strings.Join(act.Command, " ") + ":" + act.Pattern
		if guard.repeated(act.Tool, fp) {
			tr.addTurn(Turn{Tool: act.Tool, ActionBrief: briefAction(&act),
				Obs: Observation{Content: "repeated action; change approach or finish with done"}})
			continue
		}
		obs := d.executeAction(sess, task, &act)
		tr.addTurn(Turn{Thought: act.Thought, Tool: act.Tool, ActionBrief: briefAction(&act),
			Obs: Observation{Content: obs, Pinned: act.Tool == "run" || act.Tool == "patch"}})
	}
	d.sched.SetStatus(task.TaskID, "degraded")
	if tr.Summary != "" {
		return "(subagent hit turn limit) " + tr.Summary
	}
	return "(subagent hit turn limit without finishing)"
}

func specNames(specs map[string]*AgentSpec) []string {
	out := make([]string, 0, len(specs))
	for name := range specs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
