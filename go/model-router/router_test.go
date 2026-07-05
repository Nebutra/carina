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
