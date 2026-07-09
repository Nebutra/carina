package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/rpc"
)

// Sender delivers messages into the running program from goroutines.
// *tea.Program satisfies it.
type Sender interface {
	Send(tea.Msg)
}

// backoff returns the delay before reconnect attempt n: exponential from 1s,
// capped at 30s.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	d := time.Second << (attempt - 1)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// Connect drives the daemon link on the spike's two-connection go/rpc
// pattern: one request/response connection for calls, one dedicated
// connection subscribed to session.events.stream, with program.Send from the
// stream goroutine. Every failure surfaces as a message (degrade banner);
// reconnects are attempted forever with visible attempt counts.
func Connect(p Sender, socket, sessionID, workspaceRoot string) {
	go func() {
		sid := sessionID
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				p.Send(ReconnectingMsg{Attempt: attempt})
				time.Sleep(backoff(attempt))
			}

			call, err := rpc.Dial(socket)
			if err != nil {
				if attempt == 0 {
					p.Send(ConnLostMsg{Err: err})
				}
				continue
			}
			if sid == "" {
				ws := workspaceRoot
				if ws == "" {
					ws, _ = os.Getwd()
				}
				var out struct {
					SessionID string `json:"session_id"`
				}
				if err := call.Call("session.create", map[string]any{
					"workspace_root": ws,
					"profile":        "safe-edit",
				}, &out); err != nil {
					call.Close()
					p.Send(ConnLostMsg{Err: err})
					continue
				}
				sid = out.SessionID
			}

			stream, err := rpc.Dial(socket)
			if err != nil {
				call.Close()
				p.Send(ConnLostMsg{Err: err})
				continue
			}
			if err := stream.Call("session.events.stream", map[string]any{"session_id": sid}, nil); err != nil {
				call.Close()
				stream.Close()
				p.Send(ConnLostMsg{Err: err})
				continue
			}

			if attempt > 0 {
				p.Send(ConnRestoredMsg{SessionID: sid})
			}
			p.Send(SessionReadyMsg{SessionID: sid, Call: call})
			attempt = 0 // healthy link resets the retry budget

			for {
				method, params, err := stream.ReadNotification()
				if err != nil {
					p.Send(ConnLostMsg{Err: fmt.Errorf("event stream closed: %w", err)})
					break
				}
				if method != "event" {
					continue
				}
				var ev map[string]any
				if json.Unmarshal(params, &ev) == nil {
					p.Send(EventMsg{Raw: ev})
				}
			}
			call.Close()
			stream.Close()
		}
	}()
}
