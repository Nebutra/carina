package toolnorm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Path expansion ---------------------------------------------------

func TestCanonicalizeExpandsRelativePathToAbsolute(t *testing.T) {
	root := "/workspace/proj"
	got := Canonicalize([]string{"cat", "sub/file.txt"}, root)
	want := "/workspace/proj/sub/file.txt"
	if len(got.Argv) != 2 || got.Argv[1] != want {
		t.Fatalf("relative path not expanded: got Argv=%v, want [cat %s]", got.Argv, want)
	}
}

func TestCanonicalizeExpandsHomeTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir available")
	}
	got := Canonicalize([]string{"cat", "~/notes.txt"}, "/workspace/proj")
	want := filepath.Join(home, "notes.txt")
	if len(got.Argv) != 2 || got.Argv[1] != want {
		t.Fatalf("~ not expanded: got Argv=%v, want [cat %s]", got.Argv, want)
	}
}

func TestCanonicalizeLeavesAbsolutePathUnchanged(t *testing.T) {
	got := Canonicalize([]string{"cat", "/etc/passwd"}, "/workspace/proj")
	if len(got.Argv) != 2 || got.Argv[1] != "/etc/passwd" {
		t.Fatalf("absolute path was rewritten: got Argv=%v", got.Argv)
	}
}

func TestCanonicalizeExpandsBareFilenameThatExistsInWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Canonicalize([]string{"cat", "hello.txt"}, root)
	want := filepath.Join(root, "hello.txt")
	if len(got.Argv) != 2 || got.Argv[1] != want {
		t.Fatalf("bare filename with no separator that exists in the workspace was not expanded: got Argv=%v, want [cat %s]", got.Argv, want)
	}
	if got.Command != "cat "+want {
		t.Fatalf("Command = %q, want %q", got.Command, "cat "+want)
	}
}

func TestCanonicalizeLeavesOpaqueSubcommandTokenUnexpanded(t *testing.T) {
	// "npm test" must not be mangled into an absolute-path rewrite of
	// "test" just because it has no leading dash: "test" does not exist
	// as a file/dir under the workspace root, so it stays opaque.
	root := t.TempDir()
	got := Canonicalize([]string{"npm", "test"}, root)
	if len(got.Argv) != 2 || got.Argv[1] != "test" {
		t.Fatalf("opaque subcommand token was rewritten: got Argv=%v", got.Argv)
	}
}

func TestCanonicalizeExpandsDotSlashPrefix(t *testing.T) {
	got := Canonicalize([]string{"cat", "./file.txt"}, "/workspace/proj")
	want := "/workspace/proj/file.txt"
	if len(got.Argv) != 2 || got.Argv[1] != want {
		t.Fatalf("./ prefix not expanded: got Argv=%v, want [cat %s]", got.Argv, want)
	}
}

// --- Wrapper/env-prefix stripping (fixed point) ------------------------

func TestCanonicalizeStripsTimeoutWrapper(t *testing.T) {
	got := Canonicalize([]string{"timeout", "30", "npm", "test"}, "/ws")
	if got.WrapperStripped != "npm test" {
		t.Fatalf("WrapperStripped = %q, want %q", got.WrapperStripped, "npm test")
	}
	// The wrapper still actually runs — Argv must NOT drop it.
	if !strings.Contains(strings.Join(got.Argv, " "), "timeout 30 npm test") {
		t.Fatalf("Argv must preserve the wrapper for execution, got %v", got.Argv)
	}
}

func TestCanonicalizeStripsNiceWrapper(t *testing.T) {
	got := Canonicalize([]string{"nice", "-n", "5", "npm", "test"}, "/ws")
	if got.WrapperStripped != "npm test" {
		t.Fatalf("WrapperStripped = %q, want %q", got.WrapperStripped, "npm test")
	}
}

func TestCanonicalizeStripsEnvWrapper(t *testing.T) {
	got := Canonicalize([]string{"env", "npm", "test"}, "/ws")
	if got.WrapperStripped != "npm test" {
		t.Fatalf("WrapperStripped = %q, want %q", got.WrapperStripped, "npm test")
	}
}

