package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Nebutra/carina/go/kernel"
)

// TestParseCandidateEnvelope covers the strict-JSON candidate contract:
// well-formed envelopes parse; anything else is a hard error (never silently
// treated as "no changes").
func TestParseCandidateEnvelope(t *testing.T) {
	files, rationale, err := parseCandidateEnvelope(`{"files":[{"path":"a.go","new_content":"package a"}],"rationale":"why"}`)
	if err != nil {
		t.Fatalf("valid envelope should parse: %v", err)
	}
	if len(files) != 1 || files[0].Path != "a.go" || files[0].NewContent != "package a" || rationale != "why" {
		t.Fatalf("unexpected parse result: %+v %q", files, rationale)
	}

	// fenced markdown should still parse
	fenced := "```json\n{\"files\":[{\"path\":\"b.go\",\"new_content\":\"x\"}],\"rationale\":\"r\"}\n```"
	if _, _, err := parseCandidateEnvelope(fenced); err != nil {
		t.Fatalf("fenced envelope should parse: %v", err)
	}

	for _, bad := range []string{
		"",
		"not json at all",
		`{"files":[]}`,                    // empty files
		`{"files":[{"new_content":"x"}]}`, // missing path
		`{"rationale":"no files key"}`,    // missing files
	} {
		if _, _, err := parseCandidateEnvelope(bad); err == nil {
			t.Fatalf("malformed envelope %q should error", bad)
		}
	}
}

// candidateReasoner plays a scripted candidate-drafter turn: on its first
// turn it emits "done" with a fixed envelope (or garbage for invalid
// candidates); if asked to judge (prompt contains the judge marker), it
// replies with a fixed verdict.
type candidateReasoner struct {
	mu          sync.Mutex
	nextByAgent map[string]string // "" key = default; keyed loosely by content of the prompt

	// judge behavior
	judgeWinner    int
	judgeRationale string
	judgeMalformed bool
	judgeErr       error

	// candidate outputs in call order (round-robins across concurrent goroutines
	// using an index counter keyed by prompt content is unreliable, so instead
	// candidates are identified by a marker embedded in the task text).
	candidateEnvelopes map[string]string // marker substring -> raw "done" summary content

	// readFirst, when set for a marker, scripts that candidate's FIRST turn as
	// a real "read" tool call (so its child session records genuine
	// write-provenance via the gated read path) before its second turn
	// returns the "done" envelope from candidateEnvelopes. Used to test the
	// write-provenance drift check, which only has anything to compare
	// against once a candidate has actually read a file.
	readFirst map[string]string // marker -> relative path to read on turn 1
	// onSecondTurn, when set for a marker, runs immediately before that
	// candidate's second (post-read) turn response is returned — used to
	// deterministically simulate a concurrent write landing on disk between
	// when the candidate read a file and when the orchestrator submits the
	// winner, without depending on real goroutine timing.
	onSecondTurn map[string]func()
	callCount    map[string]int // marker -> candidate turns served so far
}

func (r *candidateReasoner) Name() string { return "candidate-test-reasoner" }

func (r *candidateReasoner) Think(_ context.Context, prompt string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(prompt, bestOfNJudgePrompt) {
		if r.judgeErr != nil {
			return "", r.judgeErr
		}
		if r.judgeMalformed {
			return "not json", nil
		}
		v := bestOfNJudgeVerdict{WinnerIndex: r.judgeWinner, Rationale: r.judgeRationale}
		b, _ := json.Marshal(v)
		return string(b), nil
	}
	// Candidate-drafter turn: find which variant this is via the marker we
	// embedded in the task text, and return its scripted response.
	for marker, summary := range r.candidateEnvelopes {
		if strings.Contains(prompt, marker) {
			if r.callCount == nil {
				r.callCount = map[string]int{}
			}
			r.callCount[marker]++
			if path, ok := r.readFirst[marker]; ok && r.callCount[marker] == 1 {
				b, _ := json.Marshal(map[string]string{"tool": "read", "path": path})
				return string(b), nil
			}
			if hook, ok := r.onSecondTurn[marker]; ok {
				hook()
			}
			b, _ := json.Marshal(map[string]string{"tool": "done", "summary": summary})
			return string(b), nil
		}
	}
	// Fallback: finish with an empty/invalid envelope.
	b, _ := json.Marshal(map[string]string{"tool": "done", "summary": "no envelope"})
	return string(b), nil
}

