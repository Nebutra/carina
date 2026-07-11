package daemon

import "testing"

// TestActionSignatureCanonicalAcrossAllFields verifies action.signature()
// covers parameter fields beyond the five hand-picked ones the old
// tool+path+command+pattern+query+name fingerprint used (agent.go's
// runLoopContext, pre-tightening). A field like Content or Target changing
// with everything else held constant must change the signature, otherwise a
// stuck model could dodge loop detection by varying an ignored field.
func TestActionSignatureCanonicalAcrossAllFields(t *testing.T) {
	base := action{Tool: "patch", Path: "a.go", OldText: "foo", Content: "bar"}
	changedContent := base
	changedContent.Content = "baz"
	if base.signature() == changedContent.signature() {
		t.Fatal("signature must change when Content changes, even though the old 5-field fingerprint ignored Content")
	}

	changedTarget := base
	changedTarget.Target = "somewhere"
	if base.signature() == changedTarget.signature() {
		t.Fatal("signature must change when Target changes")
	}

	changedOldText := base
	changedOldText.OldText = "different"
	if base.signature() == changedOldText.signature() {
		t.Fatal("signature must change when OldText changes")
	}
}

// TestActionSignatureExcludesThought verifies free-form Thought text does not
// participate in the signature, so a stuck model can't evade repeat detection
// by simply rephrasing its reasoning while repeating the same action.
func TestActionSignatureExcludesThought(t *testing.T) {
	a1 := action{Tool: "read", Path: "a.go", Thought: "let me check this file"}
	a2 := action{Tool: "read", Path: "a.go", Thought: "actually let me re-verify by reading it again, differently worded"}
	if a1.signature() != a2.signature() {
		t.Fatal("signature must be identical when only Thought differs")
	}
}

// TestActionSignatureIncludesActions verifies different batch payloads cannot
// collide in the loop guard.
func TestActionSignatureIncludesActions(t *testing.T) {
	a1 := action{Tool: "batch", Actions: []action{{Tool: "read", Path: "a.go"}}}
	a2 := action{Tool: "batch", Actions: []action{{Tool: "read", Path: "b.go"}}}
	if a1.signature() == a2.signature() {
		t.Fatal("signature must change when nested Actions differ")
	}
}

// TestActionSignatureStableAndDeterministic verifies repeated calls on an
// unchanged action produce the same signature (required for LoopGuard's
// counting to work at all) and that unrelated actions differ.
func TestActionSignatureStableAndDeterministic(t *testing.T) {
	a := action{Tool: "search", Pattern: "TODO"}
	if a.signature() != a.signature() {
		t.Fatal("signature must be deterministic for the same action")
	}
	other := action{Tool: "search", Pattern: "FIXME"}
	if a.signature() == other.signature() {
		t.Fatal("distinct actions must produce distinct signatures")
	}
}
