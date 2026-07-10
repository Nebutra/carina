package worker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegisterAssignsFieldsAndCapabilities(t *testing.T) {
	p := NewPool()
	w := p.Register("local-1", Local)
	if w.WorkerID == "" || w.Status != "idle" || w.Type != Local {
		t.Fatalf("unexpected worker: %+v", w)
	}
	if len(w.Capabilities) == 0 {
		t.Fatal("local worker should declare capabilities")
	}
	// sandbox is more restricted than local.
	sb := p.Register("sb", Sandbox)
	if len(sb.Capabilities) >= len(w.Capabilities) {
		t.Fatal("sandbox should have fewer capabilities than local")
	}
}

func TestHeartbeatAndRevoke(t *testing.T) {
	p := NewPool()
	w := p.Register("w", Remote)
	before := w.LastHeartbeat
	if err := p.Heartbeat(w.WorkerID); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := p.Heartbeat("wrk_missing"); err == nil {
		t.Fatal("heartbeat of unknown worker should error")
	}
	list := p.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(list))
	}
	// heartbeat updates the timestamp (monotonic-ish; not before).
	if list[0].LastHeartbeat.Before(before) {
		t.Fatal("heartbeat should not move time backwards")
	}
	if err := p.Revoke(w.WorkerID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if len(p.List()) != 0 {
		t.Fatal("worker should be gone after revoke")
	}
	if err := p.Revoke("wrk_missing"); err == nil {
		t.Fatal("revoke of unknown worker should error")
	}
}

func TestAuthenticatedRegistrationBindsOpaqueCredential(t *testing.T) {
	p := NewPool()
	w, credential, err := p.RegisterAuthenticated("remote-1", Remote)
	if err != nil {
		t.Fatal(err)
	}
	if credential == "" || credential == w.WorkerID {
		t.Fatalf("credential must be non-empty and opaque: worker=%q credential=%q", w.WorkerID, credential)
	}
	if !p.Authenticate(w.WorkerID, credential) {
		t.Fatal("issued credential should authenticate its worker")
	}
	if p.Authenticate(w.WorkerID, credential+"x") {
		t.Fatal("wrong credential should be rejected")
	}
	other, otherCredential, err := p.RegisterAuthenticated("remote-2", Remote)
	if err != nil {
		t.Fatal(err)
	}
	if p.Authenticate(other.WorkerID, credential) || p.Authenticate(w.WorkerID, otherCredential) {
		t.Fatal("credentials must be bound to exactly one worker id")
	}
	if err := p.Revoke(w.WorkerID); err != nil {
		t.Fatal(err)
	}
	if p.Authenticate(w.WorkerID, credential) {
		t.Fatal("revocation must invalidate the credential")
	}
	for _, listed := range p.List() {
		raw, err := json.Marshal(listed)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), otherCredential) || strings.Contains(string(raw), "credential") {
			t.Fatalf("worker list leaked credential material: %s", raw)
		}
	}
}

func TestCapabilitiesForKinds(t *testing.T) {
	if len(capabilitiesFor(Local)) < len(capabilitiesFor(CI)) {
		t.Fatal("local should have at least as many caps as ci")
	}
	for _, k := range []Kind{Local, Sandbox, CI, Remote} {
		if len(capabilitiesFor(k)) == 0 {
			t.Fatalf("kind %s should declare capabilities", k)
		}
	}
}
