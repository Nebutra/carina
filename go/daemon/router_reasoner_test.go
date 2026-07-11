package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	modelrouter "github.com/Nebutra/carina/go/model-router"
)

type reasonerProvider struct {
	name      string
	seenModel string
}

type failingReasoner struct {
	err   error
	calls int
}

func (r *failingReasoner) Name() string { return "failing" }
func (r *failingReasoner) Think(context.Context, string) (string, error) {
	r.calls++
	return "", r.err
}

func (p *reasonerProvider) Name() string { return p.name }

func (p *reasonerProvider) Complete(_ context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	p.seenModel = req.Model
	return &modelrouter.Response{Provider: p.name, Model: req.Model, Text: `{"tool":"done","summary":"ok"}`}, nil
}

func TestRouterReasonerUsesTaskModel(t *testing.T) {
	router := modelrouter.New()
	provider := &reasonerProvider{name: "openai"}
	router.RegisterProvider(provider)
	r := newRouterReasoner(router, "default")

	out, err := r.ThinkModel(context.Background(), "openai/gpt-5", "prompt")
	if err != nil {
		t.Fatalf("think: %v", err)
	}
	if out != `{"tool":"done","summary":"ok"}` {
		t.Fatalf("unexpected output %q", out)
	}
	if provider.seenModel != "gpt-5" {
		t.Fatalf("targeted model not passed through: %q", provider.seenModel)
	}
}

func TestRouterReasonerRejectsMockFallback(t *testing.T) {
	router := modelrouter.New()
	router.RegisterProvider(modelrouter.NewMockProvider())
	r := newRouterReasoner(router, "default")

	if _, err := r.Think(context.Background(), "prompt"); err == nil {
		t.Fatal("router reasoner must not treat mock provider as a real model")
	}
}

func TestRetryDelayHonorsWrappedProviderRetryAfter(t *testing.T) {
	err := fmt.Errorf("router wrapped: %w", providerStatusError{provider: "openai", status: 429, retry: 75 * time.Millisecond})
	if got := retryDelay(err, 2*time.Second); got != 75*time.Millisecond {
		t.Fatalf("retry delay = %s", got)
	}
}

func TestRetryPolicyDoesNotRetryPermanentHTTPFailures(t *testing.T) {
	r := &failingReasoner{err: providerStatusError{provider: "openai", status: 401}}
	_, err := thinkWithRetryPolicy(context.Background(), r, "", "prompt", "", "", retryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond, RandFloat64: func() float64 { return 0 }})
	if err == nil || r.calls != 1 {
		t.Fatalf("err=%v calls=%d, want one attempt", err, r.calls)
	}
}

func TestRetryPolicyUsesFullJitterAndBoundedAttempts(t *testing.T) {
	r := &failingReasoner{err: providerStatusError{provider: "openai", status: 503}}
	var attempts []retryAttempt
	ctx := withRetryObserver(context.Background(), func(a retryAttempt) { attempts = append(attempts, a) })
	_, err := thinkWithRetryPolicy(ctx, r, "", "prompt", "", "", retryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond, MaxElapsed: time.Second, RandFloat64: func() float64 { return .5 }})
	if err == nil || r.calls != 3 || len(attempts) != 2 {
		t.Fatalf("err=%v calls=%d attempts=%+v", err, r.calls, attempts)
	}
	if attempts[0].Delay != 500*time.Microsecond || attempts[1].Delay != time.Millisecond {
		t.Fatalf("delays=%s,%s", attempts[0].Delay, attempts[1].Delay)
	}
}

func TestRetryPolicyHonorsRetryAfterWithoutJitter(t *testing.T) {
	r := &failingReasoner{err: providerStatusError{provider: "openai", status: 429, retry: time.Millisecond}}
	var got retryAttempt
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ctx = withRetryObserver(ctx, func(a retryAttempt) { got = a })
	_, _ = thinkWithRetryPolicy(ctx, r, "", "prompt", "", "", retryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxElapsed: time.Second, RandFloat64: func() float64 { return 0 }})
	if got.Delay != time.Millisecond {
		t.Fatalf("delay=%s", got.Delay)
	}
}
