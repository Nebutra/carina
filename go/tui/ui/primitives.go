package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type Base struct {
	ComponentID ComponentID
	Bounds      Rect
	HasFocus    bool
}

func (b *Base) ID() ComponentID            { return b.ComponentID }
func (b *Base) Layout(bounds Rect)         { b.Bounds = bounds }
func (b *Base) Focus(FocusReason)          { b.HasFocus = true }
func (b *Base) Blur(FocusReason)           { b.HasFocus = false }
func (b *Base) Focused() bool              { return b.HasFocus }
func (b *Base) Handle(Event) Result        { return Result{} }
func (b *Base) Measure(c Constraints) Size { return c.Constrain(Size{Width: c.MaxWidth, Height: 1}) }
func (b *Base) Render(RenderContext) Node {
	return Node{ID: b.ComponentID, Bounds: b.Bounds, Focused: b.HasFocus}
}

type Container struct {
	Base
	Children []Component
}

func NewContainer(id ComponentID, children ...Component) *Container {
	return &Container{Base: Base{ComponentID: id}, Children: children}
}

func (c *Container) Components() []Component { return c.Children }

func (c *Container) Measure(constraints Constraints) Size {
	size := Size{}
	for _, child := range c.Children {
		childSize := child.Measure(constraints)
		size.Width = max(size.Width, childSize.Width)
		size.Height += childSize.Height
	}
	return constraints.Constrain(size)
}

func (c *Container) Layout(bounds Rect) {
	c.Bounds = bounds
	y := bounds.Y
	remaining := bounds.Height
	for i, child := range c.Children {
		height := child.Measure(Constraints{MaxWidth: bounds.Width, MaxHeight: remaining}).Height
		if i == len(c.Children)-1 {
			height = remaining
		}
		height = min(max(height, 0), remaining)
		child.Layout(Rect{X: bounds.X, Y: y, Width: bounds.Width, Height: height})
		y += height
		remaining -= height
	}
}

func (c *Container) Render(ctx RenderContext) Node {
	node := Node{ID: c.ComponentID, Bounds: c.Bounds, Focused: c.HasFocus}
	for _, child := range c.Children {
		node.Children = append(node.Children, child.Render(ctx))
	}
	return node
}

type Tab struct {
	ID       string
	Label    string
	Disabled bool
}

type Tabs struct {
	Base
	Items   []Tab
	Active  int
	Hovered int
}

func (t *Tabs) Measure(c Constraints) Size {
	width := 0
	for i, item := range t.Items {
		width += ansi.StringWidth(item.Label)
		if i > 0 {
			width += 2
		}
	}
	return c.Constrain(Size{Width: width, Height: 1})
}

func (t *Tabs) Render(RenderContext) Node {
	parts := make([]string, 0, len(t.Items))
	hits := make([]HitRegion, 0, len(t.Items))
	x := t.Bounds.X
	for i, item := range t.Items {
		label := item.Label
		if i == t.Active {
			label = "[" + label + "]"
		}
		if i > 0 {
			parts = append(parts, "  ")
			x += 2
		}
		parts = append(parts, label)
		hits = append(hits, HitRegion{
			ID: HitID(string(t.ComponentID) + ":tab:" + item.ID), Owner: t.ComponentID,
			Bounds: Rect{X: x, Y: t.Bounds.Y, Width: ansi.StringWidth(label), Height: 1}, Z: 1,
			Kind: HitActivate, Action: "select-tab", Data: i, Disabled: item.Disabled, Focusable: true,
		})
		x += ansi.StringWidth(label)
	}
	return Node{ID: t.ComponentID, Bounds: t.Bounds, Content: strings.Join(parts, ""), Focusable: true, Focused: t.HasFocus, Hit: hits}
}

func (t *Tabs) Handle(event Event) Result {
	if len(t.Items) == 0 {
		return Result{}
	}
	index := t.Active
	switch {
	case event.Kind == EventKey && (event.Key == "left" || event.Key == "h"):
		index--
	case event.Kind == EventKey && (event.Key == "right" || event.Key == "l"):
		index++
	case event.Kind == EventPointer && event.Pointer.Hit != nil && event.Pointer.Hit.Action == "select-tab":
		value, ok := event.Pointer.Hit.Data.(int)
		if !ok {
			return Result{}
		}
		index = value
	default:
		return Result{}
	}
	index = (index + len(t.Items)) % len(t.Items)
	if t.Items[index].Disabled {
		return Result{Handled: true}
	}
	t.Active = index
	return Result{Handled: true, Actions: []Action{{Source: t.ComponentID, Name: "tab-changed", Data: t.Items[index].ID}}}
}

type ActionItem struct {
	ID       string
	Label    string
	Detail   string
	Disabled bool
}

