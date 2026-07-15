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

func TestApprovedMemoryWriteProjectsAndRemovalTombstones(t *testing.T) {
	var puts, deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("missing HMS token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/memories"):
			var body struct {
				Items []struct {
					Content    string `json:"content"`
					DocumentID string `json:"document_id"`
					UpdateMode string `json:"update_mode"`
				} `json:"items"`
				Async bool `json:"async"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Items) != 1 || body.Items[0].UpdateMode != "replace" {
				t.Fatalf("unexpected desired state: %+v", body)
			}
			if !strings.Contains(body.Items[0].Content, `"tombstone":true`) {
				if !strings.Contains(body.Items[0].Content, `"entries":["Use focused tests."]`) {
					t.Fatalf("unexpected desired state: %+v", body)
				}
				puts.Add(1)
			}
			bank := strings.Split(r.URL.Path, "/")[4]
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "bank_id": bank, "items_count": 1, "async": false})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/documents/"):
			deletes.Add(1)
			documentID := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "document_id": documentID})
		default:
			t.Fatalf("unexpected HMS request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, sess.PermissionProfile, nil); err != nil {
		t.Fatal(err)
	}
	p, err := newHMSRecallProvider(memoryProviderHMSHybrid, srv.URL, "token", []byte(strings.Repeat("z", 32)), time.Second, 8)
	if err != nil {
		t.Fatal(err)
	}
	d.memoryHMS = p
	d.memoryProjection, err = newMemoryProjectionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.memoryProjectionExecutor = hmsOutboxExecutor{provider: p}

	if err := d.approvalGrants.add(approvalGrant{Scope: approvalScopeProject, WorkspaceRoot: ws, Capability: "NetworkAccess", Resource: "127.0.0.1", SourceDecisionID: "dec_network", Approver: "test", CreatedAt: time.Now().UTC()}, nil); err != nil {
		t.Fatal(err)
	}

	write := func(req memoryWriteRequest) memoryWriteResult {
		scope := memoryScopeFromSession(sess)
		summary, err := summarizeMemoryWrite(scope, req)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := d.kern.Request(sess.SessionID, "MemoryWrite", summary.Resource, "")
		if err != nil {
			t.Fatal(err)
		}
		if decision.Decision == "requires_approval" {
			decision, err = d.kern.ApproveWithRole(sess.SessionID, decision.DecisionID, "test", "")
			if err != nil {
				t.Fatal(err)
			}
		}
		result, err := d.applyMemoryWrite(sess, "", req, decision, scope, summary)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	added := write(memoryWriteRequest{Action: "add", Target: memoryTargetMemory, Content: "Use focused tests."})
	if added.Projection == nil || added.Projection.Status != projectionBlocked || added.Projection.DecisionID == "" {
		if added.Projection != nil {
			t.Fatalf("externalization approval not surfaced: %+v", *added.Projection)
		}
		t.Fatalf("externalization approval not surfaced: nil")
	}
	raw, _ := json.Marshal(map[string]any{"session_id": sess.SessionID, "decision_id": added.Projection.DecisionID, "approver": "test", "scope": "project"})
	if _, err := d.handleApprove(raw); err != nil {
		t.Fatal(err)
	}
	if processed, err := d.memoryProjection.ProcessOne(context.Background(), d.memoryProjectionExecutor); !processed || err != nil {
		t.Fatalf("projection process=%v err=%v", processed, err)
	}
	if puts.Load() != 1 {
		t.Fatalf("retain calls=%d", puts.Load())
	}

	removed := write(memoryWriteRequest{Action: "remove", Target: memoryTargetMemory, OldText: "Use focused tests."})
	if removed.Projection == nil || removed.Projection.Status != projectionBlocked {
		t.Fatalf("delete must have revision-bound approval: %+v", removed)
	}
	raw, _ = json.Marshal(map[string]any{"session_id": sess.SessionID, "decision_id": removed.Projection.DecisionID, "approver": "test", "scope": "once"})
	if _, err := d.handleApprove(raw); err != nil {
		t.Fatal(err)
	}
	if processed, err := d.memoryProjection.ProcessOne(context.Background(), d.memoryProjectionExecutor); !processed || err != nil {
		t.Fatalf("delete process=%v err=%v", processed, err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("delete calls=%d", deletes.Load())
	}
}

func TestDirtyProjectionRebuildsFromLocalAuthorityAfterCrash(t *testing.T) {
	stateDir := t.TempDir()
	scope := memoryScope{Profile: "profile", WorkspaceRoot: "/workspace", WorkspaceHash: "hash"}
	store, err := newMemoryProjectionStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	dirty, err := store.MarkDirty(scope, memoryTargetMemory, "bank", "sess_crashed")
	if err != nil {
		t.Fatal(err)
	}
	memory := newMemoryStore(stateDir)
	if _, err := memory.apply(scope, memoryWriteRequest{Action: "add", Target: memoryTargetMemory, Content: "Recovered desired state."}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newMemoryProjectionStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{memory: memory, memoryProjection: reloaded}
	d.reconcileDirtyMemoryProjections()
	intent, ok := reloaded.Get(dirty.DocumentID, dirty.Generation)
	if !ok || intent.Status != projectionBlocked || !strings.Contains(intent.Content, "Recovered desired state") {
		t.Fatalf("dirty recovery failed: %+v %v", intent, ok)
	}
}
