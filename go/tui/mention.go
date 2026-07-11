package tui

import (
	"regexp"
	"strings"
	"unicode"
)

// mentionKind identifies what a trigger character should suggest.
type mentionKind int

const (
	mentionNone    mentionKind = iota
	mentionFile                // "@" — workspace paths and agent names (merged by the caller)
	mentionCommand             // "/" at start of input — slash commands
)

// mentionTrigger describes an active suggestion trigger within a single line
// of input: the character that opened it, the partial query typed since it,
// and the rune offset (within that line) where a selected suggestion should
// be spliced back in, replacing [Start:cursor).
type mentionTrigger struct {
	Kind  mentionKind
	Query string
	Start int // rune offset of the trigger character within the line
}

// emailLike is the defense-in-depth backstop for "@" that looks like it is
// part of a pasted/typed email address even though it is token-initial (the
// structural rule above already rejects the common inline case, e.g.
// "user@example.com" where "@" is mid-word). It requires a non-empty local
// part before "@" — i.e. the whole typed token already looks like a
// complete local@domain.tld address, not just a bare "@word.ext" file
// mention (which is structurally identical to a *bare*-domain email and
// cannot be told apart by shape alone; this deliberately favors not
// blocking real file mentions like "@file.go" or "@readme.md" over
// catching the rarer case of a stray-leading-space bare-domain paste, e.g.
// " @example.com" with no local part — a named, accepted simplification).
var emailLike = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+\.[A-Za-z]{2,}$`)

// detectTrigger scans a single line of input, backward from cursor (a rune
// offset into line), for the nearest active "@" or leading "/" trigger.
//
// Scope: this operates on one line only (the textarea's current row), not
// the full multi-line Value(). Quote-parity tracking, escape handling, and
// start-of-token detection are therefore computed from the start of that
// line — a deliberate simplification documented here and in the tests: the
// mention UX targets short, mostly single-line instructions, not embedded
// multi-line quoting.
func detectTrigger(line string, cursor int) mentionTrigger {
	runes := []rune(line)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}

	// "/" only ever triggers as the very first character of the whole line
	// (slash-command position) — mid-text "/" is extremely common in typed
	// file paths ("cd /usr/bin") and must never suggest.
	if len(runes) > 0 && runes[0] == '/' && cursor >= 1 {
		// Only fire while the operator is still within the command token
		// (no whitespace typed yet after it).
		end := 1
		for end < cursor {
			if unicode.IsSpace(runes[end]) {
				return mentionTrigger{Kind: mentionNone}
			}
			end++
		}
		return mentionTrigger{Kind: mentionCommand, Query: string(runes[1:cursor]), Start: 0}
	}

	// Find the nearest unescaped, unquoted, token-initial "@" at or before
	// cursor, scanning backward. "Nearest" resolves ties toward the "@"
	// actively being typed when multiple appear earlier in the line.
	inSingle, inDouble := false, false
	lastAt := -1
	for i := 0; i < cursor && i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\\' && i+1 < len(runes):
			i++ // escaped char: skip both, neither can open/close quoting or be a trigger
			continue
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case r == '@' && !inSingle && !inDouble:
			if isTokenInitial(runes, i) {
				lastAt = i
			} else {
				lastAt = -1 // mid-word "@": not a trigger, and shadows any earlier candidate
			}
		case unicode.IsSpace(r):
			// whitespace always resets: nothing before it can still be "the
			// mention currently being typed" once a new token starts unless
			// that new token itself starts with "@" (handled above).
		}
	}
	if lastAt < 0 {
		return mentionTrigger{Kind: mentionNone}
	}
	// No whitespace between the trigger and cursor: still mid-token.
	for i := lastAt + 1; i < cursor; i++ {
		if unicode.IsSpace(runes[i]) {
			return mentionTrigger{Kind: mentionNone}
		}
	}
	query := string(runes[lastAt+1 : cursor])
	precededByWhitespace := lastAt > 0 && unicode.IsSpace(runes[lastAt-1])
	if precededByWhitespace && emailLike.MatchString(string(runes[lastAt:cursor])) {
		return mentionTrigger{Kind: mentionNone}
	}
	return mentionTrigger{Kind: mentionFile, Query: query, Start: lastAt}
}

// isTokenInitial reports whether runes[i] (an '@') begins a new token: it is
// either the first character of the line, or preceded by whitespace or a
// non-word boundary character. A mention is always a new token, never
// mid-word — this is what structurally excludes "user@example.com" typed
// inline (the '@' there is preceded by the word "user").
func isTokenInitial(runes []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := runes[i-1]
	if unicode.IsSpace(prev) {
		return true
	}
	if unicode.IsLetter(prev) || unicode.IsDigit(prev) || prev == '_' {
		return false
	}
	return true
}

// currentLine extracts the line at rowIdx from a multi-line value. The
// textarea's Column() reports the rune offset within that row directly, so
// no further conversion of the cursor position is needed by callers.
func currentLine(value string, rowIdx int) string {
	lines := strings.Split(value, "\n")
	if rowIdx < 0 || rowIdx >= len(lines) {
		return ""
	}
	return lines[rowIdx]
}
