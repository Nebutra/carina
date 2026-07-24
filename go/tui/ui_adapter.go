package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

func renderComponentFrame(frame ui.Frame, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	layers := []*lipgloss.Layer{lipgloss.NewLayer(blankComponentCanvas(width, height)).ID("component-canvas")}
	appendNodeLayers(&layers, frame.Root, width, height, 1)
	return clipBlock(lipgloss.NewCompositor(layers...).Render(), width, height)
}

func appendNodeLayers(layers *[]*lipgloss.Layer, node ui.Node, width, height, inheritedZ int) {
	if node.Content != "" {
		bounds := node.Bounds.Intersect(ui.Rect{Width: width, Height: height})
		if !bounds.Empty() {
			content := clipBlock(node.Content, bounds.Width, bounds.Height)
			*layers = append(*layers, lipgloss.NewLayer(content).
				ID(string(node.ID)).X(bounds.X).Y(bounds.Y).Z(inheritedZ+node.Z))
		}
	}
	for _, child := range node.Children {
		appendNodeLayers(layers, child, width, height, inheritedZ+node.Z+1)
	}
}

func blankComponentCanvas(width, height int) string {
	line := strings.Repeat(" ", width)
	lines := make([]string, height)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
