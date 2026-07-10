package daemon

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const backpressureReportTTL = 30 * time.Second

type PressureReport struct {
	WorkerID           string    `json:"worker_id"`
	QueueDepth         int       `json:"queue_depth"`
	Inflight           int       `json:"inflight"`
	MemUsagePermille   int       `json:"mem_usage_permille"`
	ProcLoadPermille   int       `json:"proc_load_permille"`
	EstDrainMs         int64     `json:"est_drain_ms"`
	Seq                uint64    `json:"seq"`
	ReportedAt         time.Time `json:"reported_at"`
	Accepted           bool      `json:"accepted"`
	RejectedAsStaleSeq bool      `json:"rejected_as_stale_seq,omitempty"`
}

type ThrottleDirective struct {
	WorkerID     string    `json:"worker_id,omitempty"`
	Level        string    `json:"level"` // none | warn | throttle | pause
	MaxInflight  int       `json:"max_inflight"`
	RetryAfterMs int64     `json:"retry_after_ms"`
	Reason       string    `json:"reason,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

type backpressureManager struct {
	mu      sync.Mutex
	reports map[string]PressureReport
}

func newBackpressureManager() *backpressureManager {
	return &backpressureManager{reports: map[string]PressureReport{}}
}

func (m *backpressureManager) report(r PressureReport, now time.Time) (PressureReport, ThrottleDirective) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.WorkerID = strings.TrimSpace(r.WorkerID)
	r.QueueDepth = clampNonNegative(r.QueueDepth)
	r.Inflight = clampNonNegative(r.Inflight)
	r.MemUsagePermille = clampPermille(r.MemUsagePermille)
	r.ProcLoadPermille = clampPermille(r.ProcLoadPermille)
	if r.EstDrainMs < 0 {
		r.EstDrainMs = 0
	}
	if prev, ok := m.reports[r.WorkerID]; ok && r.Seq != 0 && prev.Seq != 0 && r.Seq <= prev.Seq {
		prev.Accepted = false
		prev.RejectedAsStaleSeq = true
		return prev, directiveForPressure(prev, now)
	}
	r.ReportedAt = now.UTC()
	r.Accepted = true
	m.reports[r.WorkerID] = r
	return r, directiveForPressure(r, now)
}

func (m *backpressureManager) directive(workerID string, now time.Time) ThrottleDirective {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.reports[workerID]
	if !ok || now.Sub(r.ReportedAt) > backpressureReportTTL {
		return ThrottleDirective{WorkerID: workerID, Level: "none", MaxInflight: 1}
	}
	return directiveForPressure(r, now)
}

func (m *backpressureManager) snapshot(now time.Time) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	reports := make([]PressureReport, 0, len(m.reports))
	directives := make([]ThrottleDirective, 0, len(m.reports))
	for _, r := range m.reports {
		reports = append(reports, r)
		directives = append(directives, directiveForPressure(r, now))
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].WorkerID < reports[j].WorkerID })
	sort.Slice(directives, func(i, j int) bool { return directives[i].WorkerID < directives[j].WorkerID })
	return map[string]any{
		"reports":     reports,
		"directives":  directives,
		"ttl_seconds": int(backpressureReportTTL.Seconds()),
	}
}

func (m *backpressureManager) summary(now time.Time) map[string]any {
	snap := m.snapshot(now)
	directives, _ := snap["directives"].([]ThrottleDirective)
	levels := map[string]int{"none": 0, "warn": 0, "throttle": 0, "pause": 0}
	for _, d := range directives {
		levels[d.Level]++
	}
	return map[string]any{
		"workers_reporting": len(directives),
		"levels":            levels,
		"ttl_seconds":       int(backpressureReportTTL.Seconds()),
	}
}

func directiveForPressure(r PressureReport, now time.Time) ThrottleDirective {
	d := ThrottleDirective{WorkerID: r.WorkerID, Level: "none", MaxInflight: 1}
	switch {
	case r.MemUsagePermille >= 950:
		d.Level, d.MaxInflight, d.RetryAfterMs, d.Reason = "pause", 0, 5000, "mem_critical"
	case r.MemUsagePermille >= 900:
		d.Level, d.MaxInflight, d.RetryAfterMs, d.Reason = "throttle", 0, 3000, "mem_high"
	case r.ProcLoadPermille >= 950:
		d.Level, d.MaxInflight, d.RetryAfterMs, d.Reason = "pause", 0, 3000, "load_critical"
	case r.EstDrainMs >= 30000:
		d.Level, d.MaxInflight, d.RetryAfterMs, d.Reason = "throttle", 0, 3000, "drain_slow"
	case r.QueueDepth >= 16:
		d.Level, d.MaxInflight, d.RetryAfterMs, d.Reason = "throttle", 0, 2000, "queue_saturated"
	case r.Inflight >= 2 || r.ProcLoadPermille >= 800 || r.EstDrainMs >= 10000 || r.QueueDepth >= 8:
		d.Level, d.MaxInflight, d.RetryAfterMs, d.Reason = "warn", 1, 1000, "pressure_elevated"
	}
	if d.Level != "none" {
		d.ExpiresAt = now.UTC().Add(backpressureReportTTL)
	}
	return d
}

func clampNonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func clampPermille(n int) int {
	if n < 0 {
		return 0
	}
	if n > 1000 {
		return 1000
	}
	return n
}

func (d *Daemon) handleBackpressureReport(params json.RawMessage) (any, error) {
	var p struct {
		PressureReport
		WorkerCredential string `json:"worker_credential"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if strings.TrimSpace(p.WorkerID) == "" {
		return nil, fmt.Errorf("worker_id is required")
	}
	if err := d.authenticateWorker(p.WorkerID, p.WorkerCredential); err != nil {
		return nil, err
	}
	_ = d.pool.Heartbeat(p.WorkerID)
	report, directive := d.backpressure.report(p.PressureReport, time.Now().UTC())
	d.emitDebug("backpressure", "report", report.WorkerID, map[string]string{
		"worker_id": report.WorkerID,
		"level":     directive.Level,
		"reason":    directive.Reason,
	})
	return map[string]any{"accepted": report.Accepted, "report": report, "directive": directive}, nil
}

func (d *Daemon) handleBackpressureStatus(json.RawMessage) (any, error) {
	now := time.Now().UTC()
	status := d.backpressure.snapshot(now)
	status["scheduler"] = map[string]any{
		"tasks_by_status": d.sched.CountByStatus(),
		"dispatch_depth":  d.sched.DispatchDepth(),
		"workers":         len(d.pool.List()),
	}
	return status, nil
}
