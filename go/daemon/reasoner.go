package daemon

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
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
	Text      string
	Usage     ModelUsage
	ToolCalls []modelrouter.ToolCall
}

type nativeToolsContextKey struct{}

func withNativeTools(ctx context.Context, tools []modelrouter.ToolSpec) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(tools) == 0 {
		return ctx
	}
	return context.WithValue(ctx, nativeToolsContextKey{}, tools)
}

func nativeToolsFrom(ctx context.Context) []modelrouter.ToolSpec {
	if ctx == nil {
		return nil
	}
	tools, _ := ctx.Value(nativeToolsContextKey{}).([]modelrouter.ToolSpec)
	return tools
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

// dedicatedProviderRouteReasoner marks reasoners that can safely execute a
// provider route outside the generic model-router provider registry. The
// marker is intentionally unexported so a configured backend cannot claim a
// route merely by sharing a display name such as "model-router".
type dedicatedProviderRouteReasoner interface {
	supportsDedicatedProviderRoute(providerID string) bool
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
	reasonerBackendCodexCLI  = "codex-cli"
	reasonerBackendGrokCLI   = "grok-cli"
	reasonerBackendNone      = ""
)

var claudeCLIAvailable = func() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

func runtimeProviderUsesClaudeCLI(info provider.Info) bool {
	return info.Source != nil &&
		info.Source.Kind == provider.CCSwitchSourceKind &&
		info.Source.CredentialOwner == provider.CCSwitchCredentialOwnerClaudeCode
}

func normalizeReasonerBackend(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", reasonerBackendAuto:
		return reasonerBackendAuto, nil
	case "router", reasonerBackendRouter:
		return reasonerBackendRouter, nil
	case "claude", reasonerBackendClaudeCLI:
		return reasonerBackendClaudeCLI, nil
	case "codex", reasonerBackendCodexCLI:
		return reasonerBackendCodexCLI, nil
	default:
		return "", fmt.Errorf("unsupported CARINA_REASONER_BACKEND %q (want auto, model-router, claude-cli, or codex-cli)", value)
	}
}

func selectReasonerBackend(offline bool, configuredBackend string) string {
	if offline {
		return reasonerBackendNone
	}
	switch configuredBackend {
	case reasonerBackendRouter, reasonerBackendClaudeCLI, reasonerBackendCodexCLI:
		return configuredBackend
	case reasonerBackendAuto:
		// Keep the router reasoner stable for the daemon lifetime. Provider
		// readiness is dynamic because credentials can change while it runs.
		return reasonerBackendRouter
	}
	return reasonerBackendNone
}

func (d *Daemon) reasonerReady() bool {
	if d.reasoner == nil {
		return false
	}
	if d.reasoner.Name() != reasonerBackendRouter {
		return true
	}
	catalog := d.providerCatalog
	if !d.offline && !d.disabledProviders[provider.GrokBuildProviderID] {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		discovery := provider.DetectGrokBuild(ctx)
		cancel()
		catalog = provider.MergeGrokBuildProvider(catalog, discovery)
	}
	return hasRunnableRuntimeProviderSet(catalog, d.disabledProviders, d.authStore)
}

