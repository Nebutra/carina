package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/tui"
)

// --- renderDoctorReport (pure rendering, pass/warn/fail + remediation) ---

func TestRenderDoctorReportAllPass(t *testing.T) {
	report := map[string]any{
		"version":            "0.6.5",
		"disabled":           false,
		"kernel":             map[string]any{"ok": true},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true, "dir": "/zig/zig-out/bin"},
		"reasoner":           true,
		"auth":               map[string]any{"source": "env:ANTHROPIC_API_KEY"},
		"context_engine":     map[string]any{"ok": true},
		"lsp":                map[string]any{"servers": []any{}},
		"byok": map[string]any{
			"any_resolved": true,
			"providers":    []any{},
		},
	}
	out := renderDoctorReport(report, false)
	if !strings.Contains(out, "PASS") && !strings.Contains(out, "ok") {
		t.Fatalf("expected some pass indication in output:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("all-healthy report should not print FAIL:\n%s", out)
	}
}

func TestRenderDoctorReportKernelFailPrintsRemediation(t *testing.T) {
	report := map[string]any{
		"version":            "0.6.5",
		"disabled":           false,
		"kernel":             map[string]any{"ok": false, "error": "connection refused"},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true, "dir": "/zig/zig-out/bin"},
		"reasoner":           true,
		"auth":               map[string]any{"source": ""},
		"context_engine":     map[string]any{"ok": true},
		"lsp":                map[string]any{"servers": []any{}},
		"byok": map[string]any{
			"any_resolved": true,
			"providers":    []any{},
		},
	}
	out := renderDoctorReport(report, false)
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("kernel probe failure should render FAIL:\n%s", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Fatalf("kernel probe failure should surface the underlying error:\n%s", out)
	}
	if !strings.Contains(out, "carina-daemon") {
		t.Fatalf("kernel probe failure should suggest a copy-paste remediation naming carina-daemon:\n%s", out)
	}
}

func TestRenderDoctorReportNoReasonerIsWarnNotFail(t *testing.T) {
	report := map[string]any{
		"version":            "0.6.5",
		"disabled":           false,
		"kernel":             map[string]any{"ok": true},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true, "dir": "/zig/zig-out/bin"},
		"reasoner":           false,
		"auth":               map[string]any{"source": ""},
		"context_engine":     map[string]any{"ok": true},
		"lsp":                map[string]any{"servers": []any{}},
		"byok": map[string]any{
			"any_resolved": false,
			"providers":    []any{},
		},
	}
	out := renderDoctorReport(report, false)
	if strings.Contains(out, "FAIL") {
		t.Fatalf("no-reasoner + no-BYOK-key should render as WARN, not FAIL:\n%s", out)
	}
	if !strings.Contains(out, "WARN") {
		t.Fatalf("no-reasoner should render as WARN:\n%s", out)
	}
}

func TestRenderDoctorReportLSPMissingServerPrintsRemediation(t *testing.T) {
	report := map[string]any{
		"version":            "0.6.5",
		"disabled":           false,
		"kernel":             map[string]any{"ok": true},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true, "dir": "/zig/zig-out/bin"},
		"reasoner":           true,
		"auth":               map[string]any{"source": "env:ANTHROPIC_API_KEY"},
		"context_engine":     map[string]any{"ok": true},
		"lsp": map[string]any{"servers": []any{
			map[string]any{"LangID": "go", "Bin": "gopls", "Present": false, "Remediation": "go install golang.org/x/tools/gopls@latest"},
		}},
		"byok": map[string]any{
			"any_resolved": true,
			"providers":    []any{},
		},
	}
	out := renderDoctorReport(report, false)
	if !strings.Contains(out, "go install golang.org/x/tools/gopls@latest") {
		t.Fatalf("missing LSP server should print its copy-paste remediation:\n%s", out)
	}
}

func TestRenderDoctorReportDisabled(t *testing.T) {
	report := map[string]any{
		"version":  "0.6.5",
		"disabled": true,
		"reason":   "CARINA_DOCTOR_DISABLE is set; probes did not run",
	}
	out := renderDoctorReport(report, false)
	if !strings.Contains(out, "disabled") && !strings.Contains(out, "DISABLE") {
		t.Fatalf("disabled report should say so plainly:\n%s", out)
	}
}

// --- auditChainCheck: audit-chain head verification via audit.verify -----

func TestAuditChainCheckNoSessionsIsInfoNotWarn(t *testing.T) {
	chk := auditChainCheck(nil, false, false)
	if chk.state == "FAIL" || chk.state == "WARN" {
		t.Fatalf("no sessions yet should not read as WARN/FAIL, got %+v", chk)
	}
}

func TestAuditChainCheckPassOnValidChain(t *testing.T) {
	chk := auditChainCheck([]byte(`{"ok":true,"event_count":12,"head_hash":"abc"}`), true, false)
	if chk.state != "PASS" {
		t.Fatalf("valid chain should render PASS, got %+v", chk)
	}
}

