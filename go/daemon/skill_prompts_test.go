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

	catalog, requested := buildDynamicSkillPrompt(ws, "Use $pdf to inspect the report", builtinCommandSpecs(), false)
	if !strings.Contains(requested, "- skill://pdf (explicit)") {
		t.Fatalf("explicit skill should request skill://, got:\n%s", requested)
	}
	if strings.Contains(catalog, "REQUESTED SKILLS") || strings.Contains(catalog, "SKILL WARNING") {
		t.Fatalf("catalog must not carry turn-local requested skills:\n%s", catalog)
	}
	got := joinPromptPrefix(catalog, requested)
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

	offCatalog, offRequested := buildDynamicSkillPrompt(ws, "Please perform a security audit", nil, false)
	if strings.Contains(joinPromptPrefix(offCatalog, offRequested), "SECURITY BODY") || strings.Contains(offRequested, "skill://security") {
		t.Fatal("implicit skill must be disabled unless the operator opts in")
	}
	t.Setenv(implicitSkillsEnv, "true")
	onCatalog, onRequested := buildDynamicSkillPrompt(ws, "Please perform a security audit", nil, false)
	if onCatalog != offCatalog {
		t.Fatal("implicit opt-in must not change the stable skill catalog")
	}
	if !strings.Contains(onRequested, "- skill://security (implicit)") {
		t.Fatalf("strict declared trigger should request skill://:\n%s", onRequested)
	}
	if strings.Contains(onRequested, "SECURITY BODY") {
		t.Fatalf("implicit trigger must not inline the skill body:\n%s", onRequested)
	}
	_, noMatch := buildDynamicSkillPrompt(ws, "Please review authentication", nil, false)
	if strings.Contains(noMatch, "SECURITY BODY") || strings.Contains(noMatch, "skill://security") {
		t.Fatal("descriptions and when-to-use prose must not cause fuzzy implicit injection")
	}
}

func TestDynamicSkillPromptDisabledAndSafeModeFailClosed(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "deploy", "description: Deploy service\n", "DEPLOY BODY")
	t.Setenv(disabledSkillsEnv, "deploy")

	disabledCatalog, disabledRequested := buildDynamicSkillPrompt(ws, "Run $deploy now", nil, false)
	disabled := joinPromptPrefix(disabledCatalog, disabledRequested)
	if strings.Contains(disabled, "DEPLOY BODY") || strings.Contains(disabled, "skill://deploy") {
		t.Fatalf("disabled skill leaked into prompt:\n%s", disabled)
	}
	if strings.Contains(disabledCatalog, "SKILL WARNING") {
		t.Fatalf("SKILL WARNING must not live in the catalog:\n%s", disabledCatalog)
	}
	if !strings.Contains(disabledRequested, "SKILL WARNING") {
		t.Fatalf("explicit unavailable skill should produce a visible warning:\n%s", disabledRequested)
	}

	t.Setenv(disabledSkillsEnv, "")
	safeCatalog, safeRequested := buildDynamicSkillPrompt(ws, "Run $deploy now", nil, true)
	safe := joinPromptPrefix(safeCatalog, safeRequested)
	if strings.Contains(safe, "DEPLOY BODY") || !strings.Contains(safeRequested, "SKILL WARNING") {
		t.Fatalf("safe mode must fail closed with a visible warning:\n%s", safe)
	}
	if strings.Contains(safeCatalog, "SKILL WARNING") {
		t.Fatalf("safe-mode warning must not live in the catalog:\n%s", safeCatalog)
	}
}

