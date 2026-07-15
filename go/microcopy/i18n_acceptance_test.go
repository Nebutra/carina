package microcopy

import (
	"strings"
	"testing"
	"unicode"
)

func TestTemplateRenderingIsSinglePassDeterministicAndTerminalSafe(t *testing.T) {
	args := Args{
		"action":      "write\n\x1b[31m{path}\x1b[0m",
		"path":        "/tmp/a\r\n/tmp/b",
		"decision_id": "decision\x00-7",
	}
	first := Governed(GovernedApprovalRequired, args, WithLocale("en"))
	for i := 0; i < 100; i++ {
		if got := Governed(GovernedApprovalRequired, args, WithLocale("en")); got != first {
			t.Fatalf("render %d changed: %q vs %q", i, got, first)
		}
	}
	if !strings.Contains(first, "{path}") {
		t.Fatalf("replacement value was rescanned: %q", first)
	}
	if strings.ContainsAny(first, "\x1b\r\n") {
		t.Fatalf("render contains ANSI or newline controls: %q", first)
	}
	for _, r := range first {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			t.Fatalf("render contains control rune %U: %q", r, first)
		}
	}
}

func TestMissingOrEmptyArgumentsUseLocalizedFallback(t *testing.T) {
	for _, locale := range SupportedLocales() {
		gov := Governed(GovernedApprovalRequired, Args{"action": "write"}, WithLocale(locale))
		if gov != governedFallback[locale] || strings.Contains(gov, "{") {
			t.Errorf("governed missing fallback(%s) = %q", locale, gov)
		}
		deg := Degrade(DegradeTimedOut, Args{"seconds": "1", "action": "\x1b[31m\x1b[0m"}, WithLocale(locale))
		if strings.TrimPrefix(deg, "~ ") != degradeFallback[locale] || strings.Contains(deg, "{") {
			t.Errorf("degrade empty fallback(%s) = %q", locale, deg)
		}
	}
}

func TestPluralCategoryForSupportedLocales(t *testing.T) {
	cases := []struct {
		locale string
		count  int
		want   PluralCategory
	}{
		{"en", 1, PluralOne}, {"en", 0, PluralOther}, {"en", 2, PluralOther},
		{"es", 1, PluralOne}, {"es", 2, PluralOther},
		{"fr", 0, PluralOne}, {"fr", 1, PluralOne}, {"fr", 2, PluralOther},
		{"zh", 1, PluralOther}, {"ja", 1, PluralOther}, {"ko", 1, PluralOther},
	}
	for _, tc := range cases {
		if got := PluralCategoryFor(tc.locale, tc.count); got != tc.want {
			t.Errorf("PluralCategoryFor(%s, %d) = %s, want %s", tc.locale, tc.count, got, tc.want)
		}
	}
}

func TestGovernedAndDegradeCountNeverRenderOneWithOtherNoun(t *testing.T) {
	wantOne := map[string]string{
		"en": "1 file", "zh": "1 个文件", "ja": "1 ファイル",
		"ko": "파일 1개", "es": "1 archivo", "fr": "1 fichier",
	}
	wantOther := map[string]string{
		"en": "2 files", "zh": "2 个文件", "ja": "2 ファイル",
		"ko": "파일 2개", "es": "2 archivos", "fr": "2 fichiers",
	}
	for _, locale := range SupportedLocales() {
		one := GovernedCount(GovernedDestructiveConfirm, 1, Args{"command": "rm build"}, WithLocale(locale))
		other := GovernedCount(GovernedDestructiveConfirm, 2, Args{"command": "rm build"}, WithLocale(locale))
		if !strings.Contains(one, wantOne[locale]) || !strings.Contains(other, wantOther[locale]) {
			t.Errorf("governed plural %s: one=%q other=%q", locale, one, other)
		}
		if strings.Contains(one, "1 files") || strings.Contains(one, "1 archivos") || strings.Contains(one, "1 fichiers") {
			t.Errorf("invalid one/other noun for %s: %q", locale, one)
		}

		duration := DegradeCount(DegradeTimedOut, 1, Args{"action": "code.search"}, WithLocale(locale))
		if duration == "" || strings.Contains(duration, "1 seconds") || strings.Contains(duration, "1 segundos") || strings.Contains(duration, "1 secondes") {
			t.Errorf("invalid degrade plural for %s: %q", locale, duration)
		}
	}
}

