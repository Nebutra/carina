package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ParseKeyBindingOverrides converts the config-file action map (for example
// {"composer.submit": ["ctrl+enter"]}) into typed runtime overrides.
func ParseKeyBindingOverrides(spec map[string][]string) ([]KeyBindingOverride, error) {
	actions := make([]string, 0, len(spec))
	for action := range spec {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	overrides := make([]KeyBindingOverride, 0, len(actions))
	for _, rawAction := range actions {
		context, _, ok := strings.Cut(rawAction, ".")
		if !ok || context == "" {
			return nil, fmt.Errorf("keybinding action %q must use <context>.<action>", rawAction)
		}
		overrides = append(overrides, KeyBindingOverride{
			Context: KeyContext(context),
			Action:  KeyAction(rawAction),
			Keys:    append([]string(nil), spec[rawAction]...),
		})
	}
	return overrides, nil
}

// KeyContext identifies one focused input surface. The same physical key may
// intentionally mean different things in different contexts.
type KeyContext string

const (
	KeyContextGlobal             KeyContext = "global"
	KeyContextChat               KeyContext = "chat"
	KeyContextComposer           KeyContext = "composer"
	KeyContextEditor             KeyContext = "editor"
	KeyContextSuggestion         KeyContext = "suggestion"
	KeyContextApproval           KeyContext = "approval"
	KeyContextQuestion           KeyContext = "question"
	KeyContextHistory            KeyContext = "history"
	KeyContextPager              KeyContext = "pager"
	KeyContextKeymap             KeyContext = "keymap"
	KeyContextKeymapAction       KeyContext = "keymap-action"
	KeyContextKeymapCapture      KeyContext = "keymap-capture"
	KeyContextCheckpointList     KeyContext = "checkpoint-list"
	KeyContextCheckpointPreview  KeyContext = "checkpoint-preview"
	KeyContextCheckpointRestored KeyContext = "checkpoint-restored"
)

// KeyAction is a semantic command. UI code dispatches actions and never needs
// to know whether an operator kept the default key or supplied an override.
type KeyAction string

const (
	ActionGlobalHelp       KeyAction = "global.help"
	ActionGlobalInterrupt  KeyAction = "global.interrupt"
	ActionGlobalRedraw     KeyAction = "global.redraw"
	ActionGlobalExit       KeyAction = "global.exit"
	ActionGlobalTranscript KeyAction = "global.transcript"
	ActionGlobalModeCycle  KeyAction = "global.mode-cycle"
	ActionGlobalSettings   KeyAction = "global.settings"

	ActionChatInterrupt KeyAction = "chat.interrupt"
	ActionChatRewind    KeyAction = "chat.rewind"

	ActionComposerSubmit         KeyAction = "composer.submit"
	ActionComposerSubmitNew      KeyAction = "composer.submit-new"
	ActionComposerNewline        KeyAction = "composer.newline"
	ActionComposerQueue          KeyAction = "composer.queue"
	ActionComposerRecallQueue    KeyAction = "composer.recall-queue"
	ActionComposerExternalEditor KeyAction = "composer.external-editor"
	ActionComposerUndo           KeyAction = "composer.undo"
	ActionComposerRedo           KeyAction = "composer.redo"
	// ActionComposerUndoPaste is kept as a source-compatible alias for early
	// embedders; Ctrl+Z now undoes text after pending paste items are exhausted.
	ActionComposerUndoPaste                 = ActionComposerUndo
	ActionComposerHistoryPrevious KeyAction = "composer.history-previous"
	ActionComposerHistoryNext     KeyAction = "composer.history-next"
	ActionComposerHistorySearch   KeyAction = "composer.history-search"

	ActionEditorMoveLeft           KeyAction = "editor.move-left"
	ActionEditorMoveRight          KeyAction = "editor.move-right"
	ActionEditorMoveUp             KeyAction = "editor.move-up"
	ActionEditorMoveDown           KeyAction = "editor.move-down"
	ActionEditorMoveWordLeft       KeyAction = "editor.move-word-left"
	ActionEditorMoveWordRight      KeyAction = "editor.move-word-right"
	ActionEditorMoveLineStart      KeyAction = "editor.move-line-start"
	ActionEditorMoveLineEnd        KeyAction = "editor.move-line-end"
	ActionEditorDeleteBackward     KeyAction = "editor.delete-backward"
	ActionEditorDeleteForward      KeyAction = "editor.delete-forward"
	ActionEditorDeleteWordBackward KeyAction = "editor.delete-word-backward"
	ActionEditorDeleteWordForward  KeyAction = "editor.delete-word-forward"
	ActionEditorKillLineStart      KeyAction = "editor.kill-line-start"
	ActionEditorKillLineEnd        KeyAction = "editor.kill-line-end"
	ActionEditorYank               KeyAction = "editor.yank"
	ActionEditorInsertNewline      KeyAction = "editor.insert-newline"

	ActionSuggestionPrevious KeyAction = "suggestion.previous"
	ActionSuggestionNext     KeyAction = "suggestion.next"
	ActionSuggestionAccept   KeyAction = "suggestion.accept"
	ActionSuggestionDismiss  KeyAction = "suggestion.dismiss"

	ActionApprovalOnce     KeyAction = "approval.once"
	ActionApprovalSession  KeyAction = "approval.session"
	ActionApprovalProject  KeyAction = "approval.project"
	ActionApprovalDeny     KeyAction = "approval.deny"
	ActionApprovalUp       KeyAction = "approval.up"
	ActionApprovalDown     KeyAction = "approval.down"
	ActionApprovalPageUp   KeyAction = "approval.page-up"
	ActionApprovalPageDown KeyAction = "approval.page-down"
	ActionApprovalTop      KeyAction = "approval.top"
	ActionApprovalBottom   KeyAction = "approval.bottom"

	ActionQuestionPrevious KeyAction = "question.previous"
	ActionQuestionNext     KeyAction = "question.next"
	ActionQuestionAnswer   KeyAction = "question.answer"
	ActionQuestionPageUp   KeyAction = "question.page-up"
	ActionQuestionPageDown KeyAction = "question.page-down"
	ActionQuestionTop      KeyAction = "question.top"
	ActionQuestionBottom   KeyAction = "question.bottom"
	ActionQuestionCancel   KeyAction = "question.cancel"

	ActionHistoryPrevious    KeyAction = "history.previous"
	ActionHistoryNext        KeyAction = "history.next"
	ActionHistoryExecute     KeyAction = "history.execute"
	ActionHistoryAccept      KeyAction = "history.accept"
	ActionHistoryAcceptDraft           = ActionHistoryAccept
	ActionHistoryCancel      KeyAction = "history.cancel"
	ActionHistoryDelete      KeyAction = "history.delete"
	ActionHistoryClear       KeyAction = "history.clear"
	ActionHistoryCycleScope  KeyAction = "history.cycle-scope"

	ActionPagerUp           KeyAction = "pager.up"
	ActionPagerDown         KeyAction = "pager.down"
	ActionPagerPageUp       KeyAction = "pager.page-up"
	ActionPagerPageDown     KeyAction = "pager.page-down"
	ActionPagerTop          KeyAction = "pager.top"
	ActionPagerBottom       KeyAction = "pager.bottom"
	ActionPagerClose        KeyAction = "pager.close"
	ActionPagerToggleDetail KeyAction = "pager.toggle-detail"

	ActionKeymapClose    KeyAction = "keymap.close"
	ActionKeymapUp       KeyAction = "keymap.up"
	ActionKeymapDown     KeyAction = "keymap.down"
	ActionKeymapPageUp   KeyAction = "keymap.page-up"
	ActionKeymapPageDown KeyAction = "keymap.page-down"
	ActionKeymapTop      KeyAction = "keymap.top"
	ActionKeymapBottom   KeyAction = "keymap.bottom"
	ActionKeymapEdit     KeyAction = "keymap.edit"

	ActionKeymapActionBack    KeyAction = "keymap-action.back"
	ActionKeymapActionReplace KeyAction = "keymap-action.replace"
	ActionKeymapActionAdd     KeyAction = "keymap-action.add"
	ActionKeymapActionRestore KeyAction = "keymap-action.restore"

	ActionKeymapCaptureCommit KeyAction = "keymap-capture.commit"
	ActionKeymapCaptureCancel KeyAction = "keymap-capture.cancel"

	ActionCheckpointListClose    KeyAction = "checkpoint-list.close"
	ActionCheckpointListUp       KeyAction = "checkpoint-list.up"
	ActionCheckpointListDown     KeyAction = "checkpoint-list.down"
	ActionCheckpointListPageUp   KeyAction = "checkpoint-list.page-up"
	ActionCheckpointListPageDown KeyAction = "checkpoint-list.page-down"
	ActionCheckpointListTop      KeyAction = "checkpoint-list.top"
	ActionCheckpointListBottom   KeyAction = "checkpoint-list.bottom"
	ActionCheckpointListPreview  KeyAction = "checkpoint-list.preview"

	ActionCheckpointPreviewClose   KeyAction = "checkpoint-preview.close"
	ActionCheckpointPreviewBack    KeyAction = "checkpoint-preview.back"
	ActionCheckpointPreviewArm     KeyAction = "checkpoint-preview.arm"
	ActionCheckpointPreviewConfirm KeyAction = "checkpoint-preview.confirm"
	ActionCheckpointPreviewRetry   KeyAction = "checkpoint-preview.retry"

	ActionCheckpointRestoredClose  KeyAction = "checkpoint-restored.close"
	ActionCheckpointRestoredResume KeyAction = "checkpoint-restored.resume"
)

// KeyBindingOverride replaces all keys for one known context/action pair. An
// empty Keys slice explicitly unbinds an optional action. Safety-critical
// actions that are the only escape/commit path cannot be unbound.
type KeyBindingOverride struct {
	Context KeyContext
	Action  KeyAction
	Keys    []string
}

// KeyBindingDescriptor is the stable, read-only shape used by keymap pickers
// and configuration UIs. BindingDescriptors returns deep copies, so callers
// may sort or edit Keys without mutating live dispatch state.
type KeyBindingDescriptor struct {
	Context     KeyContext
	Action      KeyAction
	Keys        []string
	Description string
}

type keyBinding struct {
	Context     KeyContext
	Action      KeyAction
	Keys        []string
	Description string
}

type runtimeKeymap struct {
	bindings map[KeyContext]map[KeyAction]keyBinding
	order    []keyBinding
}

func defaultKeyBindings() []keyBinding {
	return []keyBinding{
		{KeyContextGlobal, ActionGlobalHelp, []string{"f1"}, "show keyboard help"},
		{KeyContextGlobal, ActionGlobalInterrupt, []string{"ctrl+c"}, "cancel, clear, or exit"},
		{KeyContextGlobal, ActionGlobalRedraw, []string{"ctrl+l"}, "redraw terminal"},
		{KeyContextGlobal, ActionGlobalExit, []string{"ctrl+d"}, "exit when input is empty"},
		{KeyContextGlobal, ActionGlobalTranscript, []string{"alt+r"}, "open plain transcript"},
		// Grok cycles modes with Shift+Tab; Carina only cycles governed build↔plan.
		{KeyContextGlobal, ActionGlobalModeCycle, []string{"shift+tab", "ctrl+shift+m"}, "cycle build/plan mode"},
		{KeyContextGlobal, ActionGlobalSettings, []string{"ctrl+,"}, "open settings shell"},

		// Both chat actions intentionally share Esc. The dispatcher gates them on
		// mutually exclusive states: an active turn interrupts, an idle chat rewinds.
		{KeyContextChat, ActionChatInterrupt, []string{"esc"}, "interrupt active turn"},
		{KeyContextChat, ActionChatRewind, []string{"esc"}, "rewind idle chat"},

		{KeyContextComposer, ActionComposerSubmit, []string{"enter"}, "submit or steer"},
		{KeyContextComposer, ActionComposerSubmitNew, []string{"alt+s"}, "force a distinct submission"},
		{KeyContextComposer, ActionComposerNewline, []string{"shift+enter", "alt+enter", "ctrl+j"}, "insert newline"},
		{KeyContextComposer, ActionComposerQueue, []string{"tab"}, "queue next turn while running"},
		{KeyContextComposer, ActionComposerRecallQueue, []string{"alt+up"}, "edit latest queued turn"},
		{KeyContextComposer, ActionComposerExternalEditor, []string{"ctrl+g"}, "edit draft externally"},
		{KeyContextComposer, ActionComposerUndo, []string{"ctrl+z"}, "undo paste or last edit"},
		{KeyContextComposer, ActionComposerRedo, []string{"ctrl+shift+z"}, "redo last edit"},
		{KeyContextComposer, ActionComposerHistoryPrevious, []string{"ctrl+p", "up"}, "previous prompt"},
		{KeyContextComposer, ActionComposerHistoryNext, []string{"ctrl+n", "down"}, "next prompt"},
		{KeyContextComposer, ActionComposerHistorySearch, []string{"ctrl+r"}, "search prompt history"},

		{KeyContextEditor, ActionEditorMoveLeft, []string{"left", "ctrl+b"}, "move character left"},
		{KeyContextEditor, ActionEditorMoveRight, []string{"right", "ctrl+f"}, "move character right"},
		{KeyContextEditor, ActionEditorMoveUp, []string{"up"}, "move line up"},
		{KeyContextEditor, ActionEditorMoveDown, []string{"down"}, "move line down"},
		{KeyContextEditor, ActionEditorMoveWordLeft, []string{"alt+left", "alt+b"}, "move word left"},
		{KeyContextEditor, ActionEditorMoveWordRight, []string{"alt+right", "alt+f"}, "move word right"},
		{KeyContextEditor, ActionEditorMoveLineStart, []string{"home", "ctrl+a"}, "move to line start"},
		{KeyContextEditor, ActionEditorMoveLineEnd, []string{"end", "ctrl+e"}, "move to line end"},
		{KeyContextEditor, ActionEditorDeleteBackward, []string{"backspace", "ctrl+h"}, "delete character backward"},
		{KeyContextEditor, ActionEditorDeleteForward, []string{"delete", "ctrl+d"}, "delete character forward"},
		{KeyContextEditor, ActionEditorDeleteWordBackward, []string{"alt+backspace", "ctrl+w"}, "delete word backward"},
		{KeyContextEditor, ActionEditorDeleteWordForward, []string{"alt+delete", "alt+d"}, "delete word forward"},
		{KeyContextEditor, ActionEditorKillLineStart, []string{"ctrl+u"}, "delete to line start"},
		{KeyContextEditor, ActionEditorKillLineEnd, []string{"ctrl+k"}, "delete to line end"},
		{KeyContextEditor, ActionEditorYank, []string{"ctrl+y", "ctrl+v"}, "yank from clipboard"},
		{KeyContextEditor, ActionEditorInsertNewline, []string{"shift+enter", "alt+enter", "ctrl+j"}, "insert newline"},

		{KeyContextSuggestion, ActionSuggestionPrevious, []string{"up", "ctrl+p"}, "previous suggestion"},
		{KeyContextSuggestion, ActionSuggestionNext, []string{"down", "ctrl+n"}, "next suggestion"},
		{KeyContextSuggestion, ActionSuggestionAccept, []string{"tab", "enter"}, "complete suggestion"},
		{KeyContextSuggestion, ActionSuggestionDismiss, []string{"esc"}, "dismiss suggestions"},

		{KeyContextApproval, ActionApprovalOnce, []string{"y", "1", "enter"}, "approve once"},
		{KeyContextApproval, ActionApprovalSession, []string{"2"}, "approve for session"},
		{KeyContextApproval, ActionApprovalProject, []string{"3"}, "approve for project"},
		{KeyContextApproval, ActionApprovalDeny, []string{"n", "4", "esc"}, "deny"},
		{KeyContextApproval, ActionApprovalUp, []string{"up", "k"}, "scroll up"},
		{KeyContextApproval, ActionApprovalDown, []string{"down", "j"}, "scroll down"},
		{KeyContextApproval, ActionApprovalPageUp, []string{"pgup"}, "page up"},
		{KeyContextApproval, ActionApprovalPageDown, []string{"pgdown", " "}, "page down"},
		{KeyContextApproval, ActionApprovalTop, []string{"home"}, "jump to top"},
		{KeyContextApproval, ActionApprovalBottom, []string{"end"}, "jump to bottom"},

		{KeyContextQuestion, ActionQuestionPrevious, []string{"up", "k", "shift+tab"}, "previous answer"},
		{KeyContextQuestion, ActionQuestionNext, []string{"down", "j", "tab"}, "next answer"},
		{KeyContextQuestion, ActionQuestionAnswer, []string{"enter", " ", "1-9"}, "answer"},
		{KeyContextQuestion, ActionQuestionPageUp, []string{"pgup"}, "page up"},
		{KeyContextQuestion, ActionQuestionPageDown, []string{"pgdown"}, "page down"},
		{KeyContextQuestion, ActionQuestionTop, []string{"home"}, "jump to top"},
		{KeyContextQuestion, ActionQuestionBottom, []string{"end"}, "jump to bottom"},
		{KeyContextQuestion, ActionQuestionCancel, []string{"esc"}, "cancel question"},

		{KeyContextHistory, ActionHistoryPrevious, []string{"ctrl+r", "up"}, "older match"},
		{KeyContextHistory, ActionHistoryNext, []string{"down"}, "newer match"},
		{KeyContextHistory, ActionHistoryExecute, []string{"enter"}, "accept and execute match"},
		{KeyContextHistory, ActionHistoryAccept, []string{"tab", "esc"}, "accept match for editing"},
		{KeyContextHistory, ActionHistoryCancel, []string{"ctrl+c"}, "cancel and restore original draft"},
		{KeyContextHistory, ActionHistoryDelete, []string{"backspace", "ctrl+h"}, "delete query character"},
		{KeyContextHistory, ActionHistoryClear, []string{"ctrl+u"}, "clear search query"},
		{KeyContextHistory, ActionHistoryCycleScope, []string{"ctrl+s"}, "cycle session, workspace, and global scope"},

		{KeyContextPager, ActionPagerUp, []string{"up", "k"}, "scroll up"},
		{KeyContextPager, ActionPagerDown, []string{"down", "j"}, "scroll down"},
		{KeyContextPager, ActionPagerPageUp, []string{"pgup"}, "page up"},
		{KeyContextPager, ActionPagerPageDown, []string{"pgdown", " "}, "page down"},
		{KeyContextPager, ActionPagerTop, []string{"home", "alt+home"}, "jump to top"},
		{KeyContextPager, ActionPagerBottom, []string{"end", "alt+end"}, "jump to bottom"},
		{KeyContextPager, ActionPagerClose, []string{"esc", "q", "ctrl+c"}, "close overlay"},
		{KeyContextPager, ActionPagerToggleDetail, []string{"ctrl+o"}, "expand latest result"},

		{KeyContextKeymap, ActionKeymapClose, []string{"esc", "q", "ctrl+c"}, "close keymap editor"},
		{KeyContextKeymap, ActionKeymapUp, []string{"up", "k"}, "move up"},
		{KeyContextKeymap, ActionKeymapDown, []string{"down", "j"}, "move down"},
		{KeyContextKeymap, ActionKeymapPageUp, []string{"pgup"}, "page up"},
		{KeyContextKeymap, ActionKeymapPageDown, []string{"pgdown", " "}, "page down"},
		{KeyContextKeymap, ActionKeymapTop, []string{"home"}, "jump to top"},
		{KeyContextKeymap, ActionKeymapBottom, []string{"end"}, "jump to bottom"},
		{KeyContextKeymap, ActionKeymapEdit, []string{"enter", "right"}, "edit selected action"},

		{KeyContextKeymapAction, ActionKeymapActionBack, []string{"esc", "left"}, "return to binding list"},
		{KeyContextKeymapAction, ActionKeymapActionReplace, []string{"r", "enter"}, "replace binding"},
		{KeyContextKeymapAction, ActionKeymapActionAdd, []string{"a"}, "add alternate binding"},
		{KeyContextKeymapAction, ActionKeymapActionRestore, []string{"d", "backspace", "delete"}, "restore inherited binding"},

		{KeyContextKeymapCapture, ActionKeymapCaptureCommit, []string{"enter"}, "save pending chord"},
		{KeyContextKeymapCapture, ActionKeymapCaptureCancel, []string{"esc"}, "cancel key capture"},

		{KeyContextCheckpointList, ActionCheckpointListClose, []string{"esc", "q", "ctrl+c"}, "close checkpoint picker"},
		{KeyContextCheckpointList, ActionCheckpointListUp, []string{"up", "k"}, "move up"},
		{KeyContextCheckpointList, ActionCheckpointListDown, []string{"down", "j"}, "move down"},
		{KeyContextCheckpointList, ActionCheckpointListPageUp, []string{"pgup"}, "page up"},
		{KeyContextCheckpointList, ActionCheckpointListPageDown, []string{"pgdown", " "}, "page down"},
		{KeyContextCheckpointList, ActionCheckpointListTop, []string{"home"}, "jump to top"},
		{KeyContextCheckpointList, ActionCheckpointListBottom, []string{"end"}, "jump to bottom"},
		{KeyContextCheckpointList, ActionCheckpointListPreview, []string{"enter", "right"}, "preview checkpoint"},

		{KeyContextCheckpointPreview, ActionCheckpointPreviewClose, []string{"esc", "q", "ctrl+c"}, "close checkpoint preview"},
		{KeyContextCheckpointPreview, ActionCheckpointPreviewBack, []string{"left", "backspace"}, "return to checkpoint list"},
		{KeyContextCheckpointPreview, ActionCheckpointPreviewArm, []string{"y"}, "arm checkpoint restore"},
		{KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm, []string{"enter"}, "confirm checkpoint restore"},
		{KeyContextCheckpointPreview, ActionCheckpointPreviewRetry, []string{"r"}, "retry checkpoint restore"},

		{KeyContextCheckpointRestored, ActionCheckpointRestoredClose, []string{"esc", "q", "ctrl+c"}, "close restored checkpoint dialog"},
		{KeyContextCheckpointRestored, ActionCheckpointRestoredResume, []string{"r", "enter"}, "resume restored task"},
	}
}

func newRuntimeKeymap(overrides []KeyBindingOverride) (runtimeKeymap, error) {
	defs := defaultKeyBindings()
	index := make(map[KeyContext]map[KeyAction]int)
	for i := range defs {
		if index[defs[i].Context] == nil {
			index[defs[i].Context] = make(map[KeyAction]int)
		}
		index[defs[i].Context][defs[i].Action] = i
	}
	for _, override := range overrides {
		actions, ok := index[override.Context]
		if !ok {
			return runtimeKeymap{}, fmt.Errorf("unknown keybinding context %q", override.Context)
		}
		i, ok := actions[override.Action]
		if !ok {
			return runtimeKeymap{}, fmt.Errorf("unknown keybinding action %q in context %q", override.Action, override.Context)
		}
		keys := make([]string, 0, len(override.Keys))
		for _, raw := range override.Keys {
			key, err := normalizeKeySpec(raw)
			if err != nil {
				return runtimeKeymap{}, fmt.Errorf("keybinding %s in %s: %w", override.Action, override.Context, err)
			}
			keys = append(keys, key)
		}
		defs[i].Keys = keys
		// composer.newline predates the editor context and remains a supported
		// configuration key. Keep it and editor.insert-newline as true aliases so
		// placeholder/help text can never drift from the textarea binding.
		if override.Context == KeyContextComposer && override.Action == ActionComposerNewline {
			defs[index[KeyContextEditor][ActionEditorInsertNewline]].Keys = append([]string(nil), keys...)
		}
		if override.Context == KeyContextEditor && override.Action == ActionEditorInsertNewline {
			defs[index[KeyContextComposer][ActionComposerNewline]].Keys = append([]string(nil), keys...)
		}
	}

	byContext := make(map[KeyContext]map[KeyAction]keyBinding)
	seen := make(map[KeyContext]map[string]bindingRef)
	for i := range defs {
		def := defs[i]
		normalized := make([]string, 0, len(def.Keys))
		for _, raw := range def.Keys {
			key, err := normalizeKeySpec(raw)
			if err != nil {
				return runtimeKeymap{}, fmt.Errorf("default keybinding %s: %w", def.Action, err)
			}
			if def.Context == KeyContextKeymapCapture &&
				(def.Action == ActionKeymapCaptureCommit || def.Action == ActionKeymapCaptureCancel) &&
				len(strings.Fields(key)) > 1 {
				return runtimeKeymap{}, fmt.Errorf(
					"keybinding %q in context %q must use a single key; chord bindings are unreachable while key capture owns input",
					def.Action, def.Context,
				)
			}
			normalized = append(normalized, key)
			if seen[def.Context] == nil {
				seen[def.Context] = make(map[string]bindingRef)
			}
			for _, concrete := range concreteKeys(key) {
				identity := terminalKeyIdentity(concrete)
				current := bindingRef{Context: def.Context, Action: def.Action}
				if previous, exists := seen[def.Context][identity]; exists && previous.Action != def.Action &&
					!allowedDispatchOverlap(previous, current, identity) {
					return runtimeKeymap{}, fmt.Errorf(
						"keybinding conflict in context %q: terminal key %q is assigned to both %q and %q; rebind one action",
						def.Context, displayKey(identity), previous.Action, def.Action,
					)
				}
				seen[def.Context][identity] = current
			}
		}
		def.Keys = normalized
		defs[i] = def
		if byContext[def.Context] == nil {
			byContext[def.Context] = make(map[KeyAction]keyBinding)
		}
		byContext[def.Context][def.Action] = def
	}
	if err := validateDispatchPathConflicts(defs); err != nil {
		return runtimeKeymap{}, err
	}
	if err := validateChordBindings(defs); err != nil {
		return runtimeKeymap{}, err
	}
	required := []struct {
		context KeyContext
		action  KeyAction
	}{
		{KeyContextGlobal, ActionGlobalInterrupt},
		{KeyContextComposer, ActionComposerSubmit},
		{KeyContextApproval, ActionApprovalOnce},
		{KeyContextApproval, ActionApprovalDeny},
		{KeyContextQuestion, ActionQuestionPrevious},
		{KeyContextQuestion, ActionQuestionNext},
		{KeyContextQuestion, ActionQuestionAnswer},
		{KeyContextHistory, ActionHistoryAccept},
		{KeyContextHistory, ActionHistoryCancel},
		{KeyContextPager, ActionPagerClose},
		{KeyContextKeymap, ActionKeymapClose},
		{KeyContextKeymapAction, ActionKeymapActionBack},
		{KeyContextKeymapCapture, ActionKeymapCaptureCommit},
		{KeyContextKeymapCapture, ActionKeymapCaptureCancel},
		{KeyContextCheckpointList, ActionCheckpointListClose},
		{KeyContextCheckpointPreview, ActionCheckpointPreviewClose},
		{KeyContextCheckpointRestored, ActionCheckpointRestoredClose},
	}
	for _, item := range required {
		if len(byContext[item.context][item.action].Keys) == 0 {
			return runtimeKeymap{}, fmt.Errorf("keybinding %q in context %q cannot be unbound", item.action, item.context)
		}
	}
	return runtimeKeymap{bindings: byContext, order: defs}, nil
}

type bindingRef struct {
	Context KeyContext
	Action  KeyAction
}

// Pager is an overlay context, but a small subset of its actions also controls
// the transcript behind the composer. Keep that subset explicit so overlay-only
// bindings (close and line navigation) cannot capture composer chords.
func transcriptBindingActive(binding keyBinding) bool {
	if binding.Context != KeyContextPager {
		return false
	}
	switch binding.Action {
	case ActionPagerPageUp, ActionPagerPageDown, ActionPagerTop,
		ActionPagerBottom, ActionPagerToggleDetail:
		return true
	default:
		return false
	}
}

// Transcript shortcuts in normal composer mode must never steal text or the
// editor's unmodified Home/End keys. Those bindings remain fully available in
// the dedicated pager/help overlays.
func transcriptKeyAllowed(action KeyAction, key string) bool {
	if !transcriptBindingActive(keyBinding{Context: KeyContextPager, Action: action}) {
		return false
	}
	if unmodifiedPrintableKey(key) || key == "home" || key == "end" {
		return false
	}
	return true
}

// validateDispatchPathConflicts covers the surfaces that handle the same key
// before textarea.Update. Modal contexts are excluded because they replace the
// composer path while focused and therefore have deterministic ownership.
func validateDispatchPathConflicts(defs []keyBinding) error {
	dispatchContext := map[KeyContext]bool{
		KeyContextGlobal: true, KeyContextChat: true, KeyContextComposer: true,
		KeyContextEditor: true, KeyContextSuggestion: true,
	}
	seen := make(map[string]bindingRef)
	for _, def := range defs {
		if !dispatchContext[def.Context] && !transcriptBindingActive(def) {
			continue
		}
		current := bindingRef{Context: def.Context, Action: def.Action}
		for _, key := range def.Keys {
			for _, concrete := range concreteKeys(key) {
				identity := terminalKeyIdentity(concrete)
				if def.Context == KeyContextPager && !transcriptKeyAllowed(def.Action, identity) {
					continue
				}
				if unmodifiedPrintableKey(identity) {
					return fmt.Errorf(
						"keybinding %q for %q shadows normal composer text input; use a modifier key",
						displayKey(identity), def.Action,
					)
				}
				previous, exists := seen[identity]
				if !exists || previous == current {
					seen[identity] = current
					continue
				}
				if allowedDispatchOverlap(previous, current, identity) {
					continue
				}
				return fmt.Errorf(
					"keybinding shadowing on the chat/composer/editor dispatch path: terminal key %q is assigned to %q (%s) and %q (%s); rebind one action under tui_keybindings",
					displayKey(identity), previous.Action, previous.Context, current.Action, current.Context,
				)
			}
		}
	}
	return nil
}

func unmodifiedPrintableKey(key string) bool {
	if key == " " || key == "1-9" {
		return true
	}
	if strings.Contains(key, "+") {
		return false
	}
	runes := []rune(key)
	return len(runes) == 1 && unicode.IsPrint(runes[0])
}

func allowedDispatchOverlap(a, b bindingRef, key string) bool {
	if a.Context == b.Context && a.Context == KeyContextChat {
		return actionPair(a, b, ActionChatInterrupt, ActionChatRewind) && key == "esc"
	}
	if actionPair(a, b, ActionComposerNewline, ActionEditorInsertNewline) {
		return true
	}
	if actionPair(a, b, ActionComposerHistoryPrevious, ActionEditorMoveUp) {
		return key == "up"
	}
	if actionPair(a, b, ActionComposerHistoryNext, ActionEditorMoveDown) {
		return key == "down"
	}
	if actionPair(a, b, ActionSuggestionPrevious, ActionComposerHistoryPrevious) ||
		actionPair(a, b, ActionSuggestionPrevious, ActionEditorMoveUp) {
		return key == "up" || key == "ctrl+p"
	}
	if actionPair(a, b, ActionSuggestionNext, ActionComposerHistoryNext) ||
		actionPair(a, b, ActionSuggestionNext, ActionEditorMoveDown) {
		return key == "down" || key == "ctrl+n"
	}
	if actionPair(a, b, ActionSuggestionAccept, ActionComposerSubmit) {
		return key == "enter"
	}
	if actionPair(a, b, ActionSuggestionAccept, ActionComposerQueue) {
		return key == "tab"
	}
	if (a.Action == ActionSuggestionDismiss && b.Context == KeyContextChat) ||
		(b.Action == ActionSuggestionDismiss && a.Context == KeyContextChat) {
		return key == "esc"
	}
	if actionPair(a, b, ActionGlobalExit, ActionEditorDeleteForward) {
		return key == "ctrl+d"
	}
	return false
}

func actionPair(a, b bindingRef, first, second KeyAction) bool {
	return (a.Action == first && b.Action == second) || (a.Action == second && b.Action == first)
}

func normalizeKeySpec(raw string) (string, error) {
	if raw == " " {
		return " ", nil
	}
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(raw)))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty key")
	}
	if len(fields) > 1 {
		if len(fields) > 3 {
			return "", fmt.Errorf("key chord %q has %d steps; at most 3 are supported", raw, len(fields))
		}
		chord := make([]string, 0, len(fields))
		for _, field := range fields {
			key, err := normalizeSingleKeySpec(field)
			if err != nil {
				return "", fmt.Errorf("invalid chord %q: %w", raw, err)
			}
			if key == " " || key == "1-9" {
				return "", fmt.Errorf("invalid chord %q: spaces and key ranges cannot be chord steps", raw)
			}
			chord = append(chord, key)
		}
		return strings.Join(chord, " "), nil
	}
	return normalizeSingleKeySpec(fields[0])
}

