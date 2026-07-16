package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductInventoriesAreReadOnlyAndRedactHookCommands(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	if err := os.MkdirAll(filepath.Join(ws, ".carina", "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".carina", "skills", "review.md"), []byte("---\ndescription: Review safely\n---\nReview changes."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".carina", "hooks.json"), []byte(`[{"event":"PreToolUse","matcher":"patch","command":["secret-command","token"]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	params := mustJSON(t, map[string]any{"session_id": sess.SessionID})
	skills, err := d.handleSkillInventory(params)
	if err != nil {
		t.Fatal(err)
	}
	if skills.(map[string]any)["count"].(int) != 1 {
		t.Fatalf("skills=%#v", skills)
	}
	hooks, err := d.handleHookInventory(params)
	if err != nil {
		t.Fatal(err)
	}
	row := hooks.(map[string]any)
	if row["commands_redacted"] != true || row["count"].(int) != 1 {
		t.Fatalf("hooks=%#v", hooks)
	}
	encoded := string(mustJSON(t, hooks))
	if strings.Contains(encoded, "secret-command") || strings.Contains(encoded, "token") {
		t.Fatalf("hook command leaked: %s", encoded)
	}
	profile, err := d.handleProfileInventory(params)
	if err != nil || profile.(map[string]any)["source"] == "" {
		t.Fatalf("profile=%#v err=%v", profile, err)
	}
	config, err := d.handleConfigInventory(params)
	if err != nil || config.(map[string]any)["mutation"] == "" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
}
