package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/workflowui"
)

func TestParseWorkflowRunArgs(t *testing.T) {
	name, input, sessionID, jsonOutput, background, err := parseWorkflowRunArgs(
		[]string{"my-workflow", "the input", "--session", "sess_1", "--json", "--background"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-workflow" || input != "the input" || sessionID != "sess_1" || !jsonOutput || !background {
		t.Fatalf("parsed = name=%q input=%q session=%q json=%v background=%v", name, input, sessionID, jsonOutput, background)
	}

	if _, _, _, _, _, err := parseWorkflowRunArgs(nil); err == nil {
		t.Fatal("expected an error for a missing workflow name")
	}
	if _, _, _, _, _, err := parseWorkflowRunArgs([]string{"name", "--session"}); err == nil {
		t.Fatal("expected an error for --session with no value")
	}
}

func TestCmdWorkflowRunCreatesSessionAndRunsWorkflow(t *testing.T) {
	s := rpc.NewServer()
	var sessionParams, runParams map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "session.create", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		_ = json.Unmarshal(params, &sessionParams)
		return map[string]any{"session_id": "sess_auto"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "workflow.run", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		_ = json.Unmarshal(params, &runParams)
		return workflowui.Run{ID: "wf_run_1", Workflow: "review", Status: workflowui.Running}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "workflow.detail", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return workflowui.Detail{Run: workflowui.Run{ID: "wf_run_1", Workflow: "review", Status: workflowui.Completed}, Completed: 1, Total: 1, Progress: 1.0}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := dialTestServer(t, s)
	defer c.Close()

	out, err := captureStdout(t, func() error { return cmdWorkflowRun(c, []string{"review"}) })
	if err != nil {
		t.Fatalf("cmdWorkflowRun: %v", err)
	}
	if sessionParams["profile"] != "safe-edit" {
		t.Fatalf("expected an auto-created safe-edit session, got %#v", sessionParams)
	}
	if runParams["session_id"] != "sess_auto" || runParams["workflow"] != "review" {
		t.Fatalf("workflow.run params = %#v", runParams)
	}
	if !strings.Contains(out, "wf_run_1") || !strings.Contains(out, "100%") {
		t.Fatalf("expected run id and final progress in output, got:\n%s", out)
	}
}

func TestCmdWorkflowRunHonorsExplicitSessionAndBackground(t *testing.T) {
	s := rpc.NewServer()
	sessionCreateCalled := false
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "session.create", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		sessionCreateCalled = true
		return map[string]any{"session_id": "should-not-be-used"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var runParams map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "workflow.run", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		_ = json.Unmarshal(params, &runParams)
		return workflowui.Run{ID: "wf_run_2", Workflow: "review", Status: workflowui.Queued}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := dialTestServer(t, s)
	defer c.Close()

	out, err := captureStdout(t, func() error {
		return cmdWorkflowRun(c, []string{"review", "--session", "sess_explicit", "--background"})
	})
	if err != nil {
		t.Fatalf("cmdWorkflowRun: %v", err)
	}
	if sessionCreateCalled {
		t.Fatal("session.create must not be called when --session is given")
	}
	if runParams["session_id"] != "sess_explicit" {
		t.Fatalf("workflow.run params = %#v", runParams)
	}
	if !strings.Contains(out, "carina workflow status wf_run_2") {
		t.Fatalf("expected a --background hint pointing at the run id, got:\n%s", out)
	}
}

func TestCmdWorkflowRunNonCompletedTerminalStatusIsAnError(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "session.create", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		return map[string]any{"session_id": "sess_auto"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "workflow.run", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		return workflowui.Run{ID: "wf_run_3", Workflow: "review", Status: workflowui.Running}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "workflow.detail", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return workflowui.Detail{Run: workflowui.Run{ID: "wf_run_3", Workflow: "review", Status: workflowui.Failed}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := dialTestServer(t, s)
	defer c.Close()

	if _, err := captureStdout(t, func() error { return cmdWorkflowRun(c, []string{"review"}) }); err == nil {
		t.Fatal("expected a non-nil error for a run that ends Failed, mirroring runWaitForTask's contract")
	}
}

func TestCmdWorkflowListRendersHumanTableAndJSON(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "workflow.list", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return []workflowui.Run{
			{ID: "wf_1", Workflow: "review", Status: workflowui.Completed, Attempt: 1, UpdatedAt: time.Unix(0, 0).UTC()},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := dialTestServer(t, s)
	defer c.Close()

	human, err := captureStdout(t, func() error { return cmdWorkflowList(c, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human, "wf_1") || !strings.Contains(human, "review") || !strings.Contains(human, "completed") {
		t.Fatalf("expected a human table with run/workflow/status, got:\n%s", human)
	}

	jsonOut, err := captureStdout(t, func() error { return cmdWorkflowList(c, []string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut, `"id": "wf_1"`) {
		t.Fatalf("expected raw JSON output, got:\n%s", jsonOut)
	}
}

func TestCmdWorkflowStatusRendersStepsAndErrors(t *testing.T) {
	s := rpc.NewServer()
	var gotRunID string
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "workflow.detail", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		var p struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal(params, &p)
		gotRunID = p.RunID
		return workflowui.Detail{
			Run: workflowui.Run{ID: p.RunID, Workflow: "review", Status: workflowui.Failed, Steps: []workflowui.Step{
				{ID: "a", Status: workflowui.Completed},
				{ID: "b", Status: workflowui.Failed, Error: "boom"},
			}},
			Completed: 1, Failed: 1, Total: 2, Progress: 1.0,
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := dialTestServer(t, s)
	defer c.Close()

	out, err := captureStdout(t, func() error { return cmdWorkflowStatus(c, []string{"wf_run_9"}) })
	if err != nil {
		t.Fatal(err)
	}
	if gotRunID != "wf_run_9" {
		t.Fatalf("expected run_id=wf_run_9 in workflow.detail params, got %q", gotRunID)
	}
	for _, want := range []string{"wf_run_9", "failed", "100%", "boom"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in status output, got:\n%s", want, out)
		}
	}
	if err := cmdWorkflowStatus(c, nil); err == nil {
		t.Fatal("expected an error for a missing run id")
	}
}

func TestCmdWorkflowControlSubcommandsCallTheRightRPC(t *testing.T) {
	s := rpc.NewServer()
	calls := map[string]map[string]any{}
	for _, method := range []string{"workflow.pause", "workflow.resume", "workflow.stop", "workflow.restart"} {
		method := method
		if err := s.RegisterMethod(rpc.MethodDescriptor{Method: method, Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
			var decoded map[string]any
			_ = json.Unmarshal(params, &decoded)
			calls[method] = decoded
			return workflowui.Run{ID: "wf_run_x"}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := dialTestServer(t, s)
	defer c.Close()

	for _, sub := range []string{"pause", "resume", "stop", "restart"} {
		if _, err := captureStdout(t, func() error { return cmdWorkflow(c, []string{sub, "wf_run_x"}) }); err != nil {
			t.Fatalf("workflow %s: %v", sub, err)
		}
	}
	for _, method := range []string{"workflow.pause", "workflow.resume", "workflow.stop", "workflow.restart"} {
		if calls[method]["run_id"] != "wf_run_x" {
			t.Fatalf("%s params = %#v", method, calls[method])
		}
	}
}

func TestCmdWorkerRegisterSendsDeclaredPools(t *testing.T) {
	s := rpc.NewServer()
	var got map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "worker.register", Scope: rpc.ScopeWorker, Remote: true}, func(params json.RawMessage) (any, error) {
		// Reset (not merge): json.Unmarshal into an already-populated map
		// only OVERWRITES keys present in the new JSON, it never clears keys
		// absent from it — without this, a later call's genuinely-omitted
		// "pools" key would still read back as present from a PRIOR call.
		got = map[string]any{}
		_ = json.Unmarshal(params, &got)
		return map[string]any{"worker_id": "wrk_1", "worker_credential": "cred_1"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := dialTestServer(t, s)
	defer c.Close()

	if _, err := captureStdout(t, func() error {
		return cmdWorker(c, []string{"register", "gpu-worker", "remote", "--pool", "gpu-heavy", "--pool", "eu-west"})
	}); err != nil {
		t.Fatal(err)
	}
	pools, ok := got["pools"].([]any)
	if !ok || len(pools) != 2 || pools[0] != "gpu-heavy" || pools[1] != "eu-west" {
		t.Fatalf("worker.register params = %#v", got)
	}

	// No --pool given: the field must be omitted entirely, not sent as [].
	if _, err := captureStdout(t, func() error { return cmdWorker(c, []string{"register", "plain-worker"}) }); err != nil {
		t.Fatal(err)
	}
	if _, hasPools := got["pools"]; hasPools {
		t.Fatalf("expected no pools field when --pool was never given, got %#v", got)
	}
}

func TestRenderWorkflowDetail(t *testing.T) {
	var b strings.Builder
	renderWorkflowDetail(&b, workflowui.Detail{
		Run: workflowui.Run{ID: "wf_1", Workflow: "review", Status: workflowui.Completed, Steps: []workflowui.Step{
			{ID: "scan", Status: workflowui.Completed},
			{ID: "fix", Status: workflowui.Skipped, TokenUsageStatus: "unavailable_remote"},
		}},
		Completed: 1, Skipped: 1, Total: 2, Progress: 1.0, InputTokens: 100, OutputTokens: 40, CostUSD: 0.01, TokensUsed: 140, UnmeteredSteps: 1,
	})
	out := b.String()
	for _, want := range []string{"wf_1", "review", "completed", "100%", "scan", "fix", "skipped", "input=100", "observed_tokens=140", "unmetered_steps=1", "tokens=unavailable(remote)", "observed-only"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in rendered detail, got:\n%s", want, out)
		}
	}
}
