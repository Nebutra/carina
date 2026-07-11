package daemon

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var templateTokenRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// interpolateStreaming extends interpolate() (used by the batch/BSP
// scheduler, left completely untouched — see workflow.go) with dotted-path
// field access: ${step_id.field.path} substitutes a value looked up inside
// that step's JSON-parsed output, not just the whole raw string. Whole-token
// exact step ID matches ("${step_id}", "${input}") are resolved FIRST, via
// the exact same literal-substring logic interpolate() has always used —
// this matters because nothing forbids a step ID from containing a ".", so
// a literal step ID with a dot in it must still resolve as a whole token
// rather than being misparsed as "id.fieldpath".
func interpolateStreaming(s, input string, outputs map[string]string) string {
	s = strings.ReplaceAll(s, "${input}", input)
	for id, out := range outputs {
		s = strings.ReplaceAll(s, "${"+id+"}", out)
	}
	return templateTokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		inner := tok[2 : len(tok)-1] // strip leading "${" and trailing "}"
		dot := strings.IndexByte(inner, '.')
		if dot < 0 {
			return tok // not a dotted path and not a known whole id (already handled above) — leave unresolved tokens as-is, matching interpolate()'s existing behavior
		}
		stepID, path := inner[:dot], inner[dot+1:]
		out, ok := outputs[stepID]
		if !ok {
			return tok
		}
		val := lookupPath(parseStepOutputAsData(out), path)
		if val == nil {
			return tok
		}
		return stringifyValue(val)
	})
}

func stringifyValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// wholeTokenValue resolves tmpl to a TYPED value (not stringified) when tmpl
// is exactly one ${...} token with nothing else around it — the case
// resolveStructuredInput wants, so a caller referencing ${review.approved}
// gets a real JSON boolean in the structured input block, not the string
// "true".
func wholeTokenValue(tmpl, input string, outputs map[string]string) (any, bool) {
	if !strings.HasPrefix(tmpl, "${") || !strings.HasSuffix(tmpl, "}") {
		return nil, false
	}
	inner := tmpl[2 : len(tmpl)-1]
	if strings.Contains(inner, "${") {
		return nil, false // not a single bare token
	}
	if inner == "input" {
		return input, true
	}
	dot := strings.IndexByte(inner, '.')
	if dot < 0 {
		out, ok := outputs[inner]
		if !ok {
			return nil, false
		}
		return out, true
	}
	stepID, path := inner[:dot], inner[dot+1:]
	out, ok := outputs[stepID]
	if !ok {
		return nil, false
	}
	return lookupPath(parseStepOutputAsData(out), path), true
}

// resolveStructuredInput resolves WorkflowStep.Input into a JSON-typed map:
// a value that is exactly one ${...} token resolves to its real typed value
// (string/number/bool/object/array); any other value is treated as a
// template string and interpolated the same way Task is.
func resolveStructuredInput(spec map[string]string, input string, outputs map[string]string) map[string]any {
	if len(spec) == 0 {
		return nil
	}
	out := make(map[string]any, len(spec))
	for key, tmpl := range spec {
		if v, ok := wholeTokenValue(tmpl, input, outputs); ok {
			out[key] = v
			continue
		}
		out[key] = interpolateStreaming(tmpl, input, outputs)
	}
	return out
}

// formatStructuredInput resolves spec and renders it as a labeled JSON block
// appended after a step's (already-interpolated) task text — additive
// structured data on top of the plain-text instruction, not a replacement.
func formatStructuredInput(spec map[string]string, input string, outputs map[string]string) (string, error) {
	resolved := resolveStructuredInput(spec, input, outputs)
	b, err := json.MarshalIndent(resolved, "", "  ")
	if err != nil {
		return "", err
	}
	return "\n\nSTRUCTURED INPUT (JSON):\n" + string(b), nil
}
