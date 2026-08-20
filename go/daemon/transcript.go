package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/artifact"
)

const (
	observationSnipInlineEnv    = "CARINA_INLINE_TOOL_OUTPUT"
	pinnedObservationMaxChars   = 1 << 20
	observationSnipPointerSlack = 220
	maxRebuildFiles             = 5
	maxRebuildFileBytes         = 20_000 // 5k tok ceiling per file
	maxRebuildTotalBytes        = 8000   // do not refill the window compact just freed
)

// The agent's view of history is a *bounded projection* of the append-only
// event log: the audit chain always keeps everything, while what we feed the
// model is compacted (elided/summarized) to stay within budget. This is the
// key idea from the loop research — context is a finite resource, so the
// model view must be managed while the audit trail stays complete.

// Observation is one tool result in the transcript. Content can be replaced
// by an elision placeholder or dropped into a summary; the original always
// remains in the event log.
type Observation struct {
	Tool              string   `json:"tool,omitempty"`
	Content           string   `json:"content"`
	Pinned            bool     `json:"pinned,omitempty"` // failing tests / current edit / patch result — never elided
	Elided            bool     `json:"elided,omitempty"`
	OriginalRef       string   `json:"original_ref,omitempty"`
	OriginalSHA256    string   `json:"original_sha256,omitempty"`
	CompressionEngine string   `json:"compression_engine,omitempty"`
	OriginalBytes     int      `json:"original_bytes,omitempty"`
	CompressedBytes   int      `json:"compressed_bytes,omitempty"`
	OriginalTokens    int      `json:"original_tokens,omitempty"`
	CompressedTokens  int      `json:"compressed_tokens,omitempty"`
	SavingsPercent    float64  `json:"savings_percent,omitempty"`
	Transforms        []string `json:"transforms,omitempty"`
	// MediaRefs are content-addressed references (see media.go) to non-text
	// media produced by this observation. Only the placeholder line ever
	// reaches the model view (see render); raw bytes stay in the artifact
	// store. omitempty keeps media-free turns byte-identical in checkpoints
	// and compaction-receipt preimages to before this field existed.
	MediaRefs []MediaRef `json:"media_refs,omitempty"`
}

// Turn is one model decision + its observation.
type Turn struct {
	Index       int
	Thought     string
	Tool        string
	ActionBrief string // e.g. `read greet.py` / `run [go test]`
	Path        string // set for read-family tools; drives supersedeStaleReads
	Obs         Observation
}

// Transcript is the model-facing conversation state.
type Transcript struct {
	Task               string
	Summary            string // rolling summary of compacted-away head turns
	Rebuild            string `json:"rebuild,omitempty"` // post-compact cited-file rehydrate; volatile; not prefix
	Turns              []Turn
	CompactionReceipts []CompactionReceipt      `json:"compaction_receipts,omitempty"`
	CompactionBudget   CompactionBudgetSnapshot `json:"compaction_budget,omitempty"`
	SummarizerFailures int                      `json:"summarizer_failures,omitempty"`
	policy             CompactionPolicy
	artifacts          *artifact.Store
	artifactScope      artifact.Scope
	// observedInputTokens is the last provider-reported prompt size for this
	// view. Zero means pressure falls back to chars/4. Not checkpointed:
	// after resume, usage is absent until the next provider response.
	observedInputTokens int
}

func (t *Transcript) bindArtifacts(store *artifact.Store, scope artifact.Scope) {
	if t == nil {
		return
	}
	t.artifacts = store
	t.artifactScope = scope
}

type CompactionBudgetSnapshot struct {
	PolicyVersion  string `json:"policy_version,omitempty"`
	WindowTokens   int    `json:"window_tokens,omitempty"`
	ReserveTokens  int    `json:"reserve_tokens,omitempty"`
	TriggerTokens  int    `json:"trigger_tokens,omitempty"`
	MetadataSource string `json:"metadata_source,omitempty"`
}

type CompactionMode string

const (
	compactionModeCollapseOnly CompactionMode = "collapse_only"
	compactionModeSummary      CompactionMode = "summary"

	defaultCollapseOnlyMaxPressure = 1.10
	maxCollapsedPriorSummaryChars  = 2000
	maxCollapsedActionBriefs       = 20
	maxCollapsedActionBriefChars   = 160
)

