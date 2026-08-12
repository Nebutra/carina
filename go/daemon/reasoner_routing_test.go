package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/Nebutra/carina/go/provider"
)

type routedClaudeCodeReasonerStub struct {
	model  string
	prompt string
	effort string
	stream bool
	closed bool
}

func (s *routedClaudeCodeReasonerStub) ThinkRoutedModel(ctx context.Context, model, prompt string) (ReasonerResult, error) {
	s.model = model
	s.prompt = prompt
	s.effort = reasoningEffortFrom(ctx)
	if stream := reasonerStreamFrom(ctx); stream != nil {
		s.stream = true
		stream.emit("live")
	}
	return ReasonerResult{
		Text: "delegated",
		Usage: ModelUsage{
			Provider: "anthropic",
			Model:    model,
		},
	}, nil
}

func (s *routedClaudeCodeReasonerStub) Close() { s.closed = true }

func TestRouterReasonerDelegatesClaudeCodeOwnedRoute(t *testing.T) {
	const providerID = "ccswitch-claude-test"
	delegate := &routedClaudeCodeReasonerStub{}
	reasoner := newRouterReasonerWithCatalog(nil, "default", provider.Catalog{
		providerID: {
			ID: providerID,
			Source: &provider.Source{
				Kind:            provider.CCSwitchSourceKind,
				CredentialOwner: provider.CCSwitchCredentialOwnerClaudeCode,
			},
		},
	})
	reasoner.claudeCode = delegate

	var updates []ReasonerStreamUpdate
	stream := newReasonerStreamController(func(update ReasonerStreamUpdate) { updates = append(updates, update) })
	ctx := withReasonerStream(withReasoningEffort(context.Background(), "high"), stream)
	result, err := reasoner.ThinkModelResult(ctx, providerID+"/claude-opus-5", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "delegated" || result.Usage.Provider != providerID || result.Usage.Model != "claude-opus-5" {
		t.Fatalf("result = %+v", result)
	}
	if delegate.model != "claude-opus-5" || delegate.prompt != "prompt" || delegate.effort != "high" || !delegate.stream {
		t.Fatalf("delegate call = model %q prompt %q effort %q", delegate.model, delegate.prompt, delegate.effort)
	}
	if len(updates) != 1 || updates[0].Text != "live" {
		t.Fatalf("stream updates = %+v", updates)
	}

	reasoner.Close()
	if !delegate.closed {
		t.Fatal("Claude Code delegate was not closed")
	}
}

func TestRouterReasonerNeverFallsBackToDirectAPIForClaudeCodeOwnedRoute(t *testing.T) {
	const providerID = "ccswitch-claude-test"
	reasoner := newRouterReasonerWithCatalog(nil, "default", provider.Catalog{
		providerID: {
			ID: providerID,
			Source: &provider.Source{
				Kind:            provider.CCSwitchSourceKind,
				CredentialOwner: provider.CCSwitchCredentialOwnerClaudeCode,
			},
		},
	})
	reasoner.claudeCodeFactory = func() (routedClaudeCodeReasoner, error) {
		return nil, errors.New("claude CLI unavailable")
	}

	_, err := reasoner.ThinkModelResult(context.Background(), providerID+"/claude-opus-5", "prompt")
	if err == nil || err.Error() != "claude reasoner: claude CLI unavailable" {
		t.Fatalf("error = %v", err)
	}
}
