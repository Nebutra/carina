package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	codexCLIEventLineLimit   = 1 << 20
	codexCLIEventStreamLimit = 4 << 20
	codexCLIStderrLimit      = 32 << 10
	codexCLIReasonerTimeout  = 180 * time.Second

	codexCLIReasonerGuard = "Act only as a pure inference reasoner. Do not call, request, or use any tools. " +
		"Reason only from the prompt below and return only the requested answer."
)

var codexCLIDisabledFeatures = []string{
	"apps",
	"browser_use",
	"browser_use_external",
	"browser_use_full_cdp_access",
	"code_mode_host",
	"computer_use",
	"goals",
	"hooks",
	"image_generation",
	"in_app_browser",
	"multi_agent",
	"plugin_sharing",
	"plugins",
	"remote_plugin",
	"shell_snapshot",
	"shell_tool",
	"skill_mcp_dependency_install",
	"skill_search",
	"tool_call_mcp_elicitation",
	"unified_exec",
}

type codexCLIReasoner struct {
	bin       string
	model     string
	workdir   string
	timeout   time.Duration
	closeOnce sync.Once
}

type codexCLIUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

type codexCLIEvent struct {
	Type    string          `json:"type"`
	Item    json.RawMessage `json:"item"`
	Error   json.RawMessage `json:"error"`
	Message string          `json:"message"`
	Usage   *codexCLIUsage  `json:"usage"`
}

type codexCLIItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexCLIStreamResult struct {
	text      string
	completed bool
	usage     codexCLIUsage
}

type codexCLIError struct {
	message string
	kind    string
}

func (e codexCLIError) Error() string {
	if message := boundedMetadata(e.message, 500); message != "" {
		return "codex reasoner: " + message
	}
	return "codex reasoner failed"
}

func (e codexCLIError) ProviderError() providerErrorInfo {
	if e.kind == "safety" {
		return providerErrorInfo{
			Code:       "reasoner_safety_violation",
			Category:   "internal",
			Provider:   "openai",
			UserAction: "use model-router or update the Codex CLI compatibility policy",
		}
	}
	if e.kind == "protocol" {
		return providerErrorInfo{
			Code:       "reasoner_protocol_error",
			Category:   "internal",
			Provider:   "openai",
			UserAction: "update Codex CLI or use model-router",
		}
	}
	message := strings.ToLower(e.message)
	switch {
	case strings.Contains(message, "not logged in"),
		strings.Contains(message, "login required"),
		strings.Contains(message, "authentication"),
		strings.Contains(message, "unauthorized"),
		strings.Contains(message, "invalid api key"),
		strings.Contains(message, "missing api key"):
		return providerErrorInfo{
			Code:       "provider_authentication_failed",
			Category:   "authentication",
			Provider:   "openai",
			UserAction: "run `codex login` or configure the Codex CLI credential",
		}
	case strings.Contains(message, "rate limit"), strings.Contains(message, "too many requests"):
		return providerErrorInfo{
			Code:       "provider_rate_limited",
			Category:   "rate_limit",
			Provider:   "openai",
			Retryable:  true,
			UserAction: "wait or choose another provider",
		}
	case strings.Contains(message, "quota"),
		strings.Contains(message, "credit balance"),
		strings.Contains(message, "billing"):
		return providerErrorInfo{
			Code:       "provider_quota_exhausted",
			Category:   "rate_limit",
			Provider:   "openai",
			UserAction: "increase quota or choose another provider",
		}
	case strings.Contains(message, "overloaded"),
		strings.Contains(message, "temporarily unavailable"),
		strings.Contains(message, "service unavailable"):
		return providerErrorInfo{
			Code:      "provider_unavailable",
			Category:  "unavailable",
			Provider:  "openai",
			Retryable: true,
		}
	default:
		return providerErrorInfo{
			Code:       "reasoner_internal_error",
			Category:   "internal",
			Provider:   "openai",
			UserAction: "run `codex login status` and inspect the Codex CLI configuration",
		}
	}
}

func newCodexCLIReasoner() (*codexCLIReasoner, error) {
	return newCodexCLIReasonerModel(os.Getenv("CARINA_REASONER_MODEL"))
}

func newCodexCLIReasonerModel(model string) (*codexCLIReasoner, error) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("codex CLI not found on PATH: %w", err)
	}
	dir, err := os.MkdirTemp("", "carina-codex-reasoner-")
	if err != nil {
		return nil, err
	}
	return &codexCLIReasoner{
		bin:     bin,
		model:   strings.TrimSpace(model),
		workdir: dir,
		timeout: codexCLIReasonerTimeout,
	}, nil
}

func (r *codexCLIReasoner) Name() string { return reasonerBackendCodexCLI }

func (r *codexCLIReasoner) Think(ctx context.Context, prompt string) (string, error) {
	result, err := r.ThinkResult(ctx, prompt)
	return result.Text, err
}

func (r *codexCLIReasoner) ThinkResult(ctx context.Context, prompt string) (ReasonerResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(callCtx, r.bin, r.args(prompt)...)
	configureCodexCLICommand(cmd)
	cmd.Dir = r.workdir
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ReasonerResult{}, codexCLIError{message: err.Error(), kind: "protocol"}
	}
	stderr := &codexBoundedBuffer{limit: codexCLIStderrLimit}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return ReasonerResult{}, codexCLIError{message: err.Error()}
	}

	stream, streamErr := decodeCodexCLIStream(stdout)
	if streamErr != nil {
		_ = stdout.Close()
		cancel()
		killCodexCLICommand(cmd)
	}
	waitErr := cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ReasonerResult{}, ctxErr
	}
	if streamErr != nil {
		return ReasonerResult{}, streamErr
	}
	if callCtx.Err() != nil {
		return ReasonerResult{}, callCtx.Err()
	}
	return finishCodexCLIStream(stream, stderr.String(), waitErr, r.model)
}

