package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func TestBuiltinToolRegistryContractFixture(t *testing.T) {
	wantNames := []string{
		"list", "read", "search", "git.status", "git.diff", "git.log",
		"browser.open", "browser.snapshot", "browser.action", "browser.tabs", "browser.capture", "browser.close",
		"web.fetch", "web.search", "run", "patch", "add_dir", "edit", "memory",
		"ask_user", "todo", "update_plan", "code.search", "code.symbols", "code.map", "code.def", "code.refs",
		"code.impact", "spawn", "job.list", "job.wait", "job.cancel", "workflow", "mcp", "mcp_find", "done", "best_of_n", "swarm_publish", "swarm_receive",
	}
	gotNames := make([]string, 0, len(defaultBuiltinTools.ordered))
	for _, descriptor := range defaultBuiltinTools.ordered {
		gotNames = append(gotNames, descriptor.Name)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("builtin tool order = %v, want %v", gotNames, wantNames)
	}
	if err := defaultBuiltinTools.Validate(); err != nil {
		t.Fatal(err)
	}
	openDescriptor, ok := defaultBuiltinTools.lookup("browser.open")
	if !ok {
		t.Fatal("browser.open descriptor missing")
	}
	properties := openDescriptor.Schema["properties"].(map[string]any)
	if _, exposed := properties["attach_endpoint"]; exposed {
		t.Fatal("operator attach endpoint leaked into the model schema")
	}
	if got := defaultBuiltinTools.projectionDigest(); got != legacyBuiltinProjectionDigest {
		t.Fatalf("builtin projection digest = %s, update reviewed fixture %s only with an intentional contract change", got, legacyBuiltinProjectionDigest)
	}

	wantPrompt := `Available tools:
- list: workspace file tree
- read: path/range or skill://name (no tool grants)
- search: workspace text
- git.status/diff/log: bounded read-only Git evidence
- browser.open/snapshot/action/tabs/capture/close: governed browser
- web.search / web.fetch: public web after approval
- run: policy-gated argv (missing helper fails closed)
- patch: complete-file transactional write
- add_dir: extra existing directory
- edit: unique exact span already read (never shell)
- memory: governed long-term memory
- ask_user: choice (2-6) or free text
- todo / update_plan: session checklist
- code.search: ranked code search
- code.symbols: definitions + references
- code.map: ranked repo map
- code.def / code.refs: precise definition/references (LSP)
- code.impact: bounded transitive dependents
- spawn + job.list/wait/cancel: background subagents
- workflow: named DAG
- done: finish the task`
	if got := defaultBuiltinTools.promptCatalog(); got != wantPrompt {
		t.Fatalf("prompt catalog drifted:\n%s", got)
	}

	specs := defaultBuiltinTools.nativeToolSpecs()
	if len(specs) != 36 {
		t.Fatalf("native specs = %d, want 36", len(specs))
	}
	for _, spec := range specs {
		if spec.Name == "best_of_n" || strings.HasPrefix(spec.Name, "swarm_") {
			t.Fatalf("conditional JSON-only tool %q leaked into native schemas", spec.Name)
		}
		if err := validateClosedToolSchema(spec.Parameters, spec.Name); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := json.Marshal(specs)
	second, _ := json.Marshal(defaultBuiltinTools.nativeToolSpecs())
	if string(first) != string(second) {
		t.Fatal("native schema projection is not deterministic")
	}
	// Projection callers receive clones, not mutable registry state.
	specs[0].Parameters["additionalProperties"] = true
	fresh := defaultBuiltinTools.nativeToolSpecs()
	if fresh[0].Parameters["additionalProperties"] != false {
		t.Fatal("native schema mutation escaped into the registry")
	}
}

func TestBuiltinToolRegistryValidationFailsClosed(t *testing.T) {
	valid := builtinToolDescriptors()[0]
	tests := []struct {
		name   string
		mutate func(*builtinToolDescriptor)
		want   string
	}{
		{name: "unknown exposure", mutate: func(d *builtinToolDescriptor) { d.Exposure = "model_authored" }, want: "unknown exposure"},
		{name: "unknown effect", mutate: func(d *builtinToolDescriptor) { d.Effect = "ambient" }, want: "unknown effect"},
		{name: "unknown capability", mutate: func(d *builtinToolDescriptor) { d.Capabilities = []string{"RootAccess"} }, want: "unknown capability"},
		{name: "duplicate capability", mutate: func(d *builtinToolDescriptor) { d.Capabilities = []string{"FileRead", "FileRead"} }, want: "repeats capability"},
		{name: "zero timeout", mutate: func(d *builtinToolDescriptor) { d.Timeout = 0 }, want: "zero timeout"},
		{name: "missing handler", mutate: func(d *builtinToolDescriptor) { d.Handler = nil }, want: "no concrete handler"},
		{name: "open root schema", mutate: func(d *builtinToolDescriptor) { d.Schema["additionalProperties"] = true }, want: "open object schema"},
		{name: "open nested schema", mutate: func(d *builtinToolDescriptor) {
			d.Schema = closedObjectSchema(nil, map[string]any{"nested": map[string]any{"type": "object", "properties": map[string]any{}}})
		}, want: "open object schema"},
		{name: "malformed required", mutate: func(d *builtinToolDescriptor) { d.Schema["required"] = []any{"intent", 7} }, want: "only strings"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor := cloneBuiltinToolDescriptor(valid)
			test.mutate(&descriptor)
			_, err := newBuiltinToolRegistry([]builtinToolDescriptor{descriptor})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	duplicate := []builtinToolDescriptor{cloneBuiltinToolDescriptor(valid), cloneBuiltinToolDescriptor(valid)}
	if _, err := newBuiltinToolRegistry(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
	second := cloneBuiltinToolDescriptor(builtinToolDescriptors()[1])
	second.HandlerID = valid.HandlerID
	if _, err := newBuiltinToolRegistry([]builtinToolDescriptor{valid, second}); err == nil || !strings.Contains(err.Error(), "share handler identity") {
		t.Fatalf("handler identity error = %v", err)
	}
}

func TestBuiltinToolRegistryModesExecuteEffectsOnce(t *testing.T) {
	var calls atomic.Int32
	doneDescriptor, ok := defaultBuiltinTools.lookup("done")
	if !ok {
		t.Fatal("done descriptor missing")
	}
	descriptor := cloneBuiltinToolDescriptor(doneDescriptor)
	descriptor.Handler = func(context.Context, *Daemon, *sessionstore.Session, *scheduler.ExecutionRun, *action) toolExecutionOutcome {
		calls.Add(1)
		return toolCompleted("descriptor")
	}
	registry, err := newBuiltinToolRegistry([]builtinToolDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	sess := &sessionstore.Session{SessionID: "session"}
	task := &scheduler.ExecutionRun{RunID: "run"}
	act := &action{Tool: "done", Summary: "legacy"}

	for _, mode := range []builtinToolRegistryMode{builtinToolRegistryLegacy, builtinToolRegistryShadow} {
		d := &Daemon{builtinTools: registry, builtinToolsMode: mode}
		_ = d.dispatchBuiltinActionOutcome(sess, task, act)
		if calls.Load() != 0 {
			t.Fatalf("%s mode executed descriptor effect", mode)
		}
	}
	d := &Daemon{builtinTools: registry, builtinToolsMode: builtinToolRegistryDescriptor}
	if got := d.dispatchBuiltinActionOutcome(sess, task, act); got.display != "descriptor" || calls.Load() != 1 {
		t.Fatalf("descriptor outcome = %+v, handler calls = %d", got, calls.Load())
	}

	// A missing descriptor is exactly what the rollback path is for. Shadow
	// still executes the legacy handler once and never calls an unrelated
	// descriptor handler.
	shadow := &Daemon{builtinTools: registry, builtinToolsMode: builtinToolRegistryShadow}
	todo := &action{Tool: "todo", Todos: []todoItem{{Content: "inspect parity", Status: "in_progress"}}}
	if got := shadow.dispatchBuiltinActionOutcome(sess, task, todo); got.status != "completed" {
		t.Fatalf("shadow legacy fallback = %+v", got)
	}
	if _, ok := shadow.todos.Load(sess.SessionID); !ok || calls.Load() != 1 {
		t.Fatalf("shadow fallback did not execute exactly once; descriptor calls=%d", calls.Load())
	}
}

func TestBuiltinToolRegistryModeNormalization(t *testing.T) {
	for _, value := range []string{"", "descriptor", "shadow", "legacy", " DESCRIPTOR "} {
		if _, err := normalizeBuiltinToolRegistryMode(value); err != nil {
			t.Fatalf("normalize %q: %v", value, err)
		}
	}
	if _, err := normalizeBuiltinToolRegistryMode("dynamic"); err == nil {
		t.Fatal("unknown registry mode must fail closed")
	}
}

func TestBuiltinToolDescriptorTimeoutsAreBounded(t *testing.T) {
	for _, descriptor := range defaultBuiltinTools.ordered {
		if descriptor.Timeout <= 0 || descriptor.Timeout > 30*time.Minute {
			t.Fatalf("%s timeout = %s", descriptor.Name, descriptor.Timeout)
		}
	}
}

func TestConfigInventoryProjectsBuiltinRegistry(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		store: store, planMode: map[string]bool{}, builtinTools: defaultBuiltinTools,
		builtinToolsMode:             builtinToolRegistryDescriptor,
		builtinToolsShadowMismatches: builtinRegistryShadowMismatches(defaultBuiltinTools),
	}
	sess, err := store.CreateSession(t.TempDir(), "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.handleConfigInventory(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	inventory := out.(map[string]any)
	if inventory["builtin_tool_count"] != len(defaultBuiltinTools.ordered) {
		t.Fatalf("builtin tool count = %#v", inventory["builtin_tool_count"])
	}
	rows := inventory["builtin_tools"].([]map[string]any)
	if len(rows) != len(defaultBuiltinTools.ordered) || rows[0]["name"] != "list" || rows[len(rows)-1]["name"] != "swarm_receive" {
		t.Fatalf("builtin inventory order = %#v", rows)
	}
	parity := inventory["builtin_tool_shadow_parity"].(map[string]any)
	if parity["matched"] != true || parity["projection_digest"] != legacyBuiltinProjectionDigest {
		t.Fatalf("shadow parity = %#v", parity)
	}
	effective := inventory["effective"].(map[string]any)
	if effective["builtin_tool_registry_mode"] != builtinToolRegistryDescriptor || effective["builtin_tool_registry_version"] != builtinToolRegistryVersion {
		t.Fatalf("registry effective state = %#v", effective)
	}
}
