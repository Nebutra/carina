package tui

import (
	"fmt"
	"sort"
	"strings"
)

// KeyContext identifies one focused input surface. The same physical key may
// intentionally mean different things in different contexts.
type KeyContext string

const (
	KeyContextGlobal     KeyContext = "global"
	KeyContextComposer   KeyContext = "composer"
	KeyContextSuggestion KeyContext = "suggestion"
	KeyContextApproval   KeyContext = "approval"
	KeyContextQuestion   KeyContext = "question"
	KeyContextHistory    KeyContext = "history"
	KeyContextPager      KeyContext = "pager"
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

	ActionComposerSubmit          KeyAction = "composer.submit"
	ActionComposerNewline         KeyAction = "composer.newline"
	ActionComposerQueue           KeyAction = "composer.queue"
	ActionComposerRecallQueue     KeyAction = "composer.recall-queue"
	ActionComposerExternalEditor  KeyAction = "composer.external-editor"
	ActionComposerUndo            KeyAction = "composer.undo"
	ActionComposerRedo            KeyAction = "composer.redo"
	// ActionComposerUndoPaste is kept as a source-compatible alias for early
	// embedders; Ctrl+Z now undoes text after pending paste items are exhausted.
	ActionComposerUndoPaste = ActionComposerUndo
	ActionComposerHistoryPrevious KeyAction = "composer.history-previous"
	ActionComposerHistoryNext     KeyAction = "composer.history-next"
	ActionComposerHistorySearch   KeyAction = "composer.history-search"

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

	ActionHistoryPrevious KeyAction = "history.previous"
	ActionHistoryNext     KeyAction = "history.next"
	ActionHistoryAccept   KeyAction = "history.accept"
	ActionHistoryCancel   KeyAction = "history.cancel"

	ActionPagerUp           KeyAction = "pager.up"
	ActionPagerDown         KeyAction = "pager.down"
	ActionPagerPageUp       KeyAction = "pager.page-up"
	ActionPagerPageDown     KeyAction = "pager.page-down"
	ActionPagerTop          KeyAction = "pager.top"
	ActionPagerBottom       KeyAction = "pager.bottom"
	ActionPagerClose        KeyAction = "pager.close"
	ActionPagerToggleDetail KeyAction = "pager.toggle-detail"
)