func TestCanonicalizeStripsInlineEnvPrefixAssignments(t *testing.T) {
	got := Canonicalize([]string{"FOO=bar", "BAZ=qux", "npm", "test"}, "/ws")
	if got.WrapperStripped != "npm test" {
		t.Fatalf("WrapperStripped = %q, want %q", got.WrapperStripped, "npm test")
	}
	if got.EnvPrefix["FOO"] != "bar" || got.EnvPrefix["BAZ"] != "qux" {
		t.Fatalf("EnvPrefix = %#v, want FOO=bar BAZ=qux", got.EnvPrefix)
	}
	// Execution argv must still carry the env assignments.
	joined := strings.Join(got.Argv, " ")
	if !strings.Contains(joined, "FOO=bar") || !strings.Contains(joined, "BAZ=qux") {
		t.Fatalf("Argv must preserve inline env assignments for execution, got %v", got.Argv)
	}
}

func TestCanonicalizeFixedPointPeelsMultipleWrappers(t *testing.T) {
	got := Canonicalize([]string{"timeout", "30", "nice", "-n", "5", "npm", "test"}, "/ws")
	if got.WrapperStripped != "npm test" {
		t.Fatalf("fixed-point wrapper peeling failed: WrapperStripped = %q, want %q", got.WrapperStripped, "npm test")
	}
}

func TestCanonicalizeDoesNotStripSudo(t *testing.T) {
	// sudo changes privilege and must classify as itself — it is
	// deliberately NOT a no-op-for-policy wrapper.
	got := Canonicalize([]string{"sudo", "rm", "-rf", "/"}, "/ws")
	if !strings.HasPrefix(got.WrapperStripped, "sudo") {
		t.Fatalf("sudo must not be stripped from the classification target: WrapperStripped = %q", got.WrapperStripped)
	}
}

// --- wrapperArgCount: malformed/adversarial wrapper invocations --------

func TestCanonicalizeTimeoutWithoutDurationDoesNotSwallowCommandVerb(t *testing.T) {
	// "timeout sudo rm -rf /" omits the DURATION operand entirely — the
	// naive implementation treats "sudo" as the duration and strips it
	// along with "timeout", hiding sudo (a privilege change) from
	// classification.
	got := Canonicalize([]string{"timeout", "sudo", "rm", "-rf", "/"}, "/ws")
	if !strings.HasPrefix(got.WrapperStripped, "sudo") {
		t.Fatalf("timeout without a duration must not swallow the wrapped command's own leading token: WrapperStripped = %q, want to start with %q", got.WrapperStripped, "sudo")
	}
}

func TestCanonicalizeTimeoutFlagWithSeparateValueDoesNotShiftDuration(t *testing.T) {
	// "-s KILL" is a two-token flag (signal name is a separate argv slot);
	// the naive implementation only skips one token per "-"-prefixed flag,
	// so it treats "KILL" as the flag's own token (skipped as a flag) then
	// "30" (the real duration) is also consumed as if it were a second
	// flag value, leaving "30" glued onto WrapperStripped.
	got := Canonicalize([]string{"timeout", "-s", "KILL", "30", "npm", "install", "evil-package"}, "/ws")
	want := "npm install evil-package"
	if got.WrapperStripped != want {
		t.Fatalf("timeout -s KILL 30 ... : WrapperStripped = %q, want %q", got.WrapperStripped, want)
	}
}

func TestCanonicalizeNiceFlagArgumentNotConfusedWithWrappedCommand(t *testing.T) {
	// "nice -n git push ..." (adjustment value omitted) must not consume
	// "git" as the -n adjustment — nice -n always takes the very next
	// token as its numeric adjustment operand only when it is present as
	// intended; a malformed/adversarial invocation without a numeric
	// adjustment must leave the wrapped command's own verb intact.
	got := Canonicalize([]string{"nice", "-n", "git", "push", "origin", "main", "--force"}, "/ws")
	want := "git push origin main --force"
	if got.WrapperStripped != want {
		t.Fatalf("nice -n <non-numeric>: WrapperStripped = %q, want %q (git must not be swallowed as the adjustment)", got.WrapperStripped, want)
	}
}

func TestCanonicalizeEnvFlagWithSeparateValueDoesNotLeakOperand(t *testing.T) {
	// "env -u PATH sudo rm -rf /": -u takes a following NAME operand
	// (unlike bare boolean flags such as -i); the naive implementation
	// only skips "-u" itself and leaves "PATH" glued onto
	// WrapperStripped's front, corrupting the leading token the policy
	// classifier keys on.
	got := Canonicalize([]string{"env", "-u", "PATH", "sudo", "rm", "-rf", "/"}, "/ws")
	want := "sudo rm -rf /"
	if got.WrapperStripped != want {
		t.Fatalf("env -u PATH ...: WrapperStripped = %q, want %q", got.WrapperStripped, want)
	}
}

