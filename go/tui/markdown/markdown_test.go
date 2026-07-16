package markdown

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

// passWrap keeps golden expectations line-oriented: real soft wrapping is
// wrapText's contract, golden-tested in go/tui. Here it only applies the
// indents the renderer hands over.
func passWrap(s string, _ int, initial, _ string) []string {
	return []string{initial + s}
}

// Golden rendering per element class, Mono profile: the NO_COLOR contract
// means the plain-text shape of every construct is exact and escape-free.
func TestRenderMonoGolden(t *testing.T) {
	th := theme.New(theme.Mono)
	cases := []struct {
		name   string
		src    string
		width  int
		indent string
		want   []string
	}{
		{
			name: "heading tiers keep their markers",
			src:  "# One\n\n## Two\n\n#### Four",
			want: []string{"# One", "", "## Two", "", "#### Four"},
		},
		{
			name: "emphasis strikethrough and inline code flatten to plain text",
			src:  "mix *em* **strong** ~~gone~~ `x := 1` end",
			want: []string{"mix em strong gone x := 1 end"},
		},
		{
			name: "link shows destination in parentheses",
			src:  "see [docs](https://example.com/a) now",
			want: []string{"see docs (https://example.com/a) now"},
		},
		{
			name: "autolink is not doubled",
			src:  "go to <https://example.com/a>",
			want: []string{"go to https://example.com/a"},
		},
		{
			name: "tight and nested lists",
			src:  "- alpha\n- beta\n  - gamma",
			want: []string{"- alpha", "- beta", "  - gamma"},
		},
		{
			name: "ordered list honors start",
			src:  "3. three\n4. four",
			want: []string{"3. three", "4. four"},
		},
		{
			name: "blockquote prefixes every line",
			src:  "> quoted 引用\n>\n> more",
			want: []string{"> quoted 引用", "> ", "> more"},
		},
		{
			name: "fenced code block is verbatim",
			src:  "```go\nfmt.Println(\"你好\")\n\treturn\n```",
			want: []string{"fmt.Println(\"你好\")", "\treturn"},
		},
		{
			name:  "thematic break fills the width",
			src:   "---",
			width: 10,
			want:  []string{"----------"},
		},
		{
			name: "cjk table aligns by cell width",
			src:  "| 名称 | Val |\n|:-----|----:|\n| 长名字 | 2 |\n| b | 10 |",
			want: []string{
				"名称   | Val",
				"-------+----",
				"长名字 |   2",
				"b      |  10",
			},
		},
		{
			name: "markdown fence with a table unwraps",
			src:  "```markdown\n| A | B |\n|---|---|\n| 1 | 2 |\n```",
			want: []string{
				"A | B",
				"--+--",
				"1 | 2",
			},
		},
		{
			name: "code fence with pipes stays code",
			src:  "```go\n| A | B |\n|---|---|\n```",
			want: []string{"| A | B |", "|---|---|"},
		},
		{
			name: "hard line break splits the paragraph",
			src:  "line one  \nline two",
			want: []string{"line one", "line two"},
		},
		{
			name:   "indent prefixes every block line",
			src:    "para\n\n- item",
			indent: "  ",
			want:   []string{"  para", "  ", "  - item"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			width := tc.width
			if width == 0 {
				width = 80
			}
			got := Render(tc.src, th, width, tc.indent, passWrap)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Render(%q) = %#v, want %#v", tc.src, got, tc.want)
			}
			for i, line := range got {
				if strings.Contains(line, "\x1b") {
					t.Errorf("Mono line %d carries an escape sequence: %q", i, line)
				}
			}
		})
	}
}

