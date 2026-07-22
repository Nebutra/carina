package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexCLIArgsAreIsolated(t *testing.T) {
	r := &codexCLIReasoner{workdir: "/tmp/empty", model: "gpt-5.4"}
	want := []string{
		"exec",
		"--ephemeral",
		"--json",
		"--sandbox", "read-only",
		"--ignore-user-config",
		"--ignore-rules",
		"--skip-git-repo-check",
		"--disable", "apps",
		"--disable", "browser_use",
		"--disable", "browser_use_external",
		"--disable", "browser_use_full_cdp_access",
		"--disable", "code_mode_host",
		"--disable", "computer_use",
		"--disable", "goals",
		"--disable", "hooks",
		"--disable", "image_generation",
		"--disable", "in_app_browser",
		"--disable", "multi_agent",
		"--disable", "plugin_sharing",
		"--disable", "plugins",
		"--disable", "remote_plugin",
		"--disable", "shell_snapshot",
		"--disable", "shell_tool",
		"--disable", "skill_mcp_dependency_install",
		"--disable", "skill_search",
		"--disable", "tool_call_mcp_elicitation",
		"--disable", "unified_exec",
		"-c", `web_search="disabled"`,
		"-c", "project_doc_max_bytes=0",
		"-C", "/tmp/empty",
		"--model", "gpt-5.4",
		codexCLIReasonerGuard + "\n\nreturn JSON",
	}
	if got := r.args("return JSON"); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v\nwant %#v", got, want)
	}
}

func TestNewCodexCLIReasonerRequiresBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := newCodexCLIReasoner(); err == nil || !strings.Contains(err.Error(), "codex CLI not found on PATH") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewCodexCLIReasonerCleansTemporaryDirectory(t *testing.T) {
	requireUnixShell(t)
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "codex"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir)

	r, err := newCodexCLIReasonerModel("gpt-5.4")
	if err != nil {
		t.Fatalf("new reasoner: %v", err)
	}
	if info, err := os.Stat(r.workdir); err != nil || !info.IsDir() {
		t.Fatalf("workdir = %q: %v", r.workdir, err)
	}
	r.Close()
	r.Close()
	if _, err := os.Stat(r.workdir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workdir still exists: %v", err)
	}
}

func TestNewConfiguredReasonerBuildsCodexModelTiers(t *testing.T) {
	requireUnixShell(t)
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "codex"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir)

	var reasoners []Reasoner
	for _, model := range []string{"primary", "summary", "verifier", "risk"} {
		reasoner, err := newConfiguredReasoner(reasonerBackendCodexCLI, nil, model)
		if err != nil {
			t.Fatalf("newConfiguredReasoner(%q): %v", model, err)
		}
		codex, ok := reasoner.(*codexCLIReasoner)
		if !ok || codex.model != model {
			t.Fatalf("reasoner = %#v, want codex model %q", reasoner, model)
		}
		reasoners = append(reasoners, reasoner)
	}
	closeReasoners(reasoners...)
}

func TestDecodeCodexCLIStreamReturnsFinalMessageAndUsage(t *testing.T) {
	stream, err := decodeCodexCLIStream(strings.NewReader(strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"reasoning","text":"summary"}}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"draft"}}`,
		`{"type":"item.completed","item":{"id":"item-3","type":"agent_message","text":" final "}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":7}}`,
	}, "\n")))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	result, err := finishCodexCLIStream(stream, "", nil, "gpt-5.4")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if result.Text != "final" || result.Usage.Provider != "openai" || result.Usage.Model != "gpt-5.4" || result.Usage.InputTokens != 60 || result.Usage.CacheReadTokens != 40 || result.Usage.OutputTokens != 20 || result.Usage.CacheWriteTokens != 0 || result.Usage.Estimated {
		t.Fatalf("result = %+v", result)
	}
}

func TestDecodeCodexCLIStreamClampsCachedInput(t *testing.T) {
	stream := codexCLIStreamResult{
		text:      "ok",
		completed: true,
		usage:     codexCLIUsage{InputTokens: 3, CachedInputTokens: 9, OutputTokens: -1},
	}
	result, err := finishCodexCLIStream(stream, "", nil, "")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if result.Usage.InputTokens != 0 || result.Usage.CacheReadTokens != 3 || result.Usage.OutputTokens != 0 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestDecodeCodexCLIStreamRejectsToolAndUnknownItems(t *testing.T) {
	for _, itemType := range []string{
		"command_execution",
		"file_change",
		"mcp_tool_call",
		"web_search",
		"app_tool_call",
		"collab_tool_call",
		"browser_use",
		"computer_use",
		"image_generation",
		"future_item",
	} {
		t.Run(itemType, func(t *testing.T) {
			_, err := decodeCodexCLIStream(strings.NewReader(`{"type":"item.started","item":{"type":"` + itemType + `"}}`))
			if err == nil {
				t.Fatal("expected safety error")
			}
			info := classifyProviderError(err)
			if info.Code != "reasoner_safety_violation" || info.Retryable {
				t.Fatalf("classification = %+v", info)
			}
		})
	}
}

func TestDecodeCodexCLIStreamRejectsUnknownEvent(t *testing.T) {
	_, err := decodeCodexCLIStream(strings.NewReader(`{"type":"future.event"}`))
	if info := classifyProviderError(err); err == nil || info.Code != "reasoner_protocol_error" {
		t.Fatalf("error = %v, classification = %+v", err, info)
	}
}

func TestDecodeCodexCLIStreamReturnsStructuredFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		line  string
		code  string
		retry bool
	}{
		{name: "turn failed", line: `{"type":"turn.failed","error":{"message":"Rate limit exceeded"}}`, code: "provider_rate_limited", retry: true},
		{name: "top-level error", line: `{"type":"error","message":"Not logged in"}`, code: "provider_authentication_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeCodexCLIStream(strings.NewReader(test.line))
			info := classifyProviderError(err)
			if info.Code != test.code || info.Retryable != test.retry || info.Provider != "openai" {
				t.Fatalf("error = %v, classification = %+v", err, info)
			}
		})
	}
}

