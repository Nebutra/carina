package markdown

import "strings"

// unwrapMarkdownFences conservatively removes ```md / ```markdown fences whose
// body contains a GFM table header+delimiter pair. Models often quote an entire
// markdown answer inside
// such a fence, which would otherwise flatten tables into a code block. Any
// fence that is unterminated, carries another info string, or holds no table
// stays verbatim — unwrapping never guesses.
func unwrapMarkdownFences(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		marker, info := fenceOpen(lines[i])
		if marker == "" || !isMarkdownInfo(info) {
			out = append(out, lines[i])
			continue
		}
		j := i + 1
		for j < len(lines) && !fenceClose(lines[j], marker) {
			j++
		}
		if j >= len(lines) {
			// Unterminated fence: leave the source untouched.
			out = append(out, lines[i])
			continue
		}
		body := lines[i+1 : j]
		if containsTablePair(body) {
			out = append(out, body...)
		} else {
			out = append(out, lines[i:j+1]...)
		}
		i = j
	}
	return strings.Join(out, "\n")
}

// fenceOpen reports the fence marker run ("```", "~~~~", …) and the lowercased
// info string of an opening fence line, or "" when the line opens no fence.
func fenceOpen(line string) (marker, info string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return "", ""
	}
	for _, ch := range []byte{'`', '~'} {
		run := 0
		for run < len(trimmed) && trimmed[run] == ch {
			run++
		}
		if run >= 3 {
			return trimmed[:run], strings.ToLower(strings.TrimSpace(trimmed[run:]))
		}
	}
	return "", ""
}

// fenceClose matches a closing fence: the same character repeated at least as
// long as the opener, with nothing but whitespace around it.
func fenceClose(line, marker string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < len(marker) {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != marker[0] {
			return false
		}
	}
	return true
}

func isMarkdownInfo(info string) bool {
	first, _, _ := strings.Cut(info, " ")
	return first == "md" || first == "markdown"
}

// containsTablePair looks for a pipe-bearing header line directly above a GFM
// delimiter row — the conservative signal that the fence body is a table, not
// code that merely resembles one.
func containsTablePair(body []string) bool {
	for i := 0; i+1 < len(body); i++ {
		if strings.Contains(body[i], "|") && isTableDelimiter(body[i+1]) {
			return true
		}
	}
	return false
}

// isTableDelimiter matches the GFM delimiter row: only pipes, dashes, colons,
// and spaces, with at least one dash and one pipe.
func isTableDelimiter(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "-") || !strings.Contains(trimmed, "|") {
		return false
	}
	for _, r := range trimmed {
		switch r {
		case '|', '-', ':', ' ', '\t':
		default:
			return false
		}
	}
	return true
}
