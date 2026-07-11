package daemon

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// swarmChannelMessage is one message on a swarm run's named channel.
type swarmChannelMessage struct {
	Seq     int             `json:"seq"`
	Channel string          `json:"channel"`
	From    string          `json:"from_step"`
	Payload json.RawMessage `json:"payload"`
}

// swarmChannelBroker is an in-process pub/sub bus scoped to exactly one
// streaming workflow run (owned by that run's streamCoordinator), giving
// steps declared via WorkflowStep.ConsumesChannel a way to receive messages
// from steps that are still RUNNING — the control/data edges P1/P2 already
// implement only ever hand off a step's TERMINAL output, so they can't
// express "tell me as scan_module_7 makes progress, not just when it's
// done" (Agent Swarm design §6).
//
// Reuses go/channels' trust POSTURE (identity-scoped delivery, audited,
// no free-text bypass of governance) without depending on it directly:
// go/channels solves authenticating external senders crossing a process
// boundary via HMAC; swarm nodes are already kernel-authenticated Carina
// sessions living inside the same daemon process, so that problem doesn't
// apply here — what carries over is "every message is attributable to a
// real identity and every publish is a governed, audited effect", not the
// wire protocol.
//
// Every message ever published in a run is retained for that run's
// lifetime (bounded — one workflow run has a bounded step count and a
// bounded generator-injection ceiling, so this cannot grow unboundedly the
// way a long-lived process-wide bus could).
type swarmChannelBroker struct {
	mu       sync.Mutex
	nextSeq  int
	messages map[string][]swarmChannelMessage // channel -> append-only log
	cursor   map[string]map[string]int        // stepID -> channel -> next unread index
}

func newSwarmChannelBroker() *swarmChannelBroker {
	return &swarmChannelBroker{
		messages: map[string][]swarmChannelMessage{},
		cursor:   map[string]map[string]int{},
	}
}

func (b *swarmChannelBroker) publish(channel, fromStep string, payload json.RawMessage) swarmChannelMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextSeq++
	msg := swarmChannelMessage{Seq: b.nextSeq, Channel: channel, From: fromStep, Payload: payload}
	b.messages[channel] = append(b.messages[channel], msg)
	return msg
}

// receive returns every message published on any of channels since stepID
// last called receive for that channel, then advances stepID's cursor so a
// repeat call doesn't redeliver. A step only ever sees channels it actually
// asked about — receive never scans channels outside the requested set,
// even ones with pending messages, so a step cannot eavesdrop on a channel
// it wasn't handed by the caller (dispatchActionOutcome only ever passes
// the step's own ConsumesChannel list — see swarmReceiveOutcome).
func (b *swarmChannelBroker) receive(stepID string, channels []string) []swarmChannelMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cursor[stepID] == nil {
		b.cursor[stepID] = map[string]int{}
	}
	var out []swarmChannelMessage
	for _, ch := range channels {
		log := b.messages[ch]
		start := b.cursor[stepID][ch]
		if start < len(log) {
			out = append(out, log[start:]...)
			b.cursor[stepID][ch] = len(log)
		}
	}
	return out
}

// swarmChannelBinding is registered on a spawned child session for exactly
// the duration of its synchronous run (Store before runSubagentLoopContext,
// deferred Delete right after — same lifetime pattern subagent.go already
// uses for restrictedTools/allowedTools/allowedSpawnAgents), so a
// swarm_publish/swarm_receive tool call made mid-run by that session's
// reasoner can find the right run-scoped broker via the session ID alone.
type swarmChannelBinding struct {
	broker     *swarmChannelBroker
	stepID     string
	subscribed []string
}

// swarmChannelInstructionSuffix is appended to a streaming-mode step's task
// text when it declares consumes_channel, so the subagent knows it can pull
// live messages from still-running upstream/sibling steps mid-run instead of
// only ever seeing a finished dependency's terminal output.
func swarmChannelInstructionSuffix(subscribed []string) string {
	if len(subscribed) == 0 {
		return ""
	}
	return fmt.Sprintf(`

This step is subscribed to the following swarm channel(s): %s. Other steps in
this workflow run may publish live progress/messages to these channels WHILE
THEY ARE STILL RUNNING (not just after they finish). Call the "swarm_receive"
tool (optionally with a "channel" field to check just one) at any point to
pull new messages since you last checked; it returns immediately with
whatever is available, possibly nothing. You may also call "swarm_publish"
with {"channel":"...","payload":{...}} to send a message to any channel,
whether or not you are subscribed to it.`, joinQuoted(subscribed))
}

