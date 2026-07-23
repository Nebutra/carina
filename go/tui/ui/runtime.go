package ui

type ScreenState struct {
	ID         ScreenID
	Root       ComponentID
	Generation uint64
}

type TransitionReceipt struct {
	From, To ScreenID
	Focus    FocusSnapshot
	State    map[string]any
}

type ScreenRouter struct {
	current ScreenState
}

func (r *ScreenRouter) Current() ScreenState { return r.current }

func (r *ScreenRouter) Transition(id ScreenID, root ComponentID, focus FocusSnapshot, state map[string]any) TransitionReceipt {
	receipt := TransitionReceipt{From: r.current.ID, To: id, Focus: focus, State: state}
	r.current = ScreenState{ID: id, Root: root, Generation: r.current.Generation + 1}
	return receipt
}

type InvalidationToken struct {
	Component           ComponentID
	ComponentGeneration uint64
	TargetGeneration    uint64
	Revision            uint64
}

type invalidationState struct {
	generation uint64
	revision   uint64
}

type InvalidationRegistry struct {
	targetGeneration uint64
	components       map[ComponentID]invalidationState
}

func (r *InvalidationRegistry) SetTargetGeneration(generation uint64) {
	if generation == r.targetGeneration {
		return
	}
	r.targetGeneration = generation
	for id, state := range r.components {
		state.generation++
		state.revision = 0
		r.components[id] = state
	}
}

func (r *InvalidationRegistry) TargetGeneration() uint64 { return r.targetGeneration }

func (r *InvalidationRegistry) Mount(id ComponentID) InvalidationToken {
	if r.components == nil {
		r.components = make(map[ComponentID]invalidationState)
	}
	state := r.components[id]
	if state.generation == 0 {
		state.generation = 1
		r.components[id] = state
	}
	return r.token(id, state)
}

func (r *InvalidationRegistry) Invalidate(id ComponentID) InvalidationToken {
	if r.components == nil {
		r.components = make(map[ComponentID]invalidationState)
	}
	state := r.components[id]
	state.generation++
	state.revision = 0
	r.components[id] = state
	return r.token(id, state)
}

func (r *InvalidationRegistry) Revise(id ComponentID) InvalidationToken {
	if r.components == nil {
		r.components = make(map[ComponentID]invalidationState)
	}
	state := r.components[id]
	if state.generation == 0 {
		state.generation = 1
	}
	state.revision++
	r.components[id] = state
	return r.token(id, state)
}

func (r *InvalidationRegistry) Accept(token InvalidationToken) bool {
	state, ok := r.components[token.Component]
	return ok && token.TargetGeneration == r.targetGeneration &&
		token.ComponentGeneration == state.generation && token.Revision == state.revision
}

func (r *InvalidationRegistry) token(id ComponentID, state invalidationState) InvalidationToken {
	return InvalidationToken{
		Component: id, ComponentGeneration: state.generation,
		TargetGeneration: r.targetGeneration, Revision: state.revision,
	}
}

type CursorArbiter struct{}

func (CursorArbiter) Resolve(requests []CursorRequest, focused ComponentID) *CursorRequest {
	if focused == "" {
		return nil
	}
	for i := len(requests) - 1; i >= 0; i-- {
		if requests[i].Owner == focused && requests[i].Visible {
			cursor := requests[i]
			return &cursor
		}
	}
	return nil
}

type Frame struct {
	Generation uint64
	Root       Node
	Cursor     *CursorRequest
	Graphics   []GraphicsPlacement
	AllMotion  bool
}

type Runtime struct {
	Screens      ScreenRouter
	Focus        FocusManager
	Overlays     OverlayStack
	Pointer      PointerRouter
	Cursor       CursorArbiter
	Invalidation InvalidationRegistry

	components map[ComponentID]Component
	frame      uint64
}

func NewRuntime() *Runtime {
	return &Runtime{components: make(map[ComponentID]Component)}
}

func (r *Runtime) Mount(component Component) {
	if component == nil || component.ID() == "" {
		return
	}
	r.components[component.ID()] = component
	r.Invalidation.Mount(component.ID())
	if component.ID() == r.Focus.Current() {
		if focusable, ok := component.(Focusable); ok {
			focusable.Focus(FocusRestore)
		}
	}
	if container, ok := component.(ComponentContainer); ok {
		for _, child := range container.Components() {
			r.Mount(child)
		}
	}
}

