package ui

import "testing"

func TestFocusCyclesAndOverlayRestoresPreviousOwner(t *testing.T) {
	first := &TextSurface{Base: Base{ComponentID: "first"}, Content: "first"}
	second := &TextSurface{Base: Base{ComponentID: "second"}, Content: "second"}
	overlay := &TextSurface{Base: Base{ComponentID: "overlay"}, Content: "overlay"}
	runtime := NewRuntime()
	runtime.Mount(first)
	runtime.Mount(second)
	runtime.Mount(overlay)
	runtime.Focus.SetOrder([]ComponentID{"first", "second"})
	runtime.SetFocus("first", FocusProgrammatic)
	if got := runtime.Focus.Cycle(1); got != "second" {
		t.Fatalf("cycle focus=%q", got)
	}
	runtime.SetFocus("second", FocusKeyboard)
	runtime.PushOverlay("dialog", "overlay", true)
	runtime.BeginFrame(overlay, Rect{Width: 20, Height: 4})
	if got := runtime.Focus.Current(); got != "overlay" {
		t.Fatalf("overlay focus=%q", got)
	}
	runtime.PopOverlay()
	if got := runtime.Focus.Current(); got != "second" {
		t.Fatalf("restored focus=%q", got)
	}
}

func TestPointerRouterUsesTopmostPublishedGeometryAndRejectsStaleFrame(t *testing.T) {
	router := PointerRouter{}
	root := Node{Children: []Node{
		{ID: "back", Z: 1, Hit: []HitRegion{{ID: "back-row", Bounds: Rect{Width: 10, Height: 2}, Kind: HitActivate}}},
		{ID: "front", Z: 2, Hit: []HitRegion{{ID: "front-row", Bounds: Rect{Width: 10, Height: 2}, Kind: HitActivate}}},
	}}
	router.Publish(4, root)
	if _, ok := router.Route(3, PointerEvent{Kind: PointerClick, X: 1, Y: 1}); ok {
		t.Fatal("stale frame geometry accepted click")
	}
	event, ok := router.Route(4, PointerEvent{Kind: PointerClick, X: 1, Y: 1})
	if !ok || event.Hit == nil || event.Hit.Owner != "front" {
		t.Fatalf("topmost route=%#v ok=%v", event, ok)
	}
}

type eventSurface struct {
	Base
	handle func(Event) Result
	hit    []HitRegion
}

func (s *eventSurface) Handle(event Event) Result {
	if s.handle == nil {
		return Result{}
	}
	return s.handle(event)
}

func (s *eventSurface) Render(RenderContext) Node {
	return Node{ID: s.ComponentID, Bounds: s.Bounds, Focusable: true, Hit: s.hit}
}

type eventContainer struct {
	eventSurface
	children []Component
}

func (c *eventContainer) Components() []Component { return c.children }

func (c *eventContainer) Layout(bounds Rect) {
	c.Bounds = bounds
	for _, child := range c.children {
		child.Layout(bounds)
	}
}

func (c *eventContainer) Render(ctx RenderContext) Node {
	node := c.eventSurface.Render(ctx)
	for _, child := range c.children {
		node.Children = append(node.Children, child.Render(ctx))
	}
	return node
}

func TestRuntimeBubblesUnhandledEventsToRetainedParent(t *testing.T) {
	child := &eventSurface{Base: Base{ComponentID: "child"}}
	root := &eventContainer{
		eventSurface: eventSurface{Base: Base{ComponentID: "root"}, handle: func(event Event) Result {
			return Result{Handled: true, Actions: []Action{{Source: "root", Name: string(event.Kind)}}}
		}},
		children: []Component{child},
	}

	runtime := NewRuntime()
	runtime.BeginFrame(root, Rect{Width: 20, Height: 4})
	runtime.SetFocus("child", FocusProgrammatic)
	result := runtime.Dispatch(Event{Kind: EventPaste, Text: "hello"})
	if !result.Handled || len(result.Actions) != 1 || result.Actions[0].Name != string(EventPaste) {
		t.Fatalf("bubbled result=%#v", result)
	}
}

