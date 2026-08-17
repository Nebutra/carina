package daemon

import (
	"encoding/json"
	"errors"
	"strings"

	modelrouter "github.com/Nebutra/carina/go/model-router"
)

func attachOpenAITools(body map[string]any, tools []modelrouter.ToolSpec) {
	if len(tools) == 0 {
		return
	}
	encoded := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		encoded = append(encoded, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		})
	}
	body["tools"] = encoded
	body["tool_choice"] = "auto"
}

func attachResponsesTools(body map[string]any, tools []modelrouter.ToolSpec) {
	if len(tools) == 0 {
		return
	}
	encoded := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		encoded = append(encoded, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		})
	}
	body["tools"] = encoded
}

func attachGeminiTools(body map[string]any, tools []modelrouter.ToolSpec) {
	if len(tools) == 0 {
		return
	}
	decls := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		decls = append(decls, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		})
	}
	body["tools"] = []map[string]any{{"functionDeclarations": decls}}
}

func attachAnthropicTools(body map[string]any, tools []modelrouter.ToolSpec) {
	if len(tools) == 0 {
		return
	}
	encoded := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		encoded = append(encoded, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
		})
	}
	body["tools"] = encoded
}

func encodeJSONArguments(value any) json.RawMessage {
	switch typed := value.(type) {
	case json.RawMessage:
		if len(typed) == 0 {
			return json.RawMessage(`{}`)
		}
		return typed
	case string:
		if strings.TrimSpace(typed) == "" {
			return json.RawMessage(`{}`)
		}
		if json.Valid([]byte(typed)) {
			return json.RawMessage(typed)
		}
		raw, _ := json.Marshal(typed)
		return raw
	case map[string]any:
		raw, err := json.Marshal(typed)
		if err != nil {
			return json.RawMessage(`{}`)
		}
		return raw
	default:
		if value == nil {
			return json.RawMessage(`{}`)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return json.RawMessage(`{}`)
		}
		return raw
	}
}

func toolsUnsupported(err error) bool {
	var status providerStatusError
	return errors.As(err, &status) && status.toolsUnsupported
}

func responsePromptTooLong(status int, raw []byte) bool {
	if status != 400 && status != 413 && status != 422 {
		return false
	}
	return looksLikePromptTooLongMessage(string(raw))
}

func responseToolsUnsupported(status int, raw []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	lower := strings.ToLower(string(raw))
	if !strings.Contains(lower, "tool") {
		return false
	}
	return strings.Contains(lower, "unsupported") ||
		strings.Contains(lower, "unrecognized") ||
		strings.Contains(lower, "unknown") ||
		strings.Contains(lower, "invalid") ||
		strings.Contains(lower, "not enabled")
}
