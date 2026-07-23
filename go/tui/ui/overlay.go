package ui

type OverlayEntry struct {
	ID            ComponentID
	Root          ComponentID
	Modal         bool
	PreviousFocus ComponentID
}

type OverlayStack struct {
	entries []OverlayEntry
}

func (s *OverlayStack) Push(entry OverlayEntry) {
	s.Remove(entry.ID)
	s.entries = append(s.entries, entry)
}

func (s *OverlayStack) Pop() (OverlayEntry, bool) {
	if len(s.entries) == 0 {
		return OverlayEntry{}, false
	}
	last := len(s.entries) - 1
	entry := s.entries[last]
	s.entries = s.entries[:last]
	return entry, true
}

func (s *OverlayStack) Remove(id ComponentID) (OverlayEntry, bool) {
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].ID != id {
			continue
		}
		entry := s.entries[i]
		s.entries = append(s.entries[:i], s.entries[i+1:]...)
		return entry, true
	}
	return OverlayEntry{}, false
}

func (s *OverlayStack) Top() (OverlayEntry, bool) {
	if len(s.entries) == 0 {
		return OverlayEntry{}, false
	}
	return s.entries[len(s.entries)-1], true
}

func (s *OverlayStack) Len() int { return len(s.entries) }

func (s *OverlayStack) RemoveRoot(root ComponentID) {
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].Root == root {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
		}
	}
}
