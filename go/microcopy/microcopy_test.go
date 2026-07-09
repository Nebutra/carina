package microcopy

import (
	"os"
	"strings"
	"testing"
)

// scrubLocaleEnv unsets every locale-relevant variable so hermetic tests are
// not polluted by the host or CI job environment.
func scrubLocaleEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"CARINA_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if val, ok := os.LookupEnv(key); ok {
			t.Setenv(key, val) // registers restore-on-cleanup
			os.Unsetenv(key)
		}
	}
}

// TestLoadingDeterministic: same seed + locale + pool version always yields
// the same line — spinner text is snapshot-testable.
func TestLoadingDeterministic(t *testing.T) {
	seeds := []string{"code.search", "kernel.patch.apply", "session.resume",
		"task.action.approve", "workspace.patch.propose", "audit.verify",
		"command.exec", "totally.unknown.method", ""}
	for _, locale := range []string{"en", "zh"} {
		for _, seed := range seeds {
			a := Loading(seed, WithLocale(locale), WithPoolVersion(1))
			b := Loading(seed, WithLocale(locale), WithPoolVersion(1))
			if a != b {
				t.Errorf("Loading(%q, %s) not deterministic: %q vs %q", seed, locale, a, b)
			}
			if a == "" {
				t.Errorf("Loading(%q, %s) returned empty line", seed, locale)
			}
		}
	}
}

// TestLoadingContextRouting: ordered regex patterns over wire-format seeds
// resolve to Carina-native context pools; unmatched seeds fall to generic.
// The approval context deliberately has no Ambient pool (permission moments
// are Governed territory), so approval seeds fall through to generic.
func TestLoadingContextRouting(t *testing.T) {
	cases := []struct {
		seed    string
		context string
	}{
		{"code.search", "codeintel"},
		{"code.refs", "codeintel"},
		{"kernel.patch.apply", "patch"},
		{"workspace.patch.propose", "patch"},
		{"session.fork", "session"},
		{"audit.verify", "audit"},
		{"audit.chain.export", "audit"},
		{"task.action.approve", "generic"}, // approval → no ambient pool → generic
		{"command.exec", "terminal"},
		{"model.route", "provider"},
		{"git.log", "git"},
		{"file.read", "file"},
		{"kernel.handshake", "kernel"},
		{"policy.snapshot", "policy"},
		{"agent.turn", "agent"},
		{"zzz.unmatched", "generic"},
	}
	for _, locale := range []string{"en", "zh"} {
		for _, tc := range cases {
			got := Loading(tc.seed, WithLocale(locale))
			pool := registry.ambientPool(locale, tc.context)
			if len(pool) == 0 {
				t.Fatalf("no ambient pool for %s/%s", locale, tc.context)
			}
			if !containsLine(pool, got) {
				t.Errorf("Loading(%q, %s) = %q, not in %s pool %v", tc.seed, locale, got, tc.context, pool)
			}
		}
	}
}

// TestLoadingOverridePattern: seed-specific regex overrides win over the
// context pool (override chain: override > context > generic).
func TestLoadingOverridePattern(t *testing.T) {
	en := Loading("session.resume", WithLocale("en"))
	if en != "session resumed. everything is where you left it, and we can prove it." {
		t.Errorf("session.resume en override not applied, got %q", en)
	}
	zh := Loading("session.resume", WithLocale("zh"))
	if zh != "会话已恢复。原样奉还，且有据可查。" {
		t.Errorf("session.resume zh override not applied, got %q", zh)
	}
}

// TestLoadingLocaleNormalization: zh*/en* prefixes normalize; everything
// else falls back to en.
func TestLoadingLocaleNormalization(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"zh", "zh"},
		{"zh-CN", "zh"},
		{"zh_TW.UTF-8", "zh"},
		{"ZH_HK", "zh"},
		{"en", "en"},
		{"en_US.UTF-8", "en"},
		{"fr_FR", "en"},
		{"ja_JP", "en"},
		{"", "en"},
		{"C", "en"},
	}
	for _, tc := range cases {
		if got := NormalizeLocale(tc.raw); got != tc.want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	// End-to-end: a zh-CN caller draws from the zh pools.
	got := Loading("zzz.unmatched", WithLocale("zh-CN"))
	if !containsLine(registry.ambientPool("zh", "generic"), got) {
		t.Errorf("zh-CN did not resolve to zh generic pool, got %q", got)
	}
}

