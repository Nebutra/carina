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
}