type ActionList struct {
	Base
	Items    []ActionItem
	Selected int
	Hovered  int
	Scroll   int
}

func (l *ActionList) Measure(c Constraints) Size {
	return c.Constrain(Size{Width: c.MaxWidth, Height: len(l.Items)})
}

func (l *ActionList) Layout(bounds Rect) {
	l.Bounds = bounds
	l.clamp()
}

func (l *ActionList) Render(RenderContext) Node {
	lines := make([]string, 0, l.Bounds.Height)
	hits := make([]HitRegion, 0, l.Bounds.Height)
	end := min(len(l.Items), l.Scroll+l.Bounds.Height)
	for i := l.Scroll; i < end; i++ {
		item := l.Items[i]
		prefix := "  "
		if i == l.Selected {
			prefix = "> "
		}
		line := prefix + item.Label
		if item.Detail != "" {
			line += "  " + item.Detail
		}
		lines = append(lines, line)
		hits = append(hits, HitRegion{
			ID: HitID(string(l.ComponentID) + ":row:" + item.ID), Owner: l.ComponentID,
			Bounds: Rect{X: l.Bounds.X, Y: l.Bounds.Y + i - l.Scroll, Width: l.Bounds.Width, Height: 1},
			Kind:   HitActivate, Action: "row", Data: i, Disabled: item.Disabled, Focusable: true,
		})
	}
	return Node{
		ID: l.ComponentID, Bounds: l.Bounds, Content: strings.Join(lines, "\n"),
		Focusable: true, Focused: l.HasFocus, Hit: hits,
	}
}

func (l *ActionList) Handle(event Event) Result {
	if len(l.Items) == 0 {
		return Result{}
	}
	activate := false
	switch {
	case event.Kind == EventKey && (event.Key == "up" || event.Key == "k"):
		l.Selected--
	case event.Kind == EventKey && (event.Key == "down" || event.Key == "j"):
		l.Selected++
	case event.Kind == EventKey && event.Key == "enter":
		activate = true
	case event.Kind == EventPointer && event.Pointer.Kind == PointerWheel:
		l.Selected += event.Pointer.WheelDelta
	case event.Kind == EventPointer && event.Pointer.Hit != nil && event.Pointer.Hit.Action == "row":
		index, ok := event.Pointer.Hit.Data.(int)
		if !ok {
			return Result{}
		}
		if event.Pointer.Kind == PointerMove {
			l.Hovered = index
			return Result{Handled: true, Actions: []Action{{Source: l.ComponentID, Name: "hover", Data: l.Items[index].ID}}}
		}
		if event.Pointer.Kind == PointerClick {
			activate = index == l.Selected
			l.Selected = index
		}
	default:
		return Result{}
	}
	l.clamp()
	item := l.Items[l.Selected]
	if item.Disabled {
		return Result{Handled: true}
	}
	name := "selected"
	if activate {
		name = "activate"
	}
	return Result{Handled: true, Actions: []Action{{Source: l.ComponentID, Name: name, Data: item.ID}}}
}

func (l *ActionList) clamp() {
	if len(l.Items) == 0 {
		l.Selected, l.Scroll = 0, 0
		return
	}
	l.Selected = min(max(l.Selected, 0), len(l.Items)-1)
	page := max(l.Bounds.Height, 1)
	if l.Selected < l.Scroll {
		l.Scroll = l.Selected
	}
	if l.Selected >= l.Scroll+page {
		l.Scroll = l.Selected - page + 1
	}
	l.Scroll = min(max(l.Scroll, 0), max(len(l.Items)-page, 0))
}

type TextSurface struct {
	Base
	Content string
	Role    SemanticRole
}

func (t *TextSurface) Measure(c Constraints) Size {
	lines := strings.Split(t.Content, "\n")
	width := 0
	for _, line := range lines {
		width = max(width, ansi.StringWidth(line))
	}
	return c.Constrain(Size{Width: width, Height: len(lines)})
}

func (t *TextSurface) Render(RenderContext) Node {
	return Node{ID: t.ComponentID, Bounds: t.Bounds, Content: t.Content, Role: t.Role, Focused: t.HasFocus}
}

type Dialog struct{ Container }
type Inspector struct{ TextSurface }
type StatusBar struct{ TextSurface }
type ComposerShell struct{ TextSurface }

type Spinner struct {
	TextSurface
	Frames []string
	Frame  int
}

func (s *Spinner) Render(ctx RenderContext) Node {
	content := s.Content
	if len(s.Frames) > 0 {
		content = s.Frames[s.Frame%len(s.Frames)] + " " + content
	}
	node := s.TextSurface.Render(ctx)
	node.Content = content
	return node
}
