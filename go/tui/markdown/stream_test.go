package markdown

import (
	"strings"
	"testing"
)

// The holdback state machine, table-driven: the boundary only advances at
// blank lines between blocks, and a detected table, an unterminated fence, or
// an unterminated display-math block pins it earlier until closed.
func TestCommitBoundaryHoldback(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // committable prefix: src[:commitBoundary(src)]
	}{
		{
			name: "no complete line commits nothing",
			src:  "hello",
			want: "",
		},
		{
			name: "a block without its blank terminator stays mutable",
			src:  "hello\nworld\n",
			want: "",
		},
		{
			name: "blank line commits the finished block",
			src:  "one\n\ntwo",
			want: "one\n\n",
		},
		{
			name: "boundary tracks the last blank line",
			src:  "a\n\nb\n\nc\n",
			want: "a\n\nb\n\n",
		},
		{
			name: "setext underline cannot be severed from its text",
			src:  "Title\nno commit before underline arrives\n",
			want: "",
		},
		{
			name: "unterminated fence holds even across its blank lines",
			src:  "a\n\n```go\ncode\n\nmore\n",
			want: "a\n\n",
		},
		{
			name: "closed fence commits at the next blank",
			src:  "```go\nc\n```\n\nnext\n",
			want: "```go\nc\n```\n\n",
		},
		{
			name: "unterminated dollar math holds",
			src:  "a\n\n$$\nx+y\n\n",
			want: "a\n\n",
		},
		{
			name: "closed dollar math commits at the next blank",
			src:  "$$\nx\n$$\n\nafter",
			want: "$$\nx\n$$\n\n",
		},
		{
			name: "inline math on one line does not hold",
			src:  "x $$y$$ z\n\nnext",
			want: "x $$y$$ z\n\n",
		},
		{
			name: "unterminated bracket math holds",
			src:  "a\n\n\\[\nx\n\n",
			want: "a\n\n",
		},
		{
			name: "table without a terminating blank stays in the tail",
			src:  "| a | b |\n|---|---|\n| 1 | 2 |\n",
			want: "",
		},
		{
			name: "table closed by a blank line commits whole",
			src:  "| a | b |\n|---|---|\n| 1 | 2 |\n\n",
			want: "| a | b |\n|---|---|\n| 1 | 2 |\n\n",
		},
		{
			// A blank line inside a loose list item is not a block boundary:
			// the continuation paragraph re-parsed standalone would lose its
			// bullet indentation, breaking the render-prefix invariant.
			name: "blank inside a loose list item does not split the item",
			src:  "- first paragraph\n\n  continuation of item\n\n",
			want: "",
		},
		{
			name: "list commits once a line leaves its continuation indent",
			src:  "- first paragraph\n\n  continuation of item\n\nnext block\n",
			want: "- first paragraph\n\n  continuation of item\n\n",
		},
		{
			name: "blank between loose items at marker indent commits",
			src:  "- a\n\n- b\n\nafter\n",
			want: "- a\n\n- b\n\n",
		},
		{
			name: "ordered list continuation also holds",
			src:  "1. one\n\n   more of one\n\n",
			want: "",
		},
		{
			// mathOpens must not fire on literal dollars in prose: a false
			// positive would pin the boundary for the rest of the stream.
			name: "literal dollars in prose are not display math",
			src:  "totals $$40 for the month\n\nnext para\n\n",
			want: "totals $$40 for the month\n\nnext para\n\n",
		},
		{
			// A \[ block is only closed by \]; a stray $$ must not release the
			// holdback and let the next blank sever the formula.
			name: "bracket math ignores a mismatched dollar closer",
			src:  "\\[\nx $$ y\n\nafter\n",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.src[:commitBoundary(tc.src, 0)]; got != tc.want {
				t.Errorf("commitBoundary(%q): stable = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// commitBoundary resumes from the committed offset (the per-push scan must be
// proportional to the uncommitted suffix, not the whole message): pushing any
// delta split must land on exactly the boundary a full scan computes.
func TestCommitBoundaryResumesFromCommitted(t *testing.T) {
	src := "para one\n\n- item\n\n  cont\n\nnext\n\n```go\nx\n```\n\n$$\ne\n$$\n\nend\n\n"
	var s Stream
	for i := 0; i < len(src); i++ {
		s.Push(src[i : i+1])
		if want := commitBoundary(src[:i+1], 0); s.committed != want {
			t.Fatalf("after %q: committed = %d, full scan = %d", src[:i+1], s.committed, want)
		}
	}
}

// Deltas in, expected stable/tail split out — the commit sequence a streaming
// message goes through, ending with Finish committing the held-back rest.
func TestStreamCommitSequence(t *testing.T) {
	var s Stream
	steps := []struct {
		delta      string
		wantStable string
		wantTail   string
	}{
		// Newline-gated: an incomplete trailing line is never in the tail.
		{"# Ti", "", ""},
		{"tle\n\nintro ", "# Title\n\n", ""},
		{"text\n", "", "intro text\n"},
		// The blank line finishes the paragraph; the partial table row stays out.
		{"\n| a |", "intro text\n\n", ""},
		// Detected table (header + delimiter): held in the tail until closed.
		{" b |\n|---|---|\n| 1 | 2 |\n", "", "| a | b |\n|---|---|\n| 1 | 2 |\n"},
	}
	for i, st := range steps {
		stable, tail := s.Push(st.delta)
		if stable != st.wantStable || tail != st.wantTail {
			t.Fatalf("step %d Push(%q) = (%q, %q), want (%q, %q)",
				i, st.delta, stable, tail, st.wantStable, st.wantTail)
		}
	}
	if got := s.Finish(); got != "| a | b |\n|---|---|\n| 1 | 2 |\n" {
		t.Errorf("Finish() = %q", got)
	}
	if got := s.Finish(); got != "" {
		t.Errorf("second Finish() must be empty, got %q", got)
	}
}

// The boundary is monotonic: pushing more source never regresses what has
// already been reported stable.
func TestStreamCommitIsMonotonic(t *testing.T) {
	var s Stream
	var committed strings.Builder
	deltas := []string{"a\n\n", "```go\n", "x\n", "```\n", "\n", "tail\n\n", "end"}
	for _, d := range deltas {
		stable, _ := s.Push(d)
		committed.WriteString(stable)
	}
	committed.WriteString(s.Finish())
	if committed.String() != strings.Join(deltas, "") {
		t.Errorf("stable chunks + Finish must reassemble the source exactly:\n%q", committed.String())
	}
}
