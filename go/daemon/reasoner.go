package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	modelrouter "github.com/Nebutra/carina/go/model-router"
)

// Reasoner turns a prompt into the agent's next decision. It is the pure
// "thinking" step — it has NO ability to touch the system. All side effects
// happen in the carina kernel/toolchain after the reasoner decides.
type Reasoner interface {
	Name() string
	// Think returns the model's raw text response to a prompt.
	Think(ctx context.Context, prompt string) (string, error)
}

type modelAwareReasoner interface {
	ThinkModel(ctx context.Context, model, prompt string) (string, error)
}

// ReasonerResult is the optional structured result returned by production
// reasoners. Reasoner intentionally remains unchanged so existing plugins and
// test doubles continue to compile; callers fall back to explicit estimates
// when a reasoner does not implement the richer interface.
type ReasonerResult struct {
	Text  string
	Usage ModelUsage
}

type resultReasoner interface {
	ThinkResult(ctx context.Context, prompt string) (ReasonerResult, error)
}

type modelResultReasoner interface {
	ThinkModelResult(ctx context.Context, model, prompt string) (ReasonerResult, error)
}

type segmentedModelResultReasoner interface {
	ThinkModelSegments(ctx context.Context, model, stablePrefix, volatileSuffix string) (ReasonerResult, error)
}

// mediaSegmentedReasoner is the capability-upgrade interface for reasoners
// that can deliver image parts to the model (same optional-assertion pattern
// as segmentedModelResultReasoner above). Reasoners that don't implement it
// silently receive text only — the transcript already renders every MediaRef
// as a textual placeholder, so degradation is graceful by construction.
type mediaSegmentedReasoner interface {
	ThinkModelSegmentsMedia(ctx context.Context, model, stablePrefix, volatileSuffix string, media []modelrouter.MediaPart) (ReasonerResult, error)
}

// retryBaseDelay is the initial backoff; overridable in tests.
var retryBaseDelay = 2 * time.Second

const retryHeaderMaxDelay = 2 * time.Minute

type providerErrorInfo struct {
	Code, Category, UserAction, CorrelationID, Provider string
	HTTPStatus                                          int
	Retryable                                           bool
}

type providerErrorClassifier interface{ ProviderError() providerErrorInfo }

type retryPolicy struct {
	MaxAttempts int
	MaxElapsed  time.Duration
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	RandFloat64 func() float64
	Now         func() time.Time
}

type retryAttempt struct {
	Attempt, MaxAttempts int
	Delay                time.Duration
	Error                providerErrorInfo
}

type retryObserverKey struct{}

type reasoningEffortContextKey struct{}

const (
	reasonerBackendAuto      = "auto"
	reasonerBackendRouter    = "model-router"
	reasonerBackendClaudeCLI = "claude-cli"
	reasonerBackendNone      = ""
)

func normalizeReasonerBackend(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", reasonerBackendAuto:
		return reasonerBackendAuto, nil
	case "router", reasonerBackendRouter:
		return reasonerBackendRouter, nil
	case "claude", reasonerBackendClaudeCLI:
		return reasonerBackendClaudeCLI, nil
	default:
		return "", fmt.Errorf("unsupported CARINA_REASONER_BACKEND %q (want auto, model-router, or claude-cli)", value)
	}
}

func selectReasonerBackend(offline bool, configuredBackend, _ string, hasRunnableProvider bool) string {
	if offline {
		return reasonerBackendNone
	}
	switch configuredBackend {
	case reasonerBackendRouter, reasonerBackendClaudeCLI:
		return configuredBackend
	case reasonerBackendAuto:
		if hasRunnableProvider {
			return reasonerBackendRouter
		}
	}
	return reasonerBackendNone
}

func withReasoningEffort(ctx context.Context, effort string) context.Context {
	return context.WithValue(ctx, reasoningEffortContextKey{}, normalizeReasoningEffort(effort))
}

func reasoningEffortFrom(ctx context.Context) string {
	effort, _ := ctx.Value(reasoningEffortContextKey{}).(string)
	return effort
}

func withRetryObserver(ctx context.Context, observer func(retryAttempt)) context.Context {
	return context.WithValue(ctx, retryObserverKey{}, observer)
}

