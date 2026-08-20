package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFollowUpRunSeesPriorSessionAnswer(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("carina runtime\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reasoner := &conversationFirstReasoner{}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	first := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "分析这个 repo")
	d.sched.SetLocale(first.RunID, "zh")
	first, _ = d.sched.Get(first.RunID)
	d.runTask(sess, first)
	completed, _ := d.sched.Get(first.RunID)
	if completed.Status != "completed" || completed.Summary == "" {
		t.Fatalf("first run status=%q summary=%q", completed.Status, completed.Summary)
	}

	reasoner.prompt = ""
	follow := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "对此你怎么看")
	d.sched.SetLocale(follow.RunID, "zh")
	follow, _ = d.sched.Get(follow.RunID)
	d.runTask(sess, follow)

	if !strings.Contains(reasoner.prompt, "TASK: 对此你怎么看") {
		t.Fatalf("follow-up missing current TASK:\n%s", truncate(reasoner.prompt, 400))
	}
	if !strings.Contains(reasoner.prompt, "Earlier in this conversation") {
		t.Fatalf("follow-up missing session dialogue:\n%s", truncate(reasoner.prompt, 800))
	}
	if !strings.Contains(reasoner.prompt, "Operator: 分析这个 repo") {
		t.Fatalf("follow-up missing prior operator turn:\n%s", truncate(reasoner.prompt, 800))
	}
	if !strings.Contains(reasoner.prompt, "Carina: "+completed.Summary) {
		t.Fatalf("follow-up missing prior answer %q:\n%s", completed.Summary, truncate(reasoner.prompt, 800))
	}
}

func TestGreetingRunDoesNotInventSessionDialogue(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	reasoner := &conversationFirstReasoner{}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	d.sched.SetLocale(task.RunID, "zh")
	task, _ = d.sched.Get(task.RunID)
	d.runTask(sess, task)
	if strings.Contains(reasoner.prompt, "Earlier in this conversation") {
		t.Fatalf("first turn invented session dialogue:\n%s", truncate(reasoner.prompt, 400))
	}
}
