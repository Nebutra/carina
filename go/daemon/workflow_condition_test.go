package daemon

import (
	"encoding/json"
	"testing"
)

func mustCondition(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	return json.RawMessage(raw)
}

func TestEvalConditionVarAndComparisons(t *testing.T) {
	data := map[string]any{
		"review": map[string]any{"verdict": "approve", "score": 8.5, "approved": true},
	}
	cases := []struct {
		expr string
		want bool
	}{
		{`{"==": [{"var":"review.verdict"}, "approve"]}`, true},
		{`{"==": [{"var":"review.verdict"}, "reject"]}`, false},
		{`{"!=": [{"var":"review.verdict"}, "reject"]}`, true},
		{`{">": [{"var":"review.score"}, 5]}`, true},
		{`{"<": [{"var":"review.score"}, 5]}`, false},
		{`{">=": [{"var":"review.score"}, 8.5]}`, true},
		{`{"var":"review.approved"}`, true},
		{`{"not": {"var":"review.approved"}}`, false},
		{`{"and": [{"==": [{"var":"review.verdict"}, "approve"]}, {"var":"review.approved"}]}`, true},
		{`{"and": [{"==": [{"var":"review.verdict"}, "approve"]}, false]}`, false},
		{`{"or": [false, {"var":"review.approved"}]}`, true},
		{`{"var":"review.does_not_exist"}`, false}, // missing var is falsy, not an error
	}
	for _, c := range cases {
		got, err := evalCondition(mustCondition(t, c.expr), data)
		if err != nil {
			t.Fatalf("expr %s: unexpected error: %v", c.expr, err)
		}
		if got != c.want {
			t.Fatalf("expr %s: got %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalConditionFailsClosedOnMalformedExpression(t *testing.T) {
	cases := []string{
		`not valid json`,
		`{"unknown_op": [1,2]}`,
		`{"==": [1]}`,     // wrong arity
		`{"<": ["a", 1]}`, // type mismatch
		`{"a":1, "b":2}`,  // more than one operator key
	}
	for _, expr := range cases {
		_, err := evalCondition(mustCondition(t, expr), map[string]any{})
		if err == nil {
			t.Fatalf("expr %s: expected an error (fail closed), got none", expr)
		}
	}
}

func TestParseStepOutputAsData(t *testing.T) {
	obj := parseStepOutputAsData(`{"verdict":"approve"}`)
	if obj["verdict"] != "approve" {
		t.Fatalf("expected JSON object to parse through, got %#v", obj)
	}
	plain := parseStepOutputAsData("just plain text, not json")
	if plain["raw"] != "just plain text, not json" {
		t.Fatalf("expected non-JSON output wrapped as {raw: ...}, got %#v", plain)
	}
}
