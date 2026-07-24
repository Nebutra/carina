package ui

import "strings"

type LiveToolStatus string

const (
	LiveToolRequested LiveToolStatus = "requested"
	LiveToolRunning   LiveToolStatus = "running"
	LiveToolApproval  LiveToolStatus = "approval"
	LiveToolCompleted LiveToolStatus = "completed"
	LiveToolFailed    LiveToolStatus = "failed"
	LiveToolDenied    LiveToolStatus = "denied"
	LiveToolCancelled LiveToolStatus = "cancelled"
)

func (s LiveToolStatus) Terminal() bool {
	switch s {
	case LiveToolCompleted, LiveToolFailed, LiveToolDenied, LiveToolCancelled:
		return true
	default:
		return false
	}
}

type LiveToolUpdate struct {
	CallID    string
	Tool      string
	Status    LiveToolStatus
	Summary   string
	Details   []string
	Timestamp string
}

type LiveToolSnapshot struct {
	CallID    string
	Tool      string
	Status    LiveToolStatus
	Summary   string
	Details   []string
	Timestamp string
	Revision  uint64
}

type LiveToolCell struct {
	Base
	state LiveToolSnapshot
}

func newLiveToolCell(callID string) *LiveToolCell {
	return &LiveToolCell{
		Base:  Base{ComponentID: ComponentID("tool:" + callID)},
		state: LiveToolSnapshot{CallID: callID, Status: LiveToolRequested},
	}
}

func (c *LiveToolCell) Observe(update LiveToolUpdate) bool {
	if update.CallID == "" || update.CallID != c.state.CallID {
		return false
	}
	if c.state.Status.Terminal() && !update.Status.Terminal() {
		return false
	}
	if update.Tool != "" {
		c.state.Tool = update.Tool
	}
	if update.Status != "" {
		c.state.Status = update.Status
	}
	if update.Summary != "" {
		c.state.Summary = update.Summary
	}
	if update.Details != nil {
		c.state.Details = append(c.state.Details[:0], update.Details...)
	}
	if update.Timestamp != "" {
		c.state.Timestamp = update.Timestamp
	}
	c.state.Revision++
	return true
}

func (c *LiveToolCell) Snapshot() LiveToolSnapshot {
	snapshot := c.state
	snapshot.Details = append([]string(nil), c.state.Details...)
	return snapshot
}

func (c *LiveToolCell) Measure(constraints Constraints) Size {
	height := 1
	if !c.state.Status.Terminal() {
		height += len(c.state.Details)
	}
	return constraints.Constrain(Size{Width: constraints.MaxWidth, Height: height})
}

func (c *LiveToolCell) Render(RenderContext) Node {
	label := strings.TrimSpace(strings.Join([]string{c.state.Tool, string(c.state.Status), c.state.Summary}, " "))
	if label == "" {
		label = c.state.CallID
	}
	return Node{
		ID: c.ComponentID, Bounds: c.Bounds, Content: label,
		Role: liveToolRole(c.state.Status), Focused: c.HasFocus,
	}
}

func liveToolRole(status LiveToolStatus) SemanticRole {
	switch status {
	case LiveToolCompleted:
		return RoleSuccess
	case LiveToolFailed, LiveToolDenied, LiveToolCancelled:
		return RoleError
	case LiveToolApproval:
		return RoleWarning
	default:
		return RoleMuted
	}
}

type LiveToolRegistry struct {
	cells map[string]*LiveToolCell
}

func NewLiveToolRegistry() *LiveToolRegistry {
	return &LiveToolRegistry{cells: make(map[string]*LiveToolCell)}
}

func (r *LiveToolRegistry) Observe(update LiveToolUpdate) (LiveToolSnapshot, bool) {
	if update.CallID == "" {
		return LiveToolSnapshot{}, false
	}
	if r.cells == nil {
		r.cells = make(map[string]*LiveToolCell)
	}
	cell := r.cells[update.CallID]
	if cell == nil {
		cell = newLiveToolCell(update.CallID)
		r.cells[update.CallID] = cell
	}
	if !cell.Observe(update) {
		return cell.Snapshot(), false
	}
	return cell.Snapshot(), true
}

func (r *LiveToolRegistry) Get(callID string) (LiveToolSnapshot, bool) {
	cell := r.cells[callID]
	if cell == nil {
		return LiveToolSnapshot{}, false
	}
	return cell.Snapshot(), true
}

func (r *LiveToolRegistry) Component(callID string) *LiveToolCell {
	return r.cells[callID]
}

func (r *LiveToolRegistry) Len() int { return len(r.cells) }
