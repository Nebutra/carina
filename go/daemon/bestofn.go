package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	minBestOfNCandidates = 2
	maxBestOfNCandidates = 5
)

// bestOfNToolHelp documents the best_of_n tool. It is only appended to the
// system prompt when d.bestOfNEnabled is true (see runLoopContext in
// agent.go), so a model never sees the tool exists while it is off — extra
// belt-and-suspenders on top of the hard "feature_disabled" denial in
// executeBestOfNOutcome.
const bestOfNToolHelp = `EXPERIMENTAL — best-of-n patch generation (top-level only, opt-in):
- {"tool":"best_of_n","task":"description of the change","n":3}
Generates N independent candidate drafts in parallel, judges them against the
task, and applies only the winning patch. N-1 discarded candidates are never
proposed or applied. Requires operator approval and an explicit cost preview
before it launches (N parallel model calls cost roughly Nx a single patch).`

// bestOfNCandidate is one candidate drafter's parsed, unapplied proposal. It
// lives entirely in Go memory — it is never passed to kernel.patch.propose
// unless/until it is chosen as the winner (see judgeBestOfN /
// executeBestOfNOutcome). A candidate that fails to produce a valid envelope
// is kept (Valid=false) so the result event can report it, not silently
// dropped.
type bestOfNCandidate struct {
	Index      int
	Valid      bool
	InvalidWhy string
	Files      []kernel.FileChange
	Rationale  string
	RawSummary string
}

// candidateEnvelope is the strict JSON shape a candidate-drafter subagent
// must return in its "done" summary.
type candidateEnvelope struct {
	Files []struct {
		Path       string `json:"path"`
		NewContent string `json:"new_content"`
	} `json:"files"`
	Rationale string `json:"rationale"`
}

// parseCandidateEnvelope strictly parses a candidate-drafter's done summary.
// Malformed or missing envelopes are always reported as an error — never
// silently treated as "no changes" — so the orchestrator can record exactly
// why a candidate was excluded from judging.
func parseCandidateEnvelope(summary string) ([]kernel.FileChange, string, error) {
	raw := strings.TrimSpace(summary)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, "", fmt.Errorf("no JSON object in candidate summary")
	}
	var env candidateEnvelope
	if err := json.Unmarshal([]byte(raw[start:end+1]), &env); err != nil {
		return nil, "", fmt.Errorf("malformed candidate envelope: %w", err)
	}
	if len(env.Files) == 0 {
		return nil, "", fmt.Errorf("candidate envelope has no files")
	}
	files := make([]kernel.FileChange, 0, len(env.Files))
	for i, f := range env.Files {
		path := strings.TrimSpace(f.Path)
		if path == "" {
			return nil, "", fmt.Errorf("candidate envelope file[%d] missing path", i)
		}
		files = append(files, kernel.FileChange{Path: path, NewContent: f.NewContent})
	}
	return files, strings.TrimSpace(env.Rationale), nil
}

// executeBestOfN dispatches the best_of_n tool: N parallel candidate drafts
// of the same task, judged, with only the winner ever proposed+applied.
func (d *Daemon) executeBestOfN(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	return d.executeBestOfNOutcome(sess, task, act).display
}

