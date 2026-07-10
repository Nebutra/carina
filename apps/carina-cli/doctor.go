package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nebutra/carina/go/tui"
)

// carina doctor (P1.6): wires the daemon's daemon.doctor probe list to a
// three-state (pass/warn/fail) human render with copy-paste remediation
// strings, honors the CARINA_DOCTOR_DISABLE kill-switch, and backs the
// first-launch auto-run hook consumed by bare `carina` (runBareTUI).

// doctorFirstRunMarker is the file whose absence in ~/.carina marks "this
// machine has never run carina doctor" — the first-launch auto-run signal.
// A dedicated marker (rather than "does ~/.carina exist") because `carina
// init` and any governed command can create ~/.carina before doctor ever
// runs; auto-run must fire exactly once regardless of ordering.
const doctorFirstRunMarker = ".doctor-first-run"

// doctorDisabledEnv is the CLI-side kill-switch env var name, kept
// byte-for-byte identical to go/daemon's doctorDisabledEnv constant
// (CARINA_DOCTOR_DISABLE) so both sides honor the exact same spelling.
// apps/carina-cli does not import go/daemon (a client never depends on
// daemon internals), so the name is duplicated here rather than shared —
// TestCmdDoctorHonorsKillSwitchWithoutDialing and go/daemon's
// TestDoctorDisabled* tests each pin their own side.
const doctorDisabledEnv = "CARINA_DOCTOR_DISABLE"

// doctorDisabled mirrors go/daemon's doctorDisabled: the same truthy-value
// contract (""/0/false/off = unset; anything else non-empty = on), checked
// CLI-side by initGate so `carina doctor` never dials the daemon at all
// when the kill-switch is set, not merely suppresses its probes server-side.
func doctorDisabled(getenv func(string) string) bool {
	v := strings.ToLower(strings.TrimSpace(getenv(doctorDisabledEnv)))
	switch v {
	case "", "0", "false", "off":
		return false
	default:
		return true
	}
}

// shouldAutoRunDoctor reports whether carina doctor should auto-run because
// this looks like a first launch on this machine: homeDir has no
// .doctor-first-run marker yet. A stat error (including "not exist") counts
// as "should run" — matching cmdInit's own MkdirAll-creates-if-missing
// posture rather than blocking doctor on a directory that may not exist yet.
func shouldAutoRunDoctor(homeDir string) bool {
	_, err := os.Stat(filepath.Join(homeDir, doctorFirstRunMarker))
	return err != nil
}

// markDoctorAutoRun writes the first-run marker so shouldAutoRunDoctor never
// fires again on this machine. Best-effort: called after an auto-run
// attempt regardless of the probes' pass/warn/fail outcome, since the point
// is "doctor has run once as onboarding", not "doctor passed once".
func markDoctorAutoRun(homeDir string) error {
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(homeDir, doctorFirstRunMarker), []byte("1\n"), 0o600)
}

// maybeAutoRunDoctor is bare `carina`'s first-launch onboarding hook (P1.6:
// "run automatically on first launch to double as onboarding"): on a
// machine with no .doctor-first-run marker, it runs the same probes as
// `carina doctor`, prints the report to stderr (stdout is reserved for the
// TUI's own alt-screen paint that follows), and writes the marker so it
// never fires again. Honors CARINA_DOCTOR_DISABLE — a locked-down
// deployment that disabled doctor does not want it force-run on first
// launch either. Never blocks or fails the TUI launch: home-dir resolution
// or marker-write failures are swallowed (best-effort onboarding, not a
// startup precondition).
func maybeAutoRunDoctor(call Caller) {
	if doctorDisabled(os.Getenv) {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".carina")
	if !shouldAutoRunDoctor(dir) {
		return
	}
	var report map[string]any
	if err := call.Call("daemon.doctor", map[string]any{}, &report); err == nil {
		fmt.Fprintln(os.Stderr, "carina: first launch on this machine — running carina doctor once:")
		fmt.Fprint(os.Stderr, renderDoctorReport(report, false))
		fmt.Fprintln(os.Stderr, "(run `carina doctor` any time to re-check; this auto-run will not repeat.)")
	}
	_ = markDoctorAutoRun(dir)
}

