package tuiapp

import (
	"path/filepath"
	"testing"
)

func TestResolveSessionPrefersPendingThenLastActive(t *testing.T) {
	// Without real state files, both helpers return empty; list path uses call.
	call := &fakeRPC{sessions: []map[string]any{
		{"session_id": "sess_old", "workspace_root": "/ws", "created_at": "2026-01-01T00:00:00Z"},
		{"session_id": "sess_new", "workspace_root": "/ws", "created_at": "2026-06-01T00:00:00Z"},
		{"session_id": "sess_other", "workspace_root": "/other", "created_at": "2026-07-01T00:00:00Z"},
	}}
	id, err := resolveSession(call, t.TempDir(), "/ws")
	if err != nil {
		t.Fatal(err)
	}
	if id != "sess_new" {
		t.Fatalf("got %q, want sess_new", id)
	}
}

func TestResolveSessionEmptyWithoutMatch(t *testing.T) {
	call := &fakeRPC{sessions: []map[string]any{
		{"session_id": "sess_other", "workspace_root": "/other", "created_at": "2026-07-01T00:00:00Z"},
	}}
	id, err := resolveSession(call, filepath.Join(t.TempDir(), "missing"), "/ws")
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("got %q, want empty", id)
	}
}

type fakeRPC struct {
	sessions []map[string]any
}

func (f *fakeRPC) Call(method string, params any, result any) error {
	if method != "session.list" {
		return nil
	}
	// result is *[]struct{...} — use JSON round-trip via assignment of maps
	// by encoding into the pointer type loosely.
	switch out := result.(type) {
	case *[]struct {
		SessionID     string `json:"session_id"`
		WorkspaceRoot string `json:"workspace_root"`
		CreatedAt     string `json:"created_at"`
	}:
		for _, s := range f.sessions {
			*out = append(*out, struct {
				SessionID     string `json:"session_id"`
				WorkspaceRoot string `json:"workspace_root"`
				CreatedAt     string `json:"created_at"`
			}{
				SessionID:     str(s["session_id"]),
				WorkspaceRoot: str(s["workspace_root"]),
				CreatedAt:     str(s["created_at"]),
			})
		}
	}
	return nil
}

func (f *fakeRPC) Close() error { return nil }

func str(v any) string {
	s, _ := v.(string)
	return s
}
