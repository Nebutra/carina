package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/contextengine"
)

type stubContextEngine struct {
	response contextengine.CompressResponse
	err      error
	calls    int
	status   contextengine.Status
}

func (s *stubContextEngine) Compress(context.Context, contextengine.CompressRequest) (contextengine.CompressResponse, error) {
	s.calls++
	return s.response, s.err
}
func (s *stubContextEngine) Retrieve(context.Context, string) (contextengine.RetrieveResponse, error) {
	return contextengine.RetrieveResponse{}, errors.New("unavailable")
}
func (s *stubContextEngine) Stats(context.Context) (contextengine.Stats, error) {
	return contextengine.Stats{}, nil
}
func (s *stubContextEngine) Status() contextengine.Status { return s.status }
func (s *stubContextEngine) Doctor() map[string]any       { return map[string]any{"ok": s.err == nil} }
func (s *stubContextEngine) Close() error                 { return nil }

func TestCompressObservationPreservesReversibleMetadataAndAudit(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")
	engine := &stubContextEngine{
		status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom},
		response: contextengine.CompressResponse{
			Content: "summary", OriginalRef: "ccr_abc", OriginalSHA256: strings.Repeat("a", 64),
			OriginalBytes: 31, CompressedBytes: 7, Ratio: 0.25, Engine: contextengine.ModeHeadroom,
			OriginalTokens: 10, CompressedTokens: 3, SavingsPercent: 70, Transforms: []string{"smart_crusher"},
		},
	}
	d.contextEng = engine

	const original = "sensitive original tool output"
	tr := newTranscript(task.UserPrompt)
	tr.policy.MaxChars = 1 // force over-budget so the trigger fires and Headroom is actually called
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read prior", Obs: Observation{Content: "prior turn content pushes size() over the 1-char triggerChars() budget"}})
	obs, err := d.compressObservation(context.Background(), sess, task, tr, 2, "read", original, false)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Content != "summary" || obs.OriginalRef != "ccr_abc" || obs.OriginalSHA256 == "" || obs.CompressionEngine != contextengine.ModeHeadroom {
		t.Fatalf("compression metadata missing: %+v", obs)
	}
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read x", Obs: obs})
	d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: 2, Transcript: tr})
	reloaded := d.runs.loadCheckpoint(task.TaskID)
	if reloaded == nil || len(reloaded.Transcript.Turns) != 2 || reloaded.Transcript.Turns[1].Obs.OriginalRef != "ccr_abc" {
		t.Fatalf("checkpoint lost reversible metadata: %+v", reloaded)
	}
	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	if !strings.Contains(log, "context_compressed") || !strings.Contains(log, "ccr_abc") || !strings.Contains(log, obs.OriginalSHA256) {
		t.Fatalf("compression audit metadata missing: %s", log)
	}
	if strings.Contains(log, original) {
		t.Fatal("ContextCompressed audit event leaked raw original content")
	}
}

func TestCompressObservationSkipsPinnedContent(t *testing.T) {
	engine := &stubContextEngine{status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom}}
	d := &Daemon{contextEng: engine}
	obs, err := d.compressObservation(context.Background(), nil, nil, nil, 1, "run", "failing test output", true)
	if err != nil || obs.Content != "failing test output" || !obs.Pinned {
		t.Fatalf("pinned observation changed: obs=%+v err=%v", obs, err)
	}
	if engine.calls != 0 {
		t.Fatal("pinned observation reached context engine")
	}
}

func TestCompressObservationFailureIsAuditedAndCircuitBreaks(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")
	d.contextEng = &stubContextEngine{
		err:    errors.New("sidecar failed"),
		status: contextengine.Status{ConfiguredEngine: contextengine.ModeHeadroom, EffectiveEngine: contextengine.ModeHeadroom},
	}
	const original = "raw original must stay out of new audit event"
	tr := newTranscript(task.UserPrompt)
	tr.policy.MaxChars = 1 // force over-budget so the trigger fires and Headroom is actually called
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read prior", Obs: Observation{Content: "prior turn content pushes size() over the 1-char triggerChars() budget"}})
	for i := 1; i <= 4; i++ {
		obs, err := d.compressObservation(context.Background(), sess, task, tr, i, "read", original, false)
		if err != nil || obs.Content != original {
			t.Fatalf("failure must preserve original: %+v %v", obs, err)
		}
	}
	if engine := d.contextEng.(*stubContextEngine); engine.calls != 3 {
		t.Fatalf("circuit did not stop retries: calls=%d", engine.calls)
	}
	raw, _ := d.kern.ReadEvents(sess.SessionID)
	if !strings.Contains(string(raw), "context_engine_failed") || !strings.Contains(string(raw), "context_compaction_circuit_open") || strings.Contains(string(raw), original) {
		t.Fatalf("failure audit is missing or leaked content: %s", raw)
	}
}

