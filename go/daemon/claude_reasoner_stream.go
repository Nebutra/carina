package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const (
	claudeCLIEventLineLimit   = maxProviderResponseBytes
	claudeCLIEventStreamLimit = 4 * maxProviderResponseBytes
	claudeCLIStderrLimit      = 32 << 10
)

type claudeCLIUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

type claudeCLIEvent struct {
	Type              string                     `json:"type"`
	Subtype           string                     `json:"subtype"`
	Event             json.RawMessage            `json:"event"`
	Message           json.RawMessage            `json:"message"`
	Result            string                     `json:"result"`
	Model             string                     `json:"model"`
	IsError           bool                       `json:"is_error"`
	APIErrorStatus    *int                       `json:"api_error_status"`
	Usage             claudeCLIUsage             `json:"usage"`
	ModelUsage        map[string]json.RawMessage `json:"modelUsage"`
	Tools             []string                   `json:"tools"`
	PermissionDenials []json.RawMessage          `json:"permission_denials"`
	ParentToolUseID   json.RawMessage            `json:"parent_tool_use_id"`
}

type claudeCLIMessage struct {
	Model   string             `json:"model"`
	Content []claudeCLIContent `json:"content"`
	Usage   claudeCLIUsage     `json:"usage"`
}

type claudeCLIContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

type claudeCLIStreamEvent struct {
	Type         string           `json:"type"`
	Message      claudeCLIMessage `json:"message"`
	ContentBlock claudeCLIContent `json:"content_block"`
	Delta        struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Usage claudeCLIUsage `json:"usage"`
}

type claudeCLIStreamResult struct {
	text          string
	assistantText string
	streamedText  strings.Builder
	model         string
	usage         claudeCLIUsage
	assistantSeen bool
	completed     bool
}

func (r *claudeCLIReasoner) thinkRoutedModelStream(ctx context.Context, model, prompt string) (ReasonerResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	model = strings.TrimSpace(model)
	cmd := exec.CommandContext(callCtx, r.bin, r.args(callCtx, model, prompt)...)
	configureCLIReasonerCommand(cmd)
	cmd.Dir = r.workdir
	// Authentication remains owned by Claude Code, so inherit its environment.
	// --safe-mode and --tools "" disable all customization and tool surfaces.
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ReasonerResult{}, claudeCLIError{message: err.Error(), kind: "protocol"}
	}
	stderr := &boundedCLIWriter{limit: claudeCLIStderrLimit}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return ReasonerResult{}, claudeCLIError{message: err.Error()}
	}

	publicStream := reasonerStreamFrom(callCtx)
	var publicDecoder actionEnvelopeStreamDecoder
	stream, streamErr := decodeClaudeCLIStream(stdout, func(delta string) {
		if publicStream != nil {
			publicStream.emit(publicDecoder.Push(delta))
		}
	})
	if streamErr != nil {
		_ = stdout.Close()
		cancel()
		_ = killCLIReasonerCommand(cmd)
	}
	waitErr := cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		resetReasonerStream(publicStream)
		return ReasonerResult{}, ctxErr
	}
	if streamErr != nil {
		resetReasonerStream(publicStream)
		return ReasonerResult{}, streamErr
	}
	if callCtx.Err() != nil {
		resetReasonerStream(publicStream)
		return ReasonerResult{}, callCtx.Err()
	}
	result, err := finishClaudeCLIStream(stream, stderr.String(), waitErr, model)
	if err != nil {
		resetReasonerStream(publicStream)
		return ReasonerResult{}, err
	}
	result.Usage.EffectiveReasoningEffort = reasoningEffortFrom(callCtx)
	return result, nil
}

func (r *claudeCLIReasoner) args(ctx context.Context, model, prompt string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--safe-mode",
		"--tools", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--no-chrome",
		"--permission-mode", "dontAsk",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort := reasoningEffortFrom(ctx); effort != "" {
		args = append(args, "--effort", effort)
	}
	return args
}

