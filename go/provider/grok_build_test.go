package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseGrokBuildModelsReady(t *testing.T) {
	defaultModel, models, ok := parseGrokBuildModels("You are logged in with grok.com.\n\nDefault model: grok-4.6\n\nAvailable models:\n  * grok-4.6 (default)\n  - grok-4.5\n")
	if !ok || defaultModel != "grok-4.6" || strings.Join(models, ",") != "grok-4.6,grok-4.5" {
		t.Fatalf("unexpected parse: default=%q models=%v ok=%v", defaultModel, models, ok)
	}
}

func TestParseGrokBuildModelsFailsClosed(t *testing.T) {
	tests := map[string]string{
		"missing login":           "Default model: grok-4.6\nAvailable models:\n* grok-4.6 (default)",
		"default missing":         "You are logged in with grok.com.\nDefault model: grok-4.6\nAvailable models:\n- grok-4.5",
		"unsafe model":            "You are logged in with grok.com.\nDefault model: ../../secret\nAvailable models:\n* ../../secret (default)",
		"unknown leading line":    "notice\nYou are logged in with grok.com.\nDefault model: grok-4.6\nAvailable models:\n* grok-4.6 (default)",
		"unknown trailing line":   "You are logged in with grok.com.\nDefault model: grok-4.6\nAvailable models:\n* grok-4.6 (default)\nAccount details:",
		"unknown trailing bullet": "You are logged in with grok.com.\nDefault model: grok-4.6\nAvailable models:\n* grok-4.6 (default)\nFeatures:\n- web-search",
		"duplicate section":       "You are logged in with grok.com.\nDefault model: grok-4.6\nAvailable models:\n* grok-4.6 (default)\nAvailable models:\n* grok-4.6 (default)",
		"wrong auth owner":        "You are logged in with xAI API key.\nDefault model: grok-4.6\nAvailable models:\n* grok-4.6 (default)",
		"wrong default marker":    "You are logged in with grok.com.\nDefault model: grok-4.6\nAvailable models:\n- grok-4.6",
		"duplicate default":       "You are logged in with grok.com.\nDefault model: grok-4.6\nAvailable models:\n* grok-4.6 (default)\n* grok-4.6 (default)",
		"duplicate model":         "You are logged in with grok.com.\nDefault model: grok-4.6\nAvailable models:\n* grok-4.6 (default)\n- grok-4.5\n- grok-4.5",
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := parseGrokBuildModels(output); ok {
				t.Fatalf("accepted incompatible output %q", output)
			}
		})
	}
}

func TestGrokBuildDiscovererStates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	tests := []struct {
		name, body, want string
	}{
		{"ready", `if [ "$1" = "--version" ]; then echo "grok 1.0.3 (fixture)"; else printf 'You are logged in with grok.com.\n\nDefault model: grok-4.6\n\nAvailable models:\n  * grok-4.6 (default)\n  - grok-4.5\n'; fi`, GrokBuildStateReady},
		{"signed-out", `if [ "$1" = "--version" ]; then echo "grok 1.0.3 (fixture)"; else echo 'Not logged in. Run grok login.' >&2; exit 1; fi`, GrokBuildStateSignedOut},
		{"incompatible", `if [ "$1" = "--version" ]; then echo "grok 1.0.3 (fixture)"; else echo 'new output'; fi`, GrokBuildStateIncompatibleVersion},
		{"probe-failed", `if [ "$1" = "--version" ]; then echo "grok 1.0.3 (fixture)"; else echo 'network failed' >&2; exit 1; fi`, GrokBuildStateProbeFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := writeGrokBuildFixture(t, tt.body)
			d := GrokBuildDiscoverer{LookPath: func(string) (string, error) { return bin, nil }, Timeout: 5 * time.Second}
			got := d.Discover(context.Background())
			if got.State != tt.want {
				t.Fatalf("state=%q reason=%q, want %q", got.State, got.Reason, tt.want)
			}
			if strings.Contains(got.Reason, "network failed") {
				t.Fatal("raw stderr leaked into discovery reason")
			}
		})
	}
}