func joinQuoted(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += `"` + s + `"`
	}
	return out
}

// gateSwarmMessage evaluates the SwarmMessage capability before a publish is
// admitted into the broker. Defaults to Allowed (see the
// Capability::SwarmMessage verdict in carina-policy) so live inter-node
// messaging isn't stalled on approval in the common case, but the decision
// is still policy-evaluated and audited like every other kernel-gated
// effect, and an org PolicyBundle can tighten specific channels.
func (d *Daemon) gateSwarmMessage(sess *sessionstore.Session, task *scheduler.Task, channel string) (bool, *kernel.Decision) {
	resource := "channel:" + channel
	dec, err := d.kern.Request(sess.SessionID, "SwarmMessage", resource, task.TaskID)
	if err != nil {
		return false, &kernel.Decision{Decision: "denied", Reason: "governance error: " + err.Error()}
	}
	switch dec.Decision {
	case "denied":
		return false, dec
	case "requires_approval":
		approved, ok := d.resolveApprovalOrEscalate(sess, task, dec, "SwarmMessage", resource, "swarm channel publish ("+channel+")")
		if !ok {
			return false, dec
		}
		return true, approved
	default:
		return true, dec
	}
}

// swarmPublishOutcome handles the "swarm_publish" tool: only callable by a
// session currently bound to a run (registered by spawnSubagentContextIDBound
// for the duration of a streaming-workflow step's execution) — a session
// spawned outside a streaming workflow run has no broker to publish into.
func (d *Daemon) swarmPublishOutcome(sess *sessionstore.Session, task *scheduler.Task, act *action) toolExecutionOutcome {
	raw, ok := d.swarmChannels.Load(sess.SessionID)
	if !ok {
		return toolFailed("swarm_publish is only available to steps running inside a streaming workflow run", "swarm_not_bound")
	}
	binding := raw.(*swarmChannelBinding)
	if act.Channel == "" {
		return toolFailed("swarm_publish requires a non-empty \"channel\"", "invalid_args")
	}
	allowed, dec := d.gateSwarmMessage(sess, task, act.Channel)
	if !allowed {
		return toolDenied("DENIED: "+dec.Reason, "policy_denied")
	}
	payload := act.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	msg := binding.broker.publish(act.Channel, binding.stepID, payload)
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
		"status": "swarm_channel_published", "channel": act.Channel, "from_step": binding.stepID, "seq": msg.Seq,
	}, dec.DecisionID)
	return toolCompleted(fmt.Sprintf("published to %q (seq %d)", act.Channel, msg.Seq))
}

// swarmReceiveOutcome handles the "swarm_receive" tool: returns every new
// message on the requesting step's subscribed channel(s) since it last
// checked. An empty "channel" field means "all channels this step
// subscribed to" (WorkflowStep.ConsumesChannel); a non-empty one narrows to
// that single channel, but ONLY if the step actually subscribed to it —
// receive is not a general channel-browsing tool.
func (d *Daemon) swarmReceiveOutcome(sess *sessionstore.Session, task *scheduler.Task, act *action) toolExecutionOutcome {
	raw, ok := d.swarmChannels.Load(sess.SessionID)
	if !ok {
		return toolFailed("swarm_receive is only available to steps running inside a streaming workflow run", "swarm_not_bound")
	}
	binding := raw.(*swarmChannelBinding)
	channels := binding.subscribed
	if act.Channel != "" {
		found := false
		for _, c := range binding.subscribed {
			if c == act.Channel {
				found = true
				break
			}
		}
		if !found {
			return toolFailed(fmt.Sprintf("this step did not subscribe to channel %q (subscribed: %s)", act.Channel, joinQuoted(binding.subscribed)), "not_subscribed")
		}
		channels = []string{act.Channel}
	}
	if len(channels) == 0 {
		return toolCompleted("this step has no consumes_channel subscriptions")
	}
	msgs := binding.broker.receive(binding.stepID, channels)
	if len(msgs) == 0 {
		return toolCompleted("no new messages")
	}
	b, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		return toolFailed("error: "+err.Error(), "tool_error")
	}
	return toolCompleted(string(b))
}