func resetReasonerStream(stream *reasonerStreamController) {
	if stream != nil {
		stream.reset()
	}
}

func decodeClaudeCLIStream(reader io.Reader, onText func(string)) (claudeCLIStreamResult, error) {
	var result claudeCLIStreamResult
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), claudeCLIEventLineLimit)
	total := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		total += len(line) + 1
		if total > claudeCLIEventStreamLimit {
			return claudeCLIStreamResult{}, claudeCLIError{message: "JSONL event stream exceeds size limit", kind: "protocol"}
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event claudeCLIEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return claudeCLIStreamResult{}, claudeCLIError{
				message: fmt.Sprintf("decode JSONL event: %v (%s)", err, truncate(string(line), 200)),
				kind:    "protocol",
			}
		}
		if err := result.consume(event, onText); err != nil {
			return claudeCLIStreamResult{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return claudeCLIStreamResult{}, claudeCLIError{message: "read JSONL events: " + err.Error(), kind: "protocol"}
	}
	return result, nil
}

func (r *claudeCLIStreamResult) consume(event claudeCLIEvent, onText func(string)) error {
	if r.completed {
		return claudeCLIError{message: "Claude CLI emitted an event after the final result", kind: "protocol"}
	}
	if rawJSONPresent(event.ParentToolUseID) {
		return claudeCLIError{message: "Claude sub-agent output is not allowed", kind: "safety"}
	}
	switch event.Type {
	case "system":
		switch event.Subtype {
		case "init":
			if len(event.Tools) != 0 {
				return claudeCLIError{message: "Claude CLI exposed tools in isolated reasoner mode", kind: "safety"}
			}
			return nil
		case "status":
			return nil
		case "hook_started", "hook_response":
			return claudeCLIError{message: "Claude CLI executed a hook in isolated reasoner mode", kind: "safety"}
		default:
			return claudeCLIError{message: "unsupported Claude system event " + nonempty(event.Subtype, "unknown"), kind: "protocol"}
		}
	case "stream_event":
		return r.consumeStreamEvent(event.Event, onText)
	case "assistant":
		if r.assistantSeen {
			return claudeCLIError{message: "Claude CLI emitted multiple assistant messages", kind: "protocol"}
		}
		r.assistantSeen = true
		return r.consumeAssistant(event.Message)
	case "rate_limit_event":
		return nil
	case "result":
		if event.IsError || event.Subtype != "success" {
			status := 0
			if event.APIErrorStatus != nil {
				status = *event.APIErrorStatus
			}
			return claudeCLIError{message: event.Result, subtype: event.Subtype, status: status}
		}
		if len(event.PermissionDenials) != 0 {
			return claudeCLIError{message: "Claude CLI attempted a denied capability", kind: "safety"}
		}
		r.text = event.Result
		r.usage = event.Usage
		r.completed = true
		if event.Model != "" {
			r.model = event.Model
		} else if len(event.ModelUsage) == 1 {
			for model := range event.ModelUsage {
				r.model = model
			}
		}
		return nil
	case "user", "tool_progress", "tool_use_summary":
		return claudeCLIError{message: "disallowed Claude event type " + event.Type, kind: "safety"}
	default:
		return claudeCLIError{message: "unsupported Claude event type " + nonempty(event.Type, "unknown"), kind: "protocol"}
	}
}

