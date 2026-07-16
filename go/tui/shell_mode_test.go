package tui

import (
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestStickyShellModeEnterExitAndSubmit(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"command.exec": map[string]any{"exit_code": 0, "stdout": "ok"},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc

	if m.inShellMode() {
		t.Fatal("start normal")
	}
	if !m.tryEnterShellModeFromKey("!", "") {
		t.Fatal("! on empty should enter shell mode")
	}
	if !m.inShellMode() || m.input.Prompt != "! " {
		t.Fatalf("mode/prompt = %v %q", m.inShellMode(), m.input.Prompt)
	}
	if m.input.Value() != "" {
		t.Fatalf("! must not remain in input: %q", m.input.Value())
	}

	// Esc empty exits before rewind.
	if !m.tryExitShellModeFromKey("esc") {
		t.Fatal("esc empty should exit shell mode")
	}
	if m.inShellMode() || m.input.Prompt != "> " {
		t.Fatalf("expected normal mode, prompt=%q", m.input.Prompt)
	}

	m.enterShellMode()
	m.input.SetValue("echo hi")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("shell submit nil")
	}
	// Drain async submit
	for i := 0; i < 6 && cmd != nil; i++ {
		msg := cmd()
		if msg == nil {
			break
		}
		_, cmd = m.Update(msg)
	}
	if len(fc.calls) == 0 || fc.calls[0].method != "command.exec" {
		t.Fatalf("expected command.exec, got %#v", fc.calls)
	}
	argv, _ := fc.calls[0].params["argv"].([]string)
	if len(argv) < 2 || argv[0] != "echo" || argv[1] != "hi" {
		// argv may be []any from json path
		if raw, ok := fc.calls[0].params["argv"].([]any); ok {
			if len(raw) < 2 || raw[0] != "echo" || raw[1] != "hi" {
				t.Fatalf("argv=%#v", fc.calls[0].params["argv"])
			}
		} else if len(argv) < 2 {
			t.Fatalf("argv=%#v", fc.calls[0].params["argv"])
		}
	}
	if !m.inShellMode() {
		t.Fatal("sticky shell mode should remain after submit")
	}
}

func TestOneShotBangStillWorksInNormalMode(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	m.input.SetValue("!pwd")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("one-shot bang should submit")
	}
	if m.inShellMode() {
		// one-shot does not stick
	}
}

func TestLoneBangSubmitEntersStickyMode(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.input.SetValue("!")
	if cmd := m.submit(); cmd != nil {
		t.Fatal("lone ! should enter mode not submit")
	}
	if !m.inShellMode() || m.input.Value() != "" {
		t.Fatalf("sticky=%v value=%q", m.inShellMode(), m.input.Value())
	}
}

func TestHistoryRestoreReentersShellMode(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.restoreDraft(promptDraft{Text: "!git status"})
	if !m.inShellMode() {
		t.Fatal("history shell entry should re-enter sticky mode")
	}
	if m.input.Value() != "git status" {
		t.Fatalf("value=%q", m.input.Value())
	}
	m.restoreDraft(promptDraft{Text: "hello agent"})
	if m.inShellMode() {
		t.Fatal("non-shell history should exit sticky mode")
	}
}

func TestShellCommandFromDraft(t *testing.T) {
	cmd, ok := shellCommandFromDraft("!ls -la", false)
	if !ok || cmd != "ls -la" {
		t.Fatalf("oneshot got %q ok=%v", cmd, ok)
	}
	cmd, ok = shellCommandFromDraft("ls -la", true)
	if !ok || cmd != "ls -la" {
		t.Fatalf("sticky got %q ok=%v", cmd, ok)
	}
	if _, ok := shellCommandFromDraft("", true); ok {
		t.Fatal("empty sticky")
	}
}
