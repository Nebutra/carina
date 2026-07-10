// Package toolnorm canonicalizes tool-call argv/paths before they reach the
// kernel policy decision and the audit chain (P1.2 of
// docs/plans/agent-cli-productization.md §3 Phase 1): path expansion
// (~/relative -> absolute), fixed-point stripping of env-prefixes and
// no-op-for-policy wrapper commands (timeout, nice, env), so
// crates/carina-policy matches canonical forms only and the audit chain
// records the canonical form actually authorized — not whatever raw string
// the model happened to emit.
//
// This is pure, side-effect-free string/path transformation with zero
// daemon state, split into its own package (rather than a free function in
// go/daemon/agent.go) so both go/daemon and a future CLI-side normalizer can
// share it without go/daemon pulling in kernel-adjacent test fixtures just
// to unit-test string parsing.
package toolnorm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Canonical is the result of canonicalizing a tool-call argv.
type Canonical struct {
	// Argv is the fully-resolved argv: paths expanded, wrappers separated
	// out but NOT dropped (the wrapper still actually runs).
	Argv []string
	// Command is the single joined string used for policy/audit — matches
	// today's `strings.Join(argv, " ")` call-site shape, but on the
	// path-expanded, canonical Argv.
	Command string
	// WrapperStripped is the classification-relevant inner command (e.g.
	// "npm test" extracted from "timeout 30 npm test") for
	// crates/carina-policy to classify against.
	WrapperStripped string
	// EnvPrefix captures inline FOO=bar prefix assignments stripped for
	// classification but preserved in the actual executed Argv.
	EnvPrefix map[string]string

	// workspaceRoot is retained (unexported) so Validate() can re-check
	// path-traversal against the same root Canonicalize used, without the
	// caller having to pass it twice.
	workspaceRoot string
	// wasRelative[i] records whether Argv[i] originated from a relative or
	// ~-prefixed token that expandPath resolved to an absolute path (as
	// opposed to a token the model wrote as an absolute path directly).
	// Validate()'s workspace-escape check only applies to indices where
	// this is true — a directly-absolute argument outside workspaceRoot is
	// a policy decision for the kernel, not a syntactic validation
	// failure; only a traversal that expandPath had to resolve is
	// syntactically suspicious enough to pre-empt the kernel decision.
	wasRelative []bool
}

// noOpWrappers is the fixed set of commands that do not change what
// crates/carina-policy should classify against: they alter scheduling,
// niceness, or environment, not privilege or effect. sudo is deliberately
// excluded — it changes privilege and must classify as itself.
var noOpWrappers = map[string]bool{
	"timeout": true,
	"nice":    true,
	"env":     true,
}

// isEnvAssignment reports whether tok looks like an inline FOO=bar prefix
// assignment (a bare identifier, then '=', then a value with no leading
// dash/slash that would make it a path or flag).
func isEnvAssignment(tok string) (name, value string, ok bool) {
	i := strings.IndexByte(tok, '=')
	if i <= 0 {
		return "", "", false
	}
	name = tok[:i]
	for j, r := range name {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		isDigit := r >= '0' && r <= '9'
		if j == 0 && !isAlpha {
			return "", "", false
		}
		if !isAlpha && !isDigit {
			return "", "", false
		}
	}
	return name, tok[i+1:], true
}

// isEnvLikeAssignment reports whether tok is shaped like a FOO=bar inline
// assignment, so expandPath doesn't mangle a value such as PATH=/usr/bin by
// treating the whole token as a workspace-relative path.
func isEnvLikeAssignment(tok string) bool {
	_, _, ok := isEnvAssignment(tok)
	return ok
}

