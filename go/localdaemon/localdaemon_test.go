package localdaemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/product"
	"github.com/Nebutra/carina/go/rpc"
)

func TestEnsureReachableAlreadyUp(t *testing.T) {
	origDial, origSpawn := Dial, Spawn
	t.Cleanup(func() { Dial, Spawn = origDial, origSpawn })

	spawns := 0
	Spawn = func(string) error { spawns++; return nil }
	Dial = func(string) (*rpc.Client, error) {
		// Non-nil without a real conn is enough for the success branch;
		// EnsureReachable returns it as-is. Use a zero value carefully —
		// callers would Close; we only check err/spawn count.
		return &rpc.Client{}, nil
	}

	c, err := EnsureReachable("/tmp/example.sock")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
	if spawns != 0 {
		t.Fatalf("spawn calls = %d, want 0", spawns)
	}
}

func TestRequireRuntimeMethodsRejectsLegacyRuntimeBeforeUILaunch(t *testing.T) {
	if err := requireRuntimeMethods(nil, "execution.start"); err == nil {
		t.Fatal("runtime without method inventory accepted")
	} else if !strings.Contains(err.Error(), "runtime is incompatible") {
		t.Fatalf("unexpected compatibility error: %v", err)
	}
	if err := requireRuntimeMethods([]string{"session.list", "execution.start"}, "execution.start"); err != nil {
		t.Fatalf("compatible runtime rejected: %v", err)
	}
}

func TestRequiredRuntimeMethodsIncludeConversationImport(t *testing.T) {
	missing := missingRuntimeMethods([]string{"execution.start"}, requiredRuntimeMethods...)
	want := []string{"conversation.import.discover", "conversation.import.apply"}
	if !slices.Equal(missing, want) {
		t.Fatalf("missing methods = %v, want %v", missing, want)
	}
}

