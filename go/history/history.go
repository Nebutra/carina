// Package history is a shared, append-only prompt/command history that multiple
// daemon processes can write concurrently. Appends use O_APPEND, which is atomic
// for small line writes on POSIX, so entries from different processes/goroutines
// interleave cleanly line-by-line without a lock.
package history

import (
	"fmt"
	"os"
	"strings"
)

// History is a cross-process history file.
type History struct {
	path string
}

// New builds a history backed by path (created on first Append).
func New(path string) *History { return &History{path: path} }

// Append adds one entry. Newlines are collapsed to spaces so each entry is a
// single line; blank entries are dropped.
func (h *History) Append(entry string) error {
	line := strings.TrimSpace(strings.ReplaceAll(entry, "\n", " "))
	if line == "" {
		return nil
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

// Recent returns the last n entries oldest-first (all when n<=0). A missing file
// yields no entries and no error.
func (h *History) Recent(n int) ([]string, error) {
	data, err := os.ReadFile(h.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			lines = append(lines, l)
		}
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
