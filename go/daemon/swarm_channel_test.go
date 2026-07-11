package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// writeChannelWorkerAgent is writeWorkerAgent with a higher max_turns: the
// subscriber role below polls swarm_receive across several turns before a
// concurrently-running publisher step is guaranteed to have published, and
// the shared "worker" agent's max_turns:2 (see writeWorkerAgent) doesn't
// leave enough headroom for that polling to reliably win the race.
func writeChannelWorkerAgent(t *testing.T, ws string) {
	t.Helper()
	dir := filepath.Join(ws, ".carina", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := "---\nname: channel-worker\ndescription: run one channel step\nprofile: read-only\nmax_turns: 8\n---\nYou are a worker. Do the step, then finish with done.\n"
	if err := os.WriteFile(filepath.Join(dir, "channel-worker.md"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- pure broker unit tests (no Daemon involved) ---------------------------

func TestSwarmChannelBrokerDeliversPublishedMessagesOnce(t *testing.T) {
	b := newSwarmChannelBroker()
	b.publish("progress", "publisher", json.RawMessage(`{"n":1}`))
	b.publish("progress", "publisher", json.RawMessage(`{"n":2}`))

	first := b.receive("subscriber", []string{"progress"})
	if len(first) != 2 {
		t.Fatalf("expected 2 messages on first receive, got %d: %+v", len(first), first)
	}
	if first[0].Seq != 1 || first[1].Seq != 2 {
		t.Fatalf("expected sequential seq 1,2, got %d,%d", first[0].Seq, first[1].Seq)
	}

	second := b.receive("subscriber", []string{"progress"})
	if len(second) != 0 {
		t.Fatalf("expected no redelivery on second receive, got %+v", second)
	}

	b.publish("progress", "publisher", json.RawMessage(`{"n":3}`))
	third := b.receive("subscriber", []string{"progress"})
	if len(third) != 1 || third[0].Seq != 3 {
		t.Fatalf("expected exactly the new message (seq 3), got %+v", third)
	}
}

func TestSwarmChannelBrokerIsolatesChannelsAndSubscribers(t *testing.T) {
	b := newSwarmChannelBroker()
	b.publish("a", "publisher", json.RawMessage(`"on-a"`))
	b.publish("b", "publisher", json.RawMessage(`"on-b"`))

	onlyA := b.receive("s1", []string{"a"})
	if len(onlyA) != 1 || onlyA[0].Channel != "a" {
		t.Fatalf("expected only channel a's message, got %+v", onlyA)
	}

	// A different subscriber's cursor is independent — s2 hasn't received
	// anything yet, so it still sees a's message even though s1 already did.
	s2OnA := b.receive("s2", []string{"a"})
	if len(s2OnA) != 1 {
		t.Fatalf("expected s2's independent cursor to still see channel a's message, got %+v", s2OnA)
	}

	// s1 never asked about channel b, so it must never see b's message even
	// though it exists in the broker.
	s1Everything := b.receive("s1", []string{"a", "b"})
	if len(s1Everything) != 1 || s1Everything[0].Channel != "b" {
		t.Fatalf("expected s1 to now see only the new channel b message once it actually asks, got %+v", s1Everything)
	}
}

// --- tool-level gating tests (Daemon present, but session never bound) -----

func TestSwarmPublishFailsWhenSessionIsNotBoundToARun(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")

	out := d.swarmPublishOutcome(sess, task, &action{Tool: "swarm_publish", Channel: "progress", Payload: json.RawMessage(`{}`)})
	if out.errorCategory != "swarm_not_bound" {
		t.Fatalf("expected swarm_not_bound, got category=%q display=%q", out.errorCategory, out.display)
	}
}

func TestSwarmReceiveFailsWhenSessionIsNotBoundToARun(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")

	out := d.swarmReceiveOutcome(sess, task, &action{Tool: "swarm_receive"})
	if out.errorCategory != "swarm_not_bound" {
		t.Fatalf("expected swarm_not_bound, got category=%q display=%q", out.errorCategory, out.display)
	}
}

func TestSwarmPublishRequiresNonEmptyChannel(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")
	d.swarmChannels.Store(sess.SessionID, &swarmChannelBinding{broker: newSwarmChannelBroker(), stepID: "s"})
	defer d.swarmChannels.Delete(sess.SessionID)

	out := d.swarmPublishOutcome(sess, task, &action{Tool: "swarm_publish"})
	if out.errorCategory != "invalid_args" {
		t.Fatalf("expected invalid_args for missing channel, got category=%q display=%q", out.errorCategory, out.display)
	}
}

func TestSwarmReceiveRejectsChannelStepDidNotSubscribeTo(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect")
	d.swarmChannels.Store(sess.SessionID, &swarmChannelBinding{broker: newSwarmChannelBroker(), stepID: "s", subscribed: []string{"progress"}})
	defer d.swarmChannels.Delete(sess.SessionID)

	out := d.swarmReceiveOutcome(sess, task, &action{Tool: "swarm_receive", Channel: "other"})
	if out.errorCategory != "not_subscribed" {
		t.Fatalf("expected not_subscribed, got category=%q display=%q", out.errorCategory, out.display)
	}
}

func TestSwarmPublishAndReceiveRoundTripThroughBoundSession(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	broker := newSwarmChannelBroker()

	pubSess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(pubSess.SessionID, workspace, "safe-edit", nil)
	pubTask := d.sched.Submit(pubSess.SessionID, pubSess.WorkspaceID, "publish")
	d.swarmChannels.Store(pubSess.SessionID, &swarmChannelBinding{broker: broker, stepID: "publisher"})
	defer d.swarmChannels.Delete(pubSess.SessionID)

	subSess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(subSess.SessionID, workspace, "safe-edit", nil)
	subTask := d.sched.Submit(subSess.SessionID, subSess.WorkspaceID, "subscribe")
	d.swarmChannels.Store(subSess.SessionID, &swarmChannelBinding{broker: broker, stepID: "subscriber", subscribed: []string{"progress"}})
	defer d.swarmChannels.Delete(subSess.SessionID)

	pubOut := d.swarmPublishOutcome(pubSess, pubTask, &action{Tool: "swarm_publish", Channel: "progress", Payload: json.RawMessage(`{"status":"ok"}`)})
	if pubOut.errorCategory != "" || !strings.Contains(pubOut.display, "published to") {
		t.Fatalf("expected a successful publish, got category=%q display=%q", pubOut.errorCategory, pubOut.display)
	}

	recvOut := d.swarmReceiveOutcome(subSess, subTask, &action{Tool: "swarm_receive"})
	if !strings.Contains(recvOut.display, "publisher") || !strings.Contains(recvOut.display, `"status": "ok"`) {
		t.Fatalf("expected the subscriber to receive the publisher's message, got: %s", recvOut.display)
	}
}

// --- end-to-end: live delivery across two concurrently-running streaming
// workflow steps that share no needs/data edge at all -----------------------

// swarmChannelTestReasoner scripts a publisher role (immediately calls
// swarm_publish, then done) and a subscriber role (polls swarm_receive
// across turns until the publisher's payload shows up in its own transcript,
// then finishes echoing it) — proving delivery crossed from one subagent
// session to a completely independent one, live, without either step
// depending on the other via needs/input.
type swarmChannelTestReasoner struct {
	publishMarker, subscribeMarker string
	subscribeAttempts              int32
}

func (r *swarmChannelTestReasoner) Name() string { return "swarm-channel-test" }
func (r *swarmChannelTestReasoner) Think(_ context.Context, prompt string) (string, error) {
	task := extractTaskLine(prompt)
	switch {
	case strings.Contains(task, r.publishMarker):
		b, _ := json.Marshal(map[string]any{
			"tool": "swarm_publish", "channel": "progress",
			"payload": map[string]string{"status": "hello-from-publisher"},
		})
		return string(b), nil
	case strings.Contains(task, r.subscribeMarker):
		if strings.Contains(prompt, "hello-from-publisher") {
			b, _ := json.Marshal(map[string]string{"tool": "done", "summary": "received: hello-from-publisher"})
			return string(b), nil
		}
		atomic.AddInt32(&r.subscribeAttempts, 1)
		b, _ := json.Marshal(map[string]any{"tool": "swarm_receive"})
		return string(b), nil
	}
	b, _ := json.Marshal(map[string]string{"tool": "done", "summary": "did[" + task + "]"})
	return string(b), nil
}

func TestWorkflowStreamingChannelDeliversLiveMessageToConcurrentSubscriber(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeChannelWorkerAgent(t, ws)
	reasoner := &swarmChannelTestReasoner{publishMarker: "PUBLISH_STEP", subscribeMarker: "SUBSCRIBE_STEP"}
	d.SetReasoner(reasoner)

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "channel", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "publisher", Agent: "channel-worker", Task: "PUBLISH_STEP"},
		{ID: "subscriber", Agent: "channel-worker", Task: "SUBSCRIBE_STEP", ConsumesChannel: []string{"progress"}},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-channel")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !strings.Contains(out["subscriber"], "hello-from-publisher") {
		t.Fatalf("subscriber never received the publisher's live message, got: %q (subscribe attempts: %d)",
			out["subscriber"], atomic.LoadInt32(&reasoner.subscribeAttempts))
	}
}

func TestSwarmChannelInstructionSuffixAlwaysMentionsPublishEvenWithoutSubscriptions(t *testing.T) {
	// swarm_publish needs no subscription to use, so a publish-only step
	// (no consumes_channel) must still be told the tool exists — otherwise
	// a real model has no way to discover it.
	s := swarmChannelInstructionSuffix(nil)
	if !strings.Contains(s, "swarm_publish") {
		t.Fatalf("expected swarm_publish to be mentioned even with no subscriptions, got %q", s)
	}
	if strings.Contains(s, "swarm_receive") {
		t.Fatalf("swarm_receive should only be mentioned when the step actually has subscriptions, got %q", s)
	}
}

func TestSwarmChannelInstructionSuffixMentionsReceiveAndChannelsWhenSubscribed(t *testing.T) {
	s := swarmChannelInstructionSuffix([]string{"progress", "alerts"})
	if !strings.Contains(s, `"progress"`) || !strings.Contains(s, `"alerts"`) {
		t.Fatalf("expected both channel names quoted in the suffix, got %q", s)
	}
	if !strings.Contains(s, "swarm_receive") || !strings.Contains(s, "swarm_publish") {
		t.Fatalf("expected both tool names mentioned in the suffix, got %q", s)
	}
}
