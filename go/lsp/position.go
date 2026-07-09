// UTF-16 position conversion (docs/plans/code-intelligence.md, V3 D2): LSP
// wire columns count UTF-16 code units within a line, while the daemon works
// in byte columns. These helpers convert between the two so CJK/emoji lines
// query and render at the right character. Columns are 0-based; columns past
// the end of the line clamp to the line's end in the target unit.
package lsp

// utf16RuneLen is the UTF-16 code-unit width of one rune: 2 for a surrogate
// pair (outside the BMP), else 1 (invalid runes decode as U+FFFD, width 1).
func utf16RuneLen(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// UTF16Col converts a 0-based byte column in line to a 0-based UTF-16 code
// unit column. A byte column inside a multi-byte rune counts the whole rune.
func UTF16Col(line string, byteCol int) int {
	if byteCol > len(line) {
		byteCol = len(line)
	}
	col := 0
	for i, r := range line {
		if i >= byteCol {
			return col
		}
		col += utf16RuneLen(r)
	}
	return col
}

// ByteCol converts a 0-based UTF-16 code unit column in line to a 0-based
// byte column (the start of the rune the code unit falls in): a column
// landing INSIDE a surrogate pair maps to that rune's first byte, so
// ByteCol/UTF16Col are proper inverses at pair boundaries (V4 D3).
func ByteCol(line string, utf16Col int) int {
	col := 0
	for i, r := range line {
		if utf16Col < col+utf16RuneLen(r) {
			return i
		}
		col += utf16RuneLen(r)
	}
	return len(line)
}
