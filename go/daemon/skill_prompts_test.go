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

func TestDynamicSkillPromptExplicitMentionLoadsBody(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "pdf", "description: Work with PDF files\ndisable-model-invocation: true\n", "EXPLICIT PDF BODY")

	got := buildDynamicSkillPrompt(ws, "Use $pdf to inspect the report", builtinCommandSpecs(), false)
	for _, want := range []string{`name="pdf"`, `invocation="explicit"`, "EXPLICIT PDF BODY"} {
		if !strings.Contains(got, want) {
			t.Fatalf("explicit skill prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- skill $pdf") {
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
	if !strings.Contains(on, `invocation="implicit"`) || !strings.Contains(on, "SECURITY BODY") {
		t.Fatalf("strict declared trigger should inject skill body:\n%s", on)
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
	if strings.Contains(disabled, "DEPLOY BODY") || strings.Contains(disabled, "- skill $deploy") {
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
	if strings.Index(one, `name="alpha"`) < 0 || strings.Index(one, `name="zeta"`) < 0 {
		t.Fatalf("explicit skills should be represented within the bounded prompt:\n%s", one)
	}
	if strings.Index(one, `name="alpha"`) > strings.Index(one, `name="zeta"`) {
		t.Fatal("same-priority selected skills must sort by canonical name")
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
	if !strings.Contains(got, "- skill $release") || !strings.Contains(got, "- command /review") {
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
	if !strings.Contains(a.StablePrefix, "TEST SKILL BODY") || strings.Contains(a.VolatileSuffix, "TEST SKILL BODY") {
		t.Fatal("selected skill body must live only in the stable prompt segment")
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
