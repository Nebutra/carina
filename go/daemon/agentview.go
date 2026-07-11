package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/agentview"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func (d *Daemon) agentRoster() agentview.Roster {
	pending := map[string]string{}
	d.questionMu.Lock()
	for _, q := range d.pendingQuestions {
		pending[q.taskID] = "user_response"
	}
	d.questionMu.Unlock()
	return agentview.Build(d.store.List(), d.sched.List(), pending, d.agentView.Snapshot())
}

func (d *Daemon) handleAgentView(_ json.RawMessage) (any, error) { return d.agentRoster(), nil }

func (d *Daemon) handleAgentPeek(params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	for _, group := range [][]agentview.Entry{d.agentRoster().NeedsInput, d.agentRoster().Working, d.agentRoster().Completed} {
		for _, e := range group {
			if e.TaskID == p.TaskID {
				return e, nil
			}
		}
	}
	return nil, fmt.Errorf("unknown task %s", p.TaskID)
}

func (d *Daemon) handleAgentRecap(params json.RawMessage) (any, error) {
	entry, err := d.handleAgentPeek(params)
	if err != nil {
		return nil, err
	}
	e := entry.(agentview.Entry)
	events, err := d.store.ReadEvents(e.SessionID)
	if err != nil {
		return nil, err
	}
	const limit = 20
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return map[string]any{"agent": e, "recent_events": events}, nil
}

func (d *Daemon) handleAgentStop(params json.RawMessage) (any, error) {
	return d.handleTaskCancel(params)
}

func (d *Daemon) handleAgentRemove(params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	t, ok := d.sched.Get(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("unknown task %s", p.TaskID)
	}
	if err := d.sched.Remove(p.TaskID); err != nil {
		return nil, err
	}
	return map[string]any{"removed": true, "task_id": p.TaskID, "session_id": t.SessionID}, nil
}

func (d *Daemon) handleAgentMetadataSet(params json.RawMessage) (any, error) {
	var p struct {
		SessionID   string `json:"session_id"`
		Title       string `json:"title"`
		PullRequest string `json:"pull_request"`
		Branch      string `json:"branch"`
		WorktreeID  string `json:"worktree_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	m := agentview.Metadata{Title: strings.TrimSpace(p.Title), PullRequest: strings.TrimSpace(p.PullRequest), Branch: strings.TrimSpace(p.Branch), WorktreeID: strings.TrimSpace(p.WorktreeID)}
	if err := d.agentView.Set(p.SessionID, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (d *Daemon) handleAgentDispatch(params json.RawMessage) (any, error) {
	var p struct {
		WorkspaceRoot string `json:"workspace_root"`
		Prompt        string `json:"prompt"`
		Profile       string `json:"profile"`
		ApprovalMode  string `json:"approval_mode"`
		Agent         string `json:"agent"`
		Model         string `json:"model"`
		Isolate       bool   `json:"isolate"`
		BaseRef       string `json:"base_ref"`
		Branch        string `json:"branch"`
		Title         string `json:"title"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	workspace := p.WorkspaceRoot
	var worktreeID string
	if p.Isolate {
		worktreeID = sessionstore.NewID("agent")
		rec, err := d.worktrees.Create(worktreeID, workspace, p.BaseRef, p.Branch, worktreeID)
		if err != nil {
			return nil, err
		}
		workspace = rec.Path
	}
	sessAny, err := d.handleSessionCreate(agentViewRaw(map[string]any{"workspace_root": workspace, "profile": p.Profile, "approval_mode": p.ApprovalMode}))
	if err != nil {
		if worktreeID != "" {
			_ = d.worktrees.Cleanup(worktreeID, worktreeID, true)
		}
		return nil, err
	}
	sess := sessAny.(*sessionstore.Session)
	if worktreeID != "" {
		_, _ = d.worktrees.Lock(worktreeID, sess.SessionID)
	}
	_ = d.agentView.Set(sess.SessionID, agentview.Metadata{Title: p.Title, Branch: p.Branch, WorktreeID: worktreeID})
	taskAny, err := d.handleTaskSubmit(agentViewRaw(map[string]any{"session_id": sess.SessionID, "prompt": p.Prompt, "agent": p.Agent, "model": p.Model}))
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": sess, "task": taskAny, "worktree_id": worktreeID}, nil
}

func agentViewRaw(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }

func (d *Daemon) handleWorktreeCreate(params json.RawMessage) (any, error) {
	var p struct{ ID, RepoRoot, BaseRef, Branch, Owner string }
	_ = p
	var q map[string]string
	if err := json.Unmarshal(params, &q); err != nil {
		return nil, err
	}
	return d.worktrees.Create(q["id"], q["repo_root"], q["base_ref"], q["branch"], q["owner"])
}
func (d *Daemon) handleWorktreeList(_ json.RawMessage) (any, error) { return d.worktrees.List() }
func (d *Daemon) handleWorktreeEnter(params json.RawMessage) (any, error) {
	var p struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	// Enter also acquires the ownership lock, making the returned path safe to
	// hand to a session without a competing agent cleaning it up.
	return d.worktrees.Lock(p.ID, p.Owner)
}
func (d *Daemon) handleWorktreeLock(params json.RawMessage) (any, error) {
	var p struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return d.worktrees.Lock(p.ID, p.Owner)
}
func (d *Daemon) handleWorktreeUnlock(params json.RawMessage) (any, error) {
	var p struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return d.worktrees.Unlock(p.ID, p.Owner)
}
func (d *Daemon) handleWorktreeCleanup(params json.RawMessage) (any, error) {
	var p struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
		Force bool   `json:"force"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if err := d.worktrees.Cleanup(p.ID, p.Owner, p.Force); err != nil {
		return nil, err
	}
	return map[string]any{"removed": true, "id": p.ID}, nil
}
