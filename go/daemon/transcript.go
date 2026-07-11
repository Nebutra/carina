package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/artifact"
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
	Turns              []Turn
	CompactionReceipts []CompactionReceipt `json:"compaction_receipts,omitempty"`
	policy             CompactionPolicy
}

// CompactionReceipt is the auditable record of one Step-2 summarize fold.
// Semantics are versioned:
//
//   - Version 1 (historical; still valid for old checkpoints and audit
//     entries): the whole head [FirstTurn..LastTurn] was folded into the
//     rolling summary, and PreimageSHA256 covers previous-summary + the
//     entire pre-compaction head.
//   - Version 2 (current): the head is partitioned into user-authored turns
//     (kept verbatim in the transcript — see compact()) and everything else
//     (folded). PreimageSHA256 covers previous-summary + the FOLDED turns
//     only; FirstTurn/LastTurn/RemovedTurns likewise describe the folded set.
//     KeptTurnIndices records which head turns were partitioned out,
//     KeptSHA256 hashes the kept turns exactly as retained (post
//     verbatim-budget truncation/elision), and KeyFiles is the deterministic
//     top-K most-edited files among the folded turns — the substrate a later
//     content-reinjection tier consumes.
type CompactionReceipt struct {
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	FirstTurn       int       `json:"first_turn"`
	LastTurn        int       `json:"last_turn"`
	RemovedTurns    int       `json:"removed_turns"`
	PreimageSHA256  string    `json:"preimage_sha256"`
	SummarySHA256   string    `json:"summary_sha256"`
	KeptTurnIndices []int     `json:"kept_turn_indices,omitempty"`
	KeptSHA256      string    `json:"kept_sha256,omitempty"`
	KeyFiles        []string  `json:"key_files,omitempty"`
}

// CompactionPolicy bounds the model view (char-budget based; claude-cli does
// not expose token counts cheaply, so we approximate with characters).
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
	MaxTokens int
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

// addTurn records a completed turn, truncating oversized observations up front.
// A new turn carrying a Path (a read-family tool) first supersedes any
// earlier, still-verbatim turn of the identical path: the earlier read is now
// stale (this turn proves the model has the current content), so keeping both
// copies verbatim in the model view only burns budget for no benefit — see
// supersedeStaleReads.
func (t *Transcript) addTurn(turn Turn) {
	if len(turn.Obs.Content) > t.policy.ToolOutputMax && !turn.Obs.Pinned {
		preview, _, valid := artifact.Preview([]byte(turn.Obs.Content), t.policy.ToolOutputMax, 0)
		if valid {
			turn.Obs.Content = preview
		}
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
	var b strings.Builder
	if t.Summary != "" {
		fmt.Fprintf(&b, "SUMMARY OF EARLIER WORK:\n%s\n\n", t.Summary)
	}
	for _, turn := range t.Turns {
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
// above) OR the token-estimate trigger (MaxTokens, via agent.go's
// estimateTokens helper) fires. MaxTokens=0 (the default) makes the
// token-estimate side of the OR permanently false, so shouldCompact() reduces
// to the plain t.size() > triggerChars() check that predates this field —
// byte-identical to prior behavior for every existing caller/policy.
//
// This mirrors codebuff's token-triggered compaction while staying scoped to
// carina's existing char-budget machinery rather than replacing it: carina
// has no cheap exact token count (see the CompactionPolicy doc comment), so
// MaxTokens is an additional early-fire signal layered on top of the
// char-based trigger, not a replacement for it.
func (t *Transcript) shouldCompact() bool {
	if t.size() > t.triggerChars() {
		return true
	}
	if t.policy.MaxTokens > 0 && estimateTokens(t.render()) > t.policy.MaxTokens {
		return true
	}
	return false
}

// compact enforces the char budget. Step 1: elide old, non-pinned
// observations (keeping the most recent KeepRecent turns verbatim). Step 2:
// if still over budget, partition the head (all but the recent tail) into
// user-authored turns (Tool=="user": steering drains, fork-task notices —
// kept verbatim, bounded by VerbatimUserMaxChars) and everything else, and
// fold only the latter into the rolling Summary via the provided summarizer
// (a cheap model call). User turns are preserved structurally rather than
// trusted to survive a model-written summary: a compaction that folds
// "don't use X" into prose loses the correction and the model cannot know
// what it forgot. The audit log is untouched.
func (t *Transcript) compact(summarize func(head string) (string, error)) *CompactionReceipt {
	if !t.shouldCompact() {
		return nil
	}
	preCompactionSummary := t.Summary
	preCompactionTurns := append([]Turn(nil), t.Turns...)
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
	var head strings.Builder
	if t.Summary != "" {
		fmt.Fprintf(&head, "%s\n", t.Summary)
	}
	for _, turn := range folded {
		fmt.Fprintf(&head, "turn %d: %s -> %s\n", turn.Index, turn.ActionBrief, brief(turn.Obs.Content, 200))
	}
	if summary, err := summarize(head.String()); err == nil && summary != "" {
		preimageHash := compactionPreimageHash(preCompactionSummary, foldedPre)
		firstTurn, lastTurn := folded[0].Index, folded[len(folded)-1].Index
		t.Summary = summary
		kept = applyVerbatimUserBudget(kept, t.policy.VerbatimUserMaxChars)
		t.Turns = append(kept, t.Turns[headEnd:]...)
		receipt := CompactionReceipt{
			Version: 2, CreatedAt: time.Now().UTC(), FirstTurn: firstTurn, LastTurn: lastTurn,
			RemovedTurns: len(folded), PreimageSHA256: preimageHash, SummarySHA256: sha256Hex(summary),
			KeptTurnIndices: keptIdx, KeyFiles: keyFiles(folded, 5),
		}
		if len(kept) > 0 {
			receipt.KeptSHA256 = turnsSHA256(kept)
		}
		t.CompactionReceipts = append(t.CompactionReceipts, receipt)
		return &receipt
	}
	return nil
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
// ActionBriefs via the same "<tool> <path>" parsing filesTouched ships,
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
		if turn.Tool != "patch" {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(turn.ActionBrief, "patch "))
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
// which files it read or changed: ActionBrief for "read" and "patch" tools
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
		case "patch":
			path := strings.TrimSpace(strings.TrimPrefix(turn.ActionBrief, "patch "))
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
