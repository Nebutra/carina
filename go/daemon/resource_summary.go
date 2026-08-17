package daemon

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/provider"
)

type processResourceSummary struct {
	RSSBytes         *uint64 `json:"rss_bytes,omitempty"`
	RSSAvailable     bool    `json:"rss_available"`
	RSSSource        string  `json:"rss_source"`
	RSSReason        string  `json:"rss_reason,omitempty"`
	GoHeapAllocBytes uint64  `json:"go_heap_alloc_bytes"`
	GoHeapSysBytes   uint64  `json:"go_heap_sys_bytes"`
}

type sessionResourceSummary struct {
	SessionID       string `json:"session_id"`
	Status          string `json:"status"`
	Tasks           int    `json:"tasks"`
	RunningTasks    int    `json:"running_tasks"`
	CheckpointBytes int    `json:"checkpoint_bytes"`
	Compactions     int    `json:"compactions"`
}

type sessionResourceCollection struct {
	Count    int                      `json:"count"`
	ByStatus map[string]int           `json:"by_status"`
	Items    []sessionResourceSummary `json:"items"`
}

type daemonResourceSummary struct {
	SampledAt string                    `json:"sampled_at"`
	Process   processResourceSummary    `json:"process"`
	Sessions  sessionResourceCollection `json:"sessions"`
	Caches    map[string]any            `json:"caches"`
	Copies    resourceCopies            `json:"copies"`
}

// resourceCopy is one attributable replica of runtime state. These are not a
// process PSS and must not be summed into a fake "Carina memory" KPI.
type resourceCopy struct {
	Bytes     *uint64 `json:"bytes,omitempty"`
	Available bool    `json:"available"`
	Source    string  `json:"source"`
	Reason    string  `json:"reason,omitempty"`
	Scope     string  `json:"scope"`
}

type resourceCopies struct {
	Checkpoint    resourceCopy `json:"checkpoint"`
	Heap          resourceCopy `json:"heap"`
	ProviderCache resourceCopy `json:"provider_cache"`
}

func (d *Daemon) resourceSummary(now time.Time) daemonResourceSummary {
	sessions := d.store.List()
	items := make(map[string]*sessionResourceSummary, len(sessions))
	byStatus := make(map[string]int)
	for _, session := range sessions {
		if session == nil {
			continue
		}
		status := strings.TrimSpace(session.Status)
		if status == "" {
			status = "unknown"
		}
		byStatus[status]++
		items[session.SessionID] = &sessionResourceSummary{
			SessionID: session.SessionID,
			Status:    status,
		}
	}

	for _, task := range d.sched.List() {
		if task == nil {
			continue
		}
		item := items[task.SessionID]
		if item == nil {
			continue
		}
		item.Tasks++
		if task.Status == "running" {
			item.RunningTasks++
		}
		checkpointBytes, checkpoint := d.runs.checkpointResourceStats(task.RunID)
		if checkpoint == nil || checkpoint.Transcript == nil {
			continue
		}
		item.CheckpointBytes += checkpointBytes
		item.Compactions += len(checkpoint.Transcript.CompactionReceipts)
	}

	ordered := make([]sessionResourceSummary, 0, len(items))
	for _, item := range items {
		ordered = append(ordered, *item)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SessionID < ordered[j].SessionID })

	var checkpointTotal uint64
	for _, item := range ordered {
		if item.CheckpointBytes > 0 {
			checkpointTotal += uint64(item.CheckpointBytes)
		}
	}
	process := currentProcessResourceSummary()
	heapBytes := process.GoHeapAllocBytes
	return daemonResourceSummary{
		SampledAt: now.UTC().Format(time.RFC3339Nano),
		Process:   process,
		Sessions: sessionResourceCollection{
			Count:    len(ordered),
			ByStatus: byStatus,
			Items:    ordered,
		},
		Caches: map[string]any{"artifact_store": d.artifacts.Metrics()},
		Copies: resourceCopies{
			Checkpoint: resourceCopy{
				Bytes: &checkpointTotal, Available: true,
				Source: "run_checkpoint_files", Scope: "session",
			},
			Heap: resourceCopy{
				Bytes: &heapBytes, Available: true,
				Source: "go_memstats_heap_alloc", Scope: "process",
				Reason: "Go HeapAlloc; not RSS and not PSS",
			},
			ProviderCache: providerCatalogCacheCopy(),
		},
	}
}

func providerCatalogCacheCopy() resourceCopy {
	out := resourceCopy{Source: "file:~/.carina/cache/models.json", Scope: "host"}
	path, err := provider.DefaultCachePath()
	if err != nil {
		out.Reason = "home directory is unavailable"
		return out
	}
	info, err := os.Stat(path)
	if err != nil {
		out.Reason = "provider catalog cache is absent"
		return out
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		out.Reason = "provider catalog cache is not a regular file"
		return out
	}
	size := uint64(info.Size())
	out.Bytes = &size
	out.Available = true
	return out
}

func currentProcessResourceSummary() processResourceSummary {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	result := processResourceSummary{
		RSSAvailable:     false,
		RSSSource:        "unavailable",
		GoHeapAllocBytes: memory.HeapAlloc,
		GoHeapSysBytes:   memory.HeapSys,
	}
	if runtime.GOOS != "linux" {
		result.RSSReason = "current process RSS is not implemented on " + runtime.GOOS
		return result
	}
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		result.RSSReason = "cannot read /proc/self/statm"
		return result
	}
	rss, err := parseLinuxStatmRSS(raw, uint64(os.Getpagesize()))
	if err != nil {
		result.RSSReason = "invalid /proc/self/statm"
		return result
	}
	result.RSSBytes = &rss
	result.RSSAvailable = true
	result.RSSSource = "linux_proc_statm"
	return result
}

func parseLinuxStatmRSS(raw []byte, pageSize uint64) (uint64, error) {
	fields := strings.Fields(string(raw))
	if len(fields) < 2 || pageSize == 0 {
		return 0, fmt.Errorf("statm requires resident pages and a page size")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("resident pages: %w", err)
	}
	if pages > ^uint64(0)/pageSize {
		return 0, fmt.Errorf("resident byte count overflows uint64")
	}
	return pages * pageSize, nil
}