func TestGrokBuildDiscovererEnforcesMinimumVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	for _, test := range []struct {
		version string
		state   string
	}{
		{version: "1.0.2", state: GrokBuildStateIncompatibleVersion},
		{version: "1.0.3", state: GrokBuildStateReady},
		{version: "1.0.4", state: GrokBuildStateReady},
		{version: "1.1.0", state: GrokBuildStateReady},
	} {
		t.Run(test.version, func(t *testing.T) {
			bin := writeGrokBuildFixture(t, `if [ "$1" = "--version" ]; then echo "grok `+test.version+` (fixture)"; else printf 'You are logged in with grok.com.\n\nDefault model: grok-4.6\n\nAvailable models:\n  * grok-4.6 (default)\n'; fi`)
			got := (GrokBuildDiscoverer{
				LookPath: func(string) (string, error) { return bin, nil },
				Timeout:  5 * time.Second,
			}).Discover(context.Background())
			if got.State != test.state {
				t.Fatalf("version %s state=%q, want %q", test.version, got.State, test.state)
			}
		})
	}
}

func TestGrokBuildDiscovererAbsentAndTimeout(t *testing.T) {
	absent := (GrokBuildDiscoverer{
		LookPath: func(string) (string, error) { return "", errors.New("missing") },
		HomeDir:  func() (string, error) { return t.TempDir(), nil }, Getenv: func(string) string { return "" },
	}).Discover(context.Background())
	if absent.State != GrokBuildStateAbsent {
		t.Fatalf("state=%q", absent.State)
	}
	if runtime.GOOS == "windows" {
		return
	}
	bin := writeGrokBuildFixture(t, `sleep 2`)
	got := (GrokBuildDiscoverer{LookPath: func(string) (string, error) { return bin, nil }, Timeout: 20 * time.Millisecond}).Discover(context.Background())
	if got.State != GrokBuildStateProbeFailed {
		t.Fatalf("timeout state=%q", got.State)
	}
}

func TestGrokBuildFindBinaryUsesOfficialWindowsUserProfilePath(t *testing.T) {
	profile := t.TempDir()
	path := filepath.Join(profile, ".grok", "bin", "grok.exe")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// Windows executable files do not use Unix execute permission bits.
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := GrokBuildDiscoverer{
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		HomeDir:  func() (string, error) { return "", errors.New("unavailable") },
		Getenv: func(key string) string {
			if key == "USERPROFILE" {
				return profile
			}
			return ""
		},
	}
	got, err := d.findBinaryForOS("windows")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(path) {
		t.Fatalf("binary path=%q, want %q", got, path)
	}
}

func TestGrokBuildProbeEnvironmentCannotPreferAPIKey(t *testing.T) {
	env := grokBuildProbeEnvironment([]string{
		"HOME=/home/tester", "PATH=/usr/bin", "GROK_AUTH_PATH=/home/tester/.grok/auth.json",
		"XAI_API_KEY=secret", "GROK_CODE_XAI_API_KEY=secret", "GROK_DEPLOYMENT_KEY=secret", // gitleaks:allow -- synthetic rejection fixture
		"GROK_EXTRA_AUTH_KEY=secret", "OTEL_EXPORTER_OTLP_HEADERS=secret",
		"HTTP_PROXY=http://127.0.0.1:7890", "HTTPS_PROXY=https://proxy.example:7890",
		"SSL_CERT_FILE=/tmp/untrusted.pem",
	})
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"secret", "XAI_API_KEY=", "GROK_CODE_XAI_API_KEY=", "GROK_DEPLOYMENT_KEY=", "proxy.example", "SSL_CERT_FILE="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("probe environment retained %q:\n%s", forbidden, joined)
		}
	}
	for _, required := range []string{
		"HOME=/home/tester", "GROK_AUTH_PATH=/home/tester/.grok/auth.json",
		"GROK_DISABLE_API_KEY_AUTH=1", "GROK_DISABLE_AUTOUPDATER=1", "HTTP_PROXY=http://127.0.0.1:7890",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("probe environment missing %q:\n%s", required, joined)
		}
	}
}

