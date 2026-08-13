#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ToolKind {
    /// Presentation-only aggregate for adjacent, read-only discovery tools.
    Explore,
    Read,
    List,
    Search,
    WebFetch,
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
            "web.fetch" => Self::WebFetch,
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
            Self::Explore => "Explore",
            Self::Read => "Read",
            Self::List => "List",
            Self::Search => "Search",
            Self::WebFetch => "Fetch",
            Self::Run => "Run",
            Self::Patch => "Edit",
            Self::Diff => "Changes",
            Self::CodeSearch => "Code",
            Self::CodeSymbols => "Symbol",
            Self::CodeMap => "Map",
            Self::CodeDefinition => "Def",
            Self::CodeReferences => "Refs",
            Self::CodeImpact => "Impact",
            Self::McpFind => "Find",
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

    pub fn is_exploration(self) -> bool {
        matches!(
            self,
            Self::Explore
                | Self::Read
                | Self::List
                | Self::Search
                | Self::CodeSearch
                | Self::CodeSymbols
                | Self::CodeMap
                | Self::CodeDefinition
                | Self::CodeReferences
                | Self::CodeImpact
        )
    }
}

/// Collapsed multi-tool groups show **title only** (dialogue-first density).
/// Member paths live in the group title preview; expand restores full member rows.
/// Failures / diff-bearing tools force expand via the transcript reducer.
pub const COLLAPSED_TOOL_GROUP_MEMBER_LIMIT: usize = 0;
pub const COLLAPSED_TOOL_OUTPUT_LINE_LIMIT: usize = 0;
/// How many member paths appear in a collapsed group title preview.
pub const TOOL_GROUP_PATH_PREVIEW_LIMIT: usize = 2;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct VisibleCount {
    pub visible: usize,
    pub omitted: usize,
}

/// How many group member rows to paint.
///
/// Collapsed groups intentionally hide every member row (`visible = 0`,
/// `omitted = 0`). The count and path preview live on the title line; the
/// disclosure glyph is the expand escape hatch (no separate "N omitted" row).
pub fn visible_group_members(total: usize, expanded: bool) -> VisibleCount {
    visible_group_members_with_limits(
        total,
        expanded,
        COLLAPSED_TOOL_GROUP_MEMBER_LIMIT,
        usize::MAX,
    )
}

pub fn visible_group_members_with_limits(
    total: usize,
    expanded: bool,
    collapsed_limit: usize,
    expanded_limit: usize,
) -> VisibleCount {
    if expanded {
        let visible = total.min(expanded_limit);
        VisibleCount {
            visible,
            omitted: total.saturating_sub(visible),
        }
    } else {
        VisibleCount {
            visible: total.min(collapsed_limit),
            omitted: 0,
        }
    }
}

pub fn visible_output_lines(total: usize, expanded: bool) -> VisibleCount {
    visible_output_lines_with_limits(
        total,
        expanded,
        COLLAPSED_TOOL_OUTPUT_LINE_LIMIT,
        usize::MAX,
    )
}

pub fn visible_output_lines_with_limits(
    total: usize,
    expanded: bool,
    collapsed_limit: usize,
    expanded_limit: usize,
) -> VisibleCount {
    let visible = if expanded {
        total.min(expanded_limit)
    } else {
        total.min(collapsed_limit)
    };
    VisibleCount {
        visible,
        omitted: total.saturating_sub(visible),
    }
}

/// Collect non-empty member titles for a collapsed group path preview.
///
/// Truncation / ellipsis / separator copy is applied by
/// [`crate::i18n::tool_group_title`] so product glyphs stay on the i18n boundary.
pub fn tool_group_path_titles<'a>(titles: impl IntoIterator<Item = &'a str>) -> Vec<&'a str> {
    let mut unique = Vec::new();
    for title in titles.into_iter().map(str::trim) {
        if !title.is_empty() && !unique.contains(&title) {
            unique.push(title);
        }
    }
    unique
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
    /// Full title line: fixed-width label + primary detail (intent, else target).
    pub title: String,
    /// Kind label before padding (e.g. `Read`, `Edit`).
    pub label: String,
    /// Primary scannable text: intent when present, otherwise technical target.
    pub primary: String,
    /// True when `primary` came from lifecycle intent (not path/command fallback).
    pub intent_primary: bool,
    /// Technical target kept for expand/member rows and dim secondary text.
    pub target: String,
    pub body: String,
    pub todo_items: Vec<TodoItem>,
    pub collapsible: bool,
}

