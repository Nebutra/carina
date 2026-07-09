package tui

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

const sampleDiff = `diff --git a/hello.txt b/hello.txt
index e69de29..4b5fa63 100644
--- a/hello.txt
+++ b/hello.txt
@@ -0,0 +1,2 @@
+hello world
+你好，世界
-old line
 context line`

// The colorizer promoted from the spike: adds, deletes, and headers get
// distinct theme roles; everything flows through the theme, never raw ANSI.
func TestColorDiffStylesLines(t *testing.T) {
	th := theme.New(theme.ANSI256)
	lines := ColorDiff(sampleDiff, th)
	if len(lines) != 9 {
		t.Fatalf("got %d lines, want 9", len(lines))
	}
	// Adds use the DiffAdd role (Core Glow, 139).
	if !strings.Contains(lines[5], "38;5;139") {
		t.Errorf("add line not styled with DiffAdd: %q", lines[5])
	}
	if !strings.Contains(lines[6], "38;5;139") {
		t.Errorf("zh add line not styled with DiffAdd: %q", lines[6])
	}
	// Deletes use the DiffDel role (error family, 132).
	if !strings.Contains(lines[7], "38;5;132") {
		t.Errorf("del line not styled with DiffDel: %q", lines[7])
	}
	// Hunk and file headers use the DiffHunk role (Blue Giant, 189) — the
	// "+++"/"---" file headers must NOT be mistaken for add/del lines.
	for _, i := range []int{0, 1, 2, 3, 4} {
		if !strings.Contains(lines[i], "38;5;189") {
			t.Errorf("header line %d not styled as hunk: %q", i, lines[i])
		}
	}
	if strings.Contains(lines[2], "38;5;132") {
		t.Errorf("'--- a/...' header wrongly styled as delete: %q", lines[2])
	}
	if strings.Contains(lines[3], "38;5;139") {
		t.Errorf("'+++ b/...' header wrongly styled as add: %q", lines[3])
	}
	// Context lines stay plain.
	if strings.Contains(lines[8], "\x1b[") {
		t.Errorf("context line should be plain, got %q", lines[8])
	}
}

// NO_COLOR contract: under Mono the diff body passes through byte-for-byte.
func TestColorDiffMonoPassthrough(t *testing.T) {
	th := theme.New(theme.Mono)
	lines := ColorDiff(sampleDiff, th)
	want := strings.Split(sampleDiff, "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

// A CRLF-terminated diff (a patch produced against a Windows-checked-out
// file) must not leave a trailing \r baked into each rendered line — a
// stray \r corrupts the overlay layout (it re-homes the cursor to column 0
// on terminals that honor bare CR, overwriting the start of the line).
func TestColorDiffStripsCarriageReturns(t *testing.T) {
	th := theme.New(theme.Mono)
	crlf := strings.ReplaceAll(sampleDiff, "\n", "\r\n")
	lines := ColorDiff(crlf, th)
	want := strings.Split(sampleDiff, "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i, ln := range lines {
		if strings.ContainsRune(ln, '\r') {
			t.Errorf("line %d retains a carriage return: %q", i, ln)
		}
		if ln != want[i] {
			t.Errorf("line %d = %q, want %q", i, ln, want[i])
		}
	}
}

// A diff with no trailing newline on the final hunk line (unified diff's "\
// No newline at end of file" marker) must render that marker as a header,
// not fall through to the default (context) styling bucket.
func TestColorDiffNoNewlineMarker(t *testing.T) {
	th := theme.New(theme.ANSI256)
	diff := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n\\ No newline at end of file"
	lines := ColorDiff(diff, th)
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6: %q", len(lines), lines)
	}
	if !strings.Contains(lines[5], "38;5;189") {
		t.Errorf("'\\ No newline' marker not styled as header: %q", lines[5])
	}
}

// A binary-file diff ("Binary files a/x and b/x differ") must render as a
// header, not be silently mis-colored as a context (or worse, add/del)
// line — there is no textual body to color in the first place.
func TestColorDiffBinaryMarker(t *testing.T) {
	th := theme.New(theme.ANSI256)
	diff := "diff --git a/x b/x\nindex 111..222 100644\nBinary files a/x and b/x differ"
	lines := ColorDiff(diff, th)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), lines)
	}
	if !strings.Contains(lines[2], "38;5;189") {
		t.Errorf("binary marker not styled as header: %q", lines[2])
	}
}
