package channels

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestAbortAllowsRetryAndConcurrentReserveIsExclusive(t *testing.T) {
	r := New(time.Minute, time.Hour)
	secret := []byte(strings.Repeat("c", 32))
	_ = r.Register(Sender{ID: "x", Secret: secret, Sessions: []string{"s"}, Kinds: []string{"k"}})
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	e := Event{ID: "e", SenderID: "x", SessionID: "s", Kind: "k", Timestamp: now}
	sig := Sign(secret, e)
	first, err := r.Reserve(e, sig)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := r.Reserve(e, sig); err == nil {
			t.Error("concurrent reservation accepted")
		}
	}()
	wg.Wait()
	r.Abort(first)
	retry, err := r.Reserve(e, sig)
	if err != nil {
		t.Fatalf("retry after failed side effect: %v", err)
	}
	if err := r.Commit(retry); err != nil {
		t.Fatal(err)
	}
	dup, err := r.Reserve(e, sig)
	if err != nil || !dup.Receipt.Duplicate {
		t.Fatalf("duplicate=%+v err=%v", dup, err)
	}
}

func TestEffectStartedCannotBeAbortedOrRetriedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	secret := []byte(strings.Repeat("z", 32))
	resolver := func(string) ([]byte, error) { return secret, nil }
	r, err := Open(dir, time.Minute, time.Hour, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Register(Sender{ID: "ci", SecretRef: "env:CARINA_CHANNEL_CI", Sessions: []string{"s"}, Kinds: []string{"build"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	e := Event{ID: "evt", SenderID: "ci", SessionID: "s", Kind: "build", Timestamp: now}
	res, err := r.Reserve(e, Sign(secret, e))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.MarkEffectStarted(res); err != nil {
		t.Fatal(err)
	}
	r.Abort(res)
	r2, err := Open(dir, time.Minute, time.Hour, resolver)
	if err != nil {
		t.Fatal(err)
	}
	r2.now = func() time.Time { return now }
	if _, err = r2.Reserve(e, Sign(secret, e)); err == nil || !strings.Contains(err.Error(), "already reserved") {
		t.Fatalf("effect-started event was retried: %v", err)
	}
	if err = r2.Reconcile("ci", "evt", false); err == nil {
		t.Fatal("effect-started event was aborted without manual compensation")
	}
}

func TestOpenPersistsPolicyAndCommittedSeenWithoutSecret(t *testing.T) {
	dir := t.TempDir()
	secret := []byte(strings.Repeat("r", 32))
	resolver := func(ref string) ([]byte, error) {
		if ref != "env:CARINA_CHANNEL_CI" {
			return nil, errors.New("bad ref")
		}
		return secret, nil
	}
	r, err := Open(dir, time.Minute, time.Hour, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Register(Sender{ID: "ci", SecretRef: "env:CARINA_CHANNEL_CI", Sessions: []string{"s"}, Kinds: []string{"k"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	e := Event{ID: "1", SenderID: "ci", SessionID: "s", Kind: "k", Timestamp: now}
	if _, err = r.Ingest(e, Sign(secret, e)); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(dir, time.Minute, time.Hour, resolver)
	if err != nil {
		t.Fatal(err)
	}
	r2.now = func() time.Time { return now }
	res, err := r2.Reserve(e, Sign(secret, e))
	if err != nil || !res.Receipt.Duplicate {
		t.Fatalf("%+v %v", res, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "channels.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), strings.Repeat("r", 32)) {
		t.Fatal("secret persisted")
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

func TestDeduplicationKeyDoesNotAliasColonDelimitedIDs(t *testing.T) {
	r := New(time.Minute, time.Hour)
	secret := []byte(strings.Repeat("d", 32))
	for _, senderID := range []string{"a:b", "a"} {
		if err := r.Register(Sender{ID: senderID, Secret: secret, Sessions: []string{"s"}, Kinds: []string{"k"}}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	first := Event{ID: "c", SenderID: "a:b", SessionID: "s", Kind: "k", Timestamp: now}
	second := Event{ID: "b:c", SenderID: "a", SessionID: "s", Kind: "k", Timestamp: now}
	if _, err := r.Ingest(first, Sign(secret, first)); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.Ingest(second, Sign(secret, second))
	if err != nil || receipt.Duplicate {
		t.Fatalf("distinct sender/event pair aliased: %+v %v", receipt, err)
	}
}

func TestCrashReservationRequiresExplicitReconciliation(t *testing.T) {
	dir := t.TempDir()
	secret := []byte(strings.Repeat("z", 32))
	resolver := func(string) ([]byte, error) { return secret, nil }
	r, err := Open(dir, time.Minute, time.Hour, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Register(Sender{ID: "x", SecretRef: "env:CARINA_CHANNEL_X", Sessions: []string{"s"}, Kinds: []string{"k"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	e := Event{ID: "e", SenderID: "x", SessionID: "s", Kind: "k", Timestamp: now}
	res, err := r.Reserve(e, Sign(secret, e))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.MarkEffectApplied(res); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(dir, time.Minute, time.Hour, resolver)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	if _, err = restarted.Reserve(e, Sign(secret, e)); err == nil || !strings.Contains(err.Error(), "manual reconcile") {
		t.Fatalf("silent replay allowed: %v", err)
	}
	if err = restarted.Reconcile("x", "e", true); err != nil {
		t.Fatal(err)
	}
	dup, err := restarted.Reserve(e, Sign(secret, e))
	if err != nil || !dup.Receipt.Duplicate {
		t.Fatalf("reconciled receipt not idempotent: %+v %v", dup, err)
	}
}

func TestConfirmedCrashOutcomesAreDurable(t *testing.T) {
	for _, executed := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_executed", true: "executed"}[executed], func(t *testing.T) {
			dir := t.TempDir()
			secret := []byte(strings.Repeat("q", 32))
			resolver := func(string) ([]byte, error) { return secret, nil }
			r, _ := Open(dir, time.Minute, time.Hour, resolver)
			_ = r.Register(Sender{ID: "x", SecretRef: "env:CARINA_CHANNEL_X", Sessions: []string{"s"}, Kinds: []string{"k"}})
			now := time.Now().UTC()
			r.now = func() time.Time { return now }
			e := Event{ID: "e", SenderID: "x", SessionID: "s", Kind: "k", Timestamp: now}
			res, err := r.Reserve(e, Sign(secret, e))
			if err != nil {
				t.Fatal(err)
			}
			if err = r.MarkEffectStarted(res); err != nil {
				t.Fatal(err)
			}
			if executed {
				if err = r.MarkEffectApplied(res); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := Open(dir, time.Minute, time.Hour, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if len(restarted.Incidents()) != 1 {
				t.Fatal("missing incident")
			}
			if err = restarted.ReconcileConfirmed("x", "e", executed); err != nil {
				t.Fatal(err)
			}
			again, _ := Open(dir, time.Minute, time.Hour, resolver)
			again.now = func() time.Time { return now }
			retry, err := again.Reserve(e, Sign(secret, e))
			if executed {
				if err != nil || !retry.Receipt.Duplicate {
					t.Fatalf("executed event replayed: %+v %v", retry, err)
				}
			} else if err != nil || retry.Receipt.Duplicate {
				t.Fatalf("unexecuted event was not retryable: %+v %v", retry, err)
			}
		})
	}
}
