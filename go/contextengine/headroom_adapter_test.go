package contextengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeManagedMCP struct {
	schemas map[string]json.RawMessage
	outputs map[string]string
	err     error
	calls   []fakeManagedCall
}

type fakeManagedCall struct {
	server string
	tool   string
	args   map[string]any
}

func (f *fakeManagedMCP) ToolSchemas(string) (map[string]json.RawMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.schemas, nil
}

func (f *fakeManagedMCP) CallContext(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.calls = append(f.calls, fakeManagedCall{server: server, tool: tool, args: args})
	if f.err != nil {
		return "", f.err
	}
	return f.outputs[tool], nil
}

func headroomSchemas(retrieve bool) map[string]json.RawMessage {
	schemas := map[string]json.RawMessage{
		headroomCompressTool: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string"}},"required":["content"]}`),
		headroomStatsTool:    json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
	}
	if retrieve {
		schemas[headroomRetrieveTool] = json.RawMessage(`{"type":"object","properties":{"hash":{"type":"string"}},"required":["hash"]}`)
	}
	return schemas
}

func newAdapterTestManager(t *testing.T, mode string) *Manager {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "headroom")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{ContextEngine: mode, HeadroomBin: bin, CarinaStateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func attachAdapter(t *testing.T, m *Manager, adapter *fakeManagedMCP) {
	t.Helper()
	if err := m.AttachManagedMCP(adapter); err != nil {
		t.Fatal(err)
	}
	m.MarkManagedMCPConnected(nil)
}

func TestHeadroomManagedMCPCompressAndRetrieve(t *testing.T) {
	m := newAdapterTestManager(t, ModeHeadroom)
	adapter := &fakeManagedMCP{
		schemas: headroomSchemas(true),
		outputs: map[string]string{
			headroomCompressTool: `{"compressed":"summary","hash":"ccr_abc","original_tokens":10,"compressed_tokens":3,"tokens_saved":7,"savings_percent":70,"transforms":["smart_crusher"]}`,
			headroomRetrieveTool: `{"hash":"ccr_abc","source":"local","original_content":"original text"}`,
			headroomStatsTool:    `{"compressions":1,"total_tokens_saved":7}`,
		},
	}
	attachAdapter(t, m, adapter)

	res, err := m.Compress(context.Background(), CompressRequest{Content: "original text"})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("original text"))
	if res.Content != "summary" || res.OriginalRef != "ccr_abc" || res.OriginalSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected compression response: %+v", res)
	}
	if res.OriginalTokens != 10 || res.CompressedTokens != 3 || res.SavingsPercent != 70 || len(res.Transforms) != 1 {
		t.Fatalf("Headroom metrics were not preserved: %+v", res)
	}
	if len(adapter.calls) != 1 || adapter.calls[0].tool != headroomCompressTool || adapter.calls[0].args["content"] != "original text" {
		t.Fatalf("unexpected compress call: %+v", adapter.calls)
	}

	retrieved, err := m.Retrieve(context.Background(), "ccr_abc")
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.Content != "original text" || retrieved.Source != "local" || retrieved.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected retrieve response: %+v", retrieved)
	}
	if len(adapter.calls) != 2 || len(adapter.calls[1].args) != 1 || adapter.calls[1].args["hash"] != "ccr_abc" {
		t.Fatalf("retrieve must use the pinned hash-only schema: %+v", adapter.calls[1])
	}

	stats, err := m.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Headroom == nil || stats.HeadroomError != "" {
		t.Fatalf("Headroom stats unavailable: %+v", stats)
	}
	st := m.Status()
	if !st.AdapterReady || !st.CompressAvailable || !st.RetrieveAvailable || !st.StatsAvailable || st.Reason != "managed Headroom MCP connected; compression adapter active" {
		t.Fatalf("status is not truthful: %+v", st)
	}
}