func TestDecodeCodexCLIStreamProtocolFailures(t *testing.T) {
	largeLine := `{"type":"thread.started","padding":"` + strings.Repeat("x", codexCLIEventLineLimit) + `"}`
	largeEvent := `{"type":"thread.started","padding":"` + strings.Repeat("x", 900<<10) + `"}`
	for _, test := range []struct {
		name   string
		stream string
	}{
		{name: "malformed", stream: `{`},
		{name: "missing completed", stream: `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`},
		{name: "empty final", stream: "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"  \"}}\n{\"type\":\"turn.completed\",\"usage\":{}}"},
		{name: "oversized line", stream: largeLine},
		{name: "oversized stream", stream: strings.Repeat(largeEvent+"\n", 5)},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream, decodeErr := decodeCodexCLIStream(strings.NewReader(test.stream))
			if decodeErr == nil {
				_, decodeErr = finishCodexCLIStream(stream, "", nil, "")
			}
			if info := classifyProviderError(decodeErr); decodeErr == nil || info.Code != "reasoner_protocol_error" {
				t.Fatalf("error = %v, classification = %+v", decodeErr, info)
			}
		})
	}
}

func TestDecodeCodexCLIStreamRejectsEmptyLastAgentMessage(t *testing.T) {
	stream, err := decodeCodexCLIStream(strings.NewReader(strings.Join([]string{
		`{"type":"item.completed","item":{"type":"agent_message","text":"draft"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"  "}}`,
		`{"type":"turn.completed","usage":{}}`,
	}, "\n")))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, err = finishCodexCLIStream(stream, "", nil, "")
	if info := classifyProviderError(err); err == nil || info.Code != "reasoner_protocol_error" {
		t.Fatalf("error = %v, classification = %+v", err, info)
	}
}

func TestFinishCodexCLIStreamUsesBoundedStderrOnExit(t *testing.T) {
	_, err := finishCodexCLIStream(codexCLIStreamResult{}, strings.Repeat("x", 900), errors.New("exit status 1"), "")
	if err == nil || len(err.Error()) > 530 {
		t.Fatalf("error length = %d, error = %v", len(err.Error()), err)
	}
}

func TestCodexBoundedBufferCapsCapturedBytes(t *testing.T) {
	buffer := &codexBoundedBuffer{limit: 5}
	if n, err := buffer.Write([]byte("123456789")); err != nil || n != 9 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if n, err := buffer.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("second write = %d, %v", n, err)
	}
	if got := buffer.String(); got != "12345" {
		t.Fatalf("buffer = %q", got)
	}
}

func TestCodexCLIReasonerCancelsOnToolEvent(t *testing.T) {
	requireUnixShell(t)
	marker := filepath.Join(t.TempDir(), "continued")
	t.Setenv("CODEX_TEST_MARKER", marker)
	script := writeExecutable(t, filepath.Join(t.TempDir(), "codex"), `#!/bin/sh
printf '%s\n' '{"type":"item.started","item":{"type":"command_execution"}}'
sleep 1
printf continued > "$CODEX_TEST_MARKER"
`)
	r := &codexCLIReasoner{bin: script, workdir: t.TempDir(), timeout: 5 * time.Second}

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

func TestCodexCLIReasonerPreservesCancellationAndTimeout(t *testing.T) {
	requireUnixShell(t)
	script := writeExecutable(t, filepath.Join(t.TempDir(), "codex"), "#!/bin/sh\nsleep 5\n")
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
			r := &codexCLIReasoner{bin: script, workdir: t.TempDir(), timeout: test.timeout}
			_, err := r.ThinkResult(ctx, "prompt")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCloseReasonersDeduplicatesSharedInstance(t *testing.T) {
	r := &trackingCloseReasoner{}
	closeReasoners(r, r)
	if r.closes != 1 {
		t.Fatalf("closes = %d, want 1", r.closes)
	}
}

func requireUnixShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
}

func writeExecutable(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}

type trackingCloseReasoner struct {
	closes int
}

func (r *trackingCloseReasoner) Name() string { return "tracking" }

func (r *trackingCloseReasoner) Think(context.Context, string) (string, error) {
	return "", nil
}

func (r *trackingCloseReasoner) Close() { r.closes++ }
