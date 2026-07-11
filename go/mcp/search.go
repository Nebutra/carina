package mcp

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

// ToolMatch is one search hit over the connected servers' tool metadata. It
// carries the full InputSchema so a caller can surface the exact argument
// contract on demand — the always-injected prompt index deliberately omits
// schemas to keep the stable prompt prefix small, so this is the only
// agent-facing path to an MCP tool's input schema.
type ToolMatch struct {
	Server      string
	Name        string
	Description string
	InputSchema json.RawMessage
	Score       float64
}

// defaultSearchLimit bounds SearchTools when the caller passes limit <= 0.
const defaultSearchLimit = 6

// Field weights for weighted token overlap: a query token matching the tool
// (or server) name outranks one matching the description, which outranks one
// matching flattened schema text.
const (
	nameWeight   = 3.0
	descWeight   = 2.0
	schemaWeight = 1.0
)

// SearchTools ranks the connected public servers' tools against a free-text
// query by weighted token overlap (name > description > schema property
// names/descriptions) and returns the top matches with their full input
// schemas. Hidden (private) servers are excluded exactly as in Tools(). The
// search is stateless: it is recomputed per call from each Client's tools,
// which were fetched at connect time, so there is no index to invalidate.
// Ties break deterministically by server then tool name. An empty (or
// token-free) query matches nothing.
func (m *Manager) SearchTools(query string, limit int) []ToolMatch {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}
	m.mu.Lock()
	var out []ToolMatch
	for name, c := range m.clients {
		if m.hidden[name] {
			continue
		}
		c.mu.Lock()
		for _, t := range c.tools {
			score := scoreTool(queryTokens, name, t)
			if score <= 0 {
				continue
			}
			out = append(out, ToolMatch{
				Server:      name,
				Name:        t.Name,
				Description: t.Description,
				InputSchema: append(json.RawMessage(nil), t.InputSchema...),
				Score:       score,
			})
		}
		c.mu.Unlock()
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// scoreTool scores one tool against the query tokens. Each query token counts
// once, at the weight of the best field it appears in (name, then
// description, then flattened schema text). Tokenization splits on any
// non-alphanumeric rune, so an underscore/dash-composed tool name like
// "web_search" already contributes its component words.
func scoreTool(queryTokens []string, server string, t Tool) float64 {
	nameTokens := tokenSet(server + " " + t.Name)
	descTokens := tokenSet(t.Description)
	schemaTokens := tokenSet(flattenSchemaText(t.InputSchema))
	var score float64
	for _, q := range queryTokens {
		switch {
		case nameTokens[q]:
			score += nameWeight
		case descTokens[q]:
			score += descWeight
		case schemaTokens[q]:
			score += schemaWeight
		}
	}
	return score
}

// flattenSchemaText extracts searchable text from a JSON Schema object:
// description strings and property names, walking nested "properties" (and
// array "items") recursively. Property names are emitted in sorted order so
// the output is deterministic. A missing or malformed schema yields "".
func flattenSchemaText(raw json.RawMessage) string {
	var node map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &node) != nil {
		return ""
	}
	var parts []string
	var walk func(n map[string]any, depth int)
	walk = func(n map[string]any, depth int) {
		if depth > 8 {
			return // defensive bound against pathological nesting
		}
		if desc, ok := n["description"].(string); ok {
			parts = append(parts, desc)
		}
		if props, ok := n["properties"].(map[string]any); ok {
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				parts = append(parts, k)
				if child, ok := props[k].(map[string]any); ok {
					walk(child, depth+1)
				}
			}
		}
		if items, ok := n["items"].(map[string]any); ok {
			walk(items, depth+1)
		}
	}
	walk(node, 0)
	return strings.Join(parts, " ")
}

// tokenize lowercases s and splits it into alphanumeric tokens (underscores,
// dots, dashes, and every other non-alphanumeric rune are separators).
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func tokenSet(s string) map[string]bool {
	set := make(map[string]bool)
	for _, tok := range tokenize(s) {
		set[tok] = true
	}
	return set
}
