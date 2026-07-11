package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/channels"
)

func TestChannelEventIsDurableAndSteersActiveTask(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "watch CI")
	d.sched.SetStatus(task.TaskID, "running")
	secret := []byte(strings.Repeat("c", 32))
	if err := d.channels.Register(channels.Sender{ID: "ci", Secret: secret, Sessions: []string{sess.SessionID}, Kinds: []string{"build"}}); err != nil {
		t.Fatal(err)
	}
	event := channels.Event{ID: "evt-1", SenderID: "ci", SessionID: sess.SessionID, Kind: "build", Timestamp: time.Now().UTC(), Payload: map[string]any{"status": "failed"}}
	raw, _ := json.Marshal(map[string]any{"event": event, "signature": channels.Sign(secret, event)})
	if _, err := d.handleChannelEventInject(raw); err != nil {
		t.Fatal(err)
	}
	messages := d.drainMailbox(task.TaskID)
	if len(messages) != 1 || !strings.Contains(messages[0], "CHANNEL EVENT build") || !strings.Contains(messages[0], "failed") {
		t.Fatalf("channel event did not reach task mailbox: %#v", messages)
	}
	audit, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil || !strings.Contains(string(audit), "external_event") || !strings.Contains(string(audit), "evt-1") {
		t.Fatalf("channel event was not durably audited: %s (%v)", audit, err)
	}
}

func TestChannelEventWithoutActiveTaskRemainsRetryable(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	secret := []byte(strings.Repeat("d", 32))
	if err := d.channels.Register(channels.Sender{ID: "ci", Secret: secret, Sessions: []string{sess.SessionID}, Kinds: []string{"build"}}); err != nil {
		t.Fatal(err)
	}
	event := channels.Event{ID: "evt-2", SenderID: "ci", SessionID: sess.SessionID, Kind: "build", Timestamp: time.Now().UTC()}
	raw, _ := json.Marshal(map[string]any{"event": event, "signature": channels.Sign(secret, event)})
	if _, err := d.handleChannelEventInject(raw); err == nil || !strings.Contains(err.Error(), "no active task") {
		t.Fatalf("inactive session accepted channel event: %v", err)
	}
	if _, err := d.channels.Reserve(event, channels.Sign(secret, event)); err != nil {
		t.Fatalf("inactive delivery should abort before effects and remain retryable: %v", err)
	}
}