func (r *Runtime) Unmount(id ComponentID) {
	component := r.components[id]
	if container, ok := component.(ComponentContainer); ok {
		for _, child := range container.Components() {
			if child != nil {
				r.Unmount(child.ID())
			}
		}
	}
	if disposable, ok := component.(Disposable); ok {
		disposable.Dispose()
	}
	delete(r.components, id)
	r.Invalidation.Invalidate(id)
	r.Overlays.RemoveRoot(id)
	r.Pointer.Invalidate()
	if r.Focus.Current() == id {
		r.Focus.Clear()
	}
}

func (r *Runtime) InvalidateGeometry() {
	r.Pointer.Invalidate()
}

func (r *Runtime) SetFocus(id ComponentID, reason FocusReason) bool {
	previous, changed := r.Focus.Focus(id)
	if !changed {
		return false
	}
	if component, ok := r.components[previous].(Focusable); ok {
		component.Blur(reason)
	}
	if component, ok := r.components[id].(Focusable); ok {
		component.Focus(reason)
	}
	return true
}

func (r *Runtime) PushOverlay(id, root ComponentID, modal bool) {
	r.Overlays.Push(OverlayEntry{ID: id, Root: root, Modal: modal, PreviousFocus: r.Focus.Current()})
	r.SetFocus(root, FocusOverlay)
}

func (r *Runtime) PopOverlay() (OverlayEntry, bool) {
	entry, ok := r.Overlays.Pop()
	if !ok {
		return OverlayEntry{}, false
	}
	r.SetFocus(entry.PreviousFocus, FocusRestore)
	return entry, true
}

func (r *Runtime) BeginFrame(root Component, viewport Rect) Frame {
	if root == nil {
		return Frame{}
	}
	r.Mount(root)
	root.Layout(viewport)
	r.frame++
	node := root.Render(RenderContext{
		FrameGeneration: r.frame, TargetGeneration: r.Invalidation.TargetGeneration(),
		Focused: r.Focus.Current(), Hovered: r.Pointer.Hovered(), Viewport: viewport,
	})
	order := node.FocusOrder()
	if top, ok := r.Overlays.Top(); ok && top.Modal && top.Root != "" && !containsComponent(order, top.Root) {
		order = append([]ComponentID{top.Root}, order...)
	}
	r.Focus.SetOrder(order)
	if r.Focus.Current() == "" {
		if len(order) > 0 {
			r.SetFocus(order[0], FocusProgrammatic)
		}
	}
	r.Pointer.Publish(r.frame, node)
	return Frame{
		Generation: r.frame, Root: node,
		Cursor:   r.Cursor.Resolve(node.CursorRequests(), r.Focus.Current()),
		Graphics: collectGraphics(node), AllMotion: r.Pointer.HasHoverRegions(),
	}
}

func (r *Runtime) Dispatch(event Event) Result {
	var target ComponentID
	if event.Kind == EventPointer {
		previousHoverOwner := r.Pointer.HoveredOwner()
		routed, ok := r.Pointer.Route(r.frame, event.Pointer)
		if !ok {
			if event.Pointer.Kind != PointerMove || previousHoverOwner == "" {
				return Result{}
			}
			target = previousHoverOwner
			event.Pointer.Kind = PointerLeave
		} else {
			event.Pointer = routed
			if routed.Hit != nil {
				target = routed.Hit.Owner
				if event.Pointer.Kind == PointerClick && routed.Hit.Focusable {
					r.SetFocus(target, FocusPointer)
				}
			} else {
				target = r.Focus.Current()
			}
		}
	} else if top, ok := r.Overlays.Top(); ok && top.Modal {
		target = top.Root
	} else {
		target = r.Focus.Current()
	}
	component := r.components[target]
	if component == nil {
		return Result{}
	}
	return component.Handle(event)
}

func collectGraphics(node Node) []GraphicsPlacement {
	out := append([]GraphicsPlacement(nil), node.Graphics...)
	for _, child := range node.Children {
		out = append(out, collectGraphics(child)...)
	}
	return out
}