func (r *claudeCLIStreamResult) consumeStreamEvent(raw json.RawMessage, onText func(string)) error {
	var event claudeCLIStreamEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return claudeCLIError{message: "decode Claude stream event: " + err.Error(), kind: "protocol"}
	}
	switch event.Type {
	case "message_start":
		if event.Message.Model != "" {
			r.model = event.Message.Model
		}
		return validateClaudeContent(event.Message.Content)
	case "content_block_start":
		return validateClaudeContent([]claudeCLIContent{event.ContentBlock})
	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			if r.streamedText.Len()+len(event.Delta.Text) > maxProviderResponseBytes {
				return claudeCLIError{message: "Claude response exceeds size limit", kind: "protocol"}
			}
			r.streamedText.WriteString(event.Delta.Text)
			if onText != nil {
				onText(event.Delta.Text)
			}
			return nil
		case "thinking_delta", "signature_delta":
			return nil
		case "input_json_delta":
			return claudeCLIError{message: "Claude CLI emitted tool input in isolated reasoner mode", kind: "safety"}
		default:
			return claudeCLIError{message: "unsupported Claude content delta " + nonempty(event.Delta.Type, "unknown"), kind: "protocol"}
		}
	case "content_block_stop", "message_stop", "ping":
		return nil
	case "message_delta":
		r.usage = event.Usage
		return nil
	default:
		return claudeCLIError{message: "unsupported Claude stream event " + nonempty(event.Type, "unknown"), kind: "protocol"}
	}
}

func (r *claudeCLIStreamResult) consumeAssistant(raw json.RawMessage) error {
	var message claudeCLIMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return claudeCLIError{message: "decode Claude assistant event: " + err.Error(), kind: "protocol"}
	}
	if err := validateClaudeContent(message.Content); err != nil {
		return err
	}
	var text strings.Builder
	for _, content := range message.Content {
		if content.Type == "text" {
			text.WriteString(content.Text)
		}
	}
	if text.Len() > maxProviderResponseBytes {
		return claudeCLIError{message: "Claude response exceeds size limit", kind: "protocol"}
	}
	r.assistantText = text.String()
	if message.Model != "" {
		r.model = message.Model
	}
	r.usage = message.Usage
	return nil
}

func validateClaudeContent(content []claudeCLIContent) error {
	for _, block := range content {
		switch block.Type {
		case "text", "thinking", "redacted_thinking":
			continue
		case "tool_use", "server_tool_use":
			return claudeCLIError{message: "Claude CLI emitted a tool block in isolated reasoner mode", kind: "safety"}
		default:
			return claudeCLIError{message: "unsupported Claude content block " + nonempty(block.Type, "unknown"), kind: "protocol"}
		}
	}
	return nil
}

func finishClaudeCLIStream(stream claudeCLIStreamResult, stderr string, runErr error, fallbackModel string) (ReasonerResult, error) {
	if runErr != nil {
		message := boundedMetadata(stderr, 500)
		if message == "" {
			message = runErr.Error()
		}
		return ReasonerResult{}, claudeCLIError{message: message}
	}
	if !stream.completed {
		return ReasonerResult{}, claudeCLIError{message: "JSONL stream ended without a result event", kind: "protocol"}
	}
	text := strings.TrimSpace(stream.text)
	if text == "" {
		return ReasonerResult{}, claudeCLIError{message: "JSONL stream completed without a final result", kind: "protocol"}
	}
	if streamed := strings.TrimSpace(stream.streamedText.String()); streamed != "" && streamed != text {
		return ReasonerResult{}, claudeCLIError{message: "streamed text does not match the final result", kind: "protocol"}
	}
	if assistant := strings.TrimSpace(stream.assistantText); assistant != "" && assistant != text {
		return ReasonerResult{}, claudeCLIError{message: "assistant text does not match the final result", kind: "protocol"}
	}
	model := strings.TrimSpace(stream.model)
	if model == "" {
		model = fallbackModel
	}
	return ReasonerResult{Text: text, Usage: ModelUsage{
		Provider:         "anthropic",
		Model:            model,
		InputTokens:      max(0, stream.usage.InputTokens),
		OutputTokens:     max(0, stream.usage.OutputTokens),
		CacheReadTokens:  max(0, stream.usage.CacheReadTokens),
		CacheWriteTokens: max(0, stream.usage.CacheCreationTokens),
	}}, nil
}

func rawJSONPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}