// CompactionReceipt is the auditable record of one Step-2 summarize fold.
// Semantics are versioned:
//
//   - Version 1 (historical; still valid for old checkpoints and audit
//     entries): the whole head [FirstTurn..LastTurn] was folded into the
//     rolling summary, and PreimageSHA256 covers previous-summary + the
//     entire pre-compaction head.
//   - Version 2: the head is partitioned into user-authored turns
//     (kept verbatim in the transcript — see compact()) and everything else
//     (folded). PreimageSHA256 covers previous-summary + the FOLDED turns
//     only; FirstTurn/LastTurn/RemovedTurns likewise describe the folded set.
//     KeptTurnIndices records which head turns were partitioned out,
//     KeptSHA256 hashes the kept turns exactly as retained (post
//     verbatim-budget truncation/elision), and KeyFiles is the deterministic
//     top-K most-edited files among the folded turns — the substrate a later
//     content-reinjection tier consumes.
//   - Version 3: the same preimage and verbatim-user semantics as v2, but the
//     folded head is represented by a deterministic local action skeleton
//     instead of a model-written summary. Mode and Transforms disclose which
//     path produced every new receipt; their absence remains valid for old
//     v1/v2 checkpoints. SummarizerFailures is the consecutive empty/error
//     count after this fold (0 after a successful model summary).
type CompactionReceipt struct {
	Version            int            `json:"version"`
	CreatedAt          time.Time      `json:"created_at"`
	FirstTurn          int            `json:"first_turn"`
	LastTurn           int            `json:"last_turn"`
	RemovedTurns       int            `json:"removed_turns"`
	PreimageSHA256     string         `json:"preimage_sha256"`
	SummarySHA256      string         `json:"summary_sha256"`
	KeptTurnIndices    []int          `json:"kept_turn_indices,omitempty"`
	KeptSHA256         string         `json:"kept_sha256,omitempty"`
	KeyFiles           []string       `json:"key_files,omitempty"`
	CitedFiles         []string       `json:"cited_files,omitempty"`
	PolicyVersion      string         `json:"policy_version,omitempty"`
	WindowTokens       int            `json:"window_tokens,omitempty"`
	ReserveTokens      int            `json:"reserve_tokens,omitempty"`
	MetadataSource     string         `json:"metadata_source,omitempty"`
	PressureBefore     float64        `json:"pressure_before,omitempty"`
	PressureAfter      float64        `json:"pressure_after,omitempty"`
	CharsBefore        int            `json:"chars_before,omitempty"`
	CharsAfter         int            `json:"chars_after,omitempty"`
	TokensBefore       int            `json:"tokens_before,omitempty"`
	TokensAfter        int            `json:"tokens_after,omitempty"`
	Mode               CompactionMode `json:"mode,omitempty"`
	Transforms         []string       `json:"transforms,omitempty"`
	SummarizerFailures int            `json:"summarizer_failures,omitempty"`
}

// CompactionPolicy bounds the model view. Provider-neutral preflight
// tokenization is not available cheaply, so the trigger uses characters.
type CompactionPolicy struct {
	MaxChars       int // total transcript char budget before compaction
	KeepRecent     int // keep this many most-recent turns verbatim
	ToolOutputMax  int // truncate any single observation to this many chars
	SummarizeAfter int // if still over budget after eliding, summarize the head

	// ReserveChars and ThresholdRatio are an optional dual bound on the
	// effective trigger (see triggerChars): if both are zero (the default)
	// the trigger is exactly MaxChars, matching prior behavior byte for byte.
	ReserveChars   int     // if >0, floor the trigger at MaxChars-ReserveChars
	ThresholdRatio float64 // if >0, also allow the trigger up to MaxChars*ThresholdRatio

	// VerbatimUserMaxChars bounds the total verbatim content of user-authored
	// turns (Tool=="user": steering drains, fork-task notices) that compact()'s
	// Step-2 partition keeps out of the summarize fold. The budget is spent
	// newest-to-oldest: the newest kept turns stay verbatim, the first turn to
	// overflow is truncated (artifact.Preview, oldest-content-first shape), and
	// anything older is elided with the same Elided/OriginalSHA256 fields
	// Step-1 elision uses — so render() and the audit trail treat them
	// identically, and growth stays bounded across repeated compactions (kept
	// turns re-enter later partitions under the same cap). Zero (the zero
	// value) disables the cap, matching MaxTokens' zero-disables convention;
	// defaultCompactionPolicy sets 4000.
	VerbatimUserMaxChars int

	// MaxTokens is an optional token-estimate co-trigger (see shouldCompact):
	// if zero (the default) it never fires, so behavior is byte-identical to
	// before this field existed. When set, compaction fires once either the
	// char-based trigger (triggerChars) OR the estimated token count crosses
	// MaxTokens — whichever comes first. Carina has no cheap exact token
	// count (see the CompactionPolicy doc comment above), so this reuses
	// agent.go's existing estimateTokens() approximation rather than adding a
	// second estimator.
	MaxTokens      int
	PolicyVersion  string
	WindowTokens   int
	ReserveTokens  int
	MetadataSource string

	// CollapseOnlyMaxPressure selects deterministic local collapse while the
	// transcript is only modestly over its effective trigger. Zero uses the
	// default 1.10 ratio; a negative value disables the local tier.
	CollapseOnlyMaxPressure float64
}

func defaultCompactionPolicy() CompactionPolicy {
	return CompactionPolicy{
		MaxChars:             24000,
		KeepRecent:           3,
		ToolOutputMax:        2000,
		SummarizeAfter:       6,
		VerbatimUserMaxChars: 4000,
	}
}

func newTranscript(task string) *Transcript {
	return &Transcript{Task: task, policy: defaultCompactionPolicy()}
}

func observationSnipInline() bool {
	value := strings.TrimSpace(os.Getenv(observationSnipInlineEnv))
	return value == "1" || strings.EqualFold(value, "on") || strings.EqualFold(value, "true")
}