func defaultRetryPolicy() retryPolicy {
	return retryPolicy{MaxAttempts: 4, MaxElapsed: 2 * time.Minute, BaseDelay: retryBaseDelay, MaxDelay: 30 * time.Second, RandFloat64: rand.Float64, Now: time.Now}
}

type retryAfterProvider interface {
	RetryAfter() (time.Duration, bool)
}

// thinkWithRetry wraps a reasoner call with exponential backoff — transport
// errors (rate limits, 5xx, timeouts) are retried; the caller's context
// bounds total time. This fixes the "Think error => task dies" gap.
func thinkWithRetry(ctx context.Context, r Reasoner, prompt string) (string, error) {
	return thinkWithRetryModel(ctx, r, "", prompt)
}

func thinkWithRetryModel(ctx context.Context, r Reasoner, model, prompt string) (string, error) {
	result, err := thinkWithRetryModelResult(ctx, r, model, prompt)
	return result.Text, err
}

func thinkWithRetryModelResult(ctx context.Context, r Reasoner, model, prompt string) (ReasonerResult, error) {
	return thinkWithRetrySegments(ctx, r, model, prompt, "", "")
}

func thinkWithRetryModelSegments(ctx context.Context, r Reasoner, model string, segments promptSegments) (ReasonerResult, error) {
	return thinkWithRetrySegments(ctx, r, model, segments.full(), segments.StablePrefix, segments.VolatileSuffix, segments.Media...)
}

func thinkWithRetrySegments(ctx context.Context, r Reasoner, model, prompt, stablePrefix, volatileSuffix string, media ...modelrouter.MediaPart) (ReasonerResult, error) {
	return thinkWithRetryPolicy(ctx, r, model, prompt, stablePrefix, volatileSuffix, defaultRetryPolicy(), media...)
}

func thinkWithRetryPolicy(ctx context.Context, r Reasoner, model, prompt, stablePrefix, volatileSuffix string, policy retryPolicy, media ...modelrouter.MediaPart) (ReasonerResult, error) {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = retryBaseDelay
	}
	if policy.MaxDelay <= 0 {
		policy.MaxDelay = 30 * time.Second
	}
	if policy.RandFloat64 == nil {
		policy.RandFloat64 = rand.Float64
	}
	if policy.Now == nil {
		policy.Now = time.Now
	}
	started := policy.Now()
	delay := policy.BaseDelay
	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		governance, provider := retryGovernanceFrom(ctx)
		if governance != nil {
			if err := governance.admit(provider, attempt > 1); err != nil {
				return ReasonerResult{}, err
			}
		}
		out, err := thinkOnceResult(ctx, r, model, prompt, stablePrefix, volatileSuffix, media...)
		if err == nil {
			if governance != nil {
				governance.observe(provider, providerErrorInfo{}, true)
			}
			return out, nil
		}
		lastErr = err
		info := classifyProviderError(err)
		if governance != nil {
			governance.observe(provider, info, false)
		}
		if !info.Retryable || attempt == policy.MaxAttempts {
			break
		}
		wait := retryDelay(lastErr, delay)
		if _, header := retryAfterFromError(lastErr); !header {
			wait = time.Duration(policy.RandFloat64() * float64(wait))
		}
		if policy.MaxElapsed > 0 && policy.Now().Sub(started)+wait > policy.MaxElapsed {
			break
		}
		if observer, ok := ctx.Value(retryObserverKey{}).(func(retryAttempt)); ok {
			observer(retryAttempt{Attempt: attempt, MaxAttempts: policy.MaxAttempts, Delay: wait, Error: info})
		}
		select {
		case <-ctx.Done():
			return ReasonerResult{}, ctx.Err()
		case <-time.After(wait):
		}
		delay *= 2
		if delay > policy.MaxDelay {
			delay = policy.MaxDelay
		}
	}
	return ReasonerResult{}, fmt.Errorf("reasoner failed: %w", lastErr)
}

func retryAfterFromError(err error) (time.Duration, bool) {
	var p retryAfterProvider
	if errors.As(err, &p) {
		return p.RetryAfter()
	}
	return 0, false
}

