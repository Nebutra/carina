package daemon

import (
	"sort"
	"sync"
	"time"
)

const journeyMetricSampleLimit = 512

type journeySessionMetric struct {
	acceptedAt        time.Time
	firstResponse     bool
	firstVerifiedEdit bool
}

type journeyMetricSeries []int64

func (s *journeyMetricSeries) add(value time.Duration) {
	ms := value.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	*s = append(*s, ms)
	if len(*s) > journeyMetricSampleLimit {
		copy(*s, (*s)[len(*s)-journeyMetricSampleLimit:])
		*s = (*s)[:journeyMetricSampleLimit]
	}
}

func (s journeyMetricSeries) snapshot() map[string]any {
	values := append([]int64(nil), s...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	percentile := func(percent int) int64 {
		if len(values) == 0 {
			return 0
		}
		index := (len(values)*percent + 99) / 100
		if index < 1 {
			index = 1
		}
		return values[index-1]
	}
	return map[string]any{
		"count":  len(values),
		"p50_ms": percentile(50),
		"p95_ms": percentile(95),
	}
}

// journeyMetrics owns the measurable first-five-minute funnel. It records
// only durable daemon milestones, so UI reconnects cannot double-count them.
type journeyMetrics struct {
	mu  sync.Mutex
	now func() time.Time

	readinessStarted time.Time
	readyRecorded    bool
	runs             map[string]string
	sessions         map[string]*journeySessionMetric
	acceptedCount    int
	verifiedCount    int

	timeToReady             journeyMetricSeries
	timeToFirstResponse     journeyMetricSeries
	timeToFirstVerifiedEdit journeyMetricSeries
}

func newJourneyMetrics(now func() time.Time) *journeyMetrics {
	if now == nil {
		now = time.Now
	}
	return &journeyMetrics{
		now: now, runs: map[string]string{}, sessions: map[string]*journeySessionMetric{},
	}
}

func (m *journeyMetrics) observeReady(ready bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.readinessStarted.IsZero() {
		m.readinessStarted = now
	}
	if !ready {
		return
	}
	if m.readyRecorded {
		return
	}
	m.readyRecorded = true
	m.timeToReady.add(now.Sub(m.readinessStarted))
}

func (m *journeyMetrics) accepted(runID, sessionID string) {
	if m == nil || runID == "" || sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.runs[runID]; exists {
		return
	}
	m.runs[runID] = sessionID
	if _, exists := m.sessions[sessionID]; !exists {
		m.sessions[sessionID] = &journeySessionMetric{acceptedAt: m.now()}
		m.acceptedCount++
	}
}

func (m *journeyMetrics) observeEvent(eventType, runID string) {
	if m == nil || runID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sessionID := m.runs[runID]
	session := m.sessions[sessionID]
	if session == nil {
		return
	}
	now := m.now()
	switch eventType {
	case "assistant.message.delta", "assistant.message.completed", "ModelResponded", "ToolCallStarted":
		if !session.firstResponse {
			session.firstResponse = true
			m.timeToFirstResponse.add(now.Sub(session.acceptedAt))
		}
	case "PatchApplied":
		if !session.firstVerifiedEdit {
			session.firstVerifiedEdit = true
			m.verifiedCount++
			m.timeToFirstVerifiedEdit.add(now.Sub(session.acceptedAt))
		}
	case "ExecutionCompleted", "ExecutionFailed", "ExecutionCancelled":
		delete(m.runs, runID)
	}
}

func (m *journeyMetrics) snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	rate := 0.0
	if m.acceptedCount > 0 {
		rate = float64(m.verifiedCount) / float64(m.acceptedCount)
	}
	return map[string]any{
		"time_to_ready":                    m.timeToReady.snapshot(),
		"time_to_first_response":           m.timeToFirstResponse.snapshot(),
		"time_to_first_verified_edit":      m.timeToFirstVerifiedEdit.snapshot(),
		"accepted_submissions":             m.acceptedCount,
		"verified_edit_submissions":        m.verifiedCount,
		"first_verified_edit_success_rate": rate,
	}
}
