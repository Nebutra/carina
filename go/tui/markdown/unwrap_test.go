package markdown

import "testing"

// Fence unwrapping is deliberately conservative: only md/markdown fences that
// demonstrably hold a GFM table lose their fence. Everything else — other
// languages, tableless bodies, unterminated fences — stays byte-identical.
func TestUnwrapMarkdownFences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "markdown fence with table unwraps",
			in:   "before\n```markdown\n| a | b |\n|---|---|\n| 1 | 2 |\n```\nafter",
			want: "before\n| a | b |\n|---|---|\n| 1 | 2 |\nafter",
		},
		{
			name: "md info string also unwraps",
			in:   "```md\nh | k\n--|--\n```",
			want: "h | k\n--|--",
		},
		{
			name: "tilde fence and extra info tokens unwrap",
			in:   "~~~markdown table\n| a |\n|---|\n~~~",
			want: "| a |\n|---|",
		},
		{
			name: "markdown fence without a table stays",
			in:   "```markdown\njust prose\n```",
			want: "```markdown\njust prose\n```",
		},
		{
			name: "other language stays even with pipes",
			in:   "```go\n| a | b |\n|---|---|\n```",
			want: "```go\n| a | b |\n|---|---|\n```",
		},
		{
			name: "unterminated fence stays",
			in:   "```markdown\n| a | b |\n|---|---|",
			want: "```markdown\n| a | b |\n|---|---|",
		},
		{
			name: "delimiter must contain dash and pipe",
			in:   "```markdown\na | b\n:::\n```",
			want: "```markdown\na | b\n:::\n```",
		},
		{
			name: "longer close run still closes",
			in:   "```md\n| a |\n|---|\n`````",
			want: "| a |\n|---|",
		},
		{
			name: "no fences passes through",
			in:   "plain\ntext",
			want: "plain\ntext",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unwrapMarkdownFences(tc.in); got != tc.want {
				t.Errorf("unwrapMarkdownFences(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
