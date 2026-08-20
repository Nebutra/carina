package daemon

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/toolchain"
)

func TestFormatSearchObservationGroupsByFileWithWhy(t *testing.T) {
	matches := []toolchain.Match{
		{File: "go/daemon/agent.go", Line: 10, Text: "case \"search\":"},
		{File: "go/daemon/agent.go", Line: 40, Text: "search workspace"},
		{File: "go/daemon/explore.go", Line: 3, Text: "search the workspace"},
		{File: "/abs/ws/docs/README.md", Line: 1, Text: "  search   tools  "},
	}
	got := formatSearchObservation("search", matches, "/abs/ws")
	if !strings.HasPrefix(got, `search "search": 4 matches in 3 files`) {
		t.Fatalf("header = %q", firstLine(got))
	}
	if strings.Count(got, "\n- ") != 3 {
		t.Fatalf("want 3 file lines, got:\n%s", got)
	}
	if !strings.Contains(got, "- go/daemon/agent.go (2): L10 case \"search\":") {
		t.Fatalf("grouped why missing:\n%s", got)
	}
	if !strings.Contains(got, "- docs/README.md (1): L1 search tools") {
		t.Fatalf("relative path / collapsed why missing:\n%s", got)
	}
	if strings.Contains(got, "go/daemon/agent.go:40:") {
		t.Fatal("must not dump every raw grep line")
	}
}

func TestFormatSearchObservationCapsFiles(t *testing.T) {
	var matches []toolchain.Match
	for i := 0; i < maxSearchExtractFiles+5; i++ {
		matches = append(matches, toolchain.Match{File: fmt.Sprintf("pkg%d/a.go", i), Line: 1, Text: "hit"})
	}
	got := formatSearchObservation("hit", matches, "")
	if !strings.Contains(got, "files omitted") {
		t.Fatalf("file cap must be visible:\n%s", got)
	}
	if strings.Count(got, "\n- ") > maxSearchExtractFiles {
		t.Fatalf("too many file lines:\n%s", got)
	}
}

func TestFormatSearchObservationEmpty(t *testing.T) {
	if got := formatSearchObservation("x", nil, ""); got != "no matches" {
		t.Fatalf("empty = %q", got)
	}
}

func TestFormatListObservationRollsUpDirs(t *testing.T) {
	files := []toolchain.FileEntry{
		{Path: "AGENTS.md", Size: 1200, Language: "markdown"},
		{Path: "go/daemon/agent.go", Size: 8000, Language: "go"},
		{Path: "go/scheduler/scheduler.go", Size: 4000, Language: "go"},
		{Path: "docs/PROMPT_SPEC.md", Size: 2000, Language: "markdown"},
		{Path: "/abs/ws/crates/carina-tui/src/lib.rs", Size: 100, Language: "rust"},
	}
	got := formatListObservation(files, false, "/abs/ws")
	if !strings.HasPrefix(got, "list: 5 files in 3 dirs") {
		t.Fatalf("header = %q", firstLine(got))
	}
	if !strings.Contains(got, "- AGENTS.md (1200 B, markdown)") {
		t.Fatalf("root file why missing:\n%s", got)
	}
	if !strings.Contains(got, "- go/ (2): go/daemon/agent.go") {
		t.Fatalf("dir rollup missing:\n%s", got)
	}
	if strings.Count(got, "go/daemon/agent.go") != 1 {
		t.Fatalf("nested files must not also list at top level:\n%s", got)
	}
	lines := strings.Count(got, "\n- ")
	if lines > 6 {
		t.Fatalf("list dump still too long (%d lines):\n%s", lines, got)
	}
}

func TestFormatListObservationCapsRootFilesAndDirs(t *testing.T) {
	var files []toolchain.FileEntry
	for i := 0; i < maxListRootFiles+4; i++ {
		files = append(files, toolchain.FileEntry{Path: "f" + string(rune('a'+i)) + ".txt", Size: 1})
	}
	for i := 0; i < maxListDirs+3; i++ {
		name := "d" + string(rune('a'+i))
		files = append(files, toolchain.FileEntry{Path: name + "/x.go", Size: 2, Language: "go"})
	}
	got := formatListObservation(files, true, "")
	if !strings.Contains(got, "truncated at") || !strings.Contains(got, "more; use search or code.map") {
		t.Fatalf("caps must be visible:\n%s", got)
	}
	if strings.Count(got, "\n- ") > maxListRootFiles+maxListDirs {
		t.Fatalf("too many list lines:\n%s", got)
	}
	if strings.Count(got, "\n- ") == len(files) {
		t.Fatal("must not emit one line per file")
	}
}

func TestDisplayRelPath(t *testing.T) {
	root := filepath.FromSlash("/abs/ws")
	got := displayRelPath(filepath.Join(root, "go", "daemon", "agent.go"), root)
	if got != "go/daemon/agent.go" {
		t.Fatalf("rel = %q", got)
	}
	if displayRelPath("already/rel.go", root) != "already/rel.go" {
		t.Fatalf("relative path must stay: %q", displayRelPath("already/rel.go", root))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
