package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClaudeCLIArgsAreIsolatedAndStreaming(t *testing.T) {
	r := &claudeCLIReasoner{workdir: "/tmp/empty"}
	ctx := withReasoningEffort(context.Background(), "low")
	want := []string{
		"-p", "return JSON",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--safe-mode",
		"--tools", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--no-chrome",
		"--permission-mode", "dontAsk",
		"--model", "claude-opus-5",
		"--effort", "low",
	}
	if got := r.args(ctx, "claude-opus-5", "return JSON"); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v\nwant %#v", got, want)
	}
}

func TestDecodeClaudeCLIStreamReturnsDeltasFinalResultAndUsage(t *testing.T) {
	stream, err := decodeClaudeCLIStream(strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init","tools":[]}`,
		`{"type":"system","subtype":"status","status":"requesting"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-opus-5","content":[]}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"{\"tool\":\"done\",\"summary\":\"hel"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo\"}"}}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"{\"tool\":\"done\",\"summary\":\"hello\"}"}],"usage":{"input_tokens":12,"output_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}},"parent_tool_use_id":null}`,
		`{"type":"stream_event","event":{"type":"message_delta","usage":{"input_tokens":12,"output_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"tool\":\"done\",\"summary\":\"hello\"}","usage":{"input_tokens":12,"output_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5},"modelUsage":{"claude-opus-5":{}}}`,
	}, "\n")), func(delta string) {})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	result, err := finishClaudeCLIStream(stream, "", nil, "sonnet")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if result.Text != `{"tool":"done","summary":"hello"}` || result.Usage.Provider != "anthropic" || result.Usage.Model != "claude-opus-5" || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 3 || result.Usage.CacheWriteTokens != 4 || result.Usage.CacheReadTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDecodeClaudeCLIStreamPublishesOnlyDoneSummary(t *testing.T) {
	var decoder actionEnvelopeStreamDecoder
	var public strings.Builder
	stream, err := decodeClaudeCLIStream(strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init","tools":[]}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"{\"thought\":\"private\",\"tool\":\"done\",\"summary\":\"safe"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":" text\"}"}}}`,
		`{"type":"result","subtype":"success","result":"{\"thought\":\"private\",\"tool\":\"done\",\"summary\":\"safe text\"}","usage":{}}`,
	}, "\n")), func(delta string) {
		public.WriteString(decoder.Push(delta))
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finishClaudeCLIStream(stream, "", nil, "opus"); err != nil {
		t.Fatal(err)
	}
	if got := public.String(); got != "safe text" {
		t.Fatalf("public stream = %q", got)
	}
}

func TestDecodeClaudeCLIStreamAllowsThinkingThenTextAssistantSnapshots(t *testing.T) {
	stream, err := decodeClaudeCLIStream(strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init","tools":[]}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"thinking","thinking":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"sig"}}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"{\"tool\":\"done\",\"summary\":\"ok\"}"}}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"{\"tool\":\"done\",\"summary\":\"ok\"}"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"tool\":\"done\",\"summary\":\"ok\"}","usage":{"input_tokens":2,"output_tokens":4}}`,
	}, "\n")), nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	result, err := finishClaudeCLIStream(stream, "", nil, "sonnet")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if result.Text != `{"tool":"done","summary":"ok"}` || result.Usage.Model != "claude-opus-5" {
		t.Fatalf("result = %+v", result)
	}
}

func TestDecodeClaudeCLIStreamRejectsConflictingAssistantSnapshots(t *testing.T) {
	_, err := decodeClaudeCLIStream(strings.NewReader(strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`,
	}, "\n")), nil)
	if info := classifyProviderError(err); err == nil || info.Code != "reasoner_protocol_error" {
		t.Fatalf("error = %v classification = %+v", err, info)
	}
}

func TestDecodeClaudeCLIStreamRejectsToolsHooksSubagentsAndUnknownEvents(t *testing.T) {
	tests := []struct {
		name string
		line string
		code string
	}{
		{name: "init tools", line: `{"type":"system","subtype":"init","tools":["Bash"]}`, code: "reasoner_safety_violation"},
		{name: "hook", line: `{"type":"system","subtype":"hook_started"}`, code: "reasoner_safety_violation"},
		{name: "tool block", line: `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use"}}}`, code: "reasoner_safety_violation"},
		{name: "tool input", line: `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta"}}}`, code: "reasoner_safety_violation"},
		{name: "subagent", line: `{"type":"assistant","parent_tool_use_id":"tool-1","message":{"content":[]}}`, code: "reasoner_safety_violation"},
		{name: "unknown", line: `{"type":"future_event"}`, code: "reasoner_protocol_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeClaudeCLIStream(strings.NewReader(test.line), nil)
			info := classifyProviderError(err)
			if err == nil || info.Code != test.code || info.Retryable || info.Provider != "anthropic" {
				t.Fatalf("error = %v, classification = %+v", err, info)
			}
		})
	}
}

func TestDecodeClaudeCLIStreamClassifiesResultErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		line  string
		code  string
		retry bool
	}{
		{name: "auth", line: `{"type":"result","subtype":"error","is_error":true,"result":"Not logged in · Please run /login"}`, code: "provider_authentication_failed"},
		{name: "rate", line: `{"type":"result","subtype":"error","is_error":true,"api_error_status":429,"result":"request failed"}`, code: "provider_rate_limited", retry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeClaudeCLIStream(strings.NewReader(test.line), nil)
			info := classifyProviderError(err)
			if err == nil || info.Code != test.code || info.Retryable != test.retry {
				t.Fatalf("error = %v, classification = %+v", err, info)
			}
		})
	}
}

func TestClaudeCLIStreamProtocolFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		stream string
	}{
		{name: "malformed", stream: `{`},
		{name: "missing result", stream: `{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`},
		{name: "empty result", stream: `{"type":"result","subtype":"success","result":"  "}`},
		{name: "stream mismatch", stream: strings.Join([]string{
			`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"draft"}}}`,
			`{"type":"result","subtype":"success","result":"final"}`,
		}, "\n")},
		{name: "assistant mismatch", stream: strings.Join([]string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"draft"}]}}`,
			`{"type":"result","subtype":"success","result":"final"}`,
		}, "\n")},
		{name: "duplicate result", stream: strings.Join([]string{
			`{"type":"result","subtype":"success","result":"final"}`,
			`{"type":"result","subtype":"success","result":"final"}`,
		}, "\n")},
		{name: "oversized line", stream: `{"type":"system","subtype":"status","padding":"` + strings.Repeat("x", claudeCLIEventLineLimit) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream, decodeErr := decodeClaudeCLIStream(strings.NewReader(test.stream), nil)
			if decodeErr == nil {
				_, decodeErr = finishClaudeCLIStream(stream, "", nil, "")
			}
			if info := classifyProviderError(decodeErr); decodeErr == nil || info.Code != "reasoner_protocol_error" {
				t.Fatalf("error = %v, classification = %+v", decodeErr, info)
			}
		})
	}
}

func TestFinishClaudeCLIStreamUsesBoundedStderrOnExit(t *testing.T) {
	_, err := finishClaudeCLIStream(claudeCLIStreamResult{}, strings.Repeat("Not logged in ", 100), errors.New("exit status 1"), "")
	if err == nil || len(err.Error()) > 530 {
		t.Fatalf("error length = %d, error = %v", len(err.Error()), err)
	}
	if info := classifyProviderError(err); info.Code != "provider_authentication_failed" {
		t.Fatalf("classification = %+v", info)
	}
}

func TestClaudeCLIReasonerStreamsBeforeProcessExit(t *testing.T) {
	requireUnixShell(t)
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv("CLAUDE_TEST_RELEASE", release)
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0o600) })
	script := writeExecutable(t, filepath.Join(t.TempDir(), "claude"), `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","tools":[]}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"{\"tool\":\"done\",\"summary\":\"hel"}}}'
while [ ! -f "$CLAUDE_TEST_RELEASE" ]; do sleep 0.01; done
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo\"}"}}}'
printf '%s\n' '{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"{\"tool\":\"done\",\"summary\":\"hello\"}"}],"usage":{"input_tokens":2,"output_tokens":3}}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"{\"tool\":\"done\",\"summary\":\"hello\"}","usage":{"input_tokens":2,"output_tokens":3},"modelUsage":{"claude-opus-5":{}}}'
`)
	r := &claudeCLIReasoner{bin: script, workdir: t.TempDir(), timeout: 10 * time.Second}
	updates := make(chan ReasonerStreamUpdate, 4)
	stream := newReasonerStreamController(func(update ReasonerStreamUpdate) { updates <- update })
	ctx := withReasonerStream(withReasoningEffort(context.Background(), "low"), stream)
	resultCh := make(chan struct {
		result ReasonerResult
		err    error
	}, 1)
	go func() {
		result, err := r.ThinkRoutedModel(ctx, "claude-opus-5", "prompt")
		resultCh <- struct {
			result ReasonerResult
			err    error
		}{result: result, err: err}
	}()

	select {
	case update := <-updates:
		if update.Text != "hel" || update.Completed || update.Reset {
			t.Fatalf("first update = %+v", update)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first Claude delta was not published before process completion")
	}
	select {
	case result := <-resultCh:
		t.Fatalf("process completed before delayed delta: %+v", result)
	default:
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}

	finished := <-resultCh
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	if finished.result.Text != `{"tool":"done","summary":"hello"}` || finished.result.Usage.EffectiveReasoningEffort != "low" {
		t.Fatalf("result = %+v", finished.result)
	}
}

func TestClaudeCLIReasonerCancelsProcessTreeOnSafetyEvent(t *testing.T) {
	requireUnixShell(t)
	marker := filepath.Join(t.TempDir(), "continued")
	t.Setenv("CLAUDE_TEST_MARKER", marker)
	script := writeExecutable(t, filepath.Join(t.TempDir(), "claude"), `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","tools":["Bash"]}'
sleep 1
printf continued > "$CLAUDE_TEST_MARKER"
`)
	r := &claudeCLIReasoner{bin: script, workdir: t.TempDir(), timeout: 5 * time.Second}

	started := time.Now()
	_, err := r.ThinkResult(context.Background(), "prompt")
	if info := classifyProviderError(err); err == nil || info.Code != "reasoner_safety_violation" {
		t.Fatalf("error = %v, classification = %+v", err, info)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("tool cancellation took %s", elapsed)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("helper continued after cancellation: %v", err)
	}
}

func TestClaudeCLIReasonerPreservesCancellationAndTimeout(t *testing.T) {
	requireUnixShell(t)
	script := writeExecutable(t, filepath.Join(t.TempDir(), "claude"), "#!/bin/sh\nsleep 5\n")
	for _, test := range []struct {
		name    string
		timeout time.Duration
		ctx     func() (context.Context, context.CancelFunc)
		want    error
	}{
		{
			name:    "caller cancellation",
			timeout: 5 * time.Second,
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(50*time.Millisecond, cancel)
				return ctx, cancel
			},
			want: context.Canceled,
		},
		{
			name:    "reasoner timeout",
			timeout: 50 * time.Millisecond,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()
			r := &claudeCLIReasoner{bin: script, workdir: t.TempDir(), timeout: test.timeout}
			_, err := r.ThinkResult(ctx, "prompt")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