func (r *codexCLIReasoner) args(prompt string) []string {
	args := []string{
		"exec",
		"--ephemeral",
		"--json",
		"--sandbox", "read-only",
		"--ignore-user-config",
		"--ignore-rules",
		"--skip-git-repo-check",
	}
	for _, feature := range codexCLIDisabledFeatures {
		args = append(args, "--disable", feature)
	}
	args = append(args,
		"-c", `web_search="disabled"`,
		"-c", "project_doc_max_bytes=0",
		"-C", r.workdir,
	)
	if r.model != "" {
		args = append(args, "--model", r.model)
	}
	return append(args, codexGuardedPrompt(prompt))
}

func codexGuardedPrompt(prompt string) string {
	return codexCLIReasonerGuard + "\n\n" + prompt
}

func decodeCodexCLIStream(reader io.Reader) (codexCLIStreamResult, error) {
	var result codexCLIStreamResult
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), codexCLIEventLineLimit)
	total := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		total += len(line) + 1
		if total > codexCLIEventStreamLimit {
			return codexCLIStreamResult{}, codexCLIError{message: "JSONL event stream exceeds size limit", kind: "protocol"}
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var event codexCLIEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return codexCLIStreamResult{}, codexCLIError{
				message: fmt.Sprintf("decode JSONL event: %v (%s)", err, truncate(string(line), 200)),
				kind:    "protocol",
			}
		}
		if err := result.consume(event); err != nil {
			return codexCLIStreamResult{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return codexCLIStreamResult{}, codexCLIError{message: "read JSONL events: " + err.Error(), kind: "protocol"}
	}
	return result, nil
}

func (r *codexCLIStreamResult) consume(event codexCLIEvent) error {
	switch event.Type {
	case "thread.started", "turn.started":
		return nil
	case "turn.completed":
		r.completed = true
		if event.Usage != nil {
			r.usage = *event.Usage
		}
		return nil
	case "turn.failed", "error":
		message := codexCLIEventErrorMessage(event)
		if message == "" {
			message = event.Type
		}
		return codexCLIError{message: message}
	case "item.started", "item.updated", "item.completed":
		var item codexCLIItem
		if err := json.Unmarshal(event.Item, &item); err != nil {
			return codexCLIError{message: "decode JSONL item: " + err.Error(), kind: "protocol"}
		}
		switch item.Type {
		case "agent_message":
			if event.Type == "item.completed" {
				r.text = strings.TrimSpace(item.Text)
			}
			return nil
		case "reasoning", "plan", "plan_update", "todo_list":
			return nil
		default:
			itemType := strings.TrimSpace(item.Type)
			if itemType == "" {
				itemType = "unknown"
			}
			return codexCLIError{message: "disallowed Codex item type " + itemType, kind: "safety"}
		}
	default:
		return codexCLIError{message: "unsupported Codex event type " + event.Type, kind: "protocol"}
	}
}

func codexCLIEventErrorMessage(event codexCLIEvent) string {
	if message := strings.TrimSpace(event.Message); message != "" {
		return message
	}
	if len(event.Error) == 0 || string(event.Error) == "null" {
		return ""
	}
	var message string
	if json.Unmarshal(event.Error, &message) == nil {
		return strings.TrimSpace(message)
	}
	var detail struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(event.Error, &detail) == nil {
		return strings.TrimSpace(detail.Message)
	}
	return boundedMetadata(string(event.Error), 500)
}

func finishCodexCLIStream(stream codexCLIStreamResult, stderr string, runErr error, model string) (ReasonerResult, error) {
	if runErr != nil {
		message := boundedMetadata(stderr, 500)
		if message == "" {
			message = runErr.Error()
		}
		return ReasonerResult{}, codexCLIError{message: message}
	}
	if !stream.completed {
		return ReasonerResult{}, codexCLIError{message: "JSONL stream ended without turn.completed", kind: "protocol"}
	}
	if strings.TrimSpace(stream.text) == "" {
		return ReasonerResult{}, codexCLIError{message: "JSONL stream completed without a final agent message", kind: "protocol"}
	}
	input := max(0, stream.usage.InputTokens)
	cached := min(max(0, stream.usage.CachedInputTokens), input)
	return ReasonerResult{Text: stream.text, Usage: ModelUsage{
		Provider:        "openai",
		Model:           model,
		InputTokens:     input - cached,
		OutputTokens:    max(0, stream.usage.OutputTokens),
		CacheReadTokens: cached,
	}}, nil
}

func (r *codexCLIReasoner) Close() {
	r.closeOnce.Do(func() {
		_ = os.RemoveAll(r.workdir)
	})
}

type codexBoundedBuffer struct {
	data  []byte
	limit int
}

func (b *codexBoundedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return written, nil
}

func (b *codexBoundedBuffer) String() string {
	return string(b.data)
}

var _ Reasoner = (*codexCLIReasoner)(nil)
var _ resultReasoner = (*codexCLIReasoner)(nil)
var _ providerErrorClassifier = codexCLIError{}
var _ io.Writer = (*codexBoundedBuffer)(nil)
var _ error = codexCLIError{}
