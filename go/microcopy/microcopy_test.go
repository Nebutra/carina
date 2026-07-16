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
	for _, key := range []string{"CARINA_LOCALE", "CARINA_TUI_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
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
	for _, locale := range SupportedLocales() {
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

func TestSupportedLocalesReturnsSnapshot(t *testing.T) {
	first := SupportedLocales()
	if len(first) != 7 {
		t.Fatalf("supported locale count = %d, want 7 (includes zh-Hant)", len(first))
	}
	first[0] = "mutated"
	if got := SupportedLocales()[0]; got != "en" {
		t.Fatalf("SupportedLocales leaked mutable storage: first locale = %q", got)
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
	for _, locale := range SupportedLocales() {
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

// TestLoadingLocaleNormalization covers BCP47-ish and POSIX locale shapes;
// unsupported values fall back to en.
func TestLoadingLocaleNormalization(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"zh", "zh"},
		{"zh-CN", "zh"},
		{"zh-Hans", "zh"},
		{"zh_Hans_CN.UTF-8", "zh"},
		{"zh_TW.UTF-8", "zh-Hant"},
		{"ZH_HK", "zh-Hant"},
		{"zh-Hant", "zh-Hant"},
		{"en", "en"},
		{"en_US.UTF-8", "en"},
		{"ja_JP", "ja"},
		{"ja-JP-u-ca-japanese", "ja"},
		{"ko_KR.UTF-8", "ko"},
		{"es-419", "es"},
		{"fr_FR@euro", "fr"},
		{"", "en"},
		{"C", "en"},
		{"de-DE", "en"},
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
	for _, locale := range SupportedLocales() {
		if got := Loading("code.search", WithLocale(locale), WithPlain(true)); got != plainLoading[locale] {
			t.Errorf("plain Loading(%s) = %q, want %q", locale, got, plainLoading[locale])
		}
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

// TestGovernedMissingArgUsesSafeFallback: missing fields never expose a raw
// placeholder token or an internal identifier.
func TestGovernedMissingArgUsesSafeFallback(t *testing.T) {
	got := Governed(GovernedApprovalRequired, Args{"action": "write"}, WithLocale("en"))
	if got != governedFallback["en"] || strings.Contains(got, "{") {
		t.Errorf("missing args must select safe fallback, got %q", got)
	}
}

// TestGovernedLocaleParity: every governed code has a non-empty, independently
// authored template in every supported locale.
func TestGovernedLocaleParity(t *testing.T) {
	for code, tmpl := range registry.governed {
		seen := map[string]string{}
		for _, locale := range SupportedLocales() {
			body := tmpl.Text[locale]
			if strings.TrimSpace(body) == "" {
				t.Errorf("governed %s missing locale %s", code, locale)
			}
			if prior, ok := seen[body]; ok {
				t.Errorf("governed %s locales %s and %s are byte-identical", code, prior, locale)
			}
			seen[body] = locale
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
			[]string{"Timed out after 30 seconds", "carina doctor"}, "~ "},
		{DegradeBackgrounded, Args{"task_id": "t-9", "session_id": "s-4"}, "en",
			[]string{"background", "task t-9", "carina watch s-4"}, "~ "},
		{DegradeConflict, Args{"path": "go/daemon/agent.go"}, "en",
			[]string{"Conflict", "go/daemon/agent.go", "Nothing was overwritten"}, "~ "},
		{DegradeDone, nil, "en", []string{"Done"}, "✓ "},
		{DegradeDone, nil, "zh", []string{"完成"}, "✓ "},
		{DegradePartial, Args{"applied": "3", "total": "5", "tx_id": "41"}, "en",
			[]string{"transaction 41", "3/5", "audit trail"}, "~ "},
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
		for _, locale := range SupportedLocales() {
			if strings.TrimSpace(tmpl.Text[locale]) == "" {
				t.Errorf("degrade status %s missing locale %s", s, locale)
			}
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
	for _, locale := range SupportedLocales() {
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
	for _, locale := range SupportedLocales() {
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

// TestAmbientPoolsAuthoredIndependently verifies complete context coverage in
// every locale without requiring one-to-one translated line counts.
func TestAmbientPoolsAuthoredIndependently(t *testing.T) {
	for _, locale := range SupportedLocales() {
		if total := registry.ambientCount(locale); total == 0 {
			t.Fatalf("empty ambient pool for %s", locale)
		}
		for context := range registry.ambient.Locales["en"] {
			if len(registry.ambient.Locales[locale][context]) == 0 {
				t.Errorf("%s has no ambient pool for context %s", locale, context)
			}
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
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	if got := DetectLocale(); got != "zh" {
		t.Errorf("LC_ALL should outrank LC_MESSAGES, got %q", got)
	}
	t.Setenv("CARINA_LOCALE", "en")
	if got := DetectLocale(); got != "en" {
		t.Errorf("CARINA_LOCALE should outrank everything, got %q", got)
	}
}

func TestResolveLocalePrecedence(t *testing.T) {
	scrubLocaleEnv(t)
	t.Setenv("LANG", "ja_JP.UTF-8")
	t.Setenv("LC_MESSAGES", "es_ES.UTF-8")
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	t.Setenv("CARINA_LOCALE", "ko_KR.UTF-8")

	if got, err := ResolveLocale("zh-CN", "es-ES"); err != nil || got != "zh" {
		t.Fatalf("flag locale = %q, want zh", got)
	}
	if got, err := ResolveLocale("", "es-ES"); err != nil || got != "ko" {
		t.Fatalf("CARINA_LOCALE = %q, want ko", got)
	}
	t.Setenv("CARINA_LOCALE", "")
	if got, err := ResolveLocale("", "es-ES"); err != nil || got != "es" {
		t.Fatalf("config locale = %q, want es", got)
	}
	if got, err := ResolveLocale("", ""); err != nil || got != "fr" {
		t.Fatalf("system locale = %q, want fr", got)
	}
}

func TestUnknownCopyFallbackNeverLeaksIdentifierOrReturnsEmpty(t *testing.T) {
	for _, locale := range SupportedLocales() {
		govID := "internal.governed.identifier"
		gov := Governed(Code(govID), nil, WithLocale(locale))
		if strings.TrimSpace(gov) == "" || strings.Contains(gov, govID) {
			t.Errorf("governed fallback(%s) = %q", locale, gov)
		}

		degradeID := "internal.degrade.identifier"
		deg := Degrade(DegradeStatus(degradeID), nil, WithLocale(locale))
		if strings.TrimSpace(deg) == "" || strings.Contains(deg, degradeID) {
			t.Errorf("degrade fallback(%s) = %q", locale, deg)
		}
	}
}

func TestTemplatePlaceholderParityAcrossLocales(t *testing.T) {
	for register, templates := range map[string]map[string]*template{
		"governed": registry.governed,
		"degrade":  registry.degrade,
	} {
		for key, tmpl := range templates {
			for _, locale := range SupportedLocales() {
				body := tmpl.Text[locale]
				got := map[string]bool{}
				for _, token := range placeholderShape.FindAllString(body, -1) {
					got[strings.Trim(token, "{}")] = true
				}
				for _, name := range tmpl.Placeholders {
					if !got[name] {
						t.Errorf("%s/%s/%s missing {%s}", register, locale, key, name)
					}
					delete(got, name)
				}
				for name := range got {
					t.Errorf("%s/%s/%s has undeclared {%s}", register, locale, key, name)
				}
			}
		}
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
