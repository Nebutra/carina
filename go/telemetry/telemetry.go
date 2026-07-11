// Package telemetry emits Carina's documented newline JSON telemetry format.
// It is not OTLP and does not claim wire compatibility with OpenTelemetry.
package telemetry

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

const Format = "carina-telemetry-json-v1"

type Attribution struct {
	TenantID    string `json:"tenant_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	WorkflowID  string `json:"workflow_id,omitempty"`
	StepID      string `json:"step_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	PluginID    string `json:"plugin_id,omitempty"`
	WorkerID    string `json:"worker_id,omitempty"`
}
type Cost struct {
	Requests         int64   `json:"requests"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	USD              float64 `json:"usd"`
	Estimated        bool    `json:"estimated"`
}
type Record struct {
	Format     string         `json:"format"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	TraceID    string         `json:"trace_id,omitempty"`
	SpanID     string         `json:"span_id,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Status     string         `json:"status,omitempty"`
	Attributes Attribution    `json:"attributes"`
	Cost       Cost           `json:"cost"`
	Body       map[string]any `json:"body,omitempty"`
}
type Exporter struct {
	mu  sync.Mutex
	out io.Writer
	now func() time.Time
}

func New(out io.Writer) *Exporter { return &Exporter{out: out, now: time.Now} }
func (e *Exporter) Enabled() bool { return e != nil && e.out != nil }
func (e *Exporter) Emit(r Record) error {
	if !e.Enabled() {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.Format == "" {
		r.Format = Format
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = e.now().UTC()
	}
	return json.NewEncoder(e.out).Encode(r)
}
func (e *Exporter) Span(name, traceID, spanID string, a Attribution, c Cost, duration time.Duration, status string) error {
	return e.Emit(Record{Kind: "span", Name: name, TraceID: traceID, SpanID: spanID, DurationMS: duration.Milliseconds(), Status: status, Attributes: a, Cost: c})
}
func (e *Exporter) Metric(name string, a Attribution, c Cost) error {
	return e.Emit(Record{Kind: "metric", Name: name, Attributes: a, Cost: c})
}
func (e *Exporter) Log(name string, a Attribution, body map[string]any) error {
	return e.Emit(Record{Kind: "log", Name: name, Attributes: a, Body: body})
}
