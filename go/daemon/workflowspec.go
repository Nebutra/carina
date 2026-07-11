package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkflowStep is one node in a workflow DAG: it delegates to a named agent
// (an AgentSpec) with a task, and may depend on earlier steps. The task text
// may reference earlier step outputs as ${step_id} and the workflow input as
// ${input} — substituted at run time.
type WorkflowStep struct {
	ID    string   `json:"id"`
	Agent string   `json:"agent"`
	Task  string   `json:"task"`
	Needs []string `json:"needs,omitempty"`

	// FailFast only applies under ExecutionMode "streaming" (see WorkflowSpec).
	// A failing step normally isolates: only its transitive-only dependents are
	// skipped, independent branches keep running. FailFast=true restores the
	// old "one failure kills the whole run" behavior for a specific step that
	// really is on the critical path — the caller opts a step INTO fail-fast,
	// rather than the whole run defaulting to it.
	FailFast bool `json:"fail_fast,omitempty"`

	// Input, when non-empty, is resolved field-by-field (each value may be
	// "${step_id}" — the whole prior output — or "${step_id.a.b.c}" — a
	// dot-path into that output PARSED AS JSON, typed rather than
	// string-substituted) and appended to the interpolated Task as a labeled
	// JSON block the subagent is told to treat as structured parameters.
	// Additive on top of the existing ${step_id} whole-string interpolation
	// in Task, not a replacement for it — streaming and bsp both honor Task
	// interpolation; only streaming resolves Input (see runWorkflowStreaming).
	Input map[string]string `json:"input,omitempty"`

	// When, if set, is a small JSONLogic-compatible boolean expression (see
	// workflow_condition.go) evaluated once the step's dependencies resolve,
	// against a data context built from their JSON-parsed outputs (a
	// non-JSON output is exposed as {"raw": "<the string>"}). A falsy
	// result skips the step through the exact same isolate-propagation path
	// an upstream failure uses — conditional branching and failure isolation
	// are the same mechanism, not two different ones. Streaming-mode only.
	When json.RawMessage `json:"when,omitempty"`

	// Kind selects step behavior: "" (default) is a normal agent step;
	// "generator" additionally parses the step's "done" summary for a
	// spawn_steps envelope (see workflow_generator.go) and injects the
	// declared new nodes into the still-running graph. Streaming-mode only —
	// the batch scheduler's per-level barrier has no natural "graph changed
	// mid-run" hook without restructuring its loop, and dynamic graphs are
	// exactly the kind of large/irregular shape streaming mode exists for.
	Kind string `json:"kind,omitempty"`

	// ConsumesChannel names swarm channels (see swarm_channel.go) this step
	// subscribes to for live, mid-run messages from other steps — distinct
	// from Needs/Input, which only ever hand off a dependency's TERMINAL
	// output. A subscribed step calls the "swarm_receive" tool during its own
	// run to pull anything published since it last checked; publishing (via
	// "swarm_publish") requires no subscription and is open to every step.
	// Streaming-mode only, same rationale as Input/When/Kind above.
	ConsumesChannel []string `json:"consumes_channel,omitempty"`

	// Remote, when true, routes this step through the existing cross-process
	// dispatch/lease/report pipeline (go/scheduler/dispatch.go +
	// go/daemon/dispatch.go's work.poll/work.renew/work.report RPCs —
	// apps/carina-worker already implements a real external worker process
	// against that exact surface) instead of spawning an in-process
	// subagent — this is what actually lets a step run on a different
	// machine (Agent Swarm design §7). Gated by Capability::RemoteDispatch,
	// a strictly stronger trust decision than the same-process
	// SubagentSpawn gate every other step uses. Streaming-mode only.
	Remote bool `json:"remote,omitempty"`

	// Affinity is an optional scheduling hint for cluster mode (Agent Swarm
	// design §4.1's "affinity": {"worker_pool": "..."}). A non-empty
	// Affinity also implies Remote — a step that cares which worker pool it
	// lands on is, by definition, asking to be dispatched to a worker rather
	// than run in this daemon's own process. The "worker_pool" key becomes a
	// required worker capability ("worker_pool:<name>") that
	// scheduler.LeaseMatching only offers to a worker whose registered
	// Capabilities include it, so an affinity-tagged step can never be
	// silently picked up by an unrelated pool.
	Affinity map[string]string `json:"affinity,omitempty"`
}

