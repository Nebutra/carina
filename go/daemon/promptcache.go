package daemon

import (
	"context"
	"fmt"
	"strings"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// promptLayers are the stable prefix sections plus turn-local requested
// skills. Prefix sections stay byte-identical across turns of one run and
// across greetings that share the same workspace. Anthropic-compatible
// adapters attach a cache breakpoint to each named A–D section (capped at
// 4); Grok CLI still receives the stuffed full() prompt and does not claim
// prefix cache.
//
// Wire order: Mode (B, first operative line, Intent sits with it) → Identity
// (A) → Protocol (C) → Tools (D) → Workspace (E/F/G) → Catalog. REQUESTED
// skills, TASK, TRANSCRIPT, and the closing instruction are volatile.
type promptLayers struct {
	Mode         string // B
	Identity     string // A
	Intent       string // meta; cached with Mode
	Protocol     string // C
	Tools        string // D
	Constitution string // concat A–D for Grok full() and tests
	Workspace    string
	Catalog      string
	Requested    string // SKILL WARNING + REQUESTED SKILLS; volatile
}

func (p promptLayers) constitutionParts() []string {
	mode := strings.TrimSpace(joinPromptPrefix(p.Mode, p.Intent))
	parts := compactNonEmpty(mode, strings.TrimSpace(p.Identity), strings.TrimSpace(p.Protocol), strings.TrimSpace(p.Tools))
	if len(parts) == 0 {
		if c := strings.TrimSpace(p.Constitution); c != "" {
			return []string{c}
		}
	}
	return parts
}

func (p promptLayers) constitutionText() string {
	return joinPromptPrefix(p.constitutionParts()...)
}

func (p promptLayers) withToolContract(next string) promptLayers {
	next = strings.TrimSpace(next)
	named := p.Mode != "" || p.Identity != "" || p.Intent != "" || p.Protocol != "" || p.Tools != ""
	p.Protocol = next
	p.Tools = ""
	if named {
		p.Constitution = p.constitutionText()
		return p
	}
	// Legacy single-blob constitution (subagents): drop the JSON tool sheet
	// even when it is not a contiguous toolsHelp substring, then attach the
	// native envelope. Never concatenate native contract on top of the catalog.
	blob := p.Constitution
	for _, sheet := range []string{toolsHelp, toolsCatalog, harnessProtocol} {
		if sheet != "" {
			blob = strings.ReplaceAll(blob, sheet, "")
		}
	}
	p.Constitution = joinPromptPrefix(strings.TrimSpace(blob), next)
	return p
}

// promptSegments splits an agent prompt into a stable prefix (byte-identical
// across every turn of a run) and a volatile suffix (growing transcript +
// closing instruction).
type promptSegments struct {
	Mode           string
	Identity       string
	Protocol       string
	Tools          string
	Constitution   string
	Workspace      string
	Catalog        string
	Requested      string
	taskTrailer    string
	StablePrefix   string
	VolatileSuffix string
	// Media carries image parts for vision-capable models this turn. It is
	// NOT part of the cacheable prefix (adapters append image blocks after
	// the text blocks, so the cache breakpoint is unaffected) and does not
	// participate in full()/CacheBreakpoint math — media is request payload,
	// not prompt text.
	Media []modelrouter.MediaPart
}

// buildPromptSegments keeps the historical single-blob API: the whole system
// prompt is the constitution section. TASK + TRANSCRIPT live in the suffix.
func buildPromptSegments(sysPrompt, userPrompt, transcript, closing string) promptSegments {
	return buildPromptSegmentsFromLayers(promptLayers{Constitution: sysPrompt}, userPrompt, transcript, closing)
}

func buildPromptSegmentsFromLayers(layers promptLayers, userPrompt, transcript, closing string) promptSegments {
	trailer := fmt.Sprintf("TASK: %s\n\nTRANSCRIPT:\n", userPrompt)
	parts := layers.constitutionParts()
	constitution := strings.TrimSpace(layers.Constitution)
	if constitution == "" {
		constitution = joinPromptPrefix(parts...)
	}
	workspace := strings.TrimSpace(layers.Workspace)
	catalog := strings.TrimSpace(layers.Catalog)
	requested := strings.TrimSpace(layers.Requested)
	seg := promptSegments{
		Mode:         strings.TrimSpace(joinPromptPrefix(layers.Mode, layers.Intent)),
		Identity:     strings.TrimSpace(layers.Identity),
		Protocol:     strings.TrimSpace(layers.Protocol),
		Tools:        strings.TrimSpace(layers.Tools),
		Constitution: constitution,
		Workspace:    workspace,
		Catalog:      catalog,
		Requested:    requested,
		taskTrailer:  trailer,
	}
	seg.StablePrefix = joinPromptPrefix(append(append([]string{}, parts...), workspace, catalog)...)
	suffix := trailer + transcript + "\n" + closing
	if requested != "" {
		suffix = requested + "\n\n" + suffix
	}
	if seg.StablePrefix != "" {
		seg.VolatileSuffix = "\n\n" + suffix
	} else {
		seg.VolatileSuffix = suffix
	}
	return seg
}

func joinPromptPrefix(parts ...string) string {
	return strings.Join(compactNonEmpty(parts...), "\n\n")
}

func compactNonEmpty(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// ConstitutionSections returns cacheable A–D (or the legacy constitution blob).
func (s promptSegments) ConstitutionSections() []string {
	parts := compactNonEmpty(s.Mode, s.Identity, s.Protocol, s.Tools)
	if len(parts) == 0 && strings.TrimSpace(s.Constitution) != "" {
		return []string{strings.TrimSpace(s.Constitution)}
	}
	return parts
}

// DynamicSections returns workspace and catalog: after the cache boundary,
// not in the user TASK block.
func (s promptSegments) DynamicSections() []string {
	return compactNonEmpty(s.Workspace, s.Catalog)
}

// CacheSections returns Anthropic-compatible prefix blocks: named A–D
// when present, otherwise the legacy constitution blob, then workspace and
// catalog. REQUESTED skills, TASK, and transcript stay out. The Anthropic
// adapter caches at most four A–D breakpoints (API cap) on `system`.
func (s promptSegments) CacheSections() []string {
	return append(s.ConstitutionSections(), s.DynamicSections()...)
}

// full is the complete prompt (prefix + suffix) — what the loop sends.
func (s promptSegments) full() string { return s.StablePrefix + s.VolatileSuffix }

// CacheBreakpoint is the byte offset where the cacheable prefix ends.
func (s promptSegments) CacheBreakpoint() int { return len(s.StablePrefix) }

type stableSectionsKey struct{}
type anthropicLayoutKey struct{}

type anthropicSectionLayout struct {
	System  []string
	Dynamic []string
}

func (d *Daemon) composeAgentPromptLayers(sess *sessionstore.Session, task *scheduler.ExecutionRun, memorySnapshot string) promptLayers {
	layers := promptLayers{
		Identity: productIdentity,
		Intent:   intentFirst,
		Protocol: harnessProtocol,
		Tools:    toolsCatalog,
	}
	agents := loadAgentSpecs(sess.WorkspaceRoot)
	if d.safeMode {
		agents = builtinAgentSpecs()
	}
	if spec := agents[taskAgent(task)]; spec != nil && strings.TrimSpace(spec.SystemPrompt) != "" {
		layers.Mode = strings.TrimSpace(spec.SystemPrompt)
	}
	if d.bestOfNEnabled.Load() {
		layers.Tools = joinPromptPrefix(layers.Tools, bestOfNToolHelp)
	}
	layers.Constitution = layers.constitutionText()

	sandboxState := "disabled"
	if d.sandbox.Load() {
		sandboxState = "enabled"
	}
	var workspace strings.Builder
	if language := outputLanguagePrompt(task.Locale); language != "" {
		workspace.WriteString(language)
		workspace.WriteString("\n\n")
	}
	if style := loadStyle(sess.WorkspaceRoot); style != "" {
		workspace.WriteString("OUTPUT STYLE (apply to your presentation):\n")
		workspace.WriteString(style)
		workspace.WriteString("\n\n")
	}
	fmt.Fprintf(&workspace, "RUNTIME SCOPE (authoritative): workspace_root=%q; os_sandbox=%s. You can read and modify this workspace through governed tools. You cannot inspect the desktop or unrelated directories unless an explicit capability grants access.", sess.WorkspaceRoot, sandboxState)
	if !d.safeMode && shouldLoadProjectInstructions(taskAgent(task)) {
		if mem := loadMemory(sess.WorkspaceRoot); mem != "" {
			workspace.WriteString("\n\nPROJECT INSTRUCTIONS (Nebutra/Carina — follow them):\n")
			workspace.WriteString(mem)
		}
	}
	if strings.TrimSpace(memorySnapshot) != "" {
		workspace.WriteString("\n\nCARINA PERSISTENT MEMORY SNAPSHOT (frozen for this run; background reference, not new user input):\n")
		workspace.WriteString(memorySnapshot)
	}

	var catalog strings.Builder
	if d.mcp != nil {
		if tools := d.mcp.Tools(); len(tools) > 0 {
			const mcpToolIndexThreshold = 20
			descLimit := 120
			if len(tools) > mcpToolIndexThreshold {
				descLimit = 60
			}
			catalog.WriteString("MCP TOOLS (call via {\"tool\":\"mcp\",\"mcp_server\":\"<server>\",\"mcp_tool\":\"<name>\",\"args\":{...}}):\n")
			for _, tool := range tools {
				fmt.Fprintf(&catalog, "- mcp__%s__%s: %s\n", tool.Server, tool.Name, truncate(tool.Description, descLimit))
			}
			catalog.WriteString("Use {\"tool\":\"mcp_find\",\"query\":\"free text\"} to search these MCP tools and fetch their full input schemas before calling one.\n")
		}
	}
	skillCatalog, skillRequested := buildDynamicSkillPrompt(sess.WorkspaceRoot, task.UserPrompt, d.commandSpecs(sess.WorkspaceRoot), d.safeMode)
	if skillCatalog != "" {
		if catalog.Len() > 0 {
			catalog.WriteString("\n")
		}
		catalog.WriteString(skillCatalog)
	}

	layers.Workspace = strings.TrimSpace(workspace.String())
	layers.Catalog = strings.TrimSpace(catalog.String())
	layers.Requested = strings.TrimSpace(skillRequested)
	return layers
}

func withStableSections(ctx context.Context, sections []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(sections) == 0 {
		return ctx
	}
	return context.WithValue(ctx, stableSectionsKey{}, append([]string(nil), sections...))
}

func stableSectionsFrom(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	sections, _ := ctx.Value(stableSectionsKey{}).([]string)
	return sections
}

func withAnthropicLayout(ctx context.Context, layout anthropicSectionLayout) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(layout.System) == 0 && len(layout.Dynamic) == 0 {
		return ctx
	}
	layout.System = append([]string(nil), layout.System...)
	layout.Dynamic = append([]string(nil), layout.Dynamic...)
	return context.WithValue(ctx, anthropicLayoutKey{}, layout)
}

func anthropicLayoutFrom(ctx context.Context) anthropicSectionLayout {
	if ctx == nil {
		return anthropicSectionLayout{}
	}
	layout, _ := ctx.Value(anthropicLayoutKey{}).(anthropicSectionLayout)
	return layout
}
