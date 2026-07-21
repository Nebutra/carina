package mathapprox

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Approx converts one TeX-subset formula (delimiters already stripped) to its
// Unicode approximation. ok is false when any construct falls outside the
// subset — an unknown command, a script character with no super/subscript
// codepoint, unbalanced braces — and the caller then renders the source
// verbatim, so a formula is either re-spelled exactly or left untouched.
func Approx(tex string) (string, bool) {
	var ok bool
	tex, ok = expandDisplayEnvironments(tex)
	if !ok {
		return "", false
	}
	p := &parser{src: tex}
	out, ok := p.sequence(0)
	if !ok {
		return "", false
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", false
	}
	return out, true
}

// expandDisplayEnvironments lowers the small set of structural math
// environments Carina can represent faithfully in terminal cells. Newlines
// survive the inline walker and become physical transcript rows. Unknown or
// unclosed environments fail closed so their original TeX remains visible.
func expandDisplayEnvironments(tex string) (string, bool) {
	for {
		start := strings.Index(tex, `\begin{`)
		if start < 0 {
			return tex, true
		}
		nameStart := start + len(`\begin{`)
		nameEndRel := strings.IndexByte(tex[nameStart:], '}')
		if nameEndRel < 0 {
			return "", false
		}
		nameEnd := nameStart + nameEndRel
		name := tex[nameStart:nameEnd]
		close := `\end{` + name + `}`
		bodyStart := nameEnd + 1
		bodyEndRel := strings.Index(tex[bodyStart:], close)
		if bodyEndRel < 0 {
			return "", false
		}
		bodyEnd := bodyStart + bodyEndRel
		replacement, ok := renderEnvironment(name, tex[bodyStart:bodyEnd])
		if !ok {
			return "", false
		}
		tex = tex[:start] + replacement + tex[bodyEnd+len(close):]
	}
}

