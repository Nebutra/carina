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
}
