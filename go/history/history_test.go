package history

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	_ = h.Append("line1\nline2")
	_ = h.Append("   ")
	_ = h.Append("")
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
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = h.Append(fmt.Sprintf("entry-%04d", i))
		}(i)
	}
	wg.Wait()

	got, _ := h.Recent(0)
	if len(got) != n {
		t.Fatalf("expected %d entries, got %d", n, len(got))
	}
	seen := map[string]bool{}
	for _, line := range got {
		if !strings.HasPrefix(line, "entry-") || len(line) != len("entry-0000") {
			t.Fatalf("corrupted/interleaved line: %q", line)
		}
		seen[line] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct clean lines, got %d", n, len(seen))
	}
}

func TestStructuredHistoryReadsLegacyLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(path, []byte("legacy prompt\n{\"text\":\"legacy json prompt\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := New(path)
	if err := h.AppendScoped(Entry{
		Text: "scoped\nprompt", SessionID: "sess_1", WorkspaceRoot: "/workspace",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := h.RecentEntries(10)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Text: "legacy prompt"},
		{Text: `{"text":"legacy json prompt"}`},
		{Text: "scoped prompt", SessionID: "sess_1", WorkspaceRoot: "/workspace"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %#v, want %#v", got, want)
	}
}

func TestRecentStructuredHistoryRetainsNewestEntries(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "history"))
	for _, text := range []string{"one", "two", "three"} {
		if err := h.AppendScoped(Entry{Text: text, SessionID: "sess"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.Recent(2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"two", "three"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recent = %v, want %v", got, want)
	}
}
