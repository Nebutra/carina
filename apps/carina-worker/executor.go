package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	executorResultSchema         = "carina.worker.result.v1"
	maxExecutorOutput            = 4 << 20
	maxExecutorSummary           = 16 << 10
	maxExecutorPatches           = 1024
	maxExecutorChannelMessages   = 64
	maxExecutorChannelNameLength = 128
	maxExecutorReportedTokens    = 1_000_000_000
)

// executorChannelMessage is the executor-result counterpart to
// go/daemon/swarm_channel.go's remoteChannelMessage — a remote-dispatched
// streaming-workflow step's way to publish into its run's swarm channel
// broker (Agent Swarm design §6), since the external executor process has
// no in-process tool-dispatch loop to call "swarm_publish" through. Batched
// at report time, not truly live: the executor result contract is one JSON
// value at the very end, not a stream, so these surface when the step
// finishes, not continuously while it runs. The daemon (handleWorkReport)
// is the authoritative validator; this is optional and ignored entirely by
// a task that isn't a swarm-workflow dispatch.
type executorChannelMessage struct {
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
}

// executionResult is the only result shape accepted from an external executor.
// Versioning prevents an old executable from silently changing task semantics.
type executionResult struct {
	SchemaVersion   string                   `json:"schema_version"`
	Status          string                   `json:"status"`
	Summary         string                   `json:"summary"`
	Patches         []string                 `json:"patches"`
	Usage           *executorTokenUsage      `json:"usage,omitempty"`
	ChannelMessages []executorChannelMessage `json:"channel_messages,omitempty"`
}

type executionResultWire struct {
	SchemaVersion   *string                  `json:"schema_version"`
	Status          *string                  `json:"status"`
	Summary         *string                  `json:"summary"`
	Patches         *[]string                `json:"patches"`
	Usage           *executorTokenUsage      `json:"usage,omitempty"`
	ChannelMessages []executorChannelMessage `json:"channel_messages,omitempty"`
}

type executorTokenUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

func (u executorTokenUsage) total() (int, error) {
	total := 0
	for _, value := range []int{u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens} {
		if value < 0 {
			return 0, fmt.Errorf("executor usage token counts must be non-negative")
		}
		if value > maxExecutorReportedTokens-total {
			return 0, fmt.Errorf("executor usage exceeded %d tokens", maxExecutorReportedTokens)
		}
		total += value
	}
	return total, nil
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
	stdout := &limitedBuffer{limit: maxExecutorOutput}
	stderr := &limitedBuffer{limit: maxExecutorOutput}
	err := runExecutorCommand(ctx, e.program, e.args, executorEnvironment(), task, stdout, stderr)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return failedResult("executor timed out")
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return failedResult("executor cancelled")
		}
		var exitErr interface{ ExitCode() int }
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
		SchemaVersion:   *wire.SchemaVersion,
		Status:          *wire.Status,
		Summary:         *wire.Summary,
		Patches:         *wire.Patches,
		Usage:           wire.Usage,
		ChannelMessages: wire.ChannelMessages,
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
	if result.Usage != nil {
		if _, err := result.Usage.total(); err != nil {
			return err
		}
	}
	if len(result.ChannelMessages) > maxExecutorChannelMessages {
		return fmt.Errorf("executor returned too many channel_messages (max %d)", maxExecutorChannelMessages)
	}
	for _, m := range result.ChannelMessages {
		if strings.TrimSpace(m.Channel) == "" {
			return fmt.Errorf("executor returned a channel_messages entry with an empty channel")
		}
		if len(m.Channel) > maxExecutorChannelNameLength {
			return fmt.Errorf("executor returned a channel name longer than %d characters", maxExecutorChannelNameLength)
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