func (d *Daemon) canExecuteDedicatedProviderRoute(providerID string) bool {
	if d == nil || d.offline || d.reasoner == nil {
		return false
	}
	backend := strings.TrimSpace(d.reasonerBackend)
	if backend == "" {
		backend = strings.TrimSpace(d.reasoner.Name())
	}
	if backend != strings.TrimSpace(d.reasoner.Name()) {
		return false
	}
	return reasonerSupportsDedicatedProviderRoute(d.reasoner, providerID)
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
		if stream := reasonerStreamFrom(ctx); stream != nil {
			stream.reset()
		}
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
	if errors.Is(err, errReasoningEffortUnsupported) {
		return providerErrorInfo{
			Code: "provider_reasoning_effort_unsupported", Category: "compatibility", Retryable: false,
			UserAction: "clear reasoning effort or choose a model that supports it",
		}
	}
	// Stream budget / idle failures before generic net.Error: mid-body
	// Client.Timeout must not auto-retry (duplicate side effects + cost).
	var streamErr providerStreamError
	if errors.As(err, &streamErr) {
		return streamErr.ProviderError()
	}
	if msg := strings.ToLower(err.Error()); strings.Contains(msg, "while reading body") ||
		(strings.Contains(msg, "client.timeout") && strings.Contains(msg, "body")) {
		return providerErrorInfo{
			Code: "provider_stream_budget_exceeded", Category: "timeout", Retryable: false,
			UserAction: "check the model proxy and network, then retry explicitly",
		}
	}
	if msg := strings.ToLower(err.Error()); strings.Contains(msg, "reasoning effort is not supported") ||
		(strings.Contains(msg, "reasoning effort") && strings.Contains(msg, "not supported by this adapter")) {
		return providerErrorInfo{
			Code: "provider_reasoning_effort_unsupported", Category: "compatibility", Retryable: false,
			UserAction: "clear reasoning effort or choose a model that supports it",
		}
	}
	if msg := strings.ToLower(err.Error()); strings.Contains(msg, "reasoning effort") &&
		(strings.Contains(msg, "is invalid") || strings.Contains(msg, "supported values")) {
		return providerErrorInfo{
			Code: "provider_reasoning_effort_invalid", Category: "compatibility", Retryable: false,
			UserAction: "choose a supported reasoning effort for this model",
		}
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

// operatorFacingReasonerError turns a reasoner/provider failure into VOICE copy
// for ExecutionFailed.reason. Technical stacks stay on RoutingOutcome audit only.
func operatorFacingReasonerError(err error) string {
	if err == nil {
		return "The model could not complete this turn."
	}
	var streamErr providerStreamError
	if errors.As(err, &streamErr) {
		return streamErr.Error()
	}
	info := classifyProviderError(err)
	switch info.Code {
	case "provider_stream_budget_exceeded":
		return "The model stream stopped before finishing. Not auto-retried. Check the proxy and network, then retry explicitly if needed."
	case "provider_stream_unavailable", "provider_transport_error":
		return joinOperatorSentence("The model provider was temporarily unavailable", info.UserAction)
	case "provider_stream_request_failed", "provider_stream_failed":
		return joinOperatorSentence("The model stream request failed", info.UserAction)
	case "provider_circuit_open":
		return joinOperatorSentence("The model provider circuit is open", info.UserAction)
	case "retry_budget_exhausted":
		return joinOperatorSentence("Local provider retry budget is exhausted", info.UserAction)
	case "retry_paused_by_backpressure":
		return joinOperatorSentence("Provider retries are paused under backpressure", info.UserAction)
	case "provider_rate_limited", "provider_quota_exhausted":
		return joinOperatorSentence("The model provider rate-limited or quota-blocked the request", info.UserAction)
	case "provider_authentication_failed", "provider_credential_missing":
		return joinOperatorSentence("The model provider rejected the credential", info.UserAction)
	case "provider_permission_denied":
		return joinOperatorSentence("The model provider denied permission for this request", info.UserAction)
	case "request_cancelled":
		return "The model request was cancelled."
	case "request_deadline_exceeded":
		return "The model request hit a deadline before finishing. Retry explicitly if needed."
	case "provider_client_restricted":
		return joinOperatorSentence("The model endpoint rejected this client type", info.UserAction)
	case "provider_reasoning_effort_unsupported":
		return joinOperatorSentence("Reasoning effort is not supported for this model route", info.UserAction)
	case "provider_reasoning_effort_invalid":
		return joinOperatorSentence("The selected reasoning effort is not valid for this model", info.UserAction)
	}
	if info.UserAction != "" {
		return joinOperatorSentence("The model could not complete this turn", info.UserAction)
	}
	// Last resort: strip internal prefixes without dumping multi-hop stacks.
	msg := err.Error()
	for _, prefix := range []string{
		"reasoner failed: ",
		"modelrouter: all providers failed: ",
		"modelrouter: ",
	} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	if i := strings.Index(msg, ": "); i > 0 && strings.Count(msg, ": ") >= 2 {
		// Prefer the rightmost human-ish clause when still nested.
		parts := strings.Split(msg, ": ")
		if last := strings.TrimSpace(parts[len(parts)-1]); len(last) >= 12 && len(last) <= 180 {
			msg = last
		}
	}
	if len(msg) > 220 {
		msg = msg[:217] + "..."
	}
	return "The model could not complete this turn. " + msg
}

func joinOperatorSentence(what, action string) string {
	what = strings.TrimSpace(what)
	action = strings.TrimSpace(action)
	if action == "" {
		return what + "."
	}
	if strings.HasSuffix(action, ".") {
		return what + ". " + action
	}
	return what + ". " + action + "."
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
	if err := validateDedicatedReasonerRoute(r, model); err != nil {
		return ReasonerResult{}, err
	}
	// Media-capable reasoners get the image parts alongside the segments.
	// Everything below this block drops media silently — the transcript
	// already carries a textual placeholder per MediaRef, so a text-only
	// reasoner (CLI adapters, scripted test reasoners) degrades gracefully.
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

func reasonerSupportsDedicatedProviderRoute(r Reasoner, providerID string) bool {
	routeReasoner, ok := r.(dedicatedProviderRouteReasoner)
	return ok && routeReasoner.supportsDedicatedProviderRoute(providerID)
}

func validateDedicatedReasonerRoute(r Reasoner, model string) error {
	providerID, _, targeted := strings.Cut(strings.TrimSpace(model), "/")
	if !targeted || !strings.EqualFold(providerID, provider.GrokBuildProviderID) {
		return nil
	}
	if providerID != provider.GrokBuildProviderID {
		return fmt.Errorf("model provider %q is not canonical; use %q", providerID, provider.GrokBuildProviderID)
	}
	if !reasonerSupportsDedicatedProviderRoute(r, providerID) {
		backend := "unavailable"
		if r != nil && strings.TrimSpace(r.Name()) != "" {
			backend = strings.TrimSpace(r.Name())
		}
		return fmt.Errorf("reasoner backend %q cannot execute model provider %q; use %s", backend, providerID, reasonerBackendRouter)
	}
	return nil
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
	router             *modelrouter.Router
	model              string
	providerCatalog    provider.Catalog
	disabledProviders  map[string]bool
	grokBuildMu        sync.Mutex
	grokBuild          *grokBuildDelegateEntry
	grokBuildClosed    bool
	grokBuildFactory   func(provider.GrokBuildDiscovery) (routedGrokBuildReasoner, error)
	grokBuildDiscovery func(context.Context) provider.GrokBuildDiscovery
	claudeCodeMu       sync.Mutex
	claudeCode         routedClaudeCodeReasoner
	claudeCodeFactory  func() (routedClaudeCodeReasoner, error)
}

func newRouterReasoner(router *modelrouter.Router, model string) *routerReasoner {
	return &routerReasoner{
		router: router,
		model:  model,
		grokBuildFactory: func(discovery provider.GrokBuildDiscovery) (routedGrokBuildReasoner, error) {
			if discovery.State != provider.GrokBuildStateReady || discovery.BinaryPath == "" {
				return nil, errors.New(nonempty(discovery.Reason, "Grok Build is not ready"))
			}
			reasoner, err := newGrokCLIReasoner(discovery.BinaryPath)
			if err == nil {
				reasoner.version = discovery.Version
			}
			return reasoner, err
		},
		grokBuildDiscovery: provider.DetectGrokBuild,
		claudeCodeFactory: func() (routedClaudeCodeReasoner, error) {
			reasoner, err := newClaudeCLIReasoner()
			if reasoner != nil {
				reasoner.model = ""
			}
			return reasoner, err
		},
	}
}

func newRouterReasonerWithCatalog(router *modelrouter.Router, model string, catalog provider.Catalog, disabled ...map[string]bool) *routerReasoner {
	reasoner := newRouterReasoner(router, model)
	reasoner.providerCatalog = catalog
	if len(disabled) != 0 {
		reasoner.disabledProviders = disabled[0]
	}
	return reasoner
}

func (r *routerReasoner) Name() string { return "model-router" }

func (r *routerReasoner) supportsDedicatedProviderRoute(providerID string) bool {
	return providerID == provider.GrokBuildProviderID
}

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
	if tools := nativeToolsFrom(ctx); len(tools) > 0 {
		req.Tools = tools
	}
	if cliModel, discovery, targeted, routeErr := r.grokBuildRoute(ctx, model); targeted {
		if routeErr != nil {
			return ReasonerResult{}, routeErr
		}
		if len(req.Media) != 0 {
			return ReasonerResult{}, grokCLIError{message: "Grok Build accepts text input only", kind: "protocol"}
		}
		delegate, release, err := r.grokBuildDelegate(discovery)
		if err != nil {
			return ReasonerResult{}, grokCLIError{message: err.Error()}
		}
		defer release()
		result, err := delegate.ThinkRoutedModel(ctx, cliModel, req.Prompt)
		if err != nil {
			if info := classifyProviderError(err); info.Category == "authentication" {
				provider.InvalidateGrokBuildDiscovery()
			}
			return ReasonerResult{}, err
		}
		result.Usage.Provider = provider.GrokBuildProviderID
		return result, nil
	}
	if providerID, cliModel, ok := r.claudeCodeRoute(model); ok {
		delegate, err := r.claudeCodeDelegate()
		if err != nil {
			return ReasonerResult{}, claudeCLIError{message: err.Error()}
		}
		result, err := delegate.ThinkRoutedModel(ctx, cliModel, req.Prompt)
		if err != nil {
			return ReasonerResult{}, err
		}
		result.Usage.Provider = providerID
		return result, nil
	}
	var resp *modelrouter.Response
	var err error
	if stream := reasonerStreamFrom(ctx); stream != nil {
		decoder := &actionEnvelopeStreamDecoder{}
		resp, err = r.router.Stream(ctx, req, func(event modelrouter.StreamEvent) {
			if event.Reset {
				decoder.Reset()
				stream.reset()
				return
			}
			stream.emit(decoder.Push(event.Delta))
		})
	} else {
		resp, err = r.router.Complete(ctx, req)
	}
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
	}, ToolCalls: resp.ToolCalls}, nil
}

