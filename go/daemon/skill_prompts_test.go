package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProjectSkill(t *testing.T, workspace, name, frontmatter, body string) {
	t.Helper()
	dir := filepath.Join(workspace, ".carina", "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\n" + frontmatter + "---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func isolatedSkillWorkspace(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(implicitSkillsEnv, "false")
	t.Setenv(disabledSkillsEnv, "")
	return t.TempDir()
}

func TestDynamicSkillPromptExplicitMentionRequestsURI(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "pdf", "description: Work with PDF files\ndisable-model-invocation: true\n", "EXPLICIT PDF BODY")

	got := buildDynamicSkillPrompt(ws, "Use $pdf to inspect the report", builtinCommandSpecs(), false)
	if !strings.Contains(got, "- skill://pdf (explicit)") {
		t.Fatalf("explicit skill should request skill://, got:\n%s", got)
	}
	if strings.Contains(got, "EXPLICIT PDF BODY") || strings.Contains(got, "SELECTED SKILL INSTRUCTIONS") {
		t.Fatalf("explicit mention must not inline the skill body:\n%s", got)
	}
	if strings.Contains(got, "- skill://pdf:") {
		t.Fatal("disable-model-invocation skill must not appear in the model-facing catalog")
	}
}

func TestDynamicSkillPromptImplicitInvocationIsStrictAndControllable(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "security", "description: Security review\nimplicit-invocation: true\ntriggers: [threat model, security audit]\n", "SECURITY BODY")

	off := buildDynamicSkillPrompt(ws, "Please perform a security audit", nil, false)
	if strings.Contains(off, "SECURITY BODY") {
		t.Fatal("implicit skill must be disabled unless the operator opts in")
	}
	t.Setenv(implicitSkillsEnv, "true")
	on := buildDynamicSkillPrompt(ws, "Please perform a security audit", nil, false)
	if !strings.Contains(on, "- skill://security (implicit)") {
		t.Fatalf("strict declared trigger should request skill://:\n%s", on)
	}
	if strings.Contains(on, "SECURITY BODY") {
		t.Fatalf("implicit trigger must not inline the skill body:\n%s", on)
	}
	noMatch := buildDynamicSkillPrompt(ws, "Please review authentication", nil, false)
	if strings.Contains(noMatch, "SECURITY BODY") {
		t.Fatal("descriptions and when-to-use prose must not cause fuzzy implicit injection")
	}
}

func TestDynamicSkillPromptDisabledAndSafeModeFailClosed(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "deploy", "description: Deploy service\n", "DEPLOY BODY")
	t.Setenv(disabledSkillsEnv, "deploy")

	disabled := buildDynamicSkillPrompt(ws, "Run $deploy now", nil, false)
	if strings.Contains(disabled, "DEPLOY BODY") || strings.Contains(disabled, "skill://deploy") {
		t.Fatalf("disabled skill leaked into prompt:\n%s", disabled)
	}
	if !strings.Contains(disabled, "SKILL WARNING") {
		t.Fatalf("explicit unavailable skill should produce a visible warning:\n%s", disabled)
	}

	t.Setenv(disabledSkillsEnv, "")
	safe := buildDynamicSkillPrompt(ws, "Run $deploy now", nil, true)
	if strings.Contains(safe, "DEPLOY BODY") || !strings.Contains(safe, "SKILL WARNING") {
		t.Fatalf("safe mode must fail closed with a visible warning:\n%s", safe)
	}
}

