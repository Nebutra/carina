package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func TestLooksLikeTestCommand(t *testing.T) {
	if !looksLikeTestCommand([]string{"go", "test", "./internal/auth"}) {
		t.Fatal("go test")
	}
	if !looksLikeTestCommand([]string{"cargo", "test"}) {
		t.Fatal("cargo test")
	}
	if looksLikeTestCommand([]string{"echo", "test"}) {
		t.Fatal("echo test must not count")
	}
	if looksLikeTestCommand([]string{"test", "-f", "a"}) {
		t.Fatal("unix test must not count")
	}
}

func TestProactiveDefaultOff(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, task, tr := proactiveBuildSession(t, d, ws)
	proactiveTouchModule(d, sess, task, tr)
	if d.pendingProposalCount(sess.SessionID) != 0 {
		t.Fatal("default off must stay silent")
	}
}

func TestProactiveConverseSilent(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	tr := newTranscript("hi")
	before := len(tr.Turns)
	for _, path := range []string{"auth/a.go", "auth/b.go", "auth/c.go"} {
		d.considerProactive(sess, task, &action{Tool: "read", Path: path}, toolCompleted("ok"), 1, tr)
	}
	if d.pendingProposalCount(sess.SessionID) != 0 {
		t.Fatal("converse must stay silent")
	}
	if len(tr.Turns) != before {
		t.Fatalf("transcript grew: %d -> %d", before, len(tr.Turns))
	}
}

