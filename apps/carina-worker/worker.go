package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type rpcCaller interface {
	Call(method string, params any, result any) error
}

type registration struct {
	WorkerID         string `json:"worker_id"`
	WorkerCredential string `json:"worker_credential"`
}

type pollResponse struct {
	Empty        bool            `json:"empty"`
	Task         json.RawMessage `json:"task"`
	Backpressure struct {
		MaxInflight int `json:"max_inflight"`
	} `json:"backpressure"`
}

type renewResponse struct {
	OK         bool  `json:"ok"`
	Cancelled  bool  `json:"cancelled"`
	LeaseValid *bool `json:"lease_valid,omitempty"`
}

type leaseWorker struct {
	caller   rpcCaller
	executor taskExecutor
	cfg      workerConfig
	logger   *log.Logger

	reg      registration
	activeMu sync.Mutex
	active   map[string]*activeLease
	activeWG sync.WaitGroup
	pressure atomic.Uint64
	inflight atomic.Int64
}

type activeLease struct {
	cancel   context.CancelFunc
	lost     atomic.Bool
	stopping atomic.Bool
}

func newLeaseWorker(caller rpcCaller, executor taskExecutor, cfg workerConfig, logger *log.Logger) *leaseWorker {
	if logger == nil {
		logger = log.Default()
	}
	return &leaseWorker{caller: caller, executor: executor, cfg: cfg, logger: logger, active: make(map[string]*activeLease)}
}

func (w *leaseWorker) Run(stopPolling context.Context) error {
	if err := w.register(); err != nil {
		return err
	}
	w.logger.Printf("carina-worker %q (%s) joined as %s", w.cfg.Name, w.cfg.Kind, w.reg.WorkerID)

	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		w.heartbeatLoop(heartbeatCtx)
	}()

	err := w.pollLoop(stopPolling)
	stopHeartbeat()
	<-heartbeatDone
	w.drain()
	if revokeErr := w.call("worker.revoke", map[string]any{}, nil); revokeErr != nil {
		w.logger.Printf("carina-worker: revoke failed: %v", revokeErr)
	}
	w.logger.Printf("carina-worker: left the pool")
	return err
}

func (w *leaseWorker) register() error {
	if err := w.caller.Call("worker.register", map[string]any{
		"name": w.cfg.Name, "kind": w.cfg.Kind, "process_tree_containment": runtimeProcessTreeContainment(),
		"pools": []string(w.cfg.Pools),
	}, &w.reg); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if strings.TrimSpace(w.reg.WorkerID) == "" || strings.TrimSpace(w.reg.WorkerCredential) == "" {
		return fmt.Errorf("register: daemon did not return worker_id and worker_credential")
	}
	return nil
}

func (w *leaseWorker) authenticatedParams(extra map[string]any) map[string]any {
	p := make(map[string]any, len(extra)+2)
	p["worker_id"] = w.reg.WorkerID
	p["worker_credential"] = w.reg.WorkerCredential
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func (w *leaseWorker) call(method string, extra map[string]any, result any) error {
	return w.caller.Call(method, w.authenticatedParams(extra), result)
}

func (w *leaseWorker) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.call("worker.heartbeat", map[string]any{}, nil); err != nil {
				w.logger.Printf("carina-worker: heartbeat failed: %v", err)
			}
			seq := w.pressure.Add(1)
			if err := w.call("backpressure.report", map[string]any{
				"queue_depth": 0,
				"inflight":    w.inflight.Load(),
				"seq":         seq,
			}, nil); err != nil {
				w.logger.Printf("carina-worker: backpressure report failed: %v", err)
			}
		}
	}
}

