package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func TestApprovalResolveCanonicalFieldOverRPC(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	const decisionID = "decision_rpc_allow"
	pending := make(chan approvalSignal, 1)
	d.approvalMu.Lock()
	d.pendingApprovals[decisionID] = pending
	d.approvalMu.Unlock()
	defer func() {
		d.approvalMu.Lock()
		delete(d.pendingApprovals, decisionID)
		d.approvalMu.Unlock()
	}()

	socket := filepath.Join(os.TempDir(), fmt.Sprintf("carina-approval-%d.sock", time.Now().UnixNano()))
	defer os.Remove(socket)
	go func() { _ = d.Run(socket) }()
	var client *rpc.Client
	deadline := time.Now().Add(2 * time.Second)
	for client == nil {
		var err error
		client, err = rpc.Dial(socket)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial daemon: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer client.Close()
	if err := client.Call("task.approval.resolve", map[string]any{
		"decision_id": decisionID, "approve": true, "scope": "once",
	}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case signal := <-pending:
		if !signal.granted || signal.scope != approvalScopeOnce {
			t.Fatalf("approval signal = %#v", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("approval RPC did not unblock the pending decision")
	}
}