func normalizeSingleKeySpec(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "", fmt.Errorf("empty key")
	}
	aliases := map[string]string{
		"escape": "esc", "return": "enter", "space": " ", "spacebar": " ",
		"pageup": "pgup", "page-up": "pgup", "pagedown": "pgdown", "page-down": "pgdown",
		"uparrow": "up", "downarrow": "down",
	}
	modifierAliases := map[string]string{
		"control": "ctrl", "option": "alt", "opt": "alt",
		"cmd": "super", "command": "super", "win": "super", "windows": "super",
	}
	for alias, canonical := range modifierAliases {
		key = strings.Replace(key, alias+"-", canonical+"+", 1)
		key = strings.Replace(key, alias+"+", canonical+"+", 1)
	}
	for _, modifier := range []string{"ctrl", "alt", "shift", "meta", "hyper", "super"} {
		key = strings.Replace(key, modifier+"-", modifier+"+", 1)
	}
	if strings.ContainsAny(key, "\r\n\t") || (strings.Contains(key, " ") && key != " ") {
		return "", fmt.Errorf("invalid key %q", raw)
	}
	if key == "+" {
		return key, nil
	}
	parts := strings.Split(key, "+")
	base := parts[len(parts)-1]
	if alias, ok := aliases[base]; ok {
		base = alias
	}
	if base == "" || (base == " " && len(parts) > 1) || !validKeyBase(base) {
		return "", fmt.Errorf("invalid key %q", raw)
	}
	modifierOrder := []string{"ctrl", "alt", "shift", "meta", "hyper", "super"}
	want := make(map[string]bool, len(parts)-1)
	for _, modifier := range parts[:len(parts)-1] {
		if modifier == "" {
			return "", fmt.Errorf("invalid key %q", raw)
		}
		known := false
		for _, candidate := range modifierOrder {
			if modifier == candidate {
				known = true
				break
			}
		}
		if !known || want[modifier] {
			return "", fmt.Errorf("invalid modifier %q in key %q", modifier, raw)
		}
		want[modifier] = true
	}
	ordered := make([]string, 0, len(want)+1)
	for _, modifier := range modifierOrder {
		if want[modifier] {
			ordered = append(ordered, modifier)
		}
	}
	ordered = append(ordered, base)
	return strings.Join(ordered, "+"), nil
}

