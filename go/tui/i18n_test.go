package tui

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestUICatalogParityPlaceholdersAndSafety(t *testing.T) {
	seen := make(map[MessageID]bool, len(catalogRowsData))
	for _, row := range catalogRowsData {
		if row.ID == "" {
			t.Fatal("catalog contains empty message ID")
		}
		if seen[row.ID] {
			t.Fatalf("duplicate message ID %q", row.ID)
		}
		seen[row.ID] = true
	}
	if len(uiCatalog[LocaleEnglish]) != len(catalogRowsData) {
		t.Fatalf("English catalog has %d entries, want %d", len(uiCatalog[LocaleEnglish]), len(catalogRowsData))
	}
	for _, locale := range supportedLocales {
		catalog := uiCatalog[locale]
		if len(catalog) != len(uiCatalog[LocaleEnglish]) {
			t.Fatalf("locale %s has %d entries, want %d", locale, len(catalog), len(uiCatalog[LocaleEnglish]))
		}
		for id, english := range uiCatalog[LocaleEnglish] {
			template, ok := catalog[id]
			if !ok || strings.TrimSpace(template.Other) == "" {
				t.Fatalf("locale %s message %s has no other form", locale, id)
			}
			assertCatalogTemplateSafe(t, locale, id, "other", template.Other)
			if !reflect.DeepEqual(templatePlaceholders(template.Other), templatePlaceholders(english.Other)) {
				t.Fatalf("locale %s message %s placeholder drift: got %v want %v", locale, id,
					templatePlaceholders(template.Other), templatePlaceholders(english.Other))
			}
			if template.One != "" {
				assertCatalogTemplateSafe(t, locale, id, "one", template.One)
				if !reflect.DeepEqual(templatePlaceholders(template.One), templatePlaceholders(template.Other)) {
					t.Fatalf("locale %s message %s one/other placeholder drift: one=%v other=%v", locale, id,
						templatePlaceholders(template.One), templatePlaceholders(template.Other))
				}
			}
		}
	}
}

func assertCatalogTemplateSafe(t *testing.T, locale Locale, id MessageID, form, value string) {
	t.Helper()
	if strings.Contains(value, "\x1b") || ansi.Strip(value) != value {
		t.Fatalf("locale %s message %s %s form contains ANSI", locale, id, form)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			t.Fatalf("locale %s message %s %s form contains control %U", locale, id, form, r)
		}
	}
}

