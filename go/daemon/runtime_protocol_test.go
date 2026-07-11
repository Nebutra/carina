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
	caps := m["capabilities"].(map[string]any)
	if _, ok := caps["trusted_channels"].(bool); !ok {
		t.Fatalf("%+v", caps)
	}
	bad, _ := json.Marshal(map[string]any{"protocol_version": "2.0.0"})
	if _, err = d.handleRuntimeInitialize(bad); err == nil {
		t.Fatal("incompatible major accepted")
	}
	drift, _ := json.Marshal(map[string]any{"protocol_version": "1.1.0", "schema_version": "1.0.0"})
	if _, err = d.handleRuntimeInitialize(drift); err == nil {
		t.Fatal("schema drift accepted")
	}
}
