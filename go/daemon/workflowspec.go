package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkflowStep is one node in a workflow DAG: it delegates to a named agent
// (an AgentSpec) with a task, and may depend on earlier steps. The task text
// may reference earlier step outputs as ${step_id} and the workflow input as
// ${input} — substituted at run time.
type WorkflowStep struct {
	ID    string   `json:"id"`
	Agent string   `json:"agent"`
	Task  string   `json:"task"`
	Needs []string `json:"needs,omitempty"`
}

// WorkflowSpec is a declarative multi-step agent pipeline. It is the Carina
// analogue of the Workflow-orchestration mechanism: deterministic control flow
// (a dependency DAG) fanned out over isolated, capability-attenuated subagents,
// with every step audited and resumable.
type WorkflowSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Steps       []WorkflowStep `json:"steps"`
	Source      string         `json:"-"` // "user" | "project"
}

func parseWorkflowSpec(raw []byte) (*WorkflowSpec, error) {
	var s WorkflowSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.Name == "" {
		return nil, fmt.Errorf("workflow spec needs a name")
	}
	if len(s.Steps) == 0 {
		return nil, fmt.Errorf("workflow %q has no steps", s.Name)
	}
	return &s, nil
}

// loadWorkflowSpecs discovers workflows from the user dir (~/.carina/workflows)
// and the project dir (<workspace>/.carina/workflows). Project workflows
// override user ones — mirroring how agent specs are discovered.
func loadWorkflowSpecs(workspaceRoot string) map[string]*WorkflowSpec {
	out := map[string]*WorkflowSpec{}
	if home, err := os.UserHomeDir(); err == nil {
		loadWorkflowsFromDir(filepath.Join(home, ".carina", "workflows"), "user", out)
	}
	if workspaceRoot != "" {
		loadWorkflowsFromDir(filepath.Join(workspaceRoot, ".carina", "workflows"), "project", out)
	}
	return out
}

func loadWorkflowsFromDir(dir, source string, out map[string]*WorkflowSpec) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if spec, err := parseWorkflowSpec(raw); err == nil {
			spec.Source = source
			out[spec.Name] = spec
		}
	}
}

// validate rejects malformed DAGs: empty/duplicate ids, dangling deps, and
// dependency cycles (caught by the topological sort).
func (s *WorkflowSpec) validate() error {
	ids := map[string]bool{}
	for _, st := range s.Steps {
		if st.ID == "" {
			return fmt.Errorf("workflow %q has a step with an empty id", s.Name)
		}
		if ids[st.ID] {
			return fmt.Errorf("workflow %q has a duplicate step id %q", s.Name, st.ID)
		}
		if st.Agent == "" {
			return fmt.Errorf("workflow step %q has no agent", st.ID)
		}
		ids[st.ID] = true
	}
	for _, st := range s.Steps {
		for _, n := range st.Needs {
			if !ids[n] {
				return fmt.Errorf("step %q needs unknown step %q", st.ID, n)
			}
			if n == st.ID {
				return fmt.Errorf("step %q depends on itself", st.ID)
			}
		}
	}
	if _, err := s.topoOrder(); err != nil {
		return err
	}
	return nil
}

// topoOrder returns a valid execution order via Kahn's algorithm, or an error
// if the graph has a cycle. (The engine does not use the order directly — it
// schedules by readiness — but this proves the DAG is acyclic.)
func (s *WorkflowSpec) topoOrder() ([]string, error) {
	indeg := map[string]int{}
	adj := map[string][]string{}
	for _, st := range s.Steps {
		indeg[st.ID] = 0
	}
	for _, st := range s.Steps {
		for _, n := range st.Needs {
			adj[n] = append(adj[n], st.ID)
			indeg[st.ID]++
		}
	}
	var q []string
	for _, st := range s.Steps {
		if indeg[st.ID] == 0 {
			q = append(q, st.ID)
		}
	}
	var order []string
	for len(q) > 0 {
		id := q[0]
		q = q[1:]
		order = append(order, id)
		for _, m := range adj[id] {
			indeg[m]--
			if indeg[m] == 0 {
				q = append(q, m)
			}
		}
	}
	if len(order) != len(s.Steps) {
		return nil, fmt.Errorf("workflow %q has a dependency cycle", s.Name)
	}
	return order, nil
}
