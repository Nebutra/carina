package daemon

import "testing"

// TestDoctorProbes: the doctor surface reports the version and passing probes
// for a healthy daemon.
func TestDoctorProbes(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	res, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["version"] != Version {
		t.Fatalf("version missing/wrong: %v", m["version"])
	}
	if kern := m["kernel"].(map[string]any); kern["ok"] != true {
		t.Fatalf("kernel probe should pass, got %v", kern)
	}
	if sd := m["state_dir_writable"].(map[string]any); sd["ok"] != true {
		t.Fatalf("state dir should be writable, got %v", sd)
	}
	if _, ok := m["reasoner"]; !ok {
		t.Fatal("reasoner probe missing")
	}
	if ctx, ok := m["context_engine"].(map[string]any); !ok || ctx["ok"] != true {
		t.Fatalf("context engine probe missing/wrong: %v", m["context_engine"])
	}
	if _, ok := m["fix_plan"].([]map[string]any); !ok {
		t.Fatalf("doctor fix_plan missing or untyped: %T", m["fix_plan"])
	}
	if proto, ok := m["runtime_protocol"].(map[string]any); !ok || proto["version"] != runtimeProtocolVersion {
		t.Fatalf("runtime protocol check missing: %v", m["runtime_protocol"])
	}
	if telemetry, ok := m["telemetry"].(map[string]any); !ok || telemetry["otlp"] != false {
		t.Fatalf("telemetry format must be explicit: %v", m["telemetry"])
	}
}