// TestLoadingSessionSalt: WithSession salts the seed mechanically as
// seed + "|" + sessionID — per-session variety, within-session stability.
func TestLoadingSessionSalt(t *testing.T) {
	seed := "command.exec"
	salted := Loading(seed, WithLocale("en"), WithSession("sess-42"))
	manual := Loading(seed+"|sess-42", WithLocale("en"))
	if salted != manual {
		t.Errorf("session salt mismatch: %q vs %q", salted, manual)
	}
	if a, b := Loading(seed, WithLocale("en"), WithSession("s1")),
		Loading(seed, WithLocale("en"), WithSession("s1")); a != b {
		t.Errorf("within-session instability: %q vs %q", a, b)
	}
}

// TestLoadingPlainSuppression: under --json/--plain/!isatty the Ambient
// register collapses to a bare verb; Governed and Degrade still render.
func TestLoadingPlainSuppression(t *testing.T) {
	if got := Loading("code.search", WithLocale("en"), WithPlain(true)); got != "Loading..." {
		t.Errorf("plain Loading = %q, want %q", got, "Loading...")
	}
	if got := Loading("code.search", WithLocale("zh"), WithPlain(true)); got != "Loading..." {
		t.Errorf("plain zh Loading = %q, want %q", got, "Loading...")
	}
	gov := Governed(GovernedPolicyDenied, Args{"rule_id": "r", "action": "a", "audit_id": "x"}, WithPlain(true))
	if gov == "" || gov == "Loading..." {
		t.Errorf("Governed must still render under plain, got %q", gov)
	}
	deg := Degrade(DegradeTimedOut, Args{"seconds": "30", "action": "a"}, WithPlain(true))
	if deg == "" {
		t.Error("Degrade must still render under plain")
	}
}

// TestLoadingGolden pins exact picks so any pool or hash change is a
// deliberate, reviewed event (pool version 1).
func TestLoadingGolden(t *testing.T) {
	cases := []struct {
		seed   string
		locale string
		want   string
	}{
		{"code.search", "en", "cache hit. we have, in fact, been here before."},
		{"code.search", "zh", "缓存命中。此路我们走过。"},
		{"kernel.patch.apply", "en", "lining up hunks in order."},
		{"kernel.patch.apply", "zh", "补丁对齐中。量两次，切一次。"},
	}
	for _, tc := range cases {
		if got := Loading(tc.seed, WithLocale(tc.locale), WithPoolVersion(1)); got != tc.want {
			t.Errorf("golden Loading(%q, %s) = %q, want %q", tc.seed, tc.locale, got, tc.want)
		}
	}
}

// TestGovernedTemplates: slot-filled constant templates with named
// placeholders, en and zh.
func TestGovernedTemplates(t *testing.T) {
	cases := []struct {
		code   Code
		args   Args
		locale string
		want   []string // substrings that must appear
	}{
		{GovernedApprovalRequired,
			Args{"action": "write", "path": "~/.ssh/config", "decision_id": "perm_18c08fbe"},
			"en", []string{"Approval required", "write", "~/.ssh/config", "perm_18c08fbe"}},
		{GovernedApprovalRequired,
			Args{"action": "写入", "path": "~/.ssh/config", "decision_id": "perm_18c08fbe"},
			"zh", []string{"需要授权", "写入", "~/.ssh/config", "perm_18c08fbe"}},
		{GovernedPolicyDenied,
			Args{"rule_id": "net.outbound-allowlist", "action": "connect 203.0.113.7:443", "audit_id": "a8f2c1"},
			"en", []string{"Denied by policy net.outbound-allowlist", "a8f2c1"}},
		{GovernedPolicyDenied,
			Args{"rule_id": "net.outbound-allowlist", "action": "连接 203.0.113.7:443", "audit_id": "a8f2c1"},
			"zh", []string{"已被策略 net.outbound-allowlist 拒绝", "a8f2c1"}},
		{GovernedRollbackComplete,
			Args{"count": "12", "checkpoint_id": "7f3d9e"},
			"en", []string{"Rollback complete", "12", "7f3d9e"}},
		{GovernedAuditVerified,
			Args{"count": "1,204", "head": "9c4b…e21a"},
			"zh", []string{"审计链校验通过", "1,204", "9c4b…e21a"}},
		{GovernedEgressConfirm,
			Args{"subject": "file contents", "endpoint": "api.example.com"},
			"en", []string{"external endpoint", "api.example.com", "Proceed?"}},
	}
	for _, tc := range cases {
		got := Governed(tc.code, tc.args, WithLocale(tc.locale))
		for _, sub := range tc.want {
			if !strings.Contains(got, sub) {
				t.Errorf("Governed(%s, %s) = %q, missing %q", tc.code, tc.locale, got, sub)
			}
		}
		for k := range tc.args {
			if strings.Contains(got, "{"+k+"}") {
				t.Errorf("Governed(%s, %s) left placeholder {%s}: %q", tc.code, tc.locale, k, got)
			}
		}
	}
}