type grokBuildDiscoveryError struct {
	state string
}

func (e grokBuildDiscoveryError) Error() string {
	switch e.state {
	case provider.GrokBuildStateAbsent:
		return "Grok Build CLI is not installed"
	case provider.GrokBuildStateSignedOut:
		return "Grok Build is not signed in"
	case provider.GrokBuildStateIncompatibleVersion:
		return "Grok Build CLI version is incompatible"
	default:
		return "Grok Build session could not be checked"
	}
}

func (e grokBuildDiscoveryError) ProviderError() providerErrorInfo {
	info := providerErrorInfo{Provider: provider.GrokBuildProviderID}
	switch e.state {
	case provider.GrokBuildStateAbsent:
		info.Code = "provider_cli_not_installed"
		info.Category = "unavailable"
		info.UserAction = "install Grok Build, then refresh Providers"
	case provider.GrokBuildStateSignedOut:
		info.Code = "provider_authentication_failed"
		info.Category = "authentication"
		info.UserAction = "run `grok login`, then refresh Providers"
	case provider.GrokBuildStateIncompatibleVersion:
		info.Code = "provider_cli_incompatible"
		info.Category = "compatibility"
		info.UserAction = "update Grok Build, then refresh Providers"
	default:
		info.Code = "provider_discovery_failed"
		info.Category = "unavailable"
		info.UserAction = "retry Providers or run `grok doctor`"
		info.Retryable = true
	}
	return info
}