func validKeyBase(base string) bool {
	if base == "1-9" {
		return true
	}
	switch base {
	case "esc", "enter", "tab", "backspace", "delete", "insert", "up", "down", "left", "right",
		"home", "end", "pgup", "pgdown":
		return true
	}
	if strings.HasPrefix(base, "f") && len(base) > 1 {
		n, err := strconv.Atoi(strings.TrimPrefix(base, "f"))
		return err == nil && n >= 1 && n <= 24
	}
	runes := []rune(base)
	return len(runes) == 1 && !unicode.IsControl(runes[0])
}

func canonicalKey(raw string) string {
	key, err := normalizeKeySpec(raw)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	return key
}

func concreteKeys(key string) []string {
	if key != "1-9" {
		return []string{key}
	}
	return []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
}

// terminalKeyIdentity folds byte-identical control sequences emitted by
// traditional terminals. Keeping the configured spelling in Keys preserves
// readable help while matching and conflict checks use the physical identity.
func terminalKeyIdentity(key string) string {
	if fields := strings.Fields(key); len(fields) > 1 {
		for i := range fields {
			fields[i] = terminalSingleKeyIdentity(fields[i])
		}
		return strings.Join(fields, " ")
	}
	return terminalSingleKeyIdentity(key)
}

func terminalSingleKeyIdentity(key string) string {
	switch key {
	case "ctrl+m":
		return "enter"
	case "ctrl+i":
		return "tab"
	case "ctrl+[":
		return "esc"
	case "ctrl+h":
		return "backspace"
	default:
		return key
	}
}

