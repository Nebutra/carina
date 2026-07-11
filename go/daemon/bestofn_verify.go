package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/toolnorm"
)

// verifyScratchMaxBytes bounds how much of the real workspace
// materializeCandidateWorkspace will copy before giving up rather than
// hanging on a huge or artifact-heavy repo. Verification is best-effort and
// opt-in (only runs when the caller supplies a command); exceeding the cap
// skips verification for that run rather than blocking best_of_n entirely.
const verifyScratchMaxBytes = 256 * 1024 * 1024 // 256MB

// verifyScratchSkipDirs are never copied into a candidate's scratch
// workspace: version control metadata, dependency/build caches, and
// Carina's own state — none are needed to run a project's own build/test
// command, and node_modules/target-style directories are exactly what would
// blow the size cap on an otherwise reasonably-sized repo.
var verifyScratchSkipDirs = map[string]bool{
	".git": true, ".carina": true, "node_modules": true, "target": true,
	"zig-out": true, "dist": true, "vendor": true, ".venv": true, "__pycache__": true,
}

// candidateVerification is the outcome of running a caller-supplied verify
// command against one candidate's proposed changes, materialized into an
// isolated scratch copy of the workspace — never the real one, and never
// through the audited patch pipeline (only the judge-selected winner ever
// reaches kernel.patch.propose).
type candidateVerification struct {
	Ran     bool // false if verification was skipped (no command supplied, or the workspace exceeded the size cap)
	Passed  bool
	Output  string
	SkipWhy string
}

// materializeCandidateWorkspace copies the real workspace into a fresh temp
// directory (skipping verifyScratchSkipDirs), then overwrites/creates every
// path in files with its candidate content — giving the verify command a
// realistic, isolated tree without ever touching the real workspace or
// requiring the change to pass through governance twice (the copy operation
// itself is not a governed effect; nothing here is visible to, or reachable
// from, the real session). Returns ok=false with a reason if the workspace
// exceeds verifyScratchMaxBytes; the caller should skip verification, not
// fail the whole best_of_n run.
func materializeCandidateWorkspace(root string, files []kernel.FileChange) (dir string, cleanup func(), ok bool, reason string, err error) {
	scratch, err := os.MkdirTemp("", "carina-bestofn-verify-*")
	if err != nil {
		return "", func() {}, false, "", err
	}
	cleanup = func() { _ = os.RemoveAll(scratch) }

	var copied int64
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)
		if info.IsDir() {
			if verifyScratchSkipDirs[base] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(scratch, rel), 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		copied += info.Size()
		if copied > verifyScratchMaxBytes {
			return errScratchTooLarge
		}
		return copyFile(path, filepath.Join(scratch, rel), info.Mode())
	})
	if walkErr == errScratchTooLarge {
		cleanup()
		return "", func() {}, false, fmt.Sprintf("workspace exceeds the %dMB verification scratch-copy cap", verifyScratchMaxBytes/(1024*1024)), nil
	}
	if walkErr != nil {
		cleanup()
		return "", func() {}, false, "", walkErr
	}

	for _, f := range files {
		abs := resolveIn(scratch, f.Path)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			cleanup()
			return "", func() {}, false, "", err
		}
		if err := os.WriteFile(abs, []byte(f.NewContent), 0o644); err != nil {
			cleanup()
			return "", func() {}, false, "", err
		}
	}
	return scratch, cleanup, true, "", nil
}

var errScratchTooLarge = fmt.Errorf("carina: verification scratch copy exceeded size cap")

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// verifyCandidate materializes one candidate's proposed files into an
// isolated scratch workspace and runs argv there, gated by the SAME
// CommandExec capability request + risk classification the normal "run"
// tool uses — command execution is command execution regardless of which
// directory it happens in, and this project's governance model does not
// carve out an exception for "it's just a scratch copy". The resource
// string is tagged so the audit trail is unambiguous about what ran and why.
func (d *Daemon) verifyCandidate(ctx context.Context, sess *sessionstore.Session, task *scheduler.Task, argv []string, candIndex int, files []kernel.FileChange) candidateVerification {
	scratch, cleanup, ok, skipWhy, err := materializeCandidateWorkspace(sess.WorkspaceRoot, files)
	if err != nil {
		return candidateVerification{Ran: false, SkipWhy: "scratch workspace setup failed: " + err.Error()}
	}
	defer cleanup()
	if !ok {
		return candidateVerification{Ran: false, SkipWhy: skipWhy}
	}

	canon := toolnorm.Canonicalize(argv, scratch)
	if valid, _, msg := canon.Validate(); !valid {
		return candidateVerification{Ran: false, SkipWhy: "invalid verify command: " + msg}
	}
	classifyAs := canon.WrapperStripped
	resource := fmt.Sprintf("best_of_n_verify:candidate_%d:%s", candIndex, classifyAs)
	dec, err := d.kern.Request(sess.SessionID, "CommandExec", resource, task.TaskID)
	if err != nil {
		return candidateVerification{Ran: false, SkipWhy: "governance error: " + err.Error()}
	}
	switch dec.Decision {
	case "denied":
		return candidateVerification{Ran: false, SkipWhy: "denied by policy: " + dec.Reason}
	case "requires_approval":
		approved, granted := d.resolveApprovalOrEscalate(sess, task, dec, "CommandExec", resource, fmt.Sprintf("best_of_n verify candidate %d: %s", candIndex, classifyAs))
		if !granted {
			return candidateVerification{Ran: false, SkipWhy: "verify command requires approval (not granted): " + dec.Reason}
		}
		dec = approved
	}

	d.record(sess.SessionID, "CommandStarted", task.TaskID, "zig", map[string]any{
		"best_of_n_verify": true, "candidate_index": candIndex, "command": canon.Command,
	}, dec.DecisionID)

	result, runErr := d.tools.RunContext(ctx, canon.Argv, scratch, 2*time.Minute, d.egressEnv(), d.sandbox.Load())
	if runErr != nil {
		d.record(sess.SessionID, "CommandExited", task.TaskID, "zig", map[string]any{
			"best_of_n_verify": true, "candidate_index": candIndex, "exit_code": -1, "error": runErr.Error(),
		}, "")
		return candidateVerification{Ran: true, Passed: false, Output: "verify runner error: " + runErr.Error()}
	}
	stdout := strings.Join(result.Stdout, "\n")
	var out strings.Builder
	fmt.Fprintf(&out, "exit=%d\n%s", result.ExitCode, stdout)
	if len(result.Stderr) > 0 {
		fmt.Fprintf(&out, "\n[stderr] %s", strings.Join(result.Stderr, "\n"))
	}
	d.record(sess.SessionID, "CommandExited", task.TaskID, "zig", map[string]any{
		"best_of_n_verify": true, "candidate_index": candIndex, "exit_code": result.ExitCode,
		"duration_ms": result.DurationMs, "timed_out": result.TimedOut,
	}, "")
	return candidateVerification{
		Ran:    true,
		Passed: !result.TimedOut && result.ExitCode == 0,
		Output: truncate(out.String(), 1500),
	}
}
