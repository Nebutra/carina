package workflowui

import "testing"

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

func TestRejectsInvalidTransitions(t *testing.T) {
	s, _ := New(t.TempDir())
	_, _ = s.Create(Run{ID: "r", Workflow: "w"})
	if _, err := s.Transition("r", Completed); err == nil {
		t.Fatal("expected invalid transition")
	}
}
