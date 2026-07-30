use std::collections::HashMap;

use crate::transcript::TranscriptBlock;
use xai_ratatui_inline::split_into_line_segments;

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum ScrollbackWrap {
    #[default]
    PreWrap,
    Terminal,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct BlockStamp {
    id: String,
    revision: u64,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum TailState {
    Held,
    AwaitingPresentation(u64),
    Finalized(u64),
}

#[derive(Debug, Default)]
pub struct ScrollbackLedger {
    committed: Vec<BlockStamp>,
    tail: HashMap<String, TailState>,
}

#[derive(Debug, Default)]
pub struct TranscriptReflowState {
    last_observed_width: Option<u16>,
    last_reflow_width: Option<u16>,
    ran_during_stream: bool,
    resize_requested_during_stream: bool,
    force_after_stream: bool,
}

impl TranscriptReflowState {
    pub fn observe(&mut self, width: u16, streaming: bool) {
        self.last_observed_width = Some(width.max(1));
        if streaming {
            self.resize_requested_during_stream = true;
        }
    }

    pub fn needs_reflow(&self) -> bool {
        self.force_after_stream || self.last_observed_width != self.last_reflow_width
    }

    pub fn mark_reflowed(&mut self, streaming: bool) {
        self.last_reflow_width = self.last_observed_width;
        self.ran_during_stream = streaming;
        self.force_after_stream = false;
    }

    pub fn stream_finished(&mut self) -> bool {
        if !self.ran_during_stream && !self.resize_requested_during_stream {
            return false;
        }
        self.ran_during_stream = false;
        self.resize_requested_during_stream = false;
        self.force_after_stream = true;
        true
    }

    pub fn last_observed_width(&self) -> Option<u16> {
        self.last_observed_width
    }

    pub fn last_reflow_width(&self) -> Option<u16> {
        self.last_reflow_width
    }
}

pub fn reflow_line_cap() -> usize {
    std::env::var("CARINA_SCROLLBACK_REFLOW_LINES")
        .ok()
        .and_then(|value| value.parse::<usize>().ok())
        .filter(|value| *value > 0)
        .unwrap_or_else(detected_reflow_line_cap)
}

fn detected_reflow_line_cap() -> usize {
    let program = std::env::var("TERM_PROGRAM")
        .unwrap_or_default()
        .to_ascii_lowercase();
    reflow_line_cap_for(
        &program,
        std::env::var_os("VSCODE_PID").is_some(),
        std::env::var_os("WT_SESSION").is_some(),
        std::env::var_os("WEZTERM_PANE").is_some(),
        std::env::var_os("ALACRITTY_WINDOW_ID").is_some(),
    )
}

fn reflow_line_cap_for(
    program: &str,
    vscode: bool,
    windows_terminal: bool,
    wezterm: bool,
    alacritty: bool,
) -> usize {
    if program == "vscode" || vscode {
        1_000
    } else if windows_terminal {
        9_001
    } else if program.contains("wezterm") || wezterm {
        3_500
    } else if program.contains("alacritty") || alacritty {
        10_000
    } else {
        3_500
    }
}

pub fn history_for_width(
    blocks: &[TranscriptBlock],
    width: u16,
    wrap: ScrollbackWrap,
    line_cap: usize,
) -> String {
    let mut rows = Vec::<(String, usize)>::new();
    for block in blocks {
        let source = raw_block_text(block);
        let terminal_wrap =
            wrap == ScrollbackWrap::Terminal || source.lines().any(is_plain_url_line);
        if terminal_wrap {
            rows.extend(source.lines().map(|line| {
                let physical_rows = split_into_line_segments(line, width.max(1) as usize)
                    .len()
                    .max(1);
                (line.to_owned(), physical_rows)
            }));
        } else {
            rows.extend(
                split_into_line_segments(&source, width.max(1) as usize)
                    .into_iter()
                    .map(|segment| (segment.content.to_owned(), 1)),
            );
        }
    }
    let mut physical_rows = 0_usize;
    let keep_from = rows
        .iter()
        .enumerate()
        .rev()
        .take_while(|(_, (_, cost))| {
            if physical_rows > 0 && physical_rows.saturating_add(*cost) > line_cap.max(1) {
                return false;
            }
            physical_rows = physical_rows.saturating_add(*cost);
            true
        })
        .last()
        .map_or(rows.len(), |(index, _)| index);
    let mut history = rows[keep_from..]
        .iter()
        .map(|(row, _)| row.as_str())
        .collect::<Vec<_>>()
        .join("\r\n");
    if !history.is_empty() {
        history.push_str("\r\n");
    }
    history
}

impl ScrollbackLedger {
    pub fn committed_prefix_len(&self, blocks: &[TranscriptBlock]) -> usize {
        self.committed
            .iter()
            .zip(blocks)
            .take_while(|(stamp, block)| {
                stamp.id == block.id && stamp.revision == block.layout_revision
            })
            .count()
    }

    pub fn reset(&mut self) {
        self.committed.clear();
        self.tail.clear();
    }

    pub fn hold_tool_call(&mut self, blocks: &[TranscriptBlock], call_id: &str) {
        let member_id = format!("tool:{call_id}");
        if let Some(block) = blocks.iter().find(|block| {
            block.id == member_id
                || block
                    .tool_members
                    .iter()
                    .any(|member| member.id == member_id)
        }) {
            self.tail.insert(block.id.clone(), TailState::Held);
        }
    }

    pub fn release_tool_call_after_present(&mut self, blocks: &[TranscriptBlock], call_id: &str) {
        let member_id = format!("tool:{call_id}");
        if let Some(block) = blocks.iter().find(|block| {
            block.id == member_id
                || block
                    .tool_members
                    .iter()
                    .any(|member| member.id == member_id)
        }) {
            self.tail.insert(
                block.id.clone(),
                TailState::AwaitingPresentation(block.layout_revision),
            );
        }
    }

    pub fn stage_for_commit(&mut self, blocks: &[TranscriptBlock], _active_run_id: Option<&str>) {
        let committed = self.committed_prefix_len(blocks);
        for block in &blocks[committed..] {
            match self.tail.get(&block.id) {
                Some(TailState::Held) => continue,
                Some(
                    TailState::AwaitingPresentation(revision) | TailState::Finalized(revision),
                ) if *revision == block.layout_revision => {
                    continue;
                }
                _ => {}
            }
            // A disclosure component cannot be flattened into terminal history
            // without destroying its keyboard/pointer detail lifecycle. Keep it
            // and every following block in the retained interactive tail.
            if block.collapsible {
                break;
            }
            self.tail.insert(
                block.id.clone(),
                TailState::AwaitingPresentation(block.layout_revision),
            );
        }
    }

    pub fn observe_presented(&mut self, blocks: &[TranscriptBlock]) {
        for block in blocks {
            if self.tail.get(&block.id)
                == Some(&TailState::AwaitingPresentation(block.layout_revision))
            {
                self.tail.insert(
                    block.id.clone(),
                    TailState::Finalized(block.layout_revision),
                );
            }
        }
    }

    pub fn pending_finalized<'a>(&self, blocks: &'a [TranscriptBlock]) -> &'a [TranscriptBlock] {
        let committed = self.committed_prefix_len(blocks);
        let count = blocks[committed..]
            .iter()
            .take_while(|block| {
                !block.collapsible
                    && self.tail.get(&block.id)
                        == Some(&TailState::Finalized(block.layout_revision))
            })
            .count();
        &blocks[committed..committed + count]
    }

    pub fn commit(&mut self, blocks: &[TranscriptBlock]) {
        for block in blocks {
            self.committed.push(BlockStamp {
                id: block.id.clone(),
                revision: block.layout_revision,
            });
            self.tail.remove(&block.id);
        }
    }
}

