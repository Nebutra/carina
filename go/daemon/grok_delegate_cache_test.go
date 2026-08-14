package daemon

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

type grokDelegateCacheFixture struct {
	calls     atomic.Int32
	closes    atomic.Int32
	started   chan struct{}
	startOnce sync.Once
	release   <-chan struct{}
}

func (f *grokDelegateCacheFixture) ThinkRoutedModel(_ context.Context, model, _ string) (ReasonerResult, error) {
	f.calls.Add(1)
	if f.started != nil {
		f.startOnce.Do(func() { close(f.started) })
	}
	if f.release != nil {
		<-f.release
	}
	return ReasonerResult{Text: "ok", Usage: ModelUsage{Model: model}}, nil
}

func (f *grokDelegateCacheFixture) Close() { f.closes.Add(1) }

func readyGrokDelegateDiscovery(binaryPath, version string) provider.GrokBuildDiscovery {
	return provider.GrokBuildDiscovery{
		State: provider.GrokBuildStateReady, BinaryPath: binaryPath, Version: version,
		Models: []string{"grok-4.6"}, DefaultModel: "grok-4.6",
	}
}

func TestGrokBuildDelegateCacheTracksBinaryAndVersion(t *testing.T) {
	var current atomic.Value
	current.Store(readyGrokDelegateDiscovery("/fixture/grok-a", "1.0.3"))
	r := newRouterReasoner(modelrouter.New(), "default")
	r.grokBuildDiscovery = func(context.Context) provider.GrokBuildDiscovery {
		return current.Load().(provider.GrokBuildDiscovery)
	}
	var factoryCalls atomic.Int32
	var factoryMu sync.Mutex
	created := []*grokDelegateCacheFixture{}
	r.grokBuildFactory = func(discovery provider.GrokBuildDiscovery) (routedGrokBuildReasoner, error) {
		factoryCalls.Add(1)
		delegate := &grokDelegateCacheFixture{}
		factoryMu.Lock()
		created = append(created, delegate)
		factoryMu.Unlock()
		return delegate, nil
	}
	defer r.Close()

	complete := func() {
		t.Helper()
		result, err := r.complete(context.Background(), "grok-build/grok-4.6", modelrouter.Request{Prompt: "prompt"})
		if err != nil || result.Text != "ok" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	complete()
	complete()
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory calls=%d, want one for an unchanged discovery identity", got)
	}

	current.Store(readyGrokDelegateDiscovery("/fixture/grok-a", "1.0.4"))
	complete()
	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("factory calls=%d after version change, want 2", got)
	}
	if got := created[0].closes.Load(); got != 1 {
		t.Fatalf("old version close calls=%d, want 1", got)
	}

	current.Store(readyGrokDelegateDiscovery("/fixture/grok-b", "1.0.4"))
	complete()
	if got := factoryCalls.Load(); got != 3 {
		t.Fatalf("factory calls=%d after path change, want 3", got)
	}
	if got := created[1].closes.Load(); got != 1 {
		t.Fatalf("old path close calls=%d, want 1", got)
	}
}

func TestGrokBuildDelegateCacheInitializesOnceConcurrently(t *testing.T) {
	discovery := readyGrokDelegateDiscovery("/fixture/grok", "1.0.3")
	r := newRouterReasoner(modelrouter.New(), "default")
	r.grokBuildDiscovery = func(context.Context) provider.GrokBuildDiscovery { return discovery }
	delegate := &grokDelegateCacheFixture{}
	var factoryCalls atomic.Int32
	r.grokBuildFactory = func(got provider.GrokBuildDiscovery) (routedGrokBuildReasoner, error) {
		if key := grokBuildDiscoveryKey(got); key != grokBuildDiscoveryKey(discovery) {
			return nil, fmt.Errorf("discovery key=%+v", key)
		}
		factoryCalls.Add(1)
		return delegate, nil
	}
	defer r.Close()

	const callers = 24
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.complete(context.Background(), "grok-build/grok-4.6", modelrouter.Request{Prompt: "prompt"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory calls=%d, want 1", got)
	}
	if got := delegate.calls.Load(); got != callers {
		t.Fatalf("delegate calls=%d, want %d", got, callers)
	}
}

func TestGrokBuildDelegateReplacementWaitsForActiveCall(t *testing.T) {
	var current atomic.Value
	current.Store(readyGrokDelegateDiscovery("/fixture/grok-a", "1.0.3"))
	r := newRouterReasoner(modelrouter.New(), "default")
	r.grokBuildDiscovery = func(context.Context) provider.GrokBuildDiscovery {
		return current.Load().(provider.GrokBuildDiscovery)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	oldDelegate := &grokDelegateCacheFixture{started: started, release: release}
	newDelegate := &grokDelegateCacheFixture{}
	r.grokBuildFactory = func(discovery provider.GrokBuildDiscovery) (routedGrokBuildReasoner, error) {
		if discovery.BinaryPath == "/fixture/grok-a" {
			return oldDelegate, nil
		}
		return newDelegate, nil
	}
	defer r.Close()

	oldDone := make(chan error, 1)
	go func() {
		_, err := r.complete(context.Background(), "grok-build/grok-4.6", modelrouter.Request{Prompt: "old"})
		oldDone <- err
	}()
	<-started

	current.Store(readyGrokDelegateDiscovery("/fixture/grok-b", "1.0.4"))
	if _, err := r.complete(context.Background(), "grok-build/grok-4.6", modelrouter.Request{Prompt: "new"}); err != nil {
		t.Fatal(err)
	}
	if got := oldDelegate.closes.Load(); got != 0 {
		t.Fatalf("active old delegate closed early: %d", got)
	}
	close(release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	if got := oldDelegate.closes.Load(); got != 1 {
		t.Fatalf("retired old delegate close calls=%d, want 1", got)
	}
}
