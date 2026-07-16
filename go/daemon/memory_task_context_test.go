package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskMemoryEvidenceGovernanceAndModes(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, sess.PermissionProfile, nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "What are the release checks?")

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": "1", "text": "Run focused tests before release."}}})
	}))
	defer srv.Close()
	p, err := newHMSRecallProvider(memoryProviderHMSHybrid, srv.URL, "token", []byte(strings.Repeat("e", 32)), time.Second, 8)
	if err != nil {
		t.Fatal(err)
	}
	d.memoryHMS = p

	// The built-in profile requires network approval. Without a stored grant,
	// recall fails closed before the server receives a request.
	if got := d.buildTaskMemoryEvidence(context.Background(), sess, task); got != "" {
		t.Fatalf("ungoverned evidence returned: %s", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("policy denial still contacted HMS: %d", calls.Load())
	}
	if got := p.Health().LastReason; got != "network_policy_denied" {
		t.Fatalf("reason=%q", got)
	}

	if err := d.approvalGrants.add(approvalGrant{
		Scope: approvalScopeSession, SessionID: sess.SessionID, WorkspaceRoot: ws,
		Capability: "NetworkAccess", Resource: "127.0.0.1", SourceDecisionID: "dec_test",
		Approver: "test", CreatedAt: time.Now().UTC(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.approvalGrants.add(approvalGrant{
		Scope: approvalScopeSession, SessionID: sess.SessionID, WorkspaceRoot: ws,
		Capability: "MemoryExternalize", Resource: "provider=hms host=127.0.0.1 query_sha256=" + hashMemoryQuery(task.UserPrompt) + " targets=user,memory", SourceDecisionID: "dec_externalize",
		Approver: "test", CreatedAt: time.Now().UTC(),
	}, nil); err != nil {
		t.Fatal(err)
	}

	evidence := d.buildTaskMemoryEvidence(context.Background(), sess, task)
	if !strings.Contains(evidence, "Run focused tests") || calls.Load() != 2 {
		t.Fatalf("hybrid evidence/calls: %q %d", evidence, calls.Load())
	}
	if !p.Health().Authorized {
		t.Fatal("stored grant was not reflected in health")
	}

	p.mode = memoryProviderHMSShadow
	if got := d.buildTaskMemoryEvidence(context.Background(), sess, task); got != "" {
		t.Fatalf("shadow evidence entered context: %s", got)
	}
	if calls.Load() != 4 {
		t.Fatalf("shadow did not execute recall: %d", calls.Load())
	}
}
