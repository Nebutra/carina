// Package microcopy renders Carina's user-facing copy in three
// compiler-segregated registers. Ambient is the playful field register —
// spinners, idle, progress and housekeeping lines picked deterministically
// (FNV-1a over a seed such as the RPC method name, so the same command shows
// the same personality every run). Governed is the sober register — constant,
// code-reviewed templates with named placeholders for permission prompts,
// policy denials, destructive-action confirmations and audit summaries:
// anything that asks for a decision or lands in the audit narrative. Degrade
// is the calm-factual register — one line per degrade-status code, stating
// what happened plus a remedy, so no failure path is silently swallowed. The
// registers are distinct Go types with distinct constructors drawing from
// physically separate pools, so a playful line can never leak into a decision
// prompt; the daemon emits stable machine codes only and clients map
// code → copy here. The en, zh, ja, ko, es and fr pools are peers, authored
// in the same register rather than assembled at runtime.
package microcopy

import (
	"os"
	"strconv"
	"strings"
)

// Args carries named placeholder values for Governed and Degrade templates.
// Placeholders are written {name} in the pool files — templates, not fmt
// verbs — so the pool linter can verify each template names every declared
// placeholder. Values are sanitized before rendering; missing or empty
// arguments select safe localized fallback copy.
type Args map[string]string

// Code identifies a Governed register template. Governed copy is never
// generated at runtime; the constants below are the whole vocabulary.
type Code string

// Governed template codes. Each has a code-reviewed template for every
// locale returned by SupportedLocales in pools/governed.json.
const (
	GovernedApprovalRequired   Code = "approval.required"
	GovernedApprovalGranted    Code = "approval.granted"
	GovernedApprovalDenied     Code = "approval.denied"
	GovernedPolicyDenied       Code = "policy.denied"
	GovernedPolicyUpdated      Code = "policy.updated"
	GovernedDestructiveConfirm Code = "destructive.confirm"
	GovernedRollbackComplete   Code = "rollback.complete"
	GovernedCheckpointCreated  Code = "checkpoint.created"
	GovernedAuditVerified      Code = "audit.verified"
	GovernedSecretRedacted     Code = "secret.redacted"
	GovernedEgressConfirm      Code = "egress.confirm"
)

// DegradeStatus identifies a Degrade register template. The taxonomy follows
// the productization plan (P1.3): interrupted (split by initiator), timed
// out, backgrounded, conflict, done, partial — plus the daemon's existing
// observable-degrade reasons (semantic search, reranker, LSP lookup) and the
// doctor-remediable daemon-unreachable state.
type DegradeStatus string

// Degrade status codes. Each has a template for every locale returned by
// SupportedLocales in pools/degrade.json.
const (
	DegradeInterruptedByUser   DegradeStatus = "interrupted.user"
	DegradeInterruptedByPolicy DegradeStatus = "interrupted.policy"
	DegradeInterruptedByAgent  DegradeStatus = "interrupted.agent"
	DegradeTimedOut            DegradeStatus = "timed_out"
	DegradeBackgrounded        DegradeStatus = "backgrounded"
	DegradeConflict            DegradeStatus = "conflict"
	DegradeDone                DegradeStatus = "done"
	DegradePartial             DegradeStatus = "partial"
	DegradeDaemonUnreachable   DegradeStatus = "daemon_unreachable"
	DegradeSemanticOff         DegradeStatus = "semantic_off"
	DegradeRerankOff           DegradeStatus = "rerank_off"
	DegradeLookupImprecise     DegradeStatus = "lookup_imprecise"
)

var plainLoading = map[string]string{
	"en": "Loading...",
	"zh": "加载中…",
	"ja": "読み込み中…",
	"ko": "불러오는 중…",
	"es": "Cargando…",
	"fr": "Chargement…",
}

