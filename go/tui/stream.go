package tui

import (
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/tui/markdown"
)

// messageStream tracks one streaming assistant message end to end (milestone
// P2 of docs/plans/tui-rich-text.md, mirroring Codex streaming/controller.rs):
// the markdown.Stream owns the append-only sanitized source and its commit
// boundary; this side owns the transcript entries. Each message is projected
// as
//
//   - one keyed head entry carrying the header line (status flips from
//     running to its terminal state in place),
//   - zero or more immutable headerless chunk entries — the stable region,
//     appended (inserted before the tail) as commits happen,
//   - one keyed headerless tail entry — the mutable region, replaced in
//     place on every delta and removed once everything has committed.
//
// Chunk and tail entries carry the source in BodyMarkdown, so a resize or
// profile change re-renders them from source like every other presentation.
type messageStream struct {
	key string
	md  markdown.Stream
	seq int // committed chunk count; drives entry keys and blank separators
	// lastTail is the tail source currently held by the keyed tail entry (""
	// while the entry is absent). A delta that ends mid-line leaves the
	// newline-gated tail byte-identical, and skipping that no-op replace is
	// what keeps a per-token delta from re-rendering the whole held-back tail
	// (chroma included) and rebuilding the transcript.
	lastTail string
}

func (s *messageStream) headKey() string  { return "md:" + s.key }
func (s *messageStream) tailKey() string  { return "md:" + s.key + ":tail" }
func (s *messageStream) chunkKey() string { return fmt.Sprintf("md:%s:chunk:%d", s.key, s.seq) }

// lastOwnKey is the message's most recent immutable entry — the previous
// chunk, or the head before anything committed. It anchors inserts whenever
// the mutable tail entry is momentarily absent (the boundary sat exactly on a
// blank line), so an interleaved event can never capture the next chunk or a
// recreated tail under its own header.
func (s *messageStream) lastOwnKey() string {
	if s.seq > 0 {
		return fmt.Sprintf("md:%s:chunk:%d", s.key, s.seq-1)
	}
	return s.headKey()
}

// applyStreamDelta ingests one assistant output delta. delta is sanitized
// here — the same boundary every other inbound string crosses — before it
// reaches the append-only source; all styling below this point is
// renderer-emitted. done marks the end of the stream.
func (m *Model) applyStreamDelta(key, timestamp, delta string, done bool) {
	if key == "" {
		return
	}
	before := len(m.tr.lines)
	changed := false
	s := m.streams[key]
	if s == nil {
		s = &messageStream{key: key}
		if m.streams == nil {
			m.streams = make(map[string]*messageStream)
		}
		m.streams[key] = s
		m.tr.pushPresentation(m.streamHead(s, timestamp, statusRunning), m.th, m.transcriptWidth())
		changed = true
	}
	stable, tail := s.md.Push(sanitize(delta))
	changed = m.commitStreamChunk(s, stable) || changed
	changed = m.updateStreamTail(s, tail) || changed
	if done {
		m.commitStreamChunk(s, s.md.Finish())
		m.tr.removePresentation(s.tailKey())
		m.tr.pushPresentation(m.streamHead(s, timestamp, statusSuccess), m.th, m.transcriptWidth())
		delete(m.streams, key)
		changed = true
	}
	m.afterTranscriptChange(before, changed)
}

// streamHead builds the keyed header entry for a streaming message. It is the
// same agent/model projection presentModelEvent emits, so the header reads
// identically whether the response streamed or arrived whole.
func (m *Model) streamHead(s *messageStream, timestamp string, status presentationStatus) eventPresentation {
	p := eventPresentation{
		Key:       s.headKey(),
		Kind:      presentationAgent,
		Status:    status,
		Timestamp: timestamp,
		Title:     "model",
	}
	if status == statusSuccess {
		p.Summary = "completed"
	}
	localizePresentation(&p, newLocalizer(m.locale))
	return p
}

// commitStreamChunk appends one stable source chunk as an immutable
// headerless entry in the slot the mutable tail occupies — or, when the tail
// entry is absent, directly after the message's own last entry. It reports
// whether the transcript changed.
func (m *Model) commitStreamChunk(s *messageStream, stable string) bool {
	if strings.TrimSpace(stable) == "" {
		return false
	}
	anchor := s.lastOwnKey()
	p := eventPresentation{
		Key:          s.chunkKey(),
		Headerless:   true,
		LeadingBlank: s.seq > 0,
		BodyMarkdown: stable,
	}
	s.seq++
	m.tr.insertPresentationBefore(s.tailKey(), anchor, p, m.th, m.transcriptWidth())
	return true
}

// updateStreamTail replaces (or creates) the keyed mutable tail entry, and
// removes it while the tail is empty so no blank placeholder line lingers.
// An unchanged tail — every mid-line delta, gated by the newline commit — is
// a no-op. It reports whether the transcript changed.
func (m *Model) updateStreamTail(s *messageStream, tail string) bool {
	if strings.TrimSpace(tail) == "" {
		s.lastTail = ""
		return m.tr.removePresentation(s.tailKey())
	}
	if tail == s.lastTail {
		return false
	}
	s.lastTail = tail
	m.tr.upsertPresentationAfter(s.lastOwnKey(), eventPresentation{
		Key:          s.tailKey(),
		Headerless:   true,
		LeadingBlank: s.seq > 0,
		BodyMarkdown: tail,
	}, m.th, m.transcriptWidth())
	return true
}

// afterTranscriptChange refreshes the viewport after direct transcript
// mutations, with the same follow-tail contract as pushEvent. changed marks
// whether any entry was actually mutated: a no-op delta (mid-line, nothing
// committed) must not inflate the unseen-lines counter, which reports lines,
// not token deltas.
func (m *Model) afterTranscriptChange(linesBefore int, changed bool) {
	m.vp.SetContentLines(m.tr.lines)
	added := len(m.tr.lines) - linesBefore
	if added < 1 {
		added = 0
		if changed {
			// An in-place tail replacement is still unseen activity, but should
			// not pretend a whole block was appended.
			added = 1
		}
	}
	if m.followTail {
		m.vp.GotoBottom()
		m.unseenLines = 0
	} else {
		m.unseenLines += added
	}
}

// handleStreamEvent projects a ModelOutputDelta envelope onto the streaming
// pipeline. The payload carries the user-facing final-response text only:
// chain-of-thought and other suppressed internals must never be routed into
// this event (a payload marked with a non-final channel is dropped here as a
// second line of defense).
func (m *Model) handleStreamEvent(ev map[string]any) {
	payload, _ := ev["payload"].(map[string]any)
	if channel := str(payload["channel"]); channel != "" && channel != "final" {
		return
	}
	key := firstValue(payload, "call_id", "task_id")
	if key == "" {
		return
	}
	done := false
	if d, ok := payload["done"].(bool); ok {
		done = d
	}
	m.applyStreamDelta(key, presentationTimestamp(ev), str(payload["delta"]), done)
}