// KeyBindingOverride replaces all keys for one known context/action pair. An
// empty Keys slice explicitly unbinds the action.
type KeyBindingOverride struct {
	Context KeyContext
	Action  KeyAction
	Keys    []string
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

		{KeyContextComposer, ActionComposerSubmit, []string{"enter"}, "submit or steer"},
		{KeyContextComposer, ActionComposerNewline, []string{"shift+enter", "alt+enter", "ctrl+j"}, "insert newline"},
		{KeyContextComposer, ActionComposerQueue, []string{"tab"}, "queue next turn while running"},
		{KeyContextComposer, ActionComposerRecallQueue, []string{"alt+up"}, "edit latest queued turn"},
		{KeyContextComposer, ActionComposerExternalEditor, []string{"ctrl+g"}, "edit draft externally"},
		{KeyContextComposer, ActionComposerUndo, []string{"ctrl+z"}, "undo paste or last edit"},
		{KeyContextComposer, ActionComposerRedo, []string{"ctrl+shift+z"}, "redo last edit"},
		{KeyContextComposer, ActionComposerHistoryPrevious, []string{"ctrl+p", "up"}, "previous prompt"},
		{KeyContextComposer, ActionComposerHistoryNext, []string{"ctrl+n", "down"}, "next prompt"},
		{KeyContextComposer, ActionComposerHistorySearch, []string{"ctrl+r"}, "search prompt history"},

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
		{KeyContextHistory, ActionHistoryNext, []string{"ctrl+s", "down"}, "newer match"},
		{KeyContextHistory, ActionHistoryAccept, []string{"enter", "tab"}, "accept match"},
		{KeyContextHistory, ActionHistoryCancel, []string{"esc", "ctrl+c"}, "cancel search"},

		{KeyContextPager, ActionPagerUp, []string{"up", "k"}, "scroll up"},
		{KeyContextPager, ActionPagerDown, []string{"down", "j"}, "scroll down"},
		{KeyContextPager, ActionPagerPageUp, []string{"pgup"}, "page up"},
		{KeyContextPager, ActionPagerPageDown, []string{"pgdown"}, "page down"},
		{KeyContextPager, ActionPagerTop, []string{"alt+home"}, "jump to top"},
		{KeyContextPager, ActionPagerBottom, []string{"alt+end"}, "jump to bottom"},
		{KeyContextPager, ActionPagerClose, []string{"esc", "q", "ctrl+c"}, "close overlay"},
		{KeyContextPager, ActionPagerToggleDetail, []string{"ctrl+o"}, "expand latest result"},
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
	}

	byContext := make(map[KeyContext]map[KeyAction]keyBinding)
	seen := make(map[KeyContext]map[string]KeyAction)
	for i := range defs {
		def := defs[i]
		normalized := make([]string, 0, len(def.Keys))
		for _, raw := range def.Keys {
			key, err := normalizeKeySpec(raw)
			if err != nil {
				return runtimeKeymap{}, fmt.Errorf("default keybinding %s: %w", def.Action, err)
			}
			normalized = append(normalized, key)
			if seen[def.Context] == nil {
				seen[def.Context] = make(map[string]KeyAction)
			}
			for _, concrete := range concreteKeys(key) {
				if previous, exists := seen[def.Context][concrete]; exists && previous != def.Action {
					return runtimeKeymap{}, fmt.Errorf(
						"keybinding conflict in context %q: key %q is assigned to both %q and %q",
						def.Context, displayKey(concrete), previous, def.Action,
					)
				}
				seen[def.Context][concrete] = def.Action
			}
		}
		def.Keys = normalized
		defs[i] = def
		if byContext[def.Context] == nil {
			byContext[def.Context] = make(map[KeyAction]keyBinding)
		}
		byContext[def.Context][def.Action] = def
	}
	return runtimeKeymap{bindings: byContext, order: defs}, nil
}

func normalizeKeySpec(raw string) (string, error) {
	if raw == " " {
		return " ", nil
	}
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "", fmt.Errorf("empty key")
	}
	aliases := map[string]string{
		"escape": "esc", "return": "enter", "space": " ", "spacebar": " ",
		"pageup": "pgup", "page-up": "pgup", "pagedown": "pgdown", "page-down": "pgdown",
		"uparrow": "up", "downarrow": "down",
	}
	if alias, ok := aliases[key]; ok {
		key = alias
	}
	for _, modifier := range []string{"ctrl", "alt", "shift"} {
		key = strings.Replace(key, modifier+"-", modifier+"+", 1)
	}
	if strings.ContainsAny(key, "\r\n\t") || (strings.Contains(key, " ") && key != " ") {
		return "", fmt.Errorf("invalid key %q", raw)
	}
	return key, nil
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

func (k runtimeKeymap) matches(context KeyContext, action KeyAction, key string) bool {
	binding, ok := k.bindings[context][action]
	if !ok {
		return false
	}
	key, err := normalizeKeySpec(key)
	if err != nil {
		return false
	}
	for _, candidate := range binding.Keys {
		if candidate == key {
			return true
		}
		if candidate == "1-9" && len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			return true
		}
	}
	return false
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

func (k runtimeKeymap) helpLines() []string {
	titles := map[KeyContext]string{
		KeyContextGlobal: "Global", KeyContextComposer: "Composer", KeyContextSuggestion: "Suggestions",
		KeyContextApproval: "Approval", KeyContextQuestion: "Questions", KeyContextHistory: "History search",
		KeyContextPager: "Pager",
	}
	contextOrder := []KeyContext{
		KeyContextGlobal, KeyContextComposer, KeyContextSuggestion, KeyContextApproval,
		KeyContextQuestion, KeyContextHistory, KeyContextPager,
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
		lines = append(lines, titles[context])
		for _, binding := range bindings {
			lines = append(lines, fmt.Sprintf("  %-22s %s", k.label(context, binding.Action), binding.Description))
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
