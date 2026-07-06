package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
)

// handleTaskBtw answers an ephemeral "by the way" side-question in the context of
// a running task, WITHOUT adding it to the task transcript — so a quick aside
// never pollutes or redirects the main run. It reuses the latest transcript
// checkpoint as read-only context. Recorded as a side_query audit event only.
func (d *Daemon) handleTaskBtw(params json.RawMessage) (any, error) {
	var p struct {
		TaskID   string `json:"task_id"`
		Question string `json:"question"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if strings.TrimSpace(p.Question) == "" {
		return nil, fmt.Errorf("btw needs a question")
	}
	task, ok := d.sched.Get(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("unknown task %s", p.TaskID)
	}
	sess, ok := d.store.Get(task.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", task.SessionID)
	}
	if d.reasoner == nil {
		return nil, fmt.Errorf("no reasoner available for a side-query")
	}

	var transcript string
	if cp := d.runs.loadCheckpoint(p.TaskID); cp != nil && cp.Transcript != nil {
		transcript = cp.Transcript.render()
	}
	ans, err := thinkWithRetry(context.Background(), d.reasoner, buildSideQueryPrompt(task, p.Question, transcript))
	if err != nil {
		return nil, err
	}
	// Ephemeral: audited as a side_query, but never folded into the transcript,
	// so the main run's state and plan are untouched.
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
		map[string]any{"status": "side_query", "question": truncate(p.Question, 200)}, "")
	return map[string]any{"answer": ans, "ephemeral": true}, nil
}

func buildSideQueryPrompt(task *scheduler.Task, question, transcript string) string {
	var b strings.Builder
	b.WriteString("You are mid-task. Answer this SIDE QUESTION briefly and directly. ")
	b.WriteString("Your answer is ephemeral — it will NOT be added to your task transcript and must not change your plan.\n\n")
	fmt.Fprintf(&b, "CURRENT TASK: %s\n\n", task.UserPrompt)
	if transcript != "" {
		fmt.Fprintf(&b, "WORK SO FAR:\n%s\n\n", transcript)
	}
	fmt.Fprintf(&b, "SIDE QUESTION: %s\n\nAnswer:", question)
	return b.String()
}
