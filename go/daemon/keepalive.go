package daemon

import (
	"context"
	"sync"
	"time"
)

// executionKeepaliveAfter is the wait before the first bus-only liveness ping
// and the interval between later pings. Tests may shorten it.
var executionKeepaliveAfter = time.Second

type executionKeepaliveKey struct{}

type executionKeepalive struct {
	d         *Daemon
	sessionID string
	taskID    string
}

func withExecutionKeepalive(ctx context.Context, d *Daemon, sessionID, taskID string) context.Context {
	if d == nil || sessionID == "" || taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, executionKeepaliveKey{}, executionKeepalive{
		d: d, sessionID: sessionID, taskID: taskID,
	})
}

func startThinkKeepalive(ctx context.Context) func() {
	ka, ok := ctx.Value(executionKeepaliveKey{}).(executionKeepalive)
	if !ok || ka.d == nil {
		return func() {}
	}
	return ka.d.startExecutionKeepalive(ctx, ka.sessionID, ka.taskID, "think")
}

// startExecutionKeepalive publishes bus-only execution.keepalive events after
// `executionKeepaliveAfter` while a Think or tool wait is in flight. It must
// not write the hash-chained audit log.
func (d *Daemon) startExecutionKeepalive(ctx context.Context, sessionID, taskID, stage string) func() {
	if d == nil || d.events == nil || sessionID == "" || taskID == "" {
		return func() {}
	}
	after := executionKeepaliveAfter
	if after <= 0 {
		after = time.Second
	}
	stopCh := make(chan struct{})
	var once sync.Once
	started := time.Now()
	go func() {
		timer := time.NewTimer(after)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-timer.C:
				d.publishExecutionKeepalive(sessionID, taskID, stage, time.Since(started).Milliseconds())
				timer.Reset(after)
			}
		}
	}()
	return func() {
		once.Do(func() { close(stopCh) })
	}
}

func (d *Daemon) publishExecutionKeepalive(sessionID, taskID, stage string, elapsedMs int64) {
	if d == nil || d.events == nil {
		return
	}
	d.events.Publish(sessionID, map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
		"type":       "execution.keepalive",
		"actor":      "go",
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"status":     "keepalive",
			"stage":      stage,
			"elapsed_ms": elapsedMs,
		},
	})
}
