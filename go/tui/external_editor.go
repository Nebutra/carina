package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type externalEditorDraft struct {
	path     string
	original promptDraft
}

func prepareExternalEditor(draft promptDraft, getenv func(string) string) (externalEditorDraft, *exec.Cmd, error) {
	editor := strings.TrimSpace(getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(getenv("EDITOR"))
	}
	if editor == "" {
		return externalEditorDraft{}, nil, fmt.Errorf("set VISUAL or EDITOR to use the external editor")
	}

	f, err := os.CreateTemp("", "carina-draft-*.md")
	if err != nil {
		return externalEditorDraft{}, nil, fmt.Errorf("create editor draft: %w", err)
	}
	path := f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(path)
	}
	if err := f.Chmod(0o600); err != nil {
		cleanup()
		return externalEditorDraft{}, nil, fmt.Errorf("secure editor draft: %w", err)
	}
	if _, err := f.WriteString(draft.Text); err != nil {
		cleanup()
		return externalEditorDraft{}, nil, fmt.Errorf("write editor draft: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return externalEditorDraft{}, nil, fmt.Errorf("close editor draft: %w", err)
	}

	state := externalEditorDraft{path: path, original: cloneDraft(draft)}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", editor, path)
	} else {
		// The editor setting is intentionally a user-authored shell command (for
		// example "code --wait"). The draft path is passed separately as $1 so
		// it cannot alter that command even if the temp directory contains spaces.
		cmd = exec.Command("/bin/sh", "-c", editor+` "$1"`, "carina-editor", path)
	}
	return state, cmd, nil
}

func finishExternalEditor(state externalEditorDraft, processErr error) (promptDraft, error) {
	defer os.Remove(state.path)
	if processErr != nil {
		return cloneDraft(state.original), fmt.Errorf("external editor: %w", processErr)
	}
	content, err := os.ReadFile(state.path)
	if err != nil {
		return cloneDraft(state.original), fmt.Errorf("read editor draft: %w", err)
	}
	draft := cloneDraft(state.original)
	draft.Text = string(content)
	return draft, nil
}
