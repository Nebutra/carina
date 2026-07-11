package agentview

import (
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"testing"
	"time"
)

func TestBuildClassifiesAndSorts(t *testing.T) {
	now := time.Now().UTC()
	sessions := []*sessionstore.Session{{SessionID: "s1", WorkspaceRoot: "/repo", Status: "active", CreatedAt: now}}
	tasks := []*scheduler.Task{{TaskID: "old", SessionID: "s1", Status: "running", UpdatedAt: now.Add(-time.Minute)}, {TaskID: "ask", SessionID: "s1", Status: "waiting_input", UpdatedAt: now}}
	r := Build(sessions, tasks, map[string]string{"ask": "Choose target"}, map[string]Metadata{"s1": {PullRequest: "https://example/pr/1"}})
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
