package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// memoryBudget bounds how much CARINA.md memory is injected into the system
// prompt, so a large memory file can't crowd out the task/transcript.
const memoryBudget = 8000

// loadMemory concatenates persistent project/user memory (CARINA.md), the
// Carina analogue of CLAUDE.md: user memory from ~/.carina/CARINA.md, then
// project memory from <ws>/CARINA.md and <ws>/.carina/CARINA.md. The result is
// budget-truncated. Empty if no memory files exist.
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
		add("project (CARINA.md)", filepath.Join(ws, "CARINA.md"))
		add("project (.carina/CARINA.md)", filepath.Join(ws, ".carina", "CARINA.md"))
	}
	mem := strings.Join(parts, "\n\n")
	if len(mem) > memoryBudget {
		mem = mem[:memoryBudget] + "\n…[memory truncated]"
	}
	return mem
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
