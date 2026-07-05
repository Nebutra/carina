package daemon

import "testing"

func TestStrictJSONRejectsDuplicateKeys(t *testing.T) {
	var v map[string]any
	if err := decodeStrictJSON([]byte(`{"name":"a","name":"b"}`), &v); err == nil {
		t.Fatal("duplicate top-level key must be rejected")
	}
	if err := decodeStrictJSON([]byte(`{"a":{"k":1,"k":2}}`), &v); err == nil {
		t.Fatal("nested duplicate key must be rejected")
	}
	// Same key across distinct array elements is fine.
	if err := decodeStrictJSON([]byte(`{"steps":[{"id":"x"},{"id":"y"}]}`), &v); err != nil {
		t.Fatalf("distinct objects must be accepted: %v", err)
	}
	// Valid document unmarshals normally.
	var s struct {
		Name string `json:"name"`
	}
	if err := decodeStrictJSON([]byte(`{"name":"ok"}`), &s); err != nil || s.Name != "ok" {
		t.Fatalf("valid json failed: err=%v name=%q", err, s.Name)
	}
}