func TestGrokBuildProbeEnvironmentKeepsWindowsHomeAndRuntime(t *testing.T) {
	env := grokBuildProbeEnvironmentForOS([]string{
		`Path=C:\Windows\System32`, `UserProfile=C:\Users\tester`, `SystemRoot=C:\Windows`,
		`HomeDrive=C:`, `HomePath=\Users\tester`, `PATHEXT=.COM;.EXE`,
		`APPDATA=C:\Users\tester\AppData\Roaming`, `XAI_API_KEY=secret`,
	}, "windows")
	joined := strings.Join(env, "\n")
	for _, required := range []string{
		`Path=C:\Windows\System32`, `UserProfile=C:\Users\tester`, `SystemRoot=C:\Windows`,
		`HomeDrive=C:`, `HomePath=\Users\tester`, `PATHEXT=.COM;.EXE`,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("Windows probe environment missing %q:\n%s", required, joined)
		}
	}
	for _, forbidden := range []string{"APPDATA=", "XAI_API_KEY=", "secret"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Windows probe environment retained %q:\n%s", forbidden, joined)
		}
	}
}

func TestMergeGrokBuildProviderNeverCreatesHTTPRoute(t *testing.T) {
	discovery := GrokBuildDiscovery{State: GrokBuildStateReady, Version: "1.0.3", DefaultModel: "grok-4.6", Models: []string{"grok-4.6"}, Reason: "ready"}
	merged := MergeGrokBuildProvider(Catalog{"xai": {ID: "xai", API: "https://api.x.ai/v1"}}, discovery)
	got := merged[GrokBuildProviderID]
	if got.API != "" || got.APIProtocol != "" || got.Source == nil || got.Source.CredentialOwner != GrokBuildCredentialOwner {
		t.Fatalf("unsafe provider projection: %+v", got)
	}
	if merged["xai"].API != "https://api.x.ai/v1" {
		t.Fatal("native xAI provider was mutated")
	}
}

func TestMergeGrokBuildProviderActionsMatchDiscoveryState(t *testing.T) {
	for _, test := range []struct {
		state, action string
	}{
		{GrokBuildStateSignedOut, GrokBuildActionLogin},
		{GrokBuildStateIncompatibleVersion, GrokBuildActionUpdate},
		{GrokBuildStateProbeFailed, GrokBuildActionRetry},
		{GrokBuildStateReady, GrokBuildActionUseSession},
	} {
		t.Run(test.state, func(t *testing.T) {
			merged := MergeGrokBuildProvider(nil, GrokBuildDiscovery{State: test.state})
			got := merged[GrokBuildProviderID]
			if got.Source == nil || got.Source.Action != test.action {
				t.Fatalf("state %q source=%+v, want action %q", test.state, got.Source, test.action)
			}
		})
	}
	if _, exists := MergeGrokBuildProvider(nil, GrokBuildDiscovery{State: GrokBuildStateAbsent})[GrokBuildProviderID]; exists {
		t.Fatal("absent Grok Build must not create a synthetic provider")
	}
	if _, exists := MergeGrokBuildProvider(nil, GrokBuildDiscovery{State: GrokBuildStateProbing})[GrokBuildProviderID]; exists {
		t.Fatal("probing Grok Build must not create a synthetic provider")
	}
}

func TestGrokBuildDiscoveryCacheSingleFlightWaiterCancellation(t *testing.T) {
	var cache grokBuildDiscoveryCacheState
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	discover := func(context.Context) GrokBuildDiscovery {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return GrokBuildDiscovery{State: GrokBuildStateReady, Models: []string{"grok-4.6"}}
	}
	leaderDone := make(chan GrokBuildDiscovery, 1)
	go func() { leaderDone <- cache.detect(context.Background(), discover) }()
	<-started
	waiterCtx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan GrokBuildDiscovery, 1)
	go func() { waiterDone <- cache.detect(waiterCtx, discover) }()
	cancel()
	select {
	case got := <-waiterDone:
		if got.State != GrokBuildStateProbeFailed {
			t.Fatalf("canceled waiter state=%q", got.State)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter remained blocked behind the active probe")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe calls=%d, want one in flight", got)
	}
	close(release)
	if got := <-leaderDone; got.State != GrokBuildStateReady {
		t.Fatalf("leader state=%q", got.State)
	}
}