pub fn is_plain_url_line(value: &str) -> bool {
    let value = value.trim();
    !value.is_empty()
        && !value.chars().any(char::is_whitespace)
        && (value.starts_with("https://") || value.starts_with("http://"))
}

pub fn raw_block_text(block: &TranscriptBlock) -> String {
    let mut text = String::new();
    if !block.title.trim().is_empty() {
        text.push_str(block.title.trim());
        if !block.status.trim().is_empty() {
            text.push_str("  ");
            text.push_str(block.status.trim());
        }
        text.push('\n');
    }
    if block.tool_members.len() > 1 {
        for member in &block.tool_members {
            text.push_str("  ");
            text.push_str(member.title.trim());
            if !member.status.trim().is_empty() {
                text.push_str("  ");
                text.push_str(member.status.trim());
            }
            text.push('\n');
            for line in member.body.lines() {
                text.push_str("    ");
                text.push_str(line);
                text.push('\n');
            }
        }
        return text;
    }
    text.push_str(&block.body);
    if !text.ends_with('\n') {
        text.push('\n');
    }
    text
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tool_projection::ToolKind;
    use crate::transcript::{BlockBodyKind, BlockKind, ToolGroupMember};

    #[test]
    fn pure_url_detection_does_not_misclassify_prose() {
        assert!(is_plain_url_line("https://example.test/a/very/long/path"));
        assert!(!is_plain_url_line("see https://example.test/a"));
        assert!(!is_plain_url_line("https://example.test/a trailing"));
    }

    #[test]
    fn ledger_commits_only_the_presented_identity_and_revision() {
        let mut ledger = ScrollbackLedger::default();
        let blocks = vec![TranscriptBlock::local_user("one".into(), "first".into())];
        ledger.stage_for_commit(&blocks, None);
        assert!(ledger.pending_finalized(&blocks).is_empty());
        ledger.observe_presented(&blocks);
        let pending = ledger.pending_finalized(&blocks);
        assert_eq!(pending.len(), 1);
        ledger.commit(pending);
        assert_eq!(ledger.committed_prefix_len(&blocks), 1);

        let mut changed = blocks.clone();
        changed[0].layout_revision += 1;
        assert_eq!(ledger.committed_prefix_len(&changed), 0);
        ledger.reset();
        assert_eq!(ledger.committed_prefix_len(&blocks), 0);
    }

    #[test]
    fn late_artifact_keeps_terminal_disclosure_in_the_repaintable_tail() {
        let mut ledger = ScrollbackLedger::default();
        let user = TranscriptBlock::local_user("user:one".into(), "run tests".into());
        let mut tool = TranscriptBlock::local_user("tool:call-1".into(), "preview".into());
        tool.kind = BlockKind::Tool;
        tool.run_id = "run_1".into();
        tool.collapsible = true;
        tool.expanded = false;
        tool.status.clear();
        let mut blocks = vec![user, tool];

        // Terminal lifecycle arrived, but the artifact RPC is still outstanding.
        ledger.hold_tool_call(&blocks, "call-1");
        ledger.stage_for_commit(&blocks, None);
        ledger.observe_presented(&blocks);

        // A commit attempt may move the finalized user row, never the held group.
        let pending = ledger.pending_finalized(&blocks).to_vec();
        assert_eq!(
            pending
                .iter()
                .map(|block| block.id.as_str())
                .collect::<Vec<_>>(),
            ["user:one"]
        );
        ledger.commit(&pending);
        assert_eq!(ledger.committed_prefix_len(&blocks), 1);
        assert_eq!(ledger.pending_finalized(&blocks).len(), 0);

        // The late artifact changes the exact retained block revision. It must be
        // visible in the mutable tail for one successful paint before finalization.
        blocks[1].body = "complete artifact output".into();
        blocks[1].expanded = true;
        blocks[1].layout_revision += 1;
        ledger.release_tool_call_after_present(&blocks, "call-1");
        let visible = &blocks[ledger.committed_prefix_len(&blocks)..];
        assert_eq!(visible.len(), 1);
        assert_eq!(visible[0].id, "tool:call-1");
        assert!(blocks[1].body.contains("complete artifact output"));
        assert!(ledger.pending_finalized(&blocks).is_empty());

        ledger.observe_presented(&blocks);
        ledger.stage_for_commit(&blocks, Some("run_2"));
        assert!(ledger.pending_finalized(&blocks).is_empty());
    }

    #[test]
    fn artifact_hold_resolves_the_owning_compact_group_by_member_identity() {
        let mut ledger = ScrollbackLedger::default();
        let mut group = TranscriptBlock::local_user("tool:call-1".into(), String::new());
        group.kind = BlockKind::Tool;
        group.run_id = "run_1".into();
        group.collapsible = true;
        group.tool_members = vec![ToolGroupMember {
            id: "tool:call-2".into(),
            tool_name: "run".into(),
            title: "cargo test".into(),
            body: String::new(),
            body_kind: BlockBodyKind::Plain,
            additions: 0,
            deletions: 0,
            status: String::new(),
            lifecycle: "completed".into(),
        }];
        let blocks = vec![group];

        ledger.hold_tool_call(&blocks, "call-2");
        ledger.stage_for_commit(&blocks, Some("run_2"));
        ledger.observe_presented(&blocks);
        assert!(ledger.pending_finalized(&blocks).is_empty());

        ledger.release_tool_call_after_present(&blocks, "call-2");
        assert!(ledger.pending_finalized(&blocks).is_empty());
        ledger.observe_presented(&blocks);
        ledger.stage_for_commit(&blocks, Some("run_2"));
        assert!(ledger.pending_finalized(&blocks).is_empty());
    }

    #[test]
    fn observed_and_reflow_widths_are_independent() {
        let mut state = TranscriptReflowState::default();
        state.observe(80, false);
        assert_eq!(state.last_observed_width(), Some(80));
        assert_eq!(state.last_reflow_width(), None);
        state.mark_reflowed(false);
        state.observe(160, false);
        assert_eq!(state.last_observed_width(), Some(160));
        assert_eq!(state.last_reflow_width(), Some(80));
        assert!(state.needs_reflow());
    }

    #[test]
    fn resize_during_stream_forces_post_completion_reflow() {
        let mut state = TranscriptReflowState::default();
        state.observe(120, true);
        state.mark_reflowed(true);
        assert!(state.stream_finished());
        assert!(state.needs_reflow());
    }

    #[test]
    fn source_is_rewrapped_at_the_new_width_including_cjk() {
        let latin = TranscriptBlock::local_user("latin".into(), "abcdefghijabcdefghij".into());
        let cjk = TranscriptBlock::local_user("cjk".into(), "你好世界你好世界".into());

        let narrow = history_for_width(
            &[latin.clone(), cjk.clone()],
            10,
            ScrollbackWrap::PreWrap,
            100,
        );
        let wide = history_for_width(&[latin, cjk], 20, ScrollbackWrap::PreWrap, 100);

        assert!(narrow.lines().count() > wide.lines().count());
        assert!(wide.contains("abcdefghijabcdefghij\r"));
        assert!(wide.contains("你好世界你好世界\r"));
    }

    #[test]
    fn reflow_cap_keeps_the_newest_rows() {
        let block = TranscriptBlock::local_user("rows".into(), "one\ntwo\nthree\nfour".into());
        let history = history_for_width(&[block], 80, ScrollbackWrap::PreWrap, 2);
        assert_eq!(history, "three\r\nfour\r\n");
    }

    #[test]
    fn terminal_defaults_use_the_verified_reflow_caps() {
        assert_eq!(
            reflow_line_cap_for("vscode", false, false, false, false),
            1_000
        );
        assert_eq!(reflow_line_cap_for("", false, true, false, false), 9_001);
        assert_eq!(
            reflow_line_cap_for("wezterm", false, false, false, false),
            3_500
        );
        assert_eq!(
            reflow_line_cap_for("alacritty", false, false, false, false),
            10_000
        );
    }

    #[test]
    fn collapsed_groups_commit_every_member_and_full_detail() {
        let mut block = TranscriptBlock::local_user("group".into(), String::new());
        block.kind = BlockKind::Tool;
        block.tool_kind = Some(ToolKind::Run);
        block.title = "Ran 2 commands".into();
        block.body.clear();
        block.collapsible = true;
        block.expanded = false;
        block.tool_members = vec![
            ToolGroupMember {
                id: "tool:one".into(),
                tool_name: "run".into(),
                title: "cargo test".into(),
                body: "first line\nsecond line".into(),
                body_kind: BlockBodyKind::Plain,
                additions: 0,
                deletions: 0,
                status: String::new(),
                lifecycle: "completed".into(),
            },
            ToolGroupMember {
                id: "tool:two".into(),
                tool_name: "run".into(),
                title: "cargo clippy".into(),
                body: "warning detail".into(),
                body_kind: BlockBodyKind::Plain,
                additions: 0,
                deletions: 0,
                status: "failed".into(),
                lifecycle: "failed".into(),
            },
        ];

        assert_eq!(
            raw_block_text(&block),
            "Ran 2 commands\n  cargo test\n    first line\n    second line\n  cargo clippy  failed\n    warning detail\n"
        );
    }
}

