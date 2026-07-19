package scheduler

import (
	"testing"
	"time"

	"github.com/Nebutra/carina/go/continuity"
)

func TestExecutionGenerationFencesLatePublisher(t *testing.T) {
	s := New()
	task := s.Submit("s", "w", "recover")
	first, err := s.AcquireExecution(task.TaskID, task.Revision, "local", "runtime-1", 1, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	firstGeneration := first.Continuity.Execution.LeaseGeneration
	if _, err := s.SetTerminalResultFenced(task.TaskID, 0, "completed", "stale unfenced", nil); err == nil {
		t.Fatal("unfenced publisher bypassed an active execution generation")
	}
	interrupted, err := s.Interrupt(task.TaskID, continuity.InterruptionRecord{
		Kind: continuity.InterruptionRuntimeLost, Actor: "system", ObservedAt: time.Now().UTC(),
		RuntimeEpoch: 1, TaskID: task.TaskID, Certainty: continuity.CertaintyInferred, Retryable: true,
	}, continuity.RecoveryDecision{Disposition: continuity.RecoveryResumeCheckpoint, CheckpointID: "cp", Proofs: map[string]bool{
		"checkpoint": true, "effect_replay": true, "workspace_anchor": true, "external_effects": true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquireExecution(task.TaskID, interrupted.Revision, "local", "runtime-2", 2, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Continuity.Execution.LeaseGeneration <= firstGeneration {
		t.Fatal("generation did not advance")
	}
	if _, err := s.SetStatusFenced(task.TaskID, firstGeneration, "completed"); err == nil {
		t.Fatal("stale generation published completion")
	}
	if _, err := s.SetStatusFenced(task.TaskID, second.Continuity.Execution.LeaseGeneration, "completed"); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledTaskCannotBeInterruptedOrRecovered(t *testing.T) {
	s := New()
	task := s.Submit("s", "w", "stop")
	if _, err := s.Cancel(task.TaskID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Interrupt(task.TaskID, continuity.InterruptionRecord{
		Kind: continuity.InterruptionOperatorCancelled, Actor: "user", ObservedAt: time.Now().UTC(),
		TaskID: task.TaskID, Certainty: continuity.CertaintyObserved,
	}, continuity.RecoveryDecision{Disposition: continuity.RecoveryNone})
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("cancelled task changed: %+v err=%v", got, err)
	}
	if _, err := s.AcquireExecution(task.TaskID, got.Revision, "local", "runtime", 2, time.Time{}); err == nil {
		t.Fatal("cancelled task acquired execution")
	}
}

func TestAutomaticRecoveryRunsOncePerInterruptionGeneration(t *testing.T) {
	s := New()
	task := s.Submit("s", "w", "recover once")
	interrupted, err := s.Interrupt(task.TaskID, continuity.InterruptionRecord{
		Kind: continuity.InterruptionRuntimeLost, Actor: "runtime", ObservedAt: time.Now().UTC(),
		TaskID: task.TaskID, Certainty: continuity.CertaintyInferred, Retryable: true,
	}, continuity.RecoveryDecision{Disposition: continuity.RecoveryResumeCheckpoint, CheckpointID: "cp", Proofs: map[string]bool{
		"checkpoint": true, "effect_replay": true, "workspace_anchor": true, "external_effects": true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.AcquireExecution(task.TaskID, interrupted.Revision, "local", "runtime-2", 2, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatusFenced(task.TaskID, claimed.Continuity.Execution.LeaseGeneration, "interrupted"); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Get(task.TaskID)
	if _, err := s.AcquireExecution(task.TaskID, current.Revision, "local", "runtime-3", 3, time.Time{}); err == nil {
		t.Fatal("second automatic recovery in same generation was accepted")
	}
}
