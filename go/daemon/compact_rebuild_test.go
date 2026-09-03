package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCitedFilesNewestFirstUniqueAndSkipsSkillURI(t *testing.T) {
	turns := []Turn{
		{Tool: "read", Path: "old.go", ActionBrief: "read old.go"},
		{Tool: "read", Path: "skill://pdf", ActionBrief: "read skill://pdf"},
		{Tool: "search", ActionBrief: "search foo"},
		{Tool: "patch", ActionBrief: "patch new.go"},
		{Tool: "read", Path: "old.go", ActionBrief: "read old.go"},
		{Tool: "edit", ActionBrief: "edit mid.go"},
		{Tool: "read", Path: "../secret", ActionBrief: "read ../secret"},
	}
	got := citedFiles(turns, 5)
	want := []string{"mid.go", "old.go", "new.go"}
	if len(got) != len(want) {
		t.Fatalf("citedFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("citedFiles = %v, want %v", got, want)
		}
	}
	if out := citedFiles(turns, 0); out != nil {
		t.Fatalf("k=0 must select nothing, got %v", out)
	}
}

func TestRebuildRendersInVolatileTranscriptNotPrefix(t *testing.T) {
	tr := newTranscript("task")
	tr.Summary = "earlier work"
	tr.Rebuild = "REBUILT CONTEXT (post-compact; re-read, not new user input):\n--- a.go ---\npackage a"
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read b.go", Obs: Observation{Content: "package b"}})
	got := tr.render()
	if !strings.Contains(got, "SUMMARY OF EARLIER WORK:") || !strings.Contains(got, "REBUILT CONTEXT") || !strings.Contains(got, "package a") {
		t.Fatalf("render missing rebuild block:\n%s", got)
	}
	seg := buildPromptSegmentsFromLayers(promptLayers{Constitution: "SYS", Catalog: "CATALOG"}, "hi", got, "GO")
	if strings.Contains(seg.StablePrefix, "REBUILT CONTEXT") || strings.Contains(seg.StablePrefix, "package a") {
		t.Fatal("rebuild must not live in the cacheable prefix")
	}
	if !strings.Contains(seg.VolatileSuffix, "REBUILT CONTEXT") || !strings.Contains(seg.VolatileSuffix, "TASK: hi") {
		t.Fatal("rebuild belongs in the volatile suffix with TASK")
	}
	for _, section := range seg.CacheSections() {
		if strings.Contains(section, "REBUILT CONTEXT") {
			t.Fatalf("CacheSections leaked rebuild: %q", section)
		}
	}
}

func TestRebuildAfterCompactRehydratesCitedFiles(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "cited.go"), []byte("package cited\nconst X = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("GREETING_MUST_NOT_LOAD_THIS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	tr := newTranscript(task.UserPrompt)
	receipt := &CompactionReceipt{CitedFiles: []string{"cited.go"}}
	paths := d.rebuildAfterCompact(sess, task, tr, receipt)
	if len(paths) != 1 || paths[0] != "cited.go" {
		t.Fatalf("rebuild paths = %v", paths)
	}
	if !strings.Contains(tr.Rebuild, "package cited") || !strings.Contains(tr.Rebuild, "--- cited.go ---") {
		t.Fatalf("rebuild missing cited file:\n%s", tr.Rebuild)
	}
	if strings.Contains(tr.Rebuild, "GREETING_MUST_NOT_LOAD_THIS") || strings.Contains(tr.Rebuild, "PROJECT INSTRUCTIONS") {
		t.Fatalf("converse rebuild must not dump AGENTS.md:\n%s", tr.Rebuild)
	}
	layers := d.composeAgentPromptLayers(sess, task, "")
	if strings.Contains(layers.Workspace, "GREETING_MUST_NOT_LOAD_THIS") {
		t.Fatal("converse prefix must still omit project instructions")
	}
	if strings.Contains(layers.Workspace, "package cited") {
		t.Fatal("rebuild must not mutate the Workspace prefix")
	}
}

func TestRebuildAfterCompactKeepsProjectInstructionsInDynamicLayerOnly(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "cited.go"), []byte("package cited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("BUILD_RULE_MUST_SURVIVE_COMPACT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	task.Agent = "build"
	tr := newTranscript(task.UserPrompt)
	paths := d.rebuildAfterCompact(sess, task, tr, &CompactionReceipt{CitedFiles: []string{"cited.go"}})
	if len(paths) != 1 || paths[0] != "cited.go" {
		t.Fatalf("rebuild paths = %v", paths)
	}
	if !strings.Contains(tr.Rebuild, "package cited") {
		t.Fatalf("build rebuild missing cited file:\n%s", tr.Rebuild)
	}
	if strings.Contains(tr.Rebuild, "PROJECT INSTRUCTIONS") || strings.Contains(tr.Rebuild, "BUILD_RULE_MUST_SURVIVE_COMPACT") {
		t.Fatalf("rebuild must not duplicate dynamic project instructions:\n%s", tr.Rebuild)
	}
	layers := d.composeAgentPromptLayers(sess, task, "")
	if strings.Contains(layers.Workspace, "package cited") {
		t.Fatal("rebuild must not mutate the Workspace prefix")
	}
	seg := buildPromptSegmentsFromLayers(layers, task.UserPrompt, tr.render(), "GO")
	if got := strings.Count(seg.full(), "BUILD_RULE_MUST_SURVIVE_COMPACT"); got != 1 {
		t.Fatalf("current project instruction occurrences = %d, want exactly 1:\n%s", got, seg.full())
	}
}

