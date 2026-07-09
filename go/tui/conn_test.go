package tui

import (
	"testing"
	"time"
)

// Reconnect backoff: exponential from 1s, capped at 30s (P3.3 direction —
// the full retry-budget state machine lands with the reconnect plan item).
func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := backoff(0); got != time.Second {
		t.Errorf("backoff(0) = %v, want 1s", got)
	}
}
