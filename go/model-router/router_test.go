package modelrouter

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	name string
	fail bool
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Complete(_ context.Context, req Request) (*Response, error) {
	if f.fail {
		return nil, errors.New("provider down")
	}
	return &Response{Provider: f.name, Model: req.Model, Text: "ok", OutputTokens: 3}, nil
}

func TestFallbackToSecondProvider(t *testing.T) {
	r := New()
	r.RegisterProvider(&fakeProvider{name: "primary", fail: true})
	r.RegisterProvider(&fakeProvider{name: "backup"})

	resp, err := r.Complete(context.Background(), Request{Model: "m", Prompt: "hi"})
	if err != nil {
		t.Fatalf("expected fallback success: %v", err)
	}
	if resp.Provider != "backup" {
		t.Fatalf("expected backup provider, got %s", resp.Provider)
	}
	usage := r.UsageByProvider()
	if usage["backup"].Requests != 1 {
		t.Fatalf("usage not recorded: %+v", usage)
	}
}

func TestUsageIncludesCacheTokens(t *testing.T) {
	r := New()
	r.RegisterProvider(providerFunc{name: "anthropic", complete: func(_ context.Context, req Request) (*Response, error) {
		return &Response{Provider: "anthropic", Model: req.Model, Text: "ok", InputTokens: 2, OutputTokens: 3, CacheReadTokens: 5, CacheWriteTokens: 7}, nil
	}})

	if _, err := r.Complete(context.Background(), Request{Model: "claude", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := r.UsageByProvider()["anthropic"]
	if got.InputTokens != 2 || got.OutputTokens != 3 || got.CacheReadTokens != 5 || got.CacheWriteTokens != 7 {
		t.Fatalf("usage = %+v", got)
	}
}

type providerFunc struct {
	name     string
	complete func(context.Context, Request) (*Response, error)
}

func (p providerFunc) Name() string { return p.name }
func (p providerFunc) Complete(ctx context.Context, req Request) (*Response, error) {
	return p.complete(ctx, req)
}

type streamingProviderFunc struct {
	providerFunc
	stream func(context.Context, Request, StreamCallback) (*Response, error)
}

func (p streamingProviderFunc) Stream(ctx context.Context, req Request, callback StreamCallback) (*Response, error) {
	return p.stream(ctx, req, callback)
}

func TestStreamUsesOptionalCapabilityAndCompleteFallback(t *testing.T) {
	r := New()
	r.RegisterProvider(providerFunc{name: "plain", complete: func(_ context.Context, req Request) (*Response, error) {
		return &Response{Provider: "plain", Model: req.Model, Text: "complete fallback", OutputTokens: 2}, nil
	}})
	var events []StreamEvent
	resp, err := r.Stream(context.Background(), Request{Model: "m"}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil || resp.Text != "complete fallback" {
		t.Fatalf("stream fallback = %+v, err=%v", resp, err)
	}
	if len(events) != 1 || events[0].Delta != "complete fallback" || events[0].Reset {
		t.Fatalf("fallback events = %+v", events)
	}

	r = New()
	r.RegisterProvider(streamingProviderFunc{
		providerFunc: providerFunc{name: "streaming", complete: func(context.Context, Request) (*Response, error) {
			t.Fatal("Complete called for a streaming provider")
			return nil, nil
		}},
		stream: func(_ context.Context, req Request, callback StreamCallback) (*Response, error) {
			callback(StreamEvent{Delta: "one"})
			callback(StreamEvent{Delta: "two"})
			return &Response{Provider: "streaming", Model: req.Model, Text: "onetwo", OutputTokens: 3}, nil
		},
	})
	events = nil
	resp, err = r.Stream(context.Background(), Request{Model: "m"}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil || resp.Text != "onetwo" || len(events) != 2 {
		t.Fatalf("native stream = %+v, events=%+v, err=%v", resp, events, err)
	}
}

func TestStreamResetsBeforeProviderFallback(t *testing.T) {
	r := New()
	r.RegisterProvider(streamingProviderFunc{
		providerFunc: providerFunc{name: "first", complete: func(context.Context, Request) (*Response, error) { return nil, errors.New("unused") }},
		stream: func(_ context.Context, _ Request, callback StreamCallback) (*Response, error) {
			callback(StreamEvent{Delta: "discard"})
			return nil, errors.New("stream failed")
		},
	})
	r.RegisterProvider(providerFunc{name: "second", complete: func(_ context.Context, req Request) (*Response, error) {
		return &Response{Provider: "second", Model: req.Model, Text: "winner"}, nil
	}})
	var events []StreamEvent
	resp, err := r.Stream(context.Background(), Request{Model: "m"}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil || resp.Provider != "second" {
		t.Fatalf("fallback = %+v, err=%v", resp, err)
	}
	if len(events) != 3 || events[0].Delta != "discard" || !events[1].Reset || events[2].Delta != "winner" {
		t.Fatalf("fallback events = %+v", events)
	}
}

func TestStreamResetsAfterFinalProviderFailure(t *testing.T) {
	r := New()
	r.RegisterProvider(streamingProviderFunc{
		providerFunc: providerFunc{name: "only", complete: func(context.Context, Request) (*Response, error) { return nil, errors.New("unused") }},
		stream: func(_ context.Context, _ Request, callback StreamCallback) (*Response, error) {
			callback(StreamEvent{Delta: "discard"})
			return nil, errors.New("stream failed")
		},
	})
	var events []StreamEvent
	_, err := r.Stream(context.Background(), Request{Model: "m"}, func(event StreamEvent) {
		events = append(events, event)
	})
	if err == nil {
		t.Fatal("expected final provider failure")
	}
	if len(events) != 2 || events[0].Delta != "discard" || !events[1].Reset {
		t.Fatalf("failure events = %+v", events)
	}
}

func TestTargetedProviderModel(t *testing.T) {
	r := New()
	r.RegisterProvider(&fakeProvider{name: "anthropic", fail: true})
	r.RegisterProvider(&fakeProvider{name: "openrouter"})

	resp, err := r.Complete(context.Background(), Request{Model: "openrouter/anthropic/claude-sonnet-4-5", Prompt: "hi"})
	if err != nil {
		t.Fatalf("targeted provider should not try earlier providers: %v", err)
	}
	if resp.Provider != "openrouter" {
		t.Fatalf("expected openrouter, got %s", resp.Provider)
	}
	if resp.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("targeted model suffix not preserved: %q", resp.Model)
	}
}

func TestUnknownSlashModelFallsBackNormally(t *testing.T) {
	r := New()
	r.RegisterProvider(&fakeProvider{name: "openrouter"})

	resp, err := r.Complete(context.Background(), Request{Model: "meta-llama/llama-3.3", Prompt: "hi"})
	if err != nil {
		t.Fatalf("unknown slash prefix should remain a model id: %v", err)
	}
	if resp.Model != "meta-llama/llama-3.3" {
		t.Fatalf("slash model should be unchanged: %q", resp.Model)
	}
}

func TestNoProvidersAndAllFail(t *testing.T) {
	r := New()
	if _, err := r.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("empty router should error")
	}
	r.RegisterProvider(&fakeProvider{name: "a", fail: true})
	if _, err := r.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("all-failing router should error")
	}
}

func TestMockProvider(t *testing.T) {
	m := NewMockProvider()
	resp, err := m.Complete(context.Background(), Request{Prompt: "do X"})
	if err != nil || resp.Provider != "mock" {
		t.Fatalf("mock provider: %+v %v", resp, err)
	}
}
