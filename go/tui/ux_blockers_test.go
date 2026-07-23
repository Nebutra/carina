package tui

import (
	"errors"
	"strings"
	"testing"
)

func TestQueuedAsyncSlashRunsBeforeQueueContinues(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.items": canonicalFixture(), "task.submit": map[string]any{"task_id": "next"}}}
	m, _ := newTestModel(fc)
	m.followUps.enqueue(promptDraft{Text: "/search hidden-secret-marker"})
	m.followUps.enqueue(promptDraft{Text: "next task"})
	cmd := m.maybeSubmitNextQueued()
	if cmd == nil {
		t.Fatal("queued async slash returned no command")
	}
	_, next := m.Update(cmd())
	if fc.calls[0].method != "session.items" || next == nil {
		t.Fatalf("async slash/continuation = calls %#v next %v", fc.calls, next)
	}
	drain(m, next)
	if fc.last().method != "task.submit" {
		t.Fatalf("queue did not continue after slash: %#v", fc.calls)
	}
}

func TestModeCommitsOnlyAfterDaemonAck(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.plan_mode": errors.New("denied")}}
	m, _ := newTestModel(fc)
	cmd := m.slashCommand("/mode plan")
	if m.mode != "build" {
		t.Fatal("mode changed before RPC acknowledgement")
	}
	m.Update(cmd())
	if m.mode != "build" {
		t.Fatal("failed RPC changed local mode")
	}
	fc.handler["session.plan_mode"] = map[string]any{"plan_mode": true}
	m.Update(m.slashCommand("/mode plan")())
	if m.mode != "plan" {
		t.Fatal("successful RPC did not commit mode")
	}
}

func TestCanonicalPagerDropsResponseFromClosedGeneration(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.items": canonicalFixture()}}
	m, _ := newTestModel(fc)
	old := m.slashCommand("/transcript")
	m.closeTranscriptPager()
	newer := m.slashCommand("/transcript")
	m.Update(old())
	if m.transcriptPager.text != "" || !m.transcriptPager.loading {
		t.Fatal("stale canonical response populated newer pager")
	}
	m.Update(newer())
	if !strings.Contains(m.transcriptPager.text, "item_hidden") {
		t.Fatal("current canonical response was dropped")
	}
}

func TestConnectingAndDisconnectedOverlaysAreVisible(t *testing.T) {
	m, _ := newTestModel(nil)
	if got := m.banner(); !strings.Contains(got, "Opening") {
		t.Fatalf("connecting banner = %q", got)
	}
	m.conn = ConnLost
	m.approval = &approvalState{DecisionID: "perm_1", Action: "run", Resource: "cmd"}
	if got := m.overlayView(); !strings.Contains(got, "Connection unavailable") {
		t.Fatalf("approval hid disconnect: %q", got)
	}
	m.approval = nil
	m.question = &questionState{QuestionID: "q1", Prompt: "choose", Options: []questionOption{{Label: "one", Value: "1"}}}
	if got := m.questionOverlayView(); !strings.Contains(got, "Connection unavailable") {
		t.Fatalf("question hid disconnect: %q", got)
	}
}

func TestLoopUsesScheduleRPCAndFiltersCurrentSession(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"schedule.create": map[string]any{"schedule_id": "sched_1"},
		"schedule.list": map[string]any{"schedules": []map[string]any{
			{"schedule_id": "mine", "session_id": "sess_test", "expression": "5m", "enabled": true, "prompt": "check"},
			{"schedule_id": "other", "session_id": "other_session", "expression": "1m", "enabled": true, "prompt": "secret other"},
		}},
		"schedule.pause": map[string]any{"schedule_id": "mine"},
	}}
	m, _ := newTestModel(fc)
	m.Update(m.slashCommand("/loop 5m check health")())
	call := fc.last()
	if call.method != "schedule.create" || call.params["kind"] != "every" || call.params["expression"] != "5m" || call.params["prompt"] != "check health" || call.params["concurrency_policy"] != "forbid" {
		t.Fatalf("loop create = %#v", call)
	}
	m.model, m.reasoningEffort = "openai/gpt-5", "high"
	m.Update(m.slashCommand("/loop 1m --concurrency queue inspect changes")())
	call = fc.last()
	if call.params["concurrency_policy"] != "queue" || call.params["model"] != "openai/gpt-5" || call.params["reasoning_effort"] != "high" || call.params["prompt"] != "inspect changes" {
		t.Fatalf("loop frozen envelope = %#v", call)
	}
	m.Update(m.slashCommand("/loop list")())
	text := m.tr.plainText()
	if !strings.Contains(text, "mine") || strings.Contains(text, "secret other") {
		t.Fatalf("loop list scope leak: %s", text)
	}
	m.Update(m.slashCommand("/loop pause mine")())
	if fc.last().method != "schedule.pause" || fc.last().params["schedule_id"] != "mine" {
		t.Fatalf("loop pause = %#v", fc.last())
	}
}
