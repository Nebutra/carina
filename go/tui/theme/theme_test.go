package theme

import (
	"strings"
	"testing"
)

// The token table must transcribe the DTCG/terminal contract verbatim.
func TestTokenTableMatchesBrandBrief(t *testing.T) {
	want := []struct {
		token Token
		name  string
		hex   string
		ansi  string
	}{
		{Void, "Void", "#0d1214", "233"},
		{Surface, "Surface", "#141b1d", "234"},
		{Border, "Border", "#344144", "237"},
		{Starlight, "Starlight", "#f3f0e8", "255"},
		{Dust, "Dust", "#b0b7b3", "249"},
		{BrandRose, "Brand Rose", "#8e4053", "95"},
		{IonCyan, "Ion Cyan", "#8edbd2", "116"},
		{DustViolet, "Dust Violet", "#c6a6ea", "182"},
		{OxygenBlue, "Oxygen Blue", "#78bff2", "111"},
		{CopperAmber, "Copper Amber", "#e8a85f", "179"},
		{SpectralGreen, "Spectral Green", "#68d2a3", "79"},
		{EventRed, "Event Red", "#ff7c78", "210"},
	}
	for _, w := range want {
		if w.token.Name != w.name {
			t.Errorf("token name = %q, want %q", w.token.Name, w.name)
		}
		if w.token.Hex != w.hex {
			t.Errorf("%s hex = %q, want %q", w.name, w.token.Hex, w.hex)
		}
		if w.token.ANSI256 != w.ansi {
			t.Errorf("%s ansi256 = %q, want %q", w.name, w.token.ANSI256, w.ansi)
		}
	}
}

// Semantic roles follow the design system; Brand Rose is never a state color.
func TestSemanticRoleMapping(t *testing.T) {
	want := map[Role]Token{
		RoleError:    EventRed,
		RoleWarning:  CopperAmber,
		RoleSuccess:  SpectralGreen,
		RoleInfo:     OxygenBlue,
		RoleMuted:    Dust,
		RoleText:     Starlight,
		RoleTitle:    IonCyan,
		RoleBorder:   Border,
		RoleDiffAdd:  SpectralGreen,
		RoleDiffDel:  EventRed,
		RoleDiffHunk: OxygenBlue,
		// Markdown roles resolve to the same terminal palette; Brand Rose
		// stays reserved for identity surfaces here too.
		RoleHeading:     IonCyan,
		RoleCodeInline:  CopperAmber,
		RoleCodeBlock:   Dust,
		RoleLink:        OxygenBlue,
		RoleListMarker:  IonCyan,
		RoleBlockquote:  Dust,
		RoleTableBorder: Border,
		RoleMathApprox:  DustViolet,
		// Syntax roles inside highlighted code blocks reuse the same palette.
		RoleSyntaxKeyword:  DustViolet,
		RoleSyntaxString:   SpectralGreen,
		RoleSyntaxNumber:   CopperAmber,
		RoleSyntaxComment:  Dust,
		RoleSyntaxFunction: OxygenBlue,
		RoleSyntaxType:     IonCyan,
	}
	for role, tok := range want {
		if got := roleToken(role); got != tok {
			t.Errorf("roleToken(%v) = %v, want %v", role, got, tok)
		}
	}
}

func TestDetectProfile(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		name  string
		vars  map[string]string
		isTTY bool
		want  Profile
	}{
		{"no color env wins", map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"}, true, Mono},
		{"dumb terminal", map[string]string{"TERM": "dumb"}, true, Mono},
		{"not a tty", map[string]string{"COLORTERM": "truecolor"}, false, Mono},
		{"truecolor", map[string]string{"COLORTERM": "truecolor"}, true, TrueColor},
		{"24bit", map[string]string{"COLORTERM": "24bit"}, true, TrueColor},
		{"default 256", map[string]string{"TERM": "xterm-256color"}, true, ANSI256},
	}
	for _, c := range cases {
		if got := Detect(env(c.vars), c.isTTY); got != c.want {
			t.Errorf("%s: Detect = %v, want %v", c.name, got, c.want)
		}
	}
}

// Views obtain color only through Theme.Style: truecolor profiles emit RGB
// sequences from the brief's hex, 256-color profiles emit the documented
// fallback index, and Mono emits no escape sequences at all (NO_COLOR).
func TestStyleByProfile(t *testing.T) {
	if out := New(Mono).Style(RoleWarning).Render("x"); out != "x" {
		t.Errorf("Mono style must be plain, got %q", out)
	}
	if out := New(ANSI256).Style(RoleWarning).Render("x"); !strings.Contains(out, "38;5;179") {
		t.Errorf("ANSI256 warning must use fallback 179, got %q", out)
	}
	// Copper Amber #e8a85f = rgb(232,168,95)
	if out := New(TrueColor).Style(RoleWarning).Render("x"); !strings.Contains(out, "38;2;232;168;95") {
		t.Errorf("TrueColor warning must use brief hex, got %q", out)
	}
}

// Links are underlined in color profiles so they read as links even where the
// terminal ignores OSC 8; markdown roles stay entirely plain under Mono.
func TestMarkdownRoleAttributes(t *testing.T) {
	if out := New(ANSI256).Style(RoleLink).Render("x"); !strings.Contains(out, "4m") && !strings.Contains(out, ";4;") && !strings.Contains(out, "[4;") {
		t.Errorf("ANSI256 link should be underlined, got %q", out)
	}
	if out := New(ANSI256).Style(RoleHeading).Render("x"); !strings.Contains(out, "\x1b[1;") && !strings.Contains(out, "\x1b[1m") {
		t.Errorf("ANSI256 heading should be bold, got %q", out)
	}
	for _, role := range []Role{RoleHeading, RoleCodeInline, RoleCodeBlock, RoleLink, RoleListMarker, RoleBlockquote, RoleTableBorder, RoleMathApprox} {
		if out := New(Mono).Style(role).Render("x"); out != "x" {
			t.Errorf("Mono markdown role %v must be plain, got %q", role, out)
		}
	}
}

// Comments are the one italic syntax role (Dust alone would blend into plain
// code); every syntax role stays plain under Mono.
func TestSyntaxRoleAttributes(t *testing.T) {
	if out := New(ANSI256).Style(RoleSyntaxComment).Render("x"); !strings.Contains(out, "3m") && !strings.Contains(out, ";3;") && !strings.Contains(out, "[3;") {
		t.Errorf("ANSI256 comment should be italic, got %q", out)
	}
	for _, role := range []Role{RoleSyntaxKeyword, RoleSyntaxString, RoleSyntaxNumber, RoleSyntaxComment, RoleSyntaxFunction, RoleSyntaxType} {
		if out := New(Mono).Style(role).Render("x"); out != "x" {
			t.Errorf("Mono syntax role %v must be plain, got %q", role, out)
		}
	}
}

// Title and hunk headers are bold in color profiles; Mono stays entirely plain.
func TestBoldRolesStayPlainUnderMono(t *testing.T) {
	if out := New(Mono).Style(RoleTitle).Render("t"); out != "t" {
		t.Errorf("Mono title must be plain, got %q", out)
	}
	if out := New(ANSI256).Style(RoleTitle).Render("t"); !strings.Contains(out, "\x1b[1;") && !strings.Contains(out, "\x1b[1m") {
		t.Errorf("ANSI256 title should be bold, got %q", out)
	}
}
