package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
)

// installEditorKeymap is the single adapter between Carina semantic actions
// and Bubbles' editing primitives. Call it immediately after textarea.New and
// again after an in-app keymap replacement is accepted.
func installEditorKeymap(input *textarea.Model, keys runtimeKeymap) {
	input.KeyMap.CharacterBackward = editorBinding(keys, ActionEditorMoveLeft, "move character left")
	input.KeyMap.CharacterForward = editorBinding(keys, ActionEditorMoveRight, "move character right")
	input.KeyMap.LinePrevious = editorBinding(keys, ActionEditorMoveUp, "move line up")
	input.KeyMap.LineNext = editorBinding(keys, ActionEditorMoveDown, "move line down")
	input.KeyMap.WordBackward = editorBinding(keys, ActionEditorMoveWordLeft, "move word left")
	input.KeyMap.WordForward = editorBinding(keys, ActionEditorMoveWordRight, "move word right")
	input.KeyMap.LineStart = editorBinding(keys, ActionEditorMoveLineStart, "move to line start")
	input.KeyMap.LineEnd = editorBinding(keys, ActionEditorMoveLineEnd, "move to line end")
	input.KeyMap.DeleteCharacterBackward = editorBinding(keys, ActionEditorDeleteBackward, "delete character backward")
	input.KeyMap.DeleteCharacterForward = editorBinding(keys, ActionEditorDeleteForward, "delete character forward")
	input.KeyMap.DeleteWordBackward = editorBinding(keys, ActionEditorDeleteWordBackward, "delete word backward")
	input.KeyMap.DeleteWordForward = editorBinding(keys, ActionEditorDeleteWordForward, "delete word forward")
	input.KeyMap.DeleteBeforeCursor = editorBinding(keys, ActionEditorKillLineStart, "delete to line start")
	input.KeyMap.DeleteAfterCursor = editorBinding(keys, ActionEditorKillLineEnd, "delete to line end")
	input.KeyMap.TransposeCharacterBackward = editorBinding(keys, ActionEditorTransposeBackward, "transpose characters backward")
	input.KeyMap.Paste = editorBinding(keys, ActionEditorYank, "yank from clipboard")
	input.KeyMap.InsertNewline = editorBinding(keys, ActionEditorInsertNewline, "insert newline")
}

func editorBinding(keys runtimeKeymap, action KeyAction, description string) key.Binding {
	specs := keys.keys(KeyContextEditor, action)
	if len(specs) == 0 {
		return key.NewBinding()
	}
	return key.NewBinding(
		key.WithKeys(specs...),
		key.WithHelp(primaryKeyLabel(specs), description),
	)
}
