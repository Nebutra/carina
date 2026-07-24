package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

func renderComponentFrame(frame ui.Frame, width, height int, th theme.Theme) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	layers := []*lipgloss.Layer{lipgloss.NewLayer(blankComponentCanvas(width, height)).ID("component-canvas")}
	appendNodeLayers(&layers, frame.Root, width, height, 1, th)
	return clipBlock(lipgloss.NewCompositor(layers...).Render(), width, height)
}

func appendNodeLayers(layers *[]*lipgloss.Layer, node ui.Node, width, height, inheritedZ int, th theme.Theme) {
	if node.Content != "" {
		bounds := node.Bounds.Intersect(ui.Rect{Width: width, Height: height})
		if !bounds.Empty() {
			content := clipBlock(node.Content, bounds.Width, bounds.Height)
			content = semanticNodeStyle(node, th).Render(content)
			*layers = append(*layers, lipgloss.NewLayer(content).
				ID(string(node.ID)).X(bounds.X).Y(bounds.Y).Z(inheritedZ+node.Z))
		}
	}
	for _, child := range node.Children {
		appendNodeLayers(layers, child, width, height, inheritedZ+node.Z+1, th)
	}
}

func semanticNodeStyle(node ui.Node, th theme.Theme) lipgloss.Style {
	if node.ContentStyled {
		style := lipgloss.NewStyle()
		if node.Focused {
			style = style.Bold(true)
		} else if node.Hovered {
			style = style.Underline(true)
		}
		if node.Disabled {
			style = style.Faint(true)
		}
		return style
	}
	role := node.Role
	if role == "" && !node.Disabled {
		return lipgloss.NewStyle()
	}
	if node.Disabled {
		role = ui.RoleDisabled
	} else if node.Focused {
		role = ui.RoleSelected
	} else if node.Hovered {
		role = ui.RoleHovered
	}
	var style lipgloss.Style
	switch role {
	case ui.RoleMuted, ui.RoleDisabled:
		style = th.Style(theme.RoleMuted)
	case ui.RoleTitle, ui.RoleSelected:
		style = th.Style(theme.RoleTitle)
	case ui.RoleInfo:
		style = th.Style(theme.RoleInfo)
	case ui.RoleSuccess:
		style = th.Style(theme.RoleSuccess)
	case ui.RoleWarning:
		style = th.Style(theme.RoleWarning)
	case ui.RoleError:
		style = th.Style(theme.RoleError)
	case ui.RoleHovered:
		style = th.Style(theme.RoleInfo)
	default:
		style = th.Style(theme.RoleText)
	}
	// Focus and hover must remain visible in Mono/NO_COLOR. Attributes are
	// applied in addition to semantic color and introduce no new palette token.
	if node.Focused {
		style = style.Bold(true)
	} else if node.Hovered {
		style = style.Underline(true)
	}
	if node.Disabled {
		style = style.Faint(true)
	}
	return style
}

func blankComponentCanvas(width, height int) string {
	line := strings.Repeat(" ", width)
	lines := make([]string, height)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
