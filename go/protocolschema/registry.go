// Package protocolschema validates the checked-in JSON-RPC registry and emits
// a JSON Schema suitable for CI and SDK generators.
package protocolschema

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Method struct {
	Method string `json:"method"`
	Scope  string `json:"scope"`
	Remote bool   `json:"remote"`
	Params any    `json:"params"`
	Result any    `json:"result"`
	Stream bool   `json:"stream,omitempty"`
}
type Registry struct {
	Version string              `json:"version"`
	APIs    map[string][]Method `json:"apis"`
}

func Load(path string) (Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, err
	}
	var r Registry
	if err = json.Unmarshal(raw, &r); err != nil {
		return Registry{}, err
	}
	return r, Validate(r)
}
func Validate(r Registry) error {
	if r.Version == "" {
		return errors.New("protocol registry version is required")
	}
	seen := map[string]bool{}
	for group, methods := range r.APIs {
		if group == "" {
			return errors.New("empty API group")
		}
		for _, m := range methods {
			if m.Method == "" || !strings.Contains(m.Method, ".") {
				return fmt.Errorf("invalid method %q", m.Method)
			}
			if seen[m.Method] {
				return fmt.Errorf("duplicate method %q", m.Method)
			}
			seen[m.Method] = true
			switch m.Scope {
			case "read", "write", "admin", "worker", "stream":
			default:
				return fmt.Errorf("method %s has invalid scope %q", m.Method, m.Scope)
			}
		}
	}
	return nil
}
func Methods(r Registry) []string {
	var out []string
	for _, ms := range r.APIs {
		for _, m := range ms {
			out = append(out, m.Method)
		}
	}
	sort.Strings(out)
	return out
}
func JSONSchema() map[string]any {
	return map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "title": "Carina JSON-RPC method registry", "type": "object", "required": []string{"version", "apis"}, "properties": map[string]any{"version": map[string]any{"type": "string", "pattern": "^[0-9]+\\.[0-9]+\\.[0-9]+$"}, "apis": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"method", "scope", "remote", "params", "result"}, "properties": map[string]any{"method": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_.]+$"}, "scope": map[string]any{"enum": []string{"read", "write", "admin", "worker", "stream"}}, "remote": map[string]any{"type": "boolean"}, "params": map[string]any{}, "result": map[string]any{}}}}}}}
}
