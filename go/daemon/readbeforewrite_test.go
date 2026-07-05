package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadBeforeWriteGuard verifies the dirty-write guard's DECISIONS (which run
// before the patch engine touches disk, so this does not depend on the Zig
// carina-patch-native tool being built): a blind overwrite of an existing,
// never-read file is denied; a stale overwrite (file drifted since read) is
// denied; a read clears the guard; a brand-new file needs no prior read.
func TestReadBeforeWriteGuard(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("original\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit")

	// 1. Blind overwrite of an existing, never-read file -> guard denies.
	obs := d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "hacked\n"})
	if !strings.Contains(obs, "read it first") {
		t.Fatalf("blind overwrite must be denied, got: %s", obs)
	}

	// 2. After reading, the guard clears (it no longer says "read it first";
	//    the apply may still fail if the Zig tool is absent — not our concern).
	d.executeAction(sess, task, &action{Tool: "read", Path: "a.txt"})
	obs = d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "clean edit\n"})
	if strings.Contains(obs, "read it first") || strings.Contains(obs, "changed since") {
		t.Fatalf("read-then-patch must pass the guard, got: %s", obs)
	}

	// 3. Drift after read -> stale write denied by the guard.
	d.executeAction(sess, task, &action{Tool: "read", Path: "a.txt"})
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("someone else edited\n"), 0o600)
	obs = d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "stomp\n"})
	if !strings.Contains(obs, "changed since") {
		t.Fatalf("stale write must be denied, got: %s", obs)
	}

	// 4. A brand-new file needs no prior read (guard allows).
	obs = d.executeAction(sess, task, &action{Tool: "patch", Path: "new.txt", Content: "fresh\n"})
	if strings.Contains(obs, "read it first") {
		t.Fatalf("new-file create must pass the guard, got: %s", obs)
	}
}