func (r *routerReasoner) grokBuildRoute(ctx context.Context, model string) (string, provider.GrokBuildDiscovery, bool, error) {
	providerID, routedModel, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok || !strings.EqualFold(providerID, provider.GrokBuildProviderID) {
		return "", provider.GrokBuildDiscovery{}, false, nil
	}
	if providerID != provider.GrokBuildProviderID {
		return "", provider.GrokBuildDiscovery{}, true, grokCLIError{message: fmt.Sprintf("model provider %q is not canonical; use %q", providerID, provider.GrokBuildProviderID), kind: "protocol"}
	}
	if r.disabledProviders[providerID] {
		return "", provider.GrokBuildDiscovery{}, true, grokCLIError{message: "Grok Build is disabled", kind: "protocol"}
	}
	if strings.TrimSpace(routedModel) == "" || strings.Contains(routedModel, "/") {
		return "", provider.GrokBuildDiscovery{}, true, grokCLIError{message: "invalid Grok Build model", kind: "protocol"}
	}
	discover := r.grokBuildDiscovery
	if discover == nil {
		discover = provider.DetectGrokBuild
	}
	discovery := discover(ctx)
	if err := ctx.Err(); err != nil {
		return "", discovery, true, err
	}
	if discovery.State != provider.GrokBuildStateReady {
		return "", discovery, true, grokBuildDiscoveryError{state: discovery.State}
	}
	modelAvailable := false
	for _, available := range discovery.Models {
		modelAvailable = modelAvailable || available == routedModel
	}
	if !modelAvailable {
		return "", discovery, true, grokCLIError{message: "selected Grok Build model is no longer available", kind: "protocol"}
	}
	return routedModel, discovery, true, nil
}

