package modelrouter

import (
	"context"
	"testing"
)

func TestCompleteIgnoresEmptyTools(t *testing.T) {
	r := New()
	var saw []ToolSpec
	r.RegisterProvider(providerFunc{name: "plain", complete: func(_ context.Context, req Request) (*Response, error) {
		saw = req.Tools
		return &Response{Provider: "plain", Model: req.Model, Text: "ok"}, nil
	}})
	resp, err := r.Complete(context.Background(), Request{Model: "m", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" || len(saw) != 0 || len(resp.ToolCalls) != 0 {
		t.Fatalf("empty tools must be a no-op: resp=%+v saw=%+v", resp, saw)
	}
}

func TestCompleteForwardsToolsAndToolCalls(t *testing.T) {
	r := New()
	r.RegisterProvider(providerFunc{name: "tools", complete: func(_ context.Context, req Request) (*Response, error) {
		if len(req.Tools) != 1 || req.Tools[0].Name != "read" {
			t.Fatalf("tools = %+v", req.Tools)
		}
		return &Response{
			Provider: "tools", Model: req.Model,
			ToolCalls: []ToolCall{{Name: "read", Arguments: []byte(`{"path":"a.go"}`)}},
		}, nil
	}})
	resp, err := r.Complete(context.Background(), Request{
		Model: "m", Prompt: "hi",
		Tools: []ToolSpec{{Name: "read", Description: "read a file"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
}
