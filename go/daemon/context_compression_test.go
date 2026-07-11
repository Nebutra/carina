package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	obs, err := d.compressObservation(context.Background(), sess, task, 2, "read", original, false)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Content != "summary" || obs.OriginalRef != "ccr_abc" || obs.OriginalSHA256 == "" || obs.CompressionEngine != contextengine.ModeHeadroom {
		t.Fatalf("compression metadata missing: %+v", obs)
	}
	tr := newTranscript(task.UserPrompt)
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read x", Obs: obs})
	d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: 2, Transcript: tr})
	reloaded := d.runs.loadCheckpoint(task.TaskID)
	if reloaded == nil || len(reloaded.Transcript.Turns) != 1 || reloaded.Transcript.Turns[0].Obs.OriginalRef != "ccr_abc" {
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
	obs, err := d.compressObservation(context.Background(), nil, nil, 1, "run", "failing test output", true)
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
	for i := 1; i <= 4; i++ {
		obs, err := d.compressObservation(context.Background(), sess, task, i, "read", original, false)
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

func TestAgentPromptUsesCompressedObservation(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	const rawName = "raw-only-secret-file.txt"
	if err := os.WriteFile(filepath.Join(workspace, rawName), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if !strings.Contains(second, "HEADROOM COMPRESSED FILE LIST") {
		t.Fatalf("second model prompt did not use compressed observation:\n%s", second)
	}
	if strings.Contains(second, rawName) {
		t.Fatalf("second model prompt still contained raw observation %q", rawName)
	}
}

func TestSubagentPromptUsesCompressedObservation(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	const rawName = "subagent-raw-only-secret.txt"
	if err := os.WriteFile(filepath.Join(workspace, rawName), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.contextEng = &stubContextEngine{
		status: contextengine.Status{EffectiveEngine: contextengine.ModeHeadroom},
		response: contextengine.CompressResponse{
			Content: "HEADROOM COMPRESSED SUBAGENT LIST", OriginalRef: "ccr_subagent", OriginalSHA256: strings.Repeat("c", 64),
			OriginalBytes: 100, CompressedBytes: 34, Engine: contextengine.ModeHeadroom,
		},
	}
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
	if !strings.Contains(second, "HEADROOM COMPRESSED SUBAGENT LIST") {
		t.Fatalf("second subagent prompt did not use compressed observation:\n%s", second)
	}
	if strings.Contains(second, rawName) {
		t.Fatalf("second subagent prompt still contained raw observation %q", rawName)
	}
}
