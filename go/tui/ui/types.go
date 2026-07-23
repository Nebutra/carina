package ui

import "slices"

type ComponentID string
type ScreenID string
type HitID string

const (
	ScreenBootstrap    ScreenID = "bootstrap"
	ScreenConversation ScreenID = "conversation"
	ScreenNavigator    ScreenID = "navigator"
	ScreenDoctor       ScreenID = "doctor"
	ScreenOperational  ScreenID = "operational"
)

type Rect struct {
	X, Y          int
	Width, Height int
}

func (r Rect) Empty() bool { return r.Width <= 0 || r.Height <= 0 }

func (r Rect) Contains(x, y int) bool {
	return !r.Empty() && x >= r.X && y >= r.Y && x < r.X+r.Width && y < r.Y+r.Height
}

func (r Rect) Intersect(other Rect) Rect {
	x0, y0 := max(r.X, other.X), max(r.Y, other.Y)
	x1 := min(r.X+r.Width, other.X+other.Width)
	y1 := min(r.Y+r.Height, other.Y+other.Height)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}
}

type Size struct {
	Width, Height int
}

type Constraints struct {
	MinWidth, MaxWidth   int
	MinHeight, MaxHeight int
}

func (c Constraints) Constrain(size Size) Size {
	if c.MaxWidth > 0 {
		size.Width = min(size.Width, c.MaxWidth)
	}
	if c.MaxHeight > 0 {
		size.Height = min(size.Height, c.MaxHeight)
	}
	size.Width = max(size.Width, c.MinWidth)
	size.Height = max(size.Height, c.MinHeight)
	return size
}

type SemanticRole string

const (
	RoleText     SemanticRole = "text"
	RoleMuted    SemanticRole = "muted"
	RoleTitle    SemanticRole = "title"
	RoleInfo     SemanticRole = "info"
	RoleWarning  SemanticRole = "warning"
	RoleError    SemanticRole = "error"
	RoleSelected SemanticRole = "selected"
	RoleHovered  SemanticRole = "hovered"
	RoleDisabled SemanticRole = "disabled"
)

type HitKind string

const (
	HitHover    HitKind = "hover"
	HitActivate HitKind = "activate"
	HitScroll   HitKind = "scroll"
	HitFocus    HitKind = "focus"
)

type HitRegion struct {
	ID        HitID
	Owner     ComponentID
	Bounds    Rect
	Z         int
	Kind      HitKind
	Action    string
	Data      any
	Disabled  bool
	Focusable bool
}

type CursorRequest struct {
	Owner   ComponentID
	X, Y    int
	Visible bool
}

type GraphicsPlacement struct {
	Owner            ComponentID
	ID               string
	Bounds           Rect
	Generation       uint64
	TargetGeneration uint64
	Payload          any
}

type Node struct {
	ID        ComponentID
	Bounds    Rect
	Z         int
	Content   string
	Role      SemanticRole
	Focused   bool
	Hovered   bool
	Disabled  bool
	Focusable bool
	Hit       []HitRegion
	Children  []Node
	Cursor    *CursorRequest
	Graphics  []GraphicsPlacement
}

func (n Node) CursorRequests() []CursorRequest {
	var out []CursorRequest
	if n.Cursor != nil {
		out = append(out, *n.Cursor)
	}
	for _, child := range n.Children {
		out = append(out, child.CursorRequests()...)
	}
	return out
}

func (n Node) FocusOrder() []ComponentID {
	var out []ComponentID
	if n.Focusable && n.ID != "" && !n.Disabled {
		out = append(out, n.ID)
	}
	for _, child := range n.Children {
		out = append(out, child.FocusOrder()...)
	}
	return slices.Compact(out)
}

type RenderContext struct {
	FrameGeneration  uint64
	TargetGeneration uint64
	Focused          ComponentID
	Hovered          HitID
	Viewport         Rect
}

type EventKind string

const (
	EventKey     EventKind = "key"
	EventPointer EventKind = "pointer"
	EventResize  EventKind = "resize"
	EventFocus   EventKind = "focus"
	EventBlur    EventKind = "blur"
)

type PointerKind string

const (
	PointerMove    PointerKind = "move"
	PointerClick   PointerKind = "click"
	PointerRelease PointerKind = "release"
	PointerWheel   PointerKind = "wheel"
	PointerLeave   PointerKind = "leave"
)

type PointerEvent struct {
	Kind       PointerKind
	X, Y       int
	Button     int
	WheelDelta int
	Hit        *HitRegion
	LocalX     int
	LocalY     int
}

type Event struct {
	Kind    EventKind
	Key     string
	Pointer PointerEvent
	Width   int
	Height  int
}

type Action struct {
	Source ComponentID
	Name   string
	Data   any
}

type Effect struct {
	Source ComponentID
	Name   string
	Data   any
}

type Result struct {
	Handled bool
	Actions []Action
	Effects []Effect
}

type Component interface {
	ID() ComponentID
	Measure(Constraints) Size
	Layout(Rect)
	Render(RenderContext) Node
	Handle(Event) Result
}

type FocusReason string

const (
	FocusProgrammatic FocusReason = "programmatic"
	FocusKeyboard     FocusReason = "keyboard"
	FocusPointer      FocusReason = "pointer"
	FocusOverlay      FocusReason = "overlay"
	FocusRestore      FocusReason = "restore"
)

type Focusable interface {
	Component
	Focus(FocusReason)
	Blur(FocusReason)
	Focused() bool
}

type ComponentContainer interface {
	Components() []Component
}

type Disposable interface {
	Dispose()
}
