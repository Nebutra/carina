package daemon

import (
	"strings"
	"testing"
)

func TestTaskLocaleControlsUserFacingSystemPromptLanguage(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	cap := &capturingReasoner{}
	d.SetReasoner(cap)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	d.sched.SetLocale(task.RunID, "zh")
	task, _ = d.sched.Get(task.RunID)
	d.runTask(sess, task)

	for _, want := range []string{
		"OUTPUT LANGUAGE (operator preference)",
		"Use Simplified Chinese",
		"Tool-action JSON keys and schema remain exactly as specified",
	} {
		if !strings.Contains(cap.lastPrompt, want) {
			t.Fatalf("localized system prompt missing %q:\n%s", want, cap.lastPrompt)
		}
	}
}

func TestUnknownTaskLocaleDoesNotInventALanguageInstruction(t *testing.T) {
	if got := outputLanguagePrompt(""); got != "" {
		t.Fatalf("empty locale prompt = %q, want empty", got)
	}
	if got := outputLanguagePrompt("not-real"); got != "" {
		t.Fatalf("unknown locale prompt = %q, want empty", got)
	}
}
