package main

import (
	"testing"

	"github.com/Nebutra/carina/go/rpc"
)

// TestInitGateNeverDialsForUngatedCommands pins P1.8's startup-discipline
// requirement: help/version/completion and the native passthrough commands
// must never touch the daemon socket, config, or kernel. initGate is the
// single seam every governed subcommand's startup I/O must join (so a
// future addition, like P1.6's doctor auto-run, has one place to fire its
// own startup goroutine and join at this gate instead of being bolted on ad
// hoc). This test drives initGate directly for every command in the
// documented ungated allowlist and asserts dialHook was never invoked.
func TestInitGateNeverDialsForUngatedCommands(t *testing.T) {
	for cmd := range ungatedCommands {
		t.Run(cmd, func(t *testing.T) {
			dialed := false
			orig := dialHook
			dialHook = func() (*rpcClient, error) {
				dialed = true
				return nil, nil
			}
			defer func() { dialHook = orig }()

			if _, err := initGate(cmd); err != nil {
				t.Fatalf("initGate(%q) unexpected error: %v", cmd, err)
			}
			if dialed {
				t.Fatalf("initGate(%q) dialed the daemon; ungated commands must never touch the socket", cmd)
			}
		})
	}
}

// TestInitGateDialsForGovernedCommands is the complementary half: any
// command NOT in the ungated allowlist is a governed subcommand and must
// dial exactly once through the shared gate.
func TestInitGateDialsForGovernedCommands(t *testing.T) {
	for _, cmd := range []string{"status", "sessions", "run", "watch", "audit", "approve", "deny", "resume"} {
		t.Run(cmd, func(t *testing.T) {
			if ungatedCommands[cmd] {
				t.Fatalf("test setup error: %q is in ungatedCommands", cmd)
			}
			calls := 0
			orig := dialHook
			dialHook = func() (*rpcClient, error) {
				calls++
				return &rpc.Client{}, nil
			}
			defer func() { dialHook = orig }()

			if _, err := initGate(cmd); err != nil {
				t.Fatalf("initGate(%q) unexpected error: %v", cmd, err)
			}
			if calls != 1 {
				t.Fatalf("initGate(%q) dialed %d times, want exactly 1", cmd, calls)
			}
		})
	}
}

// TestUngatedCommandsMatchesDocumentedSkipList locks the allowlist itself to
// the exact skip-list already implicit in run()'s early-return branches
// (main.go), so the explicit allowlist can never silently drift from the
// commands that actually bypass dialDaemon() in run().
func TestUngatedCommandsMatchesDocumentedSkipList(t *testing.T) {
	want := []string{
		"version", "--version", "-v",
		"help", "-h", "--help",
		"completion", "update", "daemon",
		"scan", "grep", "diff", "pty",
		"run-native", "patch-native",
		"auth", "providers",
	}
	if len(ungatedCommands) != len(want) {
		t.Fatalf("ungatedCommands has %d entries, want %d: %v", len(ungatedCommands), len(want), ungatedCommands)
	}
	for _, cmd := range want {
		if !ungatedCommands[cmd] {
			t.Fatalf("ungatedCommands missing documented skip-list entry %q", cmd)
		}
	}
}
