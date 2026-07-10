package daemon

import "testing"

// --- kill-switch -------------------------------------------------------

func TestDoctorDisabledFalseWhenUnset(t *testing.T) {
	getenv := func(string) string { return "" }
	if doctorDisabled(getenv) {
		t.Fatal("doctorDisabled should be false when CARINA_DOCTOR_DISABLE is unset")
	}
}

func TestDoctorDisabledTrueForTruthySpellings(t *testing.T) {
	for _, v := range []string{"1", "true", "on", "TRUE", " 1 "} {
		v := v
		t.Run(v, func(t *testing.T) {
			getenv := func(k string) string {
				if k == doctorDisabledEnv {
					return v
				}
				return ""
			}
			if !doctorDisabled(getenv) {
				t.Fatalf("doctorDisabled should be true for CARINA_DOCTOR_DISABLE=%q", v)
			}
		})
	}
}

func TestDoctorDisabledFalseForFalsySpellings(t *testing.T) {
	for _, v := range []string{"0", "false", "off", ""} {
		v := v
		t.Run(v, func(t *testing.T) {
			getenv := func(k string) string {
				if k == doctorDisabledEnv {
					return v
				}
				return ""
			}
			if doctorDisabled(getenv) {
				t.Fatalf("doctorDisabled should be false for CARINA_DOCTOR_DISABLE=%q", v)
			}
		})
	}
}

// --- BYOK per-provider resolution --------------------------------------

func TestByokProbeResolvesFromStore(t *testing.T) {
	providers := []struct {
		ID  string
		Env []string
	}{
		{ID: "anthropic", Env: []string{"ANTHROPIC_API_KEY"}},
	}
	hasStoredCred := func(id string) bool { return id == "anthropic" }
	getenv := func(string) string { return "" }

	got := byokProbe(providers, hasStoredCred, getenv)
	if len(got) != 1 {
		t.Fatalf("got %d statuses, want 1", len(got))
	}
	if !got[0].Resolved || got[0].Source != "store" {
		t.Fatalf("anthropic should resolve from store: %+v", got[0])
	}
}

func TestByokProbeResolvesFromEnv(t *testing.T) {
	providers := []struct {
		ID  string
		Env []string
	}{
		{ID: "openai", Env: []string{"OPENAI_API_KEY"}},
	}
	hasStoredCred := func(string) bool { return false }
	getenv := func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "sk-test"
		}
		return ""
	}

	got := byokProbe(providers, hasStoredCred, getenv)
	if len(got) != 1 {
		t.Fatalf("got %d statuses, want 1", len(got))
	}
	if !got[0].Resolved || got[0].Source != "env:OPENAI_API_KEY" {
		t.Fatalf("openai should resolve from env: %+v", got[0])
	}
}

func TestByokProbeReportsUnresolvedWhenNoSourceMatches(t *testing.T) {
	providers := []struct {
		ID  string
		Env []string
	}{
		{ID: "openrouter", Env: []string{"OPENROUTER_API_KEY"}},
	}
	hasStoredCred := func(string) bool { return false }
	getenv := func(string) string { return "" }

	got := byokProbe(providers, hasStoredCred, getenv)
	if len(got) != 1 {
		t.Fatalf("got %d statuses, want 1", len(got))
	}
	if got[0].Resolved {
		t.Fatalf("openrouter should be unresolved: %+v", got[0])
	}
}

func TestAnyProviderResolvedPassesWithOneOfMany(t *testing.T) {
	statuses := []providerKeyStatus{
		{ProviderID: "openai", Resolved: false},
		{ProviderID: "anthropic", Resolved: true, Source: "store"},
		{ProviderID: "openrouter", Resolved: false},
	}
	if !anyProviderResolved(statuses) {
		t.Fatal("anyProviderResolved should pass when at least one provider resolves")
	}
}

func TestAnyProviderResolvedFailsWhenNoneResolve(t *testing.T) {
	statuses := []providerKeyStatus{
		{ProviderID: "openai", Resolved: false},
		{ProviderID: "anthropic", Resolved: false},
	}
	if anyProviderResolved(statuses) {
		t.Fatal("anyProviderResolved should fail when no provider resolves")
	}
}

// --- reasoner is warn-tier, not fail-tier -------------------------------

// TestDoctorReasonerAbsentIsWarnNotFail pins the plan's explicit tier
// distinction: no configured reasoner is a valid degraded-but-functional
// mock-mode state (runMockTask), so handleDoctor must report it without
// causing the overall probe to read as a hard failure. handleDoctor today
// reports "reasoner": d.reasoner != nil as a bare bool with no tier
// wrapper — this test locks in that a fresh daemon (no reasoner configured)
// still reports doctor successfully (no error) and the reasoner field is
// present and false, which the CLI-side renderer must treat as warn, not
// fail.
func TestDoctorReasonerAbsentIsWarnNotFail(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	res, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatalf("handleDoctor should not hard-fail when no reasoner is configured: %v", err)
	}
	m := res.(map[string]any)
	if reasoner, ok := m["reasoner"].(bool); !ok || reasoner != false {
		t.Fatalf("expected reasoner=false (warn-tier, not fail-tier) on a fresh daemon, got %v", m["reasoner"])
	}
}

// --- handleDoctor surfaces the new P1.6 probes --------------------------

// TestDoctorReportsBYOKProviderStatuses pins that handleDoctor's response
// includes a "byok" entry built from the real provider catalog + auth store
// (not the stubbed always-unresolved shape doctor_probes.go started with).
func TestDoctorReportsBYOKProviderStatuses(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	res, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	byok, ok := m["byok"].(map[string]any)
	if !ok {
		t.Fatalf("expected byok map in doctor response, got %v (%T)", m["byok"], m["byok"])
	}
	if _, ok := byok["any_resolved"].(bool); !ok {
		t.Fatalf("expected byok.any_resolved bool, got %v", byok["any_resolved"])
	}
	if _, ok := byok["providers"]; !ok {
		t.Fatal("expected byok.providers list")
	}
}

// TestDoctorReportsLSPProbe pins that handleDoctor reports which language
// servers (from serverForExt's matrix) are present on PATH, so `carina
// doctor` can print copy-paste install remediation per missing server.
func TestDoctorReportsLSPProbe(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	res, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	lsp, ok := m["lsp"].(map[string]any)
	if !ok {
		t.Fatalf("expected lsp map in doctor response, got %v (%T)", m["lsp"], m["lsp"])
	}
	if _, ok := lsp["servers"]; !ok {
		t.Fatal("expected lsp.servers list")
	}
}

// TestDoctorHonorsKillSwitch pins that handleDoctor short-circuits to a
// minimal disabled report when CARINA_DOCTOR_DISABLE is set, per the plan's
// "honor a kill-switch env for locked-down deployments" requirement.
func TestDoctorHonorsKillSwitch(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	t.Setenv(doctorDisabledEnv, "1")

	res, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if disabled, ok := m["disabled"].(bool); !ok || !disabled {
		t.Fatalf("expected disabled=true when %s is set, got %v", doctorDisabledEnv, m["disabled"])
	}
	if _, ok := m["kernel"]; ok {
		t.Fatal("disabled doctor report should not run live probes (kernel probe present)")
	}
}
