package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const executionControlVersion = 1

const (
	maxQueuedSteers = 128
	maxSteerBytes   = 16 << 10
)

type queuedSteer struct {
	SteerID  string        `json:"steer_id"`
	Message  string        `json:"message"`
	Priority steerPriority `json:"priority"`
}

type executionControlRecord struct {
	Version                int           `json:"version"`
	RunID                  string        `json:"run_id"`
	Urgent                 []queuedSteer `json:"urgent,omitempty"`
	Normal                 []queuedSteer `json:"normal,omitempty"`
	SoftInterruptRequested bool          `json:"soft_interrupt_requested,omitempty"`
}

func (r *runStore) saveExecutionControl(record executionControlRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record.Version = executionControlVersion
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(r.dir, record.RunID+".control.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r *runStore) loadExecutionControls() map[string]executionControlRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	out := map[string]executionControlRecord{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".control.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			continue
		}
		var record executionControlRecord
		if json.Unmarshal(raw, &record) == nil && record.Version == executionControlVersion && record.RunID != "" {
			out[record.RunID] = record
		}
	}
	return out
}

func (r *runStore) deleteExecutionControl(runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := os.Remove(filepath.Join(r.dir, runID+".control.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func acceptsExecutionControl(status string) bool {
	switch status {
	case "queued", "running", "waiting_approval":
		return true
	default:
		return false
	}
}

func terminalExecutionStatus(status string) bool {
	switch status {
	case "completed", "failed", "degraded", "cancelled":
		return true
	default:
		return false
	}
}

func (m *taskMailbox) record(taskID string) executionControlRecord {
	if m == nil {
		return executionControlRecord{RunID: taskID}
	}
	return executionControlRecord{
		RunID: taskID, Urgent: append([]queuedSteer(nil), m.urgent...), Normal: append([]queuedSteer(nil), m.normal...),
		SoftInterruptRequested: m.softInterruptRequested,
	}
}

func (d *Daemon) enqueueSteer(taskID, steerID, message string, priority steerPriority) (int, error) {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	task, ok := d.sched.Get(taskID)
	if !ok {
		return 0, fmt.Errorf("unknown execution %s", taskID)
	}
	if !acceptsExecutionControl(task.Status) {
		return 0, fmt.Errorf("execution %s is %s and cannot be steered", taskID, task.Status)
	}
	box := d.mailbox[taskID]
	if box == nil {
		box = &taskMailbox{}
		d.mailbox[taskID] = box
	}
	if len(message) > maxSteerBytes {
		return box.depth(), fmt.Errorf("steering message exceeds %d bytes", maxSteerBytes)
	}
	for _, existing := range append(append([]queuedSteer(nil), box.urgent...), box.normal...) {
		if existing.SteerID == steerID {
			if existing.Message != message || existing.Priority != priority {
				return box.depth(), fmt.Errorf("steer_id %s already exists with different content", steerID)
			}
			return box.depth(), nil
		}
	}
	if box.depth() >= maxQueuedSteers {
		return box.depth(), fmt.Errorf("steering queue is full (maximum %d messages)", maxQueuedSteers)
	}
	entry := queuedSteer{SteerID: steerID, Message: message, Priority: priority}
	box.pushEntry(entry)
	if err := d.runs.saveExecutionControl(box.record(taskID)); err != nil {
		box.remove(steerID)
		return box.depth(), fmt.Errorf("persist steering queue: %w", err)
	}
	return box.depth(), nil
}

func (d *Daemon) peekMailbox(taskID string) []queuedSteer {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	return d.mailbox[taskID].peek()
}

func (d *Daemon) acknowledgeMailbox(taskID string, messages []queuedSteer) error {
	if len(messages) == 0 {
		return nil
	}
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	box := d.mailbox[taskID]
	if box == nil {
		return nil
	}
	for _, message := range messages {
		box.remove(message.SteerID)
	}
	if err := d.runs.saveExecutionControl(box.record(taskID)); err != nil {
		return err
	}
	if box.empty() && !box.softInterruptRequested {
		delete(d.mailbox, taskID)
	}
	return nil
}

func (d *Daemon) queueDepth(taskID string) int {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	return d.mailbox[taskID].depth()
}

// listQueuedSteers returns operator-facing queue entries with truncated previews.
// Full message bodies are never returned on list/status surfaces.
func (d *Daemon) listQueuedSteers(taskID string, previewCells int) []map[string]any {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	pending := d.mailbox[taskID].peek()
	if len(pending) == 0 {
		return []map[string]any{}
	}
	if previewCells <= 0 {
		previewCells = 48
	}
	out := make([]map[string]any, 0, len(pending))
	for index, entry := range pending {
		out = append(out, map[string]any{
			"steer_id": entry.SteerID,
			"priority": string(entry.Priority),
			"preview":  truncateSteerPreview(entry.Message, previewCells),
			"index":    index,
		})
	}
	return out
}

func (d *Daemon) dropQueuedSteer(taskID, steerID string) (bool, int, error) {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	task, ok := d.sched.Get(taskID)
	if !ok {
		return false, 0, fmt.Errorf("unknown execution %s", taskID)
	}
	if terminalExecutionStatus(task.Status) {
		return false, 0, fmt.Errorf("execution %s is %s and cannot drop steers", taskID, task.Status)
	}
	box := d.mailbox[taskID]
	if box == nil {
		return false, 0, nil
	}
	before := box.depth()
	box.remove(steerID)
	if box.depth() == before {
		return false, before, nil
	}
	if err := d.runs.saveExecutionControl(box.record(taskID)); err != nil {
		// Best-effort restore is not available after remove; fail closed on persist
		// would leave memory ahead of disk — re-push is not safe without content.
		return false, box.depth(), fmt.Errorf("persist steering queue: %w", err)
	}
	if box.empty() && !box.softInterruptRequested {
		delete(d.mailbox, taskID)
	}
	return true, box.depth(), nil
}

func truncateSteerPreview(message string, maxCells int) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	// Approximate terminal cells as runes for operator previews (not layout-critical).
	runes := []rune(message)
	if len(runes) <= maxCells {
		return message
	}
	if maxCells <= 1 {
		return "…"
	}
	return string(runes[:maxCells-1]) + "…"
}

func (d *Daemon) requestSoftInterrupt(taskID string) (bool, int, error) {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	task, ok := d.sched.Get(taskID)
	if !ok {
		return false, 0, fmt.Errorf("unknown execution %s", taskID)
	}
	if !acceptsExecutionControl(task.Status) {
		return false, 0, fmt.Errorf("execution %s is %s and cannot be interrupted", taskID, task.Status)
	}
	box := d.mailbox[taskID]
	if box == nil {
		box = &taskMailbox{}
		d.mailbox[taskID] = box
	}
	already := box.softInterruptRequested
	box.softInterruptRequested = true
	if err := d.runs.saveExecutionControl(box.record(taskID)); err != nil {
		box.softInterruptRequested = already
		return already, box.depth(), err
	}
	return already, box.depth(), nil
}

func (d *Daemon) softInterruptRequested(taskID string) bool {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	return d.mailbox[taskID] != nil && d.mailbox[taskID].softInterruptRequested
}

func (d *Daemon) clearSoftInterrupt(taskID string) error {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	box := d.mailbox[taskID]
	if box == nil {
		return nil
	}
	box.softInterruptRequested = false
	if err := d.runs.saveExecutionControl(box.record(taskID)); err != nil {
		box.softInterruptRequested = true
		return err
	}
	if box.empty() {
		delete(d.mailbox, taskID)
	}
	return nil
}

func (d *Daemon) discardExecutionControl(taskID string) error {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	if err := d.runs.deleteExecutionControl(taskID); err != nil {
		return err
	}
	delete(d.mailbox, taskID)
	return nil
}

func (d *Daemon) cleanupTerminalExecutionControl(taskID string) error {
	d.mailboxMu.Lock()
	defer d.mailboxMu.Unlock()
	task, ok := d.sched.Get(taskID)
	if ok && !terminalExecutionStatus(task.Status) {
		return nil
	}
	if err := d.runs.deleteExecutionControl(taskID); err != nil {
		return err
	}
	delete(d.mailbox, taskID)
	return nil
}

func (d *Daemon) taskWithControl(task any, runID string) map[string]any {
	raw, _ := json.Marshal(task)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	out["queue_depth"] = d.queueDepth(runID)
	out["soft_interrupt_pending"] = d.softInterruptRequested(runID)
	return out
}

func controlRunIDs(records map[string]executionControlRecord) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
