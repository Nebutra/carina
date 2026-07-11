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

// maxMessagesPerChannel bounds how many messages one channel retains at
// once. Modeled directly on a real, documented incident in Claude Code's own
// Teammate messaging system (its TEAMMATE_MESSAGES_UI_CAP = 50): 292 agents
// spawned within two minutes drove one session to 36.8GB of retained
// message history ("whale session") from unbounded per-teammate retention.
// A swarm run can have hundreds of concurrently-publishing steps; without a
// cap, a high-frequency publisher with a slow or absent subscriber grows
// this channel's log for the run's entire lifetime. Set well above Claude
// Code's UI-facing 50 (this is workflow data, not a terminal display), but
// still a hard bound. Eviction drops the OLDEST messages first and is
// audited (see publish()) — a late/slow subscriber silently catches up from
// the retained window rather than the full history, the same tradeoff
// Claude Code's own cap makes.
// A var (not const) so tests can shrink it temporarily to exercise eviction
// without hundreds of real publish calls; production code never mutates it.
var maxMessagesPerChannel = 500

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
// Deliberately NOT persisted to disk, unlike Claude Code's own filesystem
// mailbox (which needs durability because a Teammate can sit idle across a
// long-lived process and must survive a restart): Carina's streaming-
// workflow resume semantics (wfRunStore) only ever replay COMPLETED steps'
// terminal output — a step still mid-execution when the daemon crashes is
// not resumable at all and gets re-dispatched as a fresh subagent run with
// fresh, empty channel state. A crash loses the publishing/subscribing
// steps themselves, not just their in-flight messages, so persisting this
// broker's contents would not improve recoverability — it would solve a
// problem Carina's architecture doesn't actually have. Revisit only if
// step-level resume ever starts preserving in-flight (not just completed)
// steps.
type swarmChannelBroker struct {
	mu       sync.Mutex
	nextSeq  int
	messages map[string][]swarmChannelMessage // channel -> retained window, oldest-evicted-first once over maxMessagesPerChannel
	cursor   map[string]map[string]int        // stepID -> channel -> highest seq already delivered (0 = none yet)
	evicted  map[string]int                   // channel -> total messages evicted so far (for audit + rollup)
}

func newSwarmChannelBroker() *swarmChannelBroker {
	return &swarmChannelBroker{
		messages: map[string][]swarmChannelMessage{},
		cursor:   map[string]map[string]int{},
		evicted:  map[string]int{},
	}
}

// publish appends a message and evicts the oldest entries if the channel is
// now over cap. Returns the published message plus the channel's running
// eviction total and whether THIS call caused a new eviction (so the caller
// can decide when to emit an audit event, instead of logging on every single
// publish once a channel is steadily over cap).
func (b *swarmChannelBroker) publish(channel, fromStep string, payload json.RawMessage) (msg swarmChannelMessage, evictedTotal int, evictedNow bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextSeq++
	msg = swarmChannelMessage{Seq: b.nextSeq, Channel: channel, From: fromStep, Payload: payload}
	log := append(b.messages[channel], msg)
	if len(log) > maxMessagesPerChannel {
		drop := len(log) - maxMessagesPerChannel
		log = append([]swarmChannelMessage(nil), log[drop:]...) // fresh backing array: don't retain evicted entries via the old slice's capacity
		b.evicted[channel] += drop
		evictedNow = true
	}
	b.messages[channel] = log
	return msg, b.evicted[channel], evictedNow
}