func classifyProviderError(err error) providerErrorInfo {
	if err == nil {
		return providerErrorInfo{}
	}
	if errors.Is(err, context.Canceled) {
		return providerErrorInfo{Code: "request_cancelled", Category: "timeout", Retryable: false}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return providerErrorInfo{Code: "request_deadline_exceeded", Category: "timeout", Retryable: false}
	}
	if errors.Is(err, errCircuitOpen) {
		return providerErrorInfo{Code: "provider_circuit_open", Category: "unavailable", UserAction: "wait for the provider circuit probe or choose another provider", Retryable: false}
	}
	if errors.Is(err, errRetryBudgetExceeded) {
		return providerErrorInfo{Code: "retry_budget_exhausted", Category: "unavailable", UserAction: "wait for the daemon-local retry budget to refill", Retryable: false}
	}
	if errors.Is(err, errRetryPressure) {
		return providerErrorInfo{Code: "retry_paused_by_backpressure", Category: "unavailable", UserAction: "wait for scheduler pressure to recover", Retryable: false}
	}
	var classified providerErrorClassifier
	if errors.As(err, &classified) {
		return classified.ProviderError()
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return providerErrorInfo{Code: "provider_transport_error", Category: "unavailable", Retryable: true}
	}
	return providerErrorInfo{Code: "reasoner_internal_error", Category: "internal", Retryable: false}
}

func retryDelay(err error, fallback time.Duration) time.Duration {
	var retryable retryAfterProvider
	if err != nil && errors.As(err, &retryable) {
		if d, ok := retryable.RetryAfter(); ok && d > 0 {
			if d > retryHeaderMaxDelay {
				return retryHeaderMaxDelay
			}
			return d
		}
	}
	return fallback
}

func thinkOnce(ctx context.Context, r Reasoner, model, prompt string) (string, error) {
	result, err := thinkOnceResult(ctx, r, model, prompt, "", "")
	return result.Text, err
}

func thinkOnceResult(ctx context.Context, r Reasoner, model, prompt, stablePrefix, volatileSuffix string, media ...modelrouter.MediaPart) (ReasonerResult, error) {
	// Media-capable reasoners get the image parts alongside the segments.
	// Everything below this block drops media silently — the transcript
	// already carries a textual placeholder per MediaRef, so a text-only
	// reasoner (claude-cli, scripted test reasoners) degrades gracefully.
	if len(media) > 0 {
		if mr, ok := r.(mediaSegmentedReasoner); ok {
			result, err := mr.ThinkModelSegmentsMedia(ctx, model, stablePrefix, volatileSuffix, media)
			return normalizeReasonerResult(result, err, r, model, prompt)
		}
	}
	if stablePrefix != "" {
		if sr, ok := r.(segmentedModelResultReasoner); ok {
			result, err := sr.ThinkModelSegments(ctx, model, stablePrefix, volatileSuffix)
			return normalizeReasonerResult(result, err, r, model, prompt)
		}
	}
	if model != "" {
		if mr, ok := r.(modelResultReasoner); ok {
			result, err := mr.ThinkModelResult(ctx, model, prompt)
			return normalizeReasonerResult(result, err, r, model, prompt)
		}
		if mr, ok := r.(modelAwareReasoner); ok {
			out, err := mr.ThinkModel(ctx, model, prompt)
			return estimatedReasonerResult(out, err, r.Name(), model, prompt)
		}
	}
	if rr, ok := r.(resultReasoner); ok {
		result, err := rr.ThinkResult(ctx, prompt)
		return normalizeReasonerResult(result, err, r, model, prompt)
	}
	out, err := r.Think(ctx, prompt)
	return estimatedReasonerResult(out, err, r.Name(), model, prompt)
}

func normalizeReasonerResult(result ReasonerResult, err error, r Reasoner, model, prompt string) (ReasonerResult, error) {
	if err != nil {
		return ReasonerResult{}, err
	}
	if result.Usage.Provider == "" {
		result.Usage.Provider = r.Name()
	}
	if result.Usage.Model == "" {
		result.Usage.Model = model
	}
	if result.Usage.totalTokens() == 0 {
		result.Usage.InputTokens = estimateTokens(prompt)
		result.Usage.OutputTokens = estimateTokens(result.Text)
		result.Usage.Estimated = true
	}
	return result, nil
}

func estimatedReasonerResult(out string, err error, provider, model, prompt string) (ReasonerResult, error) {
	if err != nil {
		return ReasonerResult{}, err
	}
	return ReasonerResult{Text: out, Usage: ModelUsage{
		Provider: provider, Model: model, InputTokens: estimateTokens(prompt),
		OutputTokens: estimateTokens(out), Estimated: true,
	}}, nil
}

