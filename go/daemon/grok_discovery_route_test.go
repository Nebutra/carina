package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/Nebutra/carina/go/provider"
)

func TestGrokBuildRouteClassifiesDiscoveryStates(t *testing.T) {
	for _, test := range []struct {
		state, code, category, action string
		retryable                     bool
	}{
		{
			state: provider.GrokBuildStateAbsent, code: "provider_cli_not_installed",
			category: "unavailable", action: "install Grok Build, then refresh Providers",
		},
		{
			state: provider.GrokBuildStateSignedOut, code: "provider_authentication_failed",
			category: "authentication", action: "run `grok login`, then refresh Providers",
		},
		{
			state: provider.GrokBuildStateIncompatibleVersion, code: "provider_cli_incompatible",
			category: "compatibility", action: "update Grok Build, then refresh Providers",
		},
		{
			state: provider.GrokBuildStateProbeFailed, code: "provider_discovery_failed",
			category: "unavailable", action: "retry Providers or run `grok doctor`", retryable: true,
		},
	} {
		t.Run(test.state, func(t *testing.T) {
			r := &routerReasoner{grokBuildDiscovery: func(context.Context) provider.GrokBuildDiscovery {
				return provider.GrokBuildDiscovery{State: test.state}
			}}
			_, _, targeted, err := r.grokBuildRoute(context.Background(), "grok-build/grok-4.6")
			if !targeted || err == nil {
				t.Fatalf("targeted=%v err=%v", targeted, err)
			}
			info := classifyProviderError(err)
			if info.Code != test.code || info.Category != test.category || info.UserAction != test.action || info.Retryable != test.retryable {
				t.Fatalf("classification=%+v", info)
			}
		})
	}
}

func TestGrokBuildRoutePreservesDiscoveryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &routerReasoner{grokBuildDiscovery: func(context.Context) provider.GrokBuildDiscovery {
		return provider.GrokBuildDiscovery{State: provider.GrokBuildStateProbeFailed}
	}}
	_, _, targeted, err := r.grokBuildRoute(ctx, "grok-build/grok-4.6")
	if !targeted || !errors.Is(err, context.Canceled) {
		t.Fatalf("targeted=%v err=%v", targeted, err)
	}
}
