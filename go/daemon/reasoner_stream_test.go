package daemon

import (
	"context"
	"errors"
	"testing"

	modelrouter "github.com/Nebutra/carina/go/model-router"
)

func TestActionEnvelopeStreamDecoderExposesOnlyConfirmedDoneSummary(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{name: "flat done", chunks: []string{`{"thought":"private","tool":"done","summary":"hel`, `lo"}`}, want: "hello"},
		{name: "nested done", chunks: []string{`{"thought":"private","action":{"tool":"done",`, `"summary":"nested"}}`}, want: "nested"},
		{name: "tool args", chunks: []string{`{"thought":"private","tool":"patch",`, `"content":"secret patch"}`}},
		{name: "structured report wrapper", chunks: []string{`{"tool":"done","summary":"{\"summary\":\"report\",`, `\"risks\":[\"one\"]}"}`}},
		{name: "unfinished valid prefix", chunks: []string{`{"tool":"done","summary":"never"`}, want: "never"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var decoder actionEnvelopeStreamDecoder
			var got string
			for _, chunk := range test.chunks {
				got += decoder.Push(chunk)
			}
			if got != test.want {
				t.Fatalf("decoded = %q, want %q", got, test.want)
			}
		})
	}
}

func TestActionEnvelopeStreamDecoderResetDiscardsFailedGeneration(t *testing.T) {
	var decoder actionEnvelopeStreamDecoder
	if got := decoder.Push(`{"tool":"done","summary":"failed"}`); got != "failed" {
		t.Fatalf("first generation = %q", got)
	}
	decoder.Reset()
	if got := decoder.Push(`{"tool":"done","summary":"winner"}`); got != "winner" {
		t.Fatalf("reset generation = %q", got)
	}
}

func TestActionEnvelopeStreamDecoderIncrementallyDecodesConfirmedDoneSummary(t *testing.T) {
	var decoder actionEnvelopeStreamDecoder
	if got := decoder.Push(`{"tool":"done","summary":"hel`); got != "hel" {
		t.Fatalf("first public prefix = %q, want hel", got)
	}
	if got := decoder.Push(`lo\n\u4f60`); got != "lo\n你" {
		t.Fatalf("decoded escaped suffix = %q, want %q", got, "lo\n你")
	}
	if got := decoder.Push(`\u597d"}`); got != "好" {
		t.Fatalf("decoded final suffix = %q, want 好", got)
	}
}

func TestActionEnvelopeStreamDecoderBuffersSummaryUntilDoneIsKnown(t *testing.T) {
	var decoder actionEnvelopeStreamDecoder
	if got := decoder.Push(`{"summary":"private until classified",`); got != "" {
		t.Fatalf("summary leaked before tool classification: %q", got)
	}
	if got := decoder.Push(`"tool":"done"}`); got != "private until classified" {
		t.Fatalf("buffered summary = %q", got)
	}

	decoder.Reset()
	if got := decoder.Push(`{"summary":"patch body","tool":"patch"}`); got != "" {
		t.Fatalf("tool action leaked as assistant content: %q", got)
	}
}

func TestActionEnvelopeStreamDecoderWaitsForCompleteEscapes(t *testing.T) {
	var decoder actionEnvelopeStreamDecoder
	if got := decoder.Push(`{"tool":"done","summary":"safe\`); got != "safe" {
		t.Fatalf("trailing escape prefix = %q", got)
	}
	if got := decoder.Push(`nemoji: \ud83d`); got != "\nemoji: " {
		t.Fatalf("escape suffix = %q", got)
	}
	if got := decoder.Push(`\ude00"}`); got != "😀" {
		t.Fatalf("surrogate pair suffix = %q", got)
	}
}

type retryStreamingProvider struct{ calls int }

func (p *retryStreamingProvider) Name() string { return "openai" }
func (p *retryStreamingProvider) Complete(context.Context, modelrouter.Request) (*modelrouter.Response, error) {
	return nil, errors.New("unexpected Complete")
}

type cancelledStreamingProvider struct{}

