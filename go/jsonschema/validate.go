// Package jsonschema validates the production subset used for structured agent output.
package jsonschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

func ValidateJSON(raw string, schema json.RawMessage) []string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return []string{"$: invalid JSON: " + err.Error()}
	}
	var spec map[string]any
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.UseNumber()
	if err := dec.Decode(&spec); err != nil {
		return []string{"$: invalid schema: " + err.Error()}
	}
	var errs []string
	validate(value, spec, "$", &errs)
	return errs
}
func validate(v any, s map[string]any, path string, errs *[]string) {
	if typ, _ := s["type"].(string); typ != "" && !typeMatches(v, typ) {
		*errs = append(*errs, fmt.Sprintf("%s: expected %s", path, typ))
		return
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range enum {
			if reflect.DeepEqual(v, x) || fmt.Sprint(v) == fmt.Sprint(x) {
				found = true
			}
		}
		if !found {
			*errs = append(*errs, path+": value is not in enum")
		}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return
	}
	props, _ := s["properties"].(map[string]any)
	if req, ok := s["required"].([]any); ok {
		for _, name := range req {
			k, _ := name.(string)
			if _, exists := obj[k]; !exists {
				*errs = append(*errs, path+"."+k+": required")
			}
		}
	}
	for k, value := range obj {
		raw, known := props[k]
		if !known {
			if allow, ok := s["additionalProperties"].(bool); ok && !allow {
				*errs = append(*errs, path+"."+k+": additional property not allowed")
			}
			continue
		}
		child, ok := raw.(map[string]any)
		if ok {
			validate(value, child, path+"."+k, errs)
		}
	}
}
func typeMatches(v any, typ string) bool {
	switch typ {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n == float64(int64(n))
	case "null":
		return v == nil
	}
	return false
}