func TestDynamicSkillPromptBudgetAndOrderingAreDeterministic(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "zeta", "description: Zeta\n", strings.Repeat("Z", maxSkillPromptBytes))
	writeProjectSkill(t, ws, "alpha", "description: Alpha\n", strings.Repeat("A", maxSkillPromptBytes))

	one := buildDynamicSkillPrompt(ws, "Use $zeta and $alpha", builtinCommandSpecs(), false)
	two := buildDynamicSkillPrompt(ws, "Use $zeta and $alpha", builtinCommandSpecs(), false)
	if one != two {
		t.Fatal("same workspace and task must produce a byte-identical skill prompt")
	}
	if len(one) > maxSkillPromptBytes {
		t.Fatalf("skill prompt exceeded budget: got %d want <= %d", len(one), maxSkillPromptBytes)
	}
	if strings.Index(one, "skill://alpha") < 0 || strings.Index(one, "skill://zeta") < 0 {
		t.Fatalf("explicit skills should be represented within the bounded prompt:\n%s", one)
	}
	if strings.Index(one, "skill://alpha") > strings.Index(one, "skill://zeta") {
		t.Fatal("same-priority selected skills must sort by canonical name")
	}
	if strings.Contains(one, strings.Repeat("Z", 80)) || strings.Contains(one, strings.Repeat("A", 80)) {
		t.Fatal("requested-skill list must not inline skill bodies")
	}
}

func TestDynamicSkillCatalogReportsOmittedEntries(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	commands := map[string]*CommandSpec{}
	for i := 0; i < 100; i++ {
		name := "command-" + strings.Repeat("x", 20) + string(rune('a'+i%26)) + string(rune('a'+(i/26)))
		commands[name] = &CommandSpec{Name: name, Description: strings.Repeat("description ", 20), Source: "project"}
	}
	got := buildDynamicSkillPrompt(ws, "ordinary task", commands, false)
	if !strings.Contains(got, "[skill catalog truncated:") || !strings.Contains(got, " omitted]") {
		t.Fatalf("catalog budget overflow must be visible and deterministic:\n%s", got)
	}
	if len(got) > maxSkillPromptBytes {
		t.Fatalf("catalog overflow exceeded total prompt budget: %d", len(got))
	}
}

func TestDynamicSkillPromptNoMatchKeepsBodiesOut(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "release", "description: Release workflow\n", "RELEASE BODY")

	got := buildDynamicSkillPrompt(ws, "Explain the parser", builtinCommandSpecs(), false)
	if strings.Contains(got, "RELEASE BODY") || strings.Contains(got, "SELECTED SKILL INSTRUCTIONS") {
		t.Fatalf("unmatched skill body leaked into prompt:\n%s", got)
	}
	if !strings.Contains(got, "- skill://release") || !strings.Contains(got, "- command /review") {
		t.Fatalf("bounded metadata catalogs should remain discoverable:\n%s", got)
	}
}

func TestDynamicSkillPromptLivesInStablePrefix(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "test", "description: Test changes\n", "TEST SKILL BODY")
	skillPrompt := buildDynamicSkillPrompt(ws, "Use $test", builtinCommandSpecs(), false)
	sys := "SYSTEM\n\n" + skillPrompt

	a := buildPromptSegments(sys, "Use $test", "turn one", "NEXT")
	b := buildPromptSegments(sys, "Use $test", "turn one\nturn two", "NEXT")
	if a.StablePrefix != b.StablePrefix {
		t.Fatal("skill prompt must remain byte-identical in the stable prefix across turns")
	}
	if !strings.Contains(a.StablePrefix, "skill://test") || strings.Contains(a.StablePrefix, "TEST SKILL BODY") {
		t.Fatal("catalog/request list must live in the stable prefix without inlining the body")
	}
	if strings.Contains(a.VolatileSuffix, "TEST SKILL BODY") || strings.Contains(a.VolatileSuffix, "skill://test") {
		t.Fatal("skill catalog must not leak into the volatile suffix")
	}
}

func TestSkillSlashCommandDoesNotOverrideExistingCommand(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "review", "description: Skill review\n", "SHOULD NOT WIN")
	writeProjectSkill(t, ws, "format", "description: Format files\n", "FORMAT BODY")
	d := &Daemon{}

	specs := d.commandSpecs(ws)
	if specs["review"] == nil || specs["review"].Source != "built-in" {
		t.Fatalf("existing slash command must win a name collision: %+v", specs["review"])
	}
	if specs["format"] == nil || specs["format"].Source != "skill" {
		t.Fatalf("unambiguous user-invocable skill should join slash discovery: %+v", specs["format"])
	}
	expanded, ok, err := expandSlashCommand("/format src", specs)
	if err != nil || !ok || !strings.Contains(expanded.Prompt, "FORMAT BODY") || !strings.Contains(expanded.Prompt, "src") {
		t.Fatalf("skill slash expansion failed: expanded=%+v ok=%v err=%v", expanded, ok, err)
	}
}

