package kernel

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testBins(t *testing.T) (kernelBin, toolsDir string) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	for _, p := range []string{"target/release/carina-kernel-service", "target/debug/carina-kernel-service"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			kernelBin = filepath.Join(root, p)
			break
		}
	}
	if env := os.Getenv("CARINA_KERNEL_BIN"); env != "" {
		kernelBin = env
	}
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	toolsDir = filepath.Join(root, "zig", "zig-out", "bin")
	return
}

func TestKernelClientLifecycle(t *testing.T) {
	kernelBin, toolsDir := testBins(t)
	svc, err := Start(kernelBin, t.TempDir(), toolsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("orig\n"), 0o600)
	if err := svc.InitSession("sess_k", ws, "full-workspace"); err != nil {
		t.Fatal(err)
	}

	// capability decision
	d, err := svc.Request("sess_k", "FileRead", filepath.Join(ws, "a.txt"), "")
	if err != nil || d.Decision != "allowed" {
		t.Fatalf("request: %v %+v", err, d)
	}
	// classify
	risk, err := svc.ClassifyCommand("rm -rf /")
	if err != nil || risk != 5 {
		t.Fatalf("classify: %v risk=%d", err, risk)
	}
	// profile describe
	if _, err := svc.ProfileDescribe("sess_k"); err != nil {
		t.Fatalf("profile describe: %v", err)
	}

	// patch propose -> apply -> rollback (delegates to carina-patch-native)
	if _, err := os.Stat(filepath.Join(toolsDir, "carina-patch-native")); err != nil {
		t.Skip("zig tools not built for patch")
	}
	p, err := svc.PatchPropose("sess_k", "task_1", "test", []FileChange{{Path: "a.txt", NewContent: "changed\n"}})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if p.CreatedAt == "" {
		t.Fatal("patch should carry created_at provenance")
	}
	if _, err := svc.PatchApply("sess_k", p.PatchID, "user"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "a.txt")); string(got) != "changed\n" {
		t.Fatalf("apply did not write: %q", got)
	}
	if _, err := svc.PatchRollback("sess_k", p.PatchID); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "a.txt")); string(got) != "orig\n" {
		t.Fatalf("rollback did not restore: %q", got)
	}
}

func TestKernelApprovalAndReports(t *testing.T) {
	kernelBin, toolsDir := testBins(t)
	svc, err := Start(kernelBin, t.TempDir(), toolsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ws := t.TempDir()
	if err := svc.InitSession("sess_a", ws, "safe-edit"); err != nil {
		t.Fatal(err)
	}

	// A risk-2 command requires approval; approve then deny paths.
	d, err := svc.Request("sess_a", "CommandExec", "npm install left-pad", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %s", d.Decision)
	}
	if _, err := svc.Approve("sess_a", d.DecisionID, "alice"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	d2, _ := svc.Request("sess_a", "CommandExec", "pip install requests", "")
	if _, err := svc.Deny("sess_a", d2.DecisionID, "bob", "not allowed"); err != nil {
		t.Fatalf("deny: %v", err)
	}

	// reports + exports
	if _, err := svc.AuditReport("sess_a"); err != nil {
		t.Fatalf("audit report: %v", err)
	}
	if _, err := svc.AuditExport("sess_a"); err != nil {
		t.Fatalf("audit export: %v", err)
	}
	if _, err := svc.ReadEvents("sess_a"); err != nil {
		t.Fatalf("read events: %v", err)
	}
	if _, err := svc.PatchList("sess_a"); err != nil {
		t.Fatalf("patch list: %v", err)
	}
	// role-based approval wrapper
	d3, _ := svc.Request("sess_a", "CommandExec", "npm install x", "")
	if _, err := svc.ApproveWithRole("sess_a", d3.DecisionID, "carol", "lead"); err != nil {
		t.Fatalf("approve with role: %v", err)
	}
}

func TestKernelPluginInspectAndRun(t *testing.T) {
	kernelBin, toolsDir := testBins(t)
	svc, err := Start(kernelBin, t.TempDir(), toolsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	manifest := "name = \"p\"\nversion = \"0.1.0\"\nkind = \"tool\"\n[permissions]\ncommand_exec = [\"go test ./...\"]\n"
	out, err := svc.PluginInspect(manifest)
	if err != nil || len(out) == 0 {
		t.Fatalf("plugin inspect: %v", err)
	}

	// Run the checked-in example plugin, if built.
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	wasm, rerr := os.ReadFile(filepath.Join(root, "examples/plugins/hello/hello.wasm"))
	pman, merr := os.ReadFile(filepath.Join(root, "examples/plugins/hello/plugin.toml"))
	if rerr != nil || merr != nil {
		t.Skip("example plugin not built")
	}
	if err := svc.InitSession("sess_p", t.TempDir(), "ci-runner"); err != nil {
		t.Fatal(err)
	}
	import_b64 := base64.StdEncoding.EncodeToString(wasm)
	res, err := svc.PluginRun("sess_p", string(pman), import_b64, "")
	if err != nil || len(res) == 0 {
		t.Fatalf("plugin run: %v", err)
	}
}

func TestKernelSecretRequestAndPatchShow(t *testing.T) {
	kernelBin, toolsDir := testBins(t)
	svc, err := Start(kernelBin, t.TempDir(), toolsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("x\n"), 0o600)
	svc.InitSession("sess_x", ws, "full-workspace")
	svc.GrantSecret("sess_x", "TOKEN", "abc")
	if _, _, err := svc.RequestSecret("sess_x", "TOKEN"); err != nil {
		t.Fatalf("request secret: %v", err)
	}
	p, err := svc.PatchPropose("sess_x", "", "t", []FileChange{{Path: "a.txt", NewContent: "y\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PatchShow("sess_x", p.PatchID); err != nil {
		t.Fatalf("patch show: %v", err)
	}
	if _, err := svc.ProfileDescribe("sess_x"); err != nil {
		t.Fatalf("profile describe: %v", err)
	}
}

func TestFindBinaryEnv(t *testing.T) {
	t.Setenv("CARINA_KERNEL_BIN", "/some/path/carina-kernel-service")
	got, err := FindBinary()
	if err != nil || got != "/some/path/carina-kernel-service" {
		t.Fatalf("FindBinary env: %q %v", got, err)
	}
}

func TestKernelSecretAndAudit(t *testing.T) {
	kernelBin, toolsDir := testBins(t)
	svc, err := Start(kernelBin, t.TempDir(), toolsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.InitSession("sess_s", t.TempDir(), "full-workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GrantSecret("sess_s", "API_KEY", "supersecret"); err != nil {
		t.Fatal(err)
	}
	red, err := svc.Redact("sess_s", "the key is supersecret ok")
	if err != nil {
		t.Fatal(err)
	}
	if red == "the key is supersecret ok" {
		t.Fatal("secret should be redacted")
	}
	// record an event, then verify the chain
	if err := svc.RecordEvent("sess_s", "TaskCreated", "task_1", "go", map[string]any{"x": 1}, ""); err != nil {
		t.Fatal(err)
	}
	report, err := svc.AuditVerify("sess_s")
	if err != nil {
		t.Fatal(err)
	}
	if string(report) == "" {
		t.Fatal("verify returned empty")
	}
}
