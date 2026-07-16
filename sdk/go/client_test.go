package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func TestCommonControlPlaneWrappers(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()

	want := []string{
		"session.get", "session.list", "session.replay", "workspace.search",
		"workspace.file.get", "workspace.patch.propose", "workspace.patch.apply",
		"workspace.patch.rollback", "command.exec", "audit.report",
	}
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		for i, method := range want {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				done <- err
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(line, &request); err != nil {
				done <- err
				return
			}
			if request.Method != method {
				done <- fmt.Errorf("call %d method = %s, want %s", i, request.Method, method)
				return
			}
			result := any(map[string]any{})
			switch method {
			case "session.get":
				result = map[string]any{"session_id": "s1"}
			case "session.list":
				result = []any{map[string]any{"session_id": "s1"}}
			case "session.replay":
				result = []any{map[string]any{
					"session_id": "s1", "type": "TaskCreated", "timestamp": "now",
					"permission_decision_id": "perm_1", "actor": "go", "prev_hash": "prev", "event_hash": "event",
				}}
			case "workspace.search":
				result = []any{map[string]any{"file": "a.go", "line": 3, "text": "TODO"}}
			case "workspace.file.get":
				result = map[string]any{"content": "package a", "hash": "abc"}
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			if _, err := serverConn.Write(append(response, '\n')); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	if session, err := client.GetSession("s1"); err != nil || session.SessionID != "s1" {
		t.Fatalf("get session: %+v %v", session, err)
	}
	if sessions, err := client.ListSessions(); err != nil || len(sessions) != 1 {
		t.Fatalf("list sessions: %+v %v", sessions, err)
	}
	if events, err := client.ReplaySession("s1"); err != nil || len(events) != 1 {
		t.Fatalf("replay: %+v %v", events, err)
	} else if events[0].PermissionDecisionID != "perm_1" || events[0].Actor != "go" || events[0].PrevHash != "prev" || events[0].EventHash != "event" {
		t.Fatalf("replay audit fields = %+v", events[0])
	}
	if hits, err := client.SearchWorkspace("s1", "TODO"); err != nil || len(hits) != 1 || hits[0].Line != 3 {
		t.Fatalf("search: %+v %v", hits, err)
	}
	if file, err := client.GetWorkspaceFile("s1", "a.go"); err != nil || file.Hash != "abc" {
		t.Fatalf("file: %+v %v", file, err)
	}
	if _, err := client.ProposePatch("s1", []map[string]string{{"path": "a.go", "content": "package b"}}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ApplyPatch("s1", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RollbackPatch("s1", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exec("s1", []string{"go", "test", "./..."}, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AuditReport("s1"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestResumeTaskUsesCanonicalRPC(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(serverConn).ReadBytes('\n')
		if err != nil {
			done <- err
			return
		}
		var request struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			done <- err
			return
		}
		if request.Method != "task.resume" || request.Params["task_id"] != "t1" {
			done <- fmt.Errorf("request = %+v", request)
			return
		}
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"task_id": "t1", "session_id": "s1", "status": "running"}})
		_, err = serverConn.Write(append(response, '\n'))
		done <- err
	}()

	task, err := client.ResumeTask("t1")
	if err != nil || task.TaskID != "t1" || task.Status != "running" {
		t.Fatalf("resume task = %+v, err=%v", task, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointTypedWrappersDecodeCanonicalFixtures(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		for _, method := range []string{"session.checkpoint.preview", "session.checkpoint.summarize", "session.checkpoint.restore"} {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				done <- err
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(line, &request); err != nil || request.Method != method {
				done <- fmt.Errorf("request method = %s, want %s, decode=%v", request.Method, method, err)
				return
			}
			checkpoint := map[string]any{"checkpoint_id": "t:1:9", "created_at": "2026-07-14T00:00:00Z", "sequence": "00000000000000000009", "task_id": "t", "session_id": "s", "turn": 1, "applied_patches": []string{}}
			var result any
			switch method {
			case "session.checkpoint.preview":
				result = map[string]any{"checkpoint": checkpoint, "conversation_turns": 1, "rollback_patches": []string{}, "will_resume": "paused"}
			case "session.checkpoint.summarize":
				result = map[string]any{"checkpoint_id": "t:1:9", "task_id": "t", "turn": 1, "recent": []any{}}
			case "session.checkpoint.restore":
				result = map[string]any{"restored": true, "checkpoint_id": "t:1:9", "task_id": "t", "turn": 1, "rolled_back": []string{}, "status": "paused", "idempotent": true, "reconciliation_required": false, "journal_cleanup_pending": false}
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			if _, err := serverConn.Write(append(response, '\n')); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	preview, err := client.PreviewCheckpoint("s", "t:1:9")
	if err != nil || preview.Checkpoint.Sequence != "00000000000000000009" || preview.WillResume != "paused" {
		t.Fatalf("preview = %+v, err=%v", preview, err)
	}
	summary, err := client.SummarizeCheckpoint("s", "t:1:9")
	if err != nil || summary.Turn != 1 {
		t.Fatalf("summary = %+v, err=%v", summary, err)
	}
	restored, err := client.RestoreCheckpoint("s", "t:1:9", true)
	if err != nil || !restored.Restored || !restored.Idempotent {
		t.Fatalf("restore = %+v, err=%v", restored, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTypedParityAndEventSubscription(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()

	methods := make(chan []string, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		seen := []string{}
		for len(seen) < 8 {
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
			case "session.review":
				result = reviewFixture()
			case "session.items":
				result = map[string]any{"data": []any{map[string]any{"type": "turn.started", "session_id": "s1", "task_id": "t1"}}, "next_cursor": "cp1.payload.signature", "projection_version": "1.0.0"}
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

	if CompatibleRuntimeVersion != "0.6.3" {
		t.Fatalf("compatibility version = %s", CompatibleRuntimeVersion)
	}
	if attached, err := client.AttachSession("s1", 3); err != nil || attached.Cursor != 7 {
		t.Fatalf("attach = %+v, %v", attached, err)
	}
	if forked, err := client.ForkSession("s1"); err != nil || forked.SessionID != "child" {
		t.Fatalf("fork = %+v, %v", forked, err)
	}
	if review, err := client.ReviewSession("s1"); err != nil || review.State != "completed" || review.ProjectionVersion != "1.0.0" {
		t.Fatalf("review = %+v, %v", review, err)
	}
	if page, err := client.ListSessionItems("s1", "", 1); err != nil || len(page.Data) != 1 || page.NextCursor == "" {
		t.Fatalf("items = %+v, %v", page, err)
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
	want := []string{"session.attach", "session.fork", "session.review", "session.items", "usage.cost", "task.steer", "task.user.answer", "session.events.stream"}
	if got := <-methods; !reflect.DeepEqual(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
}

func TestResolveApprovalUsesCanonicalApproveParam(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()
	params := make(chan map[string]any, 1)
	go func() {
		line, _ := bufio.NewReader(serverConn).ReadBytes('\n')
		var request struct {
			ID     json.RawMessage `json:"id"`
			Params map[string]any  `json:"params"`
		}
		_ = json.Unmarshal(line, &request)
		params <- request.Params
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resolved": true}})
		_, _ = serverConn.Write(append(response, '\n'))
	}()
	if err := client.ResolveApproval("decision-1", true, "sdk", "once"); err != nil {
		t.Fatal(err)
	}
	got := <-params
	if got["approve"] != true {
		t.Fatalf("approve param = %#v", got)
	}
	if _, legacy := got["allow"]; legacy {
		t.Fatalf("legacy allow param leaked: %#v", got)
	}
}

func reviewFixture() map[string]any {
	return map[string]any{"session_id": "s1", "projection_version": "1.0.0", "source_cursor": "cp1.payload.signature", "state": "completed", "intent": "ship", "success_criteria": []any{map[string]any{"kind": "command"}}, "changes": []any{}, "commands": []any{}, "tools": []any{}, "checks": []any{}, "diagnostics": []any{}, "policy_decisions": []any{}, "questions": []any{}, "conflicts": []any{}, "risk_and_policy": []any{}, "artifact_ids": []any{}, "rollback": map[string]any{"available": false, "patch_ids": []any{}}, "stats": map[string]any{}}
}

func TestCursorRecoveryFromTypedRPCError(t *testing.T) {
	source := CursorRecovery{Code: "cursor_expired", ProjectionVersion: "1.0.0", Recovery: "snapshot", SnapshotMethod: "session.items", EarliestCursor: "cp1.x.y"}
	recovery, ok := CursorRecoveryFromError(&rpc.Error{Code: -32010, Message: "cursor_expired", Data: source})
	if !ok || recovery.Code != "cursor_expired" || recovery.EarliestCursor == "" {
		t.Fatalf("typed cursor recovery lost: %+v %v", recovery, ok)
	}
}

func TestRunStreamedEmitsAuthoritativeEventsAndUnsubscribes(t *testing.T) {
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
			}
			_ = json.Unmarshal(line, &req)
			seen = append(seen, req.Method)
			var result any = map[string]any{}
			switch req.Method {
			case "session.events.stream":
				result = map[string]any{"subscription_id": "sub-1"}
			case "task.submit":
				result = map[string]any{"task_id": "t", "session_id": "s", "status": "queued"}
			case "task.result":
				note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "event", "params": map[string]any{"session_id": "s", "task_id": "t", "type": "ModelResponded", "timestamp": "now"}})
				_, _ = serverConn.Write(append(note, '\n'))
				result = map[string]any{"task_id": "t", "session_id": "s", "status": "completed", "summary": "ok"}
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = serverConn.Write(append(response, '\n'))
		}
		methods <- seen
	}()
	thread := &Thread{client: client, Session: Session{SessionID: "s"}}
	var got []StreamEvent
	for event := range thread.RunStreamed(context.Background(), "go", RunOptions{PollInterval: time.Millisecond}) {
		got = append(got, event)
	}
	if len(got) != 2 || got[0].Type != "event" || got[0].Event.Type != "ModelResponded" || got[1].Type != "turn.completed" {
		t.Fatalf("stream = %+v", got)
	}
	want := []string{"session.events.stream", "task.submit", "task.result", "session.events.unsubscribe"}
	if seen := <-methods; !reflect.DeepEqual(seen, want) {
		t.Fatalf("methods = %v, want %v", seen, want)
	}
}

func TestRunStreamedCancellationCancelsTaskAndUnsubscribes(t *testing.T) {
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
			}
			_ = json.Unmarshal(line, &req)
			seen = append(seen, req.Method)
			result := any(map[string]any{})
			if req.Method == "session.events.stream" {
				result = map[string]any{"subscription_id": "sub-cancel"}
			} else if req.Method == "task.submit" {
				result = map[string]any{"task_id": "t", "session_id": "s", "status": "queued"}
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = serverConn.Write(append(response, '\n'))
		}
		methods <- seen
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	thread := &Thread{client: client, Session: Session{SessionID: "s"}}
	var last StreamEvent
	for event := range thread.RunStreamed(ctx, "go", RunOptions{PollInterval: time.Millisecond}) {
		last = event
	}
	if !errors.Is(last.Err, context.Canceled) {
		t.Fatalf("stream error = %v, want context.Canceled", last.Err)
	}
	want := []string{"session.events.stream", "task.submit", "task.cancel", "session.events.unsubscribe"}
	if seen := <-methods; !reflect.DeepEqual(seen, want) {
		t.Fatalf("methods = %v, want %v", seen, want)
	}
}

func TestConcurrentRunStreamedRoutesEventsBySession(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()
	var mu sync.Mutex
	unsubscribed := map[string]bool{}
	go func() {
		reader := bufio.NewReader(serverConn)
		for {
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
			var result any = map[string]any{}
			session, _ := req.Params["session_id"].(string)
			switch req.Method {
			case "session.events.stream":
				result = map[string]any{"subscription_id": "sub-" + session}
			case "task.submit":
				result = map[string]any{"task_id": "task-" + session, "session_id": session, "status": "queued"}
			case "task.result":
				task, _ := req.Params["task_id"].(string)
				session = strings.TrimPrefix(task, "task-")
				note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "event", "params": map[string]any{"session_id": session, "task_id": task, "type": "ModelResponded"}})
				_, _ = serverConn.Write(append(note, '\n'))
				result = map[string]any{"task_id": task, "session_id": session, "status": "completed", "summary": session}
			case "session.events.unsubscribe":
				mu.Lock()
				unsubscribed[req.Params["subscription_id"].(string)] = true
				mu.Unlock()
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = serverConn.Write(append(response, '\n'))
		}
	}()
	threads := []*Thread{{client: client, Session: Session{SessionID: "s1"}}, {client: client, Session: Session{SessionID: "s2"}}}
	results := make(chan string, 2)
	for _, thread := range threads {
		go func(th *Thread) {
			var session string
			for event := range th.RunStreamed(context.Background(), "go", RunOptions{PollInterval: time.Millisecond}) {
				if event.Event != nil {
					session = event.Event.SessionID
				}
			}
			results <- session
		}(thread)
	}
	got := map[string]bool{<-results: true, <-results: true}
	if !got["s1"] || !got["s2"] {
		t.Fatalf("cross-routed streams: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !unsubscribed["sub-s1"] || !unsubscribed["sub-s2"] {
		t.Fatalf("subscriptions leaked: %+v", unsubscribed)
	}
}

func TestRunStreamedAbandonedConsumerCancelsAndUnsubscribes(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := NewClient(rpc.NewClient(clientConn, clientConn, clientConn))
	defer client.Close()
	done := make(chan []string, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		var seen []string
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(line, &req)
			seen = append(seen, req.Method)
			var result any = map[string]any{}
			switch req.Method {
			case "session.events.stream":
				result = map[string]any{"subscription_id": "sub-s"}
			case "task.submit":
				result = map[string]any{"task_id": "t", "session_id": "s", "status": "queued"}
			case "task.result":
				for i := 0; i < 100; i++ {
					note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "event", "params": map[string]any{"session_id": "s", "task_id": "t", "type": "Output", "payload": map[string]any{"n": i}}})
					_, _ = serverConn.Write(append(note, '\n'))
				}
				result = map[string]any{"task_id": "t", "session_id": "s", "status": "running"}
			}
			response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = serverConn.Write(append(response, '\n'))
			if req.Method == "session.events.unsubscribe" {
				done <- seen
				return
			}
		}
	}()
	thread := &Thread{client: client, Session: Session{SessionID: "s"}}
	stream := thread.RunStreamed(context.Background(), "go", RunOptions{PollInterval: time.Millisecond})
	select {
	case seen := <-done:
		if !containsString(seen, "task.cancel") || !containsString(seen, "session.events.unsubscribe") {
			t.Fatalf("missing cleanup: %v", seen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned consumer left RunStreamed blocked")
	}
	var streamErr error
	for event := range stream {
		if event.Err != nil {
			streamErr = event.Err
		}
	}
	if !errors.Is(streamErr, ErrStreamOverflow) {
		t.Fatalf("stream error = %v, want ErrStreamOverflow", streamErr)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
				result = map[string]any{"runtime_version": "0.6.3", "protocol_version": "1.2.0", "projection_version": "1.0.0", "capabilities": map[string]any{"tool_call_lifecycle": true, "event_schema_version": "0.3.0"}}
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

func TestRuntimeInfoValidation(t *testing.T) {
	valid := RuntimeInfo{ProtocolVersion: "1.9.0", Capabilities: map[string]any{"tool_call_lifecycle": true, "event_schema_version": "0.3.9"}}
	if err := validateRuntimeInfo(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []RuntimeInfo{
		{ProtocolVersion: "2.0.0", Capabilities: map[string]any{"tool_call_lifecycle": true}},
		{ProtocolVersion: "1.2.0", Capabilities: map[string]any{}},
		{ProtocolVersion: "1.2.0", Capabilities: map[string]any{"tool_call_lifecycle": true, "event_schema_version": "0.4.0"}},
	} {
		if err := validateRuntimeInfo(invalid); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
}
