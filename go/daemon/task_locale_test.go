package daemon

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
)

func TestTaskSubmitCanonicalizesAndPersistsLocale(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&capturingReasoner{})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	result, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "hi",
		"locale":     "zh-Hans",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := result.(*scheduler.ExecutionRun)
	if task.Locale != "zh" {
		t.Fatalf("task locale = %q, want canonical zh", task.Locale)
	}

	if _, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "hi",
		"locale":     "not-real",
	})); err == nil || !strings.Contains(err.Error(), "locale") {
		t.Fatalf("invalid locale error = %v", err)
	}
}

func TestTaskSubmissionIdentityIncludesLocale(t *testing.T) {
	en := taskSubmitParams{Prompt: "hi", Locale: "en"}
	zh := taskSubmitParams{Prompt: "hi", Locale: "zh"}
	if taskSubmissionFingerprint(en) == taskSubmissionFingerprint(zh) {
		t.Fatal("task submission fingerprint ignored locale")
	}
}
