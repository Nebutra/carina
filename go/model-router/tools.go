package modelrouter

import "encoding/json"

// ToolSpec is a provider-neutral function declaration. Adapters encode it
// into OpenAI tools, Gemini functionDeclarations, or Anthropic tools.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToolCall is one native function invocation decoded from a provider response.
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}
