package daemon

import "testing"

func TestLooksLikeRepoWork(t *testing.T) {
	t.Parallel()
	chatter := []string{"hi", "hello", "hey.", "你好", "thanks", "who are you", "what can you do"}
	for _, prompt := range chatter {
		if looksLikeRepoWork(prompt) {
			t.Errorf("%q must not count as repo work", prompt)
		}
		if shouldLoadProjectInstructions("converse", prompt) {
			t.Errorf("converse greeting %q must not load project instructions", prompt)
		}
	}
	repo := []string{
		"fix the parser in agent.go",
		"implement tabs in the workspace",
		"search the repo for composeAgentPromptLayers",
		"看看代码",
		"refactor promptcache.go",
	}
	for _, prompt := range repo {
		if !looksLikeRepoWork(prompt) {
			t.Errorf("%q must count as repo work", prompt)
		}
	}
	if shouldLoadProjectInstructions("explore", "fix agent.go") {
		t.Fatal("explore must never load project instructions")
	}
	if !shouldLoadProjectInstructions("build", "hi") {
		t.Fatal("build mode always loads project instructions")
	}
	if !shouldLoadProjectInstructions("plan", "hi") {
		t.Fatal("plan mode always loads project instructions")
	}
	if looksLikeRepoWork("what is rust ownership") {
		t.Fatal("a general question is not repo work")
	}
}
