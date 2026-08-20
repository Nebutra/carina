package daemon

import "strings"

// defaultInteractiveAgent is the operator-facing default when the client does
// not name an agent. Build remains available via an explicit agent field
// (TUI /agent, gateway carina/build, plan approval).
const defaultInteractiveAgent = "converse"

// shouldLoadProjectInstructions is a mode switch, not an utterance classifier.
// converse does not dump AGENTS.md; the model may read it with a tool if the
// ask needs repository conventions. build/plan always load. explore never.
func shouldLoadProjectInstructions(agent string) bool {
	switch strings.TrimSpace(agent) {
	case "explore":
		return false
	case "build", "plan":
		return true
	default:
		return false
	}
}
