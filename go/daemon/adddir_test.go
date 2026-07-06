package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAddDirGrantsScopedRoot: session.add_dir widens a session to an additional
// directory — a read there is denied before the grant and allowed after, while a
// read outside any root stays denied.
func TestAddDirGrantsScopedRoot(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	extra := t.TempDir()
	target := filepath.Join(extra, "notes.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before the grant: reading outside the workspace is denied.
	dec, err := d.kern.Request(sess.SessionID, "FileRead", target, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision == "allowed" {
		t.Fatal("read outside workspace must be denied before add_dir")
	}

	// Grant the extra dir via the handler.
	res, err := d.handleAddDir(mustJSON(t, map[string]any{"session_id": sess.SessionID, "path": extra}))
	if err != nil {
		t.Fatalf("handleAddDir: %v", err)
	}
	if res.(map[string]any)["granted"] != true {
		t.Fatalf("add_dir did not confirm the grant: %+v", res)
	}

	// After the grant: the same read is allowed.
	dec, err = d.kern.Request(sess.SessionID, "FileRead", target, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "allowed" {
		t.Fatalf("read in the granted dir should be allowed, got %q (%s)", dec.Decision, dec.Reason)
	}

	// A path in neither root remains denied.
	other := filepath.Join(t.TempDir(), "x.txt")
	_ = os.WriteFile(other, []byte("no\n"), 0o600)
	dec, _ = d.kern.Request(sess.SessionID, "FileRead", other, "t3")
	if dec.Decision == "allowed" {
		t.Fatal("read outside every granted root must stay denied")
	}

	// A non-existent directory is rejected.
	if _, err := d.handleAddDir(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "path": filepath.Join(extra, "nope")})); err == nil {
		t.Fatal("add_dir on a missing directory must error")
	}
}