type routedGrokBuildReasoner interface {
	ThinkRoutedModel(context.Context, string, string) (ReasonerResult, error)
	Close()
}

type grokBuildDelegateKey struct {
	binaryPath string
	version    string
}

type grokBuildDelegateEntry struct {
	key      grokBuildDelegateKey
	delegate routedGrokBuildReasoner
	users    int
	stale    bool
}

func grokBuildDiscoveryKey(discovery provider.GrokBuildDiscovery) grokBuildDelegateKey {
	return grokBuildDelegateKey{
		binaryPath: strings.TrimSpace(discovery.BinaryPath),
		version:    strings.TrimSpace(discovery.Version),
	}
}

func (r *routerReasoner) grokBuildDelegate(discovery provider.GrokBuildDiscovery) (routedGrokBuildReasoner, func(), error) {
	key := grokBuildDiscoveryKey(discovery)
	if discovery.State != provider.GrokBuildStateReady || key.binaryPath == "" {
		return nil, nil, errors.New(nonempty(discovery.Reason, "Grok Build is not ready"))
	}
	r.grokBuildMu.Lock()
	if r.grokBuildClosed {
		r.grokBuildMu.Unlock()
		return nil, nil, errors.New("Grok Build CLI delegation is closed")
	}
	if r.grokBuild != nil && r.grokBuild.key == key {
		entry := r.grokBuild
		entry.users++
		r.grokBuildMu.Unlock()
		return entry.delegate, r.grokBuildRelease(entry), nil
	}
	if r.grokBuildFactory == nil {
		r.grokBuildMu.Unlock()
		return nil, nil, errors.New("Grok Build CLI delegation is unavailable")
	}
	delegate, err := r.grokBuildFactory(discovery)
	if err != nil {
		r.grokBuildMu.Unlock()
		return nil, nil, err
	}
	old := r.grokBuild
	entry := &grokBuildDelegateEntry{key: key, delegate: delegate, users: 1}
	r.grokBuild = entry
	var closeOld routedGrokBuildReasoner
	if old != nil {
		old.stale = true
		if old.users == 0 {
			closeOld = old.delegate
		}
	}
	r.grokBuildMu.Unlock()
	if closeOld != nil {
		closeOld.Close()
	}
	return delegate, r.grokBuildRelease(entry), nil
}

func (r *routerReasoner) grokBuildRelease(entry *grokBuildDelegateEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			var closeDelegate routedGrokBuildReasoner
			r.grokBuildMu.Lock()
			if entry.users > 0 {
				entry.users--
			}
			if entry.stale && entry.users == 0 {
				closeDelegate = entry.delegate
			}
			r.grokBuildMu.Unlock()
			if closeDelegate != nil {
				closeDelegate.Close()
			}
		})
	}
}

