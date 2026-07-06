package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// verifierSystemPrompt frames the independent judge. It sees only the task, the
// success criteria, and the claimed result — never the coordinator's transcript
// — so its ruling is an independent check, not an echo of the worker's reasoning.
const verifierSystemPrompt = `You are an independent verifier (a judge). You did NOT do the work — you only rule whether the stated task was actually completed. Given the task, its objective success criteria, and the worker's claimed result, reply with ONLY a JSON object:
{"verdict":"pass"} if the task is genuinely complete, or
{"verdict":"reject","reason":"what is still missing"} if it is not.
Be skeptical of vague or unsupported completion claims. Output nothing but the JSON.`

type verdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// verifyDone asks the independent verifier whether a done-claim is real. It is
// default-lenient: with no verifier configured it passes, and any verifier
// transport error or malformed verdict fails OPEN (accepts), so a broken or
// absent verifier can never wedge a run or block legitimate completion.
func (d *Daemon) verifyDone(ctx context.Context, sess *sessionstore.Session, task *scheduler.Task, summary string) (bool, string) {
	if d.verifier == nil {
		return true, ""
	}
	prompt := buildVerifierPrompt(task, summary, d.appliedPatchIDs(sess))
	raw, err := thinkWithRetry(ctx, d.verifier, verifierSystemPrompt+"\n\n"+prompt)
	if err != nil {
		return true, "" // fail open on transport error
	}
	v, err := parseVerdict(raw)
	if err != nil {
		return true, "" // fail open on malformed verdict
	}
	if v.Verdict == "reject" {
		reason := strings.TrimSpace(v.Reason)
		if reason == "" {
			reason = "verifier rejected the done-claim"
		}
		return false, reason
	}
	return true, ""
}

// buildVerifierPrompt renders the judge's context from the task, its success
// criteria, the claimed summary, and applied patch ids — deliberately NOT the
// coordinator's transcript, to keep the check independent.
func buildVerifierPrompt(task *scheduler.Task, summary string, patches []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TASK:\n%s\n\n", task.UserPrompt)
	if len(task.SuccessCriteria) > 0 {
		b.WriteString("SUCCESS CRITERIA:\n")
		for _, c := range task.SuccessCriteria {
			fmt.Fprintf(&b, "- kind=%s command=%q path=%q pattern=%q\n", c.Kind, c.Command, c.Path, c.Pattern)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "WORKER'S CLAIMED RESULT:\n%s\n\n", summary)
	if len(patches) > 0 {
		fmt.Fprintf(&b, "APPLIED PATCHES: %s\n\n", strings.Join(patches, ", "))
	}
	b.WriteString("Did the worker actually complete the task? Reply with the verdict JSON.")
	return b.String()
}

// parseVerdict extracts a strict pass/reject verdict, tolerating markdown fences
// and surrounding prose (like parseAction).
func parseVerdict(raw string) (verdict, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return verdict{}, fmt.Errorf("no json object in verdict")
	}
	var v verdict
	if err := json.Unmarshal([]byte(raw[start:end+1]), &v); err != nil {
		return verdict{}, err
	}
	v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
	if v.Verdict != "pass" && v.Verdict != "reject" {
		return verdict{}, fmt.Errorf("invalid verdict %q", v.Verdict)
	}
	return v, nil
}
