#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ToolKind {
    Read,
    List,
    Search,
    Run,
    Patch,
    Diff,
    CodeSearch,
    CodeSymbols,
    CodeMap,
    CodeDefinition,
    CodeReferences,
    CodeImpact,
    McpFind,
    Mcp,
    Delegate,
    Workflow,
    Memory,
    Extension,
    Todo,
}

impl ToolKind {
    pub fn from_name(name: &str) -> Self {
        match name.trim().to_ascii_lowercase().as_str() {
            "read" => Self::Read,
            "list" => Self::List,
            "search" => Self::Search,
            "run" => Self::Run,
            "patch" => Self::Patch,
            "diff" => Self::Diff,
            "code.search" => Self::CodeSearch,
            "code.symbols" => Self::CodeSymbols,
            "code.map" => Self::CodeMap,
            "code.def" => Self::CodeDefinition,
            "code.refs" => Self::CodeReferences,
            "code.impact" => Self::CodeImpact,
            "mcp_find" => Self::McpFind,
            "mcp" => Self::Mcp,
            "spawn" => Self::Delegate,
            "workflow" => Self::Workflow,
            "memory" => Self::Memory,
            "todo" | "todo_write" | "todowrite" | "update_plan" | "plan_update" => Self::Todo,
            _ => Self::Extension,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::Read => "Read",
            Self::List => "List",
            Self::Search => "Search",
            Self::Run => "Run",
            Self::Patch => "Edit",
            Self::Diff => "Changes",
            Self::CodeSearch => "Code search",
            Self::CodeSymbols => "Symbols",
            Self::CodeMap => "Code map",
            Self::CodeDefinition => "Definition",
            Self::CodeReferences => "References",
            Self::CodeImpact => "Impact",
            Self::McpFind => "Find tool",
            Self::Mcp => "MCP",
            Self::Delegate => "Delegate",
            Self::Workflow => "Workflow",
            Self::Memory => "Memory",
            Self::Extension => "Tool",
            Self::Todo => "Plan",
        }
    }

    fn shows_success_detail(self) -> bool {
        !matches!(self, Self::Read | Self::List)
    }

    pub fn is_code_intelligence(self) -> bool {
        matches!(
            self,
            Self::CodeSearch
                | Self::CodeSymbols
                | Self::CodeMap
                | Self::CodeDefinition
                | Self::CodeReferences
                | Self::CodeImpact
        )
    }
}

pub const COLLAPSED_TOOL_GROUP_MEMBER_LIMIT: usize = 5;
pub const COLLAPSED_TOOL_OUTPUT_LINE_LIMIT: usize = 0;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct VisibleCount {
    pub visible: usize,
    pub omitted: usize,
}

pub fn visible_group_members(total: usize, expanded: bool) -> VisibleCount {
    visible_count(total, expanded, COLLAPSED_TOOL_GROUP_MEMBER_LIMIT)
}

pub fn visible_output_lines(total: usize, expanded: bool) -> VisibleCount {
    visible_count(total, expanded, COLLAPSED_TOOL_OUTPUT_LINE_LIMIT)
}

