package daemon

import (
	"context"
	"sync"
	"testing"
	"time"
)

type retryGovernanceReasoner string

func (r retryGovernanceReasoner) Name() string                                  { return string(r) }
func (r retryGovernanceReasoner) Think(context.Context, string) (string, error) { return "", nil }

func TestRetryGovernanceCircuitHalfOpenSingleProbe(t *testing.T) {
	now := time.Unix(100, 0)
	g := newRetryGovernance(func() time.Time { return now })
	g.minimumSample = 5
	for range 5 {
		if err := g.admit("p", false); err != nil {
			t.Fatal(err)
		}
		g.observe("p", providerErrorInfo{Retryable: true}, false)
	}
	if err := g.admit("p", false); err != errCircuitOpen {
		t.Fatalf("open admit = %v", err)
	}
	now = now.Add(g.openFor)
	if err := g.admit("p", false); err != nil {
		t.Fatalf("half-open probe = %v", err)
	}
	if err := g.admit("p", false); err != errCircuitOpen {
		t.Fatalf("second probe = %v", err)
	}
	g.observe("p", providerErrorInfo{}, true)
	if err := g.admit("p", false); err != nil {
		t.Fatalf("closed admit = %v", err)
	}
}

func TestRetryGovernanceBudgetConcurrentAndRefill(t *testing.T) {
	now := time.Unix(100, 0)
	g := newRetryGovernance(func() time.Time { return now })
	g.capacity = 4
	g.providers = map[string]*providerGovernanceState{}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- g.admit("p", true) }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 4 {
		t.Fatalf("accepted = %d", accepted)
	}
	now = now.Add(3 * time.Second)
	if err := g.admit("p", true); err != nil {
		t.Fatalf("refilled token = %v", err)
	}
}

func TestRetryGovernanceIgnoresNonRetryableFailures(t *testing.T) {
	g := newRetryGovernance(time.Now)
	for range 10 {
		g.observe("p", providerErrorInfo{Retryable: false}, false)
	}
	if err := g.admit("p", false); err != nil {
		t.Fatalf("non-retryable opened breaker: %v", err)
	}
}

func TestRetryGovernanceIsolatesProviderModels(t *testing.T) {
	g := newRetryGovernance(time.Now)
	g.minimumSample = 1
	g.observe("openai/gpt-a", providerErrorInfo{Retryable: true}, false)
	if err := g.admit("openai/gpt-a", false); err != errCircuitOpen {
		t.Fatalf("failed model circuit = %v", err)
	}
	if err := g.admit("openai/gpt-b", false); err != nil {
		t.Fatalf("independent model inherited circuit: %v", err)
	}
	for range int(g.capacity) {
		if err := g.admit("openai/gpt-a-budget", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.admit("openai/gpt-b-budget", true); err != nil {
		t.Fatalf("independent model inherited retry budget: %v", err)
	}
}

func TestRetryGovernanceHalfOpenNonRetryableCloses(t *testing.T) {
	now := time.Unix(100, 0)
	g := newRetryGovernance(func() time.Time { return now })
	g.minimumSample = 1
	g.observe("p/m", providerErrorInfo{Retryable: true}, false)
	now = now.Add(g.openFor)
	if err := g.admit("p/m", false); err != nil {
		t.Fatal(err)
	}
	g.observe("p/m", providerErrorInfo{Retryable: false}, false)
	if state := g.snapshot("p/m")["breaker_state"]; state != circuitClosed {
		t.Fatalf("non-retryable probe left breaker %v", state)
	}
}

func TestRetryGovernanceRouteKeyIncludesModel(t *testing.T) {
	r := retryGovernanceReasoner("model-router")
	if got := retryGovernanceProvider(r, "openai/gpt-a"); got != "openai/gpt-a" {
		t.Fatalf("route key = %q", got)
	}
	if got := retryGovernanceProvider(r, ""); got != "default-route/default" {
		t.Fatalf("default route key = %q", got)
	}
}