func TestDynamicSkillPromptBudgetAndOrderingAreDeterministic(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "zeta", "description: Zeta\n", strings.Repeat("Z", maxSkillPromptBytes))
	writeProjectSkill(t, ws, "alpha", "description: Alpha\n", strings.Repeat("A", maxSkillPromptBytes))

	oneCatalog, oneRequested := buildDynamicSkillPrompt(ws, "Use $zeta and $alpha", builtinCommandSpecs(), false)
	twoCatalog, twoRequested := buildDynamicSkillPrompt(ws, "Use $zeta and $alpha", builtinCommandSpecs(), false)
	if oneCatalog != twoCatalog || oneRequested != twoRequested {
		t.Fatal("same workspace and task must produce a byte-identical skill prompt")
	}
	one := joinPromptPrefix(oneCatalog, oneRequested)
	if len(oneCatalog) > maxSkillPromptBytes || len(oneRequested) > maxSkillPromptBytes {
		t.Fatalf("skill prompt exceeded budget: catalog=%d requested=%d want <= %d", len(oneCatalog), len(oneRequested), maxSkillPromptBytes)
	}
	if strings.Index(oneRequested, "skill://alpha") < 0 || strings.Index(oneRequested, "skill://zeta") < 0 {
		t.Fatalf("explicit skills should be represented within the bounded prompt:\n%s", one)
	}
	if strings.Index(oneRequested, "skill://alpha") > strings.Index(oneRequested, "skill://zeta") {
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
	catalog, requested := buildDynamicSkillPrompt(ws, "ordinary task", commands, false)
	if requested != "" {
		t.Fatalf("ordinary task must not request skills:\n%s", requested)
	}
	if !strings.Contains(catalog, "[skill catalog truncated:") || !strings.Contains(catalog, " omitted]") {
		t.Fatalf("catalog budget overflow must be visible and deterministic:\n%s", catalog)
	}
	if len(catalog) > maxSkillPromptBytes {
		t.Fatalf("catalog overflow exceeded total prompt budget: %d", len(catalog))
	}
}

func TestDynamicSkillPromptNoMatchKeepsBodiesOut(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "release", "description: Release workflow\n", "RELEASE BODY")

	catalog, requested := buildDynamicSkillPrompt(ws, "Explain the parser", builtinCommandSpecs(), false)
	got := joinPromptPrefix(catalog, requested)
	if requested != "" {
		t.Fatalf("unmatched prompt must not emit REQUESTED skills:\n%s", requested)
	}
	if strings.Contains(got, "RELEASE BODY") || strings.Contains(got, "SELECTED SKILL INSTRUCTIONS") {
		t.Fatalf("unmatched skill body leaked into prompt:\n%s", got)
	}
	if !strings.Contains(catalog, "- skill://release") || !strings.Contains(catalog, "- command /review") {
		t.Fatalf("bounded metadata catalogs should remain discoverable:\n%s", catalog)
	}
	if strings.Contains(catalog, "skill://review") {
		t.Fatalf("catalog must not advertise /review as skill://review:\n%s", catalog)
	}
}

func TestDynamicSkillPromptLivesInStablePrefix(t *testing.T) {
	ws := isolatedSkillWorkspace(t)
	writeProjectSkill(t, ws, "test", "description: Test changes\n", "TEST SKILL BODY")
	catalog, requested := buildDynamicSkillPrompt(ws, "Use $test", builtinCommandSpecs(), false)
	if !strings.Contains(catalog, "- skill://test") || strings.Contains(catalog, "REQUESTED SKILLS") {
		t.Fatalf("catalog must be the skill index only:\n%s", catalog)
	}
	if !strings.Contains(requested, "- skill://test (explicit)") {
		t.Fatalf("explicit $test must live in requested:\n%s", requested)
	}

	layers := promptLayers{Constitution: "SYSTEM", Catalog: catalog, Requested: requested}
	a := buildPromptSegmentsFromLayers(layers, "Use $test", "turn one", "NEXT")
	b := buildPromptSegmentsFromLayers(layers, "Use $test", "turn one\nturn two", "NEXT")
	if a.StablePrefix != b.StablePrefix {
		t.Fatal("skill catalog must remain byte-identical in the stable prefix across turns")
	}
	if !strings.Contains(a.StablePrefix, "skill://test") || strings.Contains(a.StablePrefix, "TEST SKILL BODY") {
		t.Fatal("catalog must live in the stable prefix without inlining the body")
	}
	if strings.Contains(a.StablePrefix, "REQUESTED SKILLS") || strings.Contains(a.StablePrefix, "(explicit)") {
		t.Fatal("REQUESTED skills must not live in the cacheable prefix")
	}
	if strings.Contains(a.VolatileSuffix, "TEST SKILL BODY") {
		t.Fatal("skill body must not leak into the volatile suffix")
	}
	if !strings.Contains(a.VolatileSuffix, "REQUESTED SKILLS") || !strings.Contains(a.VolatileSuffix, "- skill://test (explicit)") {
		t.Fatal("REQUESTED skills belong in the volatile suffix")
	}
	for _, section := range a.CacheSections() {
		if strings.Contains(section, "REQUESTED SKILLS") || strings.Contains(section, "(explicit)") {
			t.Fatalf("CacheSections leaked REQUESTED skills: %q", section)
		}
	}
}

