package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCommandExecutorContract(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		timeout    time.Duration
		wantStatus string
		wantText   string
	}{
		{name: "success", mode: "success", timeout: 5 * time.Second, wantStatus: "completed", wantText: "task_exec"},
		{name: "invalid json", mode: "invalid", timeout: 5 * time.Second, wantStatus: "failed", wantText: "invalid JSON"},
		{name: "unknown field", mode: "unknown", timeout: 5 * time.Second, wantStatus: "failed", wantText: "invalid JSON"},
		{name: "missing field", mode: "missing", timeout: 5 * time.Second, wantStatus: "failed", wantText: "missing a required field"},
		{name: "nonzero", mode: "nonzero", timeout: 5 * time.Second, wantStatus: "failed", wantText: "status 7"},
		{name: "timeout", mode: "sleep", timeout: 100 * time.Millisecond, wantStatus: "failed", wantText: "timed out"},
		{name: "gateway token scrubbed", mode: "environment", timeout: 5 * time.Second, wantStatus: "completed", wantText: "token=absent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GO_WANT_CARINA_EXECUTOR_HELPER", "1")
			t.Setenv("CARINA_GATEWAY_TOKEN", "must-not-reach-executor")
			executor := newCommandExecutor(os.Args[0], []string{"-test.run=TestCarinaExecutorHelperProcess", "--", tt.mode})
			ctx, cancel := context.WithTimeout(context.Background(), tt.timeout)
			defer cancel()
			result := executor.Execute(ctx, json.RawMessage(`{"task_id":"task_exec"}`))
			if result.Status != tt.wantStatus || !strings.Contains(result.Summary, tt.wantText) {
				t.Fatalf("result = %+v, want status=%s text=%q", result, tt.wantStatus, tt.wantText)
			}
		})
	}
}

func TestCarinaExecutorHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CARINA_EXECUTOR_HELPER") != "1" {
		return
	}
	raw, _ := io.ReadAll(os.Stdin)
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "success":
		var task struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(raw, &task)
		fmt.Printf(`{"schema_version":"%s","status":"completed","summary":%q,"patches":["patch_1"]}`, executorResultSchema, task.TaskID)
	case "invalid":
		fmt.Print("not-json")
	case "unknown":
		fmt.Printf(`{"schema_version":"%s","status":"completed","summary":"ok","patches":[],"extra":true}`, executorResultSchema)
	case "missing":
		fmt.Printf(`{"schema_version":"%s","status":"completed","summary":"ok"}`, executorResultSchema)
	case "nonzero":
		fmt.Printf(`{"schema_version":"%s","status":"completed","summary":"must not trust","patches":[]}`, executorResultSchema)
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Second)
	case "descendant":
		child := exec.Command(os.Args[0], "-test.run=TestCarinaExecutorHelperProcess", "--", "leaf")
		child.Env = append(os.Environ(), "GO_WANT_CARINA_EXECUTOR_HELPER=1")
		if err := child.Start(); err != nil {
			os.Exit(8)
		}
		if err := os.WriteFile(os.Getenv("CARINA_EXECUTOR_CHILD_PID_FILE"), []byte(fmt.Sprint(child.Process.Pid)), 0o600); err != nil {
			os.Exit(9)
		}
		_ = child.Wait()
	case "leaf":
		time.Sleep(30 * time.Second)
	case "environment":
		value := "absent"
		if os.Getenv("CARINA_GATEWAY_TOKEN") != "" {
			value = "present"
		}
		fmt.Printf(`{"schema_version":"%s","status":"completed","summary":"token=%s","patches":[]}`, executorResultSchema, value)
	}
	os.Exit(0)
}

func TestValidateExecutionResult(t *testing.T) {
	valid := executionResult{SchemaVersion: executorResultSchema, Status: "degraded", Summary: "partial", Patches: []string{"patch_1"}, Usage: &executorTokenUsage{InputTokens: 10, OutputTokens: 5}}
	if err := validateExecutionResult(valid); err != nil {
		t.Fatalf("valid result: %v", err)
	}
	for _, bad := range []executionResult{
		{Status: "completed"},
		{SchemaVersion: executorResultSchema, Status: "cancelled"},
		{SchemaVersion: executorResultSchema, Status: "completed", Patches: []string{""}},
		{SchemaVersion: executorResultSchema, Status: "completed", Usage: &executorTokenUsage{InputTokens: -1}},
		{SchemaVersion: executorResultSchema, Status: "completed", Usage: &executorTokenUsage{InputTokens: maxExecutorReportedTokens, OutputTokens: 1}},
		{SchemaVersion: executorResultSchema, Status: "completed", Usage: &executorTokenUsage{InputTokens: int(^uint(0) >> 1), OutputTokens: int(^uint(0) >> 1)}},
	} {
		if err := validateExecutionResult(bad); err == nil {
			t.Fatalf("expected invalid result: %+v", bad)
		}
	}
}
