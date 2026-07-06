package history

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAppendAndRecentTail(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "hist"))
	for i := 0; i < 5; i++ {
		if err := h.Append(fmt.Sprintf("e%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.Recent(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "e2" || got[2] != "e4" {
		t.Fatalf("Recent(3) should be the oldest-first tail, got %v", got)
	}
	all, _ := h.Recent(0)
	if len(all) != 5 {
		t.Fatalf("Recent(0) should return all, got %d", len(all))
	}
}

func TestAppendCollapsesAndSkipsBlank(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "hist"))
	h.Append("line1\nline2")
	h.Append("   ")
	h.Append("")
	got, _ := h.Recent(0)
	if len(got) != 1 || got[0] != "line1 line2" {
		t.Fatalf("multiline collapse / blank-skip wrong: %v", got)
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "nope"))
	got, err := h.Recent(0)
	if err != nil || got != nil {
		t.Fatalf("missing file should be empty+no error, got %v err=%v", got, err)
	}
}

// TestConcurrentAppendNoCorruption proves O_APPEND keeps concurrent writers'
// lines intact (the cross-process safety property).
func TestConcurrentAppendNoCorruption(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "hist"))
	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = h.Append(fmt.Sprintf("entry-%04d", i))
		}(i)
	}
	wg.Wait()

	got, _ := h.Recent(0)
	if len(got) != N {
		t.Fatalf("expected %d entries, got %d", N, len(got))
	}
	seen := map[string]bool{}
	for _, l := range got {
		if !strings.HasPrefix(l, "entry-") || len(l) != len("entry-0000") {
			t.Fatalf("corrupted/interleaved line: %q", l)
		}
		seen[l] = true
	}
	if len(seen) != N {
		t.Fatalf("expected %d distinct clean lines, got %d", N, len(seen))
	}
}