// TestAgentPromptSkipsCompressionUnderBudget covers the new end-to-end
// behavior: newTranscript's default policy (MaxChars=24000) means a single
// short observation early in a task never crosses tr.triggerChars(), so the
// real agent loop must NOT pay the Headroom round trip for it — the raw
// (here, an ordinary toolchain-error string) observation reaches the model
// unchanged. The compress-when-over-budget path itself is covered directly,
// without going through the full loop's turn/loop-guard budgets, by
// TestCompressObservationCompressesOverBudget and
// TestCompressObservationTriggerMatchesTranscriptCompact above.
func TestAgentPromptSkipsCompressionUnderBudget(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	engine := &stubContextEngine{
		status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom},
		response: contextengine.CompressResponse{
			Content: "HEADROOM COMPRESSED FILE LIST", OriginalRef: "ccr_list", OriginalSHA256: strings.Repeat("b", 64),
			OriginalBytes: 100, CompressedBytes: 29, Engine: contextengine.ModeHeadroom,
		},
	}
	d.contextEng = engine
	reasoner := &promptRecordingReasoner{steps: []string{
		`{"tool":"list"}`,
		`{"tool":"done","summary":"ok"}`,
	}}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect files")
	d.runTask(sess, task)
	if len(reasoner.prompts) < 2 {
		t.Fatalf("reasoner prompts = %d, want at least 2", len(reasoner.prompts))
	}
	second := reasoner.prompts[1]
	if strings.Contains(second, "HEADROOM COMPRESSED FILE LIST") {
		t.Fatalf("second model prompt was compressed despite being under the transcript char budget:\n%s", second)
	}
	if engine.calls != 0 {
		t.Fatalf("under-budget observation reached the context engine: calls=%d", engine.calls)
	}
}

// TestSubagentPromptSkipsCompressionUnderBudget is the subagent counterpart
// of TestAgentPromptSkipsCompressionUnderBudget.
func TestSubagentPromptSkipsCompressionUnderBudget(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	engine := &stubContextEngine{
		status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom},
		response: contextengine.CompressResponse{
			Content: "HEADROOM COMPRESSED SUBAGENT LIST", OriginalRef: "ccr_subagent", OriginalSHA256: strings.Repeat("c", 64),
			OriginalBytes: 100, CompressedBytes: 34, Engine: contextengine.ModeHeadroom,
		},
	}
	d.contextEng = engine
	reasoner := &promptRecordingReasoner{steps: []string{
		`{"tool":"list"}`,
		`{"tool":"done","summary":"ok"}`,
	}}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(workspace, "read-only")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "read-only", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect files")

	summary := d.runSubagentLoop(sess, task, &AgentSpec{Name: "scout", SystemPrompt: "Inspect files.", MaxTurns: 2})
	if summary != "ok" {
		t.Fatalf("subagent summary = %q, want ok", summary)
	}
	if len(reasoner.prompts) < 2 {
		t.Fatalf("reasoner prompts = %d, want at least 2", len(reasoner.prompts))
	}
	second := reasoner.prompts[1]
	if strings.Contains(second, "HEADROOM COMPRESSED SUBAGENT LIST") {
		t.Fatalf("second subagent prompt was compressed despite being under the transcript char budget:\n%s", second)
	}
	if engine.calls != 0 {
		t.Fatalf("under-budget observation reached the context engine: calls=%d", engine.calls)
	}
}

// TestCompressObservationSkipsUnderBudget confirms the trigger added in this
// change: a transcript comfortably under its char budget never reaches the
// Headroom adapter, so a large MaxChars with short content skips compression
// entirely (the caller's addTurn hard-truncation remains the only backstop).
func TestCompressObservationSkipsUnderBudget(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")
	engine := &stubContextEngine{status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom}}
	d.contextEng = engine

	tr := newTranscript(task.UserPrompt) // default policy: MaxChars=24000
	const original = "short observation, nowhere near the char budget"
	obs, err := d.compressObservation(context.Background(), sess, task, tr, 1, "read", original, false)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Content != original {
		t.Fatalf("under-budget observation should stay raw, got %+v", obs)
	}
	if engine.calls != 0 {
		t.Fatalf("under-budget observation reached the context engine: calls=%d", engine.calls)
	}
}

