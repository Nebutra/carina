package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const builtinToolRegistryVersion = "2"

// This is a reviewed contract fixture, not a second registry. Shadow mode
// compares the complete descriptor projection against it while executing the
// one-release legacy dispatcher exactly once.
const legacyBuiltinProjectionDigest = "43b9703cb2434ab0843bdfb263debbb1d497f0afc7f889fa61b14775a43272b6"

type builtinToolExposure string

const (
	builtinToolExposureDefault     builtinToolExposure = "default"
	builtinToolExposureDeferred    builtinToolExposure = "deferred"
	builtinToolExposureConditional builtinToolExposure = "conditional"
)

type builtinToolEffect string

const (
	builtinToolEffectRead        builtinToolEffect = "read"
	builtinToolEffectNetwork     builtinToolEffect = "network"
	builtinToolEffectWrite       builtinToolEffect = "write"
	builtinToolEffectCommand     builtinToolEffect = "command"
	builtinToolEffectDelegation  builtinToolEffect = "delegation"
	builtinToolEffectMCP         builtinToolEffect = "mcp"
	builtinToolEffectInteraction builtinToolEffect = "interaction"
	builtinToolEffectPlan        builtinToolEffect = "plan"
	builtinToolEffectCompletion  builtinToolEffect = "completion"
)

type builtinToolPlanMode string

const (
	builtinToolPlanAllowed builtinToolPlanMode = "allowed"
	builtinToolPlanBlocked builtinToolPlanMode = "blocked"
)

type builtinToolParallelClass string

const (
	builtinToolParallelSerial    builtinToolParallelClass = "serial"
	builtinToolParallelReadBatch builtinToolParallelClass = "read_batch"
)

type builtinToolRegistryMode string

const (
	builtinToolRegistryLegacy     builtinToolRegistryMode = "legacy"
	builtinToolRegistryShadow     builtinToolRegistryMode = "shadow"
	builtinToolRegistryDescriptor builtinToolRegistryMode = "descriptor"
)

type builtinToolHandler func(context.Context, *Daemon, *sessionstore.Session, *scheduler.ExecutionRun, *action) toolExecutionOutcome

type builtinToolDescriptor struct {
	Version                string
	Name                   string
	Description            string
	PromptLabel            string
	PromptSummary          string
	ConditionalPrompt      string
	Schema                 map[string]any
	Exposure               builtinToolExposure
	Effect                 builtinToolEffect
	Capabilities           []string
	Timeout                time.Duration
	PlanMode               builtinToolPlanMode
	Parallel               builtinToolParallelClass
	HandlerID              string
	Handler                builtinToolHandler
	HandlerStartsLifecycle bool
	ExternalMCP            bool
	ExternalMCPDescription string
	Explore                bool
}

type builtinToolRegistry struct {
	version string
	ordered []builtinToolDescriptor
	byName  map[string]int
}

var defaultBuiltinTools *builtinToolRegistry

func init() {
	defaultBuiltinTools = mustBuiltinToolRegistry()
	toolsCatalog = defaultBuiltinTools.promptCatalog()
	toolsHelp = toolsCatalog + "\n\n" + harnessProtocol
	exploreToolNames = defaultBuiltinTools.exploreNames()
	exploreRestrictedTools = defaultBuiltinTools.exploreRestrictedTools()
	carinaToolCatalog = externalMCPToolCatalog(defaultBuiltinTools)
}

func mustBuiltinToolRegistry() *builtinToolRegistry {
	registry, err := newBuiltinToolRegistry(builtinToolDescriptors())
	if err != nil {
		panic("invalid builtin tool registry: " + err.Error())
	}
	return registry
}

