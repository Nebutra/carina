package tui

// inputQueue holds follow-up drafts that should start after the active task
// finishes. Drafts remain local until dequeued, so the operator can inspect or
// edit them without racing the daemon's steering mailbox.
type inputQueue struct {
	drafts []promptDraft
}

func (q *inputQueue) len() int { return len(q.drafts) }

func (q *inputQueue) enqueue(draft promptDraft) {
	q.drafts = append(q.drafts, cloneDraft(draft))
}

func (q *inputQueue) front() (promptDraft, bool) {
	if len(q.drafts) == 0 {
		return promptDraft{}, false
	}
	return cloneDraft(q.drafts[0]), true
}

func (q *inputQueue) popFront() (promptDraft, bool) {
	if len(q.drafts) == 0 {
		return promptDraft{}, false
	}
	draft := cloneDraft(q.drafts[0])
	copy(q.drafts, q.drafts[1:])
	q.drafts = q.drafts[:len(q.drafts)-1]
	return draft, true
}

func (q *inputQueue) popBack() (promptDraft, bool) {
	if len(q.drafts) == 0 {
		return promptDraft{}, false
	}
	last := len(q.drafts) - 1
	draft := cloneDraft(q.drafts[last])
	q.drafts = q.drafts[:last]
	return draft, true
}

func (q *inputQueue) drain() []promptDraft {
	if len(q.drafts) == 0 {
		return nil
	}
	out := make([]promptDraft, len(q.drafts))
	for i := range q.drafts {
		out[i] = cloneDraft(q.drafts[i])
	}
	q.drafts = nil
	return out
}

func cloneDraft(draft promptDraft) promptDraft {
	draft.Prefix = append([]string(nil), draft.Prefix...)
	draft.Paste = append([]string(nil), draft.Paste...)
	return draft
}