func snipObservation(obs Observation, policy CompactionPolicy, store *artifact.Store, scope artifact.Scope) Observation {
	if strings.TrimSpace(obs.Content) == "" {
		return obs
	}
	if obs.Pinned {
		if len(obs.Content) > pinnedObservationMaxChars {
			sum := sha256Hex(obs.Content)
			obs.OriginalSHA256 = sum
			obs.OriginalBytes = len(obs.Content)
			obs.Transforms = append(obs.Transforms, "snip_pinned_fail_closed")
			obs.Content = fmt.Sprintf(
				"error: pinned observation exceeds %d bytes; re-read the source. sha256=%s bytes=%d",
				pinnedObservationMaxChars, sum, len(obs.Content),
			)
		}
		return obs
	}
	max := policy.ToolOutputMax
	if max <= 0 || len(obs.Content) <= max {
		return obs
	}
	raw := []byte(obs.Content)
	obs.OriginalSHA256 = sha256Hex(obs.Content)
	obs.OriginalBytes = len(obs.Content)
	if store != nil && strings.TrimSpace(scope.SessionID) != "" {
		meta, err := store.Put(raw, artifact.PutOptions{
			Scope:        scope,
			MediaType:    "text/plain; charset=utf-8",
			Retention:    artifact.RetentionNormal,
			PreviewBytes: max,
		})
		if err == nil {
			obs.OriginalRef = "artifact:" + meta.ID
		}
	}
	previewBudget := max
	pointer := snipPointerLine(obs)
	if previewBudget > len(pointer)+32 {
		previewBudget -= len(pointer)
	}
	preview, truncated, valid := artifact.Preview(raw, previewBudget, 0)
	if !valid {
		return obs
	}
	if truncated {
		obs.Content = strings.TrimRight(preview, "\n") + "\n" + pointer
		obs.Transforms = append(obs.Transforms, "snip_on_enqueue")
		obs.CompressedBytes = len(obs.Content)
	}
	return obs
}

func snipPointerLine(obs Observation) string {
	ref := obs.OriginalRef
	if ref == "" {
		ref = "-"
	}
	return fmt.Sprintf(
		"[artifact ref=%s sha256=%s bytes=%d — read this artifact or the original path to recover]\n",
		ref, obs.OriginalSHA256, obs.OriginalBytes,
	)
}

// addTurn records a completed turn, truncating oversized observations up front.
// A new turn carrying a Path (a read-family tool) first supersedes any
// earlier, still-verbatim turn of the identical path: the earlier read is now
// stale (this turn proves the model has the current content), so keeping both
// copies verbatim in the model view only burns budget for no benefit — see
// supersedeStaleReads.
func (t *Transcript) addTurn(turn Turn) {
	if !observationSnipInline() {
		turn.Obs = snipObservation(turn.Obs, t.policy, t.artifacts, t.artifactScope)
	}
	if turn.Path != "" {
		t.supersedeStaleReads(turn.Path)
	}
	turn.Index = len(t.Turns) + 1
	t.Turns = append(t.Turns, turn)
}

// supersedeStaleReads elides every earlier, non-pinned, not-yet-elided turn
// whose Path matches path: a fresh read of the same path makes those earlier
// copies stale re-reads, redundant with the turn about to be appended. This
// is path-keyed elision, the narrow counterpart to compact()'s age-based
// elision — both use the same Observation.Elided/OriginalSHA256 fields, so
// render() and the audit trail treat them identically regardless of which
// gate elided the turn. Pinned observations (e.g. a read pinned as part of a
// current investigation) are never touched, matching compact()'s contract.
// The audit log (recorded at read time via FileRead events) is untouched —
// this only narrows the model-facing projection.
func (t *Transcript) supersedeStaleReads(path string) int {
	elided := 0
	for i := range t.Turns {
		turn := &t.Turns[i]
		if turn.Path != path || turn.Obs.Pinned || turn.Obs.Elided {
			continue
		}
		turn.Obs.OriginalSHA256 = sha256Hex(turn.Obs.Content)
		turn.Obs.Elided = true
		elided++
	}
	return elided
}

// render projects the transcript into the prompt body the model sees.
func (t *Transcript) render() string {
	if t == nil {
		return ""
	}
	return renderTranscriptRebuild(t.Summary, t.Rebuild, t.Turns)
}

func renderTranscript(summary string, turns []Turn) string {
	return renderTranscriptRebuild(summary, "", turns)
}

func renderTranscriptRebuild(summary, rebuild string, turns []Turn) string {
	var b strings.Builder
	if summary != "" {
		fmt.Fprintf(&b, "SUMMARY OF EARLIER WORK:\n%s\n\n", summary)
	}
	if rebuild != "" {
		fmt.Fprintf(&b, "%s\n\n", rebuild)
	}
	for _, turn := range turns {
		obs := turn.Obs.Content
		if turn.Obs.Elided {
			// Elision covers the whole observation, media placeholders
			// included — "[elided to save context]" already accounts for them,
			// exactly as it does for Content.
			obs = "[elided to save context]"
		} else {
			for _, ref := range turn.Obs.MediaRefs {
				obs += "\n" + ref.placeholder()
			}
		}
		fmt.Fprintf(&b, "turn %d: %s\nobservation: %s\n\n", turn.Index, turn.ActionBrief, obs)
	}
	return b.String()
}

