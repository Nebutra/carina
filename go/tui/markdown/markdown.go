// Package markdown renders CommonMark + GFM (tables, strikethrough — nothing
// else) source into terminal lines for the transcript. It is a custom
// goldmark AST walker rather than glamour: every color flows through the
// semantic theme roles in go/tui/theme, so the Mono profile degrades to plain
// readable text and no styling can bypass the design-token system.
//
// The renderer is a pure function: same sanitized source + same theme + same
// width → same lines. It never emits escape sequences taken from the source —
// the caller sanitizes inbound text first, and all SGR/OSC output here is
// renderer-generated (OSC 8 hyperlinks included, per the rich-text plan).
package markdown

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"github.com/Nebutra/carina/go/tui/theme"
)

// WrapFunc soft-wraps one logical (possibly styled) line to a cell width with
// an initial and a continuation indent. The tui package passes its wrapText so
// markdown prose reflows on the exact same grapheme/CJK width path as every
// other transcript line.
type WrapFunc func(s string, width int, initialIndent, subsequentIndent string) []string

// parser is configured once: CommonMark plus the two GFM extensions the plan
// allows. goldmark parsers are safe for reuse across Parse calls.
var parser = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough))

// Render converts markdown source into styled terminal lines. Prose blocks
// wrap to width via wrap; structured blocks (code, tables, rules) render
// line-oriented and rely on the outer cell-grid boundary for clipping, exactly
// like the transcript's other structured bodies. indent prefixes every line.
func Render(source string, th theme.Theme, width int, indent string, wrap WrapFunc) []string {
	if width < 1 {
		width = 1
	}
	source = unwrapMarkdownFences(source)
	src := []byte(source)
	doc := parser.Parser().Parse(text.NewReader(src))
	r := renderer{src: src, th: th, width: width, wrap: wrap}
	lines := r.children(doc, indent, indent, false)
	if len(lines) == 0 {
		return nil
	}
	return lines
}

type renderer struct {
	src   []byte
	th    theme.Theme
	width int
	wrap  WrapFunc
}

// ictx is the inline style context accumulated while walking inline nodes.
// Each text segment renders with one composed style, so nested spans (bold
// inside a link inside a heading) never fight over SGR resets.
type ictx struct {
	role      theme.Role
	hasRole   bool
	bold      bool
	italic    bool
	strike    bool
	underline bool
}

func (r *renderer) mono() bool { return r.th.Profile() == theme.Mono }

// styled renders one text segment under the composed context. Mono emits the
// text verbatim: no color, no attributes — the NO_COLOR contract.
func (r *renderer) styled(seg string, c ictx) string {
	if seg == "" || r.mono() {
		return seg
	}
	s := lipgloss.NewStyle()
	if c.hasRole {
		s = r.th.Style(c.role)
	}
	if c.bold {
		s = s.Bold(true)
	}
	if c.italic {
		s = s.Italic(true)
	}
	if c.strike {
		s = s.Strikethrough(true)
	}
	if c.underline {
		s = s.Underline(true)
	}
	return s.Render(seg)
}

func roleCtx(role theme.Role) ictx { return ictx{role: role, hasRole: true} }

// children renders the block children of parent, separating sibling blocks
// with one blank line unless the parent is a tight list item. The separator
// carries the continuation indent so a blockquote bar spans its blank lines.
func (r *renderer) children(parent ast.Node, first, rest string, tight bool) []string {
	var out []string
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		b := r.block(c, first, rest)
		if len(b) == 0 {
			continue
		}
		if len(out) > 0 && !tight {
			out = append(out, rest)
		}
		out = append(out, b...)
		first = rest
	}
	return out
}

func (r *renderer) block(n ast.Node, first, rest string) []string {
	switch t := n.(type) {
	case *ast.Heading:
		c := headingCtx(t.Level)
		s := r.styled(strings.Repeat("#", t.Level)+" ", c) + r.inlines(t, c)
		return r.wrapSegments(s, first, rest)
	case *ast.Paragraph, *ast.TextBlock:
		return r.wrapSegments(r.inlines(n, ictx{}), first, rest)
	case *ast.Blockquote:
		glyph := "▌ "
		if r.mono() {
			glyph = "> "
		}
		prefix := r.styled(glyph, roleCtx(theme.RoleBlockquote))
		return r.children(t, first+prefix, rest+prefix, false)
	case *ast.List:
		return r.list(t, first, rest)
	case *ast.FencedCodeBlock:
		return r.codeLines(t.Lines(), first, rest)
	case *ast.CodeBlock:
		return r.codeLines(t.Lines(), first, rest)
	case *ast.ThematicBreak:
		glyph := "─"
		if r.mono() {
			glyph = "-"
		}
		n := maxInt(r.width-ansi.StringWidth(first), 1)
		return []string{first + r.styled(strings.Repeat(glyph, n), roleCtx(theme.RoleTableBorder))}
	case *extast.Table:
		return r.table(t, first, rest)
	case *ast.HTMLBlock:
		// Raw HTML has no terminal semantics; keep it legible as muted text.
		return r.rawLines(t.Lines(), first, rest)
	default:
		if n.Type() == ast.TypeBlock {
			return r.children(n, first, rest, false)
		}
		return nil
	}
}