func validateChordBindings(defs []keyBinding) error {
	type chordBinding struct {
		ref   bindingRef
		parts []string
	}
	var bindings []chordBinding
	for _, def := range defs {
		for _, key := range def.Keys {
			parts := strings.Fields(terminalKeyIdentity(key))
			if len(parts) == 0 {
				continue
			}
			if len(parts) > 1 {
				first := parts[0]
				if !reliableChordPrefix(first) {
					return fmt.Errorf("keybinding chord %q for %q must start with a Ctrl/Alt/Meta/Super/Hyper-modified key", key, def.Action)
				}
			}
			bindings = append(bindings, chordBinding{
				ref: bindingRef{Context: def.Context, Action: def.Action}, parts: parts,
			})
		}
	}
	for i := range bindings {
		for j := range bindings {
			if i == j || len(bindings[i].parts) >= len(bindings[j].parts) {
				continue
			}
			if bindings[i].ref.Context != bindings[j].ref.Context &&
				bindings[i].ref.Context != KeyContextGlobal && bindings[j].ref.Context != KeyContextGlobal &&
				!(normalDispatchBinding(bindings[i].ref) && normalDispatchBinding(bindings[j].ref)) {
				continue
			}
			if chordPartsPrefix(bindings[i].parts, bindings[j].parts) {
				return fmt.Errorf(
					"keybinding %q for %q is an ambiguous prefix of chord %q for %q; remove the single/prefix binding or choose another chord prefix",
					strings.Join(bindings[i].parts, " "), bindings[i].ref.Action,
					strings.Join(bindings[j].parts, " "), bindings[j].ref.Action,
				)
			}
		}
	}
	return nil
}