// cmdDoctor dispatches daemon.doctor and renders pass/warn/fail with
// remediation (default) or passes the raw JSON through (--json), matching
// the one-engine-two-renderers contract (P1.5) other commands already
// follow. c is nil when initGate short-circuited the kill-switch (doctor
// never dialed the daemon at all): render the same disabled report the
// daemon itself would have without ever needing a live connection.
//
// The report is always printed before any error is returned: a non-OK
// verdict returns a *doctorOutcomeError so classifyExitCode maps it to the
// shared tui.Outcome exit code (6 degraded-partial for WARN, 1 runtime
// error for FAIL) — doctor's exit code is truthful about its own findings
// exactly like every other governed command's.
func cmdDoctor(c *rpcClient, args []string) error {
	jsonOut := false
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
		}
	}
	var report map[string]any
	disabled := c == nil
	if disabled {
		report = map[string]any{
			"disabled": true,
			"reason":   doctorDisabledEnv + " is set; probes did not run",
		}
	} else if err := c.Call("daemon.doctor", map[string]any{}, &report); err != nil {
		return err
	}
	var auditChk *doctorCheck
	if !disabled {
		chk := auditChainCheckViaRPC(c)
		auditChk = &chk
	}

	if jsonOut {
		if err := printJSON(report); err != nil {
			return err
		}
	} else {
		fmt.Print(renderDoctorReport(report, false))
		if auditChk != nil {
			fmt.Print(renderDoctorCheck(*auditChk))
		}
	}
	if disabled {
		return nil
	}
	outcome := doctorOutcome(report)
	if auditChk != nil && auditChk.state == "FAIL" {
		outcome = tui.OutcomeRuntimeError
	}
	if outcome != tui.OutcomeOK {
		return &doctorOutcomeError{outcome: outcome}
	}
	return nil
}

// auditChainCheckViaRPC resolves the most-recently-created session across
// every workspace (doctor is a whole-machine diagnostic, not scoped to
// cwd — unlike resumeMostRecentOrFresh) and verifies its audit chain via
// audit.verify, feeding auditChainCheck the live result. A session.list
// failure or empty session list degrades to SKIP (auditChainCheck's
// hasSessions=false path) rather than failing doctor outright — an
// unreadable session list is itself unusual, but doctor's job here is
// specifically the audit chain, not session storage (state_dir_writable
// already covers general storage health).
func auditChainCheckViaRPC(c *rpcClient) doctorCheck {
	var sessions []sessionSummary
	if err := c.Call("session.list", map[string]any{}, &sessions); err != nil || len(sessions) == 0 {
		return auditChainCheck(nil, false, false)
	}
	latest := sessions[0]
	for _, s := range sessions {
		if s.CreatedAt > latest.CreatedAt {
			latest = s
		}
	}
	var raw json.RawMessage
	if err := c.Call("audit.verify", map[string]any{"session_id": latest.SessionID}, &raw); err != nil {
		return auditChainCheck(nil, true, true)
	}
	return auditChainCheck(raw, true, false)
}

// renderDoctorCheck renders one doctorCheck line in the same format
// doctorChecks-derived rows use in renderDoctorReport, for checks (like the
// audit-chain probe) that are computed client-side rather than sourced from
// the daemon.doctor report map.
func renderDoctorCheck(chk doctorCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%-4s] %-16s %s\n", chk.state, chk.name, chk.detail)
	if chk.remediation != "" {
		fmt.Fprintf(&b, "         Fix: %s\n", chk.remediation)
	}
	return b.String()
}

// doctorCheck is one rendered probe line: name, tri-state verdict, detail,
// and (for warn/fail) a copy-paste remediation command.
type doctorCheck struct {
	name        string
	state       string // "PASS", "WARN", "FAIL"
	detail      string
	remediation string
}