func TestResumeChangedInstructionManifestDropsStaleRebuildRules(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	rulesPath := filepath.Join(ws, "AGENTS.md")
	if err := os.WriteFile(rulesPath, []byte("STALE_RULE_MUST_DISAPPEAR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "cited.go"), []byte("package refreshed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "continue build", "", "build", nil)
	oldManifest := instructionManifestForTask(d, ws, task.Agent)
	tr := newTranscript(task.UserPrompt)
	tr.Rebuild = "REBUILT CONTEXT (post-compact; re-read, not new user input):\nPROJECT INSTRUCTIONS (legacy copy):\nSTALE_RULE_MUST_DISAPPEAR"
	tr.CompactionReceipts = append(tr.CompactionReceipts, CompactionReceipt{Version: 3, CitedFiles: []string{"cited.go"}})
	if err := os.WriteFile(rulesPath, []byte("CURRENT_RULE_MUST_APPEAR_ONCE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spy := &promptSpyReasoner{scriptedReasoner: scriptedReasoner{steps: []string{`{"tool":"done","summary":"resumed"}`}}}
	d.SetReasoner(spy)
	d.resumeTaskContext(t.Context(), sess, task, &runCheckpoint{Turn: 0, Transcript: tr, InstructionManifest: oldManifest})
	if len(spy.prompts) != 1 {
		t.Fatalf("resume prompts = %d, want 1", len(spy.prompts))
	}
	prompt := spy.prompts[0]
	if strings.Contains(prompt, "STALE_RULE_MUST_DISAPPEAR") {
		t.Fatalf("resume prompt retained stale project instructions:\n%s", prompt)
	}
	if got := strings.Count(prompt, "CURRENT_RULE_MUST_APPEAR_ONCE"); got != 1 {
		t.Fatalf("current project instruction occurrences = %d, want exactly 1:\n%s", got, prompt)
	}
	if !strings.Contains(prompt, "package refreshed") {
		t.Fatalf("resume prompt did not rebuild cited file evidence:\n%s", prompt)
	}
}

func TestRebuildAfterCompactSkipsUncitedProjectInstructions(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("GREETING_MUST_NOT_LOAD_THIS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	tr := newTranscript(task.UserPrompt)
	d.rebuildAfterCompact(sess, task, tr, &CompactionReceipt{})
	if tr.Rebuild != "" {
		t.Fatalf("empty cited set must not rebuild:\n%s", tr.Rebuild)
	}
}

func TestRebuildAfterCompactCapsTotalBytes(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	big := strings.Repeat("x", maxRebuildTotalBytes)
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(big), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "build it")
	tr := newTranscript(task.UserPrompt)
	d.rebuildAfterCompact(sess, task, tr, &CompactionReceipt{CitedFiles: []string{"a.go", "b.go"}})
	if len(tr.Rebuild) > maxRebuildTotalBytes {
		t.Fatalf("rebuild exceeded cap: %d", len(tr.Rebuild))
	}
	if !strings.Contains(tr.Rebuild, "--- a.go ---") {
		t.Fatalf("rebuild dropped the newest cited file:\n%s", tr.Rebuild)
	}
}

func TestCompactReceiptRecordsCitedFiles(t *testing.T) {
	tr := newTranscript("fix")
	tr.policy = CompactionPolicy{MaxChars: 400, KeepRecent: 1, ToolOutputMax: 10_000, SummarizeAfter: 1}
	tr.addTurn(Turn{Tool: "read", Path: "a.go", ActionBrief: "read a.go", Obs: Observation{Content: strings.Repeat("data ", 80)}})
	tr.addTurn(Turn{Tool: "patch", ActionBrief: "patch b.go", Obs: Observation{Content: strings.Repeat("data ", 80)}})
	tr.addTurn(Turn{Tool: "read", Path: "c.go", ActionBrief: "read c.go", Obs: Observation{Content: strings.Repeat("data ", 80)}})
	receipt := tr.compact(func(string) (string, error) { return "SUMMARY", nil })
	if receipt == nil {
		t.Fatal("expected compaction receipt")
	}
	if len(receipt.CitedFiles) == 0 || receipt.CitedFiles[0] != "b.go" {
		t.Fatalf("CitedFiles newest-first among folded turns, got %v", receipt.CitedFiles)
	}
	for _, path := range receipt.CitedFiles {
		if path == "c.go" {
			t.Fatal("KeepRecent tail must not be listed as folded cited files")
		}
	}
}

func TestElisionOnlyReceiptIsPersistedByCompactRebuild(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(ws, "read-only")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "read-only", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect evidence")
	tr := newTranscript(task.UserPrompt)
	tr.policy = CompactionPolicy{MaxChars: 700, KeepRecent: 1, ToolOutputMax: 10_000, SummarizeAfter: 100}
	for i := 0; i < 4; i++ {
		tr.addTurn(Turn{Tool: "read", ActionBrief: "read evidence", Obs: Observation{Content: strings.Repeat("evidence ", 80)}})
	}
	receipt := tr.compact(nil)
	if receipt == nil || receipt.Version != 4 {
		t.Fatalf("elision-only receipt = %+v", receipt)
	}
	d.recordCompactRebuild(sess, task, tr, receipt, nil)
	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"type":"ContextCompacted"`) || !strings.Contains(string(raw), `"version":4`) || !strings.Contains(string(raw), `"elided_turn_indices"`) {
		t.Fatalf("audit read lost elision-only compaction receipt: %s", raw)
	}
}