var governedFallback = map[string]string{
	"en": "This action needs review. No action was taken.",
	"zh": "此操作需要审查。尚未执行任何操作。",
	"ja": "この操作には確認が必要です。操作は実行されていません。",
	"ko": "이 작업은 검토가 필요합니다. 아무 작업도 실행되지 않았습니다.",
	"es": "Esta acción requiere revisión. No se ejecutó ninguna acción.",
	"fr": "Cette action doit être examinée. Aucune action n’a été exécutée.",
}

var degradeFallback = map[string]string{
	"en": "The operation did not complete. Fix: carina doctor",
	"zh": "操作未完成。修复：carina doctor",
	"ja": "操作は完了しませんでした。修正方法: carina doctor",
	"ko": "작업이 완료되지 않았습니다. 수정 방법: carina doctor",
	"es": "La operación no se completó. Solución: carina doctor",
	"fr": "L’opération n’a pas abouti. Correctif : carina doctor",
}

type options struct {
	locale      string
	session     string
	plain       bool
	poolVersion int
}

// Option adjusts rendering: WithLocale, WithSession, WithPlain,
// WithPoolVersion.
type Option func(*options)

// WithLocale selects the copy locale. BCP47-ish and POSIX values normalize to
// an authored base language; unsupported locales fall back to en. When absent,
// the locale comes from DetectLocale.
func WithLocale(locale string) Option {
	return func(o *options) { o.locale = locale }
}

// WithSession salts the Ambient seed as seed + "|" + sessionID: per-session
// variety, within-session stability.
func WithSession(sessionID string) Option {
	return func(o *options) { o.session = sessionID }
}

// WithPlain marks non-interactive output (--json, --plain, !isatty). The
// Ambient register collapses to a bare verb; Governed and Degrade still
// render — a pipe consumer deserves precise decision and degrade text — but
// with glyphs stripped.
func WithPlain(plain bool) Option {
	return func(o *options) { o.plain = plain }
}

// WithPoolVersion selects a registered catalog version. Unknown versions
// explicitly fall back to the current embedded catalog; ResolvePoolVersion
// exposes whether the request was exact.
func WithPoolVersion(v int) Option {
	return func(o *options) { o.poolVersion = v }
}

func resolveOptions(opts []Option) options {
	o := options{poolVersion: registry.version}
	for _, opt := range opts {
		opt(&o)
	}
	if o.locale == "" {
		o.locale = DetectLocale()
	}
	return o
}

// Loading returns an Ambient register line for a wait/progress moment.
// The seed is, by convention, the RPC method or tool name as it appears on
// the wire ("code.search", "workspace.patch.apply"); the same seed, locale
// and pool version always yield the same line. Seed-specific overrides win
// over the context pool, which wins over the generic pool.
func Loading(seed string, opts ...Option) string {
	o := resolveOptions(opts)
	r := registryForVersion(o.poolVersion)
	locale := NormalizeLocale(o.locale)
	if o.plain {
		return plainLoading[locale]
	}
	if o.session != "" {
		seed = seed + "|" + o.session
	}
	for _, ov := range r.ambient.Overrides {
		if ov.re.MatchString(seed) {
			if pool := ov.Locales[locale]; len(pool) > 0 {
				return pool[pick(seed, len(pool))]
			}
		}
	}
	pool := r.ambientPool(locale, resolveContext(seed))
	if len(pool) == 0 {
		return plainLoading[locale]
	}
	return pool[pick(seed, len(pool))]
}

// Governed renders a Governed register template: full sentences, exact
// nouns, no personality. Anything that asks for a decision or lands in the
// audit narrative goes through here. Missing arguments and unknown codes
// return safe localized fallback copy; internal identifiers and placeholder
// tokens are never exposed to the user.
func Governed(code Code, args Args, opts ...Option) string {
	o := resolveOptions(opts)
	r := registryForVersion(o.poolVersion)
	locale := NormalizeLocale(o.locale)
	if code == GovernedDestructiveConfirm && strings.TrimSpace(args["count_label"]) == "" {
		count, err := strconv.Atoi(strings.TrimSpace(args["count"]))
		if err != nil {
			return governedFallback[locale]
		}
		return GovernedCount(code, count, args, opts...)
	}
	tmpl, ok := r.governed[string(code)]
	if !ok {
		return governedFallback[locale]
	}
	line, valid := renderTemplate(tmpl.text(locale), args)
	if !valid || strings.TrimSpace(line) == "" {
		return governedFallback[locale]
	}
	return line
}

