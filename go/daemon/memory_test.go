package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capturingReasoner records the last prompt it was asked to reason over.
type capturingReasoner struct{ lastPrompt string }

func (c *capturingReasoner) Name() string { return "capture" }
func (c *capturingReasoner) Think(_ context.Context, prompt string) (string, error) {
	c.lastPrompt = prompt
	return `{"tool":"done","summary":"ok"}`, nil
}

// TestMemoryLoadedIntoPrompt: a project CARINA.md is injected into the system
// prompt the agent reasons over.
func TestMemoryLoadedIntoPrompt(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	os.WriteFile(filepath.Join(ws, "CARINA.md"), []byte("ALWAYS_USE_TABS marker\n"), 0o644)

	cap := &capturingReasoner{}
	d.SetReasoner(cap)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "do it")
	d.runTask(sess, task)

	if !strings.Contains(cap.lastPrompt, "ALWAYS_USE_TABS marker") {
		t.Fatal("CARINA.md memory should be injected into the prompt")
	}
	if !strings.Contains(cap.lastPrompt, "PROJECT MEMORY") {
		t.Fatal("memory section header missing from prompt")
	}
}