func (t *Transcript) projectedPressure(summary string, turns []Turn) float64 {
	view := renderTranscript(summary, turns)
	if t.policy.MaxTokens > 0 {
		return float64(estimateTokens(view)) / float64(t.policy.MaxTokens)
	}
	if trigger := t.triggerChars(); trigger > 0 {
		return float64(len(view)) / float64(trigger)
	}
	return 0
}

// size is the current rendered char count.
func (t *Transcript) size() int { return len(t.render()) }

// triggerChars is the single effective char-budget threshold used by BOTH
// compaction gates in compact() below. Before this, each gate compared
// against t.policy.MaxChars independently — harmless while both stayed
// literally identical, but a latent bug: a future change to one gate's
// threshold (e.g. an incremental token/ratio-based trigger, as scoped in
// absorption-plan.md's Wave 2 "multi-tier compaction" item) could silently
// leave the other gate on stale semantics, so elision would fire at a
// different effective budget than the summarize-decision gate expects,
// undermining the audit-completeness guarantee compaction receipts exist to
// provide. Routing both gates through one function makes that class of bug
// structurally impossible.
//
// The formula mirrors a token-budget technique (trigger = max(budget -
// reserve, budget * ratio)) adapted to carina's char-based policy: with the
// default ReserveChars=0/ThresholdRatio=0 it reduces to exactly MaxChars
// (today's behavior, unchanged). Configuring both lets a large MaxChars keep
// a small fixed reserve instead of wasting a large proportional chunk, while
// a small MaxChars still gets the more generous ratio-based bound.
func (t *Transcript) triggerChars() int {
	trigger := t.policy.MaxChars - t.policy.ReserveChars
	if ratioBound := int(float64(t.policy.MaxChars) * t.policy.ThresholdRatio); ratioBound > trigger {
		trigger = ratioBound
	}
	if trigger < 0 {
		trigger = 0
	}
	return trigger
}

// shouldCompact is the single combiner both of compact()'s gates below call:
// compaction is due once EITHER the char-based trigger (triggerChars, see
// above) OR the token trigger (MaxTokens) fires. MaxTokens=0 (the default)
// makes the token side of the OR permanently false, so shouldCompact()
// reduces to the plain t.size() > triggerChars() check that predates this
// field — byte-identical to prior behavior for every existing caller/policy
// when no provider usage has been noted.
//
// The token side prefers the last provider-reported input count
// (noteObservedInputTokens). chars/4 is the fallback when usage is absent.
func (t *Transcript) shouldCompact() bool {
	if t.size() > t.triggerChars() {
		return true
	}
	if t.policy.MaxTokens > 0 && t.viewTokens() > t.policy.MaxTokens {
		return true
	}
	return false
}

func (t *Transcript) compactionPressure() float64 {
	if t.policy.MaxTokens > 0 {
		return float64(t.viewTokens()) / float64(t.policy.MaxTokens)
	}
	if trigger := t.triggerChars(); trigger > 0 {
		return float64(t.size()) / float64(trigger)
	}
	return 0
}

func (t *Transcript) noteObservedInputTokens(n int) {
	if t == nil || n <= 0 {
		return
	}
	t.observedInputTokens = n
}

func (t *Transcript) viewTokens() int {
	if t != nil && t.observedInputTokens > 0 {
		return t.observedInputTokens
	}
	if t == nil {
		return 0
	}
	return estimateTokens(t.render())
}

func (t *Transcript) compactionMode(pressure float64) CompactionMode {
	maxPressure := t.policy.CollapseOnlyMaxPressure
	if maxPressure == 0 {
		maxPressure = defaultCollapseOnlyMaxPressure
	}
	if maxPressure > 0 && pressure <= maxPressure {
		return compactionModeCollapseOnly
	}
	return compactionModeSummary
}

func (t *Transcript) summarizerCircuitOpen() bool {
	return t != nil && t.SummarizerFailures >= compactionFailureThreshold
}

func (t *Transcript) noteSummarizerSuccess() {
	if t != nil {
		t.SummarizerFailures = 0
	}
}

func (t *Transcript) noteSummarizerFailure() {
	if t != nil {
		t.SummarizerFailures++
	}
}

// invokeSummarizer runs the model summary once. A nil summarizer or an
// already-open breaker is not an attempt. Empty text and errors both count
// as a consecutive failure so we never write an empty Summary over a
// non-empty head.
func (t *Transcript) invokeSummarizer(summarize func(string) (string, error), head string) (string, bool) {
	if t == nil || t.summarizerCircuitOpen() || summarize == nil {
		return "", false
	}
	text, err := summarize(head)
	if err != nil || strings.TrimSpace(text) == "" {
		t.noteSummarizerFailure()
		return "", false
	}
	t.noteSummarizerSuccess()
	return text, true
}

