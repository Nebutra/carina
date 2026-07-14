package tui_test

// PTY-harness smoke test: spawns a real daemon + Rust kernel + Zig tools
// (the spikes/tui-bubbletea/harness pattern), runs the production TUI inside
// a tmux pane (the PTY), streams real session events into it, captures the
// screen, and asserts transcript content, zh east-asian-width alignment,
// the Ctrl-C exit path, the governance exit code, and that the terminal is
// never left in raw mode. Skips gracefully wherever the environment cannot
// support it (no tmux, no kernel/zig builds, CI without a PTY).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	runewidth "github.com/mattn/go-runewidth"

	"github.com/Nebutra/carina/go/rpc"
)

func TestTUIUnderPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping PTY harness test")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("PTY harness not supported on %s", runtime.GOOS)
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable: the PTY harness needs it")
	}
	root := repoRoot(t)
	kernelBin := filepath.Join(root, "target", "debug", "carina-kernel-service")
	if _, err := os.Stat(kernelBin); err != nil {
		t.Skip("rust kernel not built (cargo build -p carina-kernel --bin carina-kernel-service)")
	}
	toolsDir := filepath.Join(root, "zig", "zig-out", "bin")
	if _, err := os.Stat(filepath.Join(toolsDir, "carina-run")); err != nil {
		t.Skip("zig tools not built (zig/zig-out/bin missing)")
	}

	state := t.TempDir()
	ws := filepath.Join(state, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "readme.txt"), []byte("hello workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build the production binaries fresh.
	bins := t.TempDir()
	daemonBin := filepath.Join(bins, "carina-daemon")
	tuiBin := filepath.Join(bins, "carina-tui")
	goBuild(t, root, daemonBin, "./apps/carina-daemon")
	goBuild(t, root, tuiBin, "./apps/carina-tui")

	// Spawn the daemon against the real kernel and zig tools.
	sock := filepath.Join(state, "d.sock")
	logPath := filepath.Join(state, "daemon.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command(daemonBin,
		"-socket", sock, "-state", filepath.Join(state, "st"),
		"-kernel", kernelBin, "-tools", toolsDir, "-offline")
	daemon.Env = append(os.Environ(), "CARINA_TOOLS_DIR="+toolsDir)
	daemon.Stdout, daemon.Stderr = logFile, logFile
	if err := daemon.Start(); err != nil {
		t.Fatalf("daemon start: %v", err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_, _ = daemon.Process.Wait()
		_ = logFile.Close()
	})
	waitForSocket(t, sock, logPath)

	// tmux server on a private socket = the PTY.
	tmuxSock := fmt.Sprintf("carina-tui-test-%d", os.Getpid())
	tm := func(args ...string) (string, error) {
		out, err := exec.Command(tmux, append([]string{"-L", tmuxSock}, args...)...).CombinedOutput()
		return string(out), err
	}
	if out, err := tm("-f", "/dev/null", "new-session", "-d", "-s", "main", "-x", "110", "-y", "32", "/bin/sh"); err != nil {
		t.Skipf("cannot create tmux session (no PTY in this environment?): %v: %s", err, out)
	}
	t.Cleanup(func() { _, _ = tm("kill-server") })
	capture := func() string {
		out, _ := tm("capture-pane", "-t", "main", "-p")
		return out
	}
	waitFor := func(what, substr string, timeout time.Duration) string {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if scr := capture(); strings.Contains(scr, substr) {
				return scr
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s (%q); daemon log %s; screen:\n%s", what, substr, logPath, capture())
		return ""
	}

	// Launch the TUI in the pane, recording its exit code for the
	// governance-exit-code assertion.
	cmdline := fmt.Sprintf("%q -socket %q -workspace %q -locale zh; echo EXIT_CODE=$?", tuiBin, sock, ws)
	if _, err := tm("send-keys", "-t", "main", cmdline, "Enter"); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
	waitFor("session attach", "attached to", 30*time.Second)

	// Drive real events through the daemon from outside, like a second
	// client: command.exec publishes CommandStarted/Output/Exited envelopes
	// that must stream into the TUI transcript live.
	client, err := rpc.Dial(sock)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	defer client.Close()
	var sessions []struct {
		SessionID string `json:"session_id"`
	}
	if err := client.Call("session.list", map[string]any{}, &sessions); err != nil || len(sessions) == 0 {
		t.Fatalf("session.list: %v (%d sessions)", err, len(sessions))
	}
	sid := sessions[0].SessionID
	zhLine := "你好，审批测试 with mixed 中英 text"
	var execOut map[string]any
	if err := client.Call("command.exec", map[string]any{
		"session_id": sid, "argv": []string{"echo", zhLine},
	}, &execOut); err != nil {
		t.Fatalf("command.exec: %v", err)
	}

	// Transcript: the zh output must appear, streamed live.
	scr := waitFor("zh transcript line", "你好", 20*time.Second)

	// zh alignment: every border row must occupy the same display width —
	// east-asian-width correctness (CJK = 2 columns), the spike's G3 check.
	assertBorderAlignment(t, scr)

	// Ctrl-C cascade: two presses inside the 2s window exit the TUI.
	if _, err := tm("send-keys", "-t", "main", "C-c"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := tm("send-keys", "-t", "main", "C-c"); err != nil {
		t.Fatal(err)
	}
	scr = waitFor("clean TUI exit", "EXIT_CODE=0", 30*time.Second)

	// The TUI must never leave the terminal in raw mode: after exit, the
	// pane's shell tty still has canonical mode and echo enabled.
	sttyPath := filepath.Join(state, "stty.txt")
	const sttyComplete = "__CARINA_STTY_COMPLETE__"
	if _, err := tm("send-keys", "-t", "main", fmt.Sprintf(
		"stty -a > %q 2>&1; printf '\\n%s\\n' >> %q", sttyPath, sttyComplete, sttyPath), "Enter"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var sttyOut []byte
	for !strings.Contains(string(sttyOut), sttyComplete) && time.Now().Before(deadline) {
		sttyOut, _ = os.ReadFile(sttyPath)
		if !strings.Contains(string(sttyOut), sttyComplete) {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !strings.Contains(string(sttyOut), sttyComplete) {
		t.Fatal("stty capture did not produce output")
	}
	tokens := strings.FieldsFunc(string(sttyOut), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';'
	})
	assertToken := func(want string) {
		for _, tok := range tokens {
			if tok == want {
				return
			}
			if tok == "-"+want {
				t.Errorf("terminal left in raw mode: stty reports %s\n%s", tok, sttyOut)
				return
			}
		}
		t.Errorf("stty output missing %q:\n%s", want, sttyOut)
	}
	assertToken("icanon")
	assertToken("echo")
}

// assertBorderAlignment checks that every frame row (rounded-border rows and
// content rows delimited by │) renders to the same display column count.
func assertBorderAlignment(t *testing.T, screen string) {
	t.Helper()
	var widths []int
	var rows []string
	for _, line := range strings.Split(screen, "\n") {
		trimmed := strings.TrimRight(line, " ")
		if trimmed == "" {
			continue
		}
		r := []rune(trimmed)[0]
		if r == '╭' || r == '╰' || r == '│' {
			widths = append(widths, runewidth.StringWidth(trimmed))
			rows = append(rows, trimmed)
		}
	}
	if len(widths) < 3 {
		t.Fatalf("no bordered rows captured:\n%s", screen)
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Errorf("zh alignment broken: row %d is %d cols, want %d: %q", i, w, widths[0], rows[i])
		}
	}
}

func goBuild(t *testing.T, root, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
}

func waitForSocket(t *testing.T, sock, logPath string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log, _ := os.ReadFile(logPath)
	t.Fatalf("daemon socket never appeared; log:\n%s", log)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}