// looksLikeDuration reports whether tok is shaped like a `timeout` DURATION
// operand: a plain number, optionally with a single trailing unit suffix
// (s/m/h/d), e.g. "30", "0.5", "2m". Used to distinguish a genuine DURATION
// operand from the wrapped command's own leading token in a malformed/
// adversarial "timeout <non-numeric>..." invocation (no DURATION supplied).
func looksLikeDuration(tok string) bool {
	if tok == "" {
		return false
	}
	body := tok
	if last := tok[len(tok)-1]; last == 's' || last == 'm' || last == 'h' || last == 'd' {
		body = tok[:len(tok)-1]
	}
	if body == "" {
		return false
	}
	seenDigit, seenDot := false, false
	for _, r := range body {
		switch {
		case r >= '0' && r <= '9':
			seenDigit = true
		case r == '.' && !seenDot:
			seenDot = true
		default:
			return false
		}
	}
	return seenDigit
}

// looksLikeNumericAdjustment reports whether tok is shaped like `nice`'s -n
// ADJUSTMENT operand: an optional sign followed by digits, e.g. "5", "-5",
// "+10". Used to distinguish a genuine ADJUSTMENT from the wrapped command's
// own leading token in a malformed/adversarial "nice -n <non-numeric>..."
// invocation (no ADJUSTMENT supplied after -n).
func looksLikeNumericAdjustment(tok string) bool {
	if tok == "" {
		return false
	}
	body := tok
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "" {
		return false
	}
	for _, r := range body {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// wrapperValueFlags lists, per wrapper, the flags that consume the
// following argv token as their own operand (rather than being a bare
// boolean flag) — so the flag-skipping loop in wrapperArgCount does not
// mistake that operand for the start of the wrapped command. Best-effort:
// covers the flags most likely to appear ahead of a wrapped command, not
// every flag either tool accepts.
var wrapperValueFlags = map[string]map[string]bool{
	"timeout": {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
	"env":     {"-u": true, "--unset": true, "-C": true, "--chdir": true, "-S": true, "--split-string": true},
}

// wrapperArgCount returns how many argv slots after the wrapper name itself
// are consumed by the wrapper's own flags/operands (not the wrapped
// command), so peeling can skip past them. Best-effort: only handles the
// forms the fixed-point loop actually needs to peel. Conservative by
// design: when a flag or operand this wrapper is known to require is
// missing (a malformed/adversarial invocation), stop skipping rather than
// guess — swallowing the wrapped command's own leading token as a wrapper
// operand is exactly the corruption this function must never produce.
func wrapperArgCount(wrapper string, rest []string) int {
	switch wrapper {
	case "timeout":
		// timeout [OPTIONS] DURATION COMMAND... — skip flags (accounting
		// for flags that take a separate value token), then the duration
		// operand, but ONLY if the next token actually looks like a
		// duration; otherwise DURATION was omitted and what follows is the
		// wrapped command itself.
		n := 0
		for n < len(rest) && strings.HasPrefix(rest[n], "-") {
			flag := rest[n]
			if eq := strings.IndexByte(flag, '='); eq >= 0 {
				flag = flag[:eq] // --signal=KILL form carries its own value
				n++
				continue
			}
			n++
			if wrapperValueFlags["timeout"][flag] {
				if n >= len(rest) {
					return n
				}
				n++
			}
		}
		if n < len(rest) && looksLikeDuration(rest[n]) {
			n++ // duration
		}
		return n
	case "nice":
		// nice [-n ADJUSTMENT] COMMAND...
		n := 0
		for n < len(rest) {
			if rest[n] == "-n" {
				if n+1 < len(rest) && looksLikeNumericAdjustment(rest[n+1]) {
					n += 2
					continue
				}
				// -n without a numeric adjustment following: malformed/
				// adversarial input. Skip past "-n" itself (it is still a
				// nice flag, not part of the wrapped command) but stop
				// before consuming the next token — do not swallow the
				// wrapped command's own leading token as the adjustment.
				return n + 1
			}
			if strings.HasPrefix(rest[n], "-n") && len(rest[n]) > 2 && looksLikeNumericAdjustment(rest[n][2:]) {
				n++ // "-n5" glued form
				continue
			}
			if strings.HasPrefix(rest[n], "-") {
				n++
				continue
			}
			break
		}
		return n
	case "env":
		// env [-i] [-u NAME] [NAME=VALUE]... COMMAND... — env's own inline
		// assignments are handled by the outer env-prefix loop; here we
		// skip flags, accounting for flags that consume a separate value
		// token (-u NAME, -C dir, ...) so that operand is not left glued
		// onto WrapperStripped's front.
		n := 0
		for n < len(rest) && strings.HasPrefix(rest[n], "-") {
			flag := rest[n]
			if eq := strings.IndexByte(flag, '='); eq >= 0 {
				flag = flag[:eq]
			}
			n++
			if wrapperValueFlags["env"][flag] {
				if n >= len(rest) {
					return n
				}
				n++
			}
		}
		return n
	default:
		return 0
	}
}

// Canonicalize resolves argv into a policy/audit-stable canonical form:
// relative and ~-prefixed paths become absolute (resolved against
// workspaceRoot / the user's home dir respectively), and env-prefix /
// no-op-for-policy wrapper commands (timeout, nice, env) are peeled to a
// fixed point to produce WrapperStripped, the string crates/carina-policy
// classifies against. Argv itself is NOT stripped of wrappers — they still
// actually execute — only paths within it are expanded.
func Canonicalize(argv []string, workspaceRoot string) Canonical {
	expanded := make([]string, len(argv))
	wasRelative := make([]bool, len(argv))
	for i, a := range argv {
		expanded[i], wasRelative[i] = expandPath(a, workspaceRoot, i)
	}

	envPrefix := map[string]string{}
	rest := expanded
	// Fixed point: repeatedly strip a leading env assignment or a leading
	// no-op wrapper (plus its own args) until neither applies.
	for {
		if len(rest) == 0 {
			break
		}
		if name, value, ok := isEnvAssignment(rest[0]); ok {
			envPrefix[name] = value
			rest = rest[1:]
			continue
		}
		if noOpWrappers[rest[0]] {
			skip := 1 + wrapperArgCount(rest[0], rest[1:])
			if skip > len(rest) {
				skip = len(rest)
			}
			rest = rest[skip:]
			continue
		}
		break
	}

	wrapperStripped := strings.Join(rest, " ")
	command := strings.Join(expanded, " ")

	return Canonical{
		Argv:            expanded,
		Command:         command,
		WrapperStripped: wrapperStripped,
		EnvPrefix:       envPrefix,
		workspaceRoot:   workspaceRoot,
		wasRelative:     wasRelative,
	}
}

// expandPath resolves a single argv token to an absolute path when it looks
// like a path expression (~-prefixed, ./ or ../ prefixed, or otherwise
// relative-looking with a path separator). argv[0] (the command name
// itself, index 0) is never treated as a path — it is resolved via
// exec.LookPath at Validate() time instead. The second return value reports
// whether tok was relative/~-prefixed (i.e. expandPath itself resolved it
// to an absolute form) as opposed to already being a directly-absolute
// token the model wrote out itself — Validate()'s workspace-escape check
// uses this to only flag traversal-derived paths, not every absolute
// argument outside workspaceRoot (that is a kernel policy decision).
func expandPath(tok string, workspaceRoot string, index int) (string, bool) {
	if index == 0 {
		return tok, false
	}
	if filepath.IsAbs(tok) {
		return tok, false
	}
	switch {
	case tok == "~" || strings.HasPrefix(tok, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return tok, false
		}
		if tok == "~" {
			return home, true
		}
		return filepath.Join(home, tok[2:]), true
	case strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") || tok == ".." || tok == ".":
		if workspaceRoot == "" {
			return tok, false
		}
		return filepath.Join(workspaceRoot, tok), true
	case !strings.HasPrefix(tok, "-") && strings.ContainsRune(tok, '/') && !isEnvLikeAssignment(tok):
		// A bare relative path with an internal separator (e.g.
		// "sub/file.txt") is path-shaped without needing a "./" prefix.
		// Flags (leading "-") and inline FOO=bar assignments (handled by
		// the wrapper-peeling loop, and which may legitimately contain
		// "/" in their value, e.g. PATH=/usr/bin) are left alone.
		if workspaceRoot == "" {
			return tok, false
		}
		return filepath.Join(workspaceRoot, tok), true
	case !strings.HasPrefix(tok, "-") && workspaceRoot != "" && !isEnvLikeAssignment(tok):
		// A bare token with no separator at all (e.g. "hello.txt") is
		// ambiguous — it could be a workspace-relative filename or an
		// opaque subcommand/argument like "test" in "npm test". Resolve
		// the ambiguity empirically: only expand it if it actually exists
		// as a file/dir under workspaceRoot, so "npm test" is untouched
		// (no such path on disk) while "cat hello.txt" canonicalizes to
		// the absolute form the audit chain should record. Side-effect-free
		// (Stat is a read).
		if _, err := os.Stat(filepath.Join(workspaceRoot, tok)); err == nil {
			return filepath.Join(workspaceRoot, tok), true
		}
		return tok, false
	default:
		// Flags (-x, --flag) and bare non-path tokens (subcommand names,
		// wrapper args already handled above) pass through unchanged —
		// only clearly path-shaped tokens are expanded to avoid mangling
		// e.g. "npm test" into an absolute-path rewrite of "test".
		return tok, false
	}
}

// Validate runs cheap, side-effect-free syntactic checks ahead of the
// kernel policy decision: empty command after wrapper-stripping, an argv[0]
// that doesn't resolve via exec.LookPath, or a path escaping workspaceRoot
// via ../ traversal. On failure it returns a teachable tool_result-style
// message (matching the existing "error: ..."/"DENIED: ..." string-return
// convention) so the model self-corrects without ever reaching
// d.kern.Request — no permission.request is published and no human is
// asked to approve a typo.
func (c Canonical) Validate() (ok bool, errorCode, message string) {
	if strings.TrimSpace(c.WrapperStripped) == "" {
		return false, "empty_command", "the command is empty after stripping wrappers (timeout/nice/env) — nothing to run"
	}
	if len(c.Argv) == 0 {
		return false, "empty_command", "the command has no argv[0] to execute"
	}
	bin := c.Argv[0]
	if !strings.ContainsAny(bin, "/\\") {
		if _, err := exec.LookPath(bin); err != nil {
			return false, "binary_not_found", "argv[0] " + bin + " does not resolve on PATH — check the command name for typos"
		}
	} else if _, err := os.Stat(bin); err != nil {
		if _, err := exec.LookPath(bin); err != nil {
			return false, "binary_not_found", "argv[0] " + bin + " does not resolve to an executable — check the path for typos"
		}
	}
	if c.workspaceRoot != "" {
		for i, a := range c.Argv {
			if i == 0 {
				continue
			}
			// Only a token expandPath itself resolved from a relative/~-
			// prefixed form (i.e. a ../ traversal or similar) is a
			// syntactic red flag worth pre-empting the kernel decision
			// for. A token the model wrote as an absolute path directly
			// (e.g. "/etc/hosts") is not a typo or traversal — whether
			// it's allowed is exactly the kernel policy decision's job,
			// not a blanket pre-kernel rejection.
			if i < len(c.wasRelative) && !c.wasRelative[i] {
				continue
			}
			if escapesWorkspace(a, c.workspaceRoot) {
				return false, "path_escapes_workspace", "path " + a + " escapes the workspace root " + c.workspaceRoot + " — use a path inside the workspace"
			}
		}
	}
	return true, "", ""
}

// escapesWorkspace reports whether an already-expanded path argument
// resolves outside workspaceRoot. Only applies to paths that were relative
// (contained ../ traversal) before expansion is not tracked here, so this
// re-derives via filepath.Rel on the canonical absolute form: if the
// cleaned absolute path is not workspaceRoot or under it, and it looks like
// it originated from a traversal (kept as absolute already by expandPath
// for genuinely absolute input), flag it.
func escapesWorkspace(absOrTok, workspaceRoot string) bool {
	if !filepath.IsAbs(absOrTok) {
		return false
	}
	root := filepath.Clean(workspaceRoot)
	target := filepath.Clean(absOrTok)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	return rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "..\\")
}