func contextCompactedPayload(receipt *CompactionReceipt, extra map[string]any) map[string]any {
	payload := map[string]any{}
	for k, v := range extra {
		payload[k] = v
	}
	if receipt == nil {
		return payload
	}
	payload["receipt"] = receipt
	if receipt.SummarizerFailures >= compactionFailureThreshold {
		payload["summarizer_circuit"] = "open"
		if _, exists := payload["status"]; !exists {
			payload["status"] = "summarizer_circuit_open"
		}
	}
	return payload
}

// compact enforces the char budget as a cheap-first cascade. Step 1: elide
// old, non-pinned observations (keeping the most recent KeepRecent turns
// verbatim). Step 2: if still over budget, partition the head (all but the
// recent tail) into user-authored turns (Tool=="user": steering drains,
// fork-task notices — kept verbatim, bounded by VerbatimUserMaxChars) and
// everything else. Fold only the latter into the rolling Summary: first a
// local action skeleton when post-elision pressure is modest; if that
// projection is still over budget, escalate to the provided summarizer in
// the same call. Three consecutive empty or failed summaries open a
// circuit: later calls stay on collapse_only and do not invoke the
// summarizer. A later successful summary resets the counter. User turns
// are preserved structurally rather than trusted to survive a model-written
// summary: a compaction that folds "don't use X" into prose loses the
// correction and the model cannot know what it forgot. The audit log is
// untouched. MiniLM / semantic compact stays off.
func (t *Transcript) compact(summarize func(head string) (string, error)) *CompactionReceipt {
	if !t.shouldCompact() {
		return nil
	}
	preCompactionSummary := t.Summary
	preCompactionTurns := append([]Turn(nil), t.Turns...)
	preRender := t.render()
	charsBefore := len(preRender)
	tokensBefore := t.viewTokens()
	pressureBefore := t.compactionPressure()
	// The view is about to change; stale provider input tokens must not
	// force a summary after cheap elision. Next Think will note usage again.
	t.observedInputTokens = 0
	// Step 1: elide.
	cutoff := len(t.Turns) - t.policy.KeepRecent
	for i := 0; i < cutoff; i++ {
		if !t.Turns[i].Obs.Pinned {
			t.Turns[i].Obs.Elided = true
		}
	}
	if !t.shouldCompact() || len(t.Turns) <= t.policy.SummarizeAfter {
		return nil
	}
	// Step 2: summarize the head (all but the recent tail) into Summary.
	tail := t.policy.KeepRecent
	headEnd := len(t.Turns) - tail
	if headEnd <= 0 {
		return nil
	}
	// Partition the head. kept turns retain their original ascending Index
	// values (indices are already non-contiguous post-compaction, so no
	// reorder is needed). foldedPre carries the pre-Step-1 copies of the
	// folded turns so the receipt preimage covers pre-compaction state,
	// exactly as v1 did for the whole head.
	var kept, folded, foldedPre []Turn
	var keptIdx []int
	for i, turn := range t.Turns[:headEnd] {
		if turn.Tool == "user" {
			kept = append(kept, turn)
			keptIdx = append(keptIdx, turn.Index)
		} else {
			folded = append(folded, turn)
			foldedPre = append(foldedPre, preCompactionTurns[i])
		}
	}
	if len(folded) == 0 {
		// Nothing to fold — the head is entirely user turns and Step-1
		// elision already ran. Fail closed: no summarizer call, no receipt.
		return nil
	}
	// Select after the cheap Step-1 elision. PressureBefore remains the honest
	// pre-compaction measurement in the receipt, while the tier decision asks
	// whether local transforms left only a modest overshoot. Selecting before
	// elision would route large-but-easily-elided observations straight to the
	// model and defeat the purpose of the local tier.
	mode := t.compactionMode(t.compactionPressure())
	var head strings.Builder
	if t.Summary != "" {
		fmt.Fprintf(&head, "%s\n", t.Summary)
	}
	for _, turn := range folded {
		fmt.Fprintf(&head, "turn %d: %s -> %s\n", turn.Index, turn.ActionBrief, brief(turn.Obs.Content, 200))
	}
	var summary string
	var transforms []string
	receiptVersion := 2
	escalate := mode == compactionModeSummary
	if mode == compactionModeCollapseOnly {
		summary = collapseActionSkeleton(preCompactionSummary, folded)
		transforms = []string{"elide_tool_output", "collapse_action_skeleton"}
		receiptVersion = 3
		projectedKept := applyVerbatimUserBudget(append([]Turn(nil), kept...), t.policy.VerbatimUserMaxChars)
		projected := append(projectedKept, t.Turns[headEnd:]...)
		if t.compactionMode(t.projectedPressure(summary, projected)) == compactionModeSummary {
			escalate = true
		}
	}
	if escalate {
		if t.summarizerCircuitOpen() {
			if summary == "" {
				summary = collapseActionSkeleton(preCompactionSummary, folded)
			}
			transforms = []string{"elide_tool_output", "collapse_action_skeleton", "summarizer_circuit_open"}
			receiptVersion = 3
			mode = compactionModeCollapseOnly
		} else if modelSummary, ok := t.invokeSummarizer(summarize, head.String()); ok {
			summary = modelSummary
			if mode == compactionModeCollapseOnly {
				transforms = append([]string{"elide_tool_output", "collapse_action_skeleton"}, "model_summary")
			} else {
				transforms = []string{"elide_tool_output", "model_summary"}
			}
			receiptVersion = 2
			mode = compactionModeSummary
		} else {
			if summary == "" {
				summary = collapseActionSkeleton(preCompactionSummary, folded)
			}
			if summarize == nil {
				transforms = []string{"elide_tool_output", "collapse_action_skeleton"}
			} else {
				transforms = []string{"elide_tool_output", "collapse_action_skeleton", "summarizer_failed"}
			}
			receiptVersion = 3
			mode = compactionModeCollapseOnly
		}
	}
	if strings.TrimSpace(summary) != "" {
		preimageHash := compactionPreimageHash(preCompactionSummary, foldedPre)
		firstTurn, lastTurn := folded[0].Index, folded[len(folded)-1].Index
		t.Summary = summary
		kept = applyVerbatimUserBudget(kept, t.policy.VerbatimUserMaxChars)
		t.Turns = append(kept, t.Turns[headEnd:]...)
		afterRender := t.render()
		receipt := CompactionReceipt{
			Version: receiptVersion, CreatedAt: time.Now().UTC(), FirstTurn: firstTurn, LastTurn: lastTurn,
			RemovedTurns: len(folded), PreimageSHA256: preimageHash, SummarySHA256: sha256Hex(summary),
			KeptTurnIndices: keptIdx, KeyFiles: keyFiles(folded, 5),
			CitedFiles: citedFiles(folded, maxRebuildFiles),
			PolicyVersion: t.policy.PolicyVersion, WindowTokens: t.policy.WindowTokens,
			ReserveTokens: t.policy.ReserveTokens, MetadataSource: t.policy.MetadataSource,
			PressureBefore: pressureBefore, PressureAfter: t.compactionPressure(),
			CharsBefore: charsBefore, CharsAfter: len(afterRender),
			TokensBefore: tokensBefore, TokensAfter: estimateTokens(afterRender),
			Mode: mode, Transforms: transforms, SummarizerFailures: t.SummarizerFailures,
		}
		if len(kept) > 0 {
			receipt.KeptSHA256 = turnsSHA256(kept)
		}
		t.CompactionReceipts = append(t.CompactionReceipts, receipt)
		return &receipt
	}
	return nil
}