func TestAuditChainCheckFailOnBrokenChain(t *testing.T) {
	chk := auditChainCheck([]byte(`{"ok":false,"event_count":12,"broken_at":4,"reason":"event 4 content tampered (hash mismatch)"}`), true, false)
	if chk.state != "FAIL" {
		t.Fatalf("tampered chain should render FAIL, got %+v", chk)
	}
	if !strings.Contains(chk.detail, "tampered") {
		t.Fatalf("FAIL detail should surface the tamper reason, got %+v", chk)
	}
}

func TestAuditChainCheckFailOnVerifyRPCError(t *testing.T) {
	chk := auditChainCheck(nil, true, true)
	if chk.state != "FAIL" {
		t.Fatalf("an audit.verify RPC error should render FAIL, not silently pass, got %+v", chk)
	}
}

// --- cmdDoctor: dispatches daemon.doctor and renders/JSON-passes ---

func TestCmdDoctorRendersHumanReport(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "daemon.doctor", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return map[string]any{
			"version":            "0.6.5",
			"disabled":           false,
			"kernel":             map[string]any{"ok": true},
			"state_dir_writable": map[string]any{"ok": true},
			"tools":              map[string]any{"available": true, "dir": "/zig/zig-out/bin"},
			"reasoner":           true,
			"auth":               map[string]any{"source": "env:ANTHROPIC_API_KEY"},
			"context_engine":     map[string]any{"ok": true},
			"lsp":                map[string]any{"servers": []any{}},
			"byok": map[string]any{
				"any_resolved": true,
				"providers":    []any{},
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenTCP(addr) }()
	defer s.Close()
	waitTCP(t, addr)
	c, err := rpc.DialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	out, err := captureStdout(t, func() error {
		return cmdDoctor(c, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "{") {
		t.Fatalf("human doctor output should not be raw JSON:\n%s", out)
	}
	if !strings.Contains(out, "kernel") {
		t.Fatalf("doctor output missing kernel probe:\n%s", out)
	}
}

func TestCmdDoctorJSONPassthrough(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "daemon.doctor", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return map[string]any{"version": "0.6.5", "disabled": false}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenTCP(addr) }()
	defer s.Close()
	waitTCP(t, addr)
	c, err := rpc.DialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	out, err := captureStdout(t, func() error {
		return cmdDoctor(c, []string{"--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("--json output should be valid JSON: %v\n%s", err, out)
	}
	if decoded["version"] != "0.6.5" {
		t.Fatalf("unexpected decoded version: %v", decoded["version"])
	}
}

// TestCmdDoctorReturnsOutcomeErrorOnWarn closes the loop end-to-end: a
// daemon.doctor response with a WARN-tier check (no reasoner configured)
// must make cmdDoctor return a *doctorOutcomeError classifyExitCode maps to
// exit 6 — not nil (which would make a degraded machine report exit 0).
func TestCmdDoctorReturnsOutcomeErrorOnWarn(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "daemon.doctor", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return map[string]any{
			"version":            "0.6.5",
			"disabled":           false,
			"kernel":             map[string]any{"ok": true},
			"state_dir_writable": map[string]any{"ok": true},
			"tools":              map[string]any{"available": true, "dir": "/zig/zig-out/bin"},
			"reasoner":           false, // warn-tier
			"auth":               map[string]any{"source": ""},
			"context_engine":     map[string]any{"ok": true},
			"lsp":                map[string]any{"servers": []any{}},
			"byok": map[string]any{
				"any_resolved": false,
				"providers":    []any{},
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenTCP(addr) }()
	defer s.Close()
	waitTCP(t, addr)
	c, err := rpc.DialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var cmdErr error
	if _, err := captureStdout(t, func() error {
		cmdErr = cmdDoctor(c, nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if cmdErr == nil {
		t.Fatal("cmdDoctor should return a non-nil error when the report has a WARN-tier check")
	}
	if got := classifyExitCode(cmdErr); got != tui.OutcomeDegradedPartial {
		t.Fatalf("classifyExitCode(cmdDoctor WARN result) = %v (exit %d), want OutcomeDegradedPartial (exit %d)",
			got, got.ExitCode(), tui.OutcomeDegradedPartial.ExitCode())
	}
}

// --- doctor exit code reflects overall pass/warn/fail ------------------

func TestDoctorExitCodeAllPass(t *testing.T) {
	report := map[string]any{
		"disabled":           false,
		"kernel":             map[string]any{"ok": true},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true},
		"reasoner":           true,
		"context_engine":     map[string]any{"ok": true},
		"lsp":                map[string]any{"servers": []any{}},
		"byok":               map[string]any{"any_resolved": true, "providers": []any{}},
	}
	if got := doctorExitCode(report); got != 0 {
		t.Fatalf("all-pass doctor report should exit 0, got %d", got)
	}
}

func TestDoctorExitCodeWarnOnly(t *testing.T) {
	report := map[string]any{
		"disabled":           false,
		"kernel":             map[string]any{"ok": true},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true},
		"reasoner":           false, // warn-tier
		"context_engine":     map[string]any{"ok": true},
		"lsp":                map[string]any{"servers": []any{}},
		"byok":               map[string]any{"any_resolved": true, "providers": []any{}},
	}
	if got := doctorExitCode(report); got != 6 {
		t.Fatalf("warn-only doctor report should exit 6 (degraded-partial), got %d", got)
	}
}

func TestDoctorExitCodeHardFail(t *testing.T) {
	report := map[string]any{
		"disabled":           false,
		"kernel":             map[string]any{"ok": false, "error": "boom"},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true},
		"reasoner":           true,
		"context_engine":     map[string]any{"ok": true},
		"lsp":                map[string]any{"servers": []any{}},
		"byok":               map[string]any{"any_resolved": true, "providers": []any{}},
	}
	if got := doctorExitCode(report); got != 1 {
		t.Fatalf("kernel-fail doctor report should exit 1 (runtime error), got %d", got)
	}
}

// --- kill-switch: carina doctor must not dial the daemon when disabled --

func TestCmdDoctorHonorsKillSwitchWithoutDialing(t *testing.T) {
	t.Setenv(doctorDisabledEnv, "1")
	dialed := false
	orig := dialHook
	dialHook = func() (*rpcClient, error) {
		dialed = true
		return nil, nil
	}
	defer func() { dialHook = orig }()

	if _, err := initGate("doctor"); err != nil {
		t.Fatalf("initGate(doctor) unexpected error: %v", err)
	}
	if dialed {
		t.Fatal("initGate(doctor) dialed the daemon while CARINA_DOCTOR_DISABLE was set")
	}
}

// --- first-launch auto-run detection ------------------------------------

func TestShouldAutoRunDoctorTrueOnFreshHome(t *testing.T) {
	dir := t.TempDir()
	if !shouldAutoRunDoctor(dir) {
		t.Fatal("a fresh ~/.carina with no marker file should trigger doctor auto-run")
	}
}

func TestShouldAutoRunDoctorFalseAfterMarker(t *testing.T) {
	dir := t.TempDir()
	if err := markDoctorAutoRun(dir); err != nil {
		t.Fatal(err)
	}
	if shouldAutoRunDoctor(dir) {
		t.Fatal("doctor auto-run should not re-fire once the marker file exists")
	}
}

// fakeCaller is a minimal Caller stub for maybeAutoRunDoctor's tests: it
// records whether daemon.doctor was invoked and returns a canned response.
type fakeCaller struct {
	called   bool
	response map[string]any
	err      error
}

func (f *fakeCaller) Call(method string, params any, result any) error {
	if method != "daemon.doctor" {
		return nil
	}
	f.called = true
	if f.err != nil {
		return f.err
	}
	b, err := json.Marshal(f.response)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, result)
}
func (f *fakeCaller) Close() error { return nil }

// TestMaybeAutoRunDoctorFiresOnceOnFreshHome pins the full first-launch
// wiring: a fresh HOME with no .carina/.doctor-first-run marker makes
// maybeAutoRunDoctor call daemon.doctor and write the marker so it never
// fires again.
func TestMaybeAutoRunDoctorFiresOnceOnFreshHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	fc := &fakeCaller{response: map[string]any{
		"disabled": false, "kernel": map[string]any{"ok": true},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true},
		"reasoner":           true, "context_engine": map[string]any{"ok": true},
		"lsp":  map[string]any{"servers": []any{}},
		"byok": map[string]any{"any_resolved": true, "providers": []any{}},
	}}
	maybeAutoRunDoctor(fc)
	if !fc.called {
		t.Fatal("maybeAutoRunDoctor should call daemon.doctor on a fresh HOME")
	}
	if shouldAutoRunDoctor(filepath.Join(home, ".carina")) {
		t.Fatal("maybeAutoRunDoctor should write the first-run marker so it never fires again")
	}

	// Second call on the same HOME must not re-invoke daemon.doctor.
	fc2 := &fakeCaller{response: fc.response}
	maybeAutoRunDoctor(fc2)
	if fc2.called {
		t.Fatal("maybeAutoRunDoctor fired a second time on the same HOME")
	}
}

// TestMaybeAutoRunDoctorHonorsKillSwitch pins that CARINA_DOCTOR_DISABLE
// suppresses the first-launch auto-run too, not just the explicit `carina
// doctor` command — and, critically, does NOT write the marker, so
// re-enabling doctor later still gets its onboarding first-run.
func TestMaybeAutoRunDoctorHonorsKillSwitch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(doctorDisabledEnv, "1")

	fc := &fakeCaller{}
	maybeAutoRunDoctor(fc)
	if fc.called {
		t.Fatal("maybeAutoRunDoctor should not call daemon.doctor when CARINA_DOCTOR_DISABLE is set")
	}
	if !shouldAutoRunDoctor(filepath.Join(home, ".carina")) {
		t.Fatal("a disabled auto-run must not consume the first-run marker")
	}
}
