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
			// Tabs expand to a deterministic four-space stop: a raw tab byte
			// would defeat the cell-width math against the terminal's own
			// eight-column stops, and color profiles (whose lipgloss render
			// expands tabs implicitly) must match Mono cell for cell.
			name: "fenced code block is verbatim with tabs expanded",
			src:  "```go\nfmt.Println(\"你好\")\n\treturn\n```",
			want: []string{"fmt.Println(\"你好\")", "    return"},
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

// Math approximation (P3) applies to inline prose across profiles: the
// Unicode transform is plain text, so Mono keeps it; unrecognized spans stay
// verbatim; currency and inline code are never math.
func TestRenderMathApprox(t *testing.T) {
	th := theme.New(theme.Mono)
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "inline dollars transform",
			src:  "area $\\pi r^2$ done",
			want: []string{"area π r² done"},
		},
		{
			name: "escaped parens transform",
			src:  `so \(x^2 + y_1\) holds`,
			want: []string{"so x² + y₁ holds"},
		},
		{
			name: "display paragraph transforms",
			src:  "$$\nE = mc^2\n$$",
			want: []string{"E = mc²"},
		},
		{
			name: "currency stays text",
			src:  "costs $5 and $10 today",
			want: []string{"costs $5 and $10 today"},
		},
		{
			name: "escaped dollar stays text",
			src:  `pay \$x\$ now`,
			want: []string{"pay $x$ now"},
		},
		{
			name: "unrecognized math renders verbatim",
			src:  `check $\weird{x}$ later`,
			want: []string{`check $\weird{x}$ later`},
		},
		{
			name: "inline code is never math",
			src:  "run `$x^2$` now",
			want: []string{"run $x^2$ now"},
		},
		{
			name: "math inside a heading",
			src:  "## Energy $E = mc^2$",
			want: []string{"## Energy E = mc²"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.src, th, 80, "", passWrap)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Render(%q) = %#v, want %#v", tc.src, got, tc.want)
			}
		})
	}

	// Color profiles style both the approximation and the verbatim fallback
	// through RoleMathApprox: Dust Violet #c6a6ea = rgb(198,166,234).
	tru := theme.New(theme.TrueColor)
	math := strings.Join(Render("solve $x^2$ now", tru, 80, "", passWrap), "\n")
	if !strings.Contains(math, "38;2;198;166;234") {
		t.Errorf("TrueColor math must use Dust Violet: %q", math)
	}
	if got := ansi.Strip(math); got != "solve x² now" {
		t.Errorf("math plain text = %q", got)
	}
	fallback := strings.Join(Render(`odd $\weird{x}$ here`, tru, 80, "", passWrap), "\n")
	if !strings.Contains(fallback, "38;2;198;166;234") {
		t.Errorf("verbatim fallback must still style as math: %q", fallback)
	}
	if got := ansi.Strip(fallback); got != `odd $\weird{x}$ here` {
		t.Errorf("fallback must preserve the source: %q", got)
	}
}

func TestRenderDisplayMathFromAssistantMarkdown(t *testing.T) {
	src := "矩阵： $$ A = \\begin{pmatrix} 1 & 2 \\\\ 3 & 4 \\end{pmatrix} $$\n\n" +
		"分段： $$ f(x) = \\begin{cases} x^2, & x \\geq 0 \\\\ -x, & x < 0 \\end{cases} $$"
	got := strings.Join(Render(src, theme.New(theme.Mono), 80, "", passWrap), "\n")
	// Display math ($$…$$) is its own block: the label and each approximation
	// row land on their own transcript lines (matrices/cases carry newlines).
	for _, want := range []string{"矩阵：", "A = ⎛ 1  2 ⎞", "⎝ 3  4 ⎠", "分段：", "f(x) = ⎧ x²,  x ≥ 0", "⎩ -x,  x < 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("display math missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `\begin`) || strings.Contains(got, "$$") {
		t.Errorf("rendered display math leaked TeX delimiters:\n%s", got)
	}
}

