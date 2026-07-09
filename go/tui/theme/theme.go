// Package theme carries the Carina brand palette (docs/brand/brand-brief.md
// §2) as code: each token is the brief's truecolor hex plus its documented
// ANSI-256 fallback, and semantic roles (error, warning, success, info,
// muted) are the only way views obtain color — no view hardcodes ANSI. The
// profile decides what a style emits: TrueColor renders the exact hex,
// ANSI256 renders the fallback index, Mono (NO_COLOR, dumb terminals, pipes)
// renders nothing at all.
package theme

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// Token is one palette entry from brand-brief §2.
type Token struct {
	Name    string
	Hex     string
	ANSI256 string
}

// The brand-brief §2 token table, transcribed verbatim.
var (
	Void          = Token{"Void", "#1a191d", "234"}
	EmberShadow   = Token{"Ember Shadow", "#261316", "235"}
	CarinaCrimson = Token{"Carina Crimson", "#55212d", "52"}
	IonizedRose   = Token{"Ionized Rose", "#733445", "95"}
	DustMauve     = Token{"Dust Mauve", "#60344f", "96"}
	NebulaOrchid  = Token{"Nebula Orchid", "#a3688f", "132"}
	CoreGlow      = Token{"Core Glow", "#c18ba3", "139"}
	Starlight     = Token{"Starlight", "#fff8fe", "231"}
	StarGold      = Token{"Star Gold", "#b98b6a", "137"}
	BlueGiant     = Token{"Blue Giant", "#e3e3ff", "189"}
)

// Profile is the terminal color capability the theme renders for.
type Profile int

const (
	TrueColor Profile = iota
	ANSI256
	Mono
)

// Detect resolves the color profile from the environment. NO_COLOR, a dumb
// terminal, or a non-TTY collapse to Mono; COLORTERM=truecolor/24bit selects
// TrueColor; everything else renders through the ANSI-256 fallbacks.
func Detect(getenv func(string) string, isTTY bool) Profile {
	if !isTTY || getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return Mono
	}
	ct := strings.ToLower(getenv("COLORTERM"))
	if strings.Contains(ct, "truecolor") || strings.Contains(ct, "24bit") {
		return TrueColor
	}
	return ANSI256
}

// Role is a semantic color slot. Views name intent; the theme names color.
type Role int

const (
	RoleText Role = iota
	RoleTitle
	RoleMuted
	RoleError
	RoleWarning
	RoleSuccess
	RoleInfo
	RoleBorder
	RoleDiffAdd
	RoleDiffDel
	RoleDiffHunk
)

// roleToken maps semantic roles to palette tokens per brand-brief §2:
// error → Carina Crimson family, bright member (ANSI 132), warning → Star
// Gold, success → Core Glow, info/link → Blue Giant, muted → Dust Mauve.
func roleToken(r Role) Token {
	switch r {
	case RoleError, RoleDiffDel:
		return NebulaOrchid
	case RoleWarning:
		return StarGold
	case RoleSuccess, RoleDiffAdd:
		return CoreGlow
	case RoleInfo, RoleTitle, RoleDiffHunk:
		return BlueGiant
	case RoleMuted, RoleBorder:
		return DustMauve
	default:
		return Starlight
	}
}

func roleBold(r Role) bool {
	return r == RoleTitle || r == RoleDiffHunk
}

// Theme renders roles for one detected profile.
type Theme struct {
	profile Profile
}

func New(p Profile) Theme { return Theme{profile: p} }

func (t Theme) Profile() Profile { return t.profile }

// Color returns the lipgloss color for a role, or nil under Mono.
func (t Theme) Color(r Role) color.Color {
	tok := roleToken(r)
	switch t.profile {
	case TrueColor:
		return lipgloss.Color(tok.Hex)
	case ANSI256:
		return lipgloss.Color(tok.ANSI256)
	default:
		return nil
	}
}

// Style returns a lipgloss style for a role. Under Mono it is the empty
// style: no color, no bold, no escape sequences — the NO_COLOR contract.
func (t Theme) Style(r Role) lipgloss.Style {
	s := lipgloss.NewStyle()
	if t.profile == Mono {
		return s
	}
	if c := t.Color(r); c != nil {
		s = s.Foreground(c)
	}
	if roleBold(r) {
		s = s.Bold(true)
	}
	return s
}