func collapseActionSkeleton(previousSummary string, folded []Turn) string {
	var b strings.Builder
	b.WriteString("COLLAPSED EARLIER WORK (local action skeleton; raw tool output removed):\n")
	if previousSummary != "" {
		preview, _, valid := artifact.Preview([]byte(previousSummary), maxCollapsedPriorSummaryChars, 0)
		if valid {
			fmt.Fprintf(&b, "Prior summary:\n%s\n", strings.TrimSpace(preview))
		}
	}
	start := max(0, len(folded)-maxCollapsedActionBriefs)
	b.WriteString("Recent actions:\n")
	for _, turn := range folded[start:] {
		action := strings.TrimSpace(turn.ActionBrief)
		if action == "" {
			action = strings.TrimSpace(turn.Tool)
		}
		fmt.Fprintf(&b, "- turn %d: %s\n", turn.Index, brief(action, maxCollapsedActionBriefChars))
	}
	if files := keyFiles(folded, 5); len(files) > 0 {
		b.WriteString("Key files:\n")
		for _, path := range files {
			fmt.Fprintf(&b, "- %s\n", path)
		}
	}
	return strings.TrimSpace(b.String())
}

// applyVerbatimUserBudget spends maxChars of verbatim budget over the kept
// user turns, newest to oldest: turns that fit stay verbatim, the first turn
// to overflow is truncated to the remaining budget via artifact.Preview (the
// same head+tail projection addTurn uses, so truncation is disclosed
// in-band), and older turns beyond the budget are elided with the same
// Elided/OriginalSHA256 fields Step-1 elision uses — render() and the audit
// trail treat them identically. Already-elided turns cost nothing (they
// render as a short placeholder). The budget applies regardless of Pinned:
// user turns are created pinned precisely so Step-1 never touches them, and
// this cap is the deliberate bound that keeps that exemption from growing the
// transcript without limit. maxChars<=0 disables the cap.
func applyVerbatimUserBudget(kept []Turn, maxChars int) []Turn {
	if maxChars <= 0 {
		return kept
	}
	remaining := maxChars
	for i := len(kept) - 1; i >= 0; i-- {
		obs := &kept[i].Obs
		if obs.Elided {
			continue
		}
		if len(obs.Content) <= remaining {
			remaining -= len(obs.Content)
			continue
		}
		if remaining > 0 {
			if preview, _, valid := artifact.Preview([]byte(obs.Content), remaining, 0); valid {
				obs.Content = preview
			}
			remaining = 0
			continue
		}
		obs.OriginalSHA256 = sha256Hex(obs.Content)
		obs.Elided = true
	}
	return kept
}

