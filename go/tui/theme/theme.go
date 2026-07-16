// Package theme carries the terminal subset of the Carina design tokens as
// code. Semantic roles are the only way views obtain color; no view hardcodes
// a palette value. TrueColor renders the DTCG hex, ANSI256 renders the
// documented fallback, and Mono renders no styling.
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

// The terminal token table from docs/brand/design-system/design-tokens.json
// and docs/brand/brand-brief.md §2.
var (
	Void          = Token{"Void", "#0d1214", "233"}
	Surface       = Token{"Surface", "#141b1d", "234"}
	Border        = Token{"Border", "#344144", "237"}
	Starlight     = Token{"Starlight", "#f3f0e8", "255"}
	Dust          = Token{"Dust", "#b0b7b3", "249"}
	BrandRose     = Token{"Brand Rose", "#8e4053", "95"}
	IonCyan       = Token{"Ion Cyan", "#8edbd2", "116"}
	DustViolet    = Token{"Dust Violet", "#c6a6ea", "182"}
	OxygenBlue    = Token{"Oxygen Blue", "#78bff2", "111"}
	CopperAmber   = Token{"Copper Amber", "#e8a85f", "179"}
	SpectralGreen = Token{"Spectral Green", "#68d2a3", "79"}
	EventRed      = Token{"Event Red", "#ff7c78", "210"}
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
	// Markdown roles: the transcript's rich-text renderer names document
	// structure, and the theme resolves it to the same palette the rest of
	// the terminal surface uses. Under Mono all of them degrade to plain text.
	RoleHeading
	RoleCodeInline
	RoleCodeBlock
	RoleLink
	RoleListMarker
	RoleBlockquote
	RoleTableBorder
	RoleMathApprox
	// Syntax roles: chroma token categories inside fenced code blocks resolve
	// to these. The category→role mapping lives in exactly one place
	// (go/tui/markdown highlight.go); the theme only names the palette slot.
	// Unmapped tokens stay RoleCodeBlock, and Mono degrades to plain text.
	RoleSyntaxKeyword
	RoleSyntaxString
	RoleSyntaxNumber
	RoleSyntaxComment
	RoleSyntaxFunction
	RoleSyntaxType
)

// roleToken keeps product semantics separate from Brand Rose, which is reserved
// for identity surfaces rather than terminal state.
func roleToken(r Role) Token {
	switch r {
	case RoleError, RoleDiffDel:
		return EventRed
	case RoleWarning:
		return CopperAmber
	case RoleSuccess, RoleDiffAdd, RoleSyntaxString:
		return SpectralGreen
	case RoleInfo, RoleDiffHunk, RoleLink, RoleSyntaxFunction:
		return OxygenBlue
	case RoleTitle, RoleHeading, RoleListMarker, RoleSyntaxType:
		return IonCyan
	case RoleMuted, RoleCodeBlock, RoleBlockquote, RoleSyntaxComment:
		return Dust
	case RoleBorder, RoleTableBorder:
		return Border
	case RoleCodeInline, RoleSyntaxNumber:
		return CopperAmber
	case RoleMathApprox, RoleSyntaxKeyword:
		return DustViolet
	default:
		return Starlight
	}
}

func roleBold(r Role) bool {
	return r == RoleTitle || r == RoleDiffHunk || r == RoleHeading
}

func roleUnderline(r Role) bool {
	return r == RoleLink
}

// Comments share the Dust token with plain code; the italic attribute is what
// keeps them distinguishable inside a highlighted block.
func roleItalic(r Role) bool {
	return r == RoleSyntaxComment
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
	if roleUnderline(r) {
		s = s.Underline(true)
	}
	if roleItalic(r) {
		s = s.Italic(true)
	}
	return s
}
