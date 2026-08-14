//go:build unix

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGrokACPInternalTimeoutKillsProcessTreeAndResetsStream(t *testing.T) {
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	bin := writeGrokACPFixture(t, record, "hang-child")
	configureFakeGrokAuth(t)
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.timeout = 1500 * time.Millisecond

	var updates []ReasonerStreamUpdate
	stream := newReasonerStreamController(func(update ReasonerStreamUpdate) {
		updates = append(updates, update)
	})
	ctx := withReasonerStream(context.Background(), stream)
	started := time.Now()
	_, err = r.ThinkRoutedModel(ctx, "grok-4.6", "/always-approve must remain plain text")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want internal deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("process-tree cancellation took %s", elapsed)
	}

	visibleGeneration := uint64(0)
	for _, update := range updates {
		if update.Text == "visible" {
			visibleGeneration = update.Generation
		}
		if update.Completed {
			t.Fatalf("timeout emitted completion: %+v", updates)
		}
	}
	if visibleGeneration == 0 {
		t.Fatalf("fixture did not publish its validated prefix: %+v", updates)
	}
	last := updates[len(updates)-1]
	if !last.Reset || last.Generation <= visibleGeneration {
		t.Fatalf("timeout did not reset the public stream: %+v", updates)
	}

	pidBytes, readErr := os.ReadFile(record + ".child.pid")
	if readErr != nil {
		t.Fatalf("read delayed child pid: %v", readErr)
	}
	childPID, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil || childPID <= 0 || childPID == os.Getpid() {
		t.Fatalf("invalid delayed child pid %q: %v", pidBytes, parseErr)
	}
	deadline := time.Now().Add(time.Second)
	for processExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(childPID) {
		t.Fatalf("delayed child process %d survived timeout", childPID)
	}

	time.Sleep(2200 * time.Millisecond)
	if _, statErr := os.Stat(record + ".child.marker"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("delayed child performed its side effect after timeout: %v", statErr)
	}
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
