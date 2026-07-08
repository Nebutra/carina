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

type promptRecordingReasoner struct {
	steps   []string
	prompts []string
}

func (p *promptRecordingReasoner) Name() string { return "prompt-recording" }
func (p *promptRecordingReasoner) Think(_ context.Context, prompt string) (string, error) {
	p.prompts = append(p.prompts, prompt)
	if len(p.steps) == 0 {
		return `{"tool":"done","summary":"ok"}`, nil
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	return step, nil
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
	if !strings.Contains(cap.lastPrompt, "PROJECT INSTRUCTIONS") {
		t.Fatal("memory section header missing from prompt")
	}
}

func TestProjectInstructionsLoadFromRepoRootToWorkspace(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(filepath.Join(nested, ".carina"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "CARINA.md"), []byte("ROOT_RULE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "AGENTS.md"), []byte("AGENTS_FALLBACK\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mem := loadMemory(nested)
	rootAt := strings.Index(mem, "ROOT_RULE")
	agentAt := strings.Index(mem, "AGENTS_FALLBACK")
	if rootAt < 0 || agentAt < 0 {
		t.Fatalf("expected root and nested instructions, got:\n%s", mem)
	}
	if rootAt > agentAt {
		t.Fatalf("root instructions should precede nested instructions:\n%s", mem)
	}
	if !strings.Contains(mem, "project (CARINA.md)") || !strings.Contains(mem, "project (services/api/AGENTS.md)") {
		t.Fatalf("expected provenance labels, got:\n%s", mem)
	}
}

func TestCarinaInstructionsWinOverAgentsFallback(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "CARINA.override.md"), []byte("CARINA_OVERRIDE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("AGENTS_SHOULD_NOT_WIN\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mem := loadMemory(repo)
	if !strings.Contains(mem, "CARINA_OVERRIDE") {
		t.Fatalf("expected CARINA override, got:\n%s", mem)
	}
	if strings.Contains(mem, "AGENTS_SHOULD_NOT_WIN") {
		t.Fatalf("AGENTS fallback should not load when CARINA candidate exists:\n%s", mem)
	}
}

func TestCarinaMemorySnapshotFrozenAcrossRun(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	scope := memoryScopeFromSession(sess)
	if _, err := d.memory.apply(scope, memoryWriteRequest{
		Action:  "add",
		Target:  "memory",
		Content: "OLD_MEMORY_MARKER",
	}); err != nil {
		t.Fatal(err)
	}

	rec := &promptRecordingReasoner{steps: []string{
		`{"tool":"memory","action":"add","target":"memory","content":"NEW_MEMORY_MARKER"}`,
		`{"tool":"done","summary":"ok"}`,
	}}
	d.SetReasoner(rec)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "remember a fact")
	d.runTask(sess, task)

	if len(rec.prompts) < 2 {
		t.Fatalf("expected at least two prompts, got %d", len(rec.prompts))
	}
	if !strings.Contains(rec.prompts[0], "OLD_MEMORY_MARKER") {
		t.Fatal("initial frozen memory snapshot missing old marker")
	}
	if strings.Contains(rec.prompts[1], "NEW_MEMORY_MARKER") {
		t.Fatal("memory written during a run must not refresh that run's frozen prompt snapshot")
	}
	state, err := d.memory.list(scope, "memory")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range state.Entries {
		if entry == "NEW_MEMORY_MARKER" {
			found = true
		}
	}
	if !found {
		t.Fatalf("memory tool write did not persist: %+v", state)
	}
}