// keyFiles is the deterministic key-file selector recorded on v2 compaction
// receipts: the top-k most-edited paths among turns, counting Tool=="patch"
// and Tool=="edit" ActionBriefs via the same "<tool> <path>" parsing
// filesTouched ships,
// ordered by edit count descending with first-seen order breaking ties. It is
// a pure function of the folded turns — a factual record of what actually ran
// through the kernel, not model recall — and the substrate a later
// content-reinjection tier consumes.
func keyFiles(turns []Turn, k int) []string {
	if k <= 0 {
		return nil
	}
	counts := map[string]int{}
	var order []string // first-seen path order
	for _, turn := range turns {
		if turn.Tool != "patch" && turn.Tool != "edit" {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(turn.ActionBrief, turn.Tool+" "))
		if path == "" {
			continue
		}
		if counts[path] == 0 {
			order = append(order, path)
		}
		counts[path]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	if len(order) > k {
		order = order[:k]
	}
	return order
}

// citedFiles is the newest-first unique path list a post-compact rebuild
// re-reads: read Path (or "read <path>" briefs) plus patch/edit briefs.
// skill:// and traversal paths stay out. Cap k. Pure function of folded turns.
func citedFiles(turns []Turn, k int) []string {
	if k <= 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i := len(turns) - 1; i >= 0 && len(out) < k; i-- {
		path, ok := rebuildRelPath(citedPath(turns[i]))
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func citedPath(turn Turn) string {
	switch turn.Tool {
	case "read":
		if strings.TrimSpace(turn.Path) != "" {
			return turn.Path
		}
		return strings.TrimSpace(strings.TrimPrefix(turn.ActionBrief, "read "))
	case "patch", "edit":
		return strings.TrimSpace(strings.TrimPrefix(turn.ActionBrief, turn.Tool+" "))
	default:
		return ""
	}
}

func rebuildRelPath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "..") {
		return "", false
	}
	if _, ok := parseSkillURI(raw); ok {
		return "", false
	}
	if strings.Contains(raw, "://") || filepath.IsAbs(raw) {
		return "", false
	}
	return raw, true
}

// SummaryContent is the structured shape of a compaction summary: Cline
// types its rolling summary as Goal/State(Done|InProgress|Blocked)/
// Highlights/Next/Files(read+modified); carina's compact() previously stored
// unstructured prose from a single hand-written instruction ("Summarize...
// <=200 words"). This gives the same rolling Transcript.Summary string field
// a predictable internal shape without changing its type or any persisted
// schema — renderSummaryTemplate still produces a plain string, so
// checkpoint.go/subagent.go/render() (all of which treat Summary as prose)
// need no changes.
//
// FilesRead/FilesModified are deliberately NOT filled from model output:
// filesTouched derives them from the transcript's own turns (Tool=="read"/
// "patch" ActionBrief), so the "Files" section is a factual record grounded
// in what actually ran through the kernel, not something the model could get
// wrong or omit.
type SummaryContent struct {
	Goal          string
	Done          []string
	InProgress    []string
	Blocked       []string
	Highlights    []string
	Next          []string
	FilesRead     []string
	FilesModified []string
}

// summaryTemplateHeadings are the section markers renderSummaryTemplate
// writes and parseSummaryContent looks for. Keeping them as a shared slice
// of (heading, field-setter) pairs would be overkill for five sections; they
// are duplicated as literal strings in both functions instead, with this
// comment as the single place documenting that the two must stay in sync.
const (
	headingGoal       = "Goal:"
	headingDone       = "Done:"
	headingInProgress = "In Progress:"
	headingBlocked    = "Blocked:"
	headingHighlights = "Highlights:"
	headingNext       = "Next:"
	headingFilesRead  = "Files Read:"
	headingFilesMod   = "Files Modified:"
)

// renderSummaryTemplate formats a SummaryContent into the plain-text shape
// stored in Transcript.Summary. Empty list sections are omitted entirely
// (a compaction with nothing blocked, e.g., should not render a dangling
// "Blocked:" heading with no bullets under it).
func renderSummaryTemplate(sc SummaryContent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", headingGoal, strings.TrimSpace(sc.Goal))
	writeSummaryList(&b, headingDone, sc.Done)
	writeSummaryList(&b, headingInProgress, sc.InProgress)
	writeSummaryList(&b, headingBlocked, sc.Blocked)
	writeSummaryList(&b, headingHighlights, sc.Highlights)
	writeSummaryList(&b, headingNext, sc.Next)
	writeSummaryList(&b, headingFilesRead, sc.FilesRead)
	writeSummaryList(&b, headingFilesMod, sc.FilesModified)
	return strings.TrimRight(b.String(), "\n")
}

func writeSummaryList(b *strings.Builder, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n", heading)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Fprintf(b, "- %s\n", item)
	}
}