func TestRuntimeHandshakeRejectsMissingConversationImportMethods(t *testing.T) {
	spec := runtimeSpecFixture(t)
	description := matchingDescription(spec)
	clientConn, serverConn := net.Pipe()
	client := rpc.NewClient(clientConn, clientConn, clientConn)
	t.Cleanup(func() { _ = client.Close() })

	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		for _, expectedMethod := range []string{"runtime.describe", "runtime.initialize"} {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				serverDone <- err
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(line, &request); err != nil {
				serverDone <- err
				return
			}
			if request.Method != expectedMethod {
				serverDone <- errors.New("unexpected runtime handshake method: " + request.Method)
				return
			}
			result := any(description)
			if request.Method == "runtime.initialize" {
				result = map[string]any{
					"runtime": description,
					"capabilities": map[string]any{
						"rpc_methods": []string{"execution.start"},
					},
				}
			}
			response, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result":  result,
			})
			if err == nil {
				_, err = serverConn.Write(append(response, '\n'))
			}
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	_, err := runtimeHandshake(client, spec)
	var compatibility *RuntimeCompatibilityError
	if !errors.As(err, &compatibility) {
		t.Fatalf("runtimeHandshake error = %v, want RuntimeCompatibilityError", err)
	}
	want := []string{"conversation.import.discover", "conversation.import.apply"}
	if !slices.Equal(compatibility.MissingMethods, want) {
		t.Fatalf("missing methods = %v, want %v", compatibility.MissingMethods, want)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeHandshakeReloadsVerifiedRuntimeConfiguration(t *testing.T) {
	spec := runtimeSpecFixture(t)
	stale := matchingDescription(spec)
	stale.ConfigFingerprint = "cfg1_stale"
	refreshed := stale
	refreshed.ConfigFingerprint = spec.Config.Fingerprint
	clientConn, serverConn := net.Pipe()
	client := rpc.NewClient(clientConn, clientConn, clientConn)
	t.Cleanup(func() { _ = client.Close() })

	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		methods := []string{"runtime.describe", "runtime.initialize", "daemon.reload", "runtime.describe"}
		for index, expectedMethod := range methods {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				serverDone <- err
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(line, &request); err != nil {
				serverDone <- err
				return
			}
			if request.Method != expectedMethod {
				serverDone <- errors.New("unexpected runtime handshake method: " + request.Method)
				return
			}
			result := any(stale)
			switch request.Method {
			case "runtime.initialize":
				result = map[string]any{
					"runtime":      stale,
					"capabilities": map[string]any{"rpc_methods": requiredRuntimeMethods},
				}
			case "daemon.reload":
				result = map[string]any{"reloaded": true}
			case "runtime.describe":
				if index == len(methods)-1 {
					result = refreshed
				}
			}
			response, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result":  result,
			})
			if err == nil {
				_, err = serverConn.Write(append(response, '\n'))
			}
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	description, err := runtimeHandshake(client, spec)
	if err != nil {
		t.Fatal(err)
	}
	if description.ConfigFingerprint != spec.Config.Fingerprint || description.Epoch != stale.Epoch {
		t.Fatalf("refreshed description = %+v", description)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeIdentitySeparatesConfigurationFreshness(t *testing.T) {
	spec := runtimeSpecFixture(t)
	description := matchingDescription(spec)
	description.ConfigFingerprint = "cfg1_stale"
	if err := validateRuntimeIdentity(spec, description); err != nil {
		t.Fatalf("configuration drift rejected as runtime identity: %v", err)
	}
	configurationErr := runtimeConfigurationMismatch(spec, description)
	if configurationErr == nil {
		t.Fatal("stale configuration accepted as fresh")
	}
	if configurationErr.Description.Epoch != description.Epoch || configurationErr.ExpectedFingerprint != spec.Config.Fingerprint {
		t.Fatalf("configuration mismatch lost verified runtime evidence: %+v", configurationErr)
	}

	description.RuntimeID = "runtime_wrong"
	var rpcErr *rpc.Error
	if err := validateRuntimeIdentity(spec, description); !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeRuntimeIdentityMismatch {
		t.Fatalf("runtime identity mismatch = %v, want rpc code %d", err, rpc.CodeRuntimeIdentityMismatch)
	}
}

func TestStaleRuntimeReloadRestoresAuthoritativeSpecBeforeReplacement(t *testing.T) {
	spec := runtimeSpecFixture(t)
	stale := matchingDescription(spec)
	stale.ConfigFingerprint = "cfg1_stale"
	clientConn, serverConn := net.Pipe()
	client := rpc.NewClient(clientConn, clientConn, clientConn)
	t.Cleanup(func() { _ = client.Close() })

	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		for _, expectedMethod := range []string{"daemon.reload", "runtime.describe"} {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				serverDone <- err
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(line, &request); err != nil {
				serverDone <- err
				return
			}
			if request.Method != expectedMethod {
				serverDone <- errors.New("unexpected runtime refresh method: " + request.Method)
				return
			}
			result := any(map[string]any{"reloaded": true})
			if request.Method == "daemon.reload" {
				staleSpec := spec
				staleSpec.Config.Fingerprint = stale.ConfigFingerprint
				if err := localruntime.WriteSpec(staleSpec.Paths.SpecPath, staleSpec); err != nil {
					serverDone <- err
					return
				}
			} else {
				result = stale
			}
			response, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result":  result,
			})
			if err == nil {
				_, err = serverConn.Write(append(response, '\n'))
			}
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	_, err := refreshRuntimeConfiguration(client, spec, stale)
	var configurationErr *RuntimeConfigurationMismatchError
	if !errors.As(err, &configurationErr) {
		t.Fatalf("refresh error = %v, want RuntimeConfigurationMismatchError", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	restored, err := localruntime.LoadSpec(spec.Paths.SpecPath)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Config.Fingerprint != spec.Config.Fingerprint {
		t.Fatalf("restored fingerprint = %q, want %q", restored.Config.Fingerprint, spec.Config.Fingerprint)
	}
}

func TestEnsureReachableSpawnsOnceThenSucceeds(t *testing.T) {
	origDial, origSpawn := Dial, Spawn
	t.Cleanup(func() { Dial, Spawn = origDial, origSpawn })

	spawns := 0
	dials := 0
	Spawn = func(string) error { spawns++; return nil }
	Dial = func(string) (*rpc.Client, error) {
		dials++
		if dials == 1 {
			return nil, rpc.ErrDaemonUnreachable
		}
		return &rpc.Client{}, nil
	}

	c, err := EnsureReachable("/tmp/example.sock")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
	if spawns != 1 {
		t.Fatalf("spawn calls = %d, want 1", spawns)
	}
}

func TestEnsureReachableNonUnreachableDoesNotSpawn(t *testing.T) {
	origDial, origSpawn := Dial, Spawn
	t.Cleanup(func() { Dial, Spawn = origDial, origSpawn })

	want := errors.New("permission denied")
	spawns := 0
	Spawn = func(string) error { spawns++; return nil }
	Dial = func(string) (*rpc.Client, error) { return nil, want }

	_, err := EnsureReachable("/tmp/example.sock")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
	if spawns != 0 {
		t.Fatalf("spawn calls = %d, want 0", spawns)
	}
}

func TestEnsureReachableSpawnFailure(t *testing.T) {
	origDial, origSpawn := Dial, Spawn
	t.Cleanup(func() { Dial, Spawn = origDial, origSpawn })

	spawnErr := errors.New("exec: not found")
	Spawn = func(string) error { return spawnErr }
	Dial = func(string) (*rpc.Client, error) { return nil, rpc.ErrDaemonUnreachable }

	_, err := EnsureReachable("/tmp/example.sock")
	if !errors.Is(err, spawnErr) {
		t.Fatalf("err = %v, want spawn error", err)
	}
}

func TestEnsureReachableDeadline(t *testing.T) {
	origDial, origSpawn, origDeadline := Dial, Spawn, ReachableDeadline
	t.Cleanup(func() {
		Dial, Spawn, ReachableDeadline = origDial, origSpawn, origDeadline
	})
	ReachableDeadline = 250 * time.Millisecond
	Spawn = func(string) error { return nil }
	Dial = func(string) (*rpc.Client, error) { return nil, rpc.ErrDaemonUnreachable }

	start := time.Now()
	_, err := EnsureReachable("/tmp/example.sock")
	if err == nil || !errors.Is(err, rpc.ErrDaemonUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("deadline took too long: %v", elapsed)
	}
}

func runtimeSpecFixture(t *testing.T) localruntime.Spec {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace, err := localruntime.ResolveWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := localruntime.EnsureSpec(home, workspace, localruntime.SpecOptions{
		Mode:   localruntime.ModeWorkspace,
		Config: localruntime.ConfigIdentity{Fingerprint: "cfg1_test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func matchingDescription(spec localruntime.Spec) RuntimeDescription {
	return RuntimeDescription{
		Mode: string(spec.Mode), WorkspaceID: spec.Workspace.ID,
		WorkspaceRoot: spec.Workspace.CanonicalRoot, RuntimeID: spec.RuntimeID,
		Epoch: "runtime_process", ProcessEpoch: 1, PID: 4242,
		SocketPath: spec.Paths.SocketPath, StateDir: spec.Paths.StateDir,
		RuntimeDir: spec.Paths.RuntimeDir, ConfigFingerprint: spec.Config.Fingerprint,
		Lifecycle: localruntime.LifecycleRunning,
	}
}

func TestConnectOrStartConcurrentCallersSpawnOnce(t *testing.T) {
	spec := runtimeSpecFixture(t)
	origDial, origSpawn, origHandshake, origDeadline := Dial, SpawnRuntime, RuntimeHandshake, ReachableDeadline
	t.Cleanup(func() {
		Dial, SpawnRuntime, RuntimeHandshake, ReachableDeadline = origDial, origSpawn, origHandshake, origDeadline
	})
	ReachableDeadline = time.Second
	var running atomic.Bool
	var spawns atomic.Int32
	Dial = func(string) (*rpc.Client, error) {
		if !running.Load() {
			return nil, rpc.ErrDaemonUnreachable
		}
		return &rpc.Client{}, nil
	}
	SpawnRuntime = func(got localruntime.Spec) error {
		spawns.Add(1)
		if err := writeOwnershipRecord(got.Paths.OwnerPath, ownershipRecord{
			Owner: OwnershipMarker, PID: 4242, Socket: got.Paths.SocketPath,
			WorkspaceID: got.Workspace.ID, RuntimeID: got.RuntimeID,
		}); err != nil {
			return err
		}
		running.Store(true)
		return nil
	}
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return matchingDescription(spec), nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, _, err := ConnectOrStart(spec)
			if client == nil && err == nil {
				err = errors.New("nil client")
			}
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
	if spawns.Load() != 1 {
		t.Fatalf("spawn calls = %d, want 1", spawns.Load())
	}
}

func TestConnectOrStartReachableMismatchFailsClosed(t *testing.T) {
	spec := runtimeSpecFixture(t)
	origDial, origSpawn, origHandshake := Dial, SpawnRuntime, RuntimeHandshake
	t.Cleanup(func() { Dial, SpawnRuntime, RuntimeHandshake = origDial, origSpawn, origHandshake })
	Dial = func(string) (*rpc.Client, error) { return &rpc.Client{}, nil }
	spawns := 0
	SpawnRuntime = func(localruntime.Spec) error { spawns++; return nil }
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return RuntimeDescription{}, &rpc.Error{Code: rpc.CodeRuntimeIdentityMismatch, Message: "wrong runtime"}
	}
	if _, _, err := ConnectOrStart(spec); err == nil {
		t.Fatal("identity mismatch accepted")
	}
	if spawns != 0 {
		t.Fatalf("mismatched reachable endpoint triggered %d spawn(s)", spawns)
	}
}

func TestConnectOrStartDoesNotReplaceIncompatibleRuntimeWithObligations(t *testing.T) {
	spec := runtimeSpecFixture(t)
	description := matchingDescription(spec)
	description.Obligations = []string{"execution:run_active"}
	origDial, origSpawn, origHandshake := Dial, SpawnRuntime, RuntimeHandshake
	t.Cleanup(func() { Dial, SpawnRuntime, RuntimeHandshake = origDial, origSpawn, origHandshake })
	Dial = func(string) (*rpc.Client, error) { return &rpc.Client{}, nil }
	spawns := 0
	SpawnRuntime = func(localruntime.Spec) error { spawns++; return nil }
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return RuntimeDescription{}, &RuntimeCompatibilityError{
			Description: description, MissingMethods: []string{"execution.start"},
		}
	}

	if _, _, err := ConnectOrStart(spec); err == nil || !strings.Contains(err.Error(), "active obligations") {
		t.Fatalf("ConnectOrStart error = %v, want retained obligation diagnosis", err)
	}
	if spawns != 0 {
		t.Fatalf("active incompatible runtime triggered %d spawn(s)", spawns)
	}
}

func TestRuntimeBinaryVersionMismatchIgnoresEmptyAndCurrent(t *testing.T) {
	spec := runtimeSpecFixture(t)
	current := matchingDescription(spec)
	current.BinaryVersion = product.Version
	if mismatch := runtimeBinaryVersionMismatch(current, product.Version); mismatch != nil {
		t.Fatalf("current version treated as stale: %v", mismatch)
	}
	if mismatch := runtimeBinaryVersionMismatch(matchingDescription(spec), ""); mismatch != nil {
		t.Fatalf("unknown version treated as stale: %v", mismatch)
	}
	stale := matchingDescription(spec)
	mismatch := runtimeBinaryVersionMismatch(stale, "0.0.1")
	if mismatch == nil || mismatch.Observed != "0.0.1" || mismatch.Expected != product.Version {
		t.Fatalf("mismatch=%+v", mismatch)
	}
}

func TestConnectOrStartDoesNotReplaceVersionMismatchWithObligations(t *testing.T) {
	spec := runtimeSpecFixture(t)
	description := matchingDescription(spec)
	description.Obligations = []string{"execution:run_active"}
	origDial, origSpawn, origHandshake := Dial, SpawnRuntime, RuntimeHandshake
	t.Cleanup(func() { Dial, SpawnRuntime, RuntimeHandshake = origDial, origSpawn, origHandshake })
	Dial = func(string) (*rpc.Client, error) { return &rpc.Client{}, nil }
	spawns := 0
	SpawnRuntime = func(localruntime.Spec) error { spawns++; return nil }
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return RuntimeDescription{}, &RuntimeVersionMismatchError{
			Description: description, Observed: "0.0.1", Expected: product.Version,
		}
	}

	if _, _, err := ConnectOrStart(spec); err == nil || !strings.Contains(err.Error(), "active obligations") || !strings.Contains(err.Error(), "0.0.1") {
		t.Fatalf("ConnectOrStart error = %v, want retained version diagnosis", err)
	}
	if spawns != 0 {
		t.Fatalf("active version-stale runtime triggered %d spawn(s)", spawns)
	}
}

func TestConnectOrStartDoesNotReplaceStaleConfigurationWithObligations(t *testing.T) {
	spec := runtimeSpecFixture(t)
	description := matchingDescription(spec)
	description.ConfigFingerprint = "cfg1_stale"
	description.Obligations = []string{"execution:run_active"}
	origDial, origSpawn, origHandshake := Dial, SpawnRuntime, RuntimeHandshake
	t.Cleanup(func() { Dial, SpawnRuntime, RuntimeHandshake = origDial, origSpawn, origHandshake })
	Dial = func(string) (*rpc.Client, error) { return &rpc.Client{}, nil }
	spawns := 0
	SpawnRuntime = func(localruntime.Spec) error { spawns++; return nil }
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return RuntimeDescription{}, &RuntimeConfigurationMismatchError{
			Description:         description,
			ExpectedFingerprint: spec.Config.Fingerprint,
			ObservedFingerprint: description.ConfigFingerprint,
		}
	}

	if _, _, err := ConnectOrStart(spec); err == nil || !strings.Contains(err.Error(), "configuration refresh deferred") {
		t.Fatalf("ConnectOrStart error = %v, want retained obligation diagnosis", err)
	}
	if spawns != 0 {
		t.Fatalf("active stale runtime triggered %d spawn(s)", spawns)
	}
}

func TestReleaseRuntimeOwnershipVerifiesCurrentProcess(t *testing.T) {
	spec := runtimeSpecFixture(t)
	record := ownershipRecord{
		Owner: OwnershipMarker, PID: 4242, Socket: spec.Paths.SocketPath,
		WorkspaceID: spec.Workspace.ID, RuntimeID: spec.RuntimeID,
		Epoch: "runtime_process", StartedAt: time.Now().UTC(),
	}
	if err := writeOwnershipRecord(spec.Paths.OwnerPath, record); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseRuntimeOwnership(spec, 4243); err == nil {
		t.Fatal("mismatched process removed runtime ownership")
	}
	if _, err := os.Stat(spec.Paths.OwnerPath); err != nil {
		t.Fatalf("mismatched release changed owner record: %v", err)
	}
	if err := ReleaseRuntimeOwnership(spec, 4242); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spec.Paths.OwnerPath); !os.IsNotExist(err) {
		t.Fatalf("owner record still exists: %v", err)
	}
}
