//go:build unix

package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestGrokBuildProbeTimeoutKillsProcessTreeAndClosesInheritedPipes(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "child.pid")
	triggerPath := filepath.Join(root, "child.trigger")
	markerPath := filepath.Join(root, "child.marker")
	childScript := `while [ ! -e ` + grokBuildTestShellQuote(triggerPath) + ` ]; do sleep 0.05; done; sleep 0.5; printf continued > ` + grokBuildTestShellQuote(markerPath)
	bin := writeGrokBuildFixture(t, `sh -c `+grokBuildTestShellQuote(childScript)+` &
printf '%s\n' "$!" > `+grokBuildTestShellQuote(pidPath)+`
sleep 30`)

	ctx := newGrokBuildTestDeadlineContext()
	defer ctx.expire()
	discoverer := GrokBuildDiscoverer{Timeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		_, err := discoverer.run(ctx, bin, "--version")
		done <- err
	}()

	rawPID := waitForGrokBuildTestPID(t, pidPath)
	if err := os.WriteFile(triggerPath, []byte("cancel"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	ctx.expire()
	err := <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want probe deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("probe process-tree cancellation took %s", elapsed)
	}

	childPID, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil || childPID <= 0 || childPID == os.Getpid() {
		t.Fatalf("invalid delayed child pid %q: %v", rawPID, err)
	}
	deadline := time.Now().Add(time.Second)
	for grokBuildTestProcessExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if grokBuildTestProcessExists(childPID) {
		t.Fatalf("Grok Build probe descendant pid %d survived timeout", childPID)
	}

	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe descendant performed a delayed side effect: %v", err)
	}
}

type grokBuildTestDeadlineContext struct {
	done chan struct{}
	once sync.Once
}

func newGrokBuildTestDeadlineContext() *grokBuildTestDeadlineContext {
	return &grokBuildTestDeadlineContext{done: make(chan struct{})}
}

func (*grokBuildTestDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *grokBuildTestDeadlineContext) Done() <-chan struct{} { return c.done }

func (c *grokBuildTestDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (*grokBuildTestDeadlineContext) Value(any) any { return nil }

func (c *grokBuildTestDeadlineContext) expire() { c.once.Do(func() { close(c.done) }) }

func waitForGrokBuildTestPID(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			return raw
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Grok Build probe descendant did not publish its pid")
	return nil
}

func grokBuildTestShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func grokBuildTestProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
