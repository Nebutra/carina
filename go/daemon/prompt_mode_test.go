package daemon

import "testing"

func TestShouldLoadProjectInstructionsByMode(t *testing.T) {
	t.Parallel()
	if shouldLoadProjectInstructions("converse") {
		t.Fatal("converse must not dump project instructions; the model may read them if the ask needs them")
	}
	if shouldLoadProjectInstructions("") {
		t.Fatal("unnamed agent follows converse: no host utterance classifier")
	}
	if shouldLoadProjectInstructions("explore") {
		t.Fatal("explore must never load project instructions")
	}
	if !shouldLoadProjectInstructions("build") {
		t.Fatal("build mode always loads project instructions")
	}
	if !shouldLoadProjectInstructions("plan") {
		t.Fatal("plan mode always loads project instructions")
	}
}
