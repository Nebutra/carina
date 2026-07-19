package continuity

import "strings"

// EffectContract is persisted with ToolCallRequested. Classification is made
// by the runtime, never trusted from model-authored arguments or MCP metadata.
type EffectContract struct {
	Class          EffectClass `json:"class"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
	ReplaySafe     bool        `json:"replay_safe"`
	Authority      string      `json:"authority"`
}

func NewEffectContract(class EffectClass, idempotencyKey string) EffectContract {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	return EffectContract{
		Class: class, IdempotencyKey: idempotencyKey,
		ReplaySafe: ReplaySafe(class, idempotencyKey), Authority: "carina-runtime-v1",
	}
}

// ClassifyTool is the built-in registry. Unknown tools and generic command or
// MCP execution fail closed; permission approval is intentionally irrelevant.
func ClassifyTool(tool string, arguments map[string]any) EffectContract {
	key, _ := arguments["idempotency_key"].(string)
	switch tool {
	case "read", "list", "search", "code.search", "code.symbols", "code.map", "code.def", "code.refs", "code.impact", "mcp_find":
		return NewEffectContract(EffectPure, "")
	case "patch":
		return NewEffectContract(EffectWorkspaceTransactional, "")
	case "memory":
		return NewEffectContract(EffectIdempotentExternal, key)
	case "ask_user":
		return NewEffectContract(EffectNonIdempotent, "")
	default:
		return NewEffectContract(EffectUnknown, "")
	}
}

// ReplaySafe is deliberately stricter than permission/risk classification.
// An external effect needs a stable idempotency key; unknown and merely
// non-idempotent effects always require reconciliation or operator review.
func ReplaySafe(class EffectClass, idempotencyKey string) bool {
	switch class {
	case EffectPure, EffectWorkspaceTransactional:
		return true
	case EffectIdempotentExternal:
		return idempotencyKey != ""
	default:
		return false
	}
}