#[cfg(test)]
mod multi_terminal_policy_tests {
    use super::*;

    #[test]
    fn reflow_caps_encode_verified_terminal_families() {
        // Issue #11 multi-terminal structural certification matrix.
        // Machine-checkable policy for VS Code / Windows Terminal / WezTerm / Alacritty.
        assert_eq!(
            reflow_line_cap_for("vscode", false, false, false, false),
            1_000
        );
        assert_eq!(
            reflow_line_cap_for("", false, true, false, false),
            9_001
        ); // Windows Terminal
        assert_eq!(
            reflow_line_cap_for("wezterm", false, false, false, false),
            3_500
        );
        assert_eq!(
            reflow_line_cap_for("alacritty", false, false, false, false),
            10_000
        );
        // Unknown / Ghostty / iTerm defaults stay on the safer 3500 path.
        assert_eq!(reflow_line_cap_for("ghostty", false, false, false, false), 3_500);
        assert_eq!(reflow_line_cap_for("iTerm.app", false, false, false, false), 3_500);
    }

    #[test]
    fn plain_url_lines_are_never_hard_wrapped_for_linkify() {
        assert!(is_plain_url_line("https://example.com/path"));
        assert!(!is_plain_url_line("see https://example.com later"));
    }

    #[test]
    fn scrollback_wrap_defaults_to_prewrap_for_stable_history() {
        assert_eq!(ScrollbackWrap::default(), ScrollbackWrap::PreWrap);
        assert!(is_plain_url_line("https://ghostty.org"));
    }
}