func (d *Daemon) executeBestOfNOutcome(sess *sessionstore.Session, task *scheduler.Task, act *action) toolExecutionOutcome {
	// best_of_n runs at the top level only — a candidate-drafter subagent
	// cannot itself call best_of_n (no relaxation of runSubagentLoop's
	// no-respawn guard: this tool is simply unreachable from inside a
	// subagent, same as workflow/spawn-fanout already are).
	if sess.Depth > 0 {
		return toolDenied("DENIED: best_of_n runs at the top level only (not inside a subagent)", "depth_limit")
	}
	if !d.bestOfNEnabled.Load() {
		return toolDenied("DENIED: best_of_n is disabled (opt-in feature; not enabled for this daemon)", "feature_disabled")
	}
	if strings.TrimSpace(act.Task) == "" {
		return toolFailed("error: best_of_n needs a 'task' description", "invalid_input")
	}
	n := act.N
	if n == 0 {
		n = minBestOfNCandidates
	}
	if n < minBestOfNCandidates || n > maxBestOfNCandidates {
		return toolFailed(fmt.Sprintf("error: best_of_n 'n' must be between %d and %d", minBestOfNCandidates, maxBestOfNCandidates), "invalid_input")
	}

	// Starting a best-of-n run is itself a gated effect, same idiom as
	// spawn_subagent/run_workflow (PluginLoad resource string), rather than a
	// new Capability variant — this is conceptually "may this session delegate
	// work", which PluginLoad already governs.
	dec, err := d.kern.Request(sess.SessionID, "PluginLoad", "best_of_n", task.TaskID)
	if err != nil {
		return toolFailed("best_of_n governance error: "+err.Error(), "governance_error")
	}
	if dec.Decision == "denied" {
		return toolDenied("DENIED: this session may not run best_of_n", "policy_denied")
	}

	// Cost preview: N parallel candidate generations cost roughly Nx a single
	// generation, plus a judge call. Surface that estimate in the approval
	// label/audit payload so an operator approving in interactive mode sees
	// the multiplier BEFORE any candidate is launched, and refuse to proceed
	// silently over the configured per-task ceiling in autonomous mode.
	estPerCandidate := estimateTokens(task.UserPrompt) * 4 // rough: prompt + tool-loop turns
	estTotal := estPerCandidate * n
	costLabel := fmt.Sprintf("best_of_n n=%d (~%d estimated tokens across candidates + judge)", n, estTotal)
	if mtt := d.maxTaskTokens.Load(); mtt > 0 && int64(estTotal) > mtt && !d.interactiveApproval.Load() {
		// Autonomous mode would otherwise silently blow past the operator's
		// configured per-task ceiling by ~Nx. Force a real approval pause
		// instead of proceeding, and fail closed if it isn't granted.
		dec.Decision = "requires_approval"
		if dec.Reason == "" {
			dec.Reason = "best_of_n estimated cost exceeds the per-task token ceiling; operator confirmation required"
		}
	}
	if dec.Decision == "requires_approval" {
		approved, ok := d.resolveApprovalOrEscalate(sess, task, dec, "PluginLoad", "best_of_n", costLabel)
		if !ok {
			return toolDenied("requires approval (not granted): "+dec.Reason, "approval_denied")
		}
		dec = approved
	}
	if err := d.ensureActiveToolStarted(task.TaskID); err != nil {
		return toolFailed("governance error: "+err.Error(), "audit_persistence_error")
	}

	runID := sessionstore.NewID("bestofn")
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
		"status": "best_of_n_started", "run_id": runID, "n": n, "estimated_tokens": estTotal,
	}, "")

	ctx := d.contextForTask(task.TaskID)

	// Parallel fan-out: literal reuse of the existing spawnSubagentContext
	// primitive (byte-identical pattern to workflow.go's runWorkflow and
	// subagent.go's parallel spawn branch) — not a new fan-out mechanism.
	candidates := make([]bestOfNCandidate, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			variantTask := fmt.Sprintf("%s\n\n(You are variant %d of %d independent candidate drafts for this task. Produce your own best attempt.)", act.Task, i+1, n)
			summary := d.spawnSubagentContext(ctx, sess, task, "candidate-drafter", variantTask)
			cand := bestOfNCandidate{Index: i, RawSummary: summary}
			files, rationale, perr := parseCandidateEnvelope(summary)
			if perr != nil {
				cand.Valid = false
				cand.InvalidWhy = perr.Error()
			} else {
				cand.Valid = true
				cand.Files = files
				cand.Rationale = rationale
			}
			candidates[i] = cand
		}(i)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return toolExecutionOutcome{display: "best_of_n cancelled", status: "cancelled", errorCategory: "operator_cancelled"}
	}

	winner, judgeRationale, jerr := d.judgeBestOfN(ctx, sess, task, act.Task, candidates)
	if jerr != nil {
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
			"status": "best_of_n_result", "run_id": runID, "n": n, "outcome": "no_winner", "error": jerr.Error(),
		}, "")
		return toolFailed("best_of_n: "+jerr.Error()+" — falling back: retry with a single normal patch call", "best_of_n_no_winner")
	}

	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
		"status": "best_of_n_result", "run_id": runID, "n": n, "outcome": "winner_selected",
		"winner_index": winner.Index, "judge_rationale": truncate(judgeRationale, 400),
	}, "")

	// Submission of the winner ONLY: seed read-provenance for the orchestrator
	// session (the winning content was authored by a candidate's own child
	// session, not sess, so checkWriteProvenance's normal "you must have read
	// it" guard is satisfied here by explicitly recording the current on-disk
	// content as read before the propose call — mirroring what agentPatchOutcome
	// already does after every successful apply).
	for _, f := range winner.Files {
		abs := resolveIn(sess.WorkspaceRoot, f.Path)
		if cur, err := os.ReadFile(abs); err == nil {
			d.recordRead(sess.SessionID, f.Path, string(cur))
		}
	}
	reason := fmt.Sprintf("best-of-n winner (n=%d, candidate %d, judge: %s)", n, winner.Index, truncate(judgeRationale, 200))
	outcome := d.proposeAndApplyPatch(sess, task, reason, winner.Files)
	if outcome.status != "completed" {
		return outcome
	}
	return toolCompleted(fmt.Sprintf("best_of_n (run %s): %d/%d candidates valid; winner=candidate %d.\n%s",
		runID, countValid(candidates), n, winner.Index, outcome.display))
}

func countValid(candidates []bestOfNCandidate) int {
	n := 0
	for _, c := range candidates {
		if c.Valid {
			n++
		}
	}
	return n
}
