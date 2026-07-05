package daemon

import "testing"

// TestSummarizerTiering: compaction routes to the tiered summarizer when set,
// otherwise falls back to the main reasoner.
func TestSummarizerTiering(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	d.SetReasoner(&scriptedReasoner{})
	if got := d.summarizeReasoner().Name(); got != "scripted" {
		t.Fatalf("default summarizer should be the main reasoner, got %s", got)
	}
	d.SetSummarizer(taskEchoReasoner{})
	if got := d.summarizeReasoner().Name(); got != "task-echo" {
		t.Fatalf("configured summarizer should be used, got %s", got)
	}
}