// ---- model-router reasoner ------------------------------------------------

type routerReasoner struct {
	router *modelrouter.Router
	model  string
}

func newRouterReasoner(router *modelrouter.Router, model string) *routerReasoner {
	return &routerReasoner{router: router, model: model}
}

func (r *routerReasoner) Name() string { return "model-router" }

func (r *routerReasoner) Think(ctx context.Context, prompt string) (string, error) {
	return r.ThinkModel(ctx, r.model, prompt)
}

func (r *routerReasoner) ThinkModel(ctx context.Context, model, prompt string) (string, error) {
	result, err := r.ThinkModelResult(ctx, model, prompt)
	return result.Text, err
}

func (r *routerReasoner) ThinkResult(ctx context.Context, prompt string) (ReasonerResult, error) {
	return r.ThinkModelResult(ctx, r.model, prompt)
}

func (r *routerReasoner) ThinkModelResult(ctx context.Context, model, prompt string) (ReasonerResult, error) {
	return r.complete(ctx, model, modelrouter.Request{Model: model, Prompt: prompt, ReasoningEffort: reasoningEffortFrom(ctx)})
}

func (r *routerReasoner) ThinkModelSegments(ctx context.Context, model, stablePrefix, volatileSuffix string) (ReasonerResult, error) {
	return r.complete(ctx, model, modelrouter.Request{
		Model: model, Prompt: stablePrefix + volatileSuffix,
		ReasoningEffort: reasoningEffortFrom(ctx),
		StablePrefix:    stablePrefix, VolatileSuffix: volatileSuffix,
	})
}

func (r *routerReasoner) ThinkModelSegmentsMedia(ctx context.Context, model, stablePrefix, volatileSuffix string, media []modelrouter.MediaPart) (ReasonerResult, error) {
	prompt := stablePrefix + volatileSuffix
	if prompt == "" {
		prompt = volatileSuffix
	}
	return r.complete(ctx, model, modelrouter.Request{
		Model: model, Prompt: prompt,
		ReasoningEffort: reasoningEffortFrom(ctx),
		StablePrefix:    stablePrefix, VolatileSuffix: volatileSuffix,
		Media: media,
	})
}