// TestCompressObservationCompressesOverBudget is the positive counterpart:
// once tr.size() >= tr.triggerChars(), the same effective threshold
// Transcript.compact() uses, compression proceeds as before.
func TestCompressObservationCompressesOverBudget(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")
	engine := &stubContextEngine{
		status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom},
		response: contextengine.CompressResponse{
			Content: "summary", OriginalRef: "ccr_over", OriginalSHA256: strings.Repeat("d", 64),
			Engine: contextengine.ModeHeadroom,
		},
	}
	d.contextEng = engine

	tr := newTranscript(task.UserPrompt)
	tr.policy.MaxChars = 1 // guarantee tr.size() >= tr.triggerChars()
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read prior", Obs: Observation{Content: "prior turn content pushes size() over the 1-char triggerChars() budget"}})
	obs, err := d.compressObservation(context.Background(), sess, task, tr, 1, "read", "over-budget observation", false)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Content != "summary" || obs.OriginalRef != "ccr_over" {
		t.Fatalf("over-budget observation should be compressed, got %+v", obs)
	}
	if engine.calls != 1 {
		t.Fatalf("over-budget observation did not reach the context engine: calls=%d", engine.calls)
	}
}

// TestCompressObservationNilTranscriptCompresses confirms the documented
// fail-open default: a caller that cannot supply a live transcript (nil tr)
// still gets compression attempted rather than silently skipped.
func TestCompressObservationNilTranscriptCompresses(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")
	engine := &stubContextEngine{
		status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom},
		response: contextengine.CompressResponse{
			Content: "summary", OriginalRef: "ccr_nil", OriginalSHA256: strings.Repeat("e", 64),
			Engine: contextengine.ModeHeadroom,
		},
	}
	d.contextEng = engine

	obs, err := d.compressObservation(context.Background(), sess, task, nil, 1, "read", "some observation", false)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Content != "summary" || obs.OriginalRef != "ccr_nil" {
		t.Fatalf("nil transcript should fail open toward compressing, got %+v", obs)
	}
	if engine.calls != 1 {
		t.Fatalf("nil transcript did not reach the context engine: calls=%d", engine.calls)
	}
}

// TestCompressObservationTriggerMatchesTranscriptCompact is a regression
// tripwire (in the spirit of the a817f21 "both gates agree" test) ensuring
// compressObservation and Transcript.compact() key off the exact same
// tr.triggerChars() value for a non-default policy (ReserveChars/
// ThresholdRatio both set), so a future change to one call site's threshold
// math can't silently leave the other on stale semantics.
func TestCompressObservationTriggerMatchesTranscriptCompact(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")
	engine := &stubContextEngine{
		status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom},
		response: contextengine.CompressResponse{
			Content: "summary", OriginalRef: "ccr_tripwire", OriginalSHA256: strings.Repeat("f", 64),
			Engine: contextengine.ModeHeadroom,
		},
	}
	d.contextEng = engine

	tr := newTranscript(task.UserPrompt)
	tr.policy.MaxChars = 1000
	tr.policy.ReserveChars = 100 // trigger = max(900, 1000*ratio); ratio is 0 here so trigger=900
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read x", Obs: Observation{Content: strings.Repeat("a", 850)}})

	// tr.size() is just under triggerChars() (900): compact() must be a
	// no-op, and compressObservation must skip Headroom, at the identical
	// threshold.
	if tr.size() >= tr.triggerChars() {
		t.Fatalf("test fixture assumption broken: size=%d trigger=%d", tr.size(), tr.triggerChars())
	}
	if receipt := tr.compact(func(string) (string, error) { return "should not be called", nil }); receipt != nil {
		t.Fatalf("compact() fired below triggerChars(): %+v", receipt)
	}
	obs, err := d.compressObservation(context.Background(), sess, task, tr, 1, "read", "small observation", false)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Content != "small observation" {
		t.Fatalf("compressObservation fired below the same triggerChars() compact() respected: %+v", obs)
	}
	if engine.calls != 0 {
		t.Fatalf("compressObservation reached Headroom below triggerChars(): calls=%d", engine.calls)
	}
}