func TestUILocaleNormalizationAndEnglishFallback(t *testing.T) {
	cases := map[string]Locale{
		"zh_CN.UTF-8":         LocaleChinese,
		"ja-JP-u-ca-japanese": LocaleJapanese,
		"ko_KR":               LocaleKorean,
		"es-419":              LocaleSpanish,
		"fr_FR@euro":          LocaleFrench,
		"de-DE":               LocaleEnglish,
		"":                    LocaleEnglish,
	}
	for raw, want := range cases {
		if got := normalizeUILocale(raw); got != want {
			t.Errorf("normalizeUILocale(%q) = %q, want %q", raw, got, want)
		}
	}
	m, err := NewChecked(Options{Theme: theme.New(theme.Mono), Locale: "es-419"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.locale != "es" {
		t.Fatalf("NewChecked locale = %q, want es", m.locale)
	}
	for _, locale := range supportedLocales {
		invalid := newLocalizer(string(locale)).Text(MessageID("missing.message"), nil)
		if invalid != unavailableMessage[locale] || strings.Contains(invalid, "missing.message") {
			t.Fatalf("unsafe fallback for %s: %q", locale, invalid)
		}
	}
}

func TestUICatalogPluralRulesAndArgumentIsolation(t *testing.T) {
	if pluralFormForInteger(LocaleEnglish, 1) != "one" || pluralFormForInteger(LocaleEnglish, 0) != "other" {
		t.Fatal("English cardinal rule drift")
	}
	if pluralFormForInteger(LocaleSpanish, 1) != "one" || pluralFormForInteger(LocaleSpanish, 2) != "other" {
		t.Fatal("Spanish cardinal rule drift")
	}
	if pluralFormForInteger(LocaleFrench, 0) != "one" || pluralFormForInteger(LocaleFrench, 1) != "one" || pluralFormForInteger(LocaleFrench, 2) != "other" {
		t.Fatal("French cardinal rule drift")
	}
	for _, locale := range []Locale{LocaleChinese, LocaleJapanese, LocaleKorean} {
		for _, count := range []int{0, 1, 2, 10} {
			if pluralFormForInteger(locale, count) != "other" {
				t.Fatalf("%s integer %d must use other", locale, count)
			}
		}
	}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	args := MessageArgs{"queue": "tab", "edit": "alt+up"}
	got := m.countText(MsgQueueHeader, 1, args)
	if _, mutated := args["count"]; mutated {
		t.Fatal("countText mutated caller arguments")
	}
	if !strings.Contains(got, "queued follow-up: 1") {
		t.Fatalf("one form = %q", got)
	}
	got = newLocalizer("en").Text(MsgQuestionAnswered, MessageArgs{
		"glyph": "+", "id": "\x1b[31mquestion\x1b[0m", "label": "ok",
	})
	if strings.Contains(got, "\x1b") || strings.Contains(got, "[31m") {
		t.Fatalf("catalog argument was not ANSI-sanitized: %q", got)
	}
}

func TestKeymapLocalizedCopyParityAndSafety(t *testing.T) {
	for _, binding := range defaultKeyBindings() {
		copy, ok := keyActionCopy[binding.Action]
		if !ok {
			t.Fatalf("missing localized key description for %s", binding.Action)
		}
		for _, locale := range supportedLocales {
			value := copy.get(locale)
			if strings.TrimSpace(value) == "" {
				t.Fatalf("empty localized key description for %s in %s", binding.Action, locale)
			}
			assertCatalogTemplateSafe(t, locale, MessageID(binding.Action), "key-description", value)
		}
	}
	for context, copy := range keyContextCopy {
		for _, locale := range supportedLocales {
			if strings.TrimSpace(copy.get(locale)) == "" {
				t.Fatalf("empty key context %s in %s", context, locale)
			}
		}
	}
}

func TestSixLocaleViewsStayInsideTerminal(t *testing.T) {
	sizes := []struct{ width, height int }{{80, 24}, {32, 10}, {8, 3}, {2, 1}}
	for _, locale := range supportedLocales {
		for _, size := range sizes {
			name := string(locale) + "/" + string(rune(size.width+'A')) + "x" + string(rune(size.height+'A'))
			t.Run(name, func(t *testing.T) {
				m := New(Options{Theme: theme.New(theme.Mono), Locale: string(locale)})
				defer m.Close()
				m.width, m.height = size.width, size.height
				m.sessionID = "session-123"
				m.conn = ConnConnected
				m.layout()
				assertViewBounds(t, m.View().Content, size.width, size.height)

				m.approval = &approvalState{DecisionID: "decision-1", Action: "command.exec", Resource: "/tmp/example", Reason: "workspace policy"}
				assertViewBounds(t, m.View().Content, size.width, size.height)
				m.approval = nil
				m.question = &questionState{QuestionID: "q-1", Prompt: "Choose a deployment target", Options: []questionOption{{Label: "staging", Value: "staging"}}}
				assertViewBounds(t, m.View().Content, size.width, size.height)
				m.question = nil
				m.checkpointPicker = &checkpointPickerState{restored: &checkpointRestoreResult{CheckpointID: "cp-1", TaskID: "task-1", Turn: 2}}
				assertViewBounds(t, m.View().Content, size.width, size.height)
				m.checkpointPicker = nil
				m.keymapEditor = &keymapEditorState{bindings: m.keys.BindingDescriptors()}
				assertViewBounds(t, m.View().Content, size.width, size.height)
			})
		}
	}
}

func assertViewBounds(t *testing.T, content string, width, height int) {
	t.Helper()
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		t.Fatalf("rendered %d lines into height %d:\n%s", len(lines), height, ansi.Strip(content))
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line %d width %d exceeds %d: %q", i, got, width, ansi.Strip(line))
		}
	}
}

func TestCoreInteractiveChromeHasNoLegacyEnglishLiterals(t *testing.T) {
	files := []string{
		"model.go", "view.go", "help.go", "approval.go", "question.go",
		"history_search.go", "checkpoint_picker.go", "keymap_editor.go", "attention.go",
		"update.go", "workspace_actions.go", "followup_flow.go", "submission_journal.go",
		"taskgraph.go", "transcript.go",
	}
	legacy := []string{
		"Carina help", "Agent needs input", "Resolving decision...", "Sending answer...",
		"reverse-i-search: ", "Rewind to checkpoint", "Checkpoint restored",
		"loading checkpoints...", "Press the new key now.", "Approval required", "Task finished",
		"type an instruction -", "queued follow-ups:", "not attached", "editing draft",
		"nothing to copy: no rendered", "copied last agent response", "transcript · %d lines",
		"tasks · %d active", "queued shell command is empty", "automatic follow-up submission failed",
		"submission recovery: %s", "restored an unacknowledged submission;",
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, phrase := range legacy {
			if strings.Contains(string(data), phrase) {
				t.Errorf("%s reintroduced legacy UI literal %q; use a typed message ID", file, phrase)
			}
		}
	}
}
