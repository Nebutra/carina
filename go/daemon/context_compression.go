package daemon

import (
	"context"
	"fmt"
	"sync"

	"github.com/Nebutra/carina/go/contextengine"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const compactionFailureThreshold = 3

type compactionCircuitBreaker struct {
	mu       sync.Mutex
	failures map[string]int
	opened   map[string]bool
}

func newCompactionCircuitBreaker() *compactionCircuitBreaker {
	return &compactionCircuitBreaker{failures: map[string]int{}, opened: map[string]bool{}}
}
func (b *compactionCircuitBreaker) isOpen(taskID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.opened[taskID]
}
func (b *compactionCircuitBreaker) failure(taskID string) (int, bool) {
	if b == nil {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures[taskID]++
	if b.failures[taskID] >= compactionFailureThreshold {
		b.opened[taskID] = true
	}
	return b.failures[taskID], b.opened[taskID]
}
func (b *compactionCircuitBreaker) success(taskID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failures, taskID)
	delete(b.opened, taskID)
}
func (b *compactionCircuitBreaker) snapshot() map[string]any {
	if b == nil {
		return map[string]any{"open": 0}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	open := 0
	for _, v := range b.opened {
		if v {
			open++
		}
	}
	return map[string]any{"open": open, "threshold": compactionFailureThreshold}
}

// compressObservation rewrites only the model-facing projection. The original
// tool lifecycle remains in the audit chain, while the reversible Headroom ref
// and Carina-computed preimage hash travel with the checkpointed observation.
func (d *Daemon) compressObservation(ctx context.Context, sess *sessionstore.Session, task *scheduler.Task, turn int, tool, content string, pinned bool) (Observation, error) {
	obs := Observation{Tool: tool, Content: content, Pinned: pinned}
	if pinned || d.contextEng == nil || content == "" {
		return obs, nil
	}
	if d.compactionBreaker != nil && d.compactionBreaker.isOpen(task.TaskID) {
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{"status": "context_compaction_circuit_open", "turn": turn, "tool": tool}, "")
		return obs, nil
	}
	res, err := d.contextEng.Compress(ctx, contextengine.CompressRequest{
		SessionID: sess.SessionID,
		TaskID:    task.TaskID,
		Turn:      turn,
		Kind:      "observation",
		Tool:      tool,
		Content:   content,
	})
	if err != nil {
		failures, opened := d.compactionBreaker.failure(task.TaskID)
		st := d.contextEng.Status()
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
			"status": "context_engine_failed", "engine": st.EffectiveEngine, "turn": turn, "kind": "observation", "tool": tool,
			"original_bytes": len(content), "original_sha256": sha256Hex(content), "error": err.Error(), "consecutive_failures": failures, "circuit_open": opened,
		}, "")
		return obs, nil
	}
	d.compactionBreaker.success(task.TaskID)
	if res.Engine != contextengine.ModeHeadroom {
		st := d.contextEng.Status()
		if st.Degraded && st.LastError != "" {
			d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
				"status": "context_engine_failed", "engine": contextengine.ModeHeadroom, "fallback_engine": res.Engine,
				"turn": turn, "kind": "observation", "tool": tool,
				"original_bytes": len(content), "original_sha256": res.OriginalSHA256, "error": st.LastError,
			}, "")
		}
		return obs, nil
	}
	if res.Content == "" || res.OriginalSHA256 == "" || res.OriginalRef == "" {
		return obs, fmt.Errorf("context compression returned incomplete reversible metadata")
	}
	obs.Content = res.Content
	obs.OriginalRef = res.OriginalRef
	obs.OriginalSHA256 = res.OriginalSHA256
	obs.CompressionEngine = res.Engine
	obs.OriginalBytes = res.OriginalBytes
	obs.CompressedBytes = res.CompressedBytes
	obs.OriginalTokens = res.OriginalTokens
	obs.CompressedTokens = res.CompressedTokens
	obs.SavingsPercent = res.SavingsPercent
	obs.Transforms = append([]string(nil), res.Transforms...)
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
		"status": "context_compressed", "engine": res.Engine, "turn": turn, "kind": "observation", "tool": tool,
		"original_bytes": res.OriginalBytes, "compressed_bytes": res.CompressedBytes,
		"original_tokens": res.OriginalTokens, "compressed_tokens": res.CompressedTokens,
		"savings_percent": res.SavingsPercent, "transforms": res.Transforms,
		"original_sha256": res.OriginalSHA256, "original_ref": res.OriginalRef,
	}, "")
	return obs, nil
}
