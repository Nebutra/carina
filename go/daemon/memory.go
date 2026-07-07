package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// memoryBudget bounds how much project instruction text is injected into the
// system prompt, so a large repository doc can't crowd out the task/transcript.
const memoryBudget = 8000

var projectInstructionCandidates = []string{
	"CARINA.override.md",
	filepath.Join(".carina", "CARINA.override.md"),
	"CARINA.md",
	filepath.Join(".carina", "CARINA.md"),
	"AGENTS.override.md",
	"AGENTS.md",
}

// loadMemory concatenates persistent user/project instructions. Carina-native
// files win, while AGENTS.md remains a compatibility fallback for shared coding
// agent repos. Project docs are loaded from the repository root down to the
// session workspace, with one winning candidate per directory.
func loadMemory(ws string) string {
	var parts []string
	add := func(label, path string) {
		if raw, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			parts = append(parts, "## "+label+"\n"+strings.TrimSpace(string(raw)))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add("user (~/.carina/CARINA.md)", filepath.Join(home, ".carina", "CARINA.md"))
	}
	if ws != "" {
		for _, dir := range projectInstructionDirs(ws) {
			addProjectInstruction(&parts, ws, dir)
		}
	}
	mem := strings.Join(parts, "\n\n")
	if len(mem) > memoryBudget {
		mem = mem[:memoryBudget] + "\n…[memory truncated]"
	}
	return mem
}

func addProjectInstruction(parts *[]string, ws, dir string) {
	for _, candidate := range projectInstructionCandidates {
		path := filepath.Join(dir, candidate)
		raw, err := os.ReadFile(path)
		if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		label := "project (" + projectInstructionLabel(ws, path) + ")"
		*parts = append(*parts, "## "+label+"\n"+strings.TrimSpace(string(raw)))
		return
	}
}

func projectInstructionDirs(ws string) []string {
	abs, err := filepath.Abs(ws)
	if err != nil {
		abs = ws
	}
	root := nearestProjectRoot(abs)
	dirs := []string{abs}
	for cursor := abs; cursor != root; {
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
		cursor = parent
		dirs = append(dirs, cursor)
	}
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

func nearestProjectRoot(dir string) string {
	cursor := dir
	for {
		if _, err := os.Stat(filepath.Join(cursor, ".git")); err == nil {
			return cursor
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return dir
		}
		cursor = parent
	}
}

func projectInstructionLabel(ws, path string) string {
	base := ws
	if root := nearestProjectRoot(ws); root != "" {
		base = root
	}
	if rel, err := filepath.Rel(base, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

// loadStyle returns an optional output-style directive (Carina's output-styles
// analogue) that shapes the agent's presentation/persona. Project style
// (<ws>/.carina/output-style.md) overrides the user style
// (~/.carina/output-style.md).
func loadStyle(ws string) string {
	read := func(path string) string {
		if raw, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(raw))
		}
		return ""
	}
	if ws != "" {
		if s := read(filepath.Join(ws, ".carina", "output-style.md")); s != "" {
			return s
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if s := read(filepath.Join(home, ".carina", "output-style.md")); s != "" {
			return s
		}
	}
	return ""
}