func TestExplicitLocaleValidationAndHonestChineseFallback(t *testing.T) {
	valid := map[string]string{
		"zh": "zh", "zh-CN": "zh", "zh_Hans_CN.UTF-8": "zh",
		"en-US": "en", "ja-JP": "ja", "ko-KR": "ko", "es-419": "es", "fr-FR": "fr",
	}
	for raw, want := range valid {
		got, err := CanonicalLocale(raw)
		if err != nil || got != want {
			t.Errorf("CanonicalLocale(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "de-DE", "zh-TW", "zh-HK", "zh-Hant", "en-", "ja-!"} {
		if _, err := CanonicalLocale(raw); err == nil {
			t.Errorf("CanonicalLocale(%q) unexpectedly succeeded", raw)
		}
		if got := NormalizeLocale(raw); got != "en" {
			t.Errorf("NormalizeLocale(%q) = %q, want honest en fallback", raw, got)
		}
	}
}

func TestUnsupportedSystemLocaleFallsBackQuietly(t *testing.T) {
	scrubLocaleEnv(t)
	t.Setenv("LANG", "de_DE.UTF-8")
	got, err := ResolveLocale("", "")
	if err != nil || got != "en" {
		t.Fatalf("system fallback = %q, %v; want en without error", got, err)
	}
}

func TestPoolVersionRegistrationAndMismatchValidation(t *testing.T) {
	if effective, exact := ResolvePoolVersion(1); effective != 1 || !exact {
		t.Fatalf("registered version = %d exact=%v", effective, exact)
	}
	effective, exact := ResolvePoolVersion(999)
	if effective != registry.version || exact {
		t.Fatalf("unknown version = %d exact=%v, want current fallback", effective, exact)
	}
	if got, want := Loading("code.search", WithLocale("en"), WithPoolVersion(999)), Loading("code.search", WithLocale("en")); got != want {
		t.Fatalf("unknown version fallback = %q, want %q", got, want)
	}
	if err := validatePoolVersions(1, 2, 1); err == nil {
		t.Fatal("mismatched pool versions must fail")
	}
}

func TestGovernedFactsAndTermsMetadataAuditable(t *testing.T) {
	for code, tmpl := range registry.governed {
		if err := validateGovernedMetadata(code, tmpl); err != nil {
			t.Errorf("metadata %s: %v", code, err)
		}
	}
	secret := registry.governed[string(GovernedSecretRedacted)]
	facts := strings.Join(secret.Facts, ",")
	for _, fact := range []string{"display_redacted", "transcript_redacted", "audit_record_redacted", "original_not_retained"} {
		if !strings.Contains(facts, fact) {
			t.Errorf("secret facts missing %s: %v", fact, secret.Facts)
		}
	}
	falseStorageTerms := map[string]string{"en": "encrypted", "zh": "加密", "ja": "暗号化", "ko": "암호화", "es": "cifrado", "fr": "chiffré"}
	for locale, term := range falseStorageTerms {
		if strings.Contains(strings.ToLower(secret.Text[locale]), term) {
			t.Errorf("secret copy still claims retained encrypted original for %s: %q", locale, secret.Text[locale])
		}
	}
}

func TestOperationalCopyMatchesPublishedCLIContracts(t *testing.T) {
	checkpoint := registry.governed[string(GovernedCheckpointCreated)]
	if got := strings.Join(checkpoint.Placeholders, ","); got != "action,session_id,checkpoint_id" {
		t.Fatalf("checkpoint placeholders = %q", got)
	}
	checkpointFacts := strings.Join(checkpoint.Facts, ",")
	for _, fact := range []string{"checkpoint_preview_command_available", "checkpoint_restore_command_available", "restore_requires_explicit_confirmation"} {
		if !strings.Contains(checkpointFacts, fact) {
			t.Errorf("checkpoint facts missing %s: %v", fact, checkpoint.Facts)
		}
	}

	denied := registry.governed[string(GovernedApprovalDenied)]
	if strings.Contains(strings.Join(denied.Facts, ","), "command_not_executed") ||
		!strings.Contains(strings.Join(denied.Facts, ","), "requested_action_not_executed") {
		t.Fatalf("approval denial facts describe the wrong action type: %v", denied.Facts)
	}

	for _, locale := range SupportedLocales() {
		created := Governed(GovernedCheckpointCreated, Args{
			"action": "migration", "session_id": "session-4", "checkpoint_id": "checkpoint-9",
		}, WithLocale(locale))
		for _, command := range []string{
			"carina checkpoint preview session-4 checkpoint-9",
			"carina checkpoint restore session-4 checkpoint-9 --yes",
		} {
			if !strings.Contains(created, command) {
				t.Errorf("checkpoint copy %s missing real CLI command %q: %q", locale, command, created)
			}
		}

		backgrounded := Degrade(DegradeBackgrounded, Args{
			"task_id": "task-9", "session_id": "session-4",
		}, WithLocale(locale))
		if !strings.Contains(backgrounded, "task-9") || !strings.Contains(backgrounded, "carina watch session-4") || strings.Contains(backgrounded, "carina watch task-9") {
			t.Errorf("background copy %s has wrong watch target: %q", locale, backgrounded)
		}

		partial := Degrade(DegradePartial, Args{
			"applied": "3", "total": "5", "tx_id": "transaction-7",
		}, WithLocale(locale))
		if !strings.Contains(partial, "3/5") || !strings.Contains(partial, "transaction-7") {
			t.Errorf("partial copy %s lost stable facts: %q", locale, partial)
		}
	}

	for register, templates := range map[string]map[string]*template{
		"governed": registry.governed,
		"degrade":  registry.degrade,
	} {
		for key, tmpl := range templates {
			for locale, body := range tmpl.Text {
				for _, nonexistent := range []string{"carina rollback ", "carina log --tx"} {
					if strings.Contains(body, nonexistent) {
						t.Errorf("%s/%s/%s advertises unsupported command %q", register, key, locale, nonexistent)
					}
				}
			}
		}
	}
}

func TestBootstrapCatalogCompleteAndSafe(t *testing.T) {
	codes := []BootstrapCode{
		BootstrapTUIUsage, BootstrapBareUsage, BootstrapFlagSocket,
		BootstrapFlagSession, BootstrapFlagWorkspace, BootstrapFlagLocale,
		BootstrapFlagNoAltScreen, BootstrapInteractiveRequired,
		BootstrapResolveHomeFailed, BootstrapConfigFailed, BootstrapLocaleInvalid,
		BootstrapRecoveryFailed, BootstrapStartupFailed, BootstrapRuntimeFailed,
	}
	for _, code := range codes {
		for _, locale := range SupportedLocales() {
			got := Bootstrap(code, Args{"reason": "bad\n\x1b[31mstate\x1b[0m"}, locale)
			if got == "" || strings.ContainsAny(got, "\n\r\x1b") {
				t.Errorf("bootstrap %s/%s unsafe: %q", code, locale, got)
			}
		}
	}
	for _, locale := range []string{"zh", "ja", "ko", "es", "fr"} {
		got := Bootstrap(BootstrapLocaleInvalid, Args{"reason": "unsupported locale"}, locale)
		if strings.Contains(got, "unsupported locale") {
			t.Errorf("locale error mixes English into %s: %q", locale, got)
		}
	}
}