fn visible_count(total: usize, expanded: bool, collapsed_limit: usize) -> VisibleCount {
    let visible = if expanded {
        total
    } else {
        total.min(collapsed_limit)
    };
    VisibleCount {
        visible,
        omitted: total.saturating_sub(visible),
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ToolFacts {
    pub name: String,
    pub intent: String,
    pub path: String,
    pub affected_files: String,
    pub pattern: String,
    pub query: String,
    pub symbol: String,
    pub command: String,
    pub workflow: String,
    pub mcp_tool: String,
    pub agent: String,
    pub target: String,
    pub diff: String,
    pub output: String,
    pub error: String,
    pub status: String,
    pub todo_items: Vec<TodoItem>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct TodoItem {
    pub content: String,
    pub status: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ToolPresentation {
    pub title: String,
    pub target: String,
    pub body: String,
    pub collapsible: bool,
}

pub fn present(facts: &ToolFacts, failed: bool) -> ToolPresentation {
    let kind = ToolKind::from_name(&facts.name);
    let target = match kind {
        ToolKind::Read | ToolKind::Patch => first(&facts.path, &facts.affected_files),
        ToolKind::List => "workspace",
        ToolKind::Search => &facts.pattern,
        ToolKind::CodeSearch | ToolKind::McpFind => &facts.query,
        ToolKind::CodeSymbols
        | ToolKind::CodeDefinition
        | ToolKind::CodeReferences
        | ToolKind::CodeImpact => &facts.symbol,
        ToolKind::Run => &facts.command,
        ToolKind::Workflow => &facts.workflow,
        ToolKind::Mcp => &facts.mcp_tool,
        ToolKind::Delegate => &facts.agent,
        ToolKind::Memory => &facts.target,
        ToolKind::Diff => &facts.affected_files,
        ToolKind::CodeMap | ToolKind::Extension | ToolKind::Todo => "",
    };
    let title_detail = first(&facts.intent, target);
    let title = if title_detail.is_empty() {
        kind.label().to_owned()
    } else {
        format!("{}  {title_detail}", kind.label())
    };
    let body = match kind {
        ToolKind::Patch | ToolKind::Diff => edit_body(facts, failed),
        ToolKind::Todo if failed => first(&facts.error, &facts.output).to_owned(),
        ToolKind::Todo => todo_body(&facts.todo_items),
        _ if failed => first(&facts.error, &facts.output).to_owned(),
        _ if kind.shows_success_detail() => facts.output.clone(),
        _ => String::new(),
    };
    ToolPresentation {
        title,
        target: target.to_owned(),
        // Progress and a lone error should read in place. Folding is useful only
        // once an edit has reviewable code, or a non-edit tool has detail.
        collapsible: if failed {
            !body.is_empty()
        } else if kind == ToolKind::Todo {
            false
        } else if matches!(kind, ToolKind::Patch | ToolKind::Diff) {
            !facts.diff.is_empty()
        } else {
            !body.is_empty() && kind.shows_success_detail()
        },
        body,
    }
}

fn todo_body(items: &[TodoItem]) -> String {
    let glyphs = crate::glyphs::Glyphs::detect();
    items
        .iter()
        .filter(|item| !item.content.trim().is_empty())
        .map(|item| {
            let marker = match item.status.as_str() {
                "completed" | "done" => glyphs.success(),
                "in_progress" | "running" | "active" => glyphs.ready(),
                "pending" | "queued" | "todo" | "" => glyphs.empty(),
                _ => glyphs.bullet(),
            };
            format!("{marker} {}", item.content.trim())
        })
        .collect::<Vec<_>>()
        .join("\n")
}

fn edit_body(facts: &ToolFacts, failed: bool) -> String {
    if !facts.diff.is_empty() {
        if failed && !facts.error.is_empty() {
            return format!("{}\n\nError: {}", facts.diff, facts.error);
        }
        return facts.diff.clone();
    }
    if failed {
        return first(&facts.error, &facts.output).to_owned();
    }
    if !facts.output.is_empty() {
        return facts.output.clone();
    }
    match facts.status.as_str() {
        "requested" | "pending" => "Preparing a reviewable diff...",
        "awaiting_approval" | "permission_requested" => "Waiting for edit approval...",
        "started" | "running" => "Applying changes...",
        _ => "",
    }
    .to_owned()
}

fn first<'a>(primary: &'a str, fallback: &'a str) -> &'a str {
    if primary.is_empty() {
        fallback
    } else {
        primary
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn compact_reads_do_not_gain_empty_dropdowns() {
        let presentation = present(
            &ToolFacts {
                name: "read".into(),
                path: "src/lib.rs".into(),
                output: "42 bytes".into(),
                ..ToolFacts::default()
            },
            false,
        );
        assert_eq!(presentation.title, "Read  src/lib.rs");
        assert!(presentation.body.is_empty());
        assert!(!presentation.collapsible);
    }

    #[test]
    fn tool_titles_prefer_intent_and_retain_target_separately() {
        let presentation = present(
            &ToolFacts {
                name: "read".into(),
                intent: "Inspect the projection contract".into(),
                path: "src/lib.rs".into(),
                ..ToolFacts::default()
            },
            false,
        );
        assert_eq!(presentation.title, "Read  Inspect the projection contract");
        assert_eq!(presentation.target, "src/lib.rs");

        let fallback = present(
            &ToolFacts {
                name: "read".into(),
                path: "src/lib.rs".into(),
                ..ToolFacts::default()
            },
            false,
        );
        assert_eq!(fallback.title, "Read  src/lib.rs");
    }

    #[test]
    fn extension_and_mcp_outputs_remain_inspectable() {
        for name in ["mcp", "custom.remote_tool"] {
            let presentation = present(
                &ToolFacts {
                    name: name.into(),
                    mcp_tool: "docs.search".into(),
                    output: "Found three references".into(),
                    ..ToolFacts::default()
                },
                false,
            );
            assert_eq!(presentation.body, "Found three references");
            assert!(presentation.collapsible);
        }
    }

    #[test]
    fn edit_lifecycle_has_visible_progress_and_keeps_attempted_diff_on_failure() {
        let preparing = present(
            &ToolFacts {
                name: "patch".into(),
                path: "src/lib.rs".into(),
                status: "pending".into(),
                ..ToolFacts::default()
            },
            false,
        );
        assert_eq!(preparing.body, "Preparing a reviewable diff...");
        assert!(!preparing.collapsible);

        let failed = present(
            &ToolFacts {
                name: "patch".into(),
                path: "src/lib.rs".into(),
                diff: "--- a/src/lib.rs\n+++ b/src/lib.rs\n-old\n+new".into(),
                error: "write conflict".into(),
                status: "failed".into(),
                ..ToolFacts::default()
            },
            true,
        );
        assert!(failed.body.contains("-old\n+new"));
        assert!(failed.body.contains("Error: write conflict"));
        assert!(failed.collapsible);
    }

    #[test]
    fn todo_updates_render_as_one_inline_checklist_without_status_words() {
        let presentation = present(
            &ToolFacts {
                name: "TodoWrite".into(),
                todo_items: vec![
                    TodoItem {
                        content: "Inspect the renderer".into(),
                        status: "completed".into(),
                    },
                    TodoItem {
                        content: "Fix transcript ownership".into(),
                        status: "in_progress".into(),
                    },
                    TodoItem {
                        content: "Run PTY coverage".into(),
                        status: "pending".into(),
                    },
                ],
                ..ToolFacts::default()
            },
            false,
        );
        assert_eq!(presentation.title, "Plan");
        let glyphs = crate::glyphs::Glyphs::detect();
        assert_eq!(
            presentation.body,
            format!(
                "{} Inspect the renderer\n{} Fix transcript ownership\n{} Run PTY coverage",
                glyphs.success(),
                glyphs.ready(),
                glyphs.empty()
            )
        );
        assert!(!presentation.body.contains("completed"));
        assert!(!presentation.collapsible);
    }

    #[test]
    fn collapsed_visibility_limits_are_central_and_count_only_omissions() {
        assert_eq!(
            visible_group_members(COLLAPSED_TOOL_GROUP_MEMBER_LIMIT, false),
            VisibleCount {
                visible: COLLAPSED_TOOL_GROUP_MEMBER_LIMIT,
                omitted: 0,
            }
        );
        assert_eq!(
            visible_group_members(COLLAPSED_TOOL_GROUP_MEMBER_LIMIT + 3, false),
            VisibleCount {
                visible: COLLAPSED_TOOL_GROUP_MEMBER_LIMIT,
                omitted: 3,
            }
        );
        assert_eq!(
            visible_output_lines(COLLAPSED_TOOL_OUTPUT_LINE_LIMIT + 7, false),
            VisibleCount {
                visible: COLLAPSED_TOOL_OUTPUT_LINE_LIMIT,
                omitted: 7,
            }
        );
        assert_eq!(
            visible_group_members(COLLAPSED_TOOL_GROUP_MEMBER_LIMIT + 3, true),
            VisibleCount {
                visible: COLLAPSED_TOOL_GROUP_MEMBER_LIMIT + 3,
                omitted: 0,
            }
        );
    }
}