func (r *routerReasoner) complete(ctx context.Context, model string, req modelrouter.Request) (ReasonerResult, error) {
	if strings.TrimSpace(model) == "" {
		model = "default"
		req.Model = model
	}
	resp, err := r.router.Complete(ctx, req)
	if err != nil {
		return ReasonerResult{}, err
	}
	if resp.Provider == "mock" {
		return ReasonerResult{}, fmt.Errorf("model-router: no runtime model provider resolved")
	}
	return ReasonerResult{Text: strings.TrimSpace(resp.Text), Usage: ModelUsage{
		Provider: resp.Provider, Model: resp.Model, InputTokens: resp.InputTokens,
		OutputTokens: resp.OutputTokens, CacheReadTokens: resp.CacheReadTokens,
		CacheWriteTokens:         resp.CacheWriteTokens,
		EffectiveReasoningEffort: resp.EffectiveReasoningEffort,
	}}, nil
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

type claudeCLIResponse struct {
	Result         string `json:"result"`
	IsError        bool   `json:"is_error"`
	Subtype        string `json:"subtype"`
	Model          string `json:"model"`
	APIErrorStatus *int   `json:"api_error_status"`
	Usage          struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type claudeCLIError struct {
	message string
	subtype string
	status  int
}

func (e claudeCLIError) Error() string {
	if message := boundedMetadata(e.message, 500); message != "" {
		return "claude reasoner: " + message
	}
	if subtype := boundedMetadata(e.subtype, 500); subtype != "" && subtype != "success" {
		return "claude reasoner: " + subtype
	}
	return "claude reasoner failed"
}

func (e claudeCLIError) ProviderError() providerErrorInfo {
	if e.status > 0 {
		return providerStatusError{provider: "anthropic", status: e.status}.ProviderError()
	}
	message := strings.ToLower(e.message + " " + e.subtype)
	switch {
	case strings.Contains(message, "not logged in"),
		strings.Contains(message, "please run /login"),
		strings.Contains(message, "authentication"),
		strings.Contains(message, "unauthorized"),
		strings.Contains(message, "invalid api key"):
		return providerErrorInfo{
			Code:       "provider_authentication_failed",
			Category:   "authentication",
			Provider:   "anthropic",
			UserAction: "run `claude auth login` or configure the Claude CLI credential",
		}
	case strings.Contains(message, "rate limit"):
		return providerErrorInfo{
			Code:       "provider_rate_limited",
			Category:   "rate_limit",
			Provider:   "anthropic",
			Retryable:  true,
			UserAction: "wait or choose another provider",
		}
	case strings.Contains(message, "quota"),
		strings.Contains(message, "credit balance"),
		strings.Contains(message, "billing"):
		return providerErrorInfo{
			Code:       "provider_quota_exhausted",
			Category:   "rate_limit",
			Provider:   "anthropic",
			UserAction: "increase quota or choose another provider",
		}
	case strings.Contains(message, "overloaded"),
		strings.Contains(message, "temporarily unavailable"):
		return providerErrorInfo{
			Code:      "provider_unavailable",
			Category:  "unavailable",
			Provider:  "anthropic",
			Retryable: true,
		}
	default:
		return providerErrorInfo{
			Code:       "reasoner_internal_error",
			Category:   "internal",
			Provider:   "anthropic",
			UserAction: "run `claude auth status` and inspect the Claude CLI configuration",
		}
	}
}

func newClaudeCLIReasoner() (*claudeCLIReasoner, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude CLI not found on PATH: %w", err)
	}
	dir, err := os.MkdirTemp("", "carina-reasoner-")
	if err != nil {
		return nil, err
	}
	return &claudeCLIReasoner{
		bin:     bin,
		model:   os.Getenv("CARINA_REASONER_MODEL"),
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

func newConfiguredReasoner(backend string, router *modelrouter.Router, model string) (Reasoner, error) {
	switch backend {
	case reasonerBackendRouter:
		return newRouterReasoner(router, nonempty(strings.TrimSpace(model), "default")), nil
	case reasonerBackendClaudeCLI:
		if strings.TrimSpace(model) != "" {
			return newClaudeCLIReasonerModel(strings.TrimSpace(model))
		}
		return newClaudeCLIReasoner()
	default:
		return nil, nil
	}
}

func (r *claudeCLIReasoner) Name() string { return "claude-cli" }

func (r *claudeCLIReasoner) Think(ctx context.Context, prompt string) (string, error) {
	result, err := r.ThinkResult(ctx, prompt)
	return result.Text, err
}

func (r *claudeCLIReasoner) ThinkResult(ctx context.Context, prompt string) (ReasonerResult, error) {
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

	out, runErr := cmd.Output()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ReasonerResult{}, ctxErr
	}
	var stderr []byte
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		stderr = exitErr.Stderr
	}
	return decodeClaudeCLIOutput(out, stderr, runErr, r.model)
}

func decodeClaudeCLIOutput(out, stderr []byte, runErr error, fallbackModel string) (ReasonerResult, error) {
	var resp claudeCLIResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		if runErr != nil {
			message := boundedMetadata(string(stderr), 500)
			if message == "" {
				message = boundedMetadata(string(out), 500)
			}
			if message == "" {
				message = runErr.Error()
			}
			return ReasonerResult{}, claudeCLIError{message: message}
		}
		return ReasonerResult{}, fmt.Errorf("claude reasoner: decode: %w (%s)", err, truncate(string(out), 200))
	}
	if resp.IsError || runErr != nil {
		message := resp.Result
		if strings.TrimSpace(message) == "" {
			message = string(stderr)
		}
		if strings.TrimSpace(message) == "" && runErr != nil {
			message = runErr.Error()
		}
		status := 0
		if resp.APIErrorStatus != nil {
			status = *resp.APIErrorStatus
		}
		return ReasonerResult{}, claudeCLIError{message: message, subtype: resp.Subtype, status: status}
	}
	model := resp.Model
	if model == "" {
		model = fallbackModel
	}
	return ReasonerResult{Text: strings.TrimSpace(resp.Result), Usage: ModelUsage{
		Provider: "anthropic", Model: model, InputTokens: resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
		CacheWriteTokens: resp.Usage.CacheCreationTokens,
	}}, nil
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
		return "", providerStatusError{provider: "flaky", status: 503, requestID: fmt.Sprintf("retry-%d", f.calls)}
	}
	return f.then, nil
}
