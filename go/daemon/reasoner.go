package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Reasoner turns a prompt into the agent's next decision. It is the pure
// "thinking" step — it has NO ability to touch the system. All side effects
// happen in the carina kernel/toolchain after the reasoner decides.
type Reasoner interface {
	Name() string
	// Think returns the model's raw text response to a prompt.
	Think(ctx context.Context, prompt string) (string, error)
}

// retryBaseDelay is the initial backoff; overridable in tests.
var retryBaseDelay = 2 * time.Second

// thinkWithRetry wraps a reasoner call with exponential backoff — transport
// errors (rate limits, 5xx, timeouts) are retried; the caller's context
// bounds total time. This fixes the "Think error => task dies" gap.
func thinkWithRetry(ctx context.Context, r Reasoner, prompt string) (string, error) {
	const maxAttempts = 4
	delay := retryBaseDelay
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, err := r.Think(ctx, prompt)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
	return "", fmt.Errorf("reasoner failed after %d attempts: %w", maxAttempts, lastErr)
}

// ---- claude CLI reasoner ---------------------------------------------------

// claudeCLIReasoner uses the local `claude` binary in headless mode as a pure
// inference engine. Claude Code's OWN tools are disabled (--allowedTools "")
// and it runs in an isolated, empty cwd, so it cannot touch the workspace —
// it can only reason and emit a decision. This lets carina use the CC Switch /
// Mox gateway (which only admits the Claude Code client) while keeping every
// real side effect inside the carina capability kernel.
type claudeCLIReasoner struct {
	bin     string
	model   string // optional --model override
	workdir string // isolated empty dir
	timeout time.Duration
}

func newClaudeCLIReasoner() (*claudeCLIReasoner, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude CLI not found on PATH: %w", err)
	}
	dir, err := os.MkdirTemp("", "pi-reasoner-")
	if err != nil {
		return nil, err
	}
	return &claudeCLIReasoner{
		bin:     bin,
		model:   os.Getenv("PI_REASONER_MODEL"),
		workdir: dir,
		timeout: 180 * time.Second,
	}, nil
}

// newClaudeCLIReasonerModel builds a claude reasoner pinned to a specific
// model — used for the cheaper summarization/compaction tier.
func newClaudeCLIReasonerModel(model string) (*claudeCLIReasoner, error) {
	r, err := newClaudeCLIReasoner()
	if err != nil {
		return nil, err
	}
	r.model = model
	return r, nil
}

func (r *claudeCLIReasoner) Name() string { return "claude-cli" }

func (r *claudeCLIReasoner) Think(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// --allowedTools "" disables Claude Code's own tools; --permission-mode
	// deny is a belt-and-suspenders guard. Running in an empty temp cwd means
	// even if it tried, there is nothing to touch.
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--allowedTools", "",
	}
	if r.model != "" {
		args = append(args, "--model", r.model)
	}
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Dir = r.workdir
	// Inherit the environment (ANTHROPIC_BASE_URL / AUTH_TOKEN from CC Switch).
	cmd.Env = os.Environ()

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude reasoner: %w", err)
	}
	var resp struct {
		Result   string `json:"result"`
		IsError  bool   `json:"is_error"`
		Subtype  string `json:"subtype"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("claude reasoner: decode: %w (%s)", err, truncate(string(out), 200))
	}
	if resp.IsError {
		return "", fmt.Errorf("claude reasoner error: %s", resp.Subtype)
	}
	return strings.TrimSpace(resp.Result), nil
}

func (r *claudeCLIReasoner) Close() {
	_ = os.RemoveAll(r.workdir)
}

// ---- mock reasoner (offline) ----------------------------------------------

// scriptedReasoner replays a fixed decision sequence. Used by tests to drive
// the full agent loop deterministically without a model.
type scriptedReasoner struct {
	steps []string
	i     int
}

func (s *scriptedReasoner) Name() string { return "scripted" }
func (s *scriptedReasoner) Think(_ context.Context, _ string) (string, error) {
	if s.i >= len(s.steps) {
		return `{"thought":"done","action":{"tool":"done","summary":"no more steps"}}`, nil
	}
	step := s.steps[s.i]
	s.i++
	return step, nil
}

// flakyReasoner fails its first `failFirst` calls, then returns `then`.
// Used to test transport retry.
type flakyReasoner struct {
	failFirst int
	then      string
	calls     int
}

func (f *flakyReasoner) Name() string { return "flaky" }
func (f *flakyReasoner) Think(_ context.Context, _ string) (string, error) {
	f.calls++
	if f.calls <= f.failFirst {
		return "", fmt.Errorf("simulated transport error %d", f.calls)
	}
	return f.then, nil
}
