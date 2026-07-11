package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
)

func TestWorkerAuthorityRequiresCredentialBoundToWorkerID(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	first := registerAuthenticatedWorker(t, d, "first")
	second := registerAuthenticatedWorker(t, d, "second")

	for _, tc := range []struct {
		name   string
		invoke func(json.RawMessage) (any, error)
		params map[string]any
	}{
		{name: "heartbeat", invoke: d.handleWorkerHeartbeat, params: map[string]any{"worker_id": first.workerID}},
		{name: "revoke", invoke: d.handleWorkerRevoke, params: map[string]any{"worker_id": first.workerID}},
		{name: "backpressure", invoke: d.handleBackpressureReport, params: map[string]any{"worker_id": first.workerID}},
		{name: "poll", invoke: d.handleWorkPoll, params: map[string]any{"worker_id": first.workerID}},
		{name: "renew", invoke: d.handleWorkRenew, params: map[string]any{"worker_id": first.workerID, "task_id": "task_unknown"}},
		{name: "report", invoke: d.handleWorkReport, params: map[string]any{"worker_id": first.workerID, "task_id": "task_unknown", "status": "failed"}},
	} {
		t.Run(tc.name+" missing credential", func(t *testing.T) {
			_, err := tc.invoke(mustJSON(t, tc.params))
			assertWorkerAuthError(t, err, "")
		})
		t.Run(tc.name+" cross-worker credential", func(t *testing.T) {
			params := cloneAnyMap(tc.params)
			params["worker_credential"] = second.credential
			_, err := tc.invoke(mustJSON(t, params))
			assertWorkerAuthError(t, err, second.credential)
		})
		t.Run(tc.name+" forged worker id", func(t *testing.T) {
			params := cloneAnyMap(tc.params)
			params["worker_id"] = "wrk_forged"
			params["worker_credential"] = first.credential
			_, err := tc.invoke(mustJSON(t, params))
			assertWorkerAuthError(t, err, first.credential)
		})
	}

	if _, err := d.handleWorkerHeartbeat(mustJSON(t, map[string]any{
		"worker_id": first.workerID, "worker_credential": first.credential,
	})); err != nil {
		t.Fatalf("matching credential heartbeat: %v", err)
	}
}