func TestGrokBuildDiscoveryCacheDoesNotCacheCanceledProbe(t *testing.T) {
	var cache grokBuildDiscoveryCacheState
	var calls atomic.Int32
	started := make(chan struct{})
	discover := func(ctx context.Context) GrokBuildDiscovery {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return grokBuildCanceledDiscovery()
		}
		return GrokBuildDiscovery{State: GrokBuildStateReady, Models: []string{"grok-4.6"}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan GrokBuildDiscovery, 1)
	go func() { firstDone <- cache.detect(ctx, discover) }()
	<-started
	cancel()
	if got := <-firstDone; got.State != GrokBuildStateProbeFailed {
		t.Fatalf("canceled state=%q", got.State)
	}
	if got := cache.detect(context.Background(), discover); got.State != GrokBuildStateReady {
		t.Fatalf("fresh state=%q", got.State)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("probe calls=%d, want canceled plus fresh probe", got)
	}
}

func TestGrokBuildDiscoveryCacheServesStaleWithoutWaiting(t *testing.T) {
	var cache grokBuildDiscoveryCacheState
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	discover := func(context.Context) GrokBuildDiscovery {
		n := calls.Add(1)
		if n == 1 {
			return GrokBuildDiscovery{State: GrokBuildStateReady, Models: []string{"grok-4.6"}}
		}
		if n == 2 {
			close(started)
			<-release
		}
		return GrokBuildDiscovery{State: GrokBuildStateReady, Models: []string{"grok-4.5"}}
	}
	if got := cache.detect(context.Background(), discover); got.Models[0] != "grok-4.6" {
		t.Fatalf("seed = %#v", got)
	}
	cache.Lock()
	cache.at = time.Now().Add(-time.Minute)
	cache.Unlock()
	startedWait := time.Now()
	got := cache.lookup(context.Background(), discover, false)
	if time.Since(startedWait) > 200*time.Millisecond {
		t.Fatalf("cached lookup blocked for %v", time.Since(startedWait))
	}
	if got.State != GrokBuildStateReady || got.Models[0] != "grok-4.6" {
		t.Fatalf("stale lookup = %#v", got)
	}
	<-started
	close(release)
}

func TestGrokBuildCachedLookupDoesNotWaitForColdProbe(t *testing.T) {
	var cache grokBuildDiscoveryCacheState
	started := make(chan struct{})
	release := make(chan struct{})
	discover := func(context.Context) GrokBuildDiscovery {
		close(started)
		<-release
		return GrokBuildDiscovery{State: GrokBuildStateReady, Models: []string{"grok-4.6"}}
	}
	start := time.Now()
	got := cache.lookup(context.Background(), discover, false)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("cold cached lookup blocked for %v", time.Since(start))
	}
	if got.State != GrokBuildStateProbing {
		t.Fatalf("cold cached state=%q", got.State)
	}
	<-started
	close(release)
}

func TestGrokBuildDiscoveryCacheReturnsDefensiveModelSlices(t *testing.T) {
	var cache grokBuildDiscoveryCacheState
	models := []string{"grok-4.6", "grok-4.5"}
	discover := func(context.Context) GrokBuildDiscovery {
		return GrokBuildDiscovery{State: GrokBuildStateReady, Models: models}
	}
	first := cache.detect(context.Background(), discover)
	first.Models[0] = "mutated-return"
	models[1] = "mutated-source"
	second := cache.detect(context.Background(), discover)
	if strings.Join(second.Models, ",") != "grok-4.6,grok-4.5" {
		t.Fatalf("cached models were aliased: %v", second.Models)
	}
}

func writeGrokBuildFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
