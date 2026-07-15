package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type keymapReloadSender struct{ messages chan tea.Msg }

func (s *keymapReloadSender) Send(msg tea.Msg) { s.messages <- msg }

func TestWatchKeybindingsPublishesStableValidAndInvalidSnapshots(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	path := filepath.Join(project, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &keymapReloadSender{messages: make(chan tea.Msg, 2)}
	go WatchKeybindings(ctx, home, project, sender)
	time.Sleep(2 * keymapWatchPoll)

	if err := os.WriteFile(path, []byte(`{"tui_keybindings":{"global.help":["f2"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	msg := awaitKeymapReload(t, sender.messages)
	if msg.Err != nil || len(msg.Overrides) != 1 || msg.Overrides[0].Action != ActionGlobalHelp {
		t.Fatalf("valid reload = %+v", msg)
	}

	if err := os.WriteFile(path, []byte(`{"tui_keybindings":`), 0o600); err != nil {
		t.Fatal(err)
	}
	msg = awaitKeymapReload(t, sender.messages)
	if msg.Err == nil {
		t.Fatal("invalid reload should preserve last-good keymap and report an error")
	}
}

func awaitKeymapReload(t *testing.T, messages <-chan tea.Msg) KeymapReloadMsg {
	t.Helper()
	select {
	case raw := <-messages:
		msg, ok := raw.(KeymapReloadMsg)
		if !ok {
			t.Fatalf("message type = %T", raw)
		}
		return msg
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for keymap reload")
		return KeymapReloadMsg{}
	}
}
