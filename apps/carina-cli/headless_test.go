package main

import (
	"encoding/json"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
)

func TestParseStreamRunArgsRequiresSymmetricProtocol(t *testing.T) {
	opts, err := parseStreamRunArgs([]string{
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--session", "sess_1", "--model", "openai/gpt-5", "--effort", "high", "--mode", "plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.sessionID != "sess_1" || opts.model != "openai/gpt-5" || opts.effort != "high" || opts.mode != "plan" {
		t.Fatalf("unexpected options: %#v", opts)
	}
	for _, args := range [][]string{
		{"--input-format", "stream-json"},
		{"--output-format", "stream-json"},
		{"--input-format", "json", "--output-format", "stream-json"},
		{"--input-format", "stream-json", "--output-format", "stream-json", "unexpected", "value"},
	} {
		if _, err := parseStreamRunArgs(args); err == nil {
			t.Fatalf("accepted invalid args %#v", args)
		}
	}
}

func TestHandleStreamInputUsesOneGovernedControlContract(t *testing.T) {
	s := rpc.NewServer()
	calls := map[string]map[string]any{}
	register := func(method string, scope rpc.Scope) {
		t.Helper()
		if err := s.RegisterMethod(rpc.MethodDescriptor{Method: method, Scope: scope, Remote: true}, func(params json.RawMessage) (any, error) {
			var decoded map[string]any
			if err := json.Unmarshal(params, &decoded); err != nil {
				return nil, err
			}
			calls[method] = decoded
			return map[string]any{"ok": true, "method": method}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	register("task.submit", rpc.ScopeWrite)
	register("task.steer", rpc.ScopeWrite)
	register("task.approval.resolve", rpc.ScopeAdmin)
	register("task.user.answer", rpc.ScopeWrite)
	register("task.cancel", rpc.ScopeWrite)
	c := dialTestServer(t, s)
	defer c.Close()

	defaults := streamRunOptions{model: "openai/gpt-5", effort: "high", agent: "build", mode: "plan"}
	result, stop, err := handleStreamInput(c, "sess_1", defaults, streamInputFrame{Type: "prompt", Text: "ship", ClientSubmissionID: "sub_1"})
	if err != nil || stop || result.(map[string]any)["ok"] != true {
		t.Fatalf("prompt result=%#v stop=%v err=%v", result, stop, err)
	}
	if got := calls["task.submit"]; got["session_id"] != "sess_1" || got["model"] != defaults.model || got["reasoning_effort"] != defaults.effort || got["client_submission_id"] != "sub_1" {
		t.Fatalf("task.submit params=%#v", got)
	} else if _, leaked := got["mode"]; leaked {
		t.Fatalf("session plan/build mode leaked into task execution mode: %#v", got)
	}

	_, _, err = handleStreamInput(c, "sess_1", defaults, streamInputFrame{Type: "approval", DecisionID: "dec_1", Decision: "allow", Scope: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if got := calls["task.approval.resolve"]; got["decision_id"] != "dec_1" || got["approve"] != true || got["scope"] != "session" || got["approver"] != "headless" {
		t.Fatalf("approval params=%#v", got)
	}

	_, _, err = handleStreamInput(c, "sess_1", defaults, streamInputFrame{Type: "answer", QuestionID: "q_1", Value: "free text"})
	if err != nil || calls["task.user.answer"]["value"] != "free text" {
		t.Fatalf("answer err=%v params=%#v", err, calls["task.user.answer"])
	}

	if _, _, err := handleStreamInput(c, "sess_1", defaults, streamInputFrame{Type: "approval", DecisionID: "dec_2", Decision: "allow", Scope: "global"}); err == nil {
		t.Fatal("unsafe approval scope accepted")
	}
	if _, stop, err := handleStreamInput(c, "sess_1", defaults, streamInputFrame{Type: "close"}); err != nil || !stop {
		t.Fatalf("close stop=%v err=%v", stop, err)
	}
}

func TestStreamWriterAddsStableEnvelope(t *testing.T) {
	var out stringWriter
	w := streamWriter{w: &out}
	if err := w.write(map[string]any{"type": "response", "request_id": "r1"}); err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(out.value), &frame); err != nil {
		t.Fatal(err)
	}
	if frame["protocol"] != streamProtocol || int(frame["version"].(float64)) != streamProtocolVersion || frame["type"] != "response" {
		t.Fatalf("unexpected envelope: %#v", frame)
	}
}

type stringWriter struct{ value string }

func (w *stringWriter) Write(p []byte) (int, error) {
	w.value += string(p)
	return len(p), nil
}
