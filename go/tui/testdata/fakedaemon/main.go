// fakedaemon is a minimal go/rpc server used only by
// go/tui/conn_reconnect_test.go to drive Connect()'s reconnect state machine
// against a real OS process: killing this process (not just closing a
// listener in-process) closes its socket connections the way an actual
// daemon crash or restart would, which an in-process fake server cannot
// reproduce (rpc.Server.Close only stops accepting new connections).
//
// Protocol: session.create returns {"session_id": "sess_1"}; session.attach
// returns cursor-based history from the event file; session.events.stream
// tails only lines appended after subscription, matching the real daemon's
// attach+live split.
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func main() {
	sock := os.Getenv("CARINA_FAKEDAEMON_SOCKET")
	if sock == "" {
		log.Fatal("fakedaemon: CARINA_FAKEDAEMON_SOCKET is required")
	}
	eventsPath := os.Getenv("CARINA_FAKEDAEMON_EVENTS")

	s := rpc.NewServer()
	s.Register("session.create", func(_ json.RawMessage) (any, error) {
		return map[string]any{"session_id": "sess_1"}, nil
	})
	s.Register("session.attach", func(params json.RawMessage) (any, error) {
		var p struct {
			Since int `json:"since"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		events := readEvents(eventsPath)
		since := p.Since
		if since < 0 {
			since = 0
		}
		if since > len(events) {
			since = len(events)
		}
		return map[string]any{
			"events": events[since:],
			"from":   since,
			"cursor": len(events),
		}, nil
	})
	s.Register("task.list", func(_ json.RawMessage) (any, error) {
		return tasksFromEvents(readEvents(eventsPath)), nil
	})
	s.Register("task.user.pending", func(_ json.RawMessage) (any, error) {
		return map[string]any{"question_ids": pendingQuestionIDs(readEvents(eventsPath))}, nil
	})
	s.RegisterStream("session.events.stream", func(_ json.RawMessage, sub *rpc.Subscription) error {
		if eventsPath == "" {
			return nil
		}
		// Capture the boundary synchronously with subscription installation:
		// lines before it belong to session.attach; lines after it belong to
		// the live tail.
		var offset int64
		if info, err := os.Stat(eventsPath); err == nil {
			offset = info.Size()
		}
		go tailEvents(eventsPath, offset, sub)
		return nil
	})

	if err := s.ListenUnix(sock); err != nil {
		log.Fatalf("fakedaemon: %v", err)
	}
}

// tailEvents polls eventsPath for new lines and publishes each as an "event"
// notification, so the test can append lines after the client subscribed.
func readEvents(path string) []json.RawMessage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var events []json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		actor, _ := ev["actor"].(string)
		if _, hasPayload := ev["payload"]; actor != "" && hasPayload {
			events = append(events, append(json.RawMessage(nil), line...))
		}
	}
	return events
}

func tasksFromEvents(events []json.RawMessage) []map[string]any {
	byID := make(map[string]map[string]any)
	var order []string
	for _, raw := range events {
		var ev map[string]any
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}
		taskID, _ := ev["task_id"].(string)
		if taskID == "" {
			continue
		}
		task := byID[taskID]
		if task == nil {
			task = map[string]any{"task_id": taskID, "status": "running"}
			byID[taskID] = task
			order = append(order, taskID)
		}
		payload, _ := ev["payload"].(map[string]any)
		if status, _ := payload["status"].(string); status != "" {
			task["status"] = status
		}
		if summary, _ := payload["summary"].(string); summary != "" {
			task["summary"] = summary
		}
		if reason, _ := payload["reason"].(string); task["summary"] == nil && reason != "" {
			task["summary"] = reason
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, taskID := range order {
		out = append(out, byID[taskID])
	}
	return out
}

func pendingQuestionIDs(events []json.RawMessage) []string {
	pending := make(map[string]bool)
	var order []string
	for _, raw := range events {
		var ev map[string]any
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}
		payload, _ := ev["payload"].(map[string]any)
		questionID, _ := payload["question_id"].(string)
		switch status, _ := payload["status"].(string); status {
		case "user_question_requested":
			if questionID != "" && !pending[questionID] {
				pending[questionID] = true
				order = append(order, questionID)
			}
		case "user_question_resolved":
			delete(pending, questionID)
		}
	}
	var out []string
	for _, questionID := range order {
		if pending[questionID] {
			out = append(out, questionID)
		}
	}
	return out
}

func tailEvents(path string, offset int64, sub *rpc.Subscription) {
	for {
		time.Sleep(20 * time.Millisecond)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, 0); err != nil {
			f.Close()
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			offset += int64(len(line)) + 1
			if len(line) == 0 {
				continue
			}
			var params json.RawMessage = append([]byte(nil), line...)
			if err := sub.Notify("event", params); err != nil {
				f.Close()
				return
			}
		}
		f.Close()
	}
}