func TestWorkerCredentialReturnedOnceAndAbsentFromList(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.debugRPCEnabled.Store(true)

	registered := registerAuthenticatedWorker(t, d, "private")
	listed, err := d.handleWorkerList(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	assertCredentialAbsent(t, "worker.list", raw, registered.credential)

	if _, err := d.handleBackpressureReport(mustJSON(t, map[string]any{
		"worker_id": registered.workerID, "worker_credential": registered.credential, "queue_depth": 1,
	})); err != nil {
		t.Fatal(err)
	}
	status, err := d.handleBackpressureStatus(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(status)
	assertCredentialAbsent(t, "backpressure.status", raw, registered.credential)
	snapshot, err := d.handleDebugSnapshot(mustJSON(t, map[string]any{"limit": 100}))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(snapshot)
	assertCredentialAbsent(t, "debug.snapshot", raw, registered.credential)

	session, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(session.SessionID, workspace, session.PermissionProfile, session.ApprovalMode, d.org); err != nil {
		t.Fatal(err)
	}
	submitted, err := d.handleWorkSubmit(mustJSON(t, map[string]any{"session_id": session.SessionID, "prompt": "audit"}))
	if err != nil {
		t.Fatal(err)
	}
	task := submitted.(*scheduler.Task)
	poll, err := d.handleWorkPoll(mustJSON(t, map[string]any{
		"worker_id": registered.workerID, "worker_credential": registered.credential,
	}))
	if err != nil {
		t.Fatal(err)
	}
	leased := poll.(map[string]any)["task"].(*scheduler.Task)
	if leased.LeaseGeneration <= 0 {
		t.Fatalf("leased task has no generation: %+v", leased)
	}
	if _, err := d.handleWorkReport(mustJSON(t, map[string]any{
		"worker_id": registered.workerID, "worker_credential": registered.credential,
		"task_id": task.TaskID, "lease_generation": leased.LeaseGeneration,
		"status": "failed", "summary": "expected test failure",
	})); err != nil {
		t.Fatal(err)
	}
	audit, err := d.kern.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	assertCredentialAbsent(t, "audit events", audit, registered.credential)
}

func TestWorkerRevokeIsRemoteWorkerScoped(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	for _, descriptor := range d.server.MethodDescriptors() {
		if descriptor.Method != "worker.revoke" {
			continue
		}
		if descriptor.Scope != "worker" || !descriptor.Remote || !descriptor.ControlPlaneWrite {
			t.Fatalf("worker.revoke descriptor = %+v", descriptor)
		}
		return
	}
	t.Fatal("worker.revoke descriptor not registered")
}

func TestGatewayWebSocketFailsClosedWithoutSigningKey(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	err := d.RunGatewayWebSocket("127.0.0.1:0", nil)
	if err == nil || !strings.Contains(err.Error(), "gateway_token_signing_key_file") {
		t.Fatalf("RunGatewayWebSocket without signing key error = %v", err)
	}
}

func TestCancelledWorkerLeaseCannotRenewOrReportCompleted(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	registered := registerAuthenticatedWorker(t, d, "cancel-aware")
	submitted, err := d.handleWorkSubmit(mustJSON(t, map[string]any{
		"session_id": session.SessionID, "prompt": "cancel me",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := submitted.(*scheduler.Task)
	if _, err := d.handleWorkPoll(mustJSON(t, map[string]any{
		"worker_id": registered.workerID, "worker_credential": registered.credential,
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sched.Cancel(task.TaskID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		invoke func(json.RawMessage) (any, error)
		params map[string]any
	}{
		{name: "renew", invoke: d.handleWorkRenew, params: map[string]any{"task_id": task.TaskID}},
		{name: "report completed", invoke: d.handleWorkReport, params: map[string]any{"task_id": task.TaskID, "status": "completed", "summary": "must not win"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := cloneAnyMap(tc.params)
			params["worker_id"] = registered.workerID
			params["worker_credential"] = registered.credential
			result, err := tc.invoke(mustJSON(t, params))
			if err != nil {
				t.Fatal(err)
			}
			values := result.(map[string]any)
			if values["ok"] != false || values["cancelled"] != true {
				t.Fatalf("cancelled lease response = %#v", values)
			}
		})
	}
	got, _ := d.sched.Get(task.TaskID)
	if got.Status != "cancelled" || got.Summary == "must not win" {
		t.Fatalf("worker report overwrote cancellation: %+v", got)
	}
}

type registeredWorkerCredential struct {
	workerID   string
	credential string
}

func registerAuthenticatedWorker(t *testing.T, d *Daemon, name string) registeredWorkerCredential {
	t.Helper()
	result, err := d.handleWorkerRegister(mustJSON(t, map[string]any{"name": name, "kind": "remote"}))
	if err != nil {
		t.Fatal(err)
	}
	values, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("worker.register result = %#v", result)
	}
	workerID, _ := values["worker_id"].(string)
	credential, _ := values["worker_credential"].(string)
	if workerID == "" || credential == "" {
		t.Fatalf("worker.register missing one-time credential: %#v", values)
	}
	return registeredWorkerCredential{workerID: workerID, credential: credential}
}

func assertWorkerAuthError(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil || err.Error() != workerAuthenticationError {
		t.Fatalf("authentication error = %v, want %q", err, workerAuthenticationError)
	}
	if secret != "" && strings.Contains(err.Error(), secret) {
		t.Fatal("authentication error leaked credential")
	}
}

func assertCredentialAbsent(t *testing.T, surface string, raw []byte, credential string) {
	t.Helper()
	if strings.Contains(string(raw), credential) || strings.Contains(string(raw), "worker_credential") {
		t.Fatalf("%s leaked credential material: %s", surface, raw)
	}
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}
