package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExampleWorkflowsParseAndValidate keeps examples/workflows/*.json
// honest against real drift in WorkflowSpec/WorkflowStep — a docs example
// that silently stops parsing after a field rename or a validate() rule
// change is worse than no example at all (a user copies it, hits a
// confusing error, and has no idea whether they made a mistake or the
// example is just stale).
func TestExampleWorkflowsParseAndValidate(t *testing.T) {
	dir := "../../examples/workflows"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		found++
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		spec, err := parseWorkflowSpec(raw)
		if err != nil {
			t.Fatalf("%s: parse: %v", e.Name(), err)
		}
		if err := spec.validate(); err != nil {
			t.Fatalf("%s: validate: %v", e.Name(), err)
		}
		t.Logf("%s: OK (%d steps, execution_mode=%q)", e.Name(), len(spec.Steps), spec.ExecutionMode)
	}
	if found == 0 {
		t.Fatal("no example workflow files found")
	}
}