// Color profiles style through theme roles only: heading color is Ion Cyan,
// inline code Copper Amber, links Oxygen Blue with an OSC 8 hyperlink.
func TestRenderStylesThroughThemeRoles(t *testing.T) {
	tru := theme.New(theme.TrueColor)
	a256 := theme.New(theme.ANSI256)

	heading := strings.Join(Render("# 标题 Title", tru, 80, "", passWrap), "\n")
	// Ion Cyan #8edbd2 = rgb(142,219,210); H1 is bold+underline, attribute tier.
	for _, want := range []string{"38;2;142;219;210", "1;", "4;"} {
		if !strings.Contains(heading, want) {
			t.Errorf("TrueColor H1 missing %q: %q", want, heading)
		}
	}
	if got := ansi.Strip(heading); got != "# 标题 Title" {
		t.Errorf("H1 plain text = %q", got)
	}

	code := strings.Join(Render("run `go test` now", a256, 80, "", passWrap), "\n")
	if !strings.Contains(code, "38;5;179") {
		t.Errorf("ANSI256 inline code must use Copper Amber 179: %q", code)
	}

	link := strings.Join(Render("[docs](https://example.com/a)", tru, 80, "", passWrap), "\n")
	if !strings.Contains(link, "\x1b]8;;https://example.com/a\x1b\\") || !strings.Contains(link, "\x1b]8;;\x1b\\") {
		t.Errorf("link must open and close an OSC 8 hyperlink: %q", link)
	}
	// Oxygen Blue #78bff2 = rgb(120,191,242), underlined per RoleLink.
	for _, want := range []string{"38;2;120;191;242", "4;"} {
		if !strings.Contains(link, want) {
			t.Errorf("link missing %q: %q", want, link)
		}
	}
	if !strings.Contains(ansi.Strip(link), "docs") {
		t.Errorf("link label lost: %q", link)
	}
	if strings.Contains(ansi.Strip(link), "(https://example.com/a)") {
		t.Errorf("color profiles must not duplicate the destination: %q", link)
	}

	marker := strings.Join(Render("- item", a256, 80, "", passWrap), "\n")
	if !strings.Contains(marker, "38;5;116") || !strings.Contains(ansi.Strip(marker), "• item") {
		t.Errorf("list marker must be Ion Cyan 116 with a bullet: %q", marker)
	}

	quote := strings.Join(Render("> 引用", tru, 80, "", passWrap), "\n")
	if !strings.Contains(quote, "▌") || ansi.Strip(quote) != "▌ 引用" {
		t.Errorf("blockquote glyph missing: %q", quote)
	}

	table := strings.Join(Render("| A | B |\n|---|---|\n| 1 | 2 |", tru, 80, "", passWrap), "\n")
	// Border #344144 = rgb(52,65,68) on the separators, bold header cells.
	for _, want := range []string{"38;2;52;65;68", "│", "┼", "\x1b[1m"} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q: %q", want, table)
		}
	}
}

// The renderer hands prose to the wrapper with the hanging indents that keep
// list continuations aligned under their text, not under the marker.
func TestListContinuationIndent(t *testing.T) {
	th := theme.New(theme.Mono)
	var inits, rests []string
	spy := func(s string, w int, initial, rest string) []string {
		inits, rests = append(inits, initial), append(rests, rest)
		return []string{initial + s}
	}
	Render("- item one\n  - nested", th, 20, " ", spy)
	wantInits := []string{" - ", "   - "}
	wantRests := []string{"   ", "     "}
	if !reflect.DeepEqual(inits, wantInits) || !reflect.DeepEqual(rests, wantRests) {
		t.Errorf("wrap indents = %#v/%#v, want %#v/%#v", inits, rests, wantInits, wantRests)
	}
}

// Deterministic pure rendering: same source + theme + width → same lines.
func TestRenderIsDeterministic(t *testing.T) {
	src := "# T\n\npara *em* [l](https://a.io)\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\ncode\n```"
	th := theme.New(theme.TrueColor)
	first := Render(src, th, 60, "  ", passWrap)
	for i := 0; i < 3; i++ {
		if got := Render(src, th, 60, "  ", passWrap); !reflect.DeepEqual(got, first) {
			t.Fatalf("render %d diverged:\n%v\nvs\n%v", i, got, first)
		}
	}
}

// A hostile destination cannot smuggle bytes into the OSC 8 payload: control
// characters and spaces are dropped from the renderer-emitted hyperlink.
func TestLinkDestinationIsSanitized(t *testing.T) {
	th := theme.New(theme.TrueColor)
	out := strings.Join(Render("[x](https://a.io/p\x07q)", th, 80, "", passWrap), "\n")
	if !strings.Contains(out, "\x1b]8;;https://a.io/pq\x1b\\") {
		t.Errorf("destination not sanitized: %q", out)
	}
}
