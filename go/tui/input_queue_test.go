package tui

import "testing"

func TestInputQueuePreservesFIFOAndDraftOwnership(t *testing.T) {
	q := inputQueue{}
	first := promptDraft{Text: "first", Paste: []string{"one\ntwo"}}
	q.enqueue(first)
	q.enqueue(promptDraft{Text: "second"})
	first.Paste[0] = "mutated"

	got, ok := q.popFront()
	if !ok || got.Text != "first" || len(got.Paste) != 1 || got.Paste[0] != "one\ntwo" {
		t.Fatalf("first queued draft = %#v, ok=%v", got, ok)
	}
	got.Paste[0] = "changed after pop"

	last, ok := q.popBack()
	if !ok || last.Text != "second" || q.len() != 0 {
		t.Fatalf("last queued draft = %#v, ok=%v, remaining=%d", last, ok, q.len())
	}
}

func TestInputQueueDrainReturnsIndependentDrafts(t *testing.T) {
	q := inputQueue{}
	q.enqueue(promptDraft{Text: "a", Paste: []string{"payload"}})
	q.enqueue(promptDraft{Text: "b"})

	drained := q.drain()
	if len(drained) != 2 || q.len() != 0 {
		t.Fatalf("drained=%#v remaining=%d", drained, q.len())
	}
	drained[0].Paste[0] = "changed"
	q.enqueue(promptDraft{Text: "c", Paste: []string{"fresh"}})
	got, _ := q.popFront()
	if got.Paste[0] != "fresh" {
		t.Fatalf("queue retained an aliased paste: %#v", got)
	}
}
