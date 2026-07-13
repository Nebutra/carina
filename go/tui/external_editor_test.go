package tui

import (
	"errors"
	"os"
	"testing"
)

func TestExternalEditorRoundTripPreservesPaste(t *testing.T) {
	draft := promptDraft{Text: "before", Paste: []string{"one\ntwo"}}
	state, cmd, err := prepareExternalEditor(draft, func(key string) string {
		if key == "EDITOR" {
			return "printf edited >"
		}
		return ""
	})
	if err != nil || cmd == nil {
		t.Fatalf("prepare editor: cmd=%v err=%v", cmd, err)
	}
	if err := os.WriteFile(state.path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := finishExternalEditor(state, nil)
	if err != nil || got.Text != "after\n" || len(got.Paste) != 1 || got.Paste[0] != "one\ntwo" {
		t.Fatalf("finished draft=%#v err=%v", got, err)
	}
	if _, err := os.Stat(state.path); !os.IsNotExist(err) {
		t.Fatalf("editor temp file was not removed: %v", err)
	}
}

func TestExternalEditorFailureReturnsOriginalDraft(t *testing.T) {
	original := promptDraft{Text: "keep me", Paste: []string{"paste"}}
	state, _, err := prepareExternalEditor(original, func(string) string { return "false" })
	if err != nil {
		t.Fatal(err)
	}
	got, err := finishExternalEditor(state, errors.New("exit status 1"))
	if err == nil || !draftsEqual(got, original) {
		t.Fatalf("failure draft=%#v err=%v", got, err)
	}
}

func TestExternalEditorRequiresConfiguration(t *testing.T) {
	if _, _, err := prepareExternalEditor(promptDraft{}, func(string) string { return "" }); err == nil {
		t.Fatal("missing editor must be actionable")
	}
}