func candidateMarker(i int) string { return "variant " + strconv.Itoa(i+1) + " of" }

// newBestOfNDaemon is like newLoopDaemon but additionally points ToolsDir at
// a real carina-patch-native binary, needed only by the tests in this file
// that exercise a real PatchApply (winner submission). repoRootFromHere
// resolves inside this worktree, which may not have a built zig/zig-out —
// fall back to CARINA_ZIG_TOOLS_DIR (test-only convenience env var; not a
// daemon-recognized setting) when the in-worktree one is missing.
func newBestOfNDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	toolsDir := filepath.Join(repoRoot, "zig/zig-out/bin")
	if _, err := os.Stat(filepath.Join(toolsDir, "carina-patch-native")); err != nil {
		if alt := os.Getenv("CARINA_ZIG_TOOLS_DIR"); alt != "" {
			if _, err := os.Stat(filepath.Join(alt, "carina-patch-native")); err == nil {
				toolsDir = alt
			}
		}
	}
	if _, err := os.Stat(filepath.Join(toolsDir, "carina-patch-native")); err != nil {
		t.Skip("carina-patch-native not built (set CARINA_ZIG_TOOLS_DIR)")
	}
	ws := t.TempDir()
	d, err := New(Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: toolsDir, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	return d, ws
}

// TestBestOfN_WinnerOnlyPatchApplied is the core governance assertion (AC2):
// running best_of_n with N candidates results in exactly one applied patch
// (the judge's winner) and the discarded candidates never reach
// kernel.patch.propose.
func TestBestOfN_WinnerOnlyPatchApplied(t *testing.T) {
	d, ws := newBestOfNDaemon(t)
	defer d.Close()
	d.bestOfNEnabled.Store(true)

	reasoner := &candidateReasoner{
		judgeWinner:    1,
		judgeRationale: "candidate 1 is more correct",
		candidateEnvelopes: map[string]string{
			candidateMarker(0): `{"files":[{"path":"out.txt","new_content":"from candidate 0"}],"rationale":"c0"}`,
			candidateMarker(1): `{"files":[{"path":"out.txt","new_content":"from candidate 1"}],"rationale":"c1"}`,
		},
	}
	d.SetReasoner(reasoner)

	sess, err := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "write out.txt")

	act := &action{Tool: "best_of_n", Task: "write out.txt with some content", N: 2}
	outcome := d.executeBestOfNOutcome(sess, task, act)
	if outcome.status != "completed" {
		t.Fatalf("expected completed, got status=%s display=%s", outcome.status, outcome.display)
	}
	if !strings.Contains(outcome.display, "winner=candidate 1") {
		t.Fatalf("expected winner=candidate 1 in display, got: %s", outcome.display)
	}

	got, rerr := readWorkspaceFile(ws, "out.txt")
	if rerr != nil {
		t.Fatalf("expected out.txt to exist after apply: %v", rerr)
	}
	if got != "from candidate 1" {
		t.Fatalf("expected winner's content on disk, got %q", got)
	}
}

// TestBestOfN_WinnerOverwritesExistingFileItActuallyRead is the write-
// provenance happy path for an EXISTING file (TestBestOfN_WinnerOnlyPatchApplied
// only covers a brand-new file, which never touches checkWriteProvenance at
// all): the winning candidate reads existing.txt via the gated "read" tool
// before drafting, nothing else touches the file, and submission must
// succeed with the winner's content on disk.
func TestBestOfN_WinnerOverwritesExistingFileItActuallyRead(t *testing.T) {
	d, ws := newBestOfNDaemon(t)
	defer d.Close()
	d.bestOfNEnabled.Store(true)

	if err := os.WriteFile(filepath.Join(ws, "existing.txt"), []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}

	reasoner := &candidateReasoner{
		judgeWinner:    0,
		judgeRationale: "candidate 0 wins",
		readFirst:      map[string]string{candidateMarker(0): "existing.txt"},
		candidateEnvelopes: map[string]string{
			candidateMarker(0): `{"files":[{"path":"existing.txt","new_content":"from candidate 0, post-read"}],"rationale":"c0"}`,
			candidateMarker(1): `{"files":[{"path":"existing.txt","new_content":"from candidate 1"}],"rationale":"c1"}`,
		},
	}
	d.SetReasoner(reasoner)

	sess, err := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit existing.txt")

	act := &action{Tool: "best_of_n", Task: "edit existing.txt", N: 2}
	outcome := d.executeBestOfNOutcome(sess, task, act)
	if outcome.status != "completed" {
		t.Fatalf("expected completed when the winner actually read the file first, got status=%s display=%s", outcome.status, outcome.display)
	}
	got, rerr := readWorkspaceFile(ws, "existing.txt")
	if rerr != nil {
		t.Fatalf("existing.txt should still exist: %v", rerr)
	}
	if got != "from candidate 0, post-read" {
		t.Fatalf("expected winner's content on disk, got %q", got)
	}
}

