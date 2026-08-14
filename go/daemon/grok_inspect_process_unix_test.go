//go:build unix

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGrokInspectCancellationKillsProcessTreeAndClosesInheritedPipes(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "inspect-child.pid")
	triggerPath := filepath.Join(root, "inspect-child.trigger")
	markerPath := filepath.Join(root, "inspect-child.marker")
	childScript := `while [ ! -e ` + shellQuote(triggerPath) + ` ]; do sleep 0.05; done; sleep 0.5; printf continued > ` + shellQuote(markerPath)
	body := `#!/bin/sh
if [ "$3" = "inspect" ]; then
  sh -c ` + shellQuote(childScript) + ` &
  printf '%s\n' "$!" > ` + shellQuote(pidPath) + `
  sleep 30
fi
exit 2
`
	bin := filepath.Join(root, "grok")
	if err := os.WriteFile(bin, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	workdir := t.TempDir()
	r := &grokCLIReasoner{bin: bin, version: "1.0.3"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.verifyPureInferenceSurface(ctx, os.Environ(), workdir, filepath.Join(root, "config.toml"))
	}()

	rawPID := waitForGrokInspectTestPID(t, pidPath)
	if err := os.WriteFile(triggerPath, []byte("cancel"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want inspect cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("inspect process-tree cancellation took %s", elapsed)
	}

	childPID, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil || childPID <= 0 || childPID == os.Getpid() {
		t.Fatalf("invalid inspect child pid %q: %v", rawPID, err)
	}
	deadline := time.Now().Add(time.Second)
	for processExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(childPID) {
		t.Fatalf("inspect descendant pid %d survived cancellation", childPID)
	}

	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect descendant performed a delayed side effect: %v", err)
	}
}

func waitForGrokInspectTestPID(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			return raw
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Grok Build inspect descendant did not publish its pid")
	return nil
}
