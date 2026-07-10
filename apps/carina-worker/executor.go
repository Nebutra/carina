package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	executorResultSchema = "carina.worker.result.v1"
	maxExecutorOutput    = 4 << 20
	maxExecutorSummary   = 16 << 10
	maxExecutorPatches   = 1024
)

// executionResult is the only result shape accepted from an external executor.
// Versioning prevents an old executable from silently changing task semantics.
type executionResult struct {
	SchemaVersion string   `json:"schema_version"`
	Status        string   `json:"status"`
	Summary       string   `json:"summary"`
	Patches       []string `json:"patches"`
}

type executionResultWire struct {
	SchemaVersion *string   `json:"schema_version"`
	Status        *string   `json:"status"`
	Summary       *string   `json:"summary"`
	Patches       *[]string `json:"patches"`
}

type taskExecutor interface {
	Execute(context.Context, json.RawMessage) executionResult
}

type commandExecutor struct {
	program string
	args    []string
}

func newCommandExecutor(program string, args []string) *commandExecutor {
	return &commandExecutor{program: program, args: append([]string(nil), args...)}
}

func (e *commandExecutor) Execute(ctx context.Context, task json.RawMessage) executionResult {
	cmd := exec.CommandContext(ctx, e.program, e.args...)
	configureExecutorCommand(cmd)
	cmd.Cancel = func() error { return killExecutorProcess(cmd) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = executorEnvironment()
	cmd.Stdin = bytes.NewReader(task)
	stdout := &limitedBuffer{limit: maxExecutorOutput}
	stderr := &limitedBuffer{limit: maxExecutorOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return failedResult("executor timed out")
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return failedResult("executor cancelled")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return failedResult(fmt.Sprintf("executor exited with status %d", exitErr.ExitCode()))
		}
		return failedResult("executor could not start")
	}
	if stdout.overflow {
		return failedResult("executor stdout exceeded 4 MiB")
	}

	var wire executionResultWire
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return failedResult("executor returned invalid JSON")
	}
	if err := ensureJSONEOF(dec); err != nil {
		return failedResult("executor returned more than one JSON value")
	}
	if wire.SchemaVersion == nil || wire.Status == nil || wire.Summary == nil || wire.Patches == nil {
		return failedResult("executor result is missing a required field")
	}
	result := executionResult{
		SchemaVersion: *wire.SchemaVersion,
		Status:        *wire.Status,
		Summary:       *wire.Summary,
		Patches:       *wire.Patches,
	}
	if err := validateExecutionResult(result); err != nil {
		return failedResult(err.Error())
	}
	return result
}

func validateExecutionResult(result executionResult) error {
	if result.SchemaVersion != executorResultSchema {
		return fmt.Errorf("executor schema_version must be %q", executorResultSchema)
	}
	switch result.Status {
	case "completed", "failed", "degraded":
	default:
		return fmt.Errorf("executor status must be completed, failed, or degraded")
	}
	if len(result.Summary) > maxExecutorSummary {
		return fmt.Errorf("executor summary exceeded 16 KiB")
	}
	if len(result.Patches) > maxExecutorPatches {
		return fmt.Errorf("executor returned too many patches")
	}
	for _, patch := range result.Patches {
		if strings.TrimSpace(patch) == "" {
			return fmt.Errorf("executor returned an empty patch id")
		}
	}
	return nil
}

func failedResult(summary string) executionResult {
	return executionResult{SchemaVersion: executorResultSchema, Status: "failed", Summary: summary}
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("extra JSON value")
}

// limitedBuffer caps untrusted executor output while preserving bytes.Buffer's
// writer contract so os/exec can continue draining pipes without deadlocking.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	if n > remaining {
		b.overflow = true
	}
	return n, nil
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

func executorEnvironment() []string {
	env := os.Environ()
	out := env[:0]
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(name, "CARINA_GATEWAY_TOKEN") {
			continue
		}
		out = append(out, item)
	}
	return out
}
