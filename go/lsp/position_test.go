package lsp

// V3 D2 tests: LSP wire columns are UTF-16 code units, daemon columns are
// bytes. ASCII round-trips are regression guards; the CJK and emoji
// (surrogate-pair) cases are the fix under test.

import (
	"testing"
	"unicode/utf8"
)

func TestUTF16ColASCII(t *testing.T) {
	if got := UTF16Col("abc def", 4); got != 4 {
		t.Fatalf("UTF16Col(ascii, 4) = %d, want 4", got)
	}
	if got := ByteCol("abc def", 4); got != 4 {
		t.Fatalf("ByteCol(ascii, 4) = %d, want 4", got)
	}
}

func TestUTF16ColCJK(t *testing.T) {
	// "你好abc": 你/好 are 3 bytes but 1 UTF-16 code unit each, so the byte
	// column of 'a' (6) is UTF-16 column 2.
	line := "你好abc"
	if got := UTF16Col(line, 6); got != 2 {
		t.Fatalf("UTF16Col(%q, 6) = %d, want 2", line, got)
	}
	if got := ByteCol(line, 2); got != 6 {
		t.Fatalf("ByteCol(%q, 2) = %d, want 6", line, got)
	}
}

func TestUTF16ColSurrogatePairs(t *testing.T) {
	// "😀x": the emoji is 4 bytes and TWO UTF-16 code units (surrogate
	// pair), so 'x' sits at byte column 4 == UTF-16 column 2.
	line := "😀x"
	if got := UTF16Col(line, 4); got != 2 {
		t.Fatalf("UTF16Col(%q, 4) = %d, want 2", line, got)
	}
	if got := ByteCol(line, 2); got != 4 {
		t.Fatalf("ByteCol(%q, 2) = %d, want 4", line, got)
	}
}

func TestUTF16ColMixed(t *testing.T) {
	// "a你😀b": 'b' at byte 1+3+4=8, UTF-16 unit 1+1+2=4.
	line := "a你😀b"
	if got := UTF16Col(line, 8); got != 4 {
		t.Fatalf("UTF16Col(%q, 8) = %d, want 4", line, got)
	}
	if got := ByteCol(line, 4); got != 8 {
		t.Fatalf("ByteCol(%q, 4) = %d, want 8", line, got)
	}
}

// ---- V4 D3: surrogate-boundary contract -----------------------------------

// TestByteColInsideSurrogatePairMapsToRuneStart: ByteCol's documented
// contract is "the start of the rune the code unit falls in" — a UTF-16
// column INSIDE a surrogate pair must map to that rune's first byte, not
// fall through to the next rune.
func TestByteColInsideSurrogatePairMapsToRuneStart(t *testing.T) {
	if got := ByteCol("😀x", 1); got != 0 {
		t.Fatalf(`ByteCol("😀x", 1) = %d, want 0 — a column inside a surrogate pair maps to the START of that rune`, got)
	}
	// "a😀b😀c" — bytes: a=0, 😀=1..4, b=5, 😀=6..9, c=10, end=11;
	// UTF-16: a=0, 😀=1-2, b=3, 😀=4-5, c=6, end=7.
	line := "a😀b😀c"
	cases := []struct{ utf16Col, wantByte int }{
		{0, 0}, {1, 1}, {2, 1}, {3, 5}, {4, 6}, {5, 6}, {6, 10}, {7, 11},
	}
	for _, c := range cases {
		if got := ByteCol(line, c.utf16Col); got != c.wantByte {
			t.Errorf("ByteCol(%q, %d) = %d, want %d", line, c.utf16Col, got, c.wantByte)
		}
	}
}

// TestByteColUTF16ColAreInversesAtSurrogateBoundaries: for EVERY UTF-16
// column, ByteCol lands on a rune start and UTF16Col maps that start back to
// the column's own rune — the proper-inverse property at pair boundaries.
func TestByteColUTF16ColAreInversesAtSurrogateBoundaries(t *testing.T) {
	line := "x😀😀y"
	max := UTF16Col(line, len(line))
	for u := 0; u <= max; u++ {
		b := ByteCol(line, u)
		if b == len(line) {
			if back := UTF16Col(line, b); back != u {
				t.Fatalf("u=%d maps to end-of-line byte %d, which maps back to %d", u, b, back)
			}
			continue
		}
		if !utf8.RuneStart(line[b]) {
			t.Fatalf("ByteCol(%q, %d) = %d is not a rune start", line, u, b)
		}
		r, _ := utf8.DecodeRuneInString(line[b:])
		back := UTF16Col(line, b)
		width := utf16RuneLen(r)
		if !(back <= u && u < back+width) {
			t.Fatalf("not inverses at u=%d: byte %d starts a rune spanning UTF-16 [%d, %d)", u, b, back, back+width)
		}
	}
}

func TestByteColUTF16ColRoundTripOnRuneBoundaries(t *testing.T) {
	line := "fn 名前_x(😀: T) {}"
	for byteCol := 0; byteCol <= len(line); {
		u := UTF16Col(line, byteCol)
		if got := ByteCol(line, u); got != byteCol {
			t.Fatalf("round trip at byte %d: UTF16Col=%d, ByteCol back = %d", byteCol, u, got)
		}
		if byteCol == len(line) {
			break
		}
		_, size := utf8.DecodeRuneInString(line[byteCol:])
		byteCol += size
	}
}
