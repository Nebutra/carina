package daemon

import (
	"testing"

	"github.com/Nebutra/carina/go/provider"
)

func isolateLocalGrokBuild(t *testing.T) {
	t.Helper()
	emptyHome := t.TempDir()
	t.Setenv("PATH", emptyHome)
	t.Setenv("HOME", emptyHome)
	t.Setenv("GROK_HOME", emptyHome)
	provider.InvalidateGrokBuildDiscovery()
	t.Cleanup(provider.InvalidateGrokBuildDiscovery)
}