/// Compose the intent-first tool title grammar.
///
/// Layout: `[label ≤ TOOL_LABEL_WIDTH cells][gap 2][primary]`.
/// Glyph/disclosure is applied by the renderer, not here.
pub fn format_tool_title(label: &str, primary: &str) -> String {
    let label = pad_tool_label(label);
    if primary.is_empty() {
        label.trim_end().to_owned()
    } else {
        format!("{label}  {primary}")
    }
}

fn pad_tool_label(label: &str) -> String {
    use unicode_width::UnicodeWidthStr;
    let width = crate::layout_contract::TRANSCRIPT_TOOL_LABEL_WIDTH;
    let full_w = UnicodeWidthStr::width(label);
    if full_w <= width {
        return format!("{label}{}", " ".repeat(width - full_w));
    }
    // Truncate to width-1 and append a single-cell ellipsis.
    let mut out = String::new();
    let mut used = 0usize;
    let budget = width.saturating_sub(1);
    for ch in label.chars() {
        let ch_w = UnicodeWidthStr::width(ch.encode_utf8(&mut [0; 4]));
        if used + ch_w > budget {
            break;
        }
        out.push(ch);
        used += ch_w;
    }
    out.push('…');
    used = UnicodeWidthStr::width(out.as_str());
    if used < width {
        out.push_str(&" ".repeat(width - used));
    }
    out
}

