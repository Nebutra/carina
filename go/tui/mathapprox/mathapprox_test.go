package mathapprox

import (
	"reflect"
	"testing"
)

// Golden transform per construct class: the approximation is deterministic
// and exact, or the formula is rejected outright (ok=false) so the caller
// falls back to verbatim source. There is no third outcome.
func TestApproxGolden(t *testing.T) {
	cases := []struct {
		name string
		tex  string
		want string
	}{
		{"superscript digit", "x^2", "x²"},
		{"superscript group", "x^{n+1}", "xⁿ⁺¹"},
		{"subscript digit and group", "a_{ij} + b_1", "aᵢⱼ + b₁"},
		{"physics classic", "E = mc^2", "E = mc²"},
		{"degree via circ", "90^\\circ", "90°"},
		{"greek letters", "\\alpha\\beta\\Gamma \\pi", "αβΓ π"},
		{"fraction of atoms", "\\frac{a}{b}", "a⁄b"},
		{"fraction parenthesizes molecules", "\\frac{a+b}{2}", "(a+b)⁄2"},
		{"square root atom", "\\sqrt{2}", "√2"},
		{"square root molecule", "\\sqrt{x+1}", "√(x+1)"},
		{"indexed root", "\\sqrt[3]{8}", "³√8"},
		{"binary operators", "a \\times b \\cdot c \\pm d", "a × b ⋅ c ± d"},
		{"relations", "x \\leq y \\geq z \\neq w \\approx v", "x ≤ y ≥ z ≠ w ≈ v"},
		{"sets", "x \\in A \\subset B \\cup C", "x ∈ A ⊂ B ∪ C"},
		{"arrows", "f: X \\to Y \\Rightarrow Z", "f: X → Y ⇒ Z"},
		{"sum with scripted bounds", "\\sum_{i=1}^{n} i", "∑ᵢ₌₁ⁿ i"},
		{"sized parens drop the sizing", "\\left( \\frac{1}{2} \\right)^2", "( 1⁄2 )²"},
		{"text passes through", "\\text{speed} = 5", "speed = 5"},
		{"function names", "\\sin^2 \\theta + \\cos^2 \\theta = 1", "sin² θ + cos² θ = 1"},
		{"thin space and tie", "a\\,b~c", "a b c"},
		{"escaped braces and percent", "\\{x\\} \\% \\_", "{x} % _"},
		{"cjk passes through", "面积 = \\pi r^2", "面积 = π r²"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Approx(tc.tex)
			if !ok || got != tc.want {
				t.Errorf("Approx(%q) = %q, %v; want %q, true", tc.tex, got, ok, tc.want)
			}
		})
	}
}

func TestApproxDisplayEnvironmentGolden(t *testing.T) {
	cases := []struct {
		name string
		tex  string
		want string
	}{
		{
			name: "pmatrix aligns columns",
			tex:  `A = \begin{pmatrix} 1 & 20 \\ 300 & 4 \end{pmatrix}, \quad \det(A) = ad-bc`,
			want: "A = ⎛ 1    20 ⎞\n⎝ 300  4  ⎠,    det(A) = ad-bc",
		},
		{
			name: "cases renders physical rows",
			tex:  `f(x) = \begin{cases} x^2, & x \geq 0 \\ -x, & x < 0 \end{cases}`,
			want: "f(x) = ⎧ x²,  x ≥ 0\n⎩ -x,  x < 0",
		},
		{
			name: "aligned preserves equations",
			tex:  `\begin{aligned} x + y &= 10 \\ 2x - y &= 5 \end{aligned}`,
			want: "x + y = 10\n2x - y = 5",
		},
		{
			name: "integral bounds and nested exponent",
			tex:  `\int_{-\infty}^{\infty} e^{-x^2}\,dx = \sqrt{\pi}`,
			want: "∫₋∞∞ e⁻ˣ² dx = √π",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Approx(tc.tex)
			if !ok || got != tc.want {
				t.Fatalf("Approx(%q) = %q, %v; want %q, true", tc.tex, got, ok, tc.want)
			}
		})
	}
}

