package daemon

import (
	"encoding/json"
	"fmt"
)

// evalCondition evaluates a small JSONLogic-compatible boolean expression
// against a data context — WorkflowStep.When. This is deliberately a
// hand-written, minimal evaluator over parsed JSON rather than a vendored
// third-party JSONLogic library or any form of executable code: every
// operator it supports is enumerated below, so the whole attack/audit
// surface is readable in one file, matching this project's established
// principle (see the public-subagent-dsl and Agent Swarm design decisions)
// that graph control flow must be sandboxed data, never code. The syntax is
// a genuine subset of real JSONLogic (https://jsonlogic.com/) — {"var":
// "a.b.c"} for dot-path lookup, {"==":[x,y]} etc. for comparisons, "and"/
// "or"/"not" for boolean combinators — so expressions remain forward
// compatible if a fuller JSONLogic engine is ever substituted in.
//
// Truthiness follows JSONLogic: false, nil, 0, "", and empty arrays/objects
// are falsy; everything else (including negative numbers and "false" the
// string) is truthy.
//
// Fails closed: a malformed expression or unknown operator is an error, and
// callers must treat an error the same as "condition not satisfied" (skip
// the step) rather than defaulting to true.
func evalCondition(expr json.RawMessage, data map[string]any) (bool, error) {
	var node any
	if err := json.Unmarshal(expr, &node); err != nil {
		return false, fmt.Errorf("when: invalid JSON: %w", err)
	}
	v, err := evalNode(node, data)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

func evalNode(node any, data map[string]any) (any, error) {
	obj, ok := node.(map[string]any)
	if !ok {
		return node, nil // literal: string/number/bool/nil/array evaluate to themselves
	}
	if len(obj) != 1 {
		return nil, fmt.Errorf("when: operator object must have exactly one key, got %d", len(obj))
	}
	for op, args := range obj {
		switch op {
		case "var":
			path, ok := args.(string)
			if !ok {
				return nil, fmt.Errorf("when: \"var\" needs a string path, got %T", args)
			}
			return lookupPath(data, path), nil
		case "not", "!":
			v, err := evalNode(args, data)
			if err != nil {
				return nil, err
			}
			return !truthy(v), nil
		case "and":
			items, err := evalArgs(args, data)
			if err != nil {
				return nil, err
			}
			for _, v := range items {
				if !truthy(v) {
					return false, nil
				}
			}
			return true, nil
		case "or":
			items, err := evalArgs(args, data)
			if err != nil {
				return nil, err
			}
			for _, v := range items {
				if truthy(v) {
					return true, nil
				}
			}
			return false, nil
		case "==", "!=", "<", "<=", ">", ">=":
			items, err := evalArgs(args, data)
			if err != nil {
				return nil, err
			}
			if len(items) != 2 {
				return nil, fmt.Errorf("when: %q needs exactly 2 operands, got %d", op, len(items))
			}
			return compareOp(op, items[0], items[1])
		default:
			return nil, fmt.Errorf("when: unknown operator %q", op)
		}
	}
	panic("unreachable: len(obj) == 1 guaranteed by the check above")
}

// evalArgs evaluates a JSONLogic argument list, which may itself be given as
// a bare (non-array) value for single-operand contexts.
func evalArgs(args any, data map[string]any) ([]any, error) {
	list, ok := args.([]any)
	if !ok {
		v, err := evalNode(args, data)
		if err != nil {
			return nil, err
		}
		return []any{v}, nil
	}
	out := make([]any, 0, len(list))
	for _, a := range list {
		v, err := evalNode(a, data)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// lookupPath resolves a dot-separated path into nested maps. A missing key
// at any point returns nil (JSONLogic convention — a missing var is falsy,
// not an error), so an author can write {"==": [{"var":"review.verdict"},
// "approve"]} without first checking the field exists.
func lookupPath(data map[string]any, path string) any {
	cur := any(data)
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			if i == start {
				return nil
			}
			key := path[start:i]
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[key]
			start = i + 1
		}
	}
	return cur
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// compareOp implements ==, !=, <, <=, >, >= with JSON's native types
// (numbers decode to float64, everything else compares as its Go type).
// Ordering comparisons on non-numeric, non-string operands are an error
// rather than an arbitrary/undefined result.
func compareOp(op string, a, b any) (bool, error) {
	switch op {
	case "==":
		return jsonEqual(a, b), nil
	case "!=":
		return !jsonEqual(a, b), nil
	}
	af, aok := a.(float64)
	bf, bok := b.(float64)
	if aok && bok {
		switch op {
		case "<":
			return af < bf, nil
		case "<=":
			return af <= bf, nil
		case ">":
			return af > bf, nil
		case ">=":
			return af >= bf, nil
		}
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		switch op {
		case "<":
			return as < bs, nil
		case "<=":
			return as <= bs, nil
		case ">":
			return as > bs, nil
		case ">=":
			return as >= bs, nil
		}
	}
	return false, fmt.Errorf("when: %q needs two numbers or two strings, got %T and %T", op, a, b)
}

func jsonEqual(a, b any) bool {
	af, aIsNum := a.(float64)
	bf, bIsNum := b.(float64)
	if aIsNum && bIsNum {
		return af == bf
	}
	as, aIsStr := a.(string)
	bs, bIsStr := b.(string)
	if aIsStr && bIsStr {
		return as == bs
	}
	ab, aIsBool := a.(bool)
	bb, bIsBool := b.(bool)
	if aIsBool && bIsBool {
		return ab == bb
	}
	return a == nil && b == nil
}

// parseStepOutputAsData turns a step's raw string output into the data
// context evalCondition and structured Input resolution both use: if the
// output parses as a JSON object, that object is the context; otherwise the
// raw string is wrapped as {"raw": "<output>"} so ${step.raw} and
// {"var":"step.raw"} always work even for plain-text agent summaries.
func parseStepOutputAsData(output string) map[string]any {
	var obj map[string]any
	if err := json.Unmarshal([]byte(output), &obj); err == nil && obj != nil {
		return obj
	}
	return map[string]any{"raw": output}
}
