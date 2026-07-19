package continuity

import (
	"testing"
	"time"
)

func TestEmptyStateValid(t *testing.T) {
	if err := EmptyState().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestToolEffectRegistryFailsClosed(t *testing.T) {
	if got := ClassifyTool("read", nil); got.Class != EffectPure || !got.ReplaySafe {
		t.Fatalf("read contract = %+v", got)
	}
	if got := ClassifyTool("run", nil); got.Class != EffectUnknown || got.ReplaySafe {
		t.Fatalf("run must fail closed, got %+v", got)
	}
	if got := ClassifyTool("memory", map[string]any{"idempotency_key": "stable"}); got.Class != EffectIdempotentExternal || !got.ReplaySafe {
		t.Fatalf("keyed external contract = %+v", got)
	}
	if got := ClassifyTool("memory", nil); got.ReplaySafe {
		t.Fatalf("unkeyed external effect must not replay: %+v", got)
	}
}

func TestUnknownEffectIsNotReplaySafe(t *testing.T) {
	if ReplaySafe(EffectUnknown, "") {
		t.Fatal("unknown effects must fail closed")
	}
	if !ReplaySafe(EffectPure, "") {
		t.Fatal("pure effects should be replay-safe")
	}
	if ReplaySafe(EffectIdempotentExternal, "") {
		t.Fatal("external idempotency requires a stable key")
	}
	if !ReplaySafe(EffectIdempotentExternal, "stable-key") {
		t.Fatal("keyed external idempotency should be replay-safe")
	}
}

func TestLocalLeaseRequiresEpoch(t *testing.T) {
	lease := ExecutionLease{OwnerKind: "local", OwnerID: "runtime_1", LeaseGeneration: 1}
	if err := lease.Validate(); err == nil {
		t.Fatal("local lease without epoch must fail")
	}
	lease.RuntimeEpoch = 1
	if err := lease.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptionRequiresEvidenceShape(t *testing.T) {
	r := InterruptionRecord{Kind: InterruptionRuntimeLost, Actor: "system", TaskID: "task_1", ObservedAt: time.Now().UTC(), Certainty: CertaintyInferred}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Kind = "power_loss"
	if err := r.Validate(); err == nil {
		t.Fatal("unobservable physical cause must not be accepted")
	}
}
