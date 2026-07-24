package tui

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

func TestSemanticAdapterStylesOnlyDeclaredSemanticNodes(t *testing.T) {
	plain := renderComponentFrame(ui.Frame{Root: ui.Node{
		ID: "plain", Bounds: ui.Rect{Width: 8, Height: 1}, Content: "plain",
	}}, 8, 1, theme.New(theme.ANSI256))
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("unstyled compatibility node gained ANSI: %q", plain)
	}

	hovered := renderComponentFrame(ui.Frame{Root: ui.Node{
		ID: "semantic", Bounds: ui.Rect{Width: 8, Height: 1}, Content: "hover",
		Role: ui.RoleInfo, Hovered: true,
	}}, 8, 1, theme.New(theme.Mono))
	if !strings.Contains(hovered, "\x1b[") {
		t.Fatalf("mono hover has no visible terminal attribute: %q", hovered)
	}
}

func TestSemanticAdapterPreservesStyledTranscriptContent(t *testing.T) {
	content := "\x1b[31merror detail\x1b[0m"
	rendered := renderComponentFrame(ui.Frame{Root: ui.Node{
		ID: "transcript", Bounds: ui.Rect{Width: 20, Height: 1}, Content: content,
		ContentStyled: true, Role: ui.RoleSuccess,
	}}, 20, 1, theme.New(theme.ANSI256))
	if !strings.Contains(rendered, "\x1b[31merror detail") || strings.Contains(rendered, "38;5;79") {
		t.Fatalf("adapter overwrote internal transcript styling: %q", rendered)
	}
}
