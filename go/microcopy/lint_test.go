package microcopy

import (
	"strings"
	"testing"
)

// TestLintEmbeddedPoolsClean: the shipped pools must pass their own brand
// linter — the linter runs at test time, so a rule-breaking line cannot land.
func TestLintEmbeddedPoolsClean(t *testing.T) {
	if violations := LintPools(); len(violations) != 0 {
		for _, v := range violations {
			t.Errorf("embedded pool violation: %s", v)
		}
	}
}

// TestLintAmbientCatchesSeededViolations: the Ambient rules from the brand
// brief §4 — no exclamation marks, no emoji, no astronomy/cosmos vocabulary.
func TestLintAmbientCatchesSeededViolations(t *testing.T) {
	cases := []struct {
		locale string
		line   string
		rule   string // substring expected in the violation message
	}{
		{"en", "great job!", "exclamation"},
		{"zh", "干得漂亮！", "exclamation"},
		{"en", "done \U0001F389", "emoji"},
		{"zh", "完成 ✨", "emoji"},
		{"en", "shipping among the stars", "astronomy"},
		{"en", "a galaxy of options", "astronomy"},
		{"en", "cosmic-grade caching", "astronomy"},
		{"zh", "宇宙第一快", "astronomy"},
		{"zh", "星辰大海，说到做到", "astronomy"},
	}
	for _, tc := range cases {
		got := LintAmbientLine(tc.locale, tc.line)
		if len(got) == 0 {
			t.Errorf("LintAmbientLine(%s, %q) caught nothing, want %q", tc.locale, tc.line, tc.rule)
			continue
		}
		if !anyContains(got, tc.rule) {
			t.Errorf("LintAmbientLine(%s, %q) = %v, want a %q violation", tc.locale, tc.line, got, tc.rule)
		}
	}
	// Clean lines pass, including boundary cases the banned-term regex must
	// not overreach on ("start" is not "star").
	for _, ok := range []struct{ locale, line string }{
		{"en", "starting the daemon. one moment."},
		{"en", "restarting watchers."},
		{"zh", "缓存命中。此路我们走过。"},
	} {
		if got := LintAmbientLine(ok.locale, ok.line); len(got) != 0 {
			t.Errorf("LintAmbientLine(%s, %q) = %v, want clean", ok.locale, ok.line, got)
		}
	}
}

// TestLintGovernedCatchesSeededViolations: Governed templates are full
// sentences, must contain every declared placeholder, and carry no metaphor
// or personality.
func TestLintGovernedCatchesSeededViolations(t *testing.T) {
	cases := []struct {
		locale       string
		template     string
		placeholders []string
		rule         string
	}{
		// Declared placeholder absent from the template body.
		{"en", "Approval required for this action.", []string{"decision_id"}, "placeholder"},
		{"zh", "需要授权。", []string{"decision_id"}, "placeholder"},
		// Not a full sentence: no terminal punctuation.
		{"en", "Approval required: {decision_id}", []string{"decision_id"}, "sentence"},
		{"zh", "需要授权：{decision_id}", []string{"decision_id"}, "sentence"},
		// Not a full sentence: lowercase fragment (en only).
		{"en", "approval required: {decision_id}.", []string{"decision_id"}, "sentence"},
		// Metaphor terms are banned in the Governed register.
		{"en", "The ledger is sealed: {audit_id}.", []string{"audit_id"}, "metaphor"},
		{"en", "Rollback done, kettle's yours: {tx_id}.", []string{"tx_id"}, "metaphor"},
		{"zh", "账本已封存：{audit_id}。", []string{"audit_id"}, "metaphor"},
		// Exclamation marks and emoji never appear in Governed copy.
		{"en", "Denied by policy {rule_id}!", []string{"rule_id"}, "exclamation"},
		{"en", "Approved \U0001F44D decision {decision_id}.", []string{"decision_id"}, "emoji"},
		// Astronomy vocabulary is banned in every register.
		{"en", "Verified across the constellation: {head}.", []string{"head"}, "astronomy"},
	}
	for _, tc := range cases {
		got := LintGovernedTemplate(tc.locale, tc.template, tc.placeholders)
		if len(got) == 0 {
			t.Errorf("LintGovernedTemplate(%s, %q) caught nothing, want %q", tc.locale, tc.template, tc.rule)
			continue
		}
		if !anyContains(got, tc.rule) {
			t.Errorf("LintGovernedTemplate(%s, %q) = %v, want a %q violation", tc.locale, tc.template, got, tc.rule)
		}
	}
	// A disciplined sober line passes.
	clean := LintGovernedTemplate("en",
		"Denied by policy {rule_id}: {action} was not attempted. Audit entry {audit_id} written.",
		[]string{"rule_id", "action", "audit_id"})
	if len(clean) != 0 {
		t.Errorf("clean governed template flagged: %v", clean)
	}
}

// TestLintDegradeCatchesSeededViolations: Degrade lines are calm-factual —
// placeholders present, no metaphor, no exclamation, no emoji. (They are not
// held to the full-sentence rule: "Fix: carina doctor" is a remedy, not a
// sentence.)
func TestLintDegradeCatchesSeededViolations(t *testing.T) {
	cases := []struct {
		locale       string
		template     string
		placeholders []string
		rule         string
	}{
		{"en", "Timed out. Fix: carina doctor", []string{"seconds"}, "placeholder"},
		{"en", "Oh no, {action} timed out!", []string{"action"}, "exclamation"},
		{"en", "The stars misaligned for {action}.", []string{"action"}, "astronomy"},
		{"zh", "账本对不上：{tx_id}。", []string{"tx_id"}, "metaphor"},
	}
	for _, tc := range cases {
		got := LintDegradeTemplate(tc.locale, tc.template, tc.placeholders)
		if len(got) == 0 {
			t.Errorf("LintDegradeTemplate(%s, %q) caught nothing, want %q", tc.locale, tc.template, tc.rule)
			continue
		}
		if !anyContains(got, tc.rule) {
			t.Errorf("LintDegradeTemplate(%s, %q) = %v, want a %q violation", tc.locale, tc.template, got, tc.rule)
		}
	}
	clean := LintDegradeTemplate("en",
		"Degraded: reranker offline ({reason}) — results unranked. Fix: carina doctor",
		[]string{"reason"})
	if len(clean) != 0 {
		t.Errorf("clean degrade template flagged: %v", clean)
	}
}

func anyContains(violations []string, sub string) bool {
	for _, v := range violations {
		if strings.Contains(v, sub) {
			return true
		}
	}
	return false
}
