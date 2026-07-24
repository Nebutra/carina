package markdown

import "strings"

// Stream is the append-only source of one streaming markdown message and its
// commit boundary. The caller feeds
// sanitized deltas in; the Stream decides how much of the source has become
// stable — safe to render once and append immutably — and how much must stay
// in the mutable tail that is re-rendered and replaced in place on every
// update.
//
// Commit rules:
//   - Newline-gated: the tail never includes an incomplete trailing line, so
//     a half-received word is never rendered.
//   - The boundary only advances at blank lines between blocks. A block that
//     could still grow (a paragraph mid-stream, a list without its blank
//     terminator, a setext heading whose underline has not arrived) therefore
//     never splits across the stable/tail edge, which keeps every committed
//     prefix render-stable: Render(stable) is a prefix of Render(source).
//   - Loose lists: a blank line inside an open list item is not a block
//     boundary — the item's next paragraph is indented continuation text, and
//     re-parsing it standalone would strip its bullet indent (or worse, read
//     it as an indented code block). The boundary only advances once a
//     complete line has left the item's continuation indentation.
//   - Holdback: a detected table (header + delimiter), an unterminated code
//     fence, and an unterminated display-math block ($$…$$ or \[…\]) pin the
//     boundary before them until they close or the stream ends. Tables and
//     fences contain no blank lines of their own, so the blank-line rule and
//     the explicit fence/math states together implement the holdback.
//
// Deterministic: the boundary is a pure function of the source text and is
// monotonic — it never regresses across pushes.
type Stream struct {
	src       strings.Builder
	committed int
}

// Push appends one delta and returns the newly stable source chunk (possibly
// empty) plus the current renderable tail: everything between the commit
// boundary and the end of the last complete line.
func (s *Stream) Push(delta string) (stable, tail string) {
	s.src.WriteString(delta)
	source := s.src.String()
	boundary := commitBoundary(source, s.committed)
	if boundary < s.committed {
		boundary = s.committed
	}
	stable = source[s.committed:boundary]
	s.committed = boundary
	return stable, source[boundary:completeEnd(source)]
}

// Finish ends the stream: everything still uncommitted — held-back constructs
// and the trailing partial line included — becomes the final stable chunk.
func (s *Stream) Finish() (stable string) {
	source := s.src.String()
	stable = source[s.committed:]
	s.committed = len(source)
	return stable
}

// completeEnd returns the byte offset just past the last complete line.
func completeEnd(source string) int {
	if i := strings.LastIndexByte(source, '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

// scanState is the holdback state machine over complete source lines.
type scanState int

const (
	scanNormal scanState = iota
	scanFence
	scanMath
	scanTable
)

// commitBoundary scans the complete lines of source from start — a previously
// committed boundary, where by construction the state is scanNormal with no
// construct or list item open — and returns the largest committable byte
// offset per the rules documented on Stream. Resuming from the committed
// offset keeps the per-push scan proportional to the uncommitted suffix, not
// the whole accumulated message.
func commitBoundary(source string, start int) int {
	boundary := start
	offset := start
	state := scanNormal
	fenceMarker := ""
	mathMarker := ""
	// listIndent is the content column of the outermost open list item, or -1
	// when no list is open. It gates the blank-line rule per the loose-list
	// commit rule above.
	listIndent := -1
	end := completeEnd(source)
	if end < start {
		end = start
	}
	lines := strings.SplitAfter(source[start:end], "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	// blankCommit advances the boundary at a blank line unless the blank is
	// still inside an open list item: continuation is decided by the indent of
	// the next complete content line, and an unanswerable lookahead (nothing
	// but blanks so far) holds the boundary — monotonic and safe, since the
	// next push rescans from the same committed offset.
	blankCommit := func(i int) {
		if listIndent >= 0 {
			indent, known := nextContentIndent(lines, i+1)
			if !known || indent >= listIndent {
				return
			}
			listIndent = -1
		}
		boundary = offset
	}
	for i, line := range lines {
		offset += len(line)
		text := strings.TrimSuffix(line, "\n")
		switch state {
		case scanFence:
			if fenceClose(text, fenceMarker) {
				state = scanNormal
			}
		case scanMath:
			if mathCloses(text, mathMarker) {
				state = scanNormal
			}
		case scanTable:
			// A blank line both terminates the table and is a block boundary.
			if strings.TrimSpace(text) == "" {
				state = scanNormal
				blankCommit(i)
			}
		default:
			switch {
			case strings.TrimSpace(text) == "":
				blankCommit(i)
			default:
				if marker, _ := fenceOpen(text); marker != "" {
					state, fenceMarker = scanFence, marker
				} else if marker := mathOpen(text); marker != "" {
					state, mathMarker = scanMath, marker
				} else if i+1 < len(lines) && strings.Contains(text, "|") &&
					isTableDelimiter(strings.TrimSuffix(lines[i+1], "\n")) {
					state = scanTable
				} else if listIndent < 0 {
					if col, ok := listItemContent(text); ok {
						listIndent = col
					}
				}
			}
		}
	}
	return boundary
}

// listItemContent reports the content column of a list-item marker line:
// optional leading spaces, a bullet (-, *, +) or an ordered marker (1–9
// digits followed by "." or ")"), then a space. Continuation lines of the
// item are indented at least to that column.
func listItemContent(text string) (int, bool) {
	i := 0
	for i < len(text) && text[i] == ' ' {
		i++
	}
	switch {
	case i < len(text) && (text[i] == '-' || text[i] == '*' || text[i] == '+'):
		i++
	default:
		digits := 0
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
			digits++
		}
		if digits < 1 || digits > 9 || i >= len(text) || (text[i] != '.' && text[i] != ')') {
			return 0, false
		}
		i++
	}
	if i >= len(text) || text[i] != ' ' {
		return 0, false
	}
	return i + 1, true
}

// nextContentIndent finds the leading-space indent of the first non-blank
// complete line at or after index i. known=false means every remaining
// complete line is blank: the continuation question has no answer yet.
func nextContentIndent(lines []string, i int) (indent int, known bool) {
	for ; i < len(lines); i++ {
		text := strings.TrimSuffix(lines[i], "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		n := 0
		for n < len(text) && text[n] == ' ' {
			n++
		}
		return n, true
	}
	return 0, false
}

// mathOpen reports the display-math delimiter this line leaves open ("" when
// none). Only a line-leading $$ opens dollar math — prose like "totals $$40"
// or an escaped \$$ must never flip the scanner into a holdback it cannot
// leave — and \[ opens bracket math only while its \] has not arrived.
// Milestone P3 renders the content; the streaming holdback only refuses to
// sever a partial formula.
func mathOpen(text string) string {
	if trimmed := strings.TrimSpace(text); strings.HasPrefix(trimmed, "$$") &&
		!strings.Contains(trimmed[2:], "$$") {
		return "$$"
	}
	if i := strings.LastIndex(text, `\[`); i >= 0 && !strings.Contains(text[i:], `\]`) {
		return `\[`
	}
	return ""
}

// mathCloses matches the delimiter that opened the block: a \[ formula is not
// "closed" by a stray $$ (nor vice versa), so mismatched delimiters cannot
// sever a formula the holdback exists to protect.
func mathCloses(text, marker string) bool {
	if marker == `\[` {
		return strings.Contains(text, `\]`)
	}
	return strings.Contains(text, "$$")
}