func (r *routerReasoner) claudeCodeRoute(model string) (string, string, bool) {
	providerID, routedModel, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok || providerID == "" || routedModel == "" {
		return "", "", false
	}
	info, ok := r.providerCatalog[providerID]
	if !ok || !runtimeProviderUsesClaudeCLI(info) {
		return "", "", false
	}
	return providerID, routedModel, true
}

type routedClaudeCodeReasoner interface {
	ThinkRoutedModel(context.Context, string, string) (ReasonerResult, error)
	Close()
}

func (r *routerReasoner) claudeCodeDelegate() (routedClaudeCodeReasoner, error) {
	r.claudeCodeMu.Lock()
	defer r.claudeCodeMu.Unlock()
	if r.claudeCode != nil {
		return r.claudeCode, nil
	}
	if r.claudeCodeFactory == nil {
		return nil, errors.New("Claude CLI delegation is unavailable")
	}
	delegate, err := r.claudeCodeFactory()
	if err != nil {
		return nil, err
	}
	r.claudeCode = delegate
	return delegate, nil
}

func (r *routerReasoner) Close() {
	r.grokBuildMu.Lock()
	r.grokBuildClosed = true
	grokEntry := r.grokBuild
	r.grokBuild = nil
	var grokDelegate routedGrokBuildReasoner
	if grokEntry != nil {
		grokEntry.stale = true
		if grokEntry.users == 0 {
			grokDelegate = grokEntry.delegate
		}
	}
	r.grokBuildMu.Unlock()
	if grokDelegate != nil {
		grokDelegate.Close()
	}
	r.claudeCodeMu.Lock()
	delegate := r.claudeCode
	r.claudeCode = nil
	r.claudeCodeMu.Unlock()
	if delegate != nil {
		delegate.Close()
	}
}

// ---- claude CLI reasoner ---------------------------------------------------

// claudeCLIReasoner uses the local `claude` binary in headless mode as a pure
// inference engine. Claude Code customizations and tools are disabled and it
// runs in an isolated, empty cwd, so it can only reason and emit a decision.
// This supports gateways that only admit the Claude Code client while keeping
// every real side effect inside the carina capability kernel.
type claudeCLIReasoner struct {
	bin     string
	model   string // optional --model override
	workdir string // isolated empty dir
	timeout time.Duration
}

type claudeCLIError struct {
	message string
	subtype string
	status  int
	kind    string
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
	if e.kind == "safety" {
		return providerErrorInfo{
			Code:       "reasoner_safety_violation",
			Category:   "internal",
			Provider:   "anthropic",
			UserAction: "update Claude Code or use a direct model provider",
		}
	}
	if e.kind == "protocol" {
		return providerErrorInfo{
			Code:       "reasoner_protocol_error",
			Category:   "internal",
			Provider:   "anthropic",
			UserAction: "update Claude Code or use a direct model provider",
		}
	}
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
	return newConfiguredReasonerWithCatalog(backend, router, model, nil)
}

func newConfiguredReasonerWithCatalog(backend string, router *modelrouter.Router, model string, catalog provider.Catalog, disabled ...map[string]bool) (Reasoner, error) {
	switch backend {
	case reasonerBackendRouter:
		return newRouterReasonerWithCatalog(router, nonempty(strings.TrimSpace(model), "default"), catalog, disabled...), nil
	case reasonerBackendClaudeCLI:
		if strings.TrimSpace(model) != "" {
			return newClaudeCLIReasonerModel(strings.TrimSpace(model))
		}
		return newClaudeCLIReasoner()
	case reasonerBackendCodexCLI:
		if strings.TrimSpace(model) != "" {
			return newCodexCLIReasonerModel(strings.TrimSpace(model))
		}
		return newCodexCLIReasoner()
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
	return r.ThinkRoutedModel(ctx, r.model, prompt)
}

func (r *claudeCLIReasoner) ThinkRoutedModel(ctx context.Context, model, prompt string) (ReasonerResult, error) {
	return r.thinkRoutedModelStream(ctx, model, prompt)
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
