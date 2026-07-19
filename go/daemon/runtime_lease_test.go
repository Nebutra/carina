package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeLeaseFencesSecondOwnerAndAdvancesEpoch(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireRuntimeLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRuntimeLease(dir); err == nil {
		t.Fatal("second state-directory owner acquired the lock")
	}
	firstEpoch := first.state.Epoch
	if err := first.close(true); err != nil {
		t.Fatal(err)
	}
	second, err := acquireRuntimeLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close(false)
	if second.state.Epoch <= firstEpoch || !second.previousGraceful {
		t.Fatalf("second lease = %+v previousGraceful=%v", second.state, second.previousGraceful)
	}
}

func TestRuntimeLeasePersistsDurableState(t *testing.T) {
	dir := t.TempDir()
	lease, err := acquireRuntimeLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.close(true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state runtimeState
	if err := json.Unmarshal(raw, &state); err != nil || state.ShutdownKind != "graceful" || state.Epoch < 1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
