package daemon

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	carinatelemetry "github.com/Nebutra/carina/go/telemetry"
	"github.com/Nebutra/carina/go/workflowui"
)

const maxWorkflowSteps = 64

// executeWorkflow dispatches the `workflow` tool: run a named declarative
// pipeline of agent steps. It is a top-level effect only — a subagent cannot
// launch a workflow (bounding nesting), and starting one is gated by the
// capability kernel just like spawning a subagent.
func (d *Daemon) executeWorkflow(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	if sess.Depth > 0 {
		return "DENIED: workflows run at the top level only (not inside a subagent)"
	}
	if act.Workflow == "" {
		return "error: workflow needs a 'workflow' name"
	}
	specs := loadWorkflowSpecs(sess.WorkspaceRoot)
	spec := specs[act.Workflow]
	if spec == nil {
		return fmt.Sprintf("unknown workflow %q (available: %s)", act.Workflow, strings.Join(workflowNames(specs), ", "))
	}
	// Starting a workflow is a gated effect (same capability as spawning).
	dec, err := d.kern.Request(sess.SessionID, "PluginLoad", "run_workflow", task.TaskID)
	if err == nil && dec.Decision == "denied" {
		return "DENIED: this session may not run workflows"
	}

	runID := sessionstore.NewID("wf")
	outputs, err := d.runWorkflow(sess, task, spec, act.Task, runID)
	if err != nil {
		return fmt.Sprintf("workflow %q error (run %s): %s", spec.Name, runID, err.Error())
	}

	var b strings.Builder
	fmt.Fprintf(&b, "workflow %q completed (%d steps, run %s):\n", spec.Name, len(outputs), runID)
	for _, st := range spec.Steps {
		fmt.Fprintf(&b, "=== %s (%s) ===\n%s\n", st.ID, st.Agent, truncate(outputs[st.ID], 400))
	}
	return b.String()
}

