package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPostEditDiagnostics: when an edit puts a file into a broken state, the
// patch observation must carry the checker's diagnostics (the self-correction
// feedback loop); a clean edit must stay quiet.
func TestPostEditDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit m.py")

	// Seed a valid module and read it so the write passes the provenance guard.
	if err := os.WriteFile(filepath.Join(ws, "m.py"), []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.executeAction(sess, task, &action{Tool: "read", Path: "m.py"})

	// A syntactically broken edit must surface diagnostics.
	broken := d.executeAction(sess, task, &action{Tool: "patch", Path: "m.py", Content: "def broken(:\n"})
	if !strings.Contains(broken, "diagnostics") {
		t.Fatalf("broken edit must surface diagnostics, got: %s", broken)
	}

	// A clean follow-up edit (provenance already recorded by the prior patch)
	// must NOT carry diagnostics.
	ok := d.executeAction(sess, task, &action{Tool: "patch", Path: "m.py", Content: "y = 2\n"})
	if strings.Contains(ok, "diagnostics") {
		t.Fatalf("clean edit must not surface diagnostics, got: %s", ok)
	}
}

// TestDiagnosticsDeltaIgnoresLineShiftOnly reproduces the exact bug the
// cline-absorption adversarial review caught: a naive line-number-keyed diff
// would treat a pre-existing error as "newly introduced" whenever an
// unrelated edit elsewhere in the file shifts its line number. Same message,
// different line — must not appear in the delta.
func TestDiagnosticsDeltaIgnoresLineShiftOnly(t *testing.T) {
	before := "m.go:5:3: syntax error: unexpected newline, expecting }"
	after := "m.go:8:3: syntax error: unexpected newline, expecting }"
	if got := diagnosticsDelta(before, after); got != "" {
		t.Fatalf("a pre-existing error whose line number merely shifted must not be reported as new, got: %q", got)
	}
}

// TestDiagnosticsDeltaReportsGenuinelyNewError is the companion positive
// case: a real new diagnostic introduced by the edit must still surface,
// alongside a pre-existing shifted one that must not.
func TestDiagnosticsDeltaReportsGenuinelyNewError(t *testing.T) {
	before := "m.go:5:3: syntax error: unexpected newline, expecting }"
	after := "m.go:5:3: syntax error: unexpected newline, expecting }\nm.go:10:1: syntax error: unexpected EOF"
	got := diagnosticsDelta(before, after)
	if strings.Contains(got, "unexpected newline") {
		t.Fatalf("pre-existing unshifted error must not be reported as new, got: %q", got)
	}
	if !strings.Contains(got, "unexpected EOF") {
		t.Fatalf("genuinely new error must be reported, got: %q", got)
	}
}

// TestDiagnosticsDeltaPythonMultilineBlockLineShift covers py_compile's
// multi-line "File ..., line N" + snippet + caret + SyntaxError format: the
// whole block must be treated as one unit, and a shifted line number alone
// must not make it look new.
func TestDiagnosticsDeltaPythonMultilineBlockLineShift(t *testing.T) {
	before := "  File \"m.py\", line 5\n    x =\n      ^\nSyntaxError: invalid syntax"
	after := "  File \"m.py\", line 8\n    x =\n      ^\nSyntaxError: invalid syntax"
	if got := diagnosticsDelta(before, after); got != "" {
		t.Fatalf("python diagnostic block that only shifted line number must not be reported as new, got: %q", got)
	}
}

func TestDiagnosticsDeltaCleanAfterIsEmpty(t *testing.T) {
	if got := diagnosticsDelta("m.go:5:3: some old error", ""); got != "" {
		t.Fatalf("a clean post-edit state (no diagnostics) must produce an empty delta, got: %q", got)
	}
}

func TestDiagnosticsDeltaFirstIntroductionReportsEverything(t *testing.T) {
	after := "m.go:5:3: syntax error: unexpected newline, expecting }"
	if got := diagnosticsDelta("", after); got != after {
		t.Fatalf("with no pre-edit diagnostics, the full post-edit output must be reported as new, got: %q", got)
	}
}