// --- Classification consistency (the actual policy-closing behavior) ---

func TestCanonicalizeClassificationConsistencyAcrossPhrasing(t *testing.T) {
	// The exact motivating gap: "timeout 30 rm -rf /" and "rm -rf /" must
	// classify identically once canonicalized — today they can classify
	// differently by accident of phrasing because the raw joined argv is
	// used directly.
	bare := Canonicalize([]string{"rm", "-rf", "/"}, "/ws")
	wrapped := Canonicalize([]string{"timeout", "30", "rm", "-rf", "/"}, "/ws")
	if bare.WrapperStripped != wrapped.WrapperStripped {
		t.Fatalf("classification target diverges by phrasing: bare=%q wrapped=%q", bare.WrapperStripped, wrapped.WrapperStripped)
	}
}

func TestCanonicalizeCommandJoinsCanonicalArgvNotRawArgv(t *testing.T) {
	// Command (used for the kernel policy Request + audit) must reflect
	// the path-EXPANDED argv, not the raw argv the model emitted, so
	// "cat sub/file.txt" and "cat /workspace/proj/sub/file.txt" audit
	// identically regardless of which relative form the model used.
	root := "/workspace/proj"
	got := Canonicalize([]string{"cat", "sub/file.txt"}, root)
	want := "cat /workspace/proj/sub/file.txt"
	if got.Command != want {
		t.Fatalf("Command = %q, want %q (canonical Argv joined, not raw argv)", got.Command, want)
	}
}

// --- Validate (side-effect-free, ahead of the kernel decision) ---------

func TestValidateRejectsEmptyCommandAfterStripping(t *testing.T) {
	c := Canonicalize([]string{"timeout", "30"}, "/ws") // wrapper with nothing to wrap
	ok, code, msg := c.Validate()
	if ok {
		t.Fatal("empty command after wrapper-stripping should fail validation")
	}
	if code == "" || msg == "" {
		t.Fatalf("Validate() failure must carry an errorCode and teachable message, got code=%q msg=%q", code, msg)
	}
}

func TestValidateRejectsUnresolvableBinary(t *testing.T) {
	c := Canonicalize([]string{"this-binary-does-not-exist-xyz-12345"}, "/ws")
	ok, code, msg := c.Validate()
	if ok {
		t.Fatal("an argv[0] that does not resolve via exec.LookPath should fail validation")
	}
	if code == "" || msg == "" {
		t.Fatalf("Validate() failure must carry an errorCode and teachable message, got code=%q msg=%q", code, msg)
	}
}

func TestValidateRejectsPathTraversalEscapingWorkspace(t *testing.T) {
	c := Canonicalize([]string{"cat", "../../../../etc/passwd"}, "/workspace/proj")
	ok, code, msg := c.Validate()
	if ok {
		t.Fatal("a ../ traversal escaping workspaceRoot should fail validation")
	}
	if code == "" || msg == "" {
		t.Fatalf("Validate() failure must carry an errorCode and teachable message, got code=%q msg=%q", code, msg)
	}
}

// TestValidateAcceptsDirectAbsolutePathOutsideWorkspace pins the fix for
// the over-blocking regression: a plain absolute-path argument that was
// NEVER relative/traversal-derived (the model wrote "/etc/hosts" directly,
// not "../../../etc/hosts") must NOT be hard-rejected pre-kernel. Whether
// that path should be allowed is a policy decision (d.kern.Request), not a
// syntactic validation failure — Validate()'s job is catching typos/
// traversal-shaped tokens, not making governance calls the kernel is
// supposed to make.
func TestValidateAcceptsDirectAbsolutePathOutsideWorkspace(t *testing.T) {
	c := Canonicalize([]string{"cat", "/etc/hosts"}, "/workspace/proj")
	ok, code, msg := c.Validate()
	if !ok {
		t.Fatalf("a directly-written absolute path outside the workspace must pass Validate() (the kernel decides policy, not pre-kernel validation), got code=%q msg=%q", code, msg)
	}
}

func TestValidateAcceptsOrdinaryInWorkspaceCommand(t *testing.T) {
	c := Canonicalize([]string{"npm", "test"}, "/workspace/proj")
	ok, _, msg := c.Validate()
	if !ok {
		t.Fatalf("ordinary command should validate ok, got message %q", msg)
	}
}
