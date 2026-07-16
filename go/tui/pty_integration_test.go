package tui_test

// PTY-harness smoke test: spawns a real daemon + Rust kernel + Zig tools
// (the spikes/tui-bubbletea/harness pattern), runs the production TUI inside
// a tmux pane (the PTY), streams real session events into it, captures the
// screen, and asserts transcript content, zh east-asian-width alignment,
// the Ctrl-C exit path, the governance exit code, and that the terminal is
// never left in raw mode. Skips gracefully wherever the environment cannot
// support it (no tmux, no kernel/zig builds, CI without a PTY).

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	runewidth "github.com/mattn/go-runewidth"

	"github.com/Nebutra/carina/go/rpc"
)

// cellWidth pins ambiguous-width handling for screen measurement. The
// renderer's width math (ansi.StringWidth) treats East Asian ambiguous glyphs
// — the rounded border set ╭│╮ among them — as one cell; go-runewidth's
// package-level functions widen them to two under an East Asian locale
// (LANG=zh_CN.*), which would make every border assertion off by two on a
// Chinese-locale host. The test must count cells the way the renderer does,
// not the way the host locale suggests.
var cellWidth = func() *runewidth.Condition {
	c := runewidth.NewCondition()
	c.EastAsianWidth = false
	return c
}()

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
	rawPath := filepath.Join(state, "pty.raw")
	if out, err := tm("pipe-pane", "-o", "-t", "main", fmt.Sprintf("cat >> %q", rawPath)); err != nil {
		t.Fatalf("capture raw PTY output: %v: %s", err, out)
	}
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
	waitForScreen := func(what string, timeout time.Duration, predicate func(string) bool) string {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if scr := capture(); predicate(scr) {
				return scr
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s; daemon log %s; screen:\n%s", what, logPath, capture())
		return ""
	}
	readRaw := func() []byte {
		data, _ := os.ReadFile(rawPath)
		return data
	}
	waitForRaw := func(what string, offset int, timeout time.Duration, sequences ...string) []byte {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			data := readRaw()
			if offset > len(data) {
				offset = len(data)
			}
			tail := data[offset:]
			found := true
			for _, sequence := range sequences {
				if !bytes.Contains(tail, []byte(sequence)) {
					found = false
					break
				}
			}
			if found {
				return data
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for raw PTY output %s after byte %d", what, offset)
		return nil
	}

	// Launch the TUI in the pane, recording its exit code for the
	// governance-exit-code assertion.
	cmdline := fmt.Sprintf("CARINA_LOCALE=en LC_ALL=C %q -socket %q -workspace %q -locale en; echo EXIT_CODE=$?", tuiBin, sock, ws)
	if _, err := tm("send-keys", "-t", "main", cmdline, "Enter"); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
	waitFor("session attach", "attached to", 30*time.Second)

	// Bubble Tea must ask the real terminal for both bracketed-paste and SGR
	// cell-motion mouse reports. OnMouse alone is insufficient: without these
	// bytes, a terminal never sends wheel events to the application.
	waitForRaw("input protocol enablement", 0, 10*time.Second,
		ansi.SetModeBracketedPaste,
		ansi.SetModeMouseButtonEvent,
		ansi.SetModeMouseExtSgr,
	)

	// SIGWINCH reaches the production model through the PTY and the rendered
	// frame follows both a constrained and expanded pane width.
	if out, err := tm("resize-window", "-t", "main", "-x", "72", "-y", "18"); err != nil {
		t.Fatalf("resize small: %v: %s", err, out)
	}
	waitForScreen("72-column resize", 10*time.Second, func(screen string) bool {
		return screenHasBorderWidth(screen, 72)
	})
	if out, err := tm("resize-window", "-t", "main", "-x", "96", "-y", "28"); err != nil {
		t.Fatalf("resize large: %v: %s", err, out)
	}
	waitForScreen("96-column resize", 10*time.Second, func(screen string) bool {
		return screenHasBorderWidth(screen, 96)
	})

	// tmux -p wraps this payload in the terminal's real bracketed-paste
	// protocol because the application enabled it above. A multi-line paste is
	// a visible draft item and must never execute its shell-looking second line.
	blockedPath := filepath.Join(state, "paste-must-not-execute")
	pasteMarker := "PTY_BRACKETED_PASTE"
	payload := pasteMarker + "\n!touch " + blockedPath
	if out, err := tm("set-buffer", "-b", "carina-pty-paste", payload); err != nil {
		t.Fatalf("set paste buffer: %v: %s", err, out)
	}
	if out, err := tm("paste-buffer", "-p", "-d", "-b", "carina-pty-paste", "-t", "main"); err != nil {
		t.Fatalf("bracketed paste: %v: %s", err, out)
	}
	waitFor("multi-line paste draft", pasteMarker, 10*time.Second)
	waitFor("multi-line paste protection", "pasted draft items", 10*time.Second)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(blockedPath); !os.IsNotExist(err) {
		t.Fatalf("multi-line paste executed hidden content: stat error %v", err)
	}
	if _, err := tm("send-keys", "-t", "main", "C-z"); err != nil {
		t.Fatal(err)
	}
	waitForScreen("paste undo", 10*time.Second, func(screen string) bool {
		return !strings.Contains(screen, pasteMarker) && !strings.Contains(screen, "pasted draft items")
	})

	// Exercise both newline paths as terminal bytes. Ctrl-J is the portable
	// fallback; CSI-u Shift+Enter is what modern terminals emit when they can
	// disambiguate a modified Enter key.
	typeLiteral := func(text string) {
		t.Helper()
		if out, err := tm("send-keys", "-l", "-t", "main", text); err != nil {
			t.Fatalf("type %q: %v: %s", text, err, out)
		}
	}
	typeLiteral("PTY_CTRLJ_FIRST")
	if _, err := tm("send-keys", "-t", "main", "C-j"); err != nil {
		t.Fatal(err)
	}
	typeLiteral("PTY_CTRLJ_SECOND")
	scr := waitFor("Ctrl-J multiline draft", "PTY_CTRLJ_SECOND", 10*time.Second)
	assertMarkersOnDistinctRows(t, scr, "PTY_CTRLJ_FIRST", "PTY_CTRLJ_SECOND")
	typeLiteral("_PTY_SHIFT_FIRST")
	typeLiteral("\x1b[13;2u")
	typeLiteral("PTY_SHIFT_SECOND")
	scr = waitFor("Shift-Enter multiline draft", "PTY_SHIFT_SECOND", 10*time.Second)
	assertMarkersOnDistinctRows(t, scr, "PTY_SHIFT_FIRST", "PTY_SHIFT_SECOND")
	if _, err := tm("send-keys", "-t", "main", "C-c"); err != nil {
		t.Fatal(err)
	}
	waitForScreen("multiline draft clear", 10*time.Second, func(screen string) bool {
		return !strings.Contains(screen, "PTY_CTRLJ_FIRST") && !strings.Contains(screen, "PTY_SHIFT_SECOND")
	})

	// Opening help removes the declared composer cursor. Feed a real SGR wheel
	// report into the pane and observe the overlay scroll, then verify closing
	// help restores a physical cursor through the renderer protocol.
	rawOffset := len(readRaw())
	if _, err := tm("send-keys", "-t", "main", "F1"); err != nil {
		t.Fatal(err)
	}
	waitFor("help overlay", "Carina help", 10*time.Second)
	waitForRaw("cursor hidden for help", rawOffset, 10*time.Second, ansi.HideCursor)
	wheelDown := ansi.MouseSgr(ansi.EncodeMouseButton(ansi.MouseWheelDown, false, false, false, false), 5, 5, false)
	for range 2 {
		typeLiteral(wheelDown)
	}
	waitForScreen("help wheel scroll", 10*time.Second, func(screen string) bool {
		return strings.Contains(screen, "Carina help") && !strings.Contains(screen, "Commands")
	})
	rawOffset = len(readRaw())
	if _, err := tm("send-keys", "-t", "main", "Escape"); err != nil {
		t.Fatal(err)
	}
	waitForScreen("help close", 10*time.Second, func(screen string) bool {
		return !strings.Contains(screen, "Carina help")
	})
	waitForRaw("composer cursor restored", rawOffset, 10*time.Second, ansi.ShowCursor)

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
	scr = waitFor("zh transcript line", "你好", 20*time.Second)

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
	waitForRaw("input protocol reset", 0, 10*time.Second,
		ansi.ResetModeBracketedPaste,
		ansi.ResetModeMouseButtonEvent,
		ansi.ResetModeMouseExtSgr,
	)

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

func screenHasBorderWidth(screen string, width int) bool {
	for _, line := range strings.Split(screen, "\n") {
		trimmed := strings.TrimRight(line, " ")
		if strings.HasPrefix(trimmed, "╭") && cellWidth.StringWidth(trimmed) == width {
			return true
		}
	}
	return false
}

func TestCapturedBorderWidth(t *testing.T) {
	const frameWidth = 24
	for _, tc := range []struct {
		name string
		row  string
		want int
		ok   bool
	}{
		{name: "literal cells", row: "│" + strings.Repeat(" ", frameWidth-2) + "│", want: frameWidth, ok: true},
		{name: "hard tabs clamp at margin", row: "│prompt\t\t\t│", want: frameWidth, ok: true},
		{name: "missing closing border", row: "│prompt", ok: false},
		{name: "content after tab is not normalized", row: "│prompt\tcontent│", want: 16, ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := capturedBorderWidth(tc.row, frameWidth)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("capturedBorderWidth(%q) = (%d, %v), want (%d, %v)", tc.row, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func assertMarkersOnDistinctRows(t *testing.T, screen, first, second string) {
	t.Helper()
	firstRow, secondRow := -1, -1
	for row, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, first) {
			firstRow = row
		}
		if strings.Contains(line, second) {
			secondRow = row
		}
	}
	if firstRow < 0 || secondRow < 0 {
		t.Fatalf("multiline markers missing: %q row=%d, %q row=%d\n%s", first, firstRow, second, secondRow, screen)
	}
	if firstRow == secondRow {
		t.Fatalf("newline input rendered both markers on row %d:\n%s", firstRow, screen)
	}
}

// assertBorderAlignment checks that every frame row (rounded-border rows and
// content rows delimited by │) renders to the same display column count.
func assertBorderAlignment(t *testing.T, screen string) {
	t.Helper()
	var rows []string
	for _, line := range strings.Split(screen, "\n") {
		trimmed := strings.TrimRight(line, " ")
		if trimmed == "" {
			continue
		}
		r := []rune(trimmed)[0]
		if r == '╭' || r == '╰' || r == '│' {
			rows = append(rows, trimmed)
		}
	}
	if len(rows) < 3 {
		t.Fatalf("no bordered rows captured:\n%s", screen)
	}
	want := cellWidth.StringWidth(rows[0])
	for i, row := range rows {
		w, ok := capturedBorderWidth(row, want)
		if !ok {
			t.Errorf("zh alignment broken: row %d has no closing border: %q", i, row)
			continue
		}
		if w != want {
			t.Errorf("zh alignment broken: row %d is %d cols, want %d: %q", i, w, want, row)
		}
	}
}

// capturedBorderWidth reproduces terminal tab stops for tmux capture-pane.
// Bubble Tea's renderer uses hard tabs to skip unchanged blank cells; tmux 3.7
// preserves those bytes instead of expanding them back to spaces. At the right
// margin, a horizontal tab clamps to the final cell, where the renderer writes
// the closing border. We only apply that clamp when tabs are immediately
// followed by the closing border, so malformed content is not normalized away.
func capturedBorderWidth(row string, frameWidth int) (int, bool) {
	runes := []rune(row)
	if len(runes) == 0 {
		return 0, false
	}
	closing := runes[len(runes)-1]
	if closing != '│' && closing != '╮' && closing != '╯' {
		return 0, false
	}
	width := 0
	for i, r := range runes {
		if r != '\t' {
			width += cellWidth.RuneWidth(r)
			continue
		}
		next := width + 8 - width%8
		if onlyTabsBeforeClosingBorder(runes[i+1:]) && next >= frameWidth {
			next = frameWidth - 1
		}
		width = next
	}
	return width, true
}

func onlyTabsBeforeClosingBorder(rest []rune) bool {
	if len(rest) == 0 {
		return false
	}
	for _, r := range rest[:len(rest)-1] {
		if r != '\t' {
			return false
		}
	}
	return true
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
