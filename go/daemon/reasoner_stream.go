package daemon

import (
	"context"
	"strings"
	"sync"
)

// ReasonerStreamUpdate is the public, provider-neutral stream contract. Raw
// provider tokens remain private; Text contains only decoded final-answer
// content from a complete done action.
type ReasonerStreamUpdate struct {
	Generation uint64
	Reset      bool
	Completed  bool
	Text       string
}

const assistantPhaseFinalAnswer = "final_answer"

type reasonerStreamContextKey struct{}

type reasonerStreamController struct {
	mu         sync.Mutex
	generation uint64
	callback   func(ReasonerStreamUpdate)
}

func newReasonerStreamController(callback func(ReasonerStreamUpdate)) *reasonerStreamController {
	return &reasonerStreamController{callback: callback}
}

func withReasonerStream(ctx context.Context, stream *reasonerStreamController) context.Context {
	if stream == nil {
		return ctx
	}
	return context.WithValue(ctx, reasonerStreamContextKey{}, stream)
}

func reasonerStreamFrom(ctx context.Context) *reasonerStreamController {
	stream, _ := ctx.Value(reasonerStreamContextKey{}).(*reasonerStreamController)
	return stream
}

func (s *reasonerStreamController) reset() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	s.generation++
	generation, callback := s.generation, s.callback
	s.mu.Unlock()
	if callback != nil {
		callback(ReasonerStreamUpdate{Generation: generation, Reset: true})
	}
	return generation
}

func (s *reasonerStreamController) emit(text string) {
	if s == nil || text == "" {
		return
	}
	s.mu.Lock()
	if s.generation == 0 {
		s.generation = 1
	}
	update, callback := ReasonerStreamUpdate{Generation: s.generation, Text: text}, s.callback
	s.mu.Unlock()
	if callback != nil {
		callback(update)
	}
}

func (s *reasonerStreamController) complete(text string) {
	if s == nil || text == "" {
		return
	}
	s.mu.Lock()
	if s.generation == 0 {
		s.generation = 1
	}
	update, callback := ReasonerStreamUpdate{Generation: s.generation, Completed: true, Text: text}, s.callback
	s.mu.Unlock()
	if callback != nil {
		callback(update)
	}
}

// actionEnvelopeStreamDecoder is intentionally private. It builds a parse-only
// closed view of each valid JSON prefix and exposes summary text only after the
// action has structurally classified as done. Tool arguments, thoughts,
// malformed JSON, and raw partial JSON never cross this boundary.
type actionEnvelopeStreamDecoder struct {
	raw     strings.Builder
	emitted string
}

func (d *actionEnvelopeStreamDecoder) Reset() {
	d.raw.Reset()
	d.emitted = ""
}

func (d *actionEnvelopeStreamDecoder) Push(delta string) string {
	if delta == "" || d.raw.Len()+len(delta) > maxProviderResponseBytes {
		return ""
	}
	d.raw.WriteString(delta)
	prefix, ok := closeActionJSONPrefix(d.raw.String())
	if !ok {
		return ""
	}
	action, err := parseAction(prefix)
	if err != nil || action.Tool != "done" || action.Summary == "" {
		return ""
	}
	// A JSON document nested inside summary is a presentation wrapper, not
	// public prose. Keep it private until the complete action can be normalized.
	if first := strings.TrimSpace(action.Summary); strings.HasPrefix(first, "{") || strings.HasPrefix(first, "[") {
		return ""
	}
	if !strings.HasPrefix(action.Summary, d.emitted) {
		return ""
	}
	next := strings.TrimPrefix(action.Summary, d.emitted)
	d.emitted = action.Summary
	return next
}

