// Package mathapprox renders TeX math as a terminal-native Unicode
// approximation (rich-text plan P3). Line finds `$...$`, `$$...$$`, `\(...\)`
// and `\[...\]` spans in inline text with currency-safe escaping rules, and
// Approx maps a deterministic TeX subset — scripts, Greek letters, fractions,
// radicals, operators, arrows, relations — onto Unicode.
//
// Everything here is a pure function of its input: same text → same segments,
// suitable for golden tests. The degradation contract is total: any construct
// outside the subset makes the whole span render verbatim (the caller styles
// it RoleMathApprox either way), so source text is never corrupted or dropped
// — it is only ever re-spelled when the re-spelling is exact.
package mathapprox

import "strings"

// Segment is one run of an inline-text line after math detection.
type Segment struct {
	// Text is the display text: the Unicode approximation for recognized
	// math, the verbatim source (delimiters included) for detected-but-
	// unrecognized math, and the plain text between spans otherwise.
	Text string
	// Math marks the segment for RoleMathApprox styling by the caller.
	Math bool
}

// Line splits one logical line of inline text into plain and math segments.
//
// Delimiter rules (the standard Pandoc-style heuristic, so currency is never
// math): a single `$` opens only when the next character is not whitespace,
// and closes only when the previous character is not whitespace and the next
// is not a digit — "$5 and $10" stays text. `\$` is an escaped dollar and
// `\\` an escaped backslash; both are consumed so they can never delimit.
// `$$...$$`, `\(...\)` and `\[...\]` pair on the same line (the streaming
// holdback keeps display math intact, and goldmark folds soft breaks into
// spaces before text reaches this function). Unclosed delimiters stay plain
// text: a partial formula is never rendered as math.
func Line(s string) []Segment {
	var segs []Segment
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			segs = append(segs, Segment{Text: text.String()})
			text.Reset()
		}
	}
	math := func(source, tex string) {
		flush()
		if approx, ok := Approx(tex); ok {
			segs = append(segs, Segment{Text: approx, Math: true})
			return
		}
		segs = append(segs, Segment{Text: source, Math: true})
	}
	i := 0
	for i < len(s) {
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, `\\`):
			text.WriteString(`\\`)
			i += 2
		case strings.HasPrefix(rest, `\$`):
			text.WriteByte('$')
			i += 2
		case strings.HasPrefix(rest, `\(`):
			j := strings.Index(rest[2:], `\)`)
			if j < 0 {
				text.WriteString(`\(`)
				i += 2
				continue
			}
			math(rest[:j+4], strings.TrimSpace(rest[2:j+2]))
			i += j + 4
		case strings.HasPrefix(rest, `\[`):
			j := strings.Index(rest[2:], `\]`)
			if j < 0 {
				text.WriteString(`\[`)
				i += 2
				continue
			}
			math(rest[:j+4], strings.TrimSpace(rest[2:j+2]))
			i += j + 4
		case strings.HasPrefix(rest, "$$"):
			j := strings.Index(rest[2:], "$$")
			if j < 0 || strings.TrimSpace(rest[2:j+2]) == "" {
				text.WriteString("$$")
				i += 2
				continue
			}
			math(rest[:j+4], strings.TrimSpace(rest[2:j+2]))
			i += j + 4
		case rest[0] == '$':
			end, ok := inlineDollarEnd(rest)
			if !ok {
				text.WriteByte('$')
				i++
				continue
			}
			math(rest[:end+1], rest[1:end])
			i += end + 1
		default:
			// Plain byte copy; UTF-8 continuation bytes never match the
			// ASCII delimiters above, so multi-byte runes pass through whole.
			text.WriteByte(rest[0])
			i++
		}
	}
	flush()
	return segs
}

// inlineDollarEnd finds the closing `$` for an inline span opened at rest[0].
// It applies the currency heuristic documented on Line and skips over
// backslash-escaped characters inside the span.
func inlineDollarEnd(rest string) (int, bool) {
	if len(rest) < 3 {
		return 0, false
	}
	if c := rest[1]; c == ' ' || c == '\t' {
		return 0, false
	}
	for j := 2; j < len(rest); j++ {
		switch rest[j] {
		case '\\':
			j++
		case '$':
			if c := rest[j-1]; c == ' ' || c == '\t' {
				continue
			}
			if j+1 < len(rest) && rest[j+1] >= '0' && rest[j+1] <= '9' {
				continue
			}
			return j, true
		}
	}
	return 0, false
}
