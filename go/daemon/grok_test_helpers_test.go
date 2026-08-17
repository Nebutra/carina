package daemon

import (
	"path/filepath"
	"testing"

	"github.com/Nebutra/carina/go/provider"
)

func isolateLocalGrokBuild(t *testing.T) {
	t.Helper()
	emptyHome := t.TempDir()
	t.Setenv("PATH", emptyHome)
	t.Setenv("HOME", emptyHome)
	t.Setenv("GROK_HOME", emptyHome)
	t.Setenv("CARINA_GROK_BUILD_CACHE", filepath.Join(emptyHome, "grok-build-cache.json"))
	provider.InvalidateGrokBuildDiscovery()
	t.Cleanup(provider.InvalidateGrokBuildDiscovery)
}
