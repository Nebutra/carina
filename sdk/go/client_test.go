package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func TestTypedParityAndEventSubscription(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()

	methods := make(chan []string, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		seen := []string{}
		for len(seen) < 6 {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(line, &request)
			seen = append(seen, request.Method)
			result := any(map[string]any{})
			switch request.Method {
			case "session.attach":
				result = map[string]any{"events": []any{}, "from": 3, "cursor": 7}
			case "session.fork":
				result = map[string]any{"session_id": "child"}
			case "usage.cost":
				result = map[string]any{"providers": []any{}, "totals": map[string]any{}, "estimated": false}
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			_, _ = serverConn.Write(append(response, '\n'))
			if request.Method == "session.events.stream" {
				notification, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "event", "params": map[string]any{
					"session_id": "s1", "type": "ModelResponded", "timestamp": "now",
				}})
				_, _ = serverConn.Write(append(notification, '\n'))
			}
		}
		methods <- seen
	}()

	if CompatibleRuntimeVersion != "0.6.1" {
		t.Fatalf("compatibility version = %s", CompatibleRuntimeVersion)
	}
	if attached, err := client.AttachSession("s1", 3); err != nil || attached.Cursor != 7 {
		t.Fatalf("attach = %+v, %v", attached, err)
	}
	if forked, err := client.ForkSession("s1"); err != nil || forked.SessionID != "child" {
		t.Fatalf("fork = %+v, %v", forked, err)
	}
	if report, err := client.Cost("s1", ""); err != nil || report.Estimated {
		t.Fatalf("cost = %+v, %v", report, err)
	}
	if err := client.SteerTask("t1", "continue"); err != nil {
		t.Fatal(err)
	}
	if err := client.AnswerQuestion("q1", "yes"); err != nil {
		t.Fatal(err)
	}
	if err := client.SubscribeSessionEvents("s1"); err != nil {
		t.Fatal(err)
	}
	event, err := client.ReadEvent()
	if err != nil || event.Type != "ModelResponded" {
		t.Fatalf("event = %+v, %v", event, err)
	}
	want := []string{"session.attach", "session.fork", "usage.cost", "task.steer", "task.user.answer", "session.events.stream"}
	if got := <-methods; !reflect.DeepEqual(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
}

func TestHighLevelThreadRunNegotiatesAndUsesSchema(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()
	methods := make(chan []string, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		var seen []string
		for len(seen) < 4 {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params map[string]any  `json:"params"`
			}
			_ = json.Unmarshal(line, &req)
			seen = append(seen, req.Method)
			var result any = map[string]any{}
			switch req.Method {
			case "runtime.initialize":
				result = map[string]any{"runtime_version": "0.6.1", "protocol_version": "1.1.0", "capabilities": map[string]any{}}
			case "session.create":
				result = map[string]any{"session_id": "s", "workspace_id": "w", "workspace_root": "/tmp", "status": "active"}
			case "task.submit":
				if _, ok := req.Params["output_schema"].(map[string]any); !ok {
					t.Errorf("schema not forwarded: %T", req.Params["output_schema"])
				}
				result = map[string]any{"task_id": "t", "session_id": "s", "status": "queued"}
			case "task.result":
				result = map[string]any{"task_id": "t", "session_id": "s", "status": "completed", "summary": "{\"status\":\"ok\"}"}
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = serverConn.Write(append(response, '\n'))
		}
		methods <- seen
	}()
	thread, err := client.StartThread("/tmp", "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	result, err := thread.Run(context.Background(), "status", RunOptions{OutputSchema: json.RawMessage(`{"type":"object"}`), PollInterval: time.Millisecond})
	if err != nil || result.FinalResponse == "" {
		t.Fatalf("%+v %v", result, err)
	}
	want := []string{"runtime.initialize", "session.create", "task.submit", "task.result"}
	if got := <-methods; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestDisconnectAndTimeoutFailCalls(t *testing.T) {
	t.Run("disconnect", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
		go func() {
			_, _ = bufio.NewReader(serverConn).ReadBytes('\n')
			_ = serverConn.Close()
		}()
		err := client.Call("daemon.status", map[string]any{}, nil)
		if err == nil {
			t.Fatal("expected disconnect error")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer serverConn.Close()
		client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
		client.SetTimeout(20 * time.Millisecond)
		go func() { _, _ = bufio.NewReader(serverConn).ReadBytes('\n') }()
		err := client.Call("daemon.status", map[string]any{}, nil)
		if !errors.Is(err, rpc.ErrCallTimeout) {
			t.Fatalf("error = %v, want ErrCallTimeout", err)
		}
	})
}