// renderDoctorReport renders daemon.doctor's decoded response as a
// pass/warn/fail checklist with remediation strings. plain strips any
// future glyph/color decoration (NO_COLOR/--plain contract); the render is
// plain text either way today since no color has been added yet.
func renderDoctorReport(report map[string]any, plain bool) string {
	var b strings.Builder
	if disabled, _ := report["disabled"].(bool); disabled {
		reason, _ := report["reason"].(string)
		fmt.Fprintf(&b, "carina doctor: disabled (%s)\n", reason)
		fmt.Fprintf(&b, "Unset %s to run probes.\n", doctorDisabledEnv)
		return b.String()
	}

	if v, ok := report["version"].(string); ok {
		fmt.Fprintf(&b, "carina doctor — daemon %s\n\n", v)
	}

	for _, chk := range doctorChecks(report) {
		fmt.Fprintf(&b, "[%-4s] %-16s %s\n", chk.state, chk.name, chk.detail)
		if chk.remediation != "" {
			fmt.Fprintf(&b, "         Fix: %s\n", chk.remediation)
		}
	}
	return b.String()
}

// doctorChecks converts the decoded daemon.doctor map into the ordered list
// of rendered checks, including per-check remediation strings. Ordering is
// fixed (not map iteration order) so output is stable across runs.
func doctorChecks(report map[string]any) []doctorCheck {
	var out []doctorCheck

	if kernel, ok := report["kernel"].(map[string]any); ok {
		out = append(out, boolCheck("kernel", kernel,
			"carina-kernel-service reachable",
			"start the daemon: carina-daemon &"))
	}
	if sd, ok := report["state_dir_writable"].(map[string]any); ok {
		out = append(out, boolCheck("state_dir", sd,
			"state directory writable",
			"check permissions on ~/.carina/state"))
	}
	if tools, ok := report["tools"].(map[string]any); ok {
		available, _ := tools["available"].(bool)
		dir, _ := tools["dir"].(string)
		chk := doctorCheck{name: "zig_tools", state: "PASS", detail: "native tools available (" + dir + ")"}
		if !available {
			chk.state = "FAIL"
			chk.detail = "native Zig tools not found"
			chk.remediation = "build them: cd zig && zig build -Doptimize=ReleaseSafe"
		}
		out = append(out, chk)
	}
	if reasoner, ok := report["reasoner"].(bool); ok {
		chk := doctorCheck{name: "reasoner", state: "PASS", detail: "model reasoner configured"}
		if !reasoner {
			// Warn-tier, not fail-tier (TestDoctorReasonerAbsentIsWarnNotFail):
			// no reasoner still runs in mock mode.
			chk.state = "WARN"
			chk.detail = "no model reasoner configured — running in mock mode"
			chk.remediation = "carina auth login <provider> <api_key>"
		}
		out = append(out, chk)
	}
	if ctxEng, ok := report["context_engine"].(map[string]any); ok {
		out = append(out, boolCheck("context_engine", ctxEng,
			"context engine healthy",
			"run: carina context doctor"))
	}
	if lsp, ok := report["lsp"].(map[string]any); ok {
		out = append(out, lspChecks(lsp)...)
	}
	if byok, ok := report["byok"].(map[string]any); ok {
		out = append(out, byokCheck(byok))
	}
	if policy, ok := report["policy"].(map[string]any); ok {
		if chk, present := policyCheck(policy); present {
			out = append(out, chk)
		}
	}
	return out
}

