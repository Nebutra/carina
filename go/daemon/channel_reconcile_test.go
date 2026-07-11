package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/channels"
)

func TestChannelReconcileRPCRequiresExplicitConfirmation(t *testing.T) {
	r := channels.New(time.Minute, time.Hour)
	secret := []byte(strings.Repeat("r", 32))
	if err := r.Register(channels.Sender{ID: "ci", Secret: secret, Sessions: []string{"s"}, Kinds: []string{"build"}}); err != nil {
		t.Fatal(err)
	}
	event := channels.Event{ID: "e", SenderID: "ci", SessionID: "s", Kind: "build", Timestamp: time.Now().UTC()}
	res, err := r.Reserve(event, channels.Sign(secret, event))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.MarkEffectStarted(res); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{channels: r}
	if _, err = d.handleChannelEventReconcile(agentViewRaw(map[string]any{"sender_id": "ci", "event_id": "e", "outcome": "not_executed"})); err == nil {
		t.Fatal("reconcile accepted without confirmation")
	}
	pending, err := d.handleChannelEventPending(nil)
	if err != nil || len(pending.(map[string]any)["incidents"].([]channels.Incident)) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if _, err = d.handleChannelEventReconcile(agentViewRaw(map[string]any{"sender_id": "ci", "event_id": "e", "outcome": "not_executed", "confirmed": true})); err != nil {
		t.Fatal(err)
	}
	if len(r.Incidents()) != 0 {
		t.Fatal("incident was not cleared")
	}
}
