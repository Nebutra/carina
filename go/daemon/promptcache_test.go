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
	if !strings.Contains(a.StablePrefix, sys) {
		t.Fatal("the prefix must carry the system prompt")
	}
	if strings.Contains(a.StablePrefix, "TASK:") || strings.Contains(a.StablePrefix, task) {
		t.Fatal("TASK must not live in the cacheable prefix")
	}
	if !strings.Contains(a.VolatileSuffix, "TASK: "+task) {
		t.Fatal("TASK belongs in the volatile suffix")
	}

	// full() reconstructs exactly prefix+suffix, and the breakpoint is the prefix
	// boundary.
	if a.full() != a.StablePrefix+a.VolatileSuffix {
		t.Fatal("full must be prefix + suffix")
	}
	if a.CacheBreakpoint() != len(a.StablePrefix) {
		t.Fatalf("cache breakpoint must be the prefix length, got %d", a.CacheBreakpoint())
	}
	if got := strings.Join(a.CacheSections(), ""); !strings.Contains(got, sys) || strings.Contains(got, "TASK:") {
		t.Fatalf("cache sections must cover constitution without TASK, got %#v", a.CacheSections())
	}
}

func TestPromptSectionsKeepOrderAndOmitEmpty(t *testing.T) {
	seg := buildPromptSegmentsFromLayers(promptLayers{
		Constitution: "CONSTITUTION",
		Workspace:    "WORKSPACE",
		Catalog:      "CATALOG",
	}, "do the thing", "turn1", "GO")
	if seg.StablePrefix != "CONSTITUTION\n\nWORKSPACE\n\nCATALOG" {
		t.Fatalf("prefix order = %q", seg.StablePrefix)
	}
	if strings.Contains(seg.StablePrefix, "turn1") || strings.Contains(seg.StablePrefix, "TASK:") {
		t.Fatal("TASK and transcript must stay volatile")
	}
	if !strings.Contains(seg.VolatileSuffix, "TASK: do the thing") || !strings.Contains(seg.VolatileSuffix, "turn1") {
		t.Fatal("TASK and transcript belong in the volatile suffix")
	}
	got := seg.CacheSections()
	if len(got) != 3 || got[0] != "CONSTITUTION" || got[1] != "WORKSPACE" || got[2] != "CATALOG" {
		t.Fatalf("cache sections = %#v", got)
	}
	if strings.Join(got, "\n\n") != seg.StablePrefix {
		t.Fatalf("sections must reassemble the stuffed prefix: sections=%q prefix=%q", strings.Join(got, "\n\n"), seg.StablePrefix)
	}

	emptyCatalog := buildPromptSegmentsFromLayers(promptLayers{Constitution: "C", Workspace: "W"}, "task", "", "GO")
	if len(emptyCatalog.CacheSections()) != 2 {
		t.Fatalf("empty catalog is constitution + workspace only: %#v", emptyCatalog.CacheSections())
	}
	if strings.Contains(strings.Join(emptyCatalog.CacheSections(), "\n"), "TASK:") {
		t.Fatalf("TASK leaked into cache sections: %#v", emptyCatalog.CacheSections())
	}

	blob := buildPromptSegments("SYS", "task", "t", "GO")
	if blob.Constitution != "SYS" || blob.Workspace != "" || blob.Catalog != "" {
		t.Fatalf("legacy blob must stay one constitution section: %+v", blob)
	}
	if blob.StablePrefix != "SYS" {
		t.Fatalf("legacy prefix drifted: %q", blob.StablePrefix)
	}
	if !strings.HasPrefix(strings.TrimLeft(blob.VolatileSuffix, "\n"), "TASK: task\n\nTRANSCRIPT:\n") {
		t.Fatalf("legacy suffix drifted: %q", blob.VolatileSuffix)
	}
}
