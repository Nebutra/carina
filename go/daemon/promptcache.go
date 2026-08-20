package daemon

import (
	"context"
	"fmt"
	"strings"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// promptLayers are the three stable prefix sections. They stay byte-identical
// across turns of one run. Anthropic-compatible adapters may attach a cache
// breakpoint to each non-empty section; Grok CLI still receives the stuffed
// full() prompt and does not claim prefix cache.
//
// Order: constitution (mode + identity + tools) → workspace (scope, optional
// project rules) → catalog (MCP + selected skills). TASK, TRANSCRIPT, and the
// closing instruction are volatile and must not enter the cache prefix.
type promptLayers struct {
	Constitution string
	Workspace    string
	Catalog      string
}

func (p promptLayers) withToolContract(next string) promptLayers {
	if strings.Contains(p.Constitution, toolsHelp) {
		p.Constitution = strings.Replace(p.Constitution, toolsHelp, next, 1)
	}
	return p
}

// promptSegments splits an agent prompt into a stable prefix (byte-identical
// across every turn of a run) and a volatile suffix (growing transcript +
// closing instruction).
type promptSegments struct {
	Constitution   string
	Workspace      string
	Catalog        string
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
	seg := promptSegments{
		Constitution: strings.TrimSpace(layers.Constitution),
		Workspace:    strings.TrimSpace(layers.Workspace),
		Catalog:      strings.TrimSpace(layers.Catalog),
		taskTrailer:  trailer,
	}
	seg.StablePrefix = joinPromptPrefix(seg.Constitution, seg.Workspace, seg.Catalog)
	suffix := trailer + transcript + "\n" + closing
	if seg.StablePrefix != "" {
		seg.VolatileSuffix = "\n\n" + suffix
	} else {
		seg.VolatileSuffix = suffix
	}
	return seg
}

func joinPromptPrefix(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(part)
	}
	return b.String()
}

// CacheSections returns the Anthropic-compatible cached text blocks: one per
// non-empty constitution/workspace/catalog layer. TASK and transcript stay out.
func (s promptSegments) CacheSections() []string {
	var out []string
	for _, part := range []string{s.Constitution, s.Workspace, s.Catalog} {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// full is the complete prompt (prefix + suffix) — what the loop sends.
func (s promptSegments) full() string { return s.StablePrefix + s.VolatileSuffix }

// CacheBreakpoint is the byte offset where the cacheable prefix ends.
func (s promptSegments) CacheBreakpoint() int { return len(s.StablePrefix) }

type stableSectionsKey struct{}

func (d *Daemon) composeAgentPromptLayers(sess *sessionstore.Session, task *scheduler.ExecutionRun, memorySnapshot string) promptLayers {
	constitution := systemPrompt
	agents := loadAgentSpecs(sess.WorkspaceRoot)
	if d.safeMode {
		agents = builtinAgentSpecs()
	}
	if spec := agents[taskAgent(task)]; spec != nil && strings.TrimSpace(spec.SystemPrompt) != "" {
		constitution = strings.TrimSpace(spec.SystemPrompt) + "\n\n" + systemPrompt
	}
	if d.bestOfNEnabled.Load() {
		constitution += "\n\n" + bestOfNToolHelp
	}

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
	if skills := buildDynamicSkillPrompt(sess.WorkspaceRoot, task.UserPrompt, d.commandSpecs(sess.WorkspaceRoot), d.safeMode); skills != "" {
		if catalog.Len() > 0 {
			catalog.WriteString("\n")
		}
		catalog.WriteString(skills)
	}

	return promptLayers{
		Constitution: constitution,
		Workspace:    strings.TrimSpace(workspace.String()),
		Catalog:      strings.TrimSpace(catalog.String()),
	}
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
