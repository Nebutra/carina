package workflowui

import (
	"os"
	"testing"
)

func TestLifecycleProgressRestartAndSave(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Create(Run{ID: "r1", Workflow: "review", Steps: []Step{{ID: "scan"}, {ID: "verify"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != Queued {
		t.Fatal(r.Status)
	}
	if _, err = s.Transition("r1", Running); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateStep("r1", Step{ID: "scan", Status: Completed, InputTokens: 10, CostUSD: .1}); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Detail("r1")
	if d.Progress != .5 || d.InputTokens != 10 {
		t.Fatalf("%+v", d)
	}
	if _, err = s.Transition("r1", Paused); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Transition("r1", Running); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Transition("r1", Stopped); err != nil {
		t.Fatal(err)
	}
	rr, err := s.Restart("r1", "r2")
	if err != nil || rr.Attempt != 2 {
		t.Fatalf("%+v %v", rr, err)
	}
	if _, err = s.SaveCommand("r2", t.TempDir(), "review-saved"); err != nil {
		t.Fatal(err)
	}
}

func TestPersistFailureDoesNotAdvanceMemoryState(t *testing.T) {
	s, _ := New(t.TempDir())
	_, _ = s.Create(Run{ID: "r", Workflow: "w"})
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition("r", Running); err == nil {
		t.Fatal("expected persistence failure")
	}
	d, _ := s.Detail("r")
	if d.Run.Status != Queued {
		t.Fatalf("memory advanced to %s", d.Run.Status)
	}
}

func TestRejectsInvalidTransitions(t *testing.T) {
	s, _ := New(t.TempDir())
	_, _ = s.Create(Run{ID: "r", Workflow: "w"})
	if _, err := s.Transition("r", Completed); err == nil {
		t.Fatal("expected invalid transition")
	}
}

func TestRestartReconcileMarksLiveRunsResumable(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, _ = s.Create(Run{ID: "r", Workflow: "w", SessionID: "s", Steps: []Step{{ID: "done"}, {ID: "live"}}})
	_, _ = s.Transition("r", Running)
	_, _ = s.UpdateStep("r", Step{ID: "done", Status: Completed, Output: "kept"})
	now := s.now().UTC()
	_, _ = s.UpdateStep("r", Step{ID: "live", Status: Running, StartedAt: &now})
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := reopened.ReconcileStartup("restart")
	if err != nil || len(changed) != 1 {
		t.Fatalf("%+v %v", changed, err)
	}
	d, _ := reopened.Detail("r")
	if d.Run.Status != Interrupted || !d.Run.Resumable || d.Run.Steps[0].Output != "kept" || d.Run.Steps[1].Status != Queued {
		t.Fatalf("%+v", d.Run)
	}
	if again, err := reopened.ReconcileStartup("again"); err != nil || len(again) != 0 {
		t.Fatalf("non-idempotent: %+v %v", again, err)
	}
}

func TestAddStepsIsAtomicIdempotentAndRejectsConflictingDefinitions(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Run{ID: "r", Workflow: "dynamic", Steps: []Step{{ID: "gen", DefinitionHash: "sha256:gen"}}}); err != nil {
		t.Fatal(err)
	}
	added := []Step{{ID: "a", DefinitionHash: "sha256:a"}, {ID: "b", DefinitionHash: "sha256:b"}}
	if _, err := s.AddSteps("r", added); err != nil {
		t.Fatalf("add dynamic steps: %v", err)
	}
	if _, err := s.AddSteps("r", added); err != nil {
		t.Fatalf("identical replay must be idempotent: %v", err)
	}
	detail, _ := s.Detail("r")
	if detail.Total != 3 {
		t.Fatalf("idempotent replay duplicated steps: %+v", detail.Run.Steps)
	}
	if _, err := s.AddSteps("r", []Step{{ID: "a", DefinitionHash: "sha256:different"}}); err == nil {
		t.Fatal("same id with a different definition hash must be rejected")
	}
	if _, err := s.AddSteps("r", []Step{{ID: "c", DefinitionHash: "x"}, {ID: "c", DefinitionHash: "x"}}); err == nil {
		t.Fatal("duplicate ids in one atomic add must be rejected")
	}
	detail, _ = s.Detail("r")
	if detail.Total != 3 {
		t.Fatalf("failed atomic add mutated the run: %+v", detail.Run.Steps)
	}
}