// Anything outside the subset rejects the whole formula — a half-transformed
// exponent or a guessed macro would misread, so ok=false means verbatim.
func TestApproxRejectsOutsideSubset(t *testing.T) {
	cases := []struct {
		name string
		tex  string
	}{
		{"unknown macro", "\\weird{x}"},
		{"unmappable superscript rune", "x^q"},
		{"space inside a script", "\\lim_{x \\to 0}"},
		{"unbalanced open", "{a"},
		{"unbalanced close", "a}"},
		{"frac without braces", "\\frac12"},
		{"trailing backslash", "a\\"},
		{"empty formula", "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := Approx(tc.tex); ok {
				t.Errorf("Approx(%q) = %q, true; want rejection", tc.tex, got)
			}
		})
	}
}

// Golden span detection: currency-safe dollar rules, all four delimiter
// pairs, escapes, and the verbatim fallback for detected-but-unrecognized
// math. Unclosed delimiters never become math.
func TestLineGolden(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Segment
	}{
		{
			name: "currency is not math",
			in:   "costs $5 and $10 today",
			want: []Segment{{Text: "costs $5 and $10 today"}},
		},
		{
			name: "close before a digit is not a close",
			in:   "between $5-$10 total",
			want: []Segment{{Text: "between $5-$10 total"}},
		},
		{
			name: "escaped dollars stay text",
			in:   `pay \$x\$ now`,
			want: []Segment{{Text: "pay $x$ now"}},
		},
		{
			name: "inline dollars transform",
			in:   "solve $x^2 + y_1$ fast",
			want: []Segment{{Text: "solve "}, {Text: "x² + y₁", Math: true}, {Text: " fast"}},
		},
		{
			name: "inline parens transform",
			in:   `inline \(a \times b\) here`,
			want: []Segment{{Text: "inline "}, {Text: "a × b", Math: true}, {Text: " here"}},
		},
		{
			name: "display dollars transform",
			in:   "$$E = mc^2$$",
			want: []Segment{{Text: "E = mc²", Math: true}},
		},
		{
			name: "display brackets transform",
			in:   `\[ x \leq y \]`,
			want: []Segment{{Text: "x ≤ y", Math: true}},
		},
		{
			name: "unrecognized math renders verbatim",
			in:   `so $\weird{x}$ holds`,
			want: []Segment{{Text: "so "}, {Text: `$\weird{x}$`, Math: true}, {Text: " holds"}},
		},
		{
			name: "unclosed dollar stays text",
			in:   "just $one dollar",
			want: []Segment{{Text: "just $one dollar"}},
		},
		{
			name: "unclosed display stays text",
			in:   `\[ x + y`,
			want: []Segment{{Text: `\[ x + y`}},
		},
		{
			name: "empty display is text",
			in:   "$$ $$",
			want: []Segment{{Text: "$$ $$"}},
		},
		{
			name: "escaped backslash cannot open math",
			in:   `lit \\(foo) end`,
			want: []Segment{{Text: `lit \\(foo) end`}},
		},
		{
			name: "whole line math",
			in:   "$x^2$",
			want: []Segment{{Text: "x²", Math: true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Line(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Line(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// Deterministic pure functions: same input → same segments, every call.
func TestLineIsDeterministic(t *testing.T) {
	in := `mix $a^2$, \(b_1\), $$\frac{a}{b}$$, \$5 and $\bad{x}$`
	first := Line(in)
	for i := 0; i < 3; i++ {
		if got := Line(in); !reflect.DeepEqual(got, first) {
			t.Fatalf("Line %d diverged:\n%#v\nvs\n%#v", i, got, first)
		}
	}
}

// The fallback contract is total: for any detected span the output is either
// the exact approximation or the exact source — never a mixture, never loss.
func TestFallbackPreservesSource(t *testing.T) {
	for _, in := range []string{
		`$\weird{x}$`, `\(x^q\)`, `$$\lim_{x \to 0} f$$`, `\[\unknown\]`,
	} {
		segs := Line(in)
		if len(segs) != 1 || !segs[0].Math || segs[0].Text != in {
			t.Errorf("Line(%q) = %#v, want the verbatim source as one math segment", in, segs)
		}
	}
}
