package modelrouter

import "context"

// MockProvider is the always-available fallback provider. It lets the whole
// runtime run offline for demos, tests, and CI (PRD §16.3: LLM latency is
// not the point — safety/audit/rollback are). It echoes a deterministic
// acknowledgement so the agent loop and event log stay exercised.
type MockProvider struct{}

func NewMockProvider() *MockProvider { return &MockProvider{} }

func (m *MockProvider) Name() string { return "mock" }

func (m *MockProvider) Complete(_ context.Context, req Request) (*Response, error) {
	text := "[mock] received task: " + req.Prompt +
		"\nPlan: read workspace, locate the failing area, propose a patch, run tests, report."
	return &Response{
		Provider:     m.Name(),
		Model:        "mock-1",
		Text:         text,
		InputTokens:  len(req.Prompt) / 4,
		OutputTokens: len(text) / 4,
	}, nil
}