// TestGovernedMissingArgObservable: a missing argument stays visible as the
// literal placeholder — never silently swallowed.
func TestGovernedMissingArgObservable(t *testing.T) {
	got := Governed(GovernedApprovalRequired, Args{"action": "write"}, WithLocale("en"))
	if !strings.Contains(got, "{path}") || !strings.Contains(got, "{decision_id}") {
		t.Errorf("missing args must remain visible, got %q", got)
	}
}

// TestGovernedLocaleParity: every governed code has an en and a zh template,
// authored (never byte-identical unless placeholder-only content).
func TestGovernedLocaleParity(t *testing.T) {
	for code, tmpl := range registry.governed {
		en, zh := tmpl.Text["en"], tmpl.Text["zh"]
		if en == "" || zh == "" {
			t.Errorf("governed %s missing a locale (en=%q zh=%q)", code, en, zh)
		}
		if en == zh {
			t.Errorf("governed %s zh template is a copy of en: %q", code, en)
		}
	}
}

// TestDegradeTemplates: calm-factual + remedy lines with glyph prefixes per
// the four-glyph status vocabulary.
func TestDegradeTemplates(t *testing.T) {
	cases := []struct {
		status DegradeStatus
		args   Args
		locale string
		want   []string
		prefix string
	}{
		{DegradeInterruptedByUser, nil, "en", []string{"Stopped by you"}, "~ "},
		{DegradeInterruptedByPolicy, Args{"rule_id": "fs.deny-dotfiles"}, "en",
			[]string{"Stopped by policy fs.deny-dotfiles", "audit chain"}, "~ "},
		{DegradeInterruptedByAgent, Args{"agent_id": "agent-7"}, "zh",
			[]string{"已被代理 agent-7 停止"}, "~ "},
		{DegradeTimedOut, Args{"seconds": "30", "action": "code.search"}, "en",
			[]string{"Timed out after 30s", "carina doctor"}, "~ "},
		{DegradeBackgrounded, Args{"task_id": "t-9"}, "en",
			[]string{"background", "carina watch t-9"}, "~ "},
		{DegradeConflict, Args{"path": "go/daemon/agent.go"}, "en",
			[]string{"Conflict", "go/daemon/agent.go", "Nothing was overwritten"}, "~ "},
		{DegradeDone, nil, "en", []string{"Done"}, "✓ "},
		{DegradeDone, nil, "zh", []string{"完成"}, "✓ "},
		{DegradePartial, Args{"applied": "3", "total": "5", "tx_id": "41"}, "en",
			[]string{"3 of 5", "carina log --tx 41"}, "~ "},
		{DegradeDaemonUnreachable, Args{"socket": "/tmp/carina.sock"}, "en",
			[]string{"Daemon unreachable", "/tmp/carina.sock", "carina-daemon &"}, "~ "},
		{DegradeSemanticOff, Args{"reason": "embedder-offline"}, "en",
			[]string{"semantic search offline", "embedder-offline", "carina doctor"}, "~ "},
		{DegradeRerankOff, Args{"reason": "rerank-error"}, "en",
			[]string{"reranker offline", "results unranked", "carina doctor"}, "~ "},
		{DegradeLookupImprecise, Args{"tool": "code.def", "reason": "lsp-handshake-failed"}, "zh",
			[]string{"code.def", "tree-sitter", "lsp-handshake-failed", "carina doctor"}, "~ "},
	}
	for _, tc := range cases {
		got := Degrade(tc.status, tc.args, WithLocale(tc.locale))
		if !strings.HasPrefix(got, tc.prefix) {
			t.Errorf("Degrade(%s, %s) = %q, want prefix %q", tc.status, tc.locale, got, tc.prefix)
		}
		for _, sub := range tc.want {
			if !strings.Contains(got, sub) {
				t.Errorf("Degrade(%s, %s) = %q, missing %q", tc.status, tc.locale, got, sub)
			}
		}
	}
}

