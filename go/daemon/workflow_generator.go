package daemon

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/Nebutra/carina/go/workflowui"
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

func (sc *streamCoordinator) restoreGeneratedSteps(persisted []persistedGeneratedStep) error {
	if len(persisted)+len(sc.byID) > maxStreamingWorkflowSteps {
		return fmt.Errorf("persisted generated graph exceeds the %d-step limit", maxStreamingWorkflowSteps)
	}
	for _, generated := range persisted {
		st := generated.Step
		if generated.GeneratorID == "" || generated.Depth < 1 || generated.Depth > maxGeneratorDepth {
			return fmt.Errorf("persisted generated step %q has invalid origin metadata", st.ID)
		}
		if _, ok := sc.byID[generated.GeneratorID]; !ok {
			return fmt.Errorf("persisted generated step %q references unknown generator %q", st.ID, generated.GeneratorID)
		}
		if st.ID == "" || st.Agent == "" {
			return fmt.Errorf("persisted generated step has an invalid id or agent")
		}
		if _, collision := sc.byID[st.ID]; collision {
			return fmt.Errorf("persisted generated step id %q collides with another definition", st.ID)
		}
		sc.byID[st.ID] = st
		sc.genDepth[st.ID] = generated.Depth
		sc.genOrigin[st.ID] = generated.GeneratorID
		sc.generated = append(sc.generated, generated)
	}
	steps := make([]WorkflowStep, 0, len(sc.byID))
	for _, st := range sc.byID {
		steps = append(steps, st)
	}
	return (&WorkflowSpec{Name: sc.spec.Name, ExecutionMode: "streaming", Steps: steps}).validate()
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
	// Every generated node has an implicit causal dependency on the generator
	// that declared it. Materialize that edge so crash recovery cannot dispatch
	// a restored node before an uncommitted generator replay has completed.
	for i := range env.SpawnSteps {
		if !containsString(env.SpawnSteps[i].Needs, generatorID) {
			env.SpawnSteps[i].Needs = append(env.SpawnSteps[i].Needs, generatorID)
		}
	}

	declaredIDs := make(map[string]bool, len(env.SpawnSteps))
	addedIDs := make(map[string]bool, len(env.SpawnSteps))
	for _, st := range env.SpawnSteps {
		if st.ID == "" {
			return fmt.Errorf("generator step %q: a spawned step has an empty id", generatorID)
		}
		if declaredIDs[st.ID] {
			return fmt.Errorf("generator step %q: spawned step id %q is declared twice", generatorID, st.ID)
		}
		declaredIDs[st.ID] = true
		if existing, exists := sc.byID[st.ID]; exists {
			if sc.genOrigin[st.ID] == generatorID && reflect.DeepEqual(existing, st) {
				continue
			}
			return fmt.Errorf("generator step %q: spawned step id %q collides with an existing step", generatorID, st.ID)
		}
		if st.Agent == "" {
			return fmt.Errorf("generator step %q: spawned step %q has no agent", generatorID, st.ID)
		}
		if err := validateWorkflowStepFeatures(st, true); err != nil {
			return fmt.Errorf("generator step %q: %w", generatorID, err)
		}
		addedIDs[st.ID] = true
	}
	newCount := len(addedIDs)
	if sc.totalSteps+newCount > maxStreamingWorkflowSteps {
		return fmt.Errorf("generator step %q would push the workflow over the %d-step limit (currently %d, adding %d)",
			generatorID, maxStreamingWorkflowSteps, sc.totalSteps, newCount)
	}
	for _, st := range env.SpawnSteps {
		for _, n := range st.Needs {
			if n == st.ID {
				return fmt.Errorf("generator step %q: spawned step %q depends on itself", generatorID, st.ID)
			}
			if _, existsOld := sc.byID[n]; !existsOld && !declaredIDs[n] {
				return fmt.Errorf("generator step %q: spawned step %q needs unknown step %q", generatorID, st.ID, n)
			}
		}
	}
	if cycle := cyclicAmongNewSteps(env.SpawnSteps, declaredIDs); cycle != "" {
		return fmt.Errorf("generator step %q: spawned steps have a dependency cycle involving %q", generatorID, cycle)
	}

	// Structurally valid — now the governance checks (rate + threshold
	// approval) before anything is journaled/merged. Both fail closed:
	// neither silently truncates the batch, they refuse the whole
	// injection so a caller sees a clear reason rather than a partially
	// applied mutation.
	if err := sc.checkGeneratorInjectionRate(newCount); err != nil {
		return fmt.Errorf("generator step %q: %w", generatorID, err)
	}
	if err := sc.requestSwarmSpawnApprovalIfThresholdCrossed(generatorID, newCount); err != nil {
		return fmt.Errorf("generator step %q: %w", generatorID, err)
	}
	// Record against the rate window only now that every check actually
	// passed — a denied/refused injection must not consume rate budget it
	// never used.
	sc.genInjectEvents = append(sc.genInjectEvents, genInjectEvent{at: time.Now(), count: newCount})

	// Journal the graph mutation before the generator result is committed. If
	// the process dies between these writes, replay sees the same definitions
	// and is idempotent; it cannot silently lose already-admitted work.
	before := append([]persistedGeneratedStep(nil), sc.generated...)
	next := append([]persistedGeneratedStep(nil), before...)
	uiSteps := make([]workflowui.Step, 0, len(env.SpawnSteps))
	for _, st := range env.SpawnSteps {
		uiSteps = append(uiSteps, workflowui.Step{ID: st.ID, DefinitionHash: workflowStepDefinitionHash(st)})
		if _, exists := sc.byID[st.ID]; !exists {
			next = append(next, persistedGeneratedStep{Step: st, GeneratorID: generatorID, Depth: depth})
		}
	}
	if err := sc.store.saveGenerated(sc.runID, next); err != nil {
		return fmt.Errorf("persist generated graph: %w", err)
	}
	if sc.d.workflowRuns != nil {
		if _, managedErr := sc.d.workflowRuns.Detail(sc.runID); managedErr == nil {
			if _, err := sc.d.workflowRuns.AddSteps(sc.runID, uiSteps); err != nil {
				if rollbackErr := sc.store.saveGenerated(sc.runID, before); rollbackErr != nil {
					return fmt.Errorf("persist generated UI steps: %v (graph rollback also failed: %v)", err, rollbackErr)
				}
				return fmt.Errorf("persist generated UI steps: %w", err)
			}
		}
	}
	sc.generated = next

	// All validated and persisted — merge into the live graph. Register every new step's
	// identity and dependency edges first, THEN compute in-degree and
	// (maybe) dispatch, so cross-references between siblings in this same
	// batch resolve correctly regardless of slice order.
	for _, st := range env.SpawnSteps {
		if !addedIDs[st.ID] {
			continue // idempotent replay after graph journal commit
		}
		sc.byID[st.ID] = st
		sc.genDepth[st.ID] = depth
		sc.genOrigin[st.ID] = generatorID
		for _, n := range st.Needs {
			sc.dependents[n] = append(sc.dependents[n], st.ID)
		}
	}
	sc.totalSteps += newCount
	sc.remainingToResolve += newCount

	sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", map[string]any{
		"status": "workflow_steps_injected", "workflow": sc.spec.Name, "run_id": sc.runID,
		"generator_step": generatorID, "spawned": newCount, "gen_depth": depth,
		"rationale": truncate(env.Rationale, 300),
	}, "")

	for _, st := range env.SpawnSteps {
		if !addedIDs[st.ID] {
			continue
		}
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

// checkGeneratorInjectionRate enforces maxGeneratorNodesPerWindow within
// generatorInjectionWindow — independent of maxGeneratorDepth/
// maxStreamingWorkflowSteps, this bounds the RATE of dynamic graph growth,
// not just its eventual total: a chain of generators each individually
// within the depth/total caps could otherwise still inject in rapid bursts.
// Trims expired events as a side effect on every call (no separate GC
// needed — the window is only ever read at injection time). Does NOT
// record newCount itself; the caller records only once every check
// (including approval) has actually passed, so a refused injection never
// consumes rate budget it never used.
func (sc *streamCoordinator) checkGeneratorInjectionRate(newCount int) error {
	cutoff := time.Now().Add(-generatorInjectionWindow)
	kept := sc.genInjectEvents[:0]
	sum := 0
	for _, e := range sc.genInjectEvents {
		if e.at.After(cutoff) {
			kept = append(kept, e)
			sum += e.count
		}
	}
	sc.genInjectEvents = kept
	if sum+newCount > maxGeneratorNodesPerWindow {
		return fmt.Errorf("generator injection rate exceeded: %d node(s) already injected across this run in the last %s, refusing %d more (limit %d per window)",
			sum, generatorInjectionWindow, newCount, maxGeneratorNodesPerWindow)
	}
	return nil
}

// requestSwarmSpawnApprovalIfThresholdCrossed re-triggers
// Capability::SwarmSpawn approval (Agent Swarm design §11/§13's originally-
// proposed mitigation) once the run's graph has already grown past
// swarmSpawnApprovalThreshold — small, ordinary graphs never request this
// capability at all; once a run is large enough to matter, every further
// injection needs a governed decision instead of silently continuing to grow.
func (sc *streamCoordinator) requestSwarmSpawnApprovalIfThresholdCrossed(generatorID string, newCount int) error {
	if sc.totalSteps < swarmSpawnApprovalThreshold {
		return nil
	}
	resource := fmt.Sprintf("generator:%s:current_size:%d:requested:%d", generatorID, sc.totalSteps, newCount)
	dec, err := sc.d.kern.Request(sc.parent.SessionID, "SwarmSpawn", resource, sc.parentTask.TaskID)
	if err != nil {
		return fmt.Errorf("swarm spawn governance error: %w", err)
	}
	switch dec.Decision {
	case "denied":
		return fmt.Errorf("DENIED: dynamic graph growth beyond %d steps requires approval, and this request was denied", swarmSpawnApprovalThreshold)
	case "requires_approval":
		if _, ok := sc.d.resolveApprovalOrEscalate(sc.parent, sc.parentTask, dec, "SwarmSpawn", resource,
			fmt.Sprintf("grow workflow graph past %d steps (generator %s, +%d nodes)", swarmSpawnApprovalThreshold, generatorID, newCount)); !ok {
			return fmt.Errorf("requires approval (not granted): %s", dec.Reason)
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
