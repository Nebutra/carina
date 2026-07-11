package tui

import "testing"

func TestDetectTrigger(t *testing.T) {
	tests := []struct {
		name string
		line string
		want mentionTrigger
	}{
		{
			name: "plain at-mention fires",
			line: "@foo",
			want: mentionTrigger{Kind: mentionFile, Query: "foo", Start: 0},
		},
		{
			name: "escaped at-mention does not fire",
			line: `\@foo`,
			want: mentionTrigger{Kind: mentionNone},
		},
		{
			name: "single-quoted at-mention does not fire",
			line: "'@foo'",
			want: mentionTrigger{Kind: mentionNone},
		},
		{
			name: "double-quoted at-mention does not fire",
			line: `"@foo"`,
			want: mentionTrigger{Kind: mentionNone},
		},
		{
			name: "email address does not fire",
			line: "user@example.com",
			want: mentionTrigger{Kind: mentionNone},
		},
		{
			name: "email then a real mention: only the second fires",
			line: "email user@x.com and @file.go",
			want: mentionTrigger{Kind: mentionFile, Query: "file.go", Start: 21},
		},
		{
			name: "leading slash at input start fires as command",
			line: "/help",
			want: mentionTrigger{Kind: mentionCommand, Query: "help", Start: 0},
		},
		{
			name: "mid-text slash in a path does not fire",
			line: "cd /usr/bin",
			want: mentionTrigger{Kind: mentionNone},
		},
		{
			name: "multiple at-mentions resolve to the last one before cursor",
			line: "@one @two",
			want: mentionTrigger{Kind: mentionFile, Query: "two", Start: 5},
		},
		{
			// A bare "@word.tld" after a stray space is structurally
			// identical to a genuine file mention (e.g. "@readme.md") and
			// cannot be told apart by shape alone; this is a named,
			// accepted simplification (see emailLike's doc comment) — the
			// mention UX favors not blocking real file references.
			name: "space then bare-domain @example.com is treated as a file mention",
			line: " @example.com",
			want: mentionTrigger{Kind: mentionFile, Query: "example.com", Start: 1},
		},
		{
			name: "mid-word at is not token-initial",
			line: "foo@bar",
			want: mentionTrigger{Kind: mentionNone},
		},
		{
			name: "whitespace after mention closes it",
			line: "@foo bar",
			want: mentionTrigger{Kind: mentionNone}, // cursor defaults to end-of-line in this table; see cursor-mid-token case below
		},
		{
			name: "bare at with empty query still fires",
			line: "@",
			want: mentionTrigger{Kind: mentionFile, Query: "", Start: 0},
		},
		{
			name: "no trigger characters at all",
			line: "just a plain instruction",
			want: mentionTrigger{Kind: mentionNone},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor := len([]rune(tt.line))
			got := detectTrigger(tt.line, cursor)
			if got != tt.want {
				t.Errorf("detectTrigger(%q, %d) = %+v, want %+v", tt.line, cursor, got, tt.want)
			}
		})
	}
}

// TestDetectTriggerCursorMidToken verifies the trigger stays active for a
// cursor sitting inside the mention token (not just at end-of-line), and
// closes once the cursor moves past trailing whitespace.
func TestDetectTriggerCursorMidToken(t *testing.T) {
	line := "@foo bar"
	// cursor after "@fo" (index 3): still mid-token, query is partial.
	got := detectTrigger(line, 3)
	want := mentionTrigger{Kind: mentionFile, Query: "fo", Start: 0}
	if got != want {
		t.Errorf("mid-token cursor: got %+v, want %+v", got, want)
	}
	// cursor after "@foo " (index 5, past the space): trigger has closed.
	got = detectTrigger(line, 5)
	if got.Kind != mentionNone {
		t.Errorf("cursor past whitespace: got %+v, want none", got)
	}
}

func TestDetectTriggerCommandStopsAtWhitespace(t *testing.T) {
	// "/mode build": cursor within "build" is past the command token's own
	// whitespace boundary, so it must not still be treated as a command
	// trigger (the operator is now typing an argument, not choosing a
	// command name).
	got := detectTrigger("/mode build", 11)
	if got.Kind != mentionNone {
		t.Errorf("got %+v, want none once past the command token", got)
	}
	// but mid-command-name it still fires.
	got = detectTrigger("/mo", 3)
	want := mentionTrigger{Kind: mentionCommand, Query: "mo", Start: 0}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCurrentLine(t *testing.T) {
	value := "first\nsecond\nthird"
	cases := []struct {
		row  int
		want string
	}{
		{0, "first"},
		{1, "second"},
		{2, "third"},
		{3, ""}, // out of range
		{-1, ""},
	}
	for _, c := range cases {
		if got := currentLine(value, c.row); got != c.want {
			t.Errorf("currentLine(_, %d) = %q, want %q", c.row, got, c.want)
		}
	}
}
