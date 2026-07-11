package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
}

// Turn is one model decision + its observation.
type Turn struct {
	Index       int
	Thought     string
	Tool        string
	ActionBrief string // e.g. `read greet.py` / `run [go test]`
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

type CompactionReceipt struct {
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	FirstTurn      int       `json:"first_turn"`
	LastTurn       int       `json:"last_turn"`
	RemovedTurns   int       `json:"removed_turns"`
	PreimageSHA256 string    `json:"preimage_sha256"`
	SummarySHA256  string    `json:"summary_sha256"`
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
}

func defaultCompactionPolicy() CompactionPolicy {
	return CompactionPolicy{
		MaxChars:       24000,
		KeepRecent:     3,
		ToolOutputMax:  2000,
		SummarizeAfter: 6,
	}
}

func newTranscript(task string) *Transcript {
	return &Transcript{Task: task, policy: defaultCompactionPolicy()}
}

// addTurn records a completed turn, truncating oversized observations up front.
func (t *Transcript) addTurn(turn Turn) {
	if len(turn.Obs.Content) > t.policy.ToolOutputMax && !turn.Obs.Pinned {
		turn.Obs.Content = turn.Obs.Content[:t.policy.ToolOutputMax] + "…[truncated]"
	}
	turn.Index = len(t.Turns) + 1
	t.Turns = append(t.Turns, turn)
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
			obs = "[elided to save context]"
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

// compact enforces the char budget. Step 1: elide old, non-pinned
// observations (keeping the most recent KeepRecent turns verbatim). Step 2:
// if still over budget, fold the head turns into the rolling Summary via the
// provided summarizer (a cheap model call). The audit log is untouched.
func (t *Transcript) compact(summarize func(head string) (string, error)) *CompactionReceipt {
	trigger := t.triggerChars()
	if t.size() <= trigger {
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
	if t.size() <= trigger || len(t.Turns) <= t.policy.SummarizeAfter {
		return nil
	}
	// Step 2: summarize the head (all but the recent tail) into Summary.
	tail := t.policy.KeepRecent
	headEnd := len(t.Turns) - tail
	if headEnd <= 0 {
		return nil
	}
	var head strings.Builder
	if t.Summary != "" {
		fmt.Fprintf(&head, "%s\n", t.Summary)
	}
	for _, turn := range t.Turns[:headEnd] {
		fmt.Fprintf(&head, "turn %d: %s -> %s\n", turn.Index, turn.ActionBrief, brief(turn.Obs.Content, 200))
	}
	if summary, err := summarize(head.String()); err == nil && summary != "" {
		preimageHash := compactionPreimageHash(preCompactionSummary, preCompactionTurns[:headEnd])
		firstTurn, lastTurn := t.Turns[0].Index, t.Turns[headEnd-1].Index
		t.Summary = summary
		t.Turns = t.Turns[headEnd:]
		receipt := CompactionReceipt{
			Version: 1, CreatedAt: time.Now().UTC(), FirstTurn: firstTurn, LastTurn: lastTurn,
			RemovedTurns: headEnd, PreimageSHA256: preimageHash, SummarySHA256: sha256Hex(summary),
		}
		t.CompactionReceipts = append(t.CompactionReceipts, receipt)
		return &receipt
	}
	return nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
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
