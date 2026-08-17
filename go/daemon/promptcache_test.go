package daemon

import (
	"strings"
	"testing"
)

// TestPromptSegmentationStablePrefix proves the cacheable invariant: for a fixed
// system prompt + task, the stable prefix is byte-identical no matter how the
// transcript grows, and the transcript lives only in the volatile suffix.
func TestPromptSegmentationStablePrefix(t *testing.T) {
	const sys, task, closing = "SYSTEM PROMPT", "do the thing", "GO"

	a := buildPromptSegments(sys, task, "turn1", closing)
	b := buildPromptSegments(sys, task, "turn1\nturn2\nturn3", closing)

	if a.StablePrefix != b.StablePrefix {
		t.Fatal("stable prefix must be identical across turns (the cacheable region)")
	}
	if strings.Contains(a.StablePrefix, "turn1") {
		t.Fatal("the transcript must not appear in the stable prefix")
	}
	if !strings.Contains(a.VolatileSuffix, "turn1") || !strings.Contains(b.VolatileSuffix, "turn3") {
		t.Fatal("the transcript belongs in the volatile suffix")
	}
	if !strings.Contains(a.StablePrefix, sys) || !strings.Contains(a.StablePrefix, task) {
		t.Fatal("the prefix must carry the system prompt and task")
	}

	// full() reconstructs exactly prefix+suffix, and the breakpoint is the prefix
	// boundary.
	if a.full() != a.StablePrefix+a.VolatileSuffix {
		t.Fatal("full must be prefix + suffix")
	}
	if a.CacheBreakpoint() != len(a.StablePrefix) {
		t.Fatalf("cache breakpoint must be the prefix length, got %d", a.CacheBreakpoint())
	}
	if got := strings.Join(a.CacheSections(), ""); !strings.Contains(got, sys) || !strings.Contains(got, task) {
		t.Fatalf("cache sections must cover constitution and task, got %#v", a.CacheSections())
	}
}

func TestPromptSectionsKeepOrderAndOmitEmpty(t *testing.T) {
	seg := buildPromptSegmentsFromLayers(promptLayers{
		Constitution: "CONSTITUTION",
		Workspace:    "WORKSPACE",
		Catalog:      "CATALOG",
	}, "do the thing", "turn1", "GO")
	if !strings.HasPrefix(seg.StablePrefix, "CONSTITUTION\n\nWORKSPACE\n\nCATALOG\n\nTASK: do the thing\n\nTRANSCRIPT:\n") {
		t.Fatalf("prefix order = %q", seg.StablePrefix)
	}
	if strings.Contains(seg.StablePrefix, "turn1") || !strings.Contains(seg.VolatileSuffix, "turn1") {
		t.Fatal("transcript must stay volatile")
	}
	got := seg.CacheSections()
	if len(got) != 3 || got[0] != "CONSTITUTION" || got[1] != "WORKSPACE" || !strings.HasPrefix(got[2], "CATALOG\n\nTASK:") {
		t.Fatalf("cache sections = %#v", got)
	}
	if strings.Join(got, "\n\n") != seg.StablePrefix {
		t.Fatalf("sections must reassemble the stuffed prefix: sections=%q prefix=%q", strings.Join(got, "\n\n"), seg.StablePrefix)
	}

	emptyCatalog := buildPromptSegmentsFromLayers(promptLayers{Constitution: "C", Workspace: "W"}, "task", "", "GO")
	if len(emptyCatalog.CacheSections()) != 3 {
		t.Fatalf("empty catalog still has constitution, workspace, task trailer: %#v", emptyCatalog.CacheSections())
	}

	blob := buildPromptSegments("SYS", "task", "t", "GO")
	if blob.Constitution != "SYS" || blob.Workspace != "" || blob.Catalog != "" {
		t.Fatalf("legacy blob must stay one constitution section: %+v", blob)
	}
	if blob.StablePrefix != "SYS\n\nTASK: task\n\nTRANSCRIPT:\n" {
		t.Fatalf("legacy prefix drifted: %q", blob.StablePrefix)
	}
}
