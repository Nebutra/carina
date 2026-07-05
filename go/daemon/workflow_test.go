package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// taskEchoReasoner finishes immediately, echoing the step's TASK line into the
// done summary. Because its reply depends only on the prompt (not call order),
// parallel workflow steps are deterministic to assert on. Stateless => safe for
// concurrent Think calls.
type taskEchoReasoner struct{}

func (taskEchoReasoner) Name() string { return "task-echo" }
func (taskEchoReasoner) Think(_ context.Context, prompt string) (string, error) {
	task := ""
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "TASK: ") {
			task = strings.TrimPrefix(line, "TASK: ")
			break
		}
	}
	b, _ := json.Marshal(map[string]string{"tool": "done", "summary": "did[" + task + "]"})
	return string(b), nil
}

func writeWorkerAgent(t *testing.T, ws string) {
	t.Helper()
	dir := filepath.Join(ws, ".carina", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := "---\nname: worker\ndescription: run one step\nprofile: read-only\nmax_turns: 2\n---\nYou are a worker. Do the step, then finish with done.\n"
	if err := os.WriteFile(filepath.Join(dir, "worker.md"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseWorkflowSpec: JSON round-trips into a spec with steps + deps.
func TestParseWorkflowSpec(t *testing.T) {
	raw := []byte(`{"name":"rev","description":"d","steps":[
		{"id":"find","agent":"scout","task":"list files"},
		{"id":"review","agent":"reviewer","task":"review ${find}","needs":["find"]}]}`)
	spec, err := parseWorkflowSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "rev" || len(spec.Steps) != 2 {
		t.Fatalf("bad spec: %+v", spec)
	}
	if spec.Steps[1].Needs[0] != "find" {
		t.Fatalf("deps not parsed: %+v", spec.Steps[1])
	}
	if err := spec.validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

// TestWorkflowCycleDetected: a cyclic DAG is rejected by validation (no daemon
// needed — pure graph check).
func TestWorkflowCycleDetected(t *testing.T) {
	spec := &WorkflowSpec{Name: "loop", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "x", Needs: []string{"b"}},
		{ID: "b", Agent: "worker", Task: "y", Needs: []string{"a"}},
	}}
	if err := spec.validate(); err == nil {
		t.Fatal("cycle should be rejected")
	}
	// Dangling dependency is also rejected.
	bad := &WorkflowSpec{Name: "dangle", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "x", Needs: []string{"ghost"}},
	}}
	if err := bad.validate(); err == nil {
		t.Fatal("dangling dep should be rejected")
	}
}

// TestWorkflowDAGParallelAndInterpolation: a diamond DAG (a → b,c → d) runs
// dependencies before dependents (b,c after a; d after both), independent steps
// in parallel, and threads each step's output into its dependents via
// ${step_id}. We assert containment, which holds regardless of the parallel
// b/c ordering.
func TestWorkflowDAGParallelAndInterpolation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "diamond", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "stepA"},
		{ID: "b", Agent: "worker", Task: "B<${a}>", Needs: []string{"a"}},
		{ID: "c", Agent: "worker", Task: "C<${a}>", Needs: []string{"a"}},
		{ID: "d", Agent: "worker", Task: "D<${b}|${c}>", Needs: []string{"b", "c"}},
	}}

	out, err := d.runWorkflow(parent, parentTask, spec, "", "run-diamond")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 step outputs, got %d: %v", len(out), out)
	}
	// b and c must contain a's output (a ran first, interpolated in).
	if !strings.Contains(out["b"], out["a"]) || !strings.Contains(out["c"], out["a"]) {
		t.Fatalf("deps not interpolated: a=%q b=%q c=%q", out["a"], out["b"], out["c"])
	}
	// d must contain both b's and c's outputs (ran last, after both).
	if !strings.Contains(out["d"], out["b"]) || !strings.Contains(out["d"], out["c"]) {
		t.Fatalf("d did not see b and c: b=%q c=%q d=%q", out["b"], out["c"], out["d"])
	}
}

// TestWorkflowResume: a run whose first step is already persisted skips it
// (does not re-invoke the agent) and threads the cached output downstream.
func TestWorkflowResume(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	const runID = "run-resume"
	// Pre-seed step "a" as completed with a marker only a cached load can yield
	// (the reasoner would instead produce "did[stepA]").
	newWFRunStore(d.stateDir).save(runID, map[string]stepResult{
		"a": {Status: "completed", Output: "CACHED_A"},
	})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "resume pipeline")

	spec := &WorkflowSpec{Name: "resumable", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "stepA"},
		{ID: "b", Agent: "worker", Task: "B<${a}>", Needs: []string{"a"}},
	}}
	out, err := d.runWorkflow(parent, parentTask, spec, "", runID)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if out["a"] != "CACHED_A" {
		t.Fatalf("completed step should be skipped and reused, got %q", out["a"])
	}
	if !strings.Contains(out["b"], "CACHED_A") {
		t.Fatalf("downstream should use cached output, got %q", out["b"])
	}
}
