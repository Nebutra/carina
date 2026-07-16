package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// wrapText soft-wraps one logical line of (possibly styled) text into terminal
// lines of at most width cells, prefixing the first line with initialIndent
// and every continuation with subsequentIndent. It is the prose counterpart to
// fitLine: instead of ellipsis-clipping at the right edge it reflows at word
// boundaries.
//
//   - Width math is terminal-cell based (ansi.StringWidth — the same
//     grapheme/runewidth path as fitLine and fitRenderedLine), so a CJK or
//     other wide grapheme never straddles a soft break.
//   - A word wider than the remaining content budget is hard-broken at
//     grapheme boundaries — except URL-like tokens, which are never broken
//     mid-token: an overlong URL is emitted whole on its own line and left to
//     the outer cell-grid boundary (viewport clipping) rather than corrupted.
//   - Styled input is span-aware: SGR sequences opened by the theme are
//     tracked across breaks, closed at each line end, and re-opened after the
//     indent of the continuation line, so a wrapped span never bleeds into a
//     neighbouring viewport line. Sanitization still happens at the data
//     boundary; the only sequences this function should ever see are
//     renderer-emitted. Under Mono there are no sequences and the text passes
//     through unstyled.
//
// Deterministic: same source + same width → same lines, which keeps render a
// pure function and lets resizePresentations re-wrap from source.
func wrapText(s string, width int, initialIndent, subsequentIndent string) []string {
	if width <= 0 {
		return []string{""}
	}
	// Callers split on newlines at the data boundary; a stray newline here is
	// flattened exactly as fitLine flattens it.
	s = strings.ReplaceAll(s, "\n", " ")

	indent := initialIndent
	lines := make([]string, 0, 1)
	var cur strings.Builder
	curW := 0
	// open is the running SGR state; openAtStart is the state inherited by the
	// line currently being assembled, re-emitted after its indent.
	var open, openAtStart []string
	// link is the OSC 8 hyperlink left open by written chunks; linkAtStart is
	// the hyperlink inherited by the current line. Hyperlinks are the one
	// non-SGR state the renderer emits (markdown link()), and like SGR spans
	// they close at every soft break and re-open after the indent: transcript
	// lines are windowed independently by the viewport, so a hyperlink left
	// open at a line end would claim everything painted after it whenever the
	// closing line scrolls out of view.
	var link, linkAtStart string

	budget := func() int { return maxInt(width-ansi.StringWidth(indent), 1) }
	write := func(chunk string, w int) {
		cur.WriteString(chunk)
		curW += w
		open = advanceSGR(open, chunk)
		link = advanceOSC8(link, chunk)
	}
	flush := func() {
		line := indent + linkAtStart + strings.Join(openAtStart, "") + cur.String()
		if len(open) > 0 {
			line += "\x1b[0m"
		}
		if link != "" {
			line += osc8Close
		}
		lines = append(lines, line)
		cur.Reset()
		curW = 0
		indent = subsequentIndent
		openAtStart = append([]string(nil), open...)
		linkAtStart = link
	}

	pendingSpace := ""
	for _, tok := range splitSpaceRuns(s) {
		if tok[0] == ' ' {
			pendingSpace = tok
			continue
		}
		// Authored leading spaces survive on the first visual line; whitespace
		// at a soft break is dropped like any wrapped inter-word space.
		if curW == 0 && len(lines) == 0 && pendingSpace != "" {
			if len(pendingSpace) < budget() {
				write(pendingSpace, len(pendingSpace))
			}
			pendingSpace = ""
		}
		wordW := ansi.StringWidth(tok)
		for tok != "" {
			b := budget()
			switch {
			case curW > 0 && curW+len(pendingSpace)+wordW <= b:
				write(pendingSpace+tok, len(pendingSpace)+wordW)
				tok = ""
			case curW > 0:
				flush()
				pendingSpace = ""
				continue
			case wordW <= b:
				write(tok, wordW)
				tok = ""
			case isURLToken(tok):
				// Never break inside a URL-like token: a reflowed link that no
				// longer opens is worse than an overflowing line.
				write(tok, wordW)
				tok = ""
			default:
				head := ansi.Truncate(tok, b, "")
				headW := ansi.StringWidth(head)
				if headW == 0 {
					// A wide grapheme in a one-cell budget: take the grapheme
					// anyway rather than looping forever.
					head = ansi.Truncate(tok, 2, "")
					headW = ansi.StringWidth(head)
				}
				if headW == 0 {
					write(tok, wordW)
					tok = ""
					continue
				}
				write(head, headW)
				flush()
				tok = ansi.TruncateLeft(tok, headW, "")
				wordW = ansi.StringWidth(tok)
			}
		}
		pendingSpace = ""
	}
	if curW > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

// splitSpaceRuns splits a line into alternating runs of spaces and non-spaces
// so authored spacing inside a line survives wrapping when it fits. Only the
// plain space byte separates words: renderer-emitted SGR sequences never
// contain 0x20 and sanitize() has already removed inbound control bytes, so a
// byte scan cannot split inside an escape sequence or a UTF-8 rune.
func splitSpaceRuns(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	inSpace := s[0] == ' '
	for i := 1; i < len(s); i++ {
		if (s[i] == ' ') != inSpace {
			out = append(out, s[start:i])
			start = i
			inSpace = !inSpace
		}
	}
	return append(out, s[start:])
}

// isURLToken reports whether a word is URL-like (mirrors Codex wrapping.rs).
// Leading bracket/quote punctuation is ignored so "(https://…)" still counts.
func isURLToken(tok string) bool {
	plain := strings.TrimLeft(ansi.Strip(tok), "(<[{'\"")
	lower := strings.ToLower(plain)
	for _, prefix := range []string{"http://", "https://", "ftp://", "file://", "mailto:", "www."} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// osc8Close terminates an OSC 8 hyperlink: an empty destination.
const osc8Close = "\x1b]8;;\x1b\\"

// advanceOSC8 tracks the OSC 8 hyperlink a written chunk leaves open so
// wrapText can close it at a line break and re-open it on the continuation
// line. The renderer only ever emits ST-terminated OSC 8 sequences (markdown
// link()), and sanitize() strips inbound escapes, so a plain scan cannot
// misfire inside other data.
func advanceOSC8(link, chunk string) string {
	for {
		i := strings.Index(chunk, "\x1b]8;")
		if i < 0 {
			return link
		}
		rest := chunk[i:]
		j := strings.Index(rest, "\x1b\\")
		if j < 0 {
			return link
		}
		if seq := rest[:j+2]; seq == osc8Close {
			link = ""
		} else {
			link = seq
		}
		chunk = rest[j+2:]
	}
}

// advanceSGR tracks which SGR sequences a written chunk leaves open so
// wrapText can close them at a line break and re-open them on the
// continuation line. Only CSI …m sequences carry style state; every style the
// theme emits is plain SGR, and a full reset ("\x1b[0m" or "\x1b[m") clears
// the state exactly as it clears the terminal's.
func advanceSGR(open []string, chunk string) []string {
	for {
		i := strings.Index(chunk, "\x1b[")
		if i < 0 {
			return open
		}
		rest := chunk[i+2:]
		j := strings.IndexFunc(rest, func(r rune) bool { return r >= 0x40 && r <= 0x7e })
		if j < 0 {
			return open
		}
		if rest[j] == 'm' {
			if params := rest[:j]; params == "" || params == "0" {
				open = nil
			} else {
				open = append(open, chunk[i:i+2+j+1])
			}
		}
		chunk = rest[j+1:]
	}
}
