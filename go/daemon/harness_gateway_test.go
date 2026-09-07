package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/toolchain"
)

func TestHarnessGatewayMethodsAreRemoteAndTenantScoped(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	descriptors := d.server.MethodDescriptors()
	for _, method := range []string{"harness.workspace.tree", "harness.submit"} {
		found := false
		for _, descriptor := range descriptors {
			if descriptor.Method != method {
				continue
			}
			found = true
			if !descriptor.Remote {
				t.Fatalf("%s must be remote-safe", method)
			}
		}
		if !found {
			t.Fatalf("missing descriptor for %s", method)
		}
		if err := d.gatewayRemoteParamsAllowed(method, json.RawMessage(`{}`), rpc.GatewayTokenClaims{}); err == nil || !strings.Contains(err.Error(), "tenant") {
			t.Fatalf("%s without tenant-bound claims must fail closed, got %v", method, err)
		}
	}
}

func TestHarnessSubmitCreatesTenantSessionAndExecution(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"submitted"}`}})

	result, err := d.handleHarnessSubmit(mustRaw(map[string]any{
		"workspace_root":       workspace,
		"profile":              "safe-edit",
		"prompt":               "inspect the workspace",
		"client_submission_id": "harness-test-1",
		"tenant_id":            "org_harness",
		"input_media": []map[string]any{{
			"media_type":     "image/png",
			"content_base64": base64.StdEncoding.EncodeToString(tinyPNG()),
			"origin":         "harness-test.png",
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	projected, ok := body["session"].(sessionContinuityEntry)
	if !ok || projected.Session == nil || projected.SessionID == "" {
		t.Fatalf("session projection = %#v", body["session"])
	}
	if projected.TenantID != "org_harness" {
		t.Fatalf("tenant = %q", projected.TenantID)
	}
	execution, ok := body["execution"].(map[string]any)
	if !ok || execution["task_id"] == "" {
		t.Fatalf("execution = %#v", body["execution"])
	}
	task, ok := d.sched.Get(fmt.Sprint(execution["task_id"]))
	if !ok || task.SessionID != projected.SessionID {
		t.Fatalf("scheduled task = %#v", task)
	}
	if len(task.InputMediaRefs) != 1 || task.InputMediaRefs[0].Origin != "harness-test.png" {
		t.Fatalf("input media refs = %#v", task.InputMediaRefs)
	}
}

func TestHarnessSubmitRejectsInvalidMediaBeforeExecution(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	_, err := d.handleHarnessSubmit(mustRaw(map[string]any{
		"workspace_root": workspace,
		"prompt":         "must not run",
		"tenant_id":      "org_harness",
		"input_media": []map[string]any{{
			"media_type":     "image/png",
			"content_base64": base64.StdEncoding.EncodeToString([]byte("not a png")),
			"origin":         "invalid.png",
		}},
	}))
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("invalid media error = %v", err)
	}
	if len(d.sched.List()) != 0 {
		t.Fatal("invalid media must not submit an execution")
	}
}

func TestHarnessSubmitRejectsWorkspaceMismatch(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	created, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": workspace,
		"profile":        "safe-edit",
		"tenant_id":      "org_harness",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sessionID := created.(*sessionstore.Session).SessionID
	_, err = d.handleHarnessSubmit(mustRaw(map[string]any{
		"session_id":     sessionID,
		"workspace_root": t.TempDir(),
		"prompt":         "must fail",
		"tenant_id":      "org_harness",
	}))
	if err == nil || !strings.Contains(err.Error(), "does not match session") {
		t.Fatalf("workspace mismatch error = %v", err)
	}
}

func TestHarnessWorkspaceTreeIsBounded(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	for i := 0; i < 8; i++ {
		path := filepath.Join(workspace, fmt.Sprintf("file-%02d.txt", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	created, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": workspace,
		"profile":        "safe-edit",
		"tenant_id":      "org_harness",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sessionID := created.(*sessionstore.Session).SessionID
	result, err := d.handleHarnessWorkspaceTree(mustRaw(map[string]any{
		"session_id": sessionID,
		"tenant_id":  "org_harness",
		"max_files":  3,
		"max_depth":  2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	files, ok := body["files"].([]toolchain.FileEntry)
	if !ok {
		t.Fatalf("files type = %T", body["files"])
	}
	if len(files) > 3 || body["truncated"] != true {
		t.Fatalf("bounded result = %#v", body)
	}
}

func TestBoundedHarnessLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		requested int
		ceiling   int
		want      int
	}{
		"default": {requested: 0, ceiling: 12, want: 12},
		"lower":   {requested: 4, ceiling: 12, want: 4},
		"clamped": {requested: 99, ceiling: 12, want: 12},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := boundedHarnessLimit("limit", tc.requested, tc.ceiling)
			if err != nil || got != tc.want {
				t.Fatalf("got %d, %v; want %d", got, err, tc.want)
			}
		})
	}
	if _, err := boundedHarnessLimit("limit", -1, 12); err == nil {
		t.Fatal("negative limit must fail")
	}
}
