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
