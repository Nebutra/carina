// Package conversationimport discovers and normalizes visible conversation
// history from supported local coding harnesses. It never mutates source data.
package conversationimport

import "time"

type Source string

const (
	SourceClaude Source = "claude-code"
	SourceCodex  Source = "codex"
)

func ParseSource(value string) (Source, bool) {
	switch Source(value) {
	case SourceClaude:
		return SourceClaude, true
	case SourceCodex:
		return SourceCodex, true
	default:
		return "", false
	}
}

type Message struct {
	ID          string    `json:"id,omitempty"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp,omitempty"`
	Fingerprint string    `json:"fingerprint"`
}

type Conversation struct {
	Source        Source    `json:"source"`
	ID            string    `json:"id"`
	Path          string    `json:"path"`
	WorkspaceRoot string    `json:"workspace_root"`
	Title         string    `json:"title"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	Messages      []Message `json:"messages,omitempty"`
	MessageCount  int       `json:"message_count"`
	Warnings      []string  `json:"warnings,omitempty"`
}

type Options struct {
	Sources       []Source
	SourceRoot    string
	WorkspaceRoot string
	AllWorkspaces bool
}

type Result struct {
	Conversations []Conversation `json:"conversations"`
	Warnings      []string       `json:"warnings,omitempty"`
}
