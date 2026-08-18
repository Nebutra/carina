package agentview

import (
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"testing"
	"time"
)

func TestBuildProjectsProfileParentAndWorktree(t *testing.T) {
	now := time.Now().UTC()
	sessions := []*sessionstore.Session{{
		SessionID: "child", ParentID: "parent", WorkspaceRoot: "/repo/.carina/worktrees/wt1",
		PermissionProfile: "read-only", Status: "active", CreatedAt: now,
	}}
	runs := []*scheduler.ExecutionRun{{
		RunID: "run_child", SessionID: "child", Status: "running", Agent: "explore", UpdatedAt: now,
	}}
	r := Build(sessions, runs, nil, map[string]Metadata{"child": {WorktreeID: "wt1"}})
	if len(r.Working) != 1 {
		t.Fatalf("working: %+v", r)
	}
	got := r.Working[0]
	if got.TaskID != "run_child" || got.RunID != "run_child" {
		t.Fatalf("ids = run=%q task=%q", got.RunID, got.TaskID)
	}
	if got.ParentID != "parent" || got.Profile != "read-only" || got.WorktreeID != "wt1" || got.Agent != "explore" {
		t.Fatalf("roster fields: %+v", got)
	}
}

func TestBuildClassifiesAndSorts(t *testing.T) {
	now := time.Now().UTC()
	sessions := []*sessionstore.Session{{SessionID: "s1", WorkspaceRoot: "/repo", Status: "active", CreatedAt: now}}
	runs := []*scheduler.ExecutionRun{{RunID: "old", SessionID: "s1", Status: "running", UpdatedAt: now.Add(-time.Minute)}, {RunID: "ask", SessionID: "s1", Status: "waiting_input", UpdatedAt: now}}
	r := Build(sessions, runs, map[string]string{"ask": "Choose target"}, map[string]Metadata{"s1": {PullRequest: "https://example/pr/1"}})
	if len(r.NeedsInput) != 1 || r.NeedsInput[0].Needs != "Choose target" {
		t.Fatalf("needs input: %+v", r)
	}
	if len(r.Working) != 1 || r.Working[0].Metadata.PullRequest == "" {
		t.Fatalf("working: %+v", r)
	}
}

func TestMetadataPersists(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	if err := s.Set("s", Metadata{Branch: "feature"}); err != nil {
		t.Fatal(err)
	}
	if got := Open(dir).Snapshot()["s"].Branch; got != "feature" {
		t.Fatalf("got %q", got)
	}
}
