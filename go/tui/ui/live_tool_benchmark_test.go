package ui

import (
	"fmt"
	"testing"
)

func BenchmarkLiveToolRegistryUpdate(b *testing.B) {
	for _, size := range []int{2000, 10000} {
		b.Run(fmt.Sprintf("cells_%d", size), func(b *testing.B) {
			registry := NewLiveToolRegistry()
			for i := 0; i < size; i++ {
				registry.Observe(LiveToolUpdate{CallID: fmt.Sprintf("call_%d", i), Tool: "read", Status: LiveToolRunning, Summary: "file.go"})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id := fmt.Sprintf("call_%d", i%size)
				registry.Observe(LiveToolUpdate{CallID: id, Status: LiveToolRunning, Summary: "updated"})
			}
		})
	}
}