// headingCtx implements the attribute-only heading tiers (Codex house style):
// H1 bold+underline, H2 bold, H3 bold+italic, H4+ italic. The "#" markers stay
// in the output so Mono headings remain visually distinct as plain text.
func headingCtx(level int) ictx {
	c := roleCtx(theme.RoleHeading)
	switch {
	case level <= 1:
		c.bold, c.underline = true, true
	case level == 2:
		c.bold = true
	case level == 3:
		c.bold, c.italic = true, true
	default:
		c.italic = true
	}
	return c
}

// wrapSegments splits hard line breaks and soft-wraps each segment. The
// renderer only ever passes renderer-emitted styles into wrap, keeping the
// sanitize boundary intact.
func (r *renderer) wrapSegments(s string, first, rest string) []string {
	var out []string
	for i, seg := range strings.Split(s, "\n") {
		initial := rest
		if i == 0 {
			initial = first
		}
		if r.wrap != nil {
			out = append(out, r.wrap(seg, r.width, initial, rest)...)
		} else {
			out = append(out, initial+seg)
		}
	}
	return out
}

func (r *renderer) list(l *ast.List, first, rest string) []string {
	var out []string
	index := l.Start
	for it := l.FirstChild(); it != nil; it = it.NextSibling() {
		marker := "• "
		if r.mono() {
			marker = "- "
		}
		if l.IsOrdered() {
			marker = fmt.Sprintf("%d. ", index)
			index++
		}
		itemFirst := first
		if len(out) > 0 {
			itemFirst = rest
		}
		cont := rest + strings.Repeat(" ", ansi.StringWidth(marker))
		item := r.children(it, itemFirst+r.styled(marker, roleCtx(theme.RoleListMarker)), cont, l.IsTight)
		if len(out) > 0 && len(item) > 0 && !l.IsTight {
			out = append(out, "")
		}
		out = append(out, item...)
	}
	return out
}

// codeLines renders a code block verbatim, one styled line per source line.
// Code is never soft-wrapped: alignment is meaning; overflow is clipped by
// the outer cell grid like every other structured transcript body.
// Highlighting is milestone P2; here the whole block is RoleCodeBlock.
func (r *renderer) codeLines(lines *text.Segments, first, rest string) []string {
	out := make([]string, 0, lines.Len())
	indent := first
	for i := 0; i < lines.Len(); i++ {
		ln := strings.TrimRight(string(segValue(lines.At(i), r.src)), "\n")
		out = append(out, indent+r.styled(ln, roleCtx(theme.RoleCodeBlock)))
		indent = rest
	}
	return out
}

func (r *renderer) rawLines(lines *text.Segments, first, rest string) []string {
	out := make([]string, 0, lines.Len())
	indent := first
	for i := 0; i < lines.Len(); i++ {
		ln := strings.TrimRight(string(segValue(lines.At(i), r.src)), "\n")
		out = append(out, indent+r.styled(ln, roleCtx(theme.RoleBlockquote)))
		indent = rest
	}
	return out
}

