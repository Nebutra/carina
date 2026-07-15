package microcopy

import (
	"regexp"
	"strings"
	"unicode"
)

var ansiEscape = regexp.MustCompile("\\x1b\\][^\\x07\\x1b]*(\\x07|\\x1b\\\\)|\\x1b\\[[0-?]*[ -/]*[@-~]|\\x1b[@-_]")

// renderTemplate replaces only placeholders present in the original template.
// Replacement values are never rescanned, so a value containing another
// placeholder token cannot trigger nested or map-order-dependent expansion.
func renderTemplate(text string, args Args) (string, bool) {
	valid := true
	rendered := placeholderShape.ReplaceAllStringFunc(text, func(token string) string {
		name := strings.Trim(token, "{}")
		value, ok := args[name]
		if !ok {
			valid = false
			return ""
		}
		value = sanitizeSlot(value)
		if value == "" {
			valid = false
		}
		return value
	})
	return rendered, valid
}

// sanitizeSlot keeps microcopy single-line and terminal-safe. It strips ANSI
// control sequences, converts line separators to spaces, removes remaining
// control/format characters, and collapses whitespace.
func sanitizeSlot(value string) string {
	value = ansiEscape.ReplaceAllString(value, "")
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '\n', '\r', '\t', '\v', '\f':
			b.WriteByte(' ')
		default:
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