// Display math is rendered as the Unicode approximation even when Kitty
// graphics are available: the go-tex image path inflates math-mode glyph
// spacing ~3-4x, so the approximation is the more legible surface. The image
// path (mathimage) remains wired for previews and for when go-tex is fixed.
func TestRenderDisplayMathPrefersUnicode(t *testing.T) {
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	got := Render("before\n\n$$\\frac{pixel_91}{\\sqrt{x}}$$\n\nafter", theme.New(theme.TrueColor), 80, "  ", passWrap)
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, `\frac`) || strings.Contains(joined, "\x1b_G") || strings.Contains(joined, "\U0010eeee") {
		t.Fatalf("display math leaked TeX or fell back to pixel placeholders: %q", joined)
	}
	if !strings.Contains(joined, "before") || !strings.Contains(joined, "after") || !strings.Contains(joined, "(pixel₉1)⁄(√x)") {
		t.Fatalf("display math did not render as Unicode approximation: %q", joined)
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

// When natural column widths exceed the width budget, the widest column
// shrinks and its cells clip with an ellipsis — alignment survives, the cell
// grid is respected, CJK width math included.
func TestTableColumnSizingClipsToBudget(t *testing.T) {
	th := theme.New(theme.Mono)
	src := "| name | description |\n|------|-------------|\n| a | 一二三四五 long text here |"
	got := Render(src, th, 20, "", passWrap)
	want := []string{
		"name | description  ",
		"-----+--------------",
		"a    | 一二三四五 l…",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Render = %#v, want %#v", got, want)
	}
	for _, line := range got {
		if w := ansi.StringWidth(line); w > 20 {
			t.Errorf("line overflows the budget (%d cells): %q", w, line)
		}
	}
}

// When even minimum-width columns cannot fit, the table transposes to
// key/value records (Codex-style fallback): one wrapped line per cell under
// its header, records separated by a blank line.
func TestTableTransposesWhenColumnsCannotFit(t *testing.T) {
	th := theme.New(theme.Mono)
	src := "| key | value |\n|---|---|\n| k1 | v1 |\n| k2 | v2 |"
	got := Render(src, th, 8, "", passWrap)
	want := []string{
		"key: k1",
		"value: v1",
		"",
		"key: k2",
		"value: v2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Render = %#v, want %#v", got, want)
	}

	// Color profiles keep the header cells bold in the transposed form too.
	colored := strings.Join(Render(src, theme.New(theme.ANSI256), 8, "", passWrap), "\n")
	if !strings.Contains(colored, "\x1b[1m") && !strings.Contains(colored, "\x1b[1;") {
		t.Errorf("transposed keys should stay bold: %q", colored)
	}
	if !strings.Contains(ansi.Strip(colored), "key: k1") {
		t.Errorf("transposed plain text lost: %q", ansi.Strip(colored))
	}
}

// A header-only table (a valid GFM table, and the exact shape the streaming
// holdback renders while data rows are still in flight) must not vanish when
// the width budget forces the transpose fallback: the header cells render one
// wrapped line each, so no source is lost to the right edge.
func TestHeaderOnlyTableSurvivesTranspose(t *testing.T) {
	th := theme.New(theme.Mono)
	src := "| column-aaaaaaaaaaaaaaa | column-bbbbbbbbbbbbbbb | column-ccccccccccccccc |\n|---|---|---|\n"
	got := Render(src, th, 10, "", passWrap)
	want := []string{
		"column-aaaaaaaaaaaaaaa",
		"column-bbbbbbbbbbbbbbb",
		"column-ccccccccccccccc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Render = %#v, want %#v", got, want)
	}

	// Color profiles keep the transposed header cells bold, same as the
	// data-bearing transpose form.
	colored := strings.Join(Render(src, theme.New(theme.ANSI256), 10, "", passWrap), "\n")
	if !strings.Contains(colored, "\x1b[1m") && !strings.Contains(colored, "\x1b[1;") {
		t.Errorf("header-only transpose should keep bold headers: %q", colored)
	}
	if !strings.Contains(ansi.Strip(colored), "column-aaaaaaaaaaaaaaa") {
		t.Errorf("header text lost: %q", ansi.Strip(colored))
	}
}

// Tab handling must not diverge between the chroma path, the plain codeLines
// fallback, and Mono: every path expands tabs to the same four-space stop, so
// the same block indents identically under NO_COLOR and color profiles and no
// raw tab byte reaches the cell grid.
func TestCodeTabsExpandDeterministically(t *testing.T) {
	srcKnown := "```go\n\tprintln(\"你好\")\n```"
	srcUnknown := "```zzzunknownlang\n\tprintln(\"你好\")\n```"
	for _, tc := range []struct {
		name string
		src  string
		th   theme.Theme
	}{
		{"mono known language", srcKnown, theme.New(theme.Mono)},
		{"mono unknown language", srcUnknown, theme.New(theme.Mono)},
		{"ansi256 chroma path", srcKnown, theme.New(theme.ANSI256)},
		{"ansi256 codeLines fallback", srcUnknown, theme.New(theme.ANSI256)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.src, tc.th, 80, "", passWrap)
			if len(got) != 1 {
				t.Fatalf("Render = %#v, want one line", got)
			}
			plain := ansi.Strip(got[0])
			if plain != "    println(\"你好\")" {
				t.Errorf("plain text = %q, want four-space indent", plain)
			}
			if strings.Contains(got[0], "\t") {
				t.Errorf("raw tab byte leaked into the rendered line: %q", got[0])
			}
		})
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