// WorkflowSpec is a declarative multi-step agent pipeline. It is the Carina
// analogue of the Workflow-orchestration mechanism: deterministic control flow
// (a dependency DAG) fanned out over isolated, capability-attenuated subagents,
// with every step audited and resumable.
type WorkflowSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Steps       []WorkflowStep `json:"steps"`
	Source      string         `json:"-"` // "user" | "project"

	// ExecutionMode selects the scheduler: "" or "bsp" (default, unchanged
	// since this field was added) runs runWorkflow — batch-by-dependency-level,
	// a step only starts once every step in the previous level has finished,
	// and any single step failure aborts the whole run. "streaming" runs
	// runWorkflowStreaming — a step starts the instant its own dependencies
	// resolve, independent of how long sibling steps in the same "level" take,
	// with a higher step-count ceiling and per-step opt-in FailFast (default:
	// isolate a failure to its own dependents, keep unrelated branches going).
	// See docs/plans/2026-07-12-agent-swarm-dag-orchestration-design.md §5 for
	// why these are two execution semantics over one shared graph schema
	// rather than two competing engines.
	ExecutionMode string `json:"execution_mode,omitempty"`
}

func (s *WorkflowSpec) streaming() bool { return s.ExecutionMode == "streaming" }

func parseWorkflowSpec(raw []byte) (*WorkflowSpec, error) {
	var s WorkflowSpec
	if err := decodeStrictJSON(raw, &s); err != nil {
		return nil, err
	}
	if s.Name == "" {
		return nil, fmt.Errorf("workflow spec needs a name")
	}
	if len(s.Steps) == 0 {
		return nil, fmt.Errorf("workflow %q has no steps", s.Name)
	}
	return &s, nil
}

// loadWorkflowSpecs discovers workflows from the user dir (~/.carina/workflows)
// and the project dir (<workspace>/.carina/workflows). Project workflows
// override user ones — mirroring how agent specs are discovered.
func loadWorkflowSpecs(workspaceRoot string) map[string]*WorkflowSpec {
	out := map[string]*WorkflowSpec{}
	if home, err := os.UserHomeDir(); err == nil {
		loadWorkflowsFromDir(filepath.Join(home, ".carina", "workflows"), "user", out)
	}
	if workspaceRoot != "" {
		loadWorkflowsFromDir(filepath.Join(workspaceRoot, ".carina", "workflows"), "project", out)
	}
	return out
}

func loadWorkflowsFromDir(dir, source string, out map[string]*WorkflowSpec) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if spec, err := parseWorkflowSpec(raw); err == nil {
			spec.Source = source
			out[spec.Name] = spec
		}
	}
}

// validate rejects malformed DAGs: empty/duplicate ids, dangling deps, and
// dependency cycles (caught by the topological sort).
func (s *WorkflowSpec) validate() error {
	switch s.ExecutionMode {
	case "", "bsp", "streaming":
	default:
		return fmt.Errorf("workflow %q has unknown execution_mode %q (want \"bsp\" or \"streaming\")", s.Name, s.ExecutionMode)
	}
	ids := map[string]bool{}
	for _, st := range s.Steps {
		if st.ID == "" {
			return fmt.Errorf("workflow %q has a step with an empty id", s.Name)
		}
		if ids[st.ID] {
			return fmt.Errorf("workflow %q has a duplicate step id %q", s.Name, st.ID)
		}
		if st.Agent == "" {
			return fmt.Errorf("workflow step %q has no agent", st.ID)
		}
		switch st.Kind {
		case "", "generator":
		default:
			return fmt.Errorf("workflow step %q has unknown kind %q (want \"\" or \"generator\")", st.ID, st.Kind)
		}
		ids[st.ID] = true
	}
	for _, st := range s.Steps {
		for _, n := range st.Needs {
			if !ids[n] {
				return fmt.Errorf("step %q needs unknown step %q", st.ID, n)
			}
			if n == st.ID {
				return fmt.Errorf("step %q depends on itself", st.ID)
			}
		}
	}
	if _, err := s.topoOrder(); err != nil {
		return err
	}
	return nil
}

// topoOrder returns a valid execution order via Kahn's algorithm, or an error
// if the graph has a cycle. (The engine does not use the order directly — it
// schedules by readiness — but this proves the DAG is acyclic.)
func (s *WorkflowSpec) topoOrder() ([]string, error) {
	indeg := map[string]int{}
	adj := map[string][]string{}
	for _, st := range s.Steps {
		indeg[st.ID] = 0
	}
	for _, st := range s.Steps {
		for _, n := range st.Needs {
			adj[n] = append(adj[n], st.ID)
			indeg[st.ID]++
		}
	}
	var q []string
	for _, st := range s.Steps {
		if indeg[st.ID] == 0 {
			q = append(q, st.ID)
		}
	}
	var order []string
	for len(q) > 0 {
		id := q[0]
		q = q[1:]
		order = append(order, id)
		for _, m := range adj[id] {
			indeg[m]--
			if indeg[m] == 0 {
				q = append(q, m)
			}
		}
	}
	if len(order) != len(s.Steps) {
		return nil, fmt.Errorf("workflow %q has a dependency cycle", s.Name)
	}
	return order, nil
}