func TestProactiveModuleRepeatOnBuild(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, task, tr := proactiveBuildSession(t, d, ws)
	before := len(tr.Turns)
	proactiveTouchModule(d, sess, task, tr)
	waitForPendingProposals(t, d, sess.SessionID, 1)
	if len(tr.Turns) != before {
		t.Fatal("proposal must not become a transcript turn")
	}
	raw, err := d.handleProposalList(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	body := raw.(map[string]any)
	if body["pending"].(int) != 1 {
		t.Fatalf("list pending = %#v", body["pending"])
	}
}

func TestProactiveIgnoresThenCoolsDown(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	now := time.Now().UTC()
	d.proposals.now = func() time.Time { return now }
	sess, task, tr := proactiveBuildSession(t, d, ws)
	proactiveTouchModule(d, sess, task, tr)
	waitForPendingProposals(t, d, sess.SessionID, 1)
	listed, err := d.handleProposalList(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	id := firstProposalID(t, listed)
	if _, err := d.handleProposalIgnore(mustJSON(t, map[string]any{"proposal_id": id, "mute": true})); err != nil {
		t.Fatal(err)
	}
	if d.pendingProposalCount(sess.SessionID) != 0 {
		t.Fatal("ignore must clear pending")
	}
	proactiveTouchModule(d, sess, task, tr)
	time.Sleep(50 * time.Millisecond)
	if d.pendingProposalCount(sess.SessionID) != 0 {
		t.Fatal("muted class must stay silent")
	}
}

func TestProactiveAcceptSteers(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, task, tr := proactiveBuildSession(t, d, ws)
	proactiveTouchModule(d, sess, task, tr)
	waitForPendingProposals(t, d, sess.SessionID, 1)
	listed, err := d.handleProposalList(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	id := firstProposalID(t, listed)
	got, err := d.handleProposalAccept(mustJSON(t, map[string]any{"proposal_id": id}))
	if err != nil {
		t.Fatal(err)
	}
	out := got.(map[string]any)
	if out["accepted"] != true || out["steered"] != true {
		t.Fatalf("accept = %#v", out)
	}
	if d.pendingProposalCount(sess.SessionID) != 0 {
		t.Fatal("accept must clear pending")
	}
}

func TestProactivePendingCapAndPlanSilent(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, task, tr := proactiveBuildSession(t, d, ws)
	proactiveTouchModule(d, sess, task, tr)
	waitForPendingProposals(t, d, sess.SessionID, 1)
	d.considerProactive(sess, task, &action{Tool: "run", Command: []string{"go", "test", "./auth"}}, toolFailed("exit=1", "nonzero_exit"), 4, tr)
	if d.pendingProposalCount(sess.SessionID) != 1 {
		t.Fatalf("cap must hold at 1, got %d", d.pendingProposalCount(sess.SessionID))
	}
	d.setPlanMode(sess.SessionID, true)
	other := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "plan", "", "build", nil)
	d.considerProactive(sess, other, &action{Tool: "read", Path: "auth/z.go"}, toolCompleted("ok"), 1, tr)
	d.considerProactive(sess, other, &action{Tool: "read", Path: "auth/y.go"}, toolCompleted("ok"), 2, tr)
	d.considerProactive(sess, other, &action{Tool: "read", Path: "auth/x.go"}, toolCompleted("ok"), 3, tr)
	if d.pendingProposalCount(sess.SessionID) != 1 {
		t.Fatal("plan mode must not add cards")
	}
}

func TestProactiveTestFail(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, task, tr := proactiveBuildSession(t, d, ws)
	d.considerProactive(sess, task, &action{Tool: "run", Command: []string{"go", "test", "./..."}}, toolFailed("exit=1\nFAIL", "nonzero_exit"), 1, tr)
	waitForPendingProposals(t, d, sess.SessionID, 1)
}

func TestProactivePreparerAddsReadOnlyEvidence(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.MkdirAll(filepath.Join(ws, "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "auth", "auth_test.go"), []byte("package auth\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "other_test.go"), []byte("package other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, task, tr := proactiveBuildSession(t, d, ws)
	before := len(tr.Turns)
	proactiveTouchModule(d, sess, task, tr)
	waitForPendingProposals(t, d, sess.SessionID, 1)
	listed, err := d.handleProposalList(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	rows := listed.(map[string]any)["proposals"].([]*proposal)
	if len(rows) != 1 || !rows[0].Prepared || rows[0].TriggerVersion != proactiveTriggerVersion {
		t.Fatalf("proposal was not prepared: %#v", rows)
	}
	if len(rows[0].Evidence) != 1 || rows[0].Evidence[0].Path != "auth/auth_test.go" {
		t.Fatalf("evidence escaped module or is missing: %#v", rows[0].Evidence)
	}
	if !strings.Contains(rows[0].Done, "likely tests: auth/auth_test.go") {
		t.Fatalf("prepared evidence missing from card: %q", rows[0].Done)
	}
	if len(tr.Turns) != before {
		t.Fatal("preparer evidence must not become a transcript turn")
	}
	if rows[0].Preparation.ToolCalls != 1 || rows[0].Preparation.FilesScanned < 2 {
		t.Fatalf("preparation budget missing: %#v", rows[0].Preparation)
	}
	info, err := os.Stat(filepath.Join(d.stateDir, "proposals.json"))
	if err != nil {
		t.Fatalf("proposal was not persisted: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("proposal store mode = %o", info.Mode().Perm())
	}
}

func TestProposalStoreRoundTripPreservesPendingMuteAndCooldown(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	s := newProposalStore(stateDir)
	s.items = []*proposal{
		{
			ID: "prop_roundtrip", SessionID: "sess_1", RunID: "run_1", Class: proactiveClassModule,
			Title: "Prepare tests", Status: proactiveStatusPending, Created: now, Prepared: true,
			Evidence: []proposalEvidence{{Kind: "test_file", Path: "auth/auth_test.go"}},
		},
		{
			ID: "prop_accepted", SessionID: "sess_1", RunID: "run_1", Class: proactiveClassTestFail,
			Title: "Tests failed", Status: proactiveStatusAccepted, Created: now.Add(time.Millisecond),
		},
	}
	key := proactiveKey("sess_1", proactiveClassModule)
	s.lastFired[key] = now
	s.muted[key] = true
	s.mu.Lock()
	err := s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	reopened := newProposalStore(stateDir)
	if len(reopened.items) != 2 || reopened.items[0].ID != "prop_roundtrip" || !reopened.items[0].Prepared {
		t.Fatalf("proposal round trip failed: %#v", reopened.items)
	}
	if reopened.items[1].ID != "prop_accepted" || reopened.items[1].Status != proactiveStatusAccepted {
		t.Fatalf("accepted status round trip failed: %#v", reopened.items[1])
	}
	if len(reopened.items[0].Evidence) != 1 || reopened.items[0].Evidence[0].Path != "auth/auth_test.go" {
		t.Fatalf("evidence round trip failed: %#v", reopened.items[0].Evidence)
	}
	if !reopened.muted[key] || !reopened.lastFired[key].Equal(now) {
		t.Fatalf("mute/cooldown round trip failed: muted=%v fired=%v", reopened.muted[key], reopened.lastFired[key])
	}
}

func TestProposalPersistenceFailureSuppressesPublicationAndRollsBackCooldown(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, task, tr := proactiveBuildSession(t, d, ws)

	blockedPath := filepath.Join(t.TempDir(), "occupied")
	if err := os.Mkdir(blockedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	d.proposals.path = blockedPath
	seed := &proposal{Title: "Prepare tests", Why: "test", Done: "test", Propose: "test", Risk: "read-only"}
	d.emitProposal(sess, task, proactiveClassModule, seed, tr)
	if got := d.pendingProposalCount(sess.SessionID); got != 0 {
		t.Fatalf("persistence failure published %d proposal(s)", got)
	}

	d.proposals.path = filepath.Join(t.TempDir(), "proposals.json")
	d.emitProposal(sess, task, proactiveClassModule, seed, tr)
	if got := d.pendingProposalCount(sess.SessionID); got != 1 {
		t.Fatalf("failed write left a cooldown behind: pending=%d", got)
	}
}

func TestProposalStoreQuarantinesFutureVersion(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "proposals.json")
	raw := []byte(`{"version":2,"items":[]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	store := newProposalStore(stateDir)
	if len(store.items) != 0 {
		t.Fatalf("future store loaded items: %#v", store.items)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("future store was not quarantined: %v", err)
	}
	matches, err := filepath.Glob(path + ".v2.*.quarantine")
	if err != nil || len(matches) != 1 {
		t.Fatalf("future store quarantine files = %v, err=%v", matches, err)
	}
	got, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("quarantine changed bytes: got %q want %q", got, raw)
	}
}

func TestProactiveIgnoreAndMuteSurviveDaemonRestart(t *testing.T) {
	t.Setenv("CARINA_PROACTIVE", "1")
	stateDir := t.TempDir()
	ws := t.TempDir()
	d1 := newDaemonAt(t, stateDir)
	sess, task, tr := proactiveBuildSession(t, d1, ws)
	proactiveTouchModule(d1, sess, task, tr)
	waitForPendingProposals(t, d1, sess.SessionID, 1)
	listed, err := d1.handleProposalList(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	id := firstProposalID(t, listed)
	if _, err := d1.handleProposalIgnore(mustJSON(t, map[string]any{"proposal_id": id, "mute": true})); err != nil {
		t.Fatal(err)
	}
	_ = d1.Close()

	d2 := newDaemonAt(t, stateDir)
	defer d2.Close()
	reloaded, err := d2.handleProposalList(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	body := reloaded.(map[string]any)
	rows := body["proposals"].([]*proposal)
	if body["pending"].(int) != 0 || len(rows) != 1 || rows[0].Status != proactiveStatusIgnored {
		t.Fatalf("ignored proposal did not survive restart: %#v", body)
	}
	sess2, ok := d2.store.Get(sess.SessionID)
	if !ok {
		t.Fatal("session missing after restart")
	}
	d2.kern.InitSessionWithPolicy(sess2.SessionID, sess2.WorkspaceRoot, sess2.PermissionProfile, nil)
	task2 := d2.sched.SubmitWithGoalModelAgent(sess2.SessionID, sess2.WorkspaceID, "refactor auth again", "", "build", nil)
	proactiveTouchModule(d2, sess2, task2, newTranscript("refactor auth again"))
	time.Sleep(100 * time.Millisecond)
	if d2.pendingProposalCount(sess2.SessionID) != 0 {
		t.Fatal("persisted mute must suppress the same class after restart")
	}
}

func proactiveBuildSession(t *testing.T, d *Daemon, ws string) (*sessionstore.Session, *scheduler.ExecutionRun, *Transcript) {
	t.Helper()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "refactor auth", "", "build", nil)
	return sess, task, newTranscript("refactor auth")
}

func proactiveTouchModule(d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, tr *Transcript) {
	for _, path := range []string{"auth/a.go", "auth/b.go", "auth/c.go"} {
		d.considerProactive(sess, task, &action{Tool: "read", Path: path}, toolCompleted("ok"), 1, tr)
	}
}

func waitForPendingProposals(t *testing.T, d *Daemon, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := d.pendingProposalCount(sessionID); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pending proposals = %d, want %d", d.pendingProposalCount(sessionID), want)
}

func firstProposalID(t *testing.T, raw any) string {
	t.Helper()
	body := raw.(map[string]any)
	rows, _ := body["proposals"].([]*proposal)
	if len(rows) == 0 {
		encoded, _ := json.Marshal(body["proposals"])
		t.Fatalf("no proposals: %s", encoded)
	}
	return rows[0].ID
}