// parseSummaryContent best-effort parses a renderSummaryTemplate-shaped
// string back into a SummaryContent. It is fail-closed in the sense that
// mirrors the rest of this file's compaction machinery: if the text has no
// recognizable "Goal:" heading (the one required section), ok is false and
// callers must not assume the returned SummaryContent reflects the text —
// prior behavior (treating the whole string as opaque prose) still applies.
// This lets a caller (or future tooling/inspection) recover structure from
// an already-compacted Transcript.Summary without requiring a parallel
// structured field or a persistence-format change.
func parseSummaryContent(text string) (SummaryContent, bool) {
	var sc SummaryContent
	lines := strings.Split(text, "\n")
	var current *[]string
	sawGoal := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, headingGoal):
			sc.Goal = strings.TrimSpace(strings.TrimPrefix(trimmed, headingGoal))
			current = nil
			sawGoal = true
		case trimmed == headingDone:
			current = &sc.Done
		case trimmed == headingInProgress:
			current = &sc.InProgress
		case trimmed == headingBlocked:
			current = &sc.Blocked
		case trimmed == headingHighlights:
			current = &sc.Highlights
		case trimmed == headingNext:
			current = &sc.Next
		case trimmed == headingFilesRead:
			current = &sc.FilesRead
		case trimmed == headingFilesMod:
			current = &sc.FilesModified
		case strings.HasPrefix(trimmed, "- ") && current != nil:
			*current = append(*current, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		}
	}
	if !sawGoal {
		return SummaryContent{}, false
	}
	return sc, true
}

// filesTouched derives the Files(read+modified) section deterministically
// from the transcript's own turns rather than trusting the model to recall
// which files it read or changed: ActionBrief for "read", "patch", and "edit"
// is always exactly "<tool> <path>" (see briefAction in agent.go), so this
// is a factual read of what already ran through the kernel, not a
// re-summarization. Order is first-seen, deduplicated; both are capped so a
// long-running task's summary can't grow unboundedly with the rest of the
// template.
func filesTouched(turns []Turn) (read, modified []string) {
	const maxFiles = 20
	seenRead := map[string]bool{}
	seenMod := map[string]bool{}
	for _, turn := range turns {
		switch turn.Tool {
		case "read":
			path := strings.TrimSpace(strings.TrimPrefix(turn.ActionBrief, "read "))
			if path != "" && !seenRead[path] {
				seenRead[path] = true
				if len(read) < maxFiles {
					read = append(read, path)
				}
			}
		case "patch", "edit":
			path := strings.TrimSpace(strings.TrimPrefix(turn.ActionBrief, turn.Tool+" "))
			if path != "" && !seenMod[path] {
				seenMod[path] = true
				if len(modified) < maxFiles {
					modified = append(modified, path)
				}
			}
		}
	}
	return read, modified
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// turnsSHA256 hashes a slice of turns exactly as they would persist (JSON
// shape), for the v2 receipt's KeptSHA256 field.
func turnsSHA256(turns []Turn) string {
	raw, _ := json.Marshal(turns)
	return sha256Hex(string(raw))
}

func compactionPreimageHash(previousSummary string, turns []Turn) string {
	raw, _ := json.Marshal(struct {
		PreviousSummary string `json:"previous_summary"`
		Turns           []Turn `json:"turns"`
	}{PreviousSummary: previousSummary, Turns: turns})
	return sha256Hex(string(raw))
}

func brief(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// LoopGuard detects unproductive loops: the same action repeated, or many
// turns with no state change (no edit). This is the loop-safety net the
// research found missing in most agents.
//
// Beyond the soft nudge-at-MaxRepeat, LoopGuard also tracks a cumulative
// mistake count across *all* repeated fingerprints seen so far (not just the
// count of one signature). A model that dodges the per-signature threshold by
// rotating between a handful of repeated actions still trips the hard limit
// once its total mistake count crosses MaxHardRepeat, so the hard stop can't
// be evaded by cycling through variations.
type LoopGuard struct {
	seen           map[string]int
	MaxRepeat      int
	turnsSinceEdit int
	MaxNoProgress  int
	mistakes       int
	MaxHardRepeat  int
}

func newLoopGuard() *LoopGuard {
	return &LoopGuard{seen: map[string]int{}, MaxRepeat: 3, MaxNoProgress: 6, MaxHardRepeat: 6}
}

// fingerprint records an action; returns true if it has been repeated too
// many times (caller should nudge or abort).
func (g *LoopGuard) repeated(tool, arg string) bool {
	soft, _ := g.observe(tool, arg)
	return soft
}

// observe records one action exactly once and returns the soft-nudge and
// hard-stop decisions for that observation.
func (g *LoopGuard) observe(tool, arg string) (bool, bool) {
	h := sha256.Sum256([]byte(tool + "\x00" + arg))
	key := hex.EncodeToString(h[:8])
	g.seen[key]++
	if g.seen[key] > 1 {
		g.mistakes++
	}
	return g.seen[key] >= g.MaxRepeat, g.hardStop()
}

// hardStop reports whether the cumulative mistake count has crossed
// MaxHardRepeat without recording a new observation.
func (g *LoopGuard) hardStop() bool {
	return g.MaxHardRepeat > 0 && g.mistakes >= g.MaxHardRepeat
}

// progress resets the no-progress counter (call after a patch/edit); tick
// advances it; stalled reports whether we've gone too long with no change.
func (g *LoopGuard) madeProgress() { g.turnsSinceEdit = 0 }
func (g *LoopGuard) tick()         { g.turnsSinceEdit++ }
func (g *LoopGuard) stalled() bool { return g.turnsSinceEdit >= g.MaxNoProgress }
