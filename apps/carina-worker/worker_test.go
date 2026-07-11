package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeCaller struct {
	mu      sync.Mutex
	calls   []fakeCall
	handler func(string, map[string]any, any) error
}

type fakeCall struct {
	method string
	params map[string]any
}

func (f *fakeCaller) Call(method string, params any, result any) error {
	raw, _ := json.Marshal(params)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method: method, params: decoded})
	handler := f.handler
	f.mu.Unlock()
	return handler(method, decoded, result)
}

func (f *fakeCaller) snapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

func setResult(dst any, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

type blockingExecutor struct {
	started chan json.RawMessage
	release chan struct{}
	result  executionResult
	useCtx  bool
}

type concurrencyExecutor struct {
	started chan string
	release chan struct{}
	active  atomic.Int64
	max     atomic.Int64
}

func (e *concurrencyExecutor) Execute(_ context.Context, raw json.RawMessage) executionResult {
	var task struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(raw, &task)
	current := e.active.Add(1)
	for {
		previous := e.max.Load()
		if current <= previous || e.max.CompareAndSwap(previous, current) {
			break
		}
	}
	e.started <- task.TaskID
	<-e.release
	e.active.Add(-1)
	return executionResult{SchemaVersion: executorResultSchema, Status: "completed", Summary: task.TaskID}
}

func (e *blockingExecutor) Execute(ctx context.Context, raw json.RawMessage) executionResult {
	e.started <- append(json.RawMessage(nil), raw...)
	if e.useCtx {
		select {
		case <-ctx.Done():
			return e.result
		case <-e.release:
			return e.result
		}
	}
	<-e.release
	return e.result
}

func testWorkerConfig() workerConfig {
	cfg := defaultWorkerConfig()
	cfg.Server = "test:1"
	cfg.Executor = "test-executor"
	cfg.HeartbeatInterval = 5 * time.Millisecond
	cfg.LeaseTTL = time.Second
	cfg.RenewInterval = 10 * time.Millisecond
	cfg.PollMinBackoff = 2 * time.Millisecond
	cfg.PollMaxBackoff = 8 * time.Millisecond
	cfg.ExecutorTimeout = time.Second
	cfg.DrainTimeout = 100 * time.Millisecond
	return cfg
}

func TestLeaseWorkerPollExecuteRenewReport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	renewed := make(chan struct{})
	reported := make(chan map[string]any, 1)
	var pollCount int
	fake := &fakeCaller{}
	fake.handler = func(method string, params map[string]any, result any) error {
		switch method {
		case "worker.register":
			return setResult(result, registration{WorkerID: "wrk_1", WorkerCredential: "cred_1"})
		case "work.poll":
			pollCount++
			if pollCount == 1 {
				return setResult(result, map[string]any{"task": map[string]any{"task_id": "task_1", "user_prompt": "build", "lease_generation": 1}})
			}
			return setResult(result, map[string]any{"empty": true})
		case "work.renew":
			select {
			case <-renewed:
			default:
				close(renewed)
			}
			return setResult(result, map[string]any{"ok": true, "lease_valid": true})
		case "work.report":
			reported <- params
			cancel()
			return nil
		default:
			return nil
		}
	}
	executor := &blockingExecutor{
		started: make(chan json.RawMessage, 1),
		release: make(chan struct{}),
		result:  executionResult{SchemaVersion: executorResultSchema, Status: "completed", Summary: "done", Patches: []string{"patch_1"}},
	}
	w := newLeaseWorker(fake, executor, testWorkerConfig(), log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	raw := <-executor.started
	if !json.Valid(raw) || !jsonContainsTaskID(raw, "task_1") {
		t.Fatalf("executor task JSON = %s", raw)
	}
	select {
	case <-renewed:
	case <-time.After(time.Second):
		t.Fatal("lease was not renewed")
	}
	close(executor.release)
	var report map[string]any
	select {
	case report = <-reported:
	case <-time.After(time.Second):
		t.Fatal("work was not reported")
	}
	if report["status"] != "completed" || report["summary"] != "done" {
		t.Fatalf("report = %#v", report)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, call := range fake.snapshot() {
		if call.method == "worker.register" {
			continue
		}
		if call.params["worker_id"] != "wrk_1" || call.params["worker_credential"] != "cred_1" {
			t.Fatalf("%s missing worker authentication: %#v", call.method, call.params)
		}
	}
}

func TestRegisterSendsDeclaredPoolTags(t *testing.T) {
	fake := &fakeCaller{handler: func(_ string, _ map[string]any, result any) error {
		return setResult(result, registration{WorkerID: "wrk_pool", WorkerCredential: "cred_pool"})
	}}
	cfg := testWorkerConfig()
	cfg.Pools = stringList{"gpu-heavy", "eu-west"}
	w := newLeaseWorker(fake, nil, cfg, log.New(io.Discard, "", 0))
	if err := w.register(); err != nil {
		t.Fatalf("register: %v", err)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].method != "worker.register" {
		t.Fatalf("expected exactly one worker.register call, got %+v", calls)
	}
	pools, ok := calls[0].params["pools"].([]any)
	if !ok || len(pools) != 2 || pools[0] != "gpu-heavy" || pools[1] != "eu-west" {
		t.Fatalf("expected pools=[gpu-heavy eu-west] in worker.register params, got %#v", calls[0].params["pools"])
	}
}

func TestWorkerAuthorityCallsAlwaysCarryCredential(t *testing.T) {
	fake := &fakeCaller{handler: func(_ string, _ map[string]any, _ any) error { return nil }}
	w := newLeaseWorker(fake, nil, testWorkerConfig(), log.New(io.Discard, "", 0))
	w.reg = registration{WorkerID: "wrk_auth", WorkerCredential: "cred_auth"}
	for _, method := range []string{
		"worker.heartbeat",
		"worker.revoke",
		"backpressure.report",
		"work.poll",
		"work.renew",
		"work.report",
	} {
		if err := w.call(method, map[string]any{"sentinel": true}, nil); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	for _, call := range fake.snapshot() {
		if call.params["worker_id"] != "wrk_auth" || call.params["worker_credential"] != "cred_auth" {
			t.Fatalf("%s missing auth: %#v", call.method, call.params)
		}
	}
}

func TestLeaseWorkerHonorsMaxConcurrency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls int
	var reports atomic.Int64
	fake := &fakeCaller{}
	fake.handler = func(method string, _ map[string]any, result any) error {
		switch method {
		case "worker.register":
			return setResult(result, registration{WorkerID: "wrk_concurrency", WorkerCredential: "cred_concurrency"})
		case "work.poll":
			polls++
			if polls <= 3 {
				return setResult(result, map[string]any{"task": map[string]any{"task_id": fmt.Sprintf("task_%d", polls), "lease_generation": 1}})
			}
			return setResult(result, map[string]any{"empty": true})
		case "work.renew":
			return setResult(result, map[string]any{"ok": true})
		case "work.report":
			if reports.Add(1) == 3 {
				cancel()
			}
		}
		return nil
	}
	executor := &concurrencyExecutor{started: make(chan string, 3), release: make(chan struct{}, 3)}
	cfg := testWorkerConfig()
	cfg.MaxConcurrency = 2
	cfg.HeartbeatInterval = time.Hour
	cfg.RenewInterval = 100 * time.Millisecond
	w := newLeaseWorker(fake, executor, cfg, log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-executor.started
	<-executor.started
	select {
	case taskID := <-executor.started:
		t.Fatalf("third executor %s started while two slots were occupied", taskID)
	case <-time.After(20 * time.Millisecond):
	}
	executor.release <- struct{}{}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("third executor did not start after a slot was released")
	}
	executor.release <- struct{}{}
	executor.release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not finish concurrency test")
	}
	if got := executor.max.Load(); got != 2 {
		t.Fatalf("maximum executor concurrency = %d, want 2", got)
	}
}

func TestLeaseWorkerEmptyQueueUsesBoundedBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls int
	fake := &fakeCaller{}
	fake.handler = func(method string, _ map[string]any, result any) error {
		switch method {
		case "worker.register":
			return setResult(result, registration{WorkerID: "wrk_empty", WorkerCredential: "cred_empty"})
		case "work.poll":
			polls++
			if polls == 4 {
				cancel()
			}
			return setResult(result, map[string]any{"empty": true})
		default:
			return nil
		}
	}
	cfg := testWorkerConfig()
	cfg.HeartbeatInterval = time.Hour
	start := time.Now()
	w := newLeaseWorker(fake, &blockingExecutor{}, cfg, log.New(io.Discard, "", 0))
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if polls != 4 {
		t.Fatalf("poll count = %d, want 4", polls)
	}
	// Backoffs before polls 2-4 are 2ms, 4ms and 8ms.
	if elapsed := time.Since(start); elapsed < 12*time.Millisecond {
		t.Fatalf("empty queue loop spun too quickly: %s", elapsed)
	}
}