// policyCheck renders the policy freshness probe (P1.6): WARN when the
// enterprise policy bundle on disk has diverged from what the daemon
// loaded at startup (reload.go never re-inits kernel/policy wiring, so
// only a restart applies an on-disk bundle.toml/trusted-keys/approval.json
// edit) — catching the "doctor says PASS but the tightened policy never
// took effect" governance gap. present is false when no PolicyDir is
// configured at all: nothing to report, not even a PASS row, since an
// open-source/local deployment never opted into an enterprise bundle.
func policyCheck(policy map[string]any) (chk doctorCheck, present bool) {
	configured, _ := policy["configured"].(bool)
	if !configured {
		return doctorCheck{}, false
	}
	stale, _ := policy["stale"].(bool)
	if !stale {
		return doctorCheck{name: "policy", state: "PASS", detail: "enterprise policy bundle matches what is loaded"}, true
	}
	reason, _ := policy["reason"].(string)
	if reason == "" {
		reason = "policy bundle on disk has changed since the daemon loaded it"
	}
	return doctorCheck{
		name:        "policy",
		state:       "WARN",
		detail:      reason,
		remediation: "restart the daemon to apply the on-disk policy change: pkill carina-daemon && carina-daemon &",
	}, true
}

// boolCheck renders a simple {"ok": bool, "error": string} probe map as a
// PASS/FAIL doctorCheck.
func boolCheck(name string, probe map[string]any, passDetail, remediation string) doctorCheck {
	ok, _ := probe["ok"].(bool)
	if ok {
		return doctorCheck{name: name, state: "PASS", detail: passDetail}
	}
	errMsg, _ := probe["error"].(string)
	detail := "failed"
	if errMsg != "" {
		detail = errMsg
	}
	return doctorCheck{name: name, state: "FAIL", detail: detail, remediation: remediation}
}

// lspChecks renders each lsp.servers entry as a WARN (server not present —
// LSP is opportunistic, never blocking) with its copy-paste install
// remediation, skipping servers already present (nothing to fix).
func lspChecks(lsp map[string]any) []doctorCheck {
	servers, _ := lsp["servers"].([]any)
	var out []doctorCheck
	for _, raw := range servers {
		srv, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		present, _ := srv["Present"].(bool)
		if present {
			continue
		}
		langID, _ := srv["LangID"].(string)
		bin, _ := srv["Bin"].(string)
		remediation, _ := srv["Remediation"].(string)
		out = append(out, doctorCheck{
			name:        "lsp:" + langID,
			state:       "WARN",
			detail:      bin + " not on PATH — semantic diagnostics for " + langID + " unavailable",
			remediation: remediation,
		})
	}
	return out
}

// byokCheck rolls up the byok.providers list into one check: PASS if any
// provider resolves a key, WARN otherwise (no key means mock-mode, not a
// hard failure — mirrors the reasoner tier).
func byokCheck(byok map[string]any) doctorCheck {
	anyResolved, _ := byok["any_resolved"].(bool)
	providers, _ := byok["providers"].([]any)
	names := unresolvedProviderNames(providers)
	if anyResolved {
		return doctorCheck{name: "byok", state: "PASS", detail: "at least one provider credential resolves"}
	}
	detail := "no provider credential resolves (store or env)"
	if len(names) > 0 {
		detail = "no credential for: " + strings.Join(names, ", ")
	}
	return doctorCheck{
		name:        "byok",
		state:       "WARN",
		detail:      detail,
		remediation: "carina auth login <provider> <api_key>",
	}
}

