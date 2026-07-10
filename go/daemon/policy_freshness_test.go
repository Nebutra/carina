package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHandleDoctorReportsPolicyStaleAfterOnDiskEdit proves the fix is wired
// end to end through daemon.doctor, not just at the pure-function level:
// after New() loads an OrgPolicy snapshot from PolicyDir, editing
// bundle.toml on disk without restarting the daemon must flip
// report["policy"]["stale"] to true — closing the false-PASS gap doctor's
// prior kernel probe (liveness-only) left open.
func TestHandleDoctorReportsPolicyStaleAfterOnDiskEdit(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}

	stateDir := t.TempDir()
	policyDir := filepath.Join(stateDir, "policy")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(policyDir, "bundle.toml")
	if err := os.WriteFile(bundlePath, []byte("name = \"acme\"\nmax_command_risk = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := New(Options{
		StateDir: t.TempDir(), KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), PolicyDir: policyDir, Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	res, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	report := res.(map[string]any)
	policy, ok := report["policy"].(map[string]any)
	if !ok {
		t.Fatal("handleDoctor report missing \"policy\" key")
	}
	if policy["configured"] != true {
		t.Fatalf("policy.configured = %v, want true (PolicyDir was set)", policy["configured"])
	}
	if policy["stale"] != false {
		t.Fatalf("policy.stale = %v, want false immediately after startup (disk matches loaded)", policy["stale"])
	}

	// Operator tightens the policy on disk; the daemon keeps running with
	// its startup-time OrgPolicy snapshot (reload.go never re-inits
	// kernel/policy wiring).
	if err := os.WriteFile(bundlePath, []byte("name = \"acme\"\nmax_command_risk = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res2, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	report2 := res2.(map[string]any)
	policy2 := report2["policy"].(map[string]any)
	if policy2["stale"] != true {
		t.Fatalf("policy.stale = %v, want true after an on-disk bundle.toml edit with no daemon restart", policy2["stale"])
	}
	if reason, _ := policy2["reason"].(string); reason == "" {
		t.Fatal("policy.reason must be non-empty when stale, so doctor's report can render remediation")
	}
}

// TestHandleDoctorPolicyNotConfiguredWhenNoPolicyDir pins the common
// open-source/local case: no PolicyDir at all means doctor must report
// configured=false, stale=false — never a false staleness warning for a
// deployment that never opted into an enterprise policy bundle.
func TestHandleDoctorPolicyNotConfiguredWhenNoPolicyDir(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	res, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	report := res.(map[string]any)
	policy := report["policy"].(map[string]any)
	if policy["configured"] != false {
		t.Fatalf("policy.configured = %v, want false (no PolicyDir set)", policy["configured"])
	}
	if policy["stale"] != false {
		t.Fatalf("policy.stale = %v, want false (nothing to go stale)", policy["stale"])
	}
}

// TestPolicyBundleFreshCleanDaemonNoPolicyDir pins the common case: no
// enterprise PolicyDir configured at all means there is nothing to go
// stale — doctor must not report a false staleness warning for a
// deployment that never opted into an on-disk policy bundle.
func TestPolicyBundleFreshCleanDaemonNoPolicyDir(t *testing.T) {
	stale, _ := policyBundleStale("", nil)
	if stale {
		t.Fatal("policyBundleStale(\"\", nil) = true, want false (no PolicyDir configured)")
	}
}

// TestPolicyBundleFreshWhenDiskMatchesLoaded pins the healthy case: the
// on-disk bundle.toml is byte-identical to what was loaded into d.org at
// daemon startup — doctor's kernel probe should report fresh.
func TestPolicyBundleFreshWhenDiskMatchesLoaded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bundle.toml"), []byte("name = \"strict\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := loadOrgPolicy(dir)
	stale, reason := policyBundleStale(dir, loaded)
	if stale {
		t.Fatalf("policyBundleStale = true (reason=%q), want false: on-disk bundle matches what was loaded", reason)
	}
}

// TestPolicyBundleStaleAfterOnDiskEditWithoutRestart is the exact
// governance gap this fix closes: an operator edits bundle.toml on disk
// (tightening a rule) but the daemon has NOT been restarted (reload.go
// explicitly does not re-init kernel/policy wiring) — the running kernel's
// OrgPolicy still reflects the pre-edit content. doctor must detect this
// divergence rather than report a false PASS.
func TestPolicyBundleStaleAfterOnDiskEditWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "bundle.toml")
	if err := os.WriteFile(bundlePath, []byte("name = \"loose\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := loadOrgPolicy(dir) // simulates daemon startup's snapshot

	// Operator tightens the policy on disk; daemon is still running with
	// the old `loaded` snapshot (no restart, no re-init — matches
	// reload.go's documented restart-only contract for policy wiring).
	if err := os.WriteFile(bundlePath, []byte("name = \"strict\"\ndeny_capabilities = [\"CommandExec\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stale, reason := policyBundleStale(dir, loaded)
	if !stale {
		t.Fatal("policyBundleStale = false, want true: on-disk bundle.toml changed since the daemon loaded its OrgPolicy snapshot")
	}
	if reason == "" {
		t.Fatal("policyBundleStale must return a non-empty reason a doctor report can render")
	}
}
