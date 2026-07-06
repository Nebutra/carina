package daemon

import "fmt"

// promptSegments splits an agent prompt into a stable prefix (system prompt +
// task — byte-identical across every turn of a run) and a volatile suffix (the
// growing transcript + closing instruction). A cache-aware provider can cache
// the prefix once and only re-encode the suffix each turn, cutting repeated
// prefill of the large, unchanging preamble.
type promptSegments struct {
	StablePrefix   string
	VolatileSuffix string
}

// buildPromptSegments assembles the segments. closing is the trailing
// instruction (it differs slightly between the main agent and subagents).
func buildPromptSegments(sysPrompt, userPrompt, transcript, closing string) promptSegments {
	return promptSegments{
		StablePrefix:   fmt.Sprintf("%s\n\nTASK: %s\n\nTRANSCRIPT:\n", sysPrompt, userPrompt),
		VolatileSuffix: transcript + "\n" + closing,
	}
}

// full is the complete prompt (prefix + suffix) — what the loop sends.
func (s promptSegments) full() string { return s.StablePrefix + s.VolatileSuffix }

// CacheBreakpoint is the byte offset where the cacheable prefix ends.
func (s promptSegments) CacheBreakpoint() int { return len(s.StablePrefix) }