// unresolvedProviderNames extracts ProviderID from the byok.providers list
// for entries whose Resolved is false, sorted for stable output.
func unresolvedProviderNames(providers []any) []string {
	var out []string
	for _, raw := range providers {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if resolved, _ := p["Resolved"].(bool); resolved {
			continue
		}
		if id, ok := p["ProviderID"].(string); ok && id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// auditVerifyReport mirrors crates/carina-audit's VerifyReport JSON shape
// (kernel.audit.verify, surfaced daemon-side by audit.verify) — the fields
// carina doctor's audit-chain check needs to render pass/fail with the
// tamper reason.
type auditVerifyReport struct {
	OK         bool   `json:"ok"`
	EventCount int    `json:"event_count"`
	BrokenAt   *int   `json:"broken_at"`
	Reason     string `json:"reason"`
	HeadHash   string `json:"head_hash"`
}

// auditChainCheck renders the audit-chain head verification probe (P1.6:
// "audit-chain head verification via existing audit verify"). It is called
// client-side from cmdDoctor rather than folded into daemon.doctor's own
// report, because it needs a concrete session_id to verify — audit.verify
// has no "verify everything" mode, so doctor verifies the most-recently
// touched session as a representative sample of the chain's integrity.
//
//   - hasSessions=false: this machine has no sessions yet, so there is
//     nothing to verify. That is not a failure of doctor or the daemon — a
//     fresh install is expected to have an empty audit chain — so it must
//     not read as WARN/FAIL (TestAuditChainCheckNoSessionsIsInfoNotWarn).
//   - rpcErr=true: the audit.verify call itself errored (daemon reachable
//     but the RPC failed) — that must render FAIL, not be silently skipped,
//     since a working daemon that cannot verify its own audit chain is
//     itself a finding.
//   - raw decodes as auditVerifyReport: OK=true renders PASS; OK=false
//     renders FAIL with the tamper reason surfaced verbatim so an operator
//     can see exactly what broke.
func auditChainCheck(raw []byte, hasSessions bool, rpcErr bool) doctorCheck {
	if !hasSessions {
		return doctorCheck{name: "audit_chain", state: "SKIP", detail: "no sessions yet — nothing to verify"}
	}
	if rpcErr {
		return doctorCheck{
			name:        "audit_chain",
			state:       "FAIL",
			detail:      "audit.verify RPC failed",
			remediation: "check daemon logs; if the kernel is unreachable, run: carina-daemon &",
		}
	}
	var report auditVerifyReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return doctorCheck{
			name:        "audit_chain",
			state:       "FAIL",
			detail:      "audit.verify returned an unparsable report: " + err.Error(),
			remediation: "check daemon logs; if the kernel is unreachable, run: carina-daemon &",
		}
	}
	if report.OK {
		return doctorCheck{
			name:   "audit_chain",
			state:  "PASS",
			detail: fmt.Sprintf("hash chain intact (%d events, head %s)", report.EventCount, report.HeadHash),
		}
	}
	detail := "audit hash chain verification failed"
	if report.Reason != "" {
		detail = report.Reason
	}
	return doctorCheck{
		name:        "audit_chain",
		state:       "FAIL",
		detail:      detail,
		remediation: "run: carina audit verify <session_id> for full detail; the chain must not be trusted until resolved",
	}
}

// doctorTier rolls up a decoded daemon.doctor report's checks into the
// tri-state overall verdict: any FAIL-tier check outranks any WARN-tier
// check, which outranks an all-PASS report.
func doctorTier(report map[string]any) string {
	hasFail := false
	hasWarn := false
	for _, chk := range doctorChecks(report) {
		switch chk.state {
		case "FAIL":
			hasFail = true
		case "WARN":
			hasWarn = true
		}
	}
	switch {
	case hasFail:
		return "FAIL"
	case hasWarn:
		return "WARN"
	default:
		return "PASS"
	}
}

// doctorExitCode maps a decoded daemon.doctor report to the P1.5 governance
// exit-code enum's raw int: FAIL -> 1 (runtime error — the daemon itself is
// unhealthy, distinct from a policy/approval outcome), WARN -> 6
// (degraded-partial), PASS -> 0. Kept as int (rather than tui.Outcome
// directly) because it is doctor's own internal render-time classification;
// doctorOutcome adapts it to tui.Outcome for classifyExitCode.
func doctorExitCode(report map[string]any) int {
	switch doctorTier(report) {
	case "FAIL":
		return 1
	case "WARN":
		return 6
	default:
		return 0
	}
}

// doctorOutcome adapts doctorTier to the shared tui.Outcome enum
// (OutcomeOK/OutcomeDegradedPartial/OutcomeRuntimeError) for
// classifyExitCode — P1.5 requires every command reuse this one enum
// rather than inventing a second.
func doctorOutcome(report map[string]any) tui.Outcome {
	switch doctorTier(report) {
	case "FAIL":
		return tui.OutcomeRuntimeError
	case "WARN":
		return tui.OutcomeDegradedPartial
	default:
		return tui.OutcomeOK
	}
}
