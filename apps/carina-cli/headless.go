package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const streamProtocol = "carina-stream-json"
const streamProtocolVersion = 1

type streamRunOptions struct {
	sessionID string
	model     string
	effort    string
	agent     string
	mode      string
}

type streamInputFrame struct {
	Type               string `json:"type"`
	RequestID          string `json:"request_id,omitempty"`
	Text               string `json:"text,omitempty"`
	TaskID             string `json:"task_id,omitempty"`
	DecisionID         string `json:"decision_id,omitempty"`
	Decision           string `json:"decision,omitempty"`
	Scope              string `json:"scope,omitempty"`
	QuestionID         string `json:"question_id,omitempty"`
	Value              string `json:"value,omitempty"`
	ClientSubmissionID string `json:"client_submission_id,omitempty"`
	Model              string `json:"model,omitempty"`
	ReasoningEffort    string `json:"reasoning_effort,omitempty"`
	Agent              string `json:"agent,omitempty"`
	Mode               string `json:"mode,omitempty"`
}

type streamWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *streamWriter) write(frame map[string]any) error {
	frame["protocol"] = streamProtocol
	frame["version"] = streamProtocolVersion
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = fmt.Fprintln(w.w, string(raw))
	return err
}

func wantsStreamJSON(args []string) bool {
	for _, arg := range args {
		if arg == "--input-format" || arg == "--output-format" {
			return true
		}
	}
	return false
}

func parseStreamRunArgs(args []string) (streamRunOptions, error) {
	var out streamRunOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if i+1 >= len(args) {
			return out, fmt.Errorf("%s requires a value", arg)
		}
		value := strings.TrimSpace(args[i+1])
		if value == "" {
			return out, fmt.Errorf("%s requires a value", arg)
		}
		switch arg {
		case "--input-format", "--output-format":
			if value != "stream-json" {
				return out, fmt.Errorf("%s must be stream-json", arg)
			}
		case "--session":
			out.sessionID = value
		case "--model", "-m":
			out.model = value
		case "--effort":
			out.effort = value
		case "--agent", "-a":
			out.agent = value
		case "--mode":
			if value != "build" && value != "plan" {
				return out, fmt.Errorf("--mode must be build or plan")
			}
			out.mode = value
		default:
			return out, fmt.Errorf("unknown stream-json flag %q", arg)
		}
		i++
	}
	if !hasArgPair(args, "--input-format", "stream-json") || !hasArgPair(args, "--output-format", "stream-json") {
		return out, errors.New("stream-json mode requires both --input-format stream-json and --output-format stream-json")
	}
	return out, nil
}

func hasArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func cmdRunStream(c *rpcClient, args []string) error {
	opts, err := parseStreamRunArgs(args)
	if err != nil {
		return fmt.Errorf("usage: carina run --input-format stream-json --output-format stream-json [--session id] [--model id] [--effort level] [--agent name] [--mode build|plan]: %w", err)
	}
	stream, err := dialHook()
	if err != nil {
		return fmt.Errorf("open stream connection: %w", err)
	}
	defer stream.Close()
	return runStreamProtocol(c, stream, opts, os.Stdin, os.Stdout)
}

