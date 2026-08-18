package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestReviewTurnHonestyGate is the permanent lock for 7c7770c / ISSUE-030 / #39.
// CI job: "Go test with coverage (E2E across Go/Rust/Zig)" in
// .github/workflows/ci.yml (build-and-test). Do not re-teach /review as a
// skill:// target, and do not let a Grok high deadline look like a no-op.
func TestReviewTurnHonestyGate(t *testing.T) {
	t.Run("catalog_does_not_teach_skill_review", func(t *testing.T) {
		ws := isolatedSkillWorkspace(t)
		writeProjectSkill(t, ws, "review", "description: Skill review\n", "SHOULD NOT BE CATALOGED")
		got := buildDynamicSkillPrompt(ws, "Please review authentication", builtinCommandSpecs(), false)
		if !strings.Contains(got, "- command /review") || !strings.Contains(got, "(slash command; not skill://)") {
			t.Fatalf("catalog must keep /review as a slash command:\n%s", got)
		}
		if strings.Contains(got, "skill://review") {
			t.Fatalf("catalog must not advertise skill://review:\n%s", got)
		}
		if strings.Contains(got, "SHOULD NOT BE CATALOGED") {
			t.Fatalf("review skill body leaked into the catalog:\n%s", got)
		}
	})

	t.Run("explicit_review_mention_stays_slash", func(t *testing.T) {
		ws := isolatedSkillWorkspace(t)
		writeProjectSkill(t, ws, "review", "description: Skill review\n", "SHOULD NOT BE REQUESTED")
		got := buildDynamicSkillPrompt(ws, "Use $review on this branch", builtinCommandSpecs(), false)
		if strings.Contains(got, "skill://review") || strings.Contains(got, "SHOULD NOT BE REQUESTED") {
			t.Fatalf("$review must not request skill://review:\n%s", got)
		}
		if strings.Contains(got, "SKILL WARNING") && strings.Contains(got, "$review") {
			t.Fatalf("owned slash name must not warn as a missing skill:\n%s", got)
		}
	})

	t.Run("read_skill_review_aliases_slash", func(t *testing.T) {
		d, ws := newLoopDaemon(t)
		defer d.Close()
		writeProjectSkill(t, ws, "review", "description: Skill review\n", "SHOULD NOT WIN")
		sess, _ := d.store.CreateSession(ws, "safe-edit")
		d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
		task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "probe")

		out := d.readSkillURI(sess, task, "skill://review")
		if out.status != "completed" {
			t.Fatalf("slash command alias must complete, got %+v", out)
		}
		if !strings.Contains(out.display, "slash command") || !strings.Contains(out.display, "Do not retry skill://review") {
			t.Fatalf("alias must tell the model /review is not a skill:\n%s", out.display)
		}
		if !strings.Contains(out.display, "Review the current workspace") {
			t.Fatalf("alias must include the /review stance:\n%s", out.display)
		}
		if strings.Contains(out.display, "SHOULD NOT WIN") || strings.Contains(out.display, "<carina_skill") {
			t.Fatalf("colliding skill body must not win the URI:\n%s", out.display)
		}
	})

	t.Run("unknown_and_traversal_fail_closed", func(t *testing.T) {
		d, ws := newLoopDaemon(t)
		defer d.Close()
		writeProjectSkill(t, ws, "pdf", "description: Work with PDF files\n", "SECRET BODY")
		sess, _ := d.store.CreateSession(ws, "safe-edit")
		d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
		task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "probe")

		out := d.readSkillURI(sess, task, "skill://../secret")
		if out.status == "completed" || strings.Contains(out.display, "SECRET BODY") {
			t.Fatalf("traversal must fail closed: %+v", out)
		}
		out = d.readSkillURI(sess, task, "skill://missing")
		if out.status == "completed" || !strings.Contains(out.display, "unknown") {
			t.Fatalf("unknown skill must fail closed: %+v", out)
		}
	})

	t.Run("operator_tool_error_is_honest", func(t *testing.T) {
		got := operatorFacingToolError("error: unknown or disabled skill://review")
		if strings.HasPrefix(got, "error:") || strings.HasPrefix(got, "DENIED:") {
			t.Fatalf("operator copy leaked a stack prefix: %q", got)
		}
		if !strings.Contains(got, "skill://review") {
			t.Fatalf("operator copy dropped the skill cause: %q", got)
		}
		if len(operatorFacingToolError("error: "+strings.Repeat("x", 400))) > 240 {
			t.Fatal("operator copy exceeded 240 bytes")
		}
	})

	t.Run("grok_deadline_degrades_after_tools", func(t *testing.T) {
		tr := &Transcript{Turns: []Turn{{Tool: "read", Path: "skill://review"}}}
		if !reasonerProgressShouldDegrade(context.DeadlineExceeded, tr) {
			t.Fatal("deadline after a tool observation must degrade")
		}
		if reasonerProgressShouldDegrade(context.DeadlineExceeded, &Transcript{}) {
			t.Fatal("deadline with no tools must stay failed")
		}
		if reasonerProgressShouldDegrade(errors.New("provider boom"), tr) {
			t.Fatal("non-deadline errors must not degrade just because tools ran")
		}
	})

	t.Run("grok_think_timeout", func(t *testing.T) {
		for _, effort := range []string{"high", "xhigh", "max"} {
			if got := grokThinkTimeout(grokCLIReasonerTimeout, effort); got != grokCLIReasonerHighTimeout {
				t.Fatalf("%s = %v, want %v", effort, got, grokCLIReasonerHighTimeout)
			}
		}
		if grokCLIReasonerTimeout < 180*time.Second || grokCLIReasonerHighTimeout != 360*time.Second {
			t.Fatalf("timeout constants drifted: base=%v high=%v", grokCLIReasonerTimeout, grokCLIReasonerHighTimeout)
		}
		if got := grokThinkTimeout(5*time.Second, "high"); got != 5*time.Second {
			t.Fatalf("test-shortened timeout must stay 5s, got %v", got)
		}
	})
}