// TestDegradePlainStripsGlyph: pipe consumers get precise degrade text with
// glyphs stripped under plain/NO_COLOR.
func TestDegradePlainStripsGlyph(t *testing.T) {
	rich := Degrade(DegradeDone, nil, WithLocale("en"))
	plain := Degrade(DegradeDone, nil, WithLocale("en"), WithPlain(true))
	if plain != "Done" {
		t.Errorf("plain Degrade(done) = %q, want %q", plain, "Done")
	}
	if rich == plain {
		t.Errorf("rich Degrade(done) should carry a glyph, got %q", rich)
	}
}

// TestDegradeCoversTaxonomy: every status constant of the P1.3 taxonomy has
// templates in both locales; the pool has no unknown statuses.
func TestDegradeCoversTaxonomy(t *testing.T) {
	all := []DegradeStatus{
		DegradeInterruptedByUser, DegradeInterruptedByPolicy, DegradeInterruptedByAgent,
		DegradeTimedOut, DegradeBackgrounded, DegradeConflict, DegradeDone,
		DegradePartial, DegradeDaemonUnreachable, DegradeSemanticOff,
		DegradeRerankOff, DegradeLookupImprecise,
	}
	seen := map[string]bool{}
	for _, s := range all {
		tmpl, ok := registry.degrade[string(s)]
		if !ok {
			t.Errorf("degrade status %s has no template", s)
			continue
		}
		if tmpl.Text["en"] == "" || tmpl.Text["zh"] == "" {
			t.Errorf("degrade status %s missing a locale", s)
		}
		seen[string(s)] = true
	}
	for key := range registry.degrade {
		if !seen[key] {
			t.Errorf("degrade pool key %q has no DegradeStatus constant", key)
		}
	}
}

// TestGovernedCoversConstants: bijection between Code constants and the
// governed pool keys, so a typo'd code fails tests, not production.
func TestGovernedCoversConstants(t *testing.T) {
	all := []Code{
		GovernedApprovalRequired, GovernedApprovalGranted, GovernedApprovalDenied,
		GovernedPolicyDenied, GovernedPolicyUpdated, GovernedDestructiveConfirm,
		GovernedRollbackComplete, GovernedCheckpointCreated, GovernedAuditVerified,
		GovernedSecretRedacted, GovernedEgressConfirm,
	}
	seen := map[string]bool{}
	for _, c := range all {
		if _, ok := registry.governed[string(c)]; !ok {
			t.Errorf("governed code %s has no template", c)
		}
		seen[string(c)] = true
	}
	for key := range registry.governed {
		if !seen[key] {
			t.Errorf("governed pool key %q has no Code constant", key)
		}
	}
}

// TestRegisterSegregation: the registers are separate types drawing from
// separate pools by construction — a Governed or Degrade rendering can never
// be a member of any Ambient pool, and vice versa.
func TestRegisterSegregation(t *testing.T) {
	filler := Args{}
	for _, tmpl := range registry.governed {
		for _, ph := range tmpl.Placeholders {
			filler[ph] = "x"
		}
	}
	for _, tmpl := range registry.degrade {
		for _, ph := range tmpl.Placeholders {
			filler[ph] = "x"
		}
	}
	for _, locale := range []string{"en", "zh"} {
		for code := range registry.governed {
			got := Governed(Code(code), filler, WithLocale(locale))
			if registry.isAmbientLine(got) {
				t.Errorf("Governed(%s, %s) returned an Ambient pool member: %q", code, locale, got)
			}
		}
		for status := range registry.degrade {
			got := Degrade(DegradeStatus(status), filler, WithLocale(locale))
			if registry.isAmbientLine(got) {
				t.Errorf("Degrade(%s, %s) returned an Ambient pool member: %q", status, locale, got)
			}
		}
	}
	// And the other direction: no ambient line ever matches a governed or
	// degrade template shape.
	for _, locale := range []string{"en", "zh"} {
		for context := range registry.ambient.Locales[locale] {
			for _, line := range registry.ambientPool(locale, context) {
				if matchesTemplate(line, registry.governed) || matchesTemplate(line, registry.degrade) {
					t.Errorf("ambient line %q collides with a template", line)
				}
			}
		}
	}
}