func newBuiltinToolRegistry(descriptors []builtinToolDescriptor) (*builtinToolRegistry, error) {
	registry := &builtinToolRegistry{
		version: builtinToolRegistryVersion,
		ordered: cloneBuiltinToolDescriptors(descriptors),
		byName:  make(map[string]int, len(descriptors)),
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	for i := range registry.ordered {
		registry.byName[registry.ordered[i].Name] = i
	}
	return registry, nil
}

func (r *builtinToolRegistry) Validate() error {
	if r == nil {
		return fmt.Errorf("registry is nil")
	}
	if strings.TrimSpace(r.version) == "" {
		return fmt.Errorf("registry version is empty")
	}
	if len(r.ordered) == 0 {
		return fmt.Errorf("registry has no descriptors")
	}
	seen := make(map[string]struct{}, len(r.ordered))
	seenHandlers := make(map[string]string, len(r.ordered))
	for i := range r.ordered {
		descriptor := r.ordered[i]
		prefix := fmt.Sprintf("descriptor[%d]", i)
		if descriptor.Version != r.version {
			return fmt.Errorf("%s %q has version %q, want %q", prefix, descriptor.Name, descriptor.Version, r.version)
		}
		if descriptor.Name == "" || strings.TrimSpace(descriptor.Name) != descriptor.Name {
			return fmt.Errorf("%s has invalid name %q", prefix, descriptor.Name)
		}
		if _, ok := seen[descriptor.Name]; ok {
			return fmt.Errorf("duplicate builtin tool %q", descriptor.Name)
		}
		seen[descriptor.Name] = struct{}{}
		if strings.TrimSpace(descriptor.Description) == "" {
			return fmt.Errorf("builtin tool %q has no description", descriptor.Name)
		}
		if !validBuiltinToolExposure(descriptor.Exposure) {
			return fmt.Errorf("builtin tool %q has unknown exposure %q", descriptor.Name, descriptor.Exposure)
		}
		if !validBuiltinToolEffect(descriptor.Effect) {
			return fmt.Errorf("builtin tool %q has unknown effect %q", descriptor.Name, descriptor.Effect)
		}
		if descriptor.PlanMode != builtinToolPlanAllowed && descriptor.PlanMode != builtinToolPlanBlocked {
			return fmt.Errorf("builtin tool %q has unknown plan-mode behavior %q", descriptor.Name, descriptor.PlanMode)
		}
		if descriptor.Parallel != builtinToolParallelSerial && descriptor.Parallel != builtinToolParallelReadBatch {
			return fmt.Errorf("builtin tool %q has unknown parallel class %q", descriptor.Name, descriptor.Parallel)
		}
		if descriptor.Timeout <= 0 {
			return fmt.Errorf("builtin tool %q has zero timeout", descriptor.Name)
		}
		if descriptor.Handler == nil || strings.TrimSpace(descriptor.HandlerID) == "" {
			return fmt.Errorf("builtin tool %q has no concrete handler", descriptor.Name)
		}
		if owner, ok := seenHandlers[descriptor.HandlerID]; ok {
			return fmt.Errorf("builtin tools %q and %q share handler identity %q", owner, descriptor.Name, descriptor.HandlerID)
		}
		seenHandlers[descriptor.HandlerID] = descriptor.Name
		seenCapabilities := make(map[string]struct{}, len(descriptor.Capabilities))
		for _, capability := range descriptor.Capabilities {
			if !validBuiltinToolCapability(capability) {
				return fmt.Errorf("builtin tool %q has unknown capability %q", descriptor.Name, capability)
			}
			if _, ok := seenCapabilities[capability]; ok {
				return fmt.Errorf("builtin tool %q repeats capability %q", descriptor.Name, capability)
			}
			seenCapabilities[capability] = struct{}{}
		}
		if err := validateClosedToolSchema(descriptor.Schema, descriptor.Name); err != nil {
			return err
		}
	}
	return nil
}

func validBuiltinToolExposure(exposure builtinToolExposure) bool {
	switch exposure {
	case builtinToolExposureDefault, builtinToolExposureDeferred, builtinToolExposureConditional:
		return true
	default:
		return false
	}
}

func validBuiltinToolEffect(effect builtinToolEffect) bool {
	switch effect {
	case builtinToolEffectRead, builtinToolEffectNetwork, builtinToolEffectWrite,
		builtinToolEffectCommand, builtinToolEffectDelegation, builtinToolEffectMCP,
		builtinToolEffectInteraction, builtinToolEffectPlan, builtinToolEffectCompletion:
		return true
	default:
		return false
	}

}

func validBuiltinToolCapability(capability string) bool {
	switch capability {
	case "FileRead", "NetworkAccess", "CommandExec", "PatchApply", "MemoryWrite",
		"SubagentSpawn", "PluginLoad", "SwarmMessage", "BrowserInteract", "BrowserAttach":
		return true
	default:
		return false
	}
}

func validateClosedToolSchema(schema map[string]any, tool string) error {
	if schema == nil {
		return fmt.Errorf("builtin tool %q has no schema", tool)
	}
	if schema["type"] != "object" {
		return fmt.Errorf("builtin tool %q schema root must be an object", tool)
	}
	return validateClosedSchemaNode(schema, tool+".schema")
}

func validateClosedSchemaNode(node map[string]any, path string) error {
	typeName, _ := node["type"].(string)
	if typeName == "object" {
		closed, ok := node["additionalProperties"].(bool)
		if !ok || closed {
			return fmt.Errorf("%s is an open object schema", path)
		}
		properties, ok := node["properties"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s has no properties map", path)
		}
		for name, raw := range properties {
			child, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s is not a schema", path, name)
			}
			if err := validateClosedSchemaNode(child, path+".properties."+name); err != nil {
				return err
			}
		}
		if raw, ok := node["required"]; ok {
			required, err := requiredSchemaNames(raw)
			if err != nil {
				return fmt.Errorf("%s.required: %w", path, err)
			}
			seen := make(map[string]struct{}, len(required))
			for _, name := range required {
				if _, ok := properties[name]; !ok {
					return fmt.Errorf("%s requires unknown property %q", path, name)
				}
				if _, ok := seen[name]; ok {
					return fmt.Errorf("%s repeats required property %q", path, name)
				}
				seen[name] = struct{}{}
			}
		}
	}
	if typeName == "array" {
		items, ok := node["items"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s has no item schema", path)
		}
		return validateClosedSchemaNode(items, path+".items")
	}
	return nil
}

func requiredSchemaNames(raw any) ([]string, error) {
	values := schemaStringSlice(raw)
	if values == nil {
		return nil, fmt.Errorf("must be an array of strings")
	}
	switch typed := raw.(type) {
	case []string:
		return values, nil
	case []any:
		if len(values) != len(typed) {
			return nil, fmt.Errorf("must contain only strings")
		}
		return values, nil
	default:
		return nil, fmt.Errorf("must be an array of strings")
	}
}

