package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAddDirGrantsScopedRoot: session.add_dir widens a session to an additional
// directory — a read there is denied before the grant and allowed after, while a
// read outside any root stays denied.
func TestAddDirGrantsScopedRoot(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	extra := t.TempDir()
	target := filepath.Join(extra, "notes.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before the grant: reading outside the workspace is denied.
	dec, err := d.kern.Request(sess.SessionID, "FileRead", target, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision == "allowed" {
		t.Fatal("read outside workspace must be denied before add_dir")
	}

	// Grant the extra dir via the handler.
	res, err := d.handleAddDir(mustJSON(t, map[string]any{"session_id": sess.SessionID, "path": extra}))
	if err != nil {
		t.Fatalf("handleAddDir: %v", err)
	}
	if res.(map[string]any)["granted"] != true {
		t.Fatalf("add_dir did not confirm the grant: %+v", res)
	}

	// After the grant: the same read is allowed.
	dec, err = d.kern.Request(sess.SessionID, "FileRead", target, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "allowed" {
		t.Fatalf("read in the granted dir should be allowed, got %q (%s)", dec.Decision, dec.Reason)
	}

	// A path in neither root remains denied.
	other := filepath.Join(t.TempDir(), "x.txt")
	_ = os.WriteFile(other, []byte("no\n"), 0o600)
	dec, _ = d.kern.Request(sess.SessionID, "FileRead", other, "t3")
	if dec.Decision == "allowed" {
		t.Fatal("read outside every granted root must stay denied")
	}

	// A non-existent directory is rejected.
	if _, err := d.handleAddDir(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "path": filepath.Join(extra, "nope")})); err == nil {
		t.Fatal("add_dir on a missing directory must error")
	}
}

func TestAgentAddDirGrantsAfterOperatorConsent(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "write to extra")
	extra := t.TempDir()
	target := filepath.Join(extra, "snake.html")

	questions := make(chan map[string]any, 1)
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "user.question" {
			questions <- ev
		}
	})
	result := make(chan toolExecutionOutcome, 1)
	go func() {
		result <- d.agentAddDirOutcome(sess, task, extra)
	}()
	var question map[string]any
	select {
	case question = <-questions:
	case <-time.After(2 * time.Second):
		t.Fatal("add_dir did not ask to grant")
	}
	questionID, _ := question["question_id"].(string)
	if _, err := d.handleUserAnswer(mustJSON(t, map[string]any{"question_id": questionID, "value": "grant"})); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-result:
		if outcome.status != "completed" || !strings.Contains(outcome.display, extra) {
			t.Fatalf("grant outcome = %+v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("add_dir did not resume")
	}
	if err := os.WriteFile(target, []byte("<html></html>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dec, err := d.kern.Request(sess.SessionID, "FileRead", target, "t-grant")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "allowed" {
		t.Fatalf("read in granted dir = %q (%s)", dec.Decision, dec.Reason)
	}
}

func TestAgentAddDirRefusesHomeAndMissing(t *testing.T) {
	if home, err := os.UserHomeDir(); err == nil && addDirTooBroad(home) != true {
		t.Fatal("home directory must be too broad to grant")
	}
	if !addDirTooBroad("/") {
		t.Fatal("filesystem root must be too broad to grant")
	}
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "grant missing")
	got := d.agentAddDirOutcome(sess, task, filepath.Join(t.TempDir(), "no-such-dir"))
	if got.status == "completed" {
		t.Fatalf("missing dir = %+v", got)
	}
}

func TestWorkspacePromptDirectsAddDirInsteadOfRewritingDestination(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	layers := d.composeAgentPromptLayers(sess, task, "")
	if !strings.Contains(layers.Workspace, "add_dir") {
		t.Fatalf("workspace scope must name add_dir:\n%s", layers.Workspace)
	}
	if strings.Contains(layers.Workspace, "cannot inspect the desktop") {
		t.Fatal("workspace scope still tells the model the desktop is impossible")
	}
	if !strings.Contains(coreConstitution(), "- add_dir:") {
		t.Fatal("tools catalog missing add_dir")
	}
}