func (w *leaseWorker) pollLoop(ctx context.Context) error {
	sem := make(chan struct{}, w.cfg.MaxConcurrency)
	backoff := w.cfg.PollMinBackoff
	for {
		select {
		case <-ctx.Done():
			return nil
		case sem <- struct{}{}:
		}

		var response pollResponse
		err := w.call("work.poll", map[string]any{
			"ttl_ms":          w.cfg.LeaseTTL.Milliseconds(),
			"available_slots": w.cfg.MaxConcurrency - int(w.inflight.Load()),
		}, &response)
		if err != nil {
			<-sem
			w.logger.Printf("carina-worker: poll failed: %v", err)
			if !waitContext(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff, w.cfg.PollMaxBackoff)
			continue
		}
		if len(response.Task) == 0 || string(response.Task) == "null" || response.Empty {
			<-sem
			if !waitContext(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff, w.cfg.PollMaxBackoff)
			continue
		}
		backoff = w.cfg.PollMinBackoff

		var task struct {
			TaskID          string `json:"task_id"`
			LeaseGeneration int    `json:"lease_generation"`
		}
		if err := json.Unmarshal(response.Task, &task); err != nil || strings.TrimSpace(task.TaskID) == "" || task.LeaseGeneration <= 0 {
			<-sem
			w.logger.Printf("carina-worker: daemon returned a lease without a valid task id and generation")
			continue
		}
		if ctx.Err() != nil {
			<-sem
			w.report(task.TaskID, task.LeaseGeneration, failedResult("worker stopped before executor start"))
			return nil
		}
		w.inflight.Add(1)
		w.activeWG.Add(1)
		go func(taskID string, raw json.RawMessage) {
			defer func() {
				w.inflight.Add(-1)
				<-sem
				w.activeWG.Done()
			}()
			w.executeLease(taskID, raw)
		}(task.TaskID, append(json.RawMessage(nil), response.Task...))
	}
}

func (w *leaseWorker) executeLease(taskID string, raw json.RawMessage) {
	var task struct {
		LeaseGeneration int `json:"lease_generation"`
	}
	if err := json.Unmarshal(raw, &task); err != nil || task.LeaseGeneration <= 0 {
		w.logger.Printf("carina-worker: lease %s has no valid generation", taskID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.ExecutorTimeout)
	lease := &activeLease{cancel: cancel}
	w.activeMu.Lock()
	w.active[taskID] = lease
	w.activeMu.Unlock()
	defer func() {
		cancel()
		w.activeMu.Lock()
		delete(w.active, taskID)
		w.activeMu.Unlock()
	}()

	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		w.renewLoop(ctx, taskID, task.LeaseGeneration, lease)
	}()
	result := w.executor.Execute(ctx, raw)
	cancel()
	<-renewDone
	if lease.lost.Load() || lease.stopping.Load() {
		return
	}
	w.report(taskID, task.LeaseGeneration, result)
}

func (w *leaseWorker) renewLoop(ctx context.Context, taskID string, generation int, lease *activeLease) {
	ticker := time.NewTicker(w.cfg.RenewInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var response renewResponse
			err := w.call("work.renew", map[string]any{
				"task_id":          taskID,
				"lease_generation": generation,
				"ttl_ms":           w.cfg.LeaseTTL.Milliseconds(),
			}, &response)
			if err != nil {
				failures++
				if leaseLostError(err) || failures >= 2 {
					lease.lost.Store(true)
					lease.cancel()
					return
				}
				w.logger.Printf("carina-worker: renew %s failed: %v", taskID, err)
				continue
			}
			failures = 0
			if response.Cancelled || (response.LeaseValid != nil && !*response.LeaseValid) {
				lease.lost.Store(true)
				lease.cancel()
				return
			}
		}
	}
}

func (w *leaseWorker) report(taskID string, generation int, result executionResult) {
	if err := w.call("work.report", map[string]any{
		"task_id":          taskID,
		"lease_generation": generation,
		"status":           result.Status,
		"summary":          result.Summary,
		"patches":          result.Patches,
	}, nil); err != nil {
		w.logger.Printf("carina-worker: report %s failed: %v", taskID, err)
	}
}

func (w *leaseWorker) drain() {
	done := make(chan struct{})
	go func() {
		w.activeWG.Wait()
		close(done)
	}()
	timer := time.NewTimer(w.cfg.DrainTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		w.activeMu.Lock()
		for _, lease := range w.active {
			lease.stopping.Store(true)
			lease.cancel()
		}
		w.activeMu.Unlock()
		<-done
	}
}

func leaseLostError(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"not leased", "another worker", "unknown task", "cancelled", "canceled", "lease expired"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func waitContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current, max time.Duration) time.Duration {
	if current >= max/2 {
		return max
	}
	return current * 2
}
