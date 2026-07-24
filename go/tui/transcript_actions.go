package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// lastAgentText returns only text already projected into the visible terminal
// transcript. It never reaches back into raw event payloads, which keeps copy
// behavior aligned with the TUI's existing sanitization boundary.
func (t *transcript) lastAgentText() string {
	for i := len(t.entries) - 1; i >= 0; i-- {
		entry := t.entries[i]
		if entry.presentation == nil || entry.presentation.Kind != presentationAgent {
			continue
		}
		if text := strings.TrimSpace(ansi.Strip(entry.rendered)); text != "" {
			return text
		}
	}
	return ""
}

func (t *transcript) plainText() string {
	return strings.TrimSpace(ansi.Strip(strings.Join(t.lines, "\n")))
}

func (t *transcript) entryPlainText(key string) string {
	if key == "" {
		return ""
	}
	index := t.indexOf(key)
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(ansi.Strip(t.entries[index].rendered))
}