func TestRuntimeDispatchRejectsPointerFromStalePublishedFrame(t *testing.T) {
	child := &eventSurface{Base: Base{ComponentID: "child"}, handle: func(Event) Result {
		return Result{Handled: true}
	}, hit: []HitRegion{{ID: "child-hit", Owner: "child", Bounds: Rect{Width: 20, Height: 4}, Kind: HitActivate}}}
	root := &eventContainer{eventSurface: eventSurface{Base: Base{ComponentID: "root"}}, children: []Component{child}}
	runtime := NewRuntime()
	first := runtime.BeginFrame(root, Rect{Width: 20, Height: 4})
	runtime.BeginFrame(root, Rect{Width: 20, Height: 4})
	result := runtime.Dispatch(Event{
		Kind: EventPointer, FrameGeneration: first.Generation,
		Pointer: PointerEvent{Kind: PointerClick, X: 1, Y: 1},
	})
	if result.Handled {
		t.Fatalf("stale pointer result=%#v", result)
	}
}

func TestCursorArbiterAcceptsFocusedOwnerOnly(t *testing.T) {
	requests := []CursorRequest{{Owner: "background", X: 1, Y: 1, Visible: true}, {Owner: "dialog", X: 4, Y: 5, Visible: true}}
	cursor := (CursorArbiter{}).Resolve(requests, "dialog")
	if cursor == nil || cursor.Owner != "dialog" || cursor.X != 4 || cursor.Y != 5 {
		t.Fatalf("cursor=%#v", cursor)
	}
	if cursor := (CursorArbiter{}).Resolve(requests, "missing"); cursor != nil {
		t.Fatalf("unfocused cursor=%#v", cursor)
	}
}

func TestInvalidationRejectsOldComponentAndTargetGenerations(t *testing.T) {
	registry := InvalidationRegistry{}
	registry.SetTargetGeneration(2)
	first := registry.Mount("navigator")
	if !registry.Accept(first) {
		t.Fatal("fresh token rejected")
	}
	registry.Invalidate("navigator")
	if registry.Accept(first) {
		t.Fatal("invalidated component token accepted")
	}
	second := registry.Revise("navigator")
	registry.SetTargetGeneration(3)
	if registry.Accept(second) {
		t.Fatal("old target token accepted")
	}
}

func TestActionListClickSelectsBeforeActivation(t *testing.T) {
	list := &ActionList{Base: Base{ComponentID: "list"}, Items: []ActionItem{{ID: "one", Label: "One"}, {ID: "two", Label: "Two"}}}
	list.Layout(Rect{Width: 20, Height: 2})
	node := list.Render(RenderContext{})
	hit := node.Hit[1]
	first := list.Handle(Event{Kind: EventPointer, Pointer: PointerEvent{Kind: PointerClick, Hit: &hit}})
	if len(first.Actions) != 1 || first.Actions[0].Name != "selected" || list.Selected != 1 {
		t.Fatalf("first click=%#v selected=%d", first, list.Selected)
	}
	second := list.Handle(Event{Kind: EventPointer, Pointer: PointerEvent{Kind: PointerClick, Hit: &hit}})
	if len(second.Actions) != 1 || second.Actions[0].Name != "activate" {
		t.Fatalf("second click=%#v", second)
	}
}

type disposableSurface struct {
	TextSurface
	disposed bool
}

func (d *disposableSurface) Dispose() { d.disposed = true }

func TestUnmountRecursivelyDisposesAndInvalidatesInteractionState(t *testing.T) {
	child := &disposableSurface{TextSurface: TextSurface{Base: Base{ComponentID: "child"}, Content: "child"}}
	root := NewContainer("root", child)
	runtime := NewRuntime()
	runtime.Mount(root)
	runtime.PushOverlay("dialog", "child", true)
	runtime.BeginFrame(root, Rect{Width: 20, Height: 4})
	runtime.Unmount("root")
	if !child.disposed {
		t.Fatal("child component was not disposed")
	}
	if runtime.Overlays.Len() != 0 || runtime.Focus.Current() != "" || runtime.Pointer.Generation() != 0 {
		t.Fatalf("teardown left interaction state: overlays=%d focus=%q pointer=%d", runtime.Overlays.Len(), runtime.Focus.Current(), runtime.Pointer.Generation())
	}
}

func TestTabsUseTerminalCellWidthForCJKHitGeometry(t *testing.T) {
	tabs := &Tabs{Base: Base{ComponentID: "tabs"}, Items: []Tab{{ID: "cn", Label: "当前"}, {ID: "all", Label: "All"}}}
	tabs.Layout(Rect{X: 3, Y: 2, Width: 20, Height: 1})
	node := tabs.Render(RenderContext{})
	if got := node.Hit[0].Bounds.Width; got != 6 { // selected brackets + two double-width glyphs
		t.Fatalf("selected CJK tab width=%d", got)
	}
	if got := node.Hit[1].Bounds.X; got != 11 {
		t.Fatalf("second tab x=%d", got)
	}
}
