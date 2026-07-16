package main

import (
	"errors"
	"strings"

	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/tui"
)

// policyDeniedPrefix and userDeniedPrefix are the verbatim prefixes
// agentRun/dispatchAction (go/daemon/agent.go) and the CLI's own "carina
// deny" reason default (main.go) already emit. classifyExitCode string-
// matches on these rather than inventing a new typed error, because the
// daemon reports denials as tool_result/error text over the RPC boundary,
// not as a Go sentinel error the CLI process could errors.Is() against.
//
// usagePrefix is the verbatim prefix every one of run()'s ~44
// "usage: carina ..." errors shares (both fmt.Errorf("usage: ...") and the
// backtick fmt.Errorf(`usage: ...`, cmd) forms) — string-matched for the
// same reason: these are plain Go errors returned directly from run(), not
// worth a ~44-site typed-error migration when one shared, unambiguous
// prefix already uniquely identifies them.
const (
	policyDeniedPrefix = "DENIED by policy:"
	userDeniedPrefix   = "denied by user:"
	usagePrefix        = "usage: "
)

// doctorOutcomeError carries carina doctor's own WARN/FAIL classification
// through the normal error-return path so classifyExitCode can map it to
// the shared tui.Outcome enum, instead of doctor inventing a second exit
// path. Doctor still prints its full report before returning this — the
// error's Error() is only ever surfaced as "carina: <msg>" if something
// upstream forgets to special-case it, so its text stays short and useful.
type doctorOutcomeError struct {
	outcome tui.Outcome
}

func (e *doctorOutcomeError) Error() string {
	if e.outcome == tui.OutcomeDegradedPartial {
		return "doctor: one or more checks degraded (see report above)"
	}
	return "doctor: one or more checks failed (see report above)"
}

// classifyExitCode maps a one-shot command's terminal error into the SAME
// governance exit-code enum go/tui already ships and the interactive shell
// already reuses (tui.Outcome) — P1.5(b) must not invent a second enum.
// nil err classifies as OutcomeOK.
//
// Dial failures (errors.Is(err, rpc.ErrDaemonUnreachable)) classify as
// daemon unreachable; an explicit policy denial from agentRun/dispatchAction
// classifies as policy denied; an explicit user deny (carina deny) classifies
// as user denied; a *doctorOutcomeError carries carina doctor's own
// WARN/FAIL classification; a "usage: ..." error (any of run()'s malformed-
// invocation returns) classifies as OutcomeUsage (exit 2), distinct from a
// genuine runtime failure — the plan's frozen exit-code compatibility
// contract requires this distinction actually be reachable, not just
// documented; anything else falls back to the generic runtime error,
// matching today's uniform os.Exit(1) behavior for the residual case.
func classifyExitCode(err error) tui.Outcome {
	if err == nil {
		return tui.OutcomeOK
	}
	if errors.Is(err, rpc.ErrDaemonUnreachable) {
		return tui.OutcomeDaemonUnreachable
	}
	var doctorErr *doctorOutcomeError
	if errors.As(err, &doctorErr) {
		return doctorErr.outcome
	}
	msg := err.Error()
	if strings.Contains(msg, policyDeniedPrefix) {
		return tui.OutcomePolicyDenied
	}
	if strings.Contains(msg, userDeniedPrefix) {
		return tui.OutcomeUserDenied
	}
	if strings.HasPrefix(msg, usagePrefix) {
		return tui.OutcomeUsage
	}
	// Checked after the policy/user-denied string matches above: a
	// *taskDegradedError whose summary happens to embed the daemon's own
	// governance-denial text must still classify as the more specific
	// policy/user-denied outcome, not collapse into degraded-partial.
	var degradedErr *taskDegradedError
	if errors.As(err, &degradedErr) {
		return tui.OutcomeDegradedPartial
	}
	return tui.OutcomeRuntimeError
}