// runWorkflow executes a workflow DAG: a step runs as soon as all its
// dependencies have completed, so independent steps run in parallel. Each step
// delegates to an isolated, capability-attenuated subagent (child ⊆ parent),
// its output is threaded into dependents via ${step_id} interpolation, and
// every completed step is persisted so a crashed/paused run can resume without
// re-doing finished work. The parent session's audit log records the whole run.
func (d *Daemon) runWorkflow(parent *sessionstore.Session, parentTask *scheduler.Task, spec *WorkflowSpec, input, runID string) (map[string]string, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	if len(spec.Steps) > maxWorkflowSteps {
		return nil, fmt.Errorf("workflow %q exceeds the %d-step limit", spec.Name, maxWorkflowSteps)
	}

	store := newWFRunStore(d.stateDir)
	persisted, err := store.load(runID)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	outputs := map[string]string{}
	done := map[string]bool{}
	for id, r := range persisted { // resume: adopt already-completed steps
		if r.Status == "completed" {
			outputs[id] = r.Output
			done[id] = true
		}
	}

	d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
		"status": "workflow_started", "workflow": spec.Name, "run_id": runID,
		"steps": len(spec.Steps), "resumed": len(done),
	}, "")

	remaining := len(spec.Steps) - len(done)
	for remaining > 0 {
		if d.workflowRuns != nil {
			for {
				detail, err := d.workflowRuns.Detail(runID)
				if err != nil {
					break
				} // legacy tool-initiated workflow run
				if detail.Run.Status == workflowui.Stopped {
					return outputs, fmt.Errorf("workflow stopped by operator")
				}
				if detail.Run.Status == workflowui.Paused {
					select {
					case <-d.stopCh:
						return outputs, fmt.Errorf("daemon stopping")
					case <-time.After(100 * time.Millisecond):
						continue
					}
				}
				break
			}
		}
		// Collect every step whose dependencies are all satisfied.
		mu.Lock()
		var ready []WorkflowStep
		for _, st := range spec.Steps {
			if done[st.ID] {
				continue
			}
			runnable := true
			for _, n := range st.Needs {
				if !done[n] {
					runnable = false
					break
				}
			}
			if runnable {
				ready = append(ready, st)
			}
		}
		mu.Unlock()
		if len(ready) == 0 {
			return outputs, fmt.Errorf("workflow deadlock: %d steps remain with unmet dependencies", remaining)
		}

		// Run the ready batch concurrently; each subagent is isolated and the
		// kernel serializes their capability requests.
		var wg sync.WaitGroup
		errs := make([]error, len(ready))
		for i, st := range ready {
			wg.Add(1)
			go func(i int, st WorkflowStep) {
				defer wg.Done()
				startedAt := time.Now().UTC()
				if d.workflowRuns != nil {
					now := startedAt
					if _, managedErr := d.workflowRuns.Detail(runID); managedErr == nil {
						if _, err := d.workflowRuns.UpdateStep(runID, workflowui.Step{ID: st.ID, Status: workflowui.Running, StartedAt: &now}); err != nil {
							errs[i] = fmt.Errorf("step %q refused before execution because running state could not persist: %w", st.ID, err)
							return
						}
					}
				}
				mu.Lock()
				taskText := interpolate(st.Task, input, outputs)
				mu.Unlock()

				d.record(parent.SessionID, "ToolApproved", parentTask.TaskID, "go", map[string]any{
					"workflow": spec.Name, "run_id": runID, "step": st.ID, "agent": st.Agent,
				}, "")

				summary := d.spawnSubagent(parent, parentTask, st.Agent, taskText)
				if spawnFailed(summary) {
					errs[i] = fmt.Errorf("step %q: %s", st.ID, summary)
					_ = d.telemetry.Span("carina.workflow.step", runID, st.ID, carinatelemetry.Attribution{
						WorkspaceID: parent.WorkspaceID, SessionID: parent.SessionID, WorkflowID: runID,
						StepID: st.ID, TaskID: parentTask.TaskID,
					}, carinatelemetry.Cost{}, time.Since(startedAt), "failed")
					return
				}

				mu.Lock()
				outputs[st.ID] = summary
				done[st.ID] = true
				persisted[st.ID] = stepResult{Status: "completed", Output: summary}
				snapshot := make(map[string]stepResult, len(persisted))
				for k, v := range persisted {
					snapshot[k] = v
				}
				mu.Unlock()
				if err := store.save(runID, snapshot); err != nil {
					errs[i] = fmt.Errorf("step %q side effect completed but result persistence failed; manual reconciliation required: %w", st.ID, err)
					if d.workflowRuns != nil {
						_, _ = d.workflowRuns.MarkInterrupted(runID, errs[i].Error())
					}
					return
				}
				if d.workflowRuns != nil {
					now := time.Now().UTC()
					_, managedErr := d.workflowRuns.Detail(runID)
					if _, err := d.workflowRuns.UpdateStep(runID, workflowui.Step{ID: st.ID, Status: workflowui.Completed, FinishedAt: &now, Output: summary}); managedErr == nil && err != nil {
						errs[i] = fmt.Errorf("step %q completed but operator state persistence failed: %w", st.ID, err)
						return
					}
				}
				_ = d.telemetry.Span("carina.workflow.step", runID, st.ID, carinatelemetry.Attribution{
					WorkspaceID: parent.WorkspaceID, SessionID: parent.SessionID, WorkflowID: runID,
					StepID: st.ID, TaskID: parentTask.TaskID,
				}, carinatelemetry.Cost{}, time.Since(startedAt), "completed")
			}(i, st)
		}
		wg.Wait()

		for _, e := range errs {
			if e != nil {
				d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
					"status": "workflow_failed", "workflow": spec.Name, "run_id": runID, "error": e.Error(),
				}, "")
				return outputs, e
			}
		}
		remaining -= len(ready)
	}

	d.record(parent.SessionID, "ModelResponded", parentTask.TaskID, "go", map[string]any{
		"status": "workflow_completed", "workflow": spec.Name, "run_id": runID, "steps": len(outputs),
	}, "")
	return outputs, nil
}

// interpolate substitutes ${input} and ${step_id} tokens in a step task with
// the workflow input and prior step outputs.
func interpolate(s, input string, outputs map[string]string) string {
	s = strings.ReplaceAll(s, "${input}", input)
	for id, out := range outputs {
		s = strings.ReplaceAll(s, "${"+id+"}", out)
	}
	return s
}

// spawnFailed reports whether a subagent summary is actually a spawn/setup
// error (fatal to the step) rather than a real result. A subagent that merely
// hit its turn limit returns a degraded summary, which is NOT fatal — it flows
// downstream like any other output.
func spawnFailed(summary string) bool {
	for _, p := range []string{"unknown agent", "spawn failed", "spawn init failed", "error: spawn"} {
		if strings.HasPrefix(summary, p) {
			return true
		}
	}
	return false
}

func workflowNames(specs map[string]*WorkflowSpec) []string {
	out := make([]string, 0, len(specs))
	for n := range specs {
		out = append(out, n)
	}
	return out
}
