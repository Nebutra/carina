package tuiapp

import (
	"testing"

	"github.com/Nebutra/carina/go/tui"
)

type fakeRebindController struct {
	token        uint64
	generation   uint64
	prepared     tui.ConnectionTarget
	committed    bool
	aborted      bool
	acknowledged bool
}

type supersedingRebindController struct {
	next    uint64
	aborted []uint64
}

func (f *supersedingRebindController) PrepareTarget(tui.ConnectionTarget) (uint64, error) {
	f.next++
	return f.next, nil
}

func (f *supersedingRebindController) CommitPrepared(uint64) error { return nil }
func (f *supersedingRebindController) AbortPrepared(token uint64) uint64 {
	f.aborted = append(f.aborted, token)
	return f.next
}
func (f *supersedingRebindController) AcknowledgePrepared(uint64) {}

func (f *fakeRebindController) PrepareTarget(target tui.ConnectionTarget) (uint64, error) {
	f.prepared = target
	return f.token, nil
}

func (f *fakeRebindController) CommitPrepared(token uint64) error {
	f.committed = token == f.token
	return nil
}

func (f *fakeRebindController) AbortPrepared(token uint64) uint64 {
	f.aborted = token == f.token
	return f.generation
}

func (f *fakeRebindController) AcknowledgePrepared(token uint64) {
	f.acknowledged = token == f.token
}

func TestRuntimeCoordinatorRollbackRestoresSourceRoot(t *testing.T) {
	controller := &fakeRebindController{token: 7, generation: 5}
	coordinator := newRuntimeCoordinator(t.TempDir(), "/work/source", controller)
	token, err := coordinator.prepare(tui.ConnectionTarget{WorkspaceRoot: "/work/destination"})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.commit(token); err != nil {
		t.Fatal(err)
	}
	if got := coordinator.currentRoot(); got != "/work/destination" {
		t.Fatalf("committed root=%q", got)
	}
	if generation := coordinator.abort(token); generation != 5 {
		t.Fatalf("rollback generation=%d", generation)
	}
	if got := coordinator.currentRoot(); got != "/work/source" {
		t.Fatalf("rollback root=%q", got)
	}
	if !controller.committed || !controller.aborted {
		t.Fatalf("controller lifecycle committed=%v aborted=%v", controller.committed, controller.aborted)
	}
}

func TestRuntimeCoordinatorAcknowledgeForgetsReceipt(t *testing.T) {
	controller := &fakeRebindController{token: 11}
	coordinator := newRuntimeCoordinator(t.TempDir(), "/work/source", controller)
	token, err := coordinator.prepare(tui.ConnectionTarget{WorkspaceRoot: "/work/destination"})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.commit(token); err != nil {
		t.Fatal(err)
	}
	coordinator.acknowledge(token)
	if !controller.acknowledged {
		t.Fatal("controller was not acknowledged")
	}
	if _, ok := coordinator.prepared[token]; ok {
		t.Fatal("coordinator retained acknowledged receipt")
	}
}

func TestRuntimeCoordinatorAbortSupersededCommitPreservesNewestRoot(t *testing.T) {
	controller := &supersedingRebindController{}
	coordinator := newRuntimeCoordinator(t.TempDir(), "/work/source", controller)
	first, err := coordinator.prepare(tui.ConnectionTarget{WorkspaceRoot: "/work/first"})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.commit(first); err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.prepare(tui.ConnectionTarget{WorkspaceRoot: "/work/second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.commit(second); err != nil {
		t.Fatal(err)
	}

	coordinator.abort(first)
	if got := coordinator.currentRoot(); got != "/work/second" {
		t.Fatalf("superseded abort restored stale root: %q", got)
	}
	if len(controller.aborted) != 2 || controller.aborted[0] != first || controller.aborted[1] != first {
		t.Fatalf("superseded receipt was not retired before stale abort: %v", controller.aborted)
	}
}
