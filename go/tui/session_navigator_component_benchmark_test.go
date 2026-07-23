package tui

import (
	"fmt"
	"testing"
)

func BenchmarkNavigator1000RowsInputAndRender(b *testing.B) {
	m, _ := newTestModel(&fakeCaller{})
	defer m.Close()
	m.width, m.height = 120, 36
	items := make([]sessionListItem, 1000)
	for i := range items {
		items[i] = sessionListItem{SessionID: fmt.Sprintf("sess_%04d", i), Name: fmt.Sprintf("Workspace session %04d", i), Status: "paused"}
	}
	m.sessionPicker = &sessionPickerState{items: items, status: m.text(MsgSessionPickerHelp, nil)}
	m.layout()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.sessionPicker.selected = i % len(items)
		m.ensureNavigatorFrame()
	}
}

func BenchmarkNavigator1000RowsSearch(b *testing.B) {
	m, _ := newTestModel(&fakeCaller{})
	defer m.Close()
	m.width, m.height = 120, 36
	items := make([]sessionListItem, 1000)
	for i := range items {
		items[i] = sessionListItem{SessionID: fmt.Sprintf("sess_%04d", i), Name: fmt.Sprintf("Workspace session %04d", i), Status: "paused"}
	}
	m.sessionPicker = &sessionPickerState{items: items, query: "099", searching: true, status: m.text(MsgSessionPickerHelp, nil)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ensureNavigatorFrame()
	}
}
