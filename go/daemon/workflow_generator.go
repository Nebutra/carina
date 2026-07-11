package daemon

import (
	"encoding/json"
	"fmt"
)

// generatorInstructionSuffix is appended to a "generator" step's task text so
// the subagent knows the strict envelope its "done" summary must produce.
// Mirrors the established convention from bestofn.go's candidateEnvelope and
// public-subagent-dsl's structured-output pattern: a fixed JSON shape, no
// markdown fences, no prose.
const generatorInstructionSuffix = `

This step may dynamically extend the workflow graph. If you want to add new
steps, finish with "done" whose "summary" field is STRICT JSON (no markdown
fences, no prose) with this exact shape:

{"spawn_steps":[{"id":"new_unique_id","agent":"agent_name","task":"...","needs":["existing_or_sibling_id"]}],"rationale":"why"}

Rules for spawn_steps: every "id" must be unique across the WHOLE workflow
(not just your new steps); "needs" may reference any already-declared step
id (including ones from your own spawn_steps list, sibling steps you are
adding in this same batch) but never a step that does not exist. If you have
nothing to add, finish with "done" and either omit spawn_steps or set it to
an empty array.`

type generatorEnvelope struct {
	SpawnSteps []WorkflowStep `json:"spawn_steps"`
	Rationale  string         `json:"rationale"`
}

// parseGeneratorEnvelope strictly parses a generator step's done summary. A
// summary that isn't the expected JSON shape at all (a generator agent that
// just wrote prose) is treated as "nothing to add" rather than an error —
// only a syntactically-present-but-invalid spawn_steps entry is an error.
func parseGeneratorEnvelope(output string) (*generatorEnvelope, error) {
	var env generatorEnvelope
	if err := json.Unmarshal([]byte(output), &env); err != nil {
		return &generatorEnvelope{}, nil //nolint:nilerr // not a JSON envelope at all => no steps to add, not a hard failure
	}
	return &env, nil
}

// injectGeneratedSteps validates and merges a generator step's declared new
// nodes into the still-running graph. Structurally cycle-safe by
// construction: a new step may depend on an existing step (already fixed,
// never mutated to point at something new) or a sibling in this same batch,
// but nothing pre-existing can ever be made to depend on something new — so
// a cycle back into the old graph is impossible; only a cycle purely within
// the new batch needs checking.
func (sc *streamCoordinator) injectGeneratedSteps(generatorID string, output string) error {
	env, err := parseGeneratorEnvelope(output)
	if err != nil {
		return err
	}
	if len(env.SpawnSteps) == 0 {
		return nil
	}

	depth := sc.genDepth[generatorID] + 1
	if depth > maxGeneratorDepth {
		return fmt.Errorf("generator step %q would exceed the max generator depth (%d)", generatorID, maxGeneratorDepth)
	}
	if sc.totalSteps+len(env.SpawnSteps) > maxStreamingWorkflowSteps {
		return fmt.Errorf("generator step %q would push the workflow over the %d-step limit (currently %d, adding %d)",
			generatorID, maxStreamingWorkflowSteps, sc.totalSteps, len(env.SpawnSteps))
	}

	newIDs := make(map[string]bool, len(env.SpawnSteps))
	for _, st := range env.SpawnSteps {
		if st.ID == "" {
			return fmt.Errorf("generator step %q: a spawned step has an empty id", generatorID)
		}
		if _, exists := sc.byID[st.ID]; exists {
			return fmt.Errorf("generator step %q: spawned step id %q collides with an existing step", generatorID, st.ID)
		}
		if newIDs[st.ID] {
			return fmt.Errorf("generator step %q: spawned step id %q is declared twice", generatorID, st.ID)
		}
		if st.Agent == "" {
			return fmt.Errorf("generator step %q: spawned step %q has no agent", generatorID, st.ID)
		}
		switch st.Kind {
		case "", "generator":
		default:
			return fmt.Errorf("generator step %q: spawned step %q has unknown kind %q", generatorID, st.ID, st.Kind)
		}
		newIDs[st.ID] = true
	}
	for _, st := range env.SpawnSteps {
		for _, n := range st.Needs {
			if n == st.ID {
				return fmt.Errorf("generator step %q: spawned step %q depends on itself", generatorID, st.ID)
			}
			if _, existsOld := sc.byID[n]; !existsOld && !newIDs[n] {
				return fmt.Errorf("generator step %q: spawned step %q needs unknown step %q", generatorID, st.ID, n)
			}
		}
	}
	if cycle := cyclicAmongNewSteps(env.SpawnSteps, newIDs); cycle != "" {
		return fmt.Errorf("generator step %q: spawned steps have a dependency cycle involving %q", generatorID, cycle)
	}

	// All validated — merge into the live graph. Register every new step's
	// identity and dependency edges first, THEN compute in-degree and
	// (maybe) dispatch, so cross-references between siblings in this same
	// batch resolve correctly regardless of slice order.
	for _, st := range env.SpawnSteps {
		sc.byID[st.ID] = st
		sc.genDepth[st.ID] = depth
		for _, n := range st.Needs {
			sc.dependents[n] = append(sc.dependents[n], st.ID)
		}
	}
	sc.totalSteps += len(env.SpawnSteps)
	sc.remainingToResolve += len(env.SpawnSteps)

	sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", map[string]any{
		"status": "workflow_steps_injected", "workflow": sc.spec.Name, "run_id": sc.runID,
		"generator_step": generatorID, "spawned": len(env.SpawnSteps), "gen_depth": depth,
		"rationale": truncate(env.Rationale, 300),
	}, "")

	for _, st := range env.SpawnSteps {
		n := 0
		for _, dep := range st.Needs {
			if sc.terminal[dep] != stepDone {
				n++
			}
		}
		sc.liveIndegree[st.ID] = n
		if n == 0 {
			sc.maybeDispatch(st.ID)
		}
	}
	return nil
}

// cyclicAmongNewSteps checks only the dependency edges that stay entirely
// within the newly-spawned batch (edges pointing at an existing step are
// irrelevant to cycle detection — an existing step can never be made to
// depend on something new). Returns the id of a step involved in a cycle, or
// "" if acyclic.
func cyclicAmongNewSteps(steps []WorkflowStep, newIDs map[string]bool) string {
	adj := make(map[string][]string, len(steps))
	indeg := make(map[string]int, len(steps))
	for _, st := range steps {
		indeg[st.ID] = 0
	}
	for _, st := range steps {
		for _, n := range st.Needs {
			if !newIDs[n] {
				continue // dependency on an existing step, not part of this cycle check
			}
			adj[n] = append(adj[n], st.ID)
			indeg[st.ID]++
		}
	}
	var q []string
	for _, st := range steps {
		if indeg[st.ID] == 0 {
			q = append(q, st.ID)
		}
	}
	visited := 0
	for len(q) > 0 {
		id := q[0]
		q = q[1:]
		visited++
		for _, m := range adj[id] {
			indeg[m]--
			if indeg[m] == 0 {
				q = append(q, m)
			}
		}
	}
	if visited != len(steps) {
		for _, st := range steps {
			if indeg[st.ID] > 0 {
				return st.ID
			}
		}
	}
	return ""
}
