// Package history is a shared, append-only prompt/command history that multiple
// daemon processes can write concurrently. Appends use O_APPEND, which is atomic
// for small line writes on POSIX, so entries from different processes/goroutines
// interleave cleanly line-by-line without a lock.
package history

import (
	"encoding/json"
	"os"
	"strings"
)

// Entry is one durable prompt together with the runtime scope that produced
// it. Historical files contained plain text lines; readers continue to accept
// those as unscoped entries so upgrades never discard recall history.
type Entry struct {
	Text          string `json:"text"`
	SessionID     string `json:"session_id,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
}

type diskEntry struct {
	Version int `json:"v"`
	Entry
}

// History is a cross-process history file.
type History struct {
	path string
}

// New builds a history backed by path (created on first Append).
func New(path string) *History { return &History{path: path} }

// Append adds one entry. Newlines are collapsed to spaces so each entry is a
// single line; blank entries are dropped.
func (h *History) Append(entry string) error {
	return h.AppendScoped(Entry{Text: entry})
}

// AppendScoped adds one metadata-bearing entry. A complete JSON object is
// emitted with one O_APPEND write, preserving the existing multi-process
// line-atomicity contract.
func (h *History) AppendScoped(entry Entry) error {
	entry.Text = normalize(entry.Text)
	if entry.Text == "" {
		return nil
	}
	line, err := json.Marshal(diskEntry{Version: 1, Entry: entry})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line = append(line, '\n')
	_, err = f.Write(line)
	return err
}

// Recent returns the last n entries oldest-first (all when n<=0). A missing file
// yields no entries and no error.
func (h *History) Recent(n int) ([]string, error) {
	entries, err := h.RecentEntries(n)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Text)
	}
	return out, nil
}

// RecentEntries returns the last n structured entries oldest-first. Legacy
// text lines are surfaced with empty scope metadata.
func (h *History) RecentEntries(n int) ([]Entry, error) {
	data, err := os.ReadFile(h.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			var stored diskEntry
			entry := stored.Entry
			if json.Unmarshal([]byte(l), &stored) != nil || stored.Version != 1 || normalize(stored.Text) == "" {
				entry = Entry{Text: l}
			} else {
				entry = stored.Entry
			}
			entry.Text = normalize(entry.Text)
			if entry.Text != "" {
				entries = append(entries, entry)
			}
		}
	}
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}

func normalize(entry string) string {
	entry = strings.ReplaceAll(entry, "\r\n", "\n")
	entry = strings.ReplaceAll(entry, "\r", "\n")
	return strings.TrimSpace(strings.ReplaceAll(entry, "\n", " "))
}