func (p *cancelledStreamingProvider) Name() string { return "openai" }
func (p *cancelledStreamingProvider) Complete(context.Context, modelrouter.Request) (*modelrouter.Response, error) {
	return nil, errors.New("unexpected Complete")
}
func (p *cancelledStreamingProvider) Stream(_ context.Context, _ modelrouter.Request, callback modelrouter.StreamCallback) (*modelrouter.Response, error) {
	callback(modelrouter.StreamEvent{Delta: `{"tool":"done","summary":"temporary"}`})
	return nil, context.Canceled
}
func (p *retryStreamingProvider) Stream(_ context.Context, req modelrouter.Request, callback modelrouter.StreamCallback) (*modelrouter.Response, error) {
	p.calls++
	if p.calls == 1 {
		callback(modelrouter.StreamEvent{Delta: `{"tool":"done","summary":"discard"}`})
		return nil, providerStatusError{provider: "openai", status: 503}
	}
	text := `{"tool":"done","summary":"winner"}`
	callback(modelrouter.StreamEvent{Delta: text})
	return &modelrouter.Response{Provider: "openai", Model: req.Model, Text: text, InputTokens: 3, OutputTokens: 2, CacheReadTokens: 1, EffectiveReasoningEffort: "high"}, nil
}

func TestReasonerRetryResetsGenerationAndPreservesUsage(t *testing.T) {
	provider := &retryStreamingProvider{}
	router := modelrouter.New()
	router.RegisterProvider(provider)
	reasoner := newRouterReasoner(router, "gpt-5")
	var updates []ReasonerStreamUpdate
	stream := newReasonerStreamController(func(update ReasonerStreamUpdate) { updates = append(updates, update) })
	ctx := withReasonerStream(context.Background(), stream)
	result, err := thinkWithRetryPolicy(ctx, reasoner, "gpt-5", "prompt", "", "", retryPolicy{
		MaxAttempts: 2, BaseDelay: 1, MaxDelay: 1, RandFloat64: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != `{"tool":"done","summary":"winner"}` || result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 2 || result.Usage.CacheReadTokens != 1 || result.Usage.EffectiveReasoningEffort != "high" {
		t.Fatalf("result = %+v", result)
	}
	var discardGeneration, winnerGeneration uint64
	for _, update := range updates {
		if update.Text == "discard" {
			discardGeneration = update.Generation
		}
		if update.Text == "winner" {
			winnerGeneration = update.Generation
		}
	}
	if discardGeneration == 0 || winnerGeneration <= discardGeneration {
		t.Fatalf("generation updates = %+v", updates)
	}
}

func TestReasonerCancellationResetsVisibleContentWithoutCompletion(t *testing.T) {
	router := modelrouter.New()
	router.RegisterProvider(&cancelledStreamingProvider{})
	reasoner := newRouterReasoner(router, "gpt-5")
	var updates []ReasonerStreamUpdate
	stream := newReasonerStreamController(func(update ReasonerStreamUpdate) { updates = append(updates, update) })
	_, err := thinkWithRetryPolicy(withReasonerStream(context.Background(), stream), reasoner, "gpt-5", "prompt", "", "", retryPolicy{
		MaxAttempts: 2, BaseDelay: 1, MaxDelay: 1, RandFloat64: func() float64 { return 0 },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if len(updates) < 3 || updates[len(updates)-1].Reset != true {
		t.Fatalf("cancellation updates = %+v", updates)
	}
	for _, update := range updates {
		if update.Completed {
			t.Fatalf("cancellation emitted completion: %+v", updates)
		}
	}
}

func TestAssistantStreamPublisherEmitsOnlyTransientTypedProjection(t *testing.T) {
	d := &Daemon{events: NewBus()}
	var events []map[string]any
	d.events.Tap(func(_ string, event map[string]any) { events = append(events, event) })
	publisher := &assistantStreamPublisher{d: d, sessionID: "session-1", taskID: "task-1"}
	publisher.publish(ReasonerStreamUpdate{Generation: 1, Reset: true})
	publisher.publish(ReasonerStreamUpdate{Generation: 1, Text: "safe summary"})
	publisher.publish(ReasonerStreamUpdate{Generation: 1, Text: "safe summary", Completed: true})
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	if events[0]["type"] != "assistant.message.reset" || events[1]["type"] != "assistant.message.delta" || events[2]["type"] != "assistant.message.completed" {
		t.Fatalf("event types = %+v", events)
	}
	for _, event := range events {
		if _, durable := event[internalRawAuditCursor]; durable {
			t.Fatalf("transient event contains audit cursor: %+v", event)
		}
		payload, _ := event["payload"].(map[string]any)
		if payload["phase"] != assistantPhaseFinalAnswer {
			t.Fatalf("assistant phase = %#v", payload["phase"])
		}
		if payload["thought"] != nil || payload["arguments"] != nil || payload["raw"] != nil {
			t.Fatalf("private fields leaked: %+v", payload)
		}
	}
}
