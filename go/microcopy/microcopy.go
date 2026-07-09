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
// code → copy here. The en and zh pools are peers, authored independently
// rather than translated.
package microcopy

import (
	"os"
	"strings"
)

// Args carries named placeholder values for Governed and Degrade templates.
// Placeholders are written {name} in the pool files — templates, not fmt
// verbs — so the pool linter can verify each template names every declared
// placeholder. A missing argument is left visible as the literal {name}.
type Args map[string]string

// Code identifies a Governed register template. Governed copy is never
// generated at runtime; the constants below are the whole vocabulary.
type Code string

// Governed template codes. Each has a code-reviewed en and zh template in
// pools/governed.json.
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

// Degrade status codes. Each has an en and zh template in pools/degrade.json.
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

// plainLoading is the bare-verb suppression line the Ambient register
// collapses to under --json/--plain/!isatty.
const plainLoading = "Loading..."

type options struct {
	locale      string
	session     string
	plain       bool
	poolVersion int
}

// Option adjusts rendering: WithLocale, WithSession, WithPlain,
// WithPoolVersion.
type Option func(*options)

// WithLocale selects the copy locale. Values normalize by prefix (zh* → zh,
// en* → en); unsupported locales fall back to en. When absent, the locale
// comes from DetectLocale.
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

// WithPoolVersion pins the pool version for snapshot tests. Unknown versions
// fall back to the embedded pools (version 1).
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
	if o.plain {
		return plainLoading
	}
	locale := NormalizeLocale(o.locale)
	if o.session != "" {
		seed = seed + "|" + o.session
	}
	for _, ov := range registry.ambient.Overrides {
		if ov.re.MatchString(seed) {
			if pool := ov.Locales[locale]; len(pool) > 0 {
				return pool[pick(seed, len(pool))]
			}
		}
	}
	pool := registry.ambientPool(locale, resolveContext(seed))
	if len(pool) == 0 {
		return plainLoading
	}
	return pool[pick(seed, len(pool))]
}

// Governed renders a Governed register template: full sentences, exact
// nouns, no personality. Anything that asks for a decision or lands in the
// audit narrative goes through here. A missing argument stays visible as the
// literal placeholder; an unknown code returns an explicit marker — neither
// is ever silently swallowed.
func Governed(code Code, args Args, opts ...Option) string {
	o := resolveOptions(opts)
	tmpl, ok := registry.governed[string(code)]
	if !ok {
		return "Unknown governed microcopy code: " + string(code) + "."
	}
	return substitute(tmpl.text(NormalizeLocale(o.locale)), args)
}

// Degrade renders a Degrade register line: calm-factual statement plus a
// remedy, prefixed with the status glyph (~ degraded, ✓ done) unless plain
// output strips glyphs.
func Degrade(status DegradeStatus, args Args, opts ...Option) string {
	o := resolveOptions(opts)
	tmpl, ok := registry.degrade[string(status)]
	if !ok {
		return "Unknown degrade status: " + string(status) + "."
	}
	line := substitute(tmpl.text(NormalizeLocale(o.locale)), args)
	if o.plain {
		return line
	}
	return degradeGlyph(status) + line
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
	if value == plainLoading {
		return true
	}
	if registry.ambientIndex[value] {
		return true
	}
	for _, tmpl := range registry.governed {
		if tmpl.matches(value) {
			return true
		}
	}
	stripped := strings.TrimPrefix(strings.TrimPrefix(value, "~ "), "✓ ")
	for _, tmpl := range registry.degrade {
		if tmpl.matches(stripped) {
			return true
		}
	}
	return false
}

// DetectLocale resolves the copy locale from the environment:
// CARINA_LOCALE > LC_ALL > LC_MESSAGES > LANG > en. The --locale flag and the
// config cascade are client-side layers applied above this via WithLocale.
func DetectLocale() string {
	for _, key := range []string{"CARINA_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(key); v != "" {
			return NormalizeLocale(v)
		}
	}
	return "en"
}

// substitute slot-fills {name} placeholders. Unknown args are ignored;
// missing args leave the placeholder visible.
func substitute(text string, args Args) string {
	for k, v := range args {
		text = strings.ReplaceAll(text, "{"+k+"}", v)
	}
	return text
}