// Degrade renders a Degrade register line: calm-factual statement plus a
// remedy, prefixed with the status glyph (~ degraded, ✓ done) unless plain
// output strips glyphs.
func Degrade(status DegradeStatus, args Args, opts ...Option) string {
	o := resolveOptions(opts)
	r := registryForVersion(o.poolVersion)
	locale := NormalizeLocale(o.locale)
	if status == DegradeTimedOut && strings.TrimSpace(args["duration"]) == "" {
		count, err := strconv.Atoi(strings.TrimSpace(args["seconds"]))
		if err != nil {
			return degradeFallbackLine(locale, o.plain)
		}
		return DegradeCount(status, count, args, opts...)
	}
	tmpl, ok := r.degrade[string(status)]
	if !ok {
		return degradeFallbackLine(locale, o.plain)
	}
	line, valid := renderTemplate(tmpl.text(locale), args)
	if !valid || strings.TrimSpace(line) == "" {
		return degradeFallbackLine(locale, o.plain)
	}
	if o.plain {
		return line
	}
	return degradeGlyph(status) + line
}

func degradeFallbackLine(locale string, plain bool) string {
	line := degradeFallback[locale]
	if plain {
		return line
	}
	return "~ " + line
}

func degradeGlyph(status DegradeStatus) string {
	if status == DegradeDone {
		return "✓ "
	}
	return "~ "
}

// Is reports whether value is a rendered microcopy line from any register —
// the membership test renderers use to distinguish placeholder copy from
// real content.
func Is(value string) bool {
	for _, line := range plainLoading {
		if value == line {
			return true
		}
	}
	for _, line := range governedFallback {
		if value == line {
			return true
		}
	}
	stripped := strings.TrimPrefix(strings.TrimPrefix(value, "~ "), "✓ ")
	for _, line := range degradeFallback {
		if stripped == line {
			return true
		}
	}
	if registry.ambientIndex[value] {
		return true
	}
	for _, tmpl := range registry.governed {
		if tmpl.matches(value) {
			return true
		}
	}
	for _, tmpl := range registry.degrade {
		if tmpl.matches(stripped) {
			return true
		}
	}
	return false
}

// DetectLocale resolves the general copy locale from the environment:
// CARINA_LOCALE > LC_ALL > LC_MESSAGES > LANG > en.
func DetectLocale() string {
	if v := os.Getenv("CARINA_LOCALE"); v != "" {
		return NormalizeLocale(v)
	}
	return DetectSystemLocale()
}

// ResolveLocale applies the TUI precedence contract once at startup. Explicit
// flag, environment and config choices are validated; only OS locale detection
// silently falls back to English.
// --locale > CARINA_LOCALE > config tui_locale > system locale > en.
// CARINA_TUI_LOCALE participates through config tui_locale.
func ResolveLocale(flagLocale, configLocale string) (string, error) {
	if strings.TrimSpace(flagLocale) != "" {
		return CanonicalLocale(flagLocale)
	}
	if v := os.Getenv("CARINA_LOCALE"); v != "" {
		return CanonicalLocale(v)
	}
	if strings.TrimSpace(configLocale) != "" {
		return CanonicalLocale(configLocale)
	}
	return DetectSystemLocale(), nil
}

// DetectSystemLocale resolves only the operating-system locale variables.
func DetectSystemLocale() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(key); v != "" {
			return NormalizeLocale(v)
		}
	}
	return "en"
}
