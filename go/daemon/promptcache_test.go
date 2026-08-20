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

	withRequested := buildPromptSegmentsFromLayers(promptLayers{
		Constitution: "C", Catalog: "CATALOG", Requested: "REQUESTED SKILLS\n- skill://pdf (explicit)",
	}, "Use $pdf", "turn1", "GO")
	if strings.Contains(withRequested.StablePrefix, "REQUESTED SKILLS") {
		t.Fatal("REQUESTED skills must not live in the stable prefix")
	}
	if !strings.Contains(withRequested.VolatileSuffix, "REQUESTED SKILLS") || !strings.Contains(withRequested.VolatileSuffix, "TASK: Use $pdf") {
		t.Fatalf("REQUESTED skills belong in the suffix before TASK: %q", withRequested.VolatileSuffix)
	}
	for _, section := range withRequested.CacheSections() {
		if strings.Contains(section, "REQUESTED SKILLS") || strings.Contains(section, "TASK:") {
			t.Fatalf("CacheSections leaked volatile text: %#v", withRequested.CacheSections())
		}
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

func TestConstitutionSectionsAreNamedAndOrdered(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	layers := d.composeAgentPromptLayers(sess, task, "")
	if !strings.HasPrefix(layers.Mode, "converse:") {
		t.Fatalf("mode B must be first operative: %q", truncate(layers.Mode, 80))
	}
	if !strings.Contains(layers.Identity, "You are Carina") {
		t.Fatalf("identity A missing: %q", truncate(layers.Identity, 80))
	}
	if !strings.HasPrefix(layers.Protocol, "Harness protocol:") {
		t.Fatalf("protocol C missing: %q", truncate(layers.Protocol, 80))
	}
	if !strings.HasPrefix(layers.Tools, "Available tools:") {
		t.Fatalf("tools D missing: %q", truncate(layers.Tools, 80))
	}
	if strings.Contains(layers.Protocol, "Available tools:") {
		t.Fatal("protocol C must not carry the tool catalog")
	}
	if strings.Contains(layers.Identity, "Harness protocol:") {
		t.Fatal("identity A must not carry protocol")
	}
	if strings.Contains(layers.Tools, "Harness protocol:") {
		t.Fatal("tools D must not carry protocol C")
	}

	seg := buildPromptSegmentsFromLayers(layers, task.UserPrompt, "turn1", "GO")
	got := seg.CacheSections()
	if len(got) < 4 {
		t.Fatalf("want A–D cache sections, got %#v", got)
	}
	if !strings.HasPrefix(got[0], "converse:") || !strings.Contains(got[0], "Intent:") {
		t.Fatalf("section 0 must be Mode+Intent: %q", truncate(got[0], 160))
	}
	if !strings.Contains(got[1], "You are Carina") {
		t.Fatalf("section 1 must be Identity: %q", truncate(got[1], 80))
	}
	if !strings.HasPrefix(got[2], "Harness protocol:") {
		t.Fatalf("section 2 must be Protocol: %q", truncate(got[2], 80))
	}
	if !strings.HasPrefix(got[3], "Available tools:") {
		t.Fatalf("section 3 must be Tools: %q", truncate(got[3], 80))
	}
	if strings.Join(got, "\n\n") != seg.StablePrefix {
		t.Fatalf("named sections must reassemble the stuffed prefix")
	}
	if strings.Contains(seg.StablePrefix, "TASK:") {
		t.Fatal("TASK leaked into cache prefix")
	}
	if promptCacheKindFor(nil, nil, "grok-build/grok-4.6") != "none" {
		t.Fatal("Grok must stay cache kind none")
	}

	native := layers.withToolContract(nativeToolsContract)
	if native.Tools != "" {
		t.Fatal("native contract must not re-paste the JSON catalog")
	}
	if native.Protocol != nativeToolsContract {
		t.Fatalf("native contract should replace protocol C, got %q", truncate(native.Protocol, 80))
	}
	if strings.Contains(native.Constitution, toolsCatalog) {
		t.Fatal("native constitution must not re-paste tools catalog")
	}
	if strings.Contains(native.Constitution, toolsHelp) || strings.Contains(native.Protocol, toolsHelp) {
		t.Fatal("withToolContract must not re-paste toolsHelp")
	}
}

func TestNativeToolContractIsEnvelopeOnly(t *testing.T) {
	if len(nativeToolsContract) > 200 {
		t.Fatalf("native protocol = %d B, want ≤ 200", len(nativeToolsContract))
	}
	if strings.Contains(nativeToolsContract, `{"tool":`) || strings.Contains(nativeToolsContract, "Available tools:") {
		t.Fatalf("native protocol must not carry the JSON catalog:\n%s", nativeToolsContract)
	}
	if strings.Contains(nativeToolsContract, toolsHelp) || strings.Contains(nativeToolsContract, toolsCatalog) {
		t.Fatal("native protocol must not embed toolsHelp")
	}

	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	named := d.composeAgentPromptLayers(sess, task, "").withToolContract(nativeToolsContract)
	if named.Tools != "" || strings.Contains(named.Constitution, toolsCatalog) || strings.Contains(named.Constitution, toolsHelp) {
		t.Fatal("named native layers re-pasted the JSON catalog")
	}
	if !strings.Contains(named.Constitution, nativeToolsContract) {
		t.Fatal("named native layers dropped the envelope")
	}
	if strings.Contains(named.Constitution, "Reply with ONLY the JSON object") {
		t.Fatal("native envelope must not keep the JSON ReAct protocol")
	}

	legacy := promptLayers{Constitution: "SUBAGENT\n\n" + toolsHelp}.withToolContract(nativeToolsContract)
	if strings.Contains(legacy.Constitution, toolsHelp) || strings.Contains(legacy.Constitution, toolsCatalog) || strings.Contains(legacy.Constitution, `{"tool":`) {
		t.Fatalf("legacy withToolContract re-pasted the JSON catalog:\n%s", legacy.Constitution)
	}
	if !strings.Contains(legacy.Constitution, "SUBAGENT") || !strings.Contains(legacy.Constitution, nativeToolsContract) {
		t.Fatalf("legacy native constitution drifted: %q", legacy.Constitution)
	}

	unordered := promptLayers{Constitution: coreConstitution()}.withToolContract(nativeToolsContract)
	if strings.Contains(unordered.Constitution, toolsCatalog) || strings.Contains(unordered.Constitution, harnessProtocol) {
		t.Fatalf("unordered blob kept JSON catalog/protocol:\n%s", unordered.Constitution)
	}
	if !strings.Contains(unordered.Constitution, nativeToolsContract) {
		t.Fatal("unordered blob dropped the native envelope")
	}
}