func normalDispatchBinding(ref bindingRef) bool {
	switch ref.Context {
	case KeyContextGlobal, KeyContextChat, KeyContextComposer, KeyContextEditor, KeyContextSuggestion:
		return true
	case KeyContextPager:
		return transcriptBindingActive(keyBinding{Context: ref.Context, Action: ref.Action})
	default:
		return false
	}
}

func reliableChordPrefix(key string) bool {
	for _, modifier := range []string{"ctrl+", "alt+", "meta+", "super+", "hyper+"} {
		if strings.HasPrefix(key, modifier) {
			return terminalKeyIdentity(key) != "esc"
		}
	}
	return false
}

func chordPartsPrefix(prefix, full []string) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if prefix[i] != full[i] {
			return false
		}
	}
	return true
}

func (k runtimeKeymap) matches(context KeyContext, action KeyAction, key string) bool {
	binding, ok := k.bindings[context][action]
	if !ok {
		return false
	}
	key, err := normalizeKeySpec(key)
	if err != nil {
		return false
	}
	key = terminalKeyIdentity(key)
	for _, candidate := range binding.Keys {
		if terminalKeyIdentity(candidate) == key {
			return true
		}
		if candidate == "1-9" && len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			return true
		}
	}
	return false
}

// BindingDescriptors returns current bindings in stable help/dispatch order.
func (k runtimeKeymap) BindingDescriptors() []KeyBindingDescriptor {
	result := make([]KeyBindingDescriptor, 0, len(k.order))
	for _, binding := range k.order {
		result = append(result, KeyBindingDescriptor{
			Context: binding.Context, Action: binding.Action,
			Keys: append([]string(nil), binding.Keys...), Description: binding.Description,
		})
	}
	return result
}

