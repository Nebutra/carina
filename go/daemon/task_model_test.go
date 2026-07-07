package daemon

import (
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
)

func TestTaskSubmitStoresModelOverride(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	res, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "use a specific model",
		"model":      "openai/gpt-5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := res.(*scheduler.Task)
	if task.Model != "openai/gpt-5" {
		t.Fatalf("model override not stored: %+v", task)
	}
}
