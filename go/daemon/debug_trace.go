package daemon

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultDebugTraceCapacity = 4096

type DebugTraceEvent struct {
	Seq           uint64            `json:"seq"`
	Timestamp     time.Time         `json:"timestamp"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	Component     string            `json:"component"`
	Action        string            `json:"action"`
	Attrs         map[string]string `json:"attrs,omitempty"`
}

type debugTrace struct {
	mu      sync.Mutex
	buf     []DebugTraceEvent
	next    int
	count   int
	seq     uint64
	dropped uint64
}

func newDebugTrace(capacity int) *debugTrace {
	if capacity <= 0 {
		capacity = defaultDebugTraceCapacity
	}
	return &debugTrace{buf: make([]DebugTraceEvent, capacity)}
}

func (t *debugTrace) emit(component, action, correlationID string, attrs map[string]string) {
	component = truncateDebugString(strings.TrimSpace(component), 64)
	action = truncateDebugString(strings.TrimSpace(action), 64)
	if component == "" || action == "" {
		return
	}
	ev := DebugTraceEvent{
		Timestamp:     time.Now().UTC(),
		CorrelationID: truncateDebugString(strings.TrimSpace(correlationID), 96),
		Component:     component,
		Action:        action,
		Attrs:         sanitizeDebugAttrs(attrs),
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	ev.Seq = t.seq
	if t.count == len(t.buf) {
		t.dropped++
	} else {
		t.count++
	}
	t.buf[t.next] = ev
	t.next = (t.next + 1) % len(t.buf)
}

func (t *debugTrace) snapshot(limit int, filter func(DebugTraceEvent) bool) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if limit <= 0 || limit > len(t.buf) {
		limit = len(t.buf)
	}
	events := make([]DebugTraceEvent, 0, t.count)
	start := (t.next - t.count + len(t.buf)) % len(t.buf)
	for i := 0; i < t.count; i++ {
		ev := t.buf[(start+i)%len(t.buf)]
		if filter == nil || filter(ev) {
			events = append(events, ev)
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq > events[j].Seq })
	if len(events) > limit {
		events = events[:limit]
	}
	return map[string]any{
		"events":   events,
		"dropped":  t.dropped,
		"capacity": len(t.buf),
	}
}

func (t *debugTrace) stats(enabled bool) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]any{
		"enabled":  enabled,
		"events":   t.count,
		"dropped":  t.dropped,
		"capacity": len(t.buf),
	}
}

func (d *Daemon) debugTraceStats() map[string]any {
	if d.debugTrace == nil {
		return map[string]any{"enabled": d.debugRPCEnabled.Load(), "events": 0, "dropped": 0, "capacity": 0}
	}
	return d.debugTrace.stats(d.debugRPCEnabled.Load())
}

func sanitizeDebugAttrs(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := map[string]string{}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(out) >= 16 {
			break
		}
		key := truncateDebugString(strings.TrimSpace(k), 64)
		if key == "" {
			continue
		}
		out[key] = truncateDebugString(attrs[k], 256)
	}
	return out
}

func truncateDebugString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (d *Daemon) emitDebug(component, action, correlationID string, attrs map[string]string) {
	if d.debugTrace != nil && d.debugRPCEnabled.Load() {
		d.debugTrace.emit(component, action, correlationID, attrs)
	}
}

func (d *Daemon) handleDebugSnapshot(params json.RawMessage) (any, error) {
	if !d.debugRPCEnabled.Load() {
		return nil, fmt.Errorf("debug RPC is disabled")
	}
	if d.debugTrace == nil {
		return nil, fmt.Errorf("debug trace is not initialized")
	}
	var p struct {
		Limit     int    `json:"limit"`
		Component string `json:"component"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	component := strings.TrimSpace(p.Component)
	filter := func(ev DebugTraceEvent) bool {
		return component == "" || ev.Component == component
	}
	out := d.debugTrace.snapshot(p.Limit, filter)
	out["enabled"] = true
	return out, nil
}

func (d *Daemon) handleDebugCorrelation(params json.RawMessage) (any, error) {
	if !d.debugRPCEnabled.Load() {
		return nil, fmt.Errorf("debug RPC is disabled")
	}
	if d.debugTrace == nil {
		return nil, fmt.Errorf("debug trace is not initialized")
	}
	var p struct {
		CorrelationID string `json:"correlation_id"`
		Limit         int    `json:"limit"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	correlationID := strings.TrimSpace(p.CorrelationID)
	if correlationID == "" {
		return nil, fmt.Errorf("correlation_id is required")
	}
	out := d.debugTrace.snapshot(p.Limit, func(ev DebugTraceEvent) bool {
		return ev.CorrelationID == correlationID
	})
	out["enabled"] = true
	out["correlation_id"] = correlationID
	return out, nil
}
