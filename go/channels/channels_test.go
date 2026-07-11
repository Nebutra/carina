package channels

import (
	"strings"
	"testing"
	"time"
)

func TestGatingSignatureDedupAndPermissionRelay(t *testing.T) {
	r := New(time.Minute, time.Hour)
	secret := []byte(strings.Repeat("x", 32))
	if err := r.Register(Sender{ID: "ci", Secret: secret, Sessions: []string{"s1"}, Kinds: []string{"build"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	e := Event{ID: "1", SenderID: "ci", SessionID: "s1", Kind: "build", Timestamp: now}
	rec, err := r.Ingest(e, Sign(secret, e))
	if err != nil || !rec.Accepted || rec.Duplicate {
		t.Fatalf("%+v %v", rec, err)
	}
	rec, err = r.Ingest(e, Sign(secret, e))
	if err != nil || !rec.Duplicate {
		t.Fatalf("%+v %v", rec, err)
	}
	e.ID = "2"
	e.PermissionDecisionID = "d"
	if _, err = r.Ingest(e, Sign(secret, e)); err == nil {
		t.Fatal("permission relay should fail closed")
	}
}

func TestSignatureBindsPayload(t *testing.T) {
	r := New(time.Minute, time.Hour)
	secret := []byte(strings.Repeat("p", 32))
	_ = r.Register(Sender{ID: "x", Secret: secret, Sessions: []string{"s"}, Kinds: []string{"k"}})
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	e := Event{ID: "e", SenderID: "x", SessionID: "s", Kind: "k", Timestamp: now, Payload: map[string]any{"state": "passed"}}
	sig := Sign(secret, e)
	e.Payload["state"] = "failed"
	if _, err := r.Ingest(e, sig); err == nil {
		t.Fatal("tampered payload accepted")
	}
}
func TestRejectsWrongTargetAndStale(t *testing.T) {
	r := New(time.Minute, time.Hour)
	secret := []byte(strings.Repeat("s", 32))
	_ = r.Register(Sender{ID: "x", Secret: secret, Sessions: []string{"s"}, Kinds: []string{"k"}})
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	e := Event{ID: "e", SenderID: "x", SessionID: "other", Kind: "k", Timestamp: now}
	if _, err := r.Ingest(e, Sign(secret, e)); err == nil {
		t.Fatal("wrong target accepted")
	}
	e.SessionID = "s"
	e.Timestamp = now.Add(-time.Hour)
	if _, err := r.Ingest(e, Sign(secret, e)); err == nil {
		t.Fatal("stale accepted")
	}
}
