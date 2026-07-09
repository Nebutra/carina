package theme

import (
	"strings"
	"testing"
)

// The token table must transcribe docs/brand/brand-brief.md §2 verbatim:
// truecolor hex + ANSI-256 fallback per token. A drifted hex is a brand bug.
func TestTokenTableMatchesBrandBrief(t *testing.T) {
	want := []struct {
		token Token
		name  string
		hex   string
		ansi  string
	}{
		{Void, "Void", "#1a191d", "234"},
		{EmberShadow, "Ember Shadow", "#261316", "235"},
		{CarinaCrimson, "Carina Crimson", "#55212d", "52"},
		{IonizedRose, "Ionized Rose", "#733445", "95"},
		{DustMauve, "Dust Mauve", "#60344f", "96"},
		{NebulaOrchid, "Nebula Orchid", "#a3688f", "132"},
		{CoreGlow, "Core Glow", "#c18ba3", "139"},
		{Starlight, "Starlight", "#fff8fe", "231"},
		{StarGold, "Star Gold", "#b98b6a", "137"},
		{BlueGiant, "Blue Giant", "#e3e3ff", "189"},
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

// Semantic roles follow brand-brief §2: error→Crimson family bright/132,
// warning→Star Gold/137, success→Core Glow/139, info→Blue Giant/189,
// muted→Dust Mauve/96.
func TestSemanticRoleMapping(t *testing.T) {
	want := map[Role]Token{
		RoleError:    NebulaOrchid, // Carina Crimson family, bright member (ANSI 132)
		RoleWarning:  StarGold,
		RoleSuccess:  CoreGlow,
		RoleInfo:     BlueGiant,
		RoleMuted:    DustMauve,
		RoleText:     Starlight,
		RoleTitle:    BlueGiant,
		RoleBorder:   DustMauve,
		RoleDiffAdd:  CoreGlow,
		RoleDiffDel:  NebulaOrchid,
		RoleDiffHunk: BlueGiant,
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
	if out := New(ANSI256).Style(RoleWarning).Render("x"); !strings.Contains(out, "38;5;137") {
		t.Errorf("ANSI256 warning must use fallback 137, got %q", out)
	}
	// Star Gold #b98b6a = rgb(185,139,106)
	if out := New(TrueColor).Style(RoleWarning).Render("x"); !strings.Contains(out, "38;2;185;139;106") {
		t.Errorf("TrueColor warning must use brief hex, got %q", out)
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