// withOverride validates and returns a replacement keymap atomically. It is
// intended for an in-app key capture/picker: the live keymap remains unchanged
// when the candidate override is malformed, unsafe, or conflicting.
func (k runtimeKeymap) withOverride(override KeyBindingOverride) (runtimeKeymap, error) {
	overrides := make([]KeyBindingOverride, 0, len(k.order)+1)
	for _, binding := range k.order {
		overrides = append(overrides, KeyBindingOverride{
			Context: binding.Context, Action: binding.Action, Keys: append([]string(nil), binding.Keys...),
		})
	}
	overrides = append(overrides, override)
	return newRuntimeKeymap(overrides)
}

func (k runtimeKeymap) keys(context KeyContext, action KeyAction) []string {
	binding, ok := k.bindings[context][action]
	if !ok {
		return nil
	}
	return append([]string(nil), binding.Keys...)
}

func (k runtimeKeymap) label(context KeyContext, action KeyAction) string {
	keys := k.keys(context, action)
	labels := make([]string, 0, len(keys))
	for _, key := range keys {
		labels = append(labels, displayKey(key))
	}
	return strings.Join(labels, "/")
}

func displayKey(key string) string {
	if key == " " {
		return "space"
	}
	return key
}

func primaryKeyLabel(keys []string) string {
	if len(keys) == 0 {
		return "unbound"
	}
	return displayKey(keys[0])
}