func schemaStringSlice(raw any) []string {
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeBuiltinToolRegistryMode(value string) (builtinToolRegistryMode, error) {
	switch mode := builtinToolRegistryMode(strings.ToLower(strings.TrimSpace(value))); mode {
	case "", builtinToolRegistryDescriptor:
		return builtinToolRegistryDescriptor, nil
	case builtinToolRegistryLegacy, builtinToolRegistryShadow:
		return mode, nil
	default:
		return "", fmt.Errorf("builtin tool registry mode must be legacy, shadow, or descriptor, got %q", value)
	}
}

func (d *Daemon) builtinToolRegistry() *builtinToolRegistry {
	if d != nil && d.builtinTools != nil {
		return d.builtinTools
	}
	return defaultBuiltinTools
}

func (d *Daemon) builtinRegistryMode() builtinToolRegistryMode {
	if d == nil || d.builtinToolsMode == "" {
		return builtinToolRegistryDescriptor
	}
	return d.builtinToolsMode
}

func (d *Daemon) builtinNativeToolSpecs() []modelrouter.ToolSpec {
	return d.builtinToolRegistry().nativeToolSpecs()
}

func (d *Daemon) builtinConditionalPrompt(name string) string {
	return d.builtinToolRegistry().conditionalPrompt(name)
}

func builtinRegistryShadowMismatches(registry *builtinToolRegistry) []string {
	if registry == nil {
		return []string{"registry is nil"}
	}
	if got := registry.projectionDigest(); got != legacyBuiltinProjectionDigest {
		return []string{fmt.Sprintf("projection digest %s does not match reviewed fixture %s", got, legacyBuiltinProjectionDigest)}
	}
	return nil
}

func (d *Daemon) dispatchBuiltinActionOutcome(sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	mode := d.builtinRegistryMode()
	descriptor, ok := d.builtinToolRegistry().lookup(act.Tool)
	if !ok {
		if mode == builtinToolRegistryLegacy || mode == builtinToolRegistryShadow {
			if err := d.ensureToolCallStarted(act.lifecycleCallID); err != nil {
				return toolFailed("governance error: "+err.Error(), "audit_persistence_error")
			}
			if mode == builtinToolRegistryShadow {
				d.recordBuiltinRegistryShadowMismatch(sess, task, act.Tool, []string{"descriptor lookup missing; legacy handler selected"})
			}
			return d.legacyDispatchActionOutcome(sess, task, act)
		}
		return toolFailed("unknown tool: "+act.Tool, "unknown_tool")
	}
	if descriptor.HandlerStartsLifecycle {
		if err := d.ensureToolCallStarted(act.lifecycleCallID); err != nil {
			return toolFailed("governance error: "+err.Error(), "audit_persistence_error")
		}
	}

	switch mode {
	case builtinToolRegistryLegacy:
		return d.legacyDispatchActionOutcome(sess, task, act)
	case builtinToolRegistryShadow:
		mismatches := append([]string(nil), d.builtinToolsShadowMismatches...)
		legacyHandlerID := "builtin." + act.Tool
		if descriptor.HandlerID != legacyHandlerID {
			mismatches = append(mismatches, fmt.Sprintf("handler identity %q does not match legacy %q", descriptor.HandlerID, legacyHandlerID))
		}
		d.recordBuiltinRegistryShadowMismatch(sess, task, act.Tool, mismatches)
		// Shadow never calls the descriptor handler: effects execute once.
		return d.legacyDispatchActionOutcome(sess, task, act)
	default:
		parent := d.contextForTask(task.RunID)
		ctx, cancel := context.WithTimeout(parent, descriptor.Timeout)
		defer cancel()
		return descriptor.Handler(ctx, d, sess, task, act)
	}
}

func (d *Daemon) recordBuiltinRegistryShadowMismatch(sess *sessionstore.Session, task *scheduler.ExecutionRun, tool string, mismatches []string) {
	if len(mismatches) == 0 || d == nil || d.kern == nil || sess == nil || task == nil {
		return
	}
	d.record(sess.SessionID, "ExecutionProgressed", task.RunID, "go", map[string]any{
		"status": "builtin_registry_shadow_mismatch", "tool": tool,
		"registry_version": d.builtinToolRegistry().version,
		"mismatches":       append([]string(nil), mismatches...),
	}, "")
}

func (r *builtinToolRegistry) lookup(name string) (builtinToolDescriptor, bool) {
	if r == nil {
		return builtinToolDescriptor{}, false
	}
	index, ok := r.byName[name]
	if !ok {
		return builtinToolDescriptor{}, false
	}
	// Descriptors and their schemas are package-private and immutable after
	// construction. Projections clone schemas at the API boundary; dispatch
	// avoids a JSON round-trip on every tool call.
	return r.ordered[index], true
}

func (r *builtinToolRegistry) promptCatalog() string {
	var builder strings.Builder
	builder.WriteString("Available tools:")
	for _, descriptor := range r.ordered {
		if descriptor.Exposure != builtinToolExposureDefault || descriptor.PromptSummary == "" {
			continue
		}
		builder.WriteString("\n- ")
		label := descriptor.PromptLabel
		if label == "" {
			label = descriptor.Name
		}
		builder.WriteString(label)
		builder.WriteString(": ")
		builder.WriteString(descriptor.PromptSummary)
	}
	return builder.String()
}

func (r *builtinToolRegistry) conditionalPrompt(name string) string {
	descriptor, ok := r.lookup(name)
	if !ok || descriptor.Exposure != builtinToolExposureConditional {
		return ""
	}
	return descriptor.ConditionalPrompt
}

func (r *builtinToolRegistry) nativeToolSpecs() []modelrouter.ToolSpec {
	if r == nil {
		return nil
	}
	specs := make([]modelrouter.ToolSpec, 0, len(r.ordered))
	for _, descriptor := range r.ordered {
		if descriptor.Exposure == builtinToolExposureConditional {
			continue
		}
		specs = append(specs, modelrouter.ToolSpec{
			Name:        descriptor.Name,
			Description: descriptor.Description,
			Parameters:  cloneStringAnyMap(descriptor.Schema),
		})
	}
	return specs
}

func (r *builtinToolRegistry) exploreNames() []string {
	var names []string
	for _, descriptor := range r.ordered {
		if descriptor.Explore {
			names = append(names, descriptor.Name)
		}
	}
	return names
}

func (r *builtinToolRegistry) exploreRestrictedTools() map[string]bool {
	restricted := make(map[string]bool)
	if r == nil {
		return restricted
	}
	for _, descriptor := range r.ordered {
		if !descriptor.Explore && descriptor.Name != "done" {
			restricted[descriptor.Name] = true
		}
	}
	return restricted
}

func (r *builtinToolRegistry) inventory() []map[string]any {
	rows := make([]map[string]any, 0, len(r.ordered))
	for _, descriptor := range r.ordered {
		rows = append(rows, map[string]any{
			"version":      descriptor.Version,
			"name":         descriptor.Name,
			"description":  descriptor.Description,
			"exposure":     descriptor.Exposure,
			"effect":       descriptor.Effect,
			"capabilities": append([]string(nil), descriptor.Capabilities...),
			"timeout_ms":   descriptor.Timeout.Milliseconds(),
			"plan_mode":    descriptor.PlanMode,
			"parallel":     descriptor.Parallel,
		})
	}
	return rows
}

func (r *builtinToolRegistry) projectionDigest() string {
	type contractRow struct {
		Name                   string                   `json:"name"`
		Description            string                   `json:"description"`
		Exposure               builtinToolExposure      `json:"exposure"`
		Effect                 builtinToolEffect        `json:"effect"`
		Capabilities           []string                 `json:"capabilities"`
		TimeoutMS              int64                    `json:"timeout_ms"`
		PlanMode               builtinToolPlanMode      `json:"plan_mode"`
		Parallel               builtinToolParallelClass `json:"parallel"`
		HandlerID              string                   `json:"handler_id"`
		HandlerStartsLifecycle bool                     `json:"handler_starts_lifecycle"`
		Schema                 map[string]any           `json:"schema"`
		ConditionalPrompt      string                   `json:"conditional_prompt,omitempty"`
		ExternalMCP            bool                     `json:"external_mcp"`
		ExternalMCPDescription string                   `json:"external_mcp_description,omitempty"`
		Explore                bool                     `json:"explore"`
	}
	rows := make([]contractRow, 0, len(r.ordered))
	for _, descriptor := range r.ordered {
		rows = append(rows, contractRow{
			Name: descriptor.Name, Description: descriptor.Description, Exposure: descriptor.Exposure, Effect: descriptor.Effect,
			Capabilities: append([]string(nil), descriptor.Capabilities...), TimeoutMS: descriptor.Timeout.Milliseconds(),
			PlanMode: descriptor.PlanMode, Parallel: descriptor.Parallel, HandlerID: descriptor.HandlerID,
			HandlerStartsLifecycle: descriptor.HandlerStartsLifecycle,
			Schema:                 cloneStringAnyMap(descriptor.Schema), ConditionalPrompt: descriptor.ConditionalPrompt,
			ExternalMCP: descriptor.ExternalMCP, ExternalMCPDescription: descriptor.ExternalMCPDescription, Explore: descriptor.Explore,
		})
	}
	payload := struct {
		Version string        `json:"version"`
		Prompt  string        `json:"prompt"`
		Rows    []contractRow `json:"rows"`
	}{Version: r.version, Prompt: r.promptCatalog(), Rows: rows}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func cloneBuiltinToolDescriptors(in []builtinToolDescriptor) []builtinToolDescriptor {
	out := make([]builtinToolDescriptor, len(in))
	for i := range in {
		out[i] = cloneBuiltinToolDescriptor(in[i])
	}
	return out
}

func cloneBuiltinToolDescriptor(descriptor builtinToolDescriptor) builtinToolDescriptor {
	descriptor.Schema = cloneStringAnyMap(descriptor.Schema)
	descriptor.Capabilities = append([]string(nil), descriptor.Capabilities...)
	return descriptor
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func closedObjectSchema(required []string, properties map[string]any) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = append([]string(nil), required...)
	}
	return schema
}

func toolStringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func toolPositiveIntegerProperty(description string, maximum int) map[string]any {
	property := map[string]any{"type": "integer", "minimum": 1, "description": description}
	if maximum > 0 {
		property["maximum"] = maximum
	}
	return property
}

func toolArrayProperty(description string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": description}
}

func builtinToolDescriptors() []builtinToolDescriptor {
	intent := func() map[string]any { return toolStringProperty("brief user-visible purpose") }
	todoItemSchema := func(contentKey string) map[string]any {
		return closedObjectSchema(nil, map[string]any{
			contentKey: toolStringProperty("checklist item"),
			"status":   toolStringProperty("pending, in_progress, or completed"),
		})
	}
	optionSchema := closedObjectSchema([]string{"label", "value"}, map[string]any{
		"label":       toolStringProperty("operator-visible option label"),
		"value":       toolStringProperty("stable option value"),
		"description": toolStringProperty("optional consequence or tradeoff"),
	})
	spawnTaskSchema := closedObjectSchema([]string{"agent", "task"}, map[string]any{
		"agent": toolStringProperty("agent name"),
		"task":  toolStringProperty("task for the child"),
	})
	spawnTasksSchema := toolArrayProperty("parallel child tasks", spawnTaskSchema)
	spawnTasksSchema["maxItems"] = maxBackgroundSpawnJobs
	memoryOperationSchema := closedObjectSchema([]string{"action"}, map[string]any{
		"action":   toolStringProperty("add, replace, or remove"),
		"content":  toolStringProperty("fact"),
		"old_text": toolStringProperty("unique substring"),
	})
	gitPathItems := map[string]any{"type": "string", "maxLength": maxGitPathBytes}
	gitPathsSchema := toolArrayProperty("optional literal workspace-relative path filters", gitPathItems)
	gitPathsSchema["maxItems"] = maxGitPathFilters
	browserOriginSchema := toolArrayProperty("optional public HTTPS origins to approve", map[string]any{"type": "string", "maxLength": 2048})
	browserOriginSchema["maxItems"] = maxBrowserOrigins
	browserActionSchema := closedObjectSchema([]string{"kind"}, map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{
			"click", "type", "select", "check", "key", "scroll", "hover", "upload", "dialog_accept", "dialog_dismiss",
		}},
		"ref":     map[string]any{"type": "string", "maxLength": 128, "description": "opaque ref from the latest snapshot"},
		"text":    map[string]any{"type": "string", "maxLength": 1 << 20, "description": "text for a type action"},
		"values":  map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string", "maxLength": 64 << 10}},
		"checked": map[string]any{"type": "boolean"},
		"key": map[string]any{"type": "string", "enum": []string{
			"Enter", "Escape", "Tab", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "PageUp", "PageDown", "Home", "End", "Backspace", "Delete",
		}},
		"delta_x": map[string]any{"type": "integer", "minimum": -10000, "maximum": 10000},
		"delta_y": map[string]any{"type": "integer", "minimum": -10000, "maximum": 10000},
		"files": map[string]any{
			"type": "array", "maxItems": maxBrowserUploadFiles,
			"items":       map[string]any{"type": "string", "maxLength": 4096},
			"description": "explicit workspace-relative or granted absolute upload paths",
		},
		"declared_effect": map[string]any{"type": "string", "enum": []string{
			"observe", "reversible", "external_submit", "sensitive_transmission", "authenticated_representation",
			"upload", "executable_download", "permission", "access_change", "destructive",
		}},
	})

	readBatch := builtinToolParallelReadBatch
	serial := builtinToolParallelSerial
	allowPlan := builtinToolPlanAllowed
	blockPlan := builtinToolPlanBlocked
	startAtWrapper := true
	return []builtinToolDescriptor{
		builtinDescriptor("list", "list the workspace file tree", "workspace file tree", closedObjectSchema([]string{"intent"}, map[string]any{"intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 30*time.Second, allowPlan, readBatch, builtinListHandler, startAtWrapper, true, "List the workspace file tree.", true),
		builtinDescriptor("read", "read a workspace file or bounded line range", "path/range or skill://name (no tool grants)", closedObjectSchema([]string{"path", "intent"}, map[string]any{"path": toolStringProperty("workspace-relative path"), "start_line": toolPositiveIntegerProperty("optional one-based first line", 0), "line_count": toolPositiveIntegerProperty("optional number of complete lines (requires start_line)", maxRangedReadLines), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 30*time.Second, allowPlan, readBatch, builtinReadHandler, startAtWrapper, true, "Read a file or bounded line range (capability-gated).", true),
		builtinDescriptor("search", "search the workspace", "workspace text", closedObjectSchema([]string{"pattern", "intent"}, map[string]any{"pattern": toolStringProperty("search text"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 30*time.Second, allowPlan, readBatch, builtinSearchHandler, startAtWrapper, true, "Search the workspace for a pattern.", true),
		withBuiltinPromptLabel(builtinDescriptor("git.status", "inspect bounded Git branch and worktree status", "bounded read-only Git evidence", closedObjectSchema([]string{"intent"}, map[string]any{"limit": toolPositiveIntegerProperty("maximum status rows", maxGitStatusLimit), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, defaultGitToolTimeout, allowPlan, serial, builtinGitStatusHandler, startAtWrapper, false, "", false), "git.status/diff/log"),
		builtinDescriptor("git.diff", "inspect a bounded typed Git diff", "", closedObjectSchema([]string{"view", "intent"}, map[string]any{"view": map[string]any{"type": "string", "enum": []string{"worktree", "staged", "head"}, "description": "typed comparison view"}, "paths": gitPathsSchema, "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, defaultGitToolTimeout, allowPlan, serial, builtinGitDiffHandler, startAtWrapper, false, "", false),
		builtinDescriptor("git.log", "inspect bounded structured Git history", "", closedObjectSchema([]string{"intent"}, map[string]any{"revision": map[string]any{"type": "string", "enum": []string{"head"}, "description": "typed revision selector"}, "max_commits": toolPositiveIntegerProperty("maximum commits", maxGitLogCommits), "paths": gitPathsSchema, "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, defaultGitToolTimeout, allowPlan, serial, builtinGitLogHandler, startAtWrapper, false, "", false),
		withBuiltinPromptLabel(builtinDescriptor("browser.open", "open an isolated browser or navigate an existing tab", "governed browser", closedObjectSchema([]string{"intent"}, map[string]any{
			"browser_id":       toolStringProperty("existing browser id when navigating"),
			"tab_id":           toolStringProperty("existing tab id when navigating"),
			"mode":             map[string]any{"type": "string", "enum": []string{"managed", "attach"}, "description": "mode for a new browser"},
			"url":              map[string]any{"type": "string", "maxLength": 8192, "description": "optional public HTTPS navigation target"},
			"approved_origins": browserOriginSchema,
			"intent":           intent(),
		}), builtinToolExposureDefault, builtinToolEffectInteraction, []string{"BrowserInteract", "BrowserAttach", "NetworkAccess"}, 2*time.Minute, blockPlan, serial, builtinBrowserOpenHandler, false, false, "", false), "browser.open/snapshot/action/tabs/capture/close"),
		builtinDescriptor("browser.snapshot", "capture a bounded accessibility snapshot with opaque refs", "", closedObjectSchema([]string{"browser_id", "tab_id", "intent"}, map[string]any{
			"browser_id": toolStringProperty("browser id"), "tab_id": toolStringProperty("tab id"), "intent": intent(),
		}), builtinToolExposureDefault, builtinToolEffectRead, []string{"BrowserInteract"}, 30*time.Second, allowPlan, serial, builtinBrowserSnapshotHandler, false, false, "", false),
		builtinDescriptor("browser.action", "perform one closed typed browser action against a fresh opaque ref", "", closedObjectSchema([]string{"browser_id", "tab_id", "action", "intent"}, map[string]any{
			"browser_id": toolStringProperty("browser id"), "tab_id": toolStringProperty("tab id"), "action": browserActionSchema, "intent": intent(),
		}), builtinToolExposureDefault, builtinToolEffectInteraction, []string{"BrowserInteract", "FileRead"}, 10*time.Minute, blockPlan, serial, builtinBrowserActionHandler, false, false, "", false),
		builtinDescriptor("browser.tabs", "list, open, activate, or close browser tabs", "", closedObjectSchema([]string{"browser_id", "operation", "intent"}, map[string]any{
			"browser_id": toolStringProperty("browser id"),
			"operation":  map[string]any{"type": "string", "enum": []string{"list", "open", "activate", "close"}},
			"tab_id":     toolStringProperty("tab id for activate or close"),
			"url":        map[string]any{"type": "string", "maxLength": 8192, "description": "public HTTPS URL for open"},
			"intent":     intent(),
		}), builtinToolExposureDefault, builtinToolEffectInteraction, []string{"BrowserInteract", "NetworkAccess"}, 2*time.Minute, blockPlan, serial, builtinBrowserTabsHandler, false, false, "", false),
		builtinDescriptor("browser.capture", "capture a bounded browser screenshot as an artifact", "", closedObjectSchema([]string{"browser_id", "tab_id", "intent"}, map[string]any{
			"browser_id": toolStringProperty("browser id"), "tab_id": toolStringProperty("tab id"),
			"full_page": map[string]any{"type": "boolean", "description": "capture the bounded full page instead of the viewport"}, "intent": intent(),
		}), builtinToolExposureDefault, builtinToolEffectRead, []string{"BrowserInteract"}, 2*time.Minute, allowPlan, serial, builtinBrowserCaptureHandler, false, false, "", false),
		builtinDescriptor("browser.close", "close the session browser and remove its isolated profile", "", closedObjectSchema([]string{"browser_id", "intent"}, map[string]any{
			"browser_id": toolStringProperty("browser id"), "intent": intent(),
		}), builtinToolExposureDefault, builtinToolEffectInteraction, []string{"BrowserInteract"}, 30*time.Second, allowPlan, serial, builtinBrowserCloseHandler, false, false, "", false),
		builtinDescriptor("web.fetch", "fetch public text or JSON over HTTPS after host approval", "", closedObjectSchema([]string{"url", "intent"}, map[string]any{"url": toolStringProperty("https URL"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectNetwork, []string{"NetworkAccess"}, 30*time.Second, blockPlan, serial, builtinWebFetchHandler, false, false, "", false),
		withBuiltinPromptLabel(builtinDescriptor("web.search", "search the public web after host approval", "public web after approval", closedObjectSchema([]string{"query", "intent"}, map[string]any{"query": toolStringProperty("search query"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectNetwork, []string{"NetworkAccess"}, 30*time.Second, blockPlan, serial, builtinWebSearchHandler, false, false, "", false), "web.search / web.fetch"),
		builtinDescriptor("run", "run a workspace-scoped, policy-gated command", "policy-gated argv (missing helper fails closed)", closedObjectSchema([]string{"command", "intent"}, map[string]any{"command": toolArrayProperty("argv", map[string]any{"type": "string"}), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectCommand, []string{"CommandExec"}, 5*time.Minute, blockPlan, serial, builtinRunHandler, false, true, "Run a command (OS-sandboxed + policy-gated; risky commands are denied).", false),
		builtinDescriptor("patch", "propose and apply a complete-file transactional write", "complete-file transactional write", closedObjectSchema([]string{"path", "content", "intent"}, map[string]any{"path": toolStringProperty("workspace-relative or granted-root path"), "content": toolStringProperty("complete new file content"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectWrite, []string{"PatchApply"}, 10*time.Minute, blockPlan, serial, builtinPatchHandler, false, true, "Propose+apply a full-file edit (transactional, rollbackable, capability-gated).", false),
		builtinDescriptor("add_dir", "grant an extra existing directory the operator named", "extra existing directory", closedObjectSchema([]string{"path", "intent"}, map[string]any{"path": toolStringProperty("absolute existing directory"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectWrite, nil, 10*time.Minute, blockPlan, serial, builtinAddDirHandler, startAtWrapper, false, "", false),
		builtinDescriptor("edit", "replace one unique exact span in a previously read file", "unique exact span already read (never shell)", closedObjectSchema([]string{"path", "old", "new", "intent"}, map[string]any{"path": toolStringProperty("workspace-relative path"), "old": toolStringProperty("exact unique span to replace"), "new": toolStringProperty("replacement text"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectWrite, []string{"PatchApply"}, 10*time.Minute, blockPlan, serial, builtinEditHandler, false, true, "Replace one unique span in a file (transactional, capability-gated).", false),
		builtinDescriptor("memory", "update governed long-term memory", "governed long-term memory", closedObjectSchema([]string{"intent"}, map[string]any{"target": toolStringProperty("memory or user"), "action": toolStringProperty("add, replace, remove, or batch"), "content": toolStringProperty("fact"), "old_text": toolStringProperty("unique substring"), "operations": toolArrayProperty("batch operations", memoryOperationSchema), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectWrite, []string{"MemoryWrite"}, 10*time.Minute, blockPlan, serial, builtinMemoryHandler, false, false, "", false),
		builtinDescriptor("ask_user", "pause for a structured operator choice or free-text reply", "choice (2-6) or free text", closedObjectSchema([]string{"prompt", "intent"}, map[string]any{"prompt": toolStringProperty("question for the operator"), "options": toolArrayProperty("2-6 structured options; omit for free text", optionSchema), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectInteraction, nil, 10*time.Minute, allowPlan, serial, builtinAskUserHandler, startAtWrapper, false, "", false),
		builtinDescriptor("todo", "replace the session checklist", "", closedObjectSchema([]string{"intent"}, map[string]any{"todos": toolArrayProperty("checklist", todoItemSchema("content")), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectPlan, nil, 30*time.Second, allowPlan, serial, builtinTodoHandler, startAtWrapper, false, "", false),
		withBuiltinPromptLabel(builtinDescriptor("update_plan", "replace the session checklist (alias of todo)", "session checklist", closedObjectSchema([]string{"intent"}, map[string]any{"plan": toolArrayProperty("checklist", todoItemSchema("step")), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectPlan, nil, 30*time.Second, allowPlan, serial, builtinTodoHandler, startAtWrapper, false, "", false), "todo / update_plan"),
		builtinDescriptor("code.search", "ranked code search", "ranked code search", closedObjectSchema([]string{"query", "intent"}, map[string]any{"query": toolStringProperty("free text or identifier"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 2*time.Minute, allowPlan, serial, builtinCodeSearchHandler, startAtWrapper, false, "", true),
		builtinDescriptor("code.symbols", "definitions and references", "definitions + references", closedObjectSchema([]string{"name", "intent"}, map[string]any{"name": toolStringProperty("symbol name"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 2*time.Minute, allowPlan, serial, builtinCodeSymbolsHandler, startAtWrapper, false, "", true),
		builtinDescriptor("code.map", "compact ranked repository map", "ranked repo map", closedObjectSchema([]string{"intent"}, map[string]any{"intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 2*time.Minute, allowPlan, serial, builtinCodeMapHandler, startAtWrapper, false, "", true),
		builtinDescriptor("code.def", "precise definition", "", closedObjectSchema([]string{"name", "intent"}, map[string]any{"name": toolStringProperty("symbol name"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 2*time.Minute, allowPlan, serial, builtinCodeDefHandler, startAtWrapper, false, "", true),
		withBuiltinPromptLabel(builtinDescriptor("code.refs", "precise references", "precise definition/references (LSP)", closedObjectSchema([]string{"name", "intent"}, map[string]any{"name": toolStringProperty("symbol name"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 2*time.Minute, allowPlan, serial, builtinCodeRefsHandler, startAtWrapper, false, "", true), "code.def / code.refs"),
		builtinDescriptor("code.impact", "bounded transitive dependents of a symbol", "bounded transitive dependents", closedObjectSchema([]string{"name", "intent"}, map[string]any{"name": toolStringProperty("symbol name"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, []string{"FileRead"}, 2*time.Minute, allowPlan, serial, builtinCodeImpactHandler, startAtWrapper, false, "", true),
		withBuiltinPromptLabel(builtinDescriptor("spawn", "delegate work to a subagent", "background subagents", closedObjectSchema([]string{"intent"}, map[string]any{"agent": toolStringProperty("agent name"), "task": toolStringProperty("task for the child"), "tasks": spawnTasksSchema, "background": map[string]any{"type": "boolean", "description": "return durable job handles without waiting"}, "intent": intent()}), builtinToolExposureDefault, builtinToolEffectDelegation, []string{"SubagentSpawn"}, 30*time.Minute, allowPlan, serial, builtinSpawnHandler, false, false, "", false), "spawn + job.list/wait/cancel"),
		builtinDescriptor("job.list", "list owned background jobs", "", closedObjectSchema([]string{"intent"}, map[string]any{"statuses": map[string]any{"type": "array", "maxItems": len(validJobStatuses), "items": map[string]any{"type": "string", "enum": append([]string(nil), validJobStatuses...)}, "description": "optional status filters"}, "cursor": map[string]any{"type": "string", "maxLength": maxJobCursorBytes, "description": "opaque pagination cursor"}, "limit": toolPositiveIntegerProperty("page size up to 50", maxJobListLimit), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, nil, 30*time.Second, allowPlan, serial, builtinJobListHandler, startAtWrapper, false, "", false),
		builtinDescriptor("job.wait", "wait for owned background jobs without polling", "", closedObjectSchema([]string{"job_ids", "mode", "intent"}, map[string]any{"job_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": maxJobWaitIDs, "items": map[string]any{"type": "string", "maxLength": maxJobIDBytes}, "description": "owned background job handles"}, "mode": map[string]any{"type": "string", "enum": []string{"any", "all"}, "description": "return after any or all jobs settle"}, "timeout_ms": toolPositiveIntegerProperty("maximum wait in milliseconds", int(maxJobWait/time.Millisecond)), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectRead, nil, maxJobWait+time.Second, allowPlan, serial, builtinJobWaitHandler, startAtWrapper, false, "", false),
		builtinDescriptor("job.cancel", "cancel an owned background job", "", closedObjectSchema([]string{"job_id", "intent"}, map[string]any{"job_id": map[string]any{"type": "string", "maxLength": maxJobIDBytes, "description": "owned background job handle"}, "intent": intent()}), builtinToolExposureDefault, builtinToolEffectDelegation, nil, 30*time.Second, allowPlan, serial, builtinJobCancelHandler, startAtWrapper, false, "", false),
		builtinDescriptor("workflow", "run a named workflow DAG", "named DAG", closedObjectSchema([]string{"workflow", "intent"}, map[string]any{"workflow": toolStringProperty("workflow name"), "task": toolStringProperty("optional input"), "intent": intent()}), builtinToolExposureDefault, builtinToolEffectDelegation, []string{"PluginLoad"}, 30*time.Minute, blockPlan, serial, builtinWorkflowHandler, false, false, "", false),
		builtinDescriptor("mcp", "call a connected MCP tool", "", closedObjectSchema([]string{"mcp_server", "mcp_tool", "intent"}, map[string]any{"mcp_server": toolStringProperty("server id"), "mcp_tool": toolStringProperty("tool name"), "args": map[string]any{"description": "arguments returned by mcp_find"}, "intent": intent()}), builtinToolExposureDeferred, builtinToolEffectMCP, []string{"PluginLoad"}, 10*time.Minute, blockPlan, serial, builtinMCPHandler, false, false, "", false),
		builtinDescriptor("mcp_find", "search connected MCP tools", "", closedObjectSchema([]string{"query", "intent"}, map[string]any{"query": toolStringProperty("free text"), "intent": intent()}), builtinToolExposureDeferred, builtinToolEffectRead, nil, 30*time.Second, allowPlan, serial, builtinMCPFindHandler, startAtWrapper, false, "", false),
		builtinDescriptor("done", "finish the task with the operator-visible summary", "finish the task", closedObjectSchema([]string{"summary"}, map[string]any{"summary": toolStringProperty("plain-language final answer"), "result_kind": toolStringProperty("answer or plan when required by the active agent")}), builtinToolExposureDefault, builtinToolEffectCompletion, nil, 30*time.Second, allowPlan, serial, builtinDoneHandler, startAtWrapper, false, "", false),
		conditionalBuiltinDescriptor("best_of_n", "generate and judge multiple candidate patches", closedObjectSchema([]string{"task", "n", "intent"}, map[string]any{"task": toolStringProperty("description of the change"), "n": map[string]any{"type": "integer", "description": "candidate count from 2 to 5"}, "command": toolArrayProperty("optional verification argv", map[string]any{"type": "string"}), "intent": intent()}), builtinToolEffectDelegation, []string{"PluginLoad", "CommandExec"}, 30*time.Minute, blockPlan, serial, builtinBestOfNHandler, false, bestOfNToolHelp),
		conditionalBuiltinDescriptor("swarm_publish", "publish a live workflow channel message", closedObjectSchema([]string{"channel", "payload", "intent"}, map[string]any{"channel": toolStringProperty("workflow channel"), "payload": map[string]any{"description": "JSON payload"}, "intent": intent()}), builtinToolEffectDelegation, []string{"SwarmMessage"}, 30*time.Second, blockPlan, serial, builtinSwarmPublishHandler, startAtWrapper, ""),
		conditionalBuiltinDescriptor("swarm_receive", "receive live workflow channel messages", closedObjectSchema([]string{"intent"}, map[string]any{"channel": toolStringProperty("optional workflow channel"), "intent": intent()}), builtinToolEffectDelegation, nil, 30*time.Second, blockPlan, serial, builtinSwarmReceiveHandler, startAtWrapper, ""),
	}
}

func builtinDescriptor(name, description, promptSummary string, schema map[string]any, exposure builtinToolExposure, effect builtinToolEffect, capabilities []string, timeout time.Duration, plan builtinToolPlanMode, parallel builtinToolParallelClass, handler builtinToolHandler, wrapperStartsLifecycle, externalMCP bool, externalDescription string, explore bool) builtinToolDescriptor {
	return builtinToolDescriptor{
		Version: builtinToolRegistryVersion, Name: name, Description: description,
		PromptSummary: promptSummary, Schema: schema, Exposure: exposure, Effect: effect,
		Capabilities: capabilities, Timeout: timeout, PlanMode: plan, Parallel: parallel,
		HandlerID: "builtin." + name, Handler: handler, HandlerStartsLifecycle: wrapperStartsLifecycle,
		ExternalMCP: externalMCP, ExternalMCPDescription: externalDescription, Explore: explore,
	}
}

func withBuiltinPromptLabel(descriptor builtinToolDescriptor, label string) builtinToolDescriptor {
	descriptor.PromptLabel = label
	return descriptor
}

func conditionalBuiltinDescriptor(name, description string, schema map[string]any, effect builtinToolEffect, capabilities []string, timeout time.Duration, plan builtinToolPlanMode, parallel builtinToolParallelClass, handler builtinToolHandler, wrapperStartsLifecycle bool, prompt string) builtinToolDescriptor {
	descriptor := builtinDescriptor(name, description, "", schema, builtinToolExposureConditional, effect, capabilities, timeout, plan, parallel, handler, wrapperStartsLifecycle, false, "", false)
	descriptor.ConditionalPrompt = prompt
	return descriptor
}

func builtinListHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.listWorkspaceOutcome(sess, task, act.authorizedRead)
}

func builtinReadHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.readWorkspaceOutcome(sess, task, act)
}

func builtinSearchHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.searchWorkspaceOutcome(sess, task, act.Pattern, act.authorizedRead)
}

func builtinGitStatusHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.gitStatusOutcome(ctx, sess, task, act)
}

func builtinGitDiffHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.gitDiffOutcome(ctx, sess, task, act)
}

func builtinGitLogHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.gitLogOutcome(ctx, sess, task, act)
}

func builtinBrowserOpenHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.browserOpenOutcome(ctx, sess, task, act)
}

func builtinBrowserSnapshotHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.browserSnapshotOutcome(ctx, sess, task, act)
}

func builtinBrowserActionHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.browserActionOutcome(ctx, sess, task, act)
}

func builtinBrowserTabsHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.browserTabsOutcome(ctx, sess, task, act)
}

func builtinBrowserCaptureHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.browserCaptureOutcome(ctx, sess, task, act)
}

func builtinBrowserCloseHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.browserCloseOutcome(ctx, sess, task, act)
}

func builtinWebFetchHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.agentWebFetchOutcome(sess, task, act.URL)
}

func builtinWebSearchHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.agentWebSearchOutcome(sess, task, act.Query)
}

func builtinRunHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.agentRunOutcome(sess, task, act.Command)
}

func builtinPatchHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.agentPatchOutcome(sess, task, act.Path, act.Content)
}

func builtinAddDirHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.agentAddDirOutcome(sess, task, act.Path)
}

func builtinEditHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.agentEditOutcome(sess, task, act.Path, act.Old, act.New)
}

func builtinMemoryHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.agentMemoryOutcome(sess, task, act)
}

func builtinAskUserHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.askUserOutcome(sess, task, act.Prompt, act.Options)
}

func builtinTodoHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.executeTodoOutcome(sess, task, act)
}

func builtinCodeSearchHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return classifyLegacyToolResult(d.agentCodeSearch(sess, task, act))
}

func builtinCodeSymbolsHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return classifyLegacyToolResult(d.agentCodeSymbols(sess, task, act))
}

func builtinCodeMapHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return classifyLegacyToolResult(d.agentCodeMap(sess, task, act))
}

func builtinCodeDefHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return classifyLegacyToolResult(d.agentCodeDef(sess, task, act))
}

func builtinCodeRefsHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return classifyLegacyToolResult(d.agentCodeRefs(sess, task, act))
}

func builtinCodeImpactHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return classifyLegacyToolResult(d.agentCodeImpact(sess, task, act))
}

func builtinSpawnHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.executeSpawnOutcome(sess, task, act)
}

func builtinWorkflowHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.executeWorkflowOutcome(sess, task, act)
}

func builtinMCPHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.callMCPOutcome(sess, task, act)
}

func builtinMCPFindHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.mcpFindOutcome(sess, task, act)
}

func builtinDoneHandler(_ context.Context, _ *Daemon, _ *sessionstore.Session, _ *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return toolCompleted(act.Summary)
}

func builtinBestOfNHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.executeBestOfNOutcome(sess, task, act)
}

func builtinSwarmPublishHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.swarmPublishOutcome(sess, task, act)
}

func builtinSwarmReceiveHandler(_ context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	return d.swarmReceiveOutcome(sess, task, act)
}