func renderEnvironment(name, body string) (string, bool) {
	rows := splitEnvironmentRows(body)
	if len(rows) == 0 {
		return "", false
	}
	switch name {
	case "pmatrix", "bmatrix", "matrix":
		cells := make([][]string, 0, len(rows))
		widths := []int{}
		for _, row := range rows {
			parts := strings.Split(row, "&")
			converted := make([]string, 0, len(parts))
			for i, part := range parts {
				cell, ok := Approx(strings.TrimSpace(part))
				if !ok {
					return "", false
				}
				converted = append(converted, cell)
				for len(widths) <= i {
					widths = append(widths, 0)
				}
				widths[i] = maxInt(widths[i], utf8.RuneCountInString(cell))
			}
			cells = append(cells, converted)
		}
		lines := make([]string, 0, len(cells))
		for i, row := range cells {
			left, right := "│", "│"
			if name == "pmatrix" {
				left, right = matrixParens(i, len(cells))
			} else if name == "bmatrix" {
				left, right = matrixBrackets(i, len(cells))
			}
			for j := range row {
				row[j] += strings.Repeat(" ", widths[j]-utf8.RuneCountInString(row[j]))
			}
			lines = append(lines, left+" "+strings.Join(row, "  ")+" "+right)
		}
		return strings.Join(lines, "\n"), true
	case "aligned", "align", "align*":
		lines := make([]string, 0, len(rows))
		for _, row := range rows {
			line, ok := Approx(strings.ReplaceAll(row, "&", ""))
			if !ok {
				return "", false
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n"), true
	case "cases":
		lines := make([]string, 0, len(rows))
		for i, row := range rows {
			parts := strings.SplitN(row, "&", 2)
			value, ok := Approx(strings.TrimSpace(parts[0]))
			if !ok {
				return "", false
			}
			condition := ""
			if len(parts) == 2 {
				condition, ok = Approx(strings.TrimSpace(parts[1]))
				if !ok {
					return "", false
				}
			}
			brace := "⎨"
			if i == 0 {
				brace = "⎧"
			} else if i == len(rows)-1 {
				brace = "⎩"
			}
			lines = append(lines, strings.TrimRight(fmt.Sprintf("%s %s  %s", brace, value, condition), " "))
		}
		return strings.Join(lines, "\n"), true
	default:
		return "", false
	}
}

func splitEnvironmentRows(body string) []string {
	parts := strings.Split(strings.TrimSpace(body), `\\`)
	rows := make([]string, 0, len(parts))
	for _, part := range parts {
		row := strings.TrimSpace(part)
		if strings.HasPrefix(row, "[") {
			if end := strings.IndexByte(row, ']'); end > 0 {
				row = strings.TrimSpace(row[end+1:])
			}
		}
		if row != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

func matrixParens(row, total int) (string, string) {
	if total == 1 {
		return "(", ")"
	}
	if row == 0 {
		return "⎛", "⎞"
	}
	if row == total-1 {
		return "⎝", "⎠"
	}
	return "⎜", "⎟"
}

func matrixBrackets(row, total int) (string, string) {
	if total == 1 {
		return "[", "]"
	}
	if row == 0 {
		return "⎡", "⎤"
	}
	if row == total-1 {
		return "⎣", "⎦"
	}
	return "⎢", "⎥"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// parser is a recursive-descent walker over the formula bytes. All dispatch
// characters are ASCII, so byte indexing is safe; runs of ordinary text copy
// through rune-wise (CJK prose inside \text{} included).
type parser struct {
	src string
	pos int
}

// sequence transforms elements until the closing delimiter byte (0 = end of
// input). It leaves the delimiter unconsumed for the caller.
func (p *parser) sequence(until byte) (string, bool) {
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if until != 0 && c == until {
			return b.String(), true
		}
		switch c {
		case '}':
			return "", false // unbalanced close
		case '{':
			s, ok := p.group()
			if !ok {
				return "", false
			}
			b.WriteString(s)
		case '\\':
			s, ok := p.command()
			if !ok {
				return "", false
			}
			b.WriteString(s)
		case '^':
			p.pos++
			s, ok := p.script(superscript, '^')
			if !ok {
				return "", false
			}
			b.WriteString(s)
		case '_':
			p.pos++
			s, ok := p.script(subscript, '_')
			if !ok {
				return "", false
			}
			b.WriteString(s)
		case '~': // TeX non-breaking space
			p.pos++
			b.WriteByte(' ')
		default:
			r, n := utf8.DecodeRuneInString(p.src[p.pos:])
			p.pos += n
			b.WriteRune(r)
		}
	}
	if until != 0 {
		return "", false // unterminated group
	}
	return b.String(), true
}

// group transforms one brace-delimited group, consuming both braces.
func (p *parser) group() (string, bool) {
	if p.pos >= len(p.src) || p.src[p.pos] != '{' {
		return "", false
	}
	p.pos++
	s, ok := p.sequence('}')
	if !ok {
		return "", false
	}
	p.pos++
	return s, true
}

// argument parses one macro argument: a {group}, a \command, or a single rune
// — the TeX argument forms the subset supports (`x^2`, `x^{10}`, `90^\circ`).
func (p *parser) argument() (string, bool) {
	if p.pos >= len(p.src) {
		return "", false
	}
	switch p.src[p.pos] {
	case '{':
		return p.group()
	case '\\':
		return p.command()
	}
	r, n := utf8.DecodeRuneInString(p.src[p.pos:])
	p.pos += n
	return string(r), true
}

// script transforms a ^ or _ argument by mapping every rune of its (already
// transformed) text through the super/subscript table. One unmappable rune
// fails the whole formula: a half-raised exponent would misread.
func (p *parser) script(table map[rune]rune, marker rune) (string, bool) {
	arg, ok := p.argument()
	if !ok {
		return "", false
	}
	var b strings.Builder
	for _, r := range arg {
		m, ok := table[r]
		if !ok {
			// Unicode has no complete super/subscript alphabet (notably most
			// Greek letters and ∞). Keep the formula rendered and unambiguous
			// with explicit baseline notation instead of rejecting the entire
			// math span back to raw TeX.
			return string(marker) + "(" + arg + ")", true
		}
		b.WriteRune(m)
	}
	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

// command transforms one backslash form: an escaped character, a spacing
// command, or a named macro. Unknown names fail — verbatim fallback.
func (p *parser) command() (string, bool) {
	p.pos++ // the backslash
	if p.pos >= len(p.src) {
		return "", false
	}
	if c := p.src[p.pos]; !isLetter(c) {
		p.pos++
		switch c {
		case '{', '}', '$', '%', '&', '#', '_':
			return string(c), true
		case '\\', ' ', ',', ':', ';': // line break and spacing → one cell
			return " ", true
		case '!': // negative thin space → nothing
			return "", true
		case '|':
			return "‖", true
		}
		return "", false
	}
	start := p.pos
	for p.pos < len(p.src) && isLetter(p.src[p.pos]) {
		p.pos++
	}
	switch name := p.src[start:p.pos]; name {
	case "frac", "dfrac", "tfrac", "cfrac":
		num, ok := p.group()
		if !ok {
			return "", false
		}
		den, ok := p.group()
		if !ok {
			return "", false
		}
		return operand(num) + "⁄" + operand(den), true
	case "binom", "dbinom", "tbinom":
		top, ok := p.group()
		if !ok {
			return "", false
		}
		bottom, ok := p.group()
		if !ok {
			return "", false
		}
		return "C(" + top + "," + bottom + ")", true
	case "sqrt":
		degree := ""
		if p.pos < len(p.src) && p.src[p.pos] == '[' {
			p.pos++
			idx, ok := p.sequence(']')
			if !ok {
				return "", false
			}
			p.pos++
			var b strings.Builder
			for _, r := range idx {
				m, ok := superscript[r]
				if !ok {
					return "", false
				}
				b.WriteRune(m)
			}
			degree = b.String()
		}
		arg, ok := p.argument()
		if !ok {
			return "", false
		}
		return degree + "√" + operand(arg), true
	case "text", "textbf", "textit", "mathrm", "mathbf", "mathit", "mathsf", "mathcal", "mathbb", "operatorname":
		// Face selection has no cell representation; the contents stand.
		return p.group()
	case "hat", "widehat":
		arg, ok := p.argument()
		if !ok {
			return "", false
		}
		return arg + "̂", true
	case "bar", "overline":
		arg, ok := p.argument()
		if !ok {
			return "", false
		}
		return arg + "̄", true
	case "vec":
		arg, ok := p.argument()
		if !ok {
			return "", false
		}
		return arg + "⃗", true
	case "underline":
		arg, ok := p.argument()
		if !ok {
			return "", false
		}
		return arg + "̲", true
	case "left", "right":
		// Sizing hint; the delimiter itself follows. "." is the invisible one.
		if p.pos < len(p.src) && p.src[p.pos] == '.' {
			p.pos++
		}
		return "", true
	case "quad":
		return "  ", true
	case "qquad":
		return "    ", true
	default:
		if sym, ok := symbols[name]; ok {
			return sym, true
		}
		return "", false
	}
}

// operand parenthesizes a multi-rune fraction or radical operand so
// `\frac{a+b}{2}` reads (a+b)⁄2 — never the misparsable a+b⁄2.
func operand(s string) string {
	if utf8.RuneCountInString(s) <= 1 {
		return s
	}
	return "(" + s + ")"
}

func isLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// symbols maps macro names to their Unicode spelling: Greek letters, operator
// and relation glyphs, arrows, delimiters, and the roman function names
// (\sin → "sin"). Names outside this table fail the formula.
var symbols = map[string]string{
	// Greek, lowercase.
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ",
	"epsilon": "ε", "varepsilon": "ε", "zeta": "ζ", "eta": "η",
	"theta": "θ", "vartheta": "ϑ", "iota": "ι", "kappa": "κ",
	"lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ",
	"pi": "π", "varpi": "ϖ", "rho": "ρ", "varrho": "ϱ",
	"sigma": "σ", "varsigma": "ς", "tau": "τ", "upsilon": "υ",
	"phi": "ϕ", "varphi": "φ", "chi": "χ", "psi": "ψ", "omega": "ω",
	// Greek, uppercase.
	"Gamma": "Γ", "Delta": "Δ", "Theta": "Θ", "Lambda": "Λ",
	"Xi": "Ξ", "Pi": "Π", "Sigma": "Σ", "Upsilon": "Υ",
	"Phi": "Φ", "Psi": "Ψ", "Omega": "Ω",
	// Binary operators.
	"times": "×", "cdot": "⋅", "div": "÷", "pm": "±", "mp": "∓",
	"ast": "∗", "star": "⋆", "circ": "∘", "bullet": "•",
	"oplus": "⊕", "ominus": "⊖", "otimes": "⊗", "oslash": "⊘",
	"cup": "∪", "cap": "∩", "setminus": "∖", "wedge": "∧", "land": "∧",
	"vee": "∨", "lor": "∨", "neg": "¬", "lnot": "¬",
	// Relations.
	"leq": "≤", "le": "≤", "geq": "≥", "ge": "≥", "neq": "≠", "ne": "≠",
	"approx": "≈", "equiv": "≡", "sim": "∼", "simeq": "≃", "cong": "≅",
	"propto": "∝", "ll": "≪", "gg": "≫", "prec": "≺", "succ": "≻",
	"in": "∈", "notin": "∉", "ni": "∋",
	"subset": "⊂", "supset": "⊃", "subseteq": "⊆", "supseteq": "⊇",
	"vdash": "⊢", "dashv": "⊣", "models": "⊨", "perp": "⊥", "parallel": "∥",
	"mid": "∣", "nmid": "∤",
	// Arrows.
	"to": "→", "rightarrow": "→", "gets": "←", "leftarrow": "←",
	"leftrightarrow": "↔", "uparrow": "↑", "downarrow": "↓",
	"Rightarrow": "⇒", "Leftarrow": "⇐", "Leftrightarrow": "⇔",
	"implies": "⇒", "iff": "⇔", "mapsto": "↦",
	"longrightarrow": "⟶", "longleftarrow": "⟵", "hookrightarrow": "↪",
	// Big operators and calculus.
	"sum": "∑", "prod": "∏", "coprod": "∐",
	"int": "∫", "iint": "∬", "iiint": "∭", "oint": "∮",
	"partial": "∂", "nabla": "∇", "infty": "∞",
	// Logic and sets.
	"forall": "∀", "exists": "∃", "nexists": "∄",
	"emptyset": "∅", "varnothing": "∅",
	"therefore": "∴", "because": "∵", "top": "⊤", "bot": "⊥",
	// Dots, primes, misc glyphs.
	"cdots": "⋯", "ldots": "…", "dots": "…", "vdots": "⋮", "ddots": "⋱",
	"prime": "′", "dag": "†", "ddag": "‡", "degree": "°", "angle": "∠",
	"langle": "⟨", "rangle": "⟩",
	"lceil": "⌈", "rceil": "⌉", "lfloor": "⌊", "rfloor": "⌋",
	"hbar": "ℏ", "ell": "ℓ", "Re": "ℜ", "Im": "ℑ", "aleph": "ℵ", "wp": "℘",
	"imath": "ı", "jmath": "ȷ",
	// Roman function names spell themselves.
	"sin": "sin", "cos": "cos", "tan": "tan", "cot": "cot",
	"sec": "sec", "csc": "csc",
	"arcsin": "arcsin", "arccos": "arccos", "arctan": "arctan",
	"sinh": "sinh", "cosh": "cosh", "tanh": "tanh", "coth": "coth",
	"log": "log", "ln": "ln", "lg": "lg", "exp": "exp",
	"lim": "lim", "sup": "sup", "inf": "inf", "max": "max", "min": "min",
	"det": "det", "dim": "dim", "ker": "ker", "deg": "deg",
	"arg": "arg", "gcd": "gcd", "Pr": "Pr", "bmod": "mod",
}

// superscript maps characters that have superscript codepoints. `∘` and `°`
// both raise to the degree sign, so `90^\circ` reads 90°.
var superscript = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
	'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
	'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾',
	'a': 'ᵃ', 'b': 'ᵇ', 'c': 'ᶜ', 'd': 'ᵈ', 'e': 'ᵉ', 'f': 'ᶠ',
	'g': 'ᵍ', 'h': 'ʰ', 'i': 'ⁱ', 'j': 'ʲ', 'k': 'ᵏ', 'l': 'ˡ',
	'm': 'ᵐ', 'n': 'ⁿ', 'o': 'ᵒ', 'p': 'ᵖ', 'r': 'ʳ', 's': 'ˢ',
	't': 'ᵗ', 'u': 'ᵘ', 'v': 'ᵛ', 'w': 'ʷ', 'x': 'ˣ', 'y': 'ʸ', 'z': 'ᶻ',
	'A': 'ᴬ', 'B': 'ᴮ', 'D': 'ᴰ', 'E': 'ᴱ', 'G': 'ᴳ', 'H': 'ᴴ',
	'I': 'ᴵ', 'J': 'ᴶ', 'K': 'ᴷ', 'L': 'ᴸ', 'M': 'ᴹ', 'N': 'ᴺ',
	'O': 'ᴼ', 'P': 'ᴾ', 'R': 'ᴿ', 'T': 'ᵀ', 'U': 'ᵁ', 'V': 'ⱽ', 'W': 'ᵂ',
	'β': 'ᵝ', 'γ': 'ᵞ', 'δ': 'ᵟ', 'θ': 'ᶿ', 'φ': 'ᵠ', 'χ': 'ᵡ',
	'∘': '°', '°': '°', '∞': '∞', 'π': 'π',
	'⁰': '⁰', '¹': '¹', '²': '²', '³': '³', '⁴': '⁴',
	'⁵': '⁵', '⁶': '⁶', '⁷': '⁷', '⁸': '⁸', '⁹': '⁹',
}

// subscript maps characters that have subscript codepoints.
var subscript = map[rune]rune{
	'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
	'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
	'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎',
	'a': 'ₐ', 'e': 'ₑ', 'h': 'ₕ', 'i': 'ᵢ', 'j': 'ⱼ', 'k': 'ₖ',
	'l': 'ₗ', 'm': 'ₘ', 'n': 'ₙ', 'o': 'ₒ', 'p': 'ₚ', 'r': 'ᵣ',
	's': 'ₛ', 't': 'ₜ', 'u': 'ᵤ', 'v': 'ᵥ', 'x': 'ₓ',
	'β': 'ᵦ', 'γ': 'ᵧ', 'ρ': 'ᵨ', 'φ': 'ᵩ', 'χ': 'ᵪ',
	'∞': '∞',
}
