package daemon

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	sessionDialogueKeepPairs = 8
	sessionDialogueLastMax   = 8000
	sessionDialogueOlderMax  = 1500
	sessionDialoguePromptMax = 4000
)

// attachSessionDialogue copies prior operator/assistant turns from this
// session into the model-view transcript. Each execution.start is a fresh
// ReAct run; without this, deictic follow-ups ("对此你怎么看") have no
// antecedent. The audit log is not copied — only user prompts and
// done.summary. The latest pair is pinned so compact cannot drop it
// before the first Think.
func (d *Daemon) attachSessionDialogue(sess *sessionstore.Session, task *scheduler.ExecutionRun, tr *Transcript) {
	if d == nil || sess == nil || task == nil || tr == nil {
		return
	}
	pairs := d.sessionDialoguePairs(sess.SessionID, task.RunID)
	if len(pairs) == 0 {
		return
	}
	older := pairs[:len(pairs)-1]
	last := pairs[len(pairs)-1]
	if body := renderDialoguePairs(older, sessionDialogueOlderMax); body != "" {
		tr.addTurn(Turn{
			Tool:        "user",
			ActionBrief: "session-dialogue",
			Obs:         Observation{Content: body},
		})
	}
	if body := renderDialoguePairs([]dialoguePair{last}, sessionDialogueLastMax); body != "" {
		tr.addTurn(Turn{
			Tool:        "user",
			ActionBrief: "session-dialogue",
			Obs: Observation{
				Content: body,
				Pinned:  true,
			},
		})
	}
}

type dialoguePair struct {
	user      string
	assistant string
}

func (d *Daemon) sessionDialoguePairs(sessionID, currentRunID string) []dialoguePair {
	if d == nil || d.sched == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	runs := d.sched.List()
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].RunID < runs[j].RunID
		}
		return runs[i].CreatedAt.Before(runs[j].CreatedAt)
	})
	out := make([]dialoguePair, 0, sessionDialogueKeepPairs)
	for _, run := range runs {
		if run == nil || run.SessionID != sessionID || run.RunID == currentRunID {
			continue
		}
		user := strings.TrimSpace(run.UserPrompt)
		assistant := strings.TrimSpace(run.Summary)
		if user == "" || assistant == "" {
			continue
		}
		switch run.Status {
		case "completed", "degraded":
		default:
			continue
		}
		out = append(out, dialoguePair{
			user:      clipUTF8(user, sessionDialoguePromptMax),
			assistant: assistant,
		})
	}
	if len(out) > sessionDialogueKeepPairs {
		out = out[len(out)-sessionDialogueKeepPairs:]
	}
	return out
}

func renderDialoguePairs(pairs []dialoguePair, answerMax int) string {
	if len(pairs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Earlier in this conversation (same session):\n")
	for i, pair := range pairs {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "Operator: %s\nCarina: %s\n", pair.user, clipUTF8(pair.assistant, answerMax))
	}
	return strings.TrimSpace(b.String())
}

func clipUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimRight(value, " \n\t") + "…"
}
