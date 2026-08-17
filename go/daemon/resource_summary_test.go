package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLinuxStatmRSS(t *testing.T) {
	rss, err := parseLinuxStatmRSS([]byte("100 25 10 1 0 5 0\n"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rss != 25*4096 {
		t.Fatalf("rss = %d, want %d", rss, 25*4096)
	}
	for _, raw := range []string{"", "100", "100 nope", "100 2"} {
		pageSize := uint64(4096)
		if raw == "100 2" {
			pageSize = 0
		}
		if _, err := parseLinuxStatmRSS([]byte(raw), pageSize); err == nil {
			t.Fatalf("parseLinuxStatmRSS(%q, %d) succeeded", raw, pageSize)
		}
	}
	if _, err := parseLinuxStatmRSS([]byte("1 18446744073709551615"), 4096); err == nil {
		t.Fatal("overflowing resident byte count succeeded")
	}
}

func TestResourceSummaryAttributesSessionCountersAndCompactions(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	first, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.store.CreateSession(t.TempDir(), "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.SetStatus(second.SessionID, "paused"); err != nil {
		t.Fatal(err)
	}

	running := d.sched.Submit(first.SessionID, first.WorkspaceID, "observe resources")
	d.sched.SetStatus(running.RunID, "running")
	completed := d.sched.Submit(first.SessionID, first.WorkspaceID, "compact context")
	d.sched.SetStatus(completed.RunID, "completed")
	d.runs.saveCheckpoint(completed.RunID, &runCheckpoint{
		Turn: 3,
		Transcript: &Transcript{
			Task:               "compact context",
			CompactionReceipts: []CompactionReceipt{{Version: 2}, {Version: 2}},
		},
	})

	sampledAt := time.Date(2026, 8, 3, 4, 5, 6, 7, time.UTC)
	summary := d.resourceSummary(sampledAt)
	if summary.SampledAt != sampledAt.Format(time.RFC3339Nano) {
		t.Fatalf("sampled_at = %q", summary.SampledAt)
	}
	if summary.Sessions.Count != 2 || summary.Sessions.ByStatus["active"] != 1 || summary.Sessions.ByStatus["paused"] != 1 {
		t.Fatalf("unexpected session counts: %+v", summary.Sessions)
	}
	if len(summary.Sessions.Items) != 2 || summary.Sessions.Items[0].SessionID > summary.Sessions.Items[1].SessionID {
		t.Fatalf("session items are not deterministically sorted: %+v", summary.Sessions.Items)
	}
	var got *sessionResourceSummary
	for i := range summary.Sessions.Items {
		if summary.Sessions.Items[i].SessionID == first.SessionID {
			got = &summary.Sessions.Items[i]
		}
	}
	if got == nil || got.Tasks != 2 || got.RunningTasks != 1 || got.Compactions != 2 || got.CheckpointBytes <= 0 {
		t.Fatalf("first session resource attribution = %+v", got)
	}
	info, err := os.Stat(filepath.Join(d.runs.dir, completed.RunID+".ckpt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckpointBytes != int(info.Size()) {
		t.Fatalf("checkpoint bytes = %d, want on-disk size %d", got.CheckpointBytes, info.Size())
	}
	if _, ok := summary.Caches["artifact_store"]; !ok {
		t.Fatalf("artifact cache metrics missing: %+v", summary.Caches)
	}
	if !summary.Copies.Checkpoint.Available || summary.Copies.Checkpoint.Bytes == nil || *summary.Copies.Checkpoint.Bytes != uint64(got.CheckpointBytes) {
		t.Fatalf("checkpoint copy = %+v want %d", summary.Copies.Checkpoint, got.CheckpointBytes)
	}
	if summary.Copies.Checkpoint.Scope != "session" || summary.Copies.Heap.Scope != "process" || summary.Copies.ProviderCache.Scope != "host" {
		t.Fatalf("copy scopes = %+v", summary.Copies)
	}
	if !summary.Copies.Heap.Available || summary.Copies.Heap.Bytes == nil || *summary.Copies.Heap.Bytes == 0 {
		t.Fatalf("heap copy must be the live Go heap, got %+v", summary.Copies.Heap)
	}
	if strings.Contains(strings.ToLower(summary.Copies.Heap.Reason), "pss") && !strings.Contains(strings.ToLower(summary.Copies.Heap.Reason), "not pss") {
		t.Fatalf("heap copy must not claim PSS: %+v", summary.Copies.Heap)
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), `"pss"`) || strings.Contains(strings.ToLower(string(raw)), "pss_bytes") {
		t.Fatalf("resource summary must not claim PSS: %s", raw)
	}
}

func TestProviderCatalogCacheCopyAttributesFileWithoutInventingPSS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	absent := providerCatalogCacheCopy()
	if absent.Available || absent.Scope != "host" {
		t.Fatalf("absent cache must stay explicit: %+v", absent)
	}

	path := filepath.Join(home, ".carina", "cache", "models.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"openai":{"id":"openai"}}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	got := providerCatalogCacheCopy()
	if !got.Available || got.Bytes == nil || *got.Bytes != uint64(len(payload)) {
		t.Fatalf("present cache = %+v want %d bytes", got, len(payload))
	}
	if strings.Contains(strings.ToLower(got.Source), "pss") {
		t.Fatalf("provider cache must not pretend to be PSS: %+v", got)
	}
}

func TestDoctorAndMetricsExposeResourceSummary(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	doctorAny, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	doctor := doctorAny.(map[string]any)
	if _, ok := doctor["resources"].(daemonResourceSummary); !ok {
		t.Fatalf("doctor resources missing or untyped: %T", doctor["resources"])
	}
	metricsAny, err := d.handleMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics := metricsAny.(map[string]any)
	if _, ok := metrics["resources"].(daemonResourceSummary); !ok {
		t.Fatalf("metrics resources missing or untyped: %T", metrics["resources"])
	}
}