pub fn present(facts: &ToolFacts, failed: bool) -> ToolPresentation {
    let kind = ToolKind::from_name(&facts.name);
    let target = match kind {
        ToolKind::Explore => "",
        ToolKind::Read | ToolKind::Patch => first(&facts.path, &facts.affected_files),
        ToolKind::List => "workspace",
        ToolKind::Search => &facts.pattern,
        ToolKind::CodeSearch | ToolKind::McpFind => &facts.query,
        ToolKind::WebFetch => &facts.target,
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
    let intent = facts.intent.trim();
    let intent_primary = !intent.is_empty();
    let primary = if intent_primary { intent } else { target };
    let label = kind.label().to_owned();
    let title = format_tool_title(&label, primary);
    let body = match kind {
        ToolKind::Patch | ToolKind::Diff => edit_body(facts, failed),
        ToolKind::Todo if failed => first(&facts.error, &facts.output).to_owned(),
        ToolKind::Todo => String::new(),
        _ if failed => first(&facts.error, &facts.output).to_owned(),
        _ if kind.shows_success_detail() => facts.output.clone(),
        _ => String::new(),
    };
    ToolPresentation {
        title,
        label,
        primary: primary.to_owned(),
        intent_primary,
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
        todo_items: facts.todo_items.clone(),
    }
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
        assert_eq!(presentation.title, format_tool_title("Read", "src/lib.rs"));
        assert!(!presentation.intent_primary);
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
        assert_eq!(
            presentation.title,
            format_tool_title("Read", "Inspect the projection contract")
        );
        assert!(presentation.intent_primary);
        assert_eq!(presentation.primary, "Inspect the projection contract");
        assert_eq!(presentation.target, "src/lib.rs");
        // Intent-first title must not bury the why under the path.
        assert!(!presentation.title.contains("src/lib.rs"));

        let fallback = present(
            &ToolFacts {
                name: "read".into(),
                path: "src/lib.rs".into(),
                ..ToolFacts::default()
            },
            false,
        );
        assert_eq!(fallback.title, format_tool_title("Read", "src/lib.rs"));
        assert!(!fallback.intent_primary);
    }

    #[test]
    fn tool_label_field_is_fixed_width_for_scan_columns() {
        use unicode_width::UnicodeWidthStr;
        let width = crate::layout_contract::TRANSCRIPT_TOOL_LABEL_WIDTH;
        for label in ["Read", "Edit", "Run", "Search", "Code search", "Definition"] {
            let padded = pad_tool_label(label);
            assert_eq!(
                UnicodeWidthStr::width(padded.as_str()),
                width,
                "label {label:?} => {padded:?}"
            );
        }
        let long = format_tool_title("Definition", "Kernel");
        assert!(long.starts_with(&pad_tool_label("Definition")));
        assert!(long.contains("Kernel"));
    }

    #[test]
    fn cjk_intent_stays_primary_without_losing_target() {
        let presentation = present(
            &ToolFacts {
                name: "read".into(),
                intent: "检查投影契约".into(),
                path: "src/中文.rs".into(),
                ..ToolFacts::default()
            },
            false,
        );
        assert!(presentation.intent_primary);
        assert_eq!(presentation.primary, "检查投影契约");
        assert_eq!(presentation.target, "src/中文.rs");
        assert!(presentation.title.contains("检查投影契约"));
        assert!(!presentation.title.contains("src/中文.rs"));
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
    fn web_fetch_is_explicit_and_keeps_the_approved_host_visible() {
        let presentation = present(
            &ToolFacts {
                name: "web.fetch".into(),
                intent: "查询北京实时天气".into(),
                target: "wttr.in".into(),
                output: "current_condition".into(),
                ..ToolFacts::default()
            },
            false,
        );
        assert_eq!(ToolKind::from_name("web.fetch"), ToolKind::WebFetch);
        assert_eq!(presentation.label, "Fetch");
        assert_eq!(presentation.primary, "查询北京实时天气");
        assert_eq!(presentation.target, "wttr.in");
        assert!(presentation.collapsible);
        assert!(!ToolKind::WebFetch.is_exploration());
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
        assert!(presentation.body.is_empty());
        assert_eq!(presentation.todo_items.len(), 3);
        assert_eq!(presentation.todo_items[0].status, "completed");
        assert_eq!(presentation.todo_items[1].status, "in_progress");
        assert_eq!(presentation.todo_items[2].status, "pending");
        assert!(!presentation.body.contains("completed"));
        assert!(!presentation.collapsible);
    }

    #[test]
    fn collapsed_visibility_limits_are_central_and_count_only_omissions() {
        // Dialogue-first: collapsed groups paint zero member rows (title absorbs).
        assert_eq!(
            visible_group_members(5, false),
            VisibleCount {
                visible: 0,
                omitted: 0,
            }
        );
        assert_eq!(
            visible_group_members(7, false),
            VisibleCount {
                visible: 0,
                omitted: 0,
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
            visible_group_members(7, true),
            VisibleCount {
                visible: 7,
                omitted: 0,
            }
        );
    }

    #[test]
    fn density_limits_keep_collapsed_groups_quiet_and_expanded_output_bounded() {
        assert_eq!(
            visible_group_members_with_limits(5, false, 2, 4),
            VisibleCount {
                visible: 2,
                omitted: 0,
            }
        );
        assert_eq!(
            visible_group_members_with_limits(5, true, 2, 4),
            VisibleCount {
                visible: 4,
                omitted: 1,
            }
        );
        assert_eq!(
            visible_output_lines_with_limits(9, false, 3, 8),
            VisibleCount {
                visible: 3,
                omitted: 6,
            }
        );
        assert_eq!(
            visible_output_lines_with_limits(9, true, 3, 8),
            VisibleCount {
                visible: 8,
                omitted: 1,
            }
        );
    }

    #[test]
    fn tool_group_path_titles_skips_empty() {
        assert!(tool_group_path_titles([]).is_empty());
        assert_eq!(
            tool_group_path_titles(["a.rs", "", "  ", "b.rs", "a.rs"]),
            vec!["a.rs", "b.rs"]
        );
    }
}
