package daemon

import (
	"context"
	"testing"

	modelrouter "github.com/Nebutra/carina/go/model-router"
)

type reasonerProvider struct {
	name      string
	seenModel string
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
