package jsonschema

import (
	"encoding/json"
	"testing"
)

func TestValidateObject(t *testing.T) {
	s := json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["ok"]}},"required":["status"],"additionalProperties":false}`)
	if e := ValidateJSON(`{"status":"ok"}`, s); len(e) > 0 {
		t.Fatal(e)
	}
	if e := ValidateJSON(`{"status":"bad","extra":1}`, s); len(e) != 2 {
		t.Fatalf("%v", e)
	}
}