// inlines walks inline children and returns one styled logical line; hard
// line breaks become "\n" for wrapSegments to split.
func (r *renderer) inlines(n ast.Node, c ictx) string {
	var b strings.Builder
	// Consecutive plain text nodes (goldmark splits link labels finely)
	// coalesce into one styled run so the output is not per-rune SGR noise.
	var pending strings.Builder
	flush := func() {
		if pending.Len() > 0 {
			b.WriteString(r.styled(pending.String(), c))
			pending.Reset()
		}
	}
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch t := child.(type) {
		case *ast.Text:
			pending.Write(t.Segment.Value(r.src))
			if t.HardLineBreak() {
				flush()
				b.WriteString("\n")
			} else if t.SoftLineBreak() {
				pending.WriteString(" ")
			}
			continue
		case *ast.String:
			pending.Write(t.Value)
			continue
		}
		flush()
		switch t := child.(type) {
		case *ast.CodeSpan:
			code := c
			code.role, code.hasRole = theme.RoleCodeInline, true
			b.WriteString(r.inlines(child, code))
		case *ast.Emphasis:
			e := c
			if t.Level >= 2 {
				e.bold = true
			} else {
				e.italic = true
			}
			b.WriteString(r.inlines(child, e))
		case *extast.Strikethrough:
			e := c
			e.strike = true
			b.WriteString(r.inlines(child, e))
		case *ast.Link:
			b.WriteString(r.link(string(t.Destination), r.linkLabel(child, c), c))
		case *ast.Image:
			b.WriteString(r.link(string(t.Destination), r.linkLabel(child, c), c))
		case *ast.AutoLink:
			url := string(t.URL(r.src))
			label := c
			label.role, label.hasRole, label.underline = theme.RoleLink, true, true
			b.WriteString(r.link(url, r.styled(string(t.Label(r.src)), label), c))
		case *ast.RawHTML:
			for i := 0; i < t.Segments.Len(); i++ {
				b.WriteString(r.styled(string(segValue(t.Segments.At(i), r.src)), c))
			}
		default:
			b.WriteString(r.inlines(child, c))
		}
	}
	flush()
	return b.String()
}

func (r *renderer) linkLabel(n ast.Node, c ictx) string {
	l := c
	l.role, l.hasRole, l.underline = theme.RoleLink, true, true
	return r.inlines(n, l)
}

// link emits the styled label wrapped in an OSC 8 hyperlink. The hyperlink
// escape is renderer-generated — never taken from inbound text — and Mono
// stays escape-free by appending the destination in parentheses instead.
func (r *renderer) link(dest, label string, c ictx) string {
	dest = sanitizeDestination(dest)
	if dest == "" {
		return label
	}
	if r.mono() {
		if ansi.Strip(label) == dest {
			return label
		}
		return label + " (" + dest + ")"
	}
	return "\x1b]8;;" + dest + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

// sanitizeDestination keeps the OSC 8 payload well-formed: control bytes and
// spaces (which would also confuse the word wrapper) are percent-stripped.
func sanitizeDestination(dest string) string {
	return strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, dest)
}

// table renders a GFM table with natural column widths measured on the same
// escape-aware cell-width path as the rest of the renderer, so CJK cells
// align. Width-budgeted column sizing and transposition are milestone P2; an
// overflowing table clips at the outer cell grid like other structured rows.
func (r *renderer) table(t *extast.Table, first, rest string) []string {
	v, h, x := "│", "─", "┼"
	if r.mono() {
		v, h, x = "|", "-", "+"
	}
	var rows [][]string
	headerRows := 0
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		header := false
		switch row.(type) {
		case *extast.TableHeader:
			header = true
		case *extast.TableRow:
		default:
			continue
		}
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			c := ictx{}
			if header {
				c.bold = true
			}
			cells = append(cells, r.inlines(cell, c))
		}
		if header {
			headerRows = len(rows) + 1
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return nil
	}
	widths := make([]int, 0, len(t.Alignments))
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = maxInt(widths[i], ansi.StringWidth(cell))
		}
	}
	sep := r.styled(" "+v+" ", roleCtx(theme.RoleTableBorder))
	var out []string
	indent := first
	for ri, row := range rows {
		cols := make([]string, len(widths))
		for i := range widths {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			align := extast.AlignNone
			if i < len(t.Alignments) {
				align = t.Alignments[i]
			}
			cols[i] = padCell(cell, widths[i], align)
		}
		out = append(out, indent+strings.Join(cols, sep))
		indent = rest
		if ri == headerRows-1 {
			rule := make([]string, len(widths))
			for i, w := range widths {
				rule[i] = strings.Repeat(h, w)
			}
			out = append(out, indent+r.styled(strings.Join(rule, h+x+h), roleCtx(theme.RoleTableBorder)))
		}
	}
	return out
}

func padCell(cell string, width int, align extast.Alignment) string {
	pad := width - ansi.StringWidth(cell)
	if pad <= 0 {
		return cell
	}
	switch align {
	case extast.AlignRight:
		return strings.Repeat(" ", pad) + cell
	case extast.AlignCenter:
		left := pad / 2
		return strings.Repeat(" ", left) + cell + strings.Repeat(" ", pad-left)
	default:
		return cell + strings.Repeat(" ", pad)
	}
}

// segValue reads a segment's bytes; text.Segments.At returns a value while
// Segment.Value has a pointer receiver, so take the address here once.
func segValue(seg text.Segment, src []byte) []byte {
	return seg.Value(src)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