func (k runtimeKeymap) helpLines(m *Model) []string {
	contextOrder := []KeyContext{
		KeyContextGlobal, KeyContextChat, KeyContextComposer, KeyContextEditor, KeyContextSuggestion, KeyContextApproval,
		KeyContextQuestion, KeyContextHistory, KeyContextPager, KeyContextKeymap, KeyContextKeymapAction,
		KeyContextKeymapCapture, KeyContextCheckpointList, KeyContextCheckpointPreview, KeyContextCheckpointRestored,
	}
	byContext := make(map[KeyContext][]keyBinding)
	for _, binding := range k.order {
		if len(binding.Keys) > 0 {
			byContext[binding.Context] = append(byContext[binding.Context], binding)
		}
	}
	var lines []string
	for _, context := range contextOrder {
		bindings := byContext[context]
		if len(bindings) == 0 {
			continue
		}
		lines = append(lines, localizedKeyContext(normalizeUILocale(m.locale), context))
		for _, binding := range bindings {
			descriptor := KeyBindingDescriptor{Context: binding.Context, Action: binding.Action, Keys: binding.Keys, Description: binding.Description}
			lines = append(lines, fmt.Sprintf("  %-22s %s", k.label(context, binding.Action), m.localizedKeyDescription(descriptor)))
		}
		lines = append(lines, "")
	}
	return lines
}

// sortedActions is test support for stable diagnostics without exposing the
// mutable runtime map.
func (k runtimeKeymap) sortedActions(context KeyContext) []KeyAction {
	var actions []KeyAction
	for action := range k.bindings[context] {
		actions = append(actions, action)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i] < actions[j] })
	return actions
}