// TestIsMembership: renderers use Is to distinguish microcopy placeholder
// text from real content, mirroring the reference isLoadingMicrocopy.
func TestIsMembership(t *testing.T) {
	if !Is(Loading("code.search", WithLocale("en"))) {
		t.Error("Is should recognize an Ambient line")
	}
	if !Is(Loading("zzz", WithLocale("zh"))) {
		t.Error("Is should recognize a zh Ambient line")
	}
	if !Is(Loading("x", WithPlain(true))) {
		t.Error("Is should recognize the plain suppression line")
	}
	gov := Governed(GovernedAuditVerified, Args{"count": "5", "head": "abc"}, WithLocale("en"))
	if !Is(gov) {
		t.Errorf("Is should recognize a rendered Governed line: %q", gov)
	}
	deg := Degrade(DegradeRerankOff, Args{"reason": "offline"}, WithLocale("zh"))
	if !Is(deg) {
		t.Errorf("Is should recognize a rendered Degrade line: %q", deg)
	}
	degPlain := Degrade(DegradeRerankOff, Args{"reason": "offline"}, WithLocale("zh"), WithPlain(true))
	if !Is(degPlain) {
		t.Errorf("Is should recognize a plain Degrade line: %q", degPlain)
	}
	for _, notCopy := range []string{
		"", "hello world", "func main() {}", "error: file not found",
		"审计链校验通过", // fragment of a template, not a full rendering
	} {
		if Is(notCopy) {
			t.Errorf("Is(%q) = true, want false", notCopy)
		}
	}
}

// TestAmbientPoolsAuthoredIndependently: zh pools are authored, not
// translated — pool sizes are asserted independently and must not be forced
// into a 1:1 line mapping.
func TestAmbientPoolsAuthoredIndependently(t *testing.T) {
	enTotal, zhTotal := registry.ambientCount("en"), registry.ambientCount("zh")
	if enTotal == 0 || zhTotal == 0 {
		t.Fatalf("empty ambient pools: en=%d zh=%d", enTotal, zhTotal)
	}
	if enTotal == zhTotal {
		t.Errorf("en (%d) and zh (%d) ambient pools are size-identical; zh must be authored, not mapped 1:1", enTotal, zhTotal)
	}
	// Both locales must cover every context that the other covers (parity of
	// coverage, not of line counts).
	for context := range registry.ambient.Locales["en"] {
		if len(registry.ambient.Locales["zh"][context]) == 0 {
			t.Errorf("zh has no ambient pool for context %s", context)
		}
	}
	for context := range registry.ambient.Locales["zh"] {
		if len(registry.ambient.Locales["en"][context]) == 0 {
			t.Errorf("en has no ambient pool for context %s", context)
		}
	}
}

// TestDetectLocale: CARINA_LOCALE > LC_ALL > LC_MESSAGES > LANG > en.
func TestDetectLocale(t *testing.T) {
	scrubLocaleEnv(t)
	if got := DetectLocale(); got != "en" {
		t.Errorf("bare env DetectLocale = %q, want en", got)
	}
	t.Setenv("LANG", "zh_CN.UTF-8")
	if got := DetectLocale(); got != "zh" {
		t.Errorf("LANG DetectLocale = %q, want zh", got)
	}
	t.Setenv("LC_MESSAGES", "en_US.UTF-8")
	if got := DetectLocale(); got != "en" {
		t.Errorf("LC_MESSAGES should outrank LANG, got %q", got)
	}
	t.Setenv("LC_ALL", "zh_TW.UTF-8")
	if got := DetectLocale(); got != "zh" {
		t.Errorf("LC_ALL should outrank LC_MESSAGES, got %q", got)
	}
	t.Setenv("CARINA_LOCALE", "en")
	if got := DetectLocale(); got != "en" {
		t.Errorf("CARINA_LOCALE should outrank everything, got %q", got)
	}
}

func containsLine(pool []string, line string) bool {
	for _, l := range pool {
		if l == line {
			return true
		}
	}
	return false
}
