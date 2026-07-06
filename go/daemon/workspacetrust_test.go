package daemon

import (
	"strings"
	"testing"
)

// TestWorkspaceTrustGate: under strict trust mode, command execution is refused
// in an untrusted workspace and allowed once the workspace is trusted.
func TestWorkspaceTrustGate(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.requireTrust = true // enable strict trust (Options.RequireWorkspaceTrust)

	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")

	obs := d.agentRun(sess, task, []string{"echo", "hi"})
	if !strings.Contains(obs, "not trusted") {
		t.Fatalf("untrusted workspace must deny command exec, got: %s", obs)
	}

	d.trust.setTrust(ws, true)
	obs = d.agentRun(sess, task, []string{"echo", "hi"})
	if strings.Contains(obs, "not trusted") {
		t.Fatalf("trusted workspace must pass the trust gate, got: %s", obs)
	}
}