// receive returns every message published on any of channels with a seq
// greater than what stepID has already been delivered for that channel
// (cursors are tracked by SEQUENCE NUMBER, not slice index, specifically so
// front-eviction in publish() can never desync a cursor — an index-based
// cursor would silently skip or re-deliver messages once the log's front
// gets trimmed). A step only ever sees channels it actually asked about —
// receive never scans channels outside the requested set, even ones with
// pending messages, so a step cannot eavesdrop on a channel it wasn't
// handed by the caller (dispatchActionOutcome only ever passes the step's
// own ConsumesChannel list — see swarmReceiveOutcome). A slow subscriber
// whose cursor has fallen behind the retained window simply catches up from
// the oldest message still retained — it has no way to know how many were
// evicted before it caught up; swarmReceiveOutcome surfaces that count
// separately so the caller isn't silently left in the dark.
func (b *swarmChannelBroker) receive(stepID string, channels []string) []swarmChannelMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cursor[stepID] == nil {
		b.cursor[stepID] = map[string]int{}
	}
	var out []swarmChannelMessage
	for _, ch := range channels {
		last := b.cursor[stepID][ch]
		for _, m := range b.messages[ch] {
			if m.Seq > last {
				out = append(out, m)
			}
		}
		if log := b.messages[ch]; len(log) > 0 {
			b.cursor[stepID][ch] = log[len(log)-1].Seq
		}
	}
	return out
}

// evictedCount reports the total number of messages evicted so far for
// channel (0 if the channel was never over cap).
func (b *swarmChannelBroker) evictedCount(channel string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.evicted[channel]
}

// stats reports run-wide totals for the P5 observability rollup: how many
// messages have ever been published across every channel in this run, and
// how many were evicted under the per-channel cap. Cheap — bounded by the
// number of distinct channels a run actually used, not by message volume.
func (b *swarmChannelBroker) stats() (published, evicted int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	published = b.nextSeq
	for _, n := range b.evicted {
		evicted += n
	}
	return published, evicted
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

// swarmChannelInstructionSuffix is appended to EVERY streaming-mode step's
// task text (not only ones that declare consumes_channel — see the
// publish-only note below): swarm_publish is available to any step
// regardless of subscriptions, so a publish-only step must be told the tool
// exists too, or a real model has no way to discover it (unlike
// consumes_channel, which only ever needs to inform a SUBSCRIBING step).
// This used to be conditioned entirely on len(subscribed) > 0, which meant a
// step that only wanted to publish progress — never subscribing to
// anything — was never told "swarm_publish" exists at all.
func swarmChannelInstructionSuffix(subscribed []string) string {
	publishNote := `

You may call the "swarm_publish" tool ({"channel":"...","payload":{...}}) at
any point to send a live message to other steps in this workflow run — no
subscription needed to send.`
	if len(subscribed) == 0 {
		return publishNote
	}
	return publishNote + fmt.Sprintf(`

This step is ALSO subscribed to the following swarm channel(s): %s. Other
steps in this run may publish to these channels WHILE THEY ARE STILL RUNNING
(not just after they finish). Call the "swarm_receive" tool (optionally with
a "channel" field to check just one) at any point to pull new messages since
you last checked; it returns immediately with whatever is available,
possibly nothing.`, joinQuoted(subscribed))
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
	msg, evictedTotal, evictedNow := binding.broker.publish(act.Channel, binding.stepID, payload)
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
		"status": "swarm_channel_published", "channel": act.Channel, "from_step": binding.stepID, "seq": msg.Seq,
	}, dec.DecisionID)
	if evictedNow {
		// Audited once per eviction EVENT, not once per publish while a
		// channel stays over cap — logging every single publish once
		// steady-state eviction kicks in would itself become the firehose
		// problem this cap exists to prevent.
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
			"status": "swarm_channel_evicted", "channel": act.Channel, "cap": maxMessagesPerChannel, "evicted_total": evictedTotal,
		}, "")
	}
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
	// A slow subscriber has no other way to learn some messages never made
	// it into the retained window (see maxMessagesPerChannel) — surface it
	// as a plain heads-up rather than leaving it silently invisible.
	evictedTotal := 0
	for _, ch := range channels {
		evictedTotal += binding.broker.evictedCount(ch)
	}
	evictedNote := ""
	if evictedTotal > 0 {
		evictedNote = fmt.Sprintf("\n\n(note: %d older message(s) on the queried channel(s) were evicted under the %d-message-per-channel cap before you read them)", evictedTotal, maxMessagesPerChannel)
	}
	if len(msgs) == 0 {
		return toolCompleted("no new messages" + evictedNote)
	}
	b, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		return toolFailed("error: "+err.Error(), "tool_error")
	}
	return toolCompleted(string(b) + evictedNote)
}
