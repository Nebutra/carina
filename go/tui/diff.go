package tui

import (
	"strings"

	"github.com/Nebutra/carina/go/tui/theme"
)

// ColorDiff renders a plain unified diff (the wire format of
// PatchTransaction.diff) as per-line styled strings — the approval overlay's
// reviewable-artifact body. Promoted from the spike's colorizer, with fixes:
// "+++ "/"--- " file headers are headers, not adds/deletes; a CRLF-sourced
// diff (patch produced against a Windows-checked-out file) has its trailing
// \r stripped per line so a stray bare CR never re-homes the terminal cursor
// mid-overlay; and the "\ No newline at end of file" and "Binary files ...
// differ" markers are treated as headers rather than falling through to the
// (untouched, in Mono unstyled) context bucket. All color flows through the
// theme; under Mono the diff passes through unchanged (CRLF normalization
// aside — a bare mid-line \r is never a legitimate part of terminal output).
func ColorDiff(diff string, th theme.Theme) []string {
	add := th.Style(theme.RoleDiffAdd)
	del := th.Style(theme.RoleDiffDel)
	hdr := th.Style(theme.RoleDiffHunk)
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		ln = strings.TrimSuffix(ln, "\r")
		switch {
		case strings.HasPrefix(ln, "+++ "), strings.HasPrefix(ln, "--- "),
			strings.HasPrefix(ln, "@@"), strings.HasPrefix(ln, "diff "),
			strings.HasPrefix(ln, "index "),
			strings.HasPrefix(ln, `\ No newline at end of file`),
			strings.HasPrefix(ln, "Binary files "):
			out = append(out, hdr.Render(ln))
		case strings.HasPrefix(ln, "+"):
			out = append(out, add.Render(ln))
		case strings.HasPrefix(ln, "-"):
			out = append(out, del.Render(ln))
		default:
			out = append(out, ln)
		}
	}
	return out
}
