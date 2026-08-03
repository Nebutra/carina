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
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "pss") {
		t.Fatalf("resource summary must not claim PSS: %s", raw)
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