func TestSkillCatalogCacheIndependentOfGreetingWording(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(implicitSkillsEnv, "false")
	t.Setenv(disabledSkillsEnv, "")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeProjectSkill(t, ws, "pdf", "description: Work with PDF files\n", "PDF BODY")
	sess, _ := d.store.CreateSession(ws, "safe-edit")

	hi := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	use := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "Use $pdf")
	hiLayers := d.composeAgentPromptLayers(sess, hi, "")
	useLayers := d.composeAgentPromptLayers(sess, use, "")
	if hiLayers.Catalog != useLayers.Catalog {
		t.Fatal("skill catalog must be independent of greeting wording")
	}
	if strings.Contains(hiLayers.Catalog, "REQUESTED SKILLS") || strings.Contains(hiLayers.Catalog, "SKILL WARNING") {
		t.Fatalf("catalog must not carry turn-local requested skills:\n%s", hiLayers.Catalog)
	}
	if hiLayers.Requested != "" {
		t.Fatalf("greeting must not request skills: %q", hiLayers.Requested)
	}
	if !strings.Contains(useLayers.Requested, "- skill://pdf (explicit)") {
		t.Fatalf("$pdf must request skill://pdf:\n%s", useLayers.Requested)
	}
	if strings.Contains(useLayers.Requested, "PDF BODY") {
		t.Fatal("requested list must not inline the skill body")
	}

	hiSeg := buildPromptSegmentsFromLayers(hiLayers, hi.UserPrompt, "turn1", "GO")
	useSeg := buildPromptSegmentsFromLayers(useLayers, use.UserPrompt, "turn1", "GO")
	if strings.Join(hiSeg.CacheSections(), "\n\n") != strings.Join(useSeg.CacheSections(), "\n\n") {
		t.Fatal("CacheSections must be independent of greeting wording")
	}
	if hiSeg.StablePrefix != useSeg.StablePrefix {
		t.Fatal("stable prefix must be identical for hi and Use $pdf")
	}
	if strings.Contains(strings.Join(useSeg.CacheSections(), "\n"), "REQUESTED SKILLS") {
		t.Fatal("CacheSections must omit REQUESTED skills")
	}
	if !strings.Contains(useSeg.VolatileSuffix, "REQUESTED SKILLS") || !strings.Contains(useSeg.VolatileSuffix, "TASK: Use $pdf") {
		t.Fatalf("requested skills belong in the suffix before TASK:\n%s", useSeg.VolatileSuffix)
	}
	if strings.Contains(hiSeg.VolatileSuffix, "REQUESTED SKILLS") {
		t.Fatal("greeting suffix must not request skills")
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
	catalog, requested := buildDynamicSkillPrompt(ws, "Use $broken", nil, false)
	if strings.Contains(catalog, "SKILL WARNING") {
		t.Fatalf("malformed warning must not live in the catalog:\n%s", catalog)
	}
	if !strings.Contains(requested, "SKILL WARNING") || strings.Contains(requested, "BROKEN") || strings.Contains(catalog, "BROKEN") {
		t.Fatalf("malformed explicit skill must fail closed with warning:\n%s\n%s", catalog, requested)
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

func TestReadSkillURIAliasesSlashCommand(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
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
