package daemon

import "testing"

func TestSetInteractiveApprovalRPCTogglesAndAudits(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)

	if !d.interactiveApproval.Load() {
		t.Fatal("expected interactive approval on")
	}
	// always-approve: interactive on=false
	off := false
	out, err := d.handleSetInteractiveApproval(mustJSON(t, map[string]any{"on": off, "session_id": "sess_test"}))
	if err != nil {
		t.Fatal(err)
	}
	res, _ := out.(map[string]any)
	if res["approval_mode"] != "always-approve" || d.interactiveApproval.Load() {
		t.Fatalf("result=%#v interactive=%v", res, d.interactiveApproval.Load())
	}
	if res["warning"] == nil || res["warning"] == "" {
		t.Fatal("expected warning string")
	}
	on := true
	out, err = d.handleSetInteractiveApproval(mustJSON(t, map[string]any{"on": on}))
	if err != nil {
		t.Fatal(err)
	}
	res, _ = out.(map[string]any)
	if res["approval_mode"] != "ask" || !d.interactiveApproval.Load() {
		t.Fatalf("result=%#v interactive=%v", res, d.interactiveApproval.Load())
	}
}
