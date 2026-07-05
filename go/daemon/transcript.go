package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
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
	Tool    string
	Content string
	Pinned  bool // failing tests / current edit / patch result — never elided
	Elided  bool
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
	Task    string
	Summary string // rolling summary of compacted-away head turns
	Turns   []Turn
	policy  CompactionPolicy
}

// CompactionPolicy bounds the model view (char-budget based; claude-cli does
// not expose token counts cheaply, so we approximate with characters).
type CompactionPolicy struct {
	MaxChars       int // total transcript char budget before compaction
	KeepRecent     int // keep this many most-recent turns verbatim
	ToolOutputMax  int // truncate any single observation to this many chars
	SummarizeAfter int // if still over budget after eliding, summarize the head
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

// compact enforces the char budget. Step 1: elide old, non-pinned
// observations (keeping the most recent KeepRecent turns verbatim). Step 2:
// if still over budget, fold the head turns into the rolling Summary via the
// provided summarizer (a cheap model call). The audit log is untouched.
func (t *Transcript) compact(summarize func(head string) (string, error)) {
	if t.size() <= t.policy.MaxChars {
		return
	}
	// Step 1: elide.
	cutoff := len(t.Turns) - t.policy.KeepRecent
	for i := 0; i < cutoff; i++ {
		if !t.Turns[i].Obs.Pinned {
			t.Turns[i].Obs.Elided = true
		}
	}
	if t.size() <= t.policy.MaxChars || len(t.Turns) <= t.policy.SummarizeAfter {
		return
	}
	// Step 2: summarize the head (all but the recent tail) into Summary.
	tail := t.policy.KeepRecent
	headEnd := len(t.Turns) - tail
	if headEnd <= 0 {
		return
	}
	var head strings.Builder
	if t.Summary != "" {
		fmt.Fprintf(&head, "%s\n", t.Summary)
	}
	for _, turn := range t.Turns[:headEnd] {
		fmt.Fprintf(&head, "turn %d: %s -> %s\n", turn.Index, turn.ActionBrief, brief(turn.Obs.Content, 200))
	}
	if summary, err := summarize(head.String()); err == nil && summary != "" {
		t.Summary = summary
		t.Turns = t.Turns[headEnd:]
	}
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
type LoopGuard struct {
	seen           map[string]int
	MaxRepeat      int
	turnsSinceEdit int
	MaxNoProgress  int
}

func newLoopGuard() *LoopGuard {
	return &LoopGuard{seen: map[string]int{}, MaxRepeat: 3, MaxNoProgress: 6}
}

// fingerprint records an action; returns true if it has been repeated too
// many times (caller should nudge or abort).
func (g *LoopGuard) repeated(tool, arg string) bool {
	h := sha256.Sum256([]byte(tool + "\x00" + arg))
	key := hex.EncodeToString(h[:8])
	g.seen[key]++
	return g.seen[key] >= g.MaxRepeat
}

// progress resets the no-progress counter (call after a patch/edit); tick
// advances it; stalled reports whether we've gone too long with no change.
func (g *LoopGuard) madeProgress() { g.turnsSinceEdit = 0 }
func (g *LoopGuard) tick()         { g.turnsSinceEdit++ }
func (g *LoopGuard) stalled() bool { return g.turnsSinceEdit >= g.MaxNoProgress }
