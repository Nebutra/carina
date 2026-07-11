package doctorfix

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyRequiresConfirmation(t *testing.T) {
	h := t.TempDir()
	as := Plan([]Finding{{"state_dir", "FAIL", ""}}, h)
	if err := Apply(as, false); err == nil {
		t.Fatal("applied without confirmation")
	}
	if _, err := os.Stat(filepath.Join(h, ".carina", "state")); !os.IsNotExist(err) {
		t.Fatal("changed disk during preview")
	}
	if err := Apply(as, true); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionRepairIsPreviewable(t *testing.T) {
	h := t.TempDir()
	dir := filepath.Join(h, ".carina", "state")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(dir, 0755)
	actions := Plan([]Finding{{"state_dir_permissions", "WARN", ""}}, h)
	if len(actions) != 1 {
		t.Fatal(actions)
	}
	if err := Apply(actions, false); err == nil {
		t.Fatal("applied without confirmation")
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0755 {
		t.Fatal("preview changed permissions")
	}
	if err := Apply(actions, true); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(dir)
	if info.Mode().Perm() != 0700 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}