// TestBestOfN_RefusesWinnerWhenFileDriftedDuringGeneration is the regression
// test for the write-provenance bypass a security review found: the winning
// candidate reads existing.txt, but the file is modified on disk (simulating
// a concurrent writer) after that read and before the orchestrator submits
// the winner — exactly the multi-minute N-way parallel candidate-generation
// window where this matters most. The old code self-seeded provenance from
// whatever was on disk at submission time, silently clobbering the drift.
// The fix must detect the mismatch and refuse, leaving the drifted content
// (not the winner's stale-based patch) on disk.
func TestBestOfN_RefusesWinnerWhenFileDriftedDuringGeneration(t *testing.T) {
	d, ws := newBestOfNDaemon(t)
	defer d.Close()
	d.bestOfNEnabled.Store(true)

	if err := os.WriteFile(filepath.Join(ws, "existing.txt"), []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}

	reasoner := &candidateReasoner{
		judgeWinner:    0,
		judgeRationale: "candidate 0 wins",
		readFirst:      map[string]string{candidateMarker(0): "existing.txt"},
		onSecondTurn: map[string]func(){
			// Simulate a concurrent writer landing between the winning
			// candidate's read and the orchestrator's submission.
			candidateMarker(0): func() {
				_ = os.WriteFile(filepath.Join(ws, "existing.txt"), []byte("concurrently modified by someone else"), 0o644)
			},
		},
		candidateEnvelopes: map[string]string{
			candidateMarker(0): `{"files":[{"path":"existing.txt","new_content":"from candidate 0, based on stale read"}],"rationale":"c0"}`,
			candidateMarker(1): `{"files":[{"path":"existing.txt","new_content":"from candidate 1"}],"rationale":"c1"}`,
		},
	}
	d.SetReasoner(reasoner)

	sess, err := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit existing.txt")

	act := &action{Tool: "best_of_n", Task: "edit existing.txt", N: 2}
	outcome := d.executeBestOfNOutcome(sess, task, act)
	if outcome.status == "completed" {
		t.Fatalf("expected refusal when the file drifted during candidate generation, got completed: %+v", outcome)
	}
	if outcome.errorCategory != "write_provenance_stale" {
		t.Fatalf("expected errorCategory=write_provenance_stale, got %q (display=%s)", outcome.errorCategory, outcome.display)
	}
	got, rerr := readWorkspaceFile(ws, "existing.txt")
	if rerr != nil {
		t.Fatalf("existing.txt should still exist: %v", rerr)
	}
	if got != "concurrently modified by someone else" {
		t.Fatalf("the drifted content must survive untouched — the stale winner must never be written; got %q", got)
	}
}

// TestHeuristicBestOfNWinnerPrefersVerifiedPassingCandidate is a direct unit
// test of the no-judge-model fallback: candidate 0 has the smaller diff (the
// plain size-proxy heuristic would normally pick it) but failed
// verification; candidate 1 is larger but passed. The winner must be
// candidate 1 — real pass/fail data beats the size proxy. (This path is only
// reachable directly, not through a full executeBestOfNOutcome run, because
// the candidate-drafter subagents themselves need a working reasoner, which
// then also satisfies judgeBestOfN's "judge := d.judgeReasoner; if nil, judge
// = d.reasoner" fallback before it would ever fall through to the heuristic.)
func TestHeuristicBestOfNWinnerPrefersVerifiedPassingCandidate(t *testing.T) {
	candidates := []bestOfNCandidate{
		{Index: 0, Valid: true, Files: []kernel.FileChange{{Path: "out.txt", NewContent: "short"}}, Verify: candidateVerification{Ran: true, Passed: false, Output: "exit=1"}},
		{Index: 1, Valid: true, Files: []kernel.FileChange{{Path: "out.txt", NewContent: "a much longer candidate body"}}, Verify: candidateVerification{Ran: true, Passed: true, Output: "exit=0"}},
	}
	winner, why := heuristicBestOfNWinner(candidates)
	if winner.Index != 1 {
		t.Fatalf("expected candidate 1 (verify=PASS) to win over the smaller-diff candidate 0 (verify=FAIL), got candidate %d (%s)", winner.Index, why)
	}
	if !strings.Contains(why, "PASS") {
		t.Fatalf("expected the heuristic explanation to mention verify=PASS preference, got: %q", why)
	}

	// Sanity: with no verification data at all, the original smallest-diff
	// behavior is unchanged.
	unverified := []bestOfNCandidate{
		{Index: 0, Valid: true, Files: []kernel.FileChange{{Path: "out.txt", NewContent: "short"}}},
		{Index: 1, Valid: true, Files: []kernel.FileChange{{Path: "out.txt", NewContent: "a much longer candidate body"}}},
	}
	winner, _ = heuristicBestOfNWinner(unverified)
	if winner.Index != 0 {
		t.Fatalf("with no verification data, expected the original smallest-diff heuristic (candidate 0), got candidate %d", winner.Index)
	}
}

