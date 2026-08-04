package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

func retryGovernanceProvider(reasoner Reasoner, model string) string {
	model = strings.TrimSpace(model)
	// Governance is isolated by the requested provider/model route. The router
	// may resolve an untargeted request dynamically, so that route gets its own
	// stable key instead of contaminating an explicitly targeted model.
	if reasoner != nil && reasoner.Name() == "model-router" {
		if provider, routedModel, ok := strings.Cut(model, "/"); ok && provider != "" && routedModel != "" {
			return provider + "/" + routedModel
		}
		return "default-route/" + nonempty(model, "default")
	}
	if reasoner == nil {
		return "unknown/" + nonempty(model, "default")
	}
	return reasoner.Name() + "/" + nonempty(model, "default")
}

type circuitState string

const (
	circuitClosed   circuitState = "closed"
	circuitOpen     circuitState = "open"
	circuitHalfOpen circuitState = "half_open"
)

var (
	errCircuitOpen         = errors.New("provider circuit open")
	errRetryBudgetExceeded = errors.New("provider retry budget exhausted")
	errRetryPressure       = errors.New("provider retry paused by backpressure")
)

type providerOutcome struct {
	at      time.Time
	failure bool
}

type providerGovernanceState struct {
	outcomes     []providerOutcome
	state        circuitState
	openUntil    time.Time
	probeInUse   bool
	tokens       float64
	refilledAt   time.Time
	lastCode     string
	lastCategory string
	lastAt       time.Time
}

// routeHealth is operator-facing inventory health for a provider/model route.
type routeHealth struct {
	Status string // ready|circuit_open|rate_limited|probing|unavailable
	Reason string
}

type retryGovernance struct {
	mu            sync.Mutex
	providers     map[string]*providerGovernanceState
	now           func() time.Time
	window        time.Duration
	minimumSample int
	failureRate   float64
	openFor       time.Duration
	capacity      float64
	refillPerSec  float64
	pressure      func() string
	metrics       retryGovernanceMetrics
}

type retryGovernanceMetrics struct {
	Attempts        uint64 `json:"attempts_total"`
	BudgetExhausted uint64 `json:"budget_exhausted_total"`
	CircuitRejected uint64 `json:"circuit_rejected_total"`
	PressureBlocked uint64 `json:"pressure_blocked_total"`
	Transitions     uint64 `json:"breaker_transitions_total"`
}

func newRetryGovernance(now func() time.Time) *retryGovernance {
	if now == nil {
		now = time.Now
	}
	return &retryGovernance{providers: map[string]*providerGovernanceState{}, now: now, window: time.Minute, minimumSample: 5, failureRate: .6, openFor: 30 * time.Second, capacity: 20, refillPerSec: 1.0 / 3.0}
}

func (g *retryGovernance) stateLocked(provider string, now time.Time) *providerGovernanceState {
	s := g.providers[provider]
	if s == nil {
		s = &providerGovernanceState{state: circuitClosed, tokens: g.capacity, refilledAt: now}
		g.providers[provider] = s
	}
	if elapsed := now.Sub(s.refilledAt).Seconds(); elapsed > 0 {
		s.tokens = min(g.capacity, s.tokens+elapsed*g.refillPerSec)
		s.refilledAt = now
	}
	cutoff := now.Add(-g.window)
	i := 0
	for i < len(s.outcomes) && s.outcomes[i].at.Before(cutoff) {
		i++
	}
	s.outcomes = s.outcomes[i:]
	if s.state == circuitOpen && !now.Before(s.openUntil) {
		s.state = circuitHalfOpen
		s.probeInUse = false
		g.metrics.Transitions++
	}
	return s
}

// admit is called immediately before an attempt. Permits are not held while a
// retry sleeps; each attempt re-enters admission after its timer fires.
func (g *retryGovernance) admit(provider string, retry bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	s := g.stateLocked(provider, now)
	if g.pressure != nil && retry && g.pressure() == "pause" {
		g.metrics.PressureBlocked++
		return errRetryPressure
	}
	if s.state == circuitOpen || (s.state == circuitHalfOpen && s.probeInUse) {
		g.metrics.CircuitRejected++
		return errCircuitOpen
	}
	if retry {
		if s.tokens < 1 {
			g.metrics.BudgetExhausted++
			return errRetryBudgetExceeded
		}
		s.tokens--
	}
	if s.state == circuitHalfOpen {
		s.probeInUse = true
	}
	g.metrics.Attempts++
	return nil
}