func runStreamProtocol(c, stream *rpcClient, opts streamRunOptions, input io.Reader, output io.Writer) error {
	sessionID := opts.sessionID
	if sessionID == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		var session struct {
			SessionID string `json:"session_id"`
		}
		if err := c.Call("session.create", map[string]any{"workspace_root": cwd, "profile": "safe-edit"}, &session); err != nil {
			return err
		}
		sessionID = session.SessionID
	} else {
		var session struct {
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
		}
		if err := c.Call("session.get", map[string]any{"session_id": sessionID}, &session); err != nil {
			return err
		}
		if session.Status == "paused" {
			if err := c.Call("session.resume", map[string]any{"session_id": sessionID}, &session); err != nil {
				return err
			}
		}
		if session.Status != "" && session.Status != "active" {
			return fmt.Errorf("session %s is %s, not active", sessionID, session.Status)
		}
	}
	if opts.mode != "" {
		if err := c.Call("session.plan_mode", map[string]any{"session_id": sessionID, "on": opts.mode == "plan"}, nil); err != nil {
			return err
		}
	}
	if err := stream.Call("session.events.stream", map[string]any{"session_id": sessionID, "event_mode": "canonical"}, nil); err != nil {
		return err
	}

	w := &streamWriter{w: output}
	if err := w.write(map[string]any{"type": "session", "session_id": sessionID}); err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		for {
			method, params, err := stream.ReadNotification()
			if err != nil {
				errCh <- err
				return
			}
			if method != "event" {
				continue
			}
			var event map[string]any
			if json.Unmarshal(params, &event) != nil {
				continue
			}
			if err := w.write(map[string]any{"type": "event", "session_id": sessionID, "event": event}); err != nil {
				errCh <- err
				return
			}
			if control, ok := controlFrameForEvent(event); ok {
				control["type"] = control["frame"]
				delete(control, "frame")
				if err := w.write(control); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	scanner := bufio.NewScanner(input)
	buffer := make([]byte, 64<<10)
	scanner.Buffer(buffer, 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var frame streamInputFrame
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&frame); err != nil {
			_ = w.write(map[string]any{"type": "error", "code": "invalid_frame", "error": err.Error()})
			continue
		}
		result, stop, err := handleStreamInput(c, sessionID, opts, frame)
		if err != nil {
			_ = w.write(map[string]any{"type": "error", "request_id": frame.RequestID, "code": "request_failed", "error": err.Error()})
			continue
		}
		if stop {
			return w.write(map[string]any{"type": "closed", "request_id": frame.RequestID, "session_id": sessionID})
		}
		if err := w.write(map[string]any{"type": "response", "request_id": frame.RequestID, "session_id": sessionID, "result": result}); err != nil {
			return err
		}
		select {
		case err := <-errCh:
			return err
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func handleStreamInput(c *rpcClient, sessionID string, defaults streamRunOptions, frame streamInputFrame) (any, bool, error) {
	switch frame.Type {
	case "prompt":
		if strings.TrimSpace(frame.Text) == "" {
			return nil, false, errors.New("prompt text is required")
		}
		params := map[string]any{"session_id": sessionID, "prompt": frame.Text}
		optional(params, "client_submission_id", frame.ClientSubmissionID)
		optional(params, "model", firstNonEmpty(frame.Model, defaults.model))
		optional(params, "reasoning_effort", firstNonEmpty(frame.ReasoningEffort, defaults.effort))
		optional(params, "agent", firstNonEmpty(frame.Agent, defaults.agent))
		optional(params, "mode", frame.Mode)
		var out map[string]any
		err := c.Call("task.submit", params, &out)
		return out, false, err
	case "steer":
		if frame.TaskID == "" || strings.TrimSpace(frame.Text) == "" {
			return nil, false, errors.New("steer requires task_id and text")
		}
		var out map[string]any
		err := c.Call("task.steer", map[string]any{"task_id": frame.TaskID, "message": frame.Text}, &out)
		return out, false, err
	case "approval":
		if frame.DecisionID == "" || (frame.Decision != "allow" && frame.Decision != "deny") {
			return nil, false, errors.New("approval requires decision_id and decision allow|deny")
		}
		scope := frame.Scope
		if scope == "" {
			scope = "once"
		}
		if scope != "once" && scope != "session" && scope != "project" {
			return nil, false, errors.New("approval scope must be once, session, or project")
		}
		var out map[string]any
		err := c.Call("task.approval.resolve", map[string]any{"decision_id": frame.DecisionID, "approve": frame.Decision == "allow", "approver": "headless", "scope": scope}, &out)
		return out, false, err
	case "answer":
		if frame.QuestionID == "" {
			return nil, false, errors.New("answer requires question_id")
		}
		var out map[string]any
		err := c.Call("task.user.answer", map[string]any{"question_id": frame.QuestionID, "value": frame.Value}, &out)
		return out, false, err
	case "interrupt":
		if frame.TaskID == "" {
			return nil, false, errors.New("interrupt requires task_id")
		}
		var out map[string]any
		err := c.Call("task.cancel", map[string]any{"task_id": frame.TaskID}, &out)
		return out, false, err
	case "close":
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("unknown frame type %q", frame.Type)
	}
}

func optional(values map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		values[key] = value
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