// TestBuildBestOfNJudgePromptIncludesVerifyResults is a direct unit test
// confirming the judge model actually receives real verify pass/fail data
// (not just diff+rationale) when it's available.
func TestBuildBestOfNJudgePromptIncludesVerifyResults(t *testing.T) {
	candidates := []bestOfNCandidate{
		{Index: 0, Rationale: "r0", Verify: candidateVerification{Ran: true, Passed: false, Output: "exit=1\nassertion failed"}},
		{Index: 1, Rationale: "r1", Verify: candidateVerification{Ran: true, Passed: true, Output: "exit=0\nok"}},
		{Index: 2, Rationale: "r2"}, // no verification ran
	}
	prompt := buildBestOfNJudgePrompt("do the thing", candidates)
	if !strings.Contains(prompt, "candidate_index 0") || !strings.Contains(prompt, "FAIL") || !strings.Contains(prompt, "assertion failed") {
		t.Fatalf("expected candidate 0's FAIL verdict and output in the prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "candidate_index 1") || !strings.Contains(prompt, "PASS") {
		t.Fatalf("expected candidate 1's PASS verdict in the prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "candidate_index 2") || !strings.Contains(prompt, "SKIPPED") {
		t.Fatalf("expected candidate 2 to be marked SKIPPED (no verify command) in the prompt:\n%s", prompt)
	}
}

// TestBestOfN_VerifyCommandRunsAgainstIsolatedScratchCopy is the end-to-end
// wiring test: a real "command" field actually executes against each valid
// candidate, the judge sees and can act on the result, and — critically —
// the real workspace file is never touched by verification itself (only by
// the final winner's governed patch apply).
func TestBestOfN_VerifyCommandRunsAgainstIsolatedScratchCopy(t *testing.T) {
	d, ws := newBestOfNDaemon(t)
	defer d.Close()
	d.bestOfNEnabled.Store(true)

	// Seed the real workspace with content that would fail the verify
	// command, so if verification ever ran against the real file instead of
	// an isolated scratch copy, this would be detectable (and the test's
	// core assertion below — the real file is untouched pre-apply — would
	// need no special setup either way, but this makes the isolation claim
	// concrete rather than vacuous).
	if err := os.WriteFile(filepath.Join(ws, "out.txt"), []byte("REAL_WORKSPACE_ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	reasoner := &candidateReasoner{
		judgeWinner:    1,
		judgeRationale: "candidate 1 passed verification",
		readFirst:      map[string]string{candidateMarker(1): "out.txt"},
		candidateEnvelopes: map[string]string{
			candidateMarker(0): `{"files":[{"path":"out.txt","new_content":"no marker here"}],"rationale":"c0"}`,
			candidateMarker(1): `{"files":[{"path":"out.txt","new_content":"contains PASS_MARKER"}],"rationale":"c1"}`,
		},
	}
	d.SetReasoner(reasoner)
	d.SetJudgeReasoner(reasoner) // force the model-judge path so judgeWinner is honored deterministically

	sess, err := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "write out.txt")

	act := &action{Tool: "best_of_n", Task: "write out.txt", N: 2, Command: []string{"grep", "-q", "PASS_MARKER", "out.txt"}}
	outcome := d.executeBestOfNOutcome(sess, task, act)
	if outcome.status != "completed" {
		t.Fatalf("expected completed, got status=%s display=%s", outcome.status, outcome.display)
	}
	if !strings.Contains(outcome.display, "winner=candidate 1") || !strings.Contains(outcome.display, "verify: PASS") {
		t.Fatalf("expected winner=candidate 1 (verify: PASS), got: %s", outcome.display)
	}
	got, rerr := readWorkspaceFile(ws, "out.txt")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if got != "contains PASS_MARKER" {
		t.Fatalf("expected the judge-selected winner's content on disk, got %q", got)
	}
}

// TestBestOfN_NoVerifyCommandSkipsVerification confirms the feature is fully
// opt-in: without a "command" field, behavior is unchanged from before
// verification existed (winner.Verify.Ran is false, no scratch workspace is
// ever materialized).
func TestBestOfN_NoVerifyCommandSkipsVerification(t *testing.T) {
	d, ws := newBestOfNDaemon(t)
	defer d.Close()
	d.bestOfNEnabled.Store(true)

	reasoner := &candidateReasoner{
		judgeWinner:    0,
		judgeRationale: "candidate 0",
		candidateEnvelopes: map[string]string{
			candidateMarker(0): `{"files":[{"path":"out.txt","new_content":"c0"}],"rationale":"c0"}`,
			candidateMarker(1): `{"files":[{"path":"out.txt","new_content":"c1"}],"rationale":"c1"}`,
		},
	}
	d.SetReasoner(reasoner)

	sess, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "write out.txt")

	act := &action{Tool: "best_of_n", Task: "write out.txt", N: 2} // no Command
	outcome := d.executeBestOfNOutcome(sess, task, act)
	if outcome.status != "completed" {
		t.Fatalf("expected completed, got status=%s display=%s", outcome.status, outcome.display)
	}
	if !strings.Contains(outcome.display, "verify: skipped") {
		t.Fatalf("expected verify: skipped when no command is supplied, got: %s", outcome.display)
	}
}

// TestMaterializeCandidateWorkspaceIsolatesFromRealWorkspace is a direct
// unit test of the scratch-copy primitive: it must skip build-artifact/VCS
// directories, apply the candidate's file changes on top of a real copy, and
// never write back into the real workspace root.
func TestMaterializeCandidateWorkspaceIsolatesFromRealWorkspace(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(root, "node_modules", "pkg", "big.js"), []byte("should not be copied"), 0o644)
	os.WriteFile(filepath.Join(root, "keep.txt"), []byte("original"), 0o644)

	scratch, cleanup, ok, reason, err := materializeCandidateWorkspace(root, []kernel.FileChange{
		{Path: "keep.txt", NewContent: "candidate content"},
		{Path: "new.txt", NewContent: "brand new file"},
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true, got skip reason: %s", reason)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(scratch, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("node_modules should have been skipped, not copied into the scratch workspace")
	}
	got, err := os.ReadFile(filepath.Join(scratch, "keep.txt"))
	if err != nil || string(got) != "candidate content" {
		t.Fatalf("keep.txt in scratch should have the candidate's overwritten content, got %q, err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(scratch, "new.txt"))
	if err != nil || string(got) != "brand new file" {
		t.Fatalf("new.txt should have been created in scratch, got %q, err=%v", got, err)
	}
	// The real workspace must be completely untouched.
	realGot, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil || string(realGot) != "original" {
		t.Fatalf("real workspace keep.txt must be untouched, got %q, err=%v", realGot, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("new.txt must never be created in the real workspace by verification")
	}
}

// TestBestOfN_ZeroValidCandidatesFailsClosed: if every candidate fails to
// produce a parseable envelope, best_of_n must fail closed (never apply
// anything, never pick a candidate implicitly) — AC3.
func TestBestOfN_ZeroValidCandidatesFailsClosed(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.bestOfNEnabled.Store(true)
	// No candidateEnvelopes configured => every candidate finishes with the
	// unparseable fallback "no envelope" summary.
	d.SetReasoner(&candidateReasoner{})

	sess, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "write out.txt")

	act := &action{Tool: "best_of_n", Task: "write out.txt", N: 2}
	outcome := d.executeBestOfNOutcome(sess, task, act)
	if outcome.status == "completed" {
		t.Fatalf("expected non-completed outcome when all candidates are invalid, got: %+v", outcome)
	}
	if _, err := readWorkspaceFile(ws, "out.txt"); err == nil {
		t.Fatal("out.txt must not exist — no candidate should have been applied")
	}
}

// TestBestOfN_MalformedJudgeFailsClosed: a malformed judge reply must not
// fall through to picking a candidate implicitly — AC3.
func TestBestOfN_MalformedJudgeFailsClosed(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.bestOfNEnabled.Store(true)
	reasoner := &candidateReasoner{
		judgeMalformed: true,
		candidateEnvelopes: map[string]string{
			candidateMarker(0): `{"files":[{"path":"out.txt","new_content":"c0"}],"rationale":"c0"}`,
			candidateMarker(1): `{"files":[{"path":"out.txt","new_content":"c1"}],"rationale":"c1"}`,
		},
	}
	d.SetReasoner(reasoner)
	d.SetJudgeReasoner(reasoner) // force the model-judge path (not the no-judge heuristic)

	sess, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "write out.txt")

	act := &action{Tool: "best_of_n", Task: "write out.txt", N: 2}
	outcome := d.executeBestOfNOutcome(sess, task, act)
	if outcome.status == "completed" {
		t.Fatalf("malformed judge output must fail closed, got: %+v", outcome)
	}
	if _, err := readWorkspaceFile(ws, "out.txt"); err == nil {
		t.Fatal("out.txt must not exist — malformed judge output must never result in an apply")
	}
}

// TestBestOfN_DisabledByDefault: the feature is off unless explicitly
// enabled — AC5.
func TestBestOfN_DisabledByDefault(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	// d.bestOfNEnabled defaults to false (zero value) — do NOT enable it.
	d.SetReasoner(&candidateReasoner{})

	sess, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "write out.txt")

	act := &action{Tool: "best_of_n", Task: "write out.txt", N: 2}
	outcome := d.executeBestOfNOutcome(sess, task, act)
	if outcome.status != "denied" || outcome.errorCategory != "feature_disabled" {
		t.Fatalf("expected denied/feature_disabled by default, got status=%s category=%s", outcome.status, outcome.errorCategory)
	}
}

// TestBestOfN_DeniedInsideSubagent: best_of_n must never be reachable from
// inside a subagent (no relaxation of the no-respawn guard) — AC1.
func TestBestOfN_DeniedInsideSubagent(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.bestOfNEnabled.Store(true)
	d.SetReasoner(&candidateReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	child, err := d.store.CreateSubSession(ws, "read-only", "on_request", parent.SessionID, parent.Depth+1)
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionFull(child.SessionID, ws, "read-only", "on_request", nil)
	childTask := d.sched.Submit(child.SessionID, child.WorkspaceID, "nested best_of_n attempt")

	act := &action{Tool: "best_of_n", Task: "write out.txt", N: 2}
	outcome := d.executeBestOfNOutcome(child, childTask, act)
	if outcome.status != "denied" || outcome.errorCategory != "depth_limit" {
		t.Fatalf("expected denied/depth_limit from inside a subagent, got status=%s category=%s display=%s", outcome.status, outcome.errorCategory, outcome.display)
	}
}

// TestBestOfN_CandidateCannotCallPatch verifies the tool-restriction
// guardrail directly: a candidate-drafter session (as spawnSubagentContext
// sets it up) has "patch" in its restricted-tools set and dispatching a
// patch action for that session is denied before it ever reaches
// agentPatchOutcome / kernel.patch.propose.
func TestBestOfN_CandidateCannotCallPatch(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, _ := d.store.CreateSessionMode(ws, "read-only", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "read-only", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "candidate task")

	spec := builtinAgentSpecs()["candidate-drafter"]
	if spec == nil || !spec.RestrictedTools["patch"] {
		t.Fatal("candidate-drafter spec must restrict the patch tool")
	}
	d.restrictedTools.Store(sess.SessionID, spec.RestrictedTools)
	defer d.restrictedTools.Delete(sess.SessionID)

	outcome := d.dispatchActionOutcome(sess, task, &action{Tool: "patch", Path: "x.txt", Content: "should never land"})
	if outcome.status != "denied" || outcome.errorCategory != "tool_restricted" {
		t.Fatalf("expected denied/tool_restricted, got status=%s category=%s", outcome.status, outcome.errorCategory)
	}
	if _, err := readWorkspaceFile(ws, "x.txt"); err == nil {
		t.Fatal("restricted patch call must never write to disk")
	}
}

func readWorkspaceFile(ws, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(ws, rel))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