func (g *retryGovernance) observe(provider string, info providerErrorInfo, success bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	s := g.stateLocked(provider, now)
	if success {
		s.lastCode, s.lastCategory, s.lastAt = "", "", time.Time{}
	} else {
		s.lastCode = strings.TrimSpace(info.Code)
		s.lastCategory = strings.TrimSpace(info.Category)
		s.lastAt = now
	}
	if s.state == circuitHalfOpen {
		s.probeInUse = false
		if success || !info.Retryable {
			s.state, s.outcomes = circuitClosed, nil
		} else if info.Retryable {
			s.state, s.openUntil = circuitOpen, now.Add(g.openFor)
		}
		g.metrics.Transitions++
		return
	}
	if success || info.Retryable {
		s.outcomes = append(s.outcomes, providerOutcome{at: now, failure: !success})
	}
	if s.state != circuitClosed || len(s.outcomes) < g.minimumSample {
		return
	}
	failures := 0
	for _, outcome := range s.outcomes {
		if outcome.failure {
			failures++
		}
	}
	if float64(failures)/float64(len(s.outcomes)) >= g.failureRate {
		s.state, s.openUntil = circuitOpen, now.Add(g.openFor)
		g.metrics.Transitions++
	}
}

func (g *retryGovernance) snapshot(provider string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.stateLocked(provider, g.now())
	return map[string]any{
		"scope": "daemon", "provider": provider, "breaker_state": s.state,
		"retry_tokens": int(s.tokens), "last_code": s.lastCode, "last_category": s.lastCategory,
		"metrics": g.metrics,
	}
}

// routeHealth reports inventory-facing health for a provider/model route so the
// model picker can show circuit/rate-limit state before the operator submits.
func (g *retryGovernance) routeHealth(route string) routeHealth {
	if g == nil {
		return routeHealth{Status: "ready"}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	s := g.stateLocked(route, now)
	switch s.state {
	case circuitOpen:
		reason := "Provider circuit is open after recent failures. Wait or choose another model."
		if s.lastCategory == "rate_limit" || strings.Contains(s.lastCode, "rate") || strings.Contains(s.lastCode, "quota") {
			reason = "Rate-limited or quota-blocked; circuit is open. Wait or choose another model."
		}
		return routeHealth{Status: "circuit_open", Reason: reason}
	case circuitHalfOpen:
		return routeHealth{Status: "probing", Reason: "Provider is being probed after recent failures."}
	}
	if s.lastCategory == "rate_limit" && !s.lastAt.IsZero() && now.Sub(s.lastAt) < g.window {
		return routeHealth{
			Status: "rate_limited",
			Reason: "Recently rate-limited or quota-blocked. Wait or choose another model.",
		}
	}
	if s.lastCode != "" && !s.lastAt.IsZero() && now.Sub(s.lastAt) < g.window && !strings.EqualFold(s.lastCategory, "") {
		// Surface other recent hard failures as degraded without marking unusable forever.
		if s.lastCategory == "auth" || s.lastCategory == "permission" || s.lastCategory == "authentication" {
			return routeHealth{Status: "auth_error", Reason: "Recent authentication/permission failure for this model."}
		}
	}
	return routeHealth{Status: "ready"}
}

func (g *retryGovernance) metricsSnapshot() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	states := map[circuitState]int{circuitClosed: 0, circuitOpen: 0, circuitHalfOpen: 0}
	for provider := range g.providers {
		states[g.stateLocked(provider, now).state]++
	}
	return map[string]any{"scope": "daemon", "operations": g.metrics, "breaker_states": states}
}

type retryGovernanceKey struct{}

func withRetryGovernance(ctx context.Context, governance *retryGovernance, provider string) context.Context {
	return context.WithValue(ctx, retryGovernanceKey{}, struct {
		governance *retryGovernance
		provider   string
	}{governance, provider})
}

func retryGovernanceFrom(ctx context.Context) (*retryGovernance, string) {
	v, _ := ctx.Value(retryGovernanceKey{}).(struct {
		governance *retryGovernance
		provider   string
	})
	return v.governance, v.provider
}