func TestHeadroomAutoCompressionFailureDegradesToNoop(t *testing.T) {
	m := newAdapterTestManager(t, ModeAuto)
	adapter := &fakeManagedMCP{schemas: headroomSchemas(true), outputs: map[string]string{}}
	attachAdapter(t, m, adapter)
	adapter.err = errors.New("sidecar exited")

	res, err := m.Compress(context.Background(), CompressRequest{Content: "keep me"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "keep me" || res.Engine != ModeNoop {
		t.Fatalf("auto fallback did not preserve content: %+v", res)
	}
	st := m.Status()
	if st.EffectiveEngine != ModeNoop || !st.Degraded || st.Phase != PhaseFailed || !strings.Contains(st.LastError, "sidecar exited") {
		t.Fatalf("auto fallback was not observable: %+v", st)
	}
	stats, _ := m.Stats(context.Background())
	if stats.FallbackCalls != 1 {
		t.Fatalf("fallback counter = %d, want 1", stats.FallbackCalls)
	}
}

func TestHeadroomExplicitCompressionFailureReturnsError(t *testing.T) {
	m := newAdapterTestManager(t, ModeHeadroom)
	adapter := &fakeManagedMCP{schemas: headroomSchemas(true), outputs: map[string]string{}}
	attachAdapter(t, m, adapter)
	adapter.err = errors.New("sidecar exited")

	if _, err := m.Compress(context.Background(), CompressRequest{Content: "must compress"}); err == nil || !strings.Contains(err.Error(), "sidecar exited") {
		t.Fatalf("explicit Headroom failure = %v", err)
	}
	if doc := m.Doctor(); doc["ok"] != false {
		t.Fatalf("doctor reported healthy after explicit failure: %+v", doc)
	}
}

func TestHeadroomPayloadErrorIsNotModelContent(t *testing.T) {
	for _, mode := range []string{ModeAuto, ModeHeadroom} {
		t.Run(mode, func(t *testing.T) {
			m := newAdapterTestManager(t, mode)
			adapter := &fakeManagedMCP{
				schemas: headroomSchemas(true),
				outputs: map[string]string{headroomCompressTool: `{"error":"compressor unavailable"}`},
			}
			attachAdapter(t, m, adapter)
			res, err := m.Compress(context.Background(), CompressRequest{Content: "original"})
			if mode == ModeAuto {
				if err != nil || res.Content != "original" || res.Engine != ModeNoop {
					t.Fatalf("auto payload error did not fail closed: res=%+v err=%v", res, err)
				}
			} else if err == nil {
				t.Fatal("explicit mode treated Headroom error JSON as compressed model content")
			}
		})
	}
}

func TestHeadroomRetrieveUnavailableWhenToolNotAdvertised(t *testing.T) {
	m := newAdapterTestManager(t, ModeHeadroom)
	adapter := &fakeManagedMCP{schemas: headroomSchemas(false), outputs: map[string]string{}}
	attachAdapter(t, m, adapter)
	if m.Status().RetrieveAvailable {
		t.Fatal("retrieve reported available without a valid tools/list schema")
	}
	if _, err := m.Retrieve(context.Background(), "ccr_abc"); err == nil || !strings.Contains(err.Error(), "did not advertise") {
		t.Fatalf("retrieve unavailable error = %v", err)
	}
	if len(adapter.calls) != 0 {
		t.Fatalf("unadvertised retrieve tool was called: %+v", adapter.calls)
	}
}

func TestHeadroomPinnedObservationBypassesAdapter(t *testing.T) {
	m := newAdapterTestManager(t, ModeHeadroom)
	adapter := &fakeManagedMCP{schemas: headroomSchemas(true), outputs: map[string]string{}}
	attachAdapter(t, m, adapter)
	res, err := m.Compress(context.Background(), CompressRequest{Content: "failing test output", Pinned: true})
	if err != nil || res.Content != "failing test output" {
		t.Fatalf("pinned content changed: res=%+v err=%v", res, err)
	}
	if len(adapter.calls) != 0 {
		t.Fatalf("pinned content reached Headroom: %+v", adapter.calls)
	}
}

func TestAttachManagedMCPRejectsGuessedSchema(t *testing.T) {
	m := newAdapterTestManager(t, ModeHeadroom)
	adapter := &fakeManagedMCP{schemas: map[string]json.RawMessage{
		headroomCompressTool: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}}
	if err := m.AttachManagedMCP(adapter); err == nil {
		t.Fatal("adapter accepted a compress schema that does not match pinned Headroom")
	}
}