// closeActionJSONPrefix creates a parse-only view of an in-flight JSON action.
// It closes the current string and containers without mutating the retained raw
// bytes. Incomplete escapes are excluded until their remaining bytes arrive,
// so decoded public text is always a prefix of the eventual JSON string.
func closeActionJSONPrefix(raw string) (string, bool) {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return "", false
	}
	value := raw[start:]
	stack := make([]byte, 0, 4)
	inString := false
	escapeStart := -1
	unicodeDigits := 0
	for index := 0; index < len(value); index++ {
		current := value[index]
		if inString {
			if unicodeDigits > 0 {
				if !isJSONHex(current) {
					return "", false
				}
				unicodeDigits--
				if unicodeDigits == 0 {
					escapeStart = -1
				}
				continue
			}
			if escapeStart >= 0 {
				switch current {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					escapeStart = -1
				case 'u':
					unicodeDigits = 4
				default:
					return "", false
				}
				continue
			}
			switch current {
			case '\\':
				escapeStart = index
			case '"':
				inString = false
			case 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
				0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
				0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
				0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f:
				return "", false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != current {
				return "", false
			}
			stack = stack[:len(stack)-1]
		}
	}

	end := len(value)
	var completed strings.Builder
	completed.Grow(len(value) + len(stack) + 1)
	if inString && escapeStart >= 0 {
		end = escapeStart
	} else if inString && end >= 6 && value[end-6] == '\\' && value[end-5] == 'u' {
		if codepoint, ok := decodeJSONHex4(value[end-4 : end]); ok && codepoint >= 0xd800 && codepoint <= 0xdbff {
			end -= 6
		}
	}
	completed.WriteString(value[:end])
	if inString {
		completed.WriteByte('"')
	}
	for index := len(stack) - 1; index >= 0; index-- {
		completed.WriteByte(stack[index])
	}
	return completed.String(), true
}

func isJSONHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func decodeJSONHex4(value string) (uint16, bool) {
	if len(value) != 4 {
		return 0, false
	}
	var decoded uint16
	for index := 0; index < len(value); index++ {
		decoded <<= 4
		switch current := value[index]; {
		case current >= '0' && current <= '9':
			decoded += uint16(current - '0')
		case current >= 'a' && current <= 'f':
			decoded += uint16(current-'a') + 10
		case current >= 'A' && current <= 'F':
			decoded += uint16(current-'A') + 10
		default:
			return 0, false
		}
	}
	return decoded, true
}

type assistantStreamPublisher struct {
	d                *Daemon
	sessionID        string
	taskID           string
	structuredOutput bool
	mu               sync.Mutex
	generation       uint64
	sequence         uint64
}

func (p *assistantStreamPublisher) publish(update ReasonerStreamUpdate) {
	if p == nil || p.d == nil || update.Generation == 0 {
		return
	}
	p.mu.Lock()
	if update.Generation < p.generation {
		p.mu.Unlock()
		return
	}
	if update.Generation > p.generation {
		p.generation = update.Generation
		p.sequence = 0
	}
	if update.Reset {
		p.sequence++
		sequence := p.sequence
		p.mu.Unlock()
		p.d.publishAssistantStreamEvent(p.sessionID, p.taskID, "assistant.message.reset", map[string]any{
			"generation": update.Generation, "sequence": sequence, "phase": assistantPhaseFinalAnswer,
		})
		return
	}
	if update.Text == "" {
		p.mu.Unlock()
		return
	}
	p.sequence++
	sequence := p.sequence
	p.mu.Unlock()
	kind, key := "assistant.message.delta", "delta"
	if update.Completed {
		kind, key = "assistant.message.completed", "content"
	}
	p.d.publishAssistantStreamEvent(p.sessionID, p.taskID, kind, map[string]any{
		"generation": update.Generation, "sequence": sequence, "phase": assistantPhaseFinalAnswer,
		"structured_output": p.structuredOutput, key: update.Text,
	})
}

// Assistant stream events are transient product projections. They bypass the
// durable audit writer so private inference fragments can never be replayed or
// mistaken for authoritative task history.
func (d *Daemon) publishAssistantStreamEvent(sessionID, taskID, kind string, payload map[string]any) {
	d.events.Publish(sessionID, map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
		"type":       kind,
		"actor":      "model",
		"payload":    payload,
	})
	if d.journey != nil {
		d.journey.observeEvent(kind, taskID)
	}
}
