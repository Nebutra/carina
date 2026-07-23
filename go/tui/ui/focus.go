package ui

type FocusSnapshot struct {
	Current ComponentID
	Order   []ComponentID
}

type FocusManager struct {
	current ComponentID
	order   []ComponentID
}

func (f *FocusManager) Current() ComponentID { return f.current }

func (f *FocusManager) SetOrder(order []ComponentID) {
	seen := make(map[ComponentID]struct{}, len(order))
	f.order = f.order[:0]
	for _, id := range order {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		f.order = append(f.order, id)
	}
	if f.current != "" && !containsComponent(f.order, f.current) {
		f.current = ""
	}
}

func (f *FocusManager) Focus(id ComponentID) (previous ComponentID, changed bool) {
	if id == "" || id == f.current {
		return f.current, false
	}
	previous, f.current = f.current, id
	return previous, true
}

func (f *FocusManager) Clear() ComponentID {
	previous := f.current
	f.current = ""
	return previous
}

func (f *FocusManager) Cycle(delta int) ComponentID {
	if len(f.order) == 0 {
		f.current = ""
		return ""
	}
	index := 0
	for i, id := range f.order {
		if id == f.current {
			index = i
			break
		}
	}
	if f.current == "" {
		if delta < 0 {
			index = len(f.order) - 1
		}
	} else {
		index = (index + delta) % len(f.order)
		if index < 0 {
			index += len(f.order)
		}
	}
	f.current = f.order[index]
	return f.current
}

func (f *FocusManager) Snapshot() FocusSnapshot {
	return FocusSnapshot{Current: f.current, Order: append([]ComponentID(nil), f.order...)}
}

func (f *FocusManager) Restore(snapshot FocusSnapshot) {
	f.SetOrder(snapshot.Order)
	if snapshot.Current != "" && containsComponent(f.order, snapshot.Current) {
		f.current = snapshot.Current
	}
}

func containsComponent(values []ComponentID, target ComponentID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