func TestMalformedExplicitSkillWarnsInsteadOfPanicking(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	dir := filepath.Join(ws, ".carina", "skills", "broken")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nenabled: maybe\n---\nBROKEN"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := buildDynamicSkillPrompt(ws, "Use $broken", nil, false)
	if !strings.Contains(got, "SKILL WARNING") || strings.Contains(got, "BROKEN") {
		t.Fatalf("malformed explicit skill must fail closed with warning:\n%s", got)
	}
}

func TestParseSkillURIRejectsTraversalAndNoise(t *testing.T) {
	if name, ok := parseSkillURI("skill://pdf"); !ok || name != "pdf" {
		t.Fatalf("plain skill URI = %q %v", name, ok)
	}
	if name, ok := parseSkillURI("SKILL://Release"); !ok || name != "release" {
		t.Fatalf("canonical skill URI = %q %v", name, ok)
	}
	for _, raw := range []string{
		"pdf", "skill:", "skill://", "skill:///etc/passwd", "skill://../secret",
		"skill://foo/bar", "skill://foo?x=1", "skill://foo#h", `skill://foo\bar`,
	} {
		if name, ok := parseSkillURI(raw); ok {
			t.Fatalf("rejected URI %q parsed as %q", raw, name)
		}
	}
}

func TestCollectExplicitMentionsIncludeSkillURI(t *testing.T) {
	got := collectExplicitSkillMentions("please follow skill://pdf and $deploy")
	if !got["pdf"] || !got["deploy"] {
		t.Fatalf("mentions = %v", got)
	}
}

func TestReadSkillURILoadsBodyOnDemand(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeProjectSkill(t, ws, "pdf", "description: Work with PDF files\n", "ON_DEMAND PDF BODY")
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"read","path":"skill://pdf"}`,
		`{"tool":"done","summary":"loaded the pdf skill"}`,
	}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "Use $pdf")
	d.runTask(sess, task)
	tk, _ := d.sched.Get(task.RunID)
	if tk.Status != "completed" {
		t.Fatalf("status=%s reason=%q", tk.Status, tk.Summary)
	}
	cp := d.runs.loadCheckpoint(task.RunID)
	if cp == nil || cp.Transcript == nil {
		t.Fatal("missing checkpoint")
	}
	found := false
	for _, turn := range cp.Transcript.Turns {
		if strings.Contains(turn.Obs.Content, "ON_DEMAND PDF BODY") && strings.Contains(turn.Obs.Content, `invocation="on_demand"`) {
			found = true
		}
		if strings.Contains(turn.Obs.Content, "ON_DEMAND PDF BODY") && turn.Tool == "system" {
			t.Fatal("skill body must not be injected as a system turn")
		}
	}
	if !found {
		t.Fatalf("on-demand skill body missing from read observation: %+v", cp.Transcript.Turns)
	}
}

func TestReadSkillURIFailsClosed(t *testing.T) {
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

	t.Setenv(disabledSkillsEnv, "pdf")
	out = d.readSkillURI(sess, task, "skill://pdf")
	if out.status == "completed" || strings.Contains(out.display, "SECRET BODY") {
		t.Fatalf("disabled skill must fail closed: %+v", out)
	}
	t.Setenv(disabledSkillsEnv, "")

	d.safeMode = true
	out = d.readSkillURI(sess, task, "skill://pdf")
	if out.status == "completed" || strings.Contains(out.display, "SECRET BODY") {
		t.Fatalf("safe mode must fail closed: %+v", out)
	}
}