func TestLeaseWorkerServerCancellationStopsExecutorWithoutReport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pollCount int
	var reports int
	fake := &fakeCaller{}
	fake.handler = func(method string, _ map[string]any, result any) error {
		switch method {
		case "worker.register":
			return setResult(result, registration{WorkerID: "wrk_cancel", WorkerCredential: "cred_cancel"})
		case "work.poll":
			pollCount++
			if pollCount == 1 {
				return setResult(result, map[string]any{"task": map[string]any{"task_id": "task_cancel", "lease_generation": 1}})
			}
			cancel()
			return setResult(result, map[string]any{"empty": true})
		case "work.renew":
			return setResult(result, map[string]any{"cancelled": true, "lease_valid": false})
		case "work.report":
			reports++
		}
		return nil
	}
	executor := &blockingExecutor{
		started: make(chan json.RawMessage, 1), release: make(chan struct{}), useCtx: true,
		// A badly behaved adapter may return completed on cancellation. The worker
		// must still suppress that stale terminal report after lease loss.
		result: executionResult{SchemaVersion: executorResultSchema, Status: "completed", Summary: "stale"},
	}
	cfg := testWorkerConfig()
	cfg.HeartbeatInterval = time.Hour
	w := newLeaseWorker(fake, executor, cfg, log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-executor.started
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after server cancellation")
	}
	if reports != 0 {
		t.Fatalf("cancelled lease produced %d reports", reports)
	}
}

func TestLeaseWorkerReportsExecutorFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls int
	reported := make(chan map[string]any, 1)
	fake := &fakeCaller{}
	fake.handler = func(method string, _ map[string]any, result any) error {
		switch method {
		case "worker.register":
			return setResult(result, registration{WorkerID: "wrk_fail", WorkerCredential: "cred_fail"})
		case "work.poll":
			polls++
			if polls == 1 {
				return setResult(result, map[string]any{"task": map[string]any{"task_id": "task_fail", "lease_generation": 1}})
			}
			return setResult(result, map[string]any{"empty": true})
		case "work.report":
			// Capture the authenticated params from the caller record below.
			calls := fake.snapshot()
			reported <- calls[len(calls)-1].params
			cancel()
		}
		return nil
	}
	executor := &blockingExecutor{
		started: make(chan json.RawMessage, 1), release: make(chan struct{}, 1),
		result: failedResult("executor returned invalid JSON"),
	}
	executor.release <- struct{}{}
	w := newLeaseWorker(fake, executor, testWorkerConfig(), log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case report := <-reported:
		if report["status"] != "failed" || report["summary"] != "executor returned invalid JSON" {
			t.Fatalf("failure report = %#v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("executor failure was not reported")
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestLeaseWorkerSIGTERMDrainsExistingLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var pollCount int
	reported := make(chan struct{}, 1)
	fake := &fakeCaller{}
	fake.handler = func(method string, _ map[string]any, result any) error {
		switch method {
		case "worker.register":
			return setResult(result, registration{WorkerID: "wrk_drain", WorkerCredential: "cred_drain"})
		case "work.poll":
			pollCount++
			if pollCount == 1 {
				return setResult(result, map[string]any{"task": map[string]any{"task_id": "task_drain", "lease_generation": 1}})
			}
			return setResult(result, map[string]any{"empty": true})
		case "work.renew":
			return setResult(result, map[string]any{"ok": true})
		case "work.report":
			reported <- struct{}{}
		}
		return nil
	}
	executor := &blockingExecutor{
		started: make(chan json.RawMessage, 1), release: make(chan struct{}),
		result: executionResult{SchemaVersion: executorResultSchema, Status: "completed", Summary: "drained"},
	}
	cfg := testWorkerConfig()
	w := newLeaseWorker(fake, executor, cfg, log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-executor.started
	cancel()
	close(executor.release)
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("in-flight work was not reported during drain")
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestLeaseWorkerDrainDeadlineSuppressesCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var polls, reports int
	fake := &fakeCaller{}
	fake.handler = func(method string, _ map[string]any, result any) error {
		switch method {
		case "worker.register":
			return setResult(result, registration{WorkerID: "wrk_deadline", WorkerCredential: "cred_deadline"})
		case "work.poll":
			polls++
			if polls == 1 {
				return setResult(result, map[string]any{"task": map[string]any{"task_id": "task_deadline", "lease_generation": 1}})
			}
			return setResult(result, map[string]any{"empty": true})
		case "work.renew":
			return setResult(result, map[string]any{"ok": true})
		case "work.report":
			reports++
		}
		return nil
	}
	executor := &blockingExecutor{
		started: make(chan json.RawMessage, 1), release: make(chan struct{}), useCtx: true,
		result: executionResult{SchemaVersion: executorResultSchema, Status: "completed", Summary: "too late"},
	}
	cfg := testWorkerConfig()
	cfg.DrainTimeout = 15 * time.Millisecond
	w := newLeaseWorker(fake, executor, cfg, log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-executor.started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain deadline did not stop executor")
	}
	if reports != 0 {
		t.Fatalf("drain deadline produced %d terminal reports", reports)
	}
}

func TestLeaseWorkerRenewFailureCancelsAfterTwoAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var renews, reports, polls int
	fake := &fakeCaller{}
	fake.handler = func(method string, _ map[string]any, result any) error {
		switch method {
		case "worker.register":
			return setResult(result, registration{WorkerID: "wrk_lost", WorkerCredential: "cred_lost"})
		case "work.poll":
			polls++
			if polls == 1 {
				return setResult(result, map[string]any{"task": map[string]any{"task_id": "task_lost", "lease_generation": 1}})
			}
			cancel()
			return setResult(result, map[string]any{"empty": true})
		case "work.renew":
			renews++
			return errors.New("temporary transport failure")
		case "work.report":
			reports++
		}
		return nil
	}
	executor := &blockingExecutor{
		started: make(chan json.RawMessage, 1), release: make(chan struct{}), useCtx: true,
		result: executionResult{SchemaVersion: executorResultSchema, Status: "completed"},
	}
	w := newLeaseWorker(fake, executor, testWorkerConfig(), log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-executor.started
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if renews != 2 || reports != 0 {
		t.Fatalf("renews=%d reports=%d, want 2/0", renews, reports)
	}
}

func jsonContainsTaskID(raw json.RawMessage, want string) bool {
	var task struct {
		TaskID string `json:"task_id"`
	}
	return json.Unmarshal(raw, &task) == nil && task.TaskID == want
}
