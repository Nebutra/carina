package daemon

import (
	"encoding/json"
	"testing"
)

func TestRuntimeInitializeNegotiatesMajorAndCapabilities(t *testing.T) {
	d := &Daemon{}
	raw, _ := json.Marshal(map[string]any{"protocol_version": "1.0.0", "client_name": "test"})
	out, err := d.handleRuntimeInitialize(raw)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["runtime_version"] != Version || m["protocol_version"] != runtimeProtocolVersion {
		t.Fatalf("%+v", m)
	}
	if m["projection_version"] != sessionProjectionVersion {
		t.Fatalf("projection version = %v", m["projection_version"])
	}
	caps := m["capabilities"].(map[string]any)
	if _, ok := caps["trusted_channels"].(bool); !ok {
		t.Fatalf("%+v", caps)
	}
	if caps["session_review"] != false {
		t.Fatalf("unregistered session.review capability = %+v", caps)
	}
	bad, _ := json.Marshal(map[string]any{"protocol_version": "2.0.0"})
	if _, err = d.handleRuntimeInitialize(bad); err == nil {
		t.Fatal("incompatible major accepted")
	}
	drift, _ := json.Marshal(map[string]any{"protocol_version": "1.1.0", "schema_version": "2.0.0"})
	if _, err = d.handleRuntimeInitialize(drift); err == nil {
		t.Fatal("schema drift accepted")
	}
	additive, _ := json.Marshal(map[string]any{"protocol_version": "1.9.0", "schema_version": "1.9.0"})
	if _, err = d.handleRuntimeInitialize(additive); err != nil {
		t.Fatalf("additive 1.x schema rejected: %v", err)
	}
	projectionDrift, _ := json.Marshal(map[string]any{"protocol_version": "1.2.0", "projection_version": "2.0.0"})
	if _, err = d.handleRuntimeInitialize(projectionDrift); err == nil {
		t.Fatal("projection drift accepted")
	}
}
