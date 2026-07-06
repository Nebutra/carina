package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type mockHandler struct{}

func (mockHandler) Tools() []Tool {
	return []Tool{{Name: "echo", Description: "echo back", InputSchema: map[string]any{"type": "object"}}}
}

func (mockHandler) Call(name string, args map[string]any) (string, error) {
	if name != "echo" {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return fmt.Sprintf("echoed: %v", args["msg"]), nil
}

// run feeds newline-delimited requests through the server and returns the parsed
// responses, in order.
func run(t *testing.T, requests ...map[string]any) []map[string]any {
	t.Helper()
	var in strings.Builder
	for _, r := range requests {
		b, _ := json.Marshal(r)
		in.WriteString(string(b) + "\n")
	}
	var out strings.Builder
	srv := New("carina", "1.0.0", mockHandler{})
	if err := srv.Serve(context.Background(), strings.NewReader(in.String()), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad response line %q: %v", sc.Text(), err)
		}
		got = append(got, m)
	}
	return got
}

func TestInitializeAndList(t *testing.T) {
	resp := run(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, // no reply
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
	)
	if len(resp) != 2 {
		t.Fatalf("expected 2 replies (notification is silent), got %d: %+v", len(resp), resp)
	}
	init := resp[0]["result"].(map[string]any)
	if init["protocolVersion"] != protocolVersion {
		t.Fatalf("bad protocolVersion: %v", init["protocolVersion"])
	}
	if init["serverInfo"].(map[string]any)["name"] != "carina" {
		t.Fatalf("bad serverInfo: %v", init["serverInfo"])
	}
	tools := resp[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("tools/list wrong: %+v", tools)
	}
}

func TestToolCallSuccessAndError(t *testing.T) {
	resp := run(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "echo", "arguments": map[string]any{"msg": "hi"}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": "nope", "arguments": map[string]any{}}},
	)
	ok := resp[0]["result"].(map[string]any)
	if ok["isError"] != false {
		t.Fatalf("success call flagged as error: %+v", ok)
	}
	text := ok["content"].([]any)[0].(map[string]any)["text"]
	if text != "echoed: hi" {
		t.Fatalf("bad echo result: %v", text)
	}
	// An unknown tool is an isError content result, NOT a JSON-RPC error.
	bad := resp[1]["result"].(map[string]any)
	if bad["isError"] != true {
		t.Fatalf("tool failure should be isError content: %+v", resp[1])
	}
	if resp[1]["error"] != nil {
		t.Fatalf("tool failure must not surface as a JSON-RPC error: %+v", resp[1])
	}
}

func TestUnknownMethodIsRpcError(t *testing.T) {
	resp := run(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "bogus/method"})
	e, ok := resp[0]["error"].(map[string]any)
	if !ok || e["code"].(float64) != -32601 {
		t.Fatalf("unknown method should be JSON-RPC -32601, got %+v", resp[0])
	}
}
