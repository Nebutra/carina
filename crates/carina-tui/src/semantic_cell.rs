//! Pure presentation taxonomy for semantic transcript blocks.
//!
//! The daemon and transcript reducer remain the state owners. This module only
//! derives a visual skeleton from typed projection fields so live and replayed
//! blocks cannot drift because of localized copy.

use crate::tool_projection::ToolKind;
use crate::transcript::{BlockBodyKind, BlockKind, TranscriptBlock};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SemanticCellKind {
    User,
    Assistant,
    Thinking,
    Tool,
    ToolGroup,
    Approval,
    Patch,
    Failure,
    Notice,
    Compact,
}

impl SemanticCellKind {
    pub fn from_block(block: &TranscriptBlock) -> Self {
        if block
            .failure
            .as_ref()
            .is_some_and(|failure| !failure.leaves_document())
            || block
                .tool_members
                .iter()
                .any(|member| member.is_failure() && !member.is_policy_denied())
        {
            return Self::Failure;
        }
        if block.id.starts_with("compact:") {
            return Self::Compact;
        }

        if block.kind == BlockKind::Tool
            && (block.body_kind == BlockBodyKind::Diff
                || matches!(block.tool_kind, Some(ToolKind::Patch | ToolKind::Diff))
                || block
                    .tool_members
                    .iter()
                    .any(|member| member.body_kind == BlockBodyKind::Diff))
        {
            return Self::Patch;
        }

        if block.kind == BlockKind::Tool && block.tool_members.len() > 1 {
            return Self::ToolGroup;
        }

        match block.kind {
            BlockKind::User => Self::User,
            BlockKind::Assistant => Self::Assistant,
            BlockKind::Thinking => Self::Thinking,
            BlockKind::Tool => Self::Tool,
            BlockKind::Governance => Self::Approval,
            BlockKind::Status | BlockKind::Diagnostic => Self::Notice,
        }
    }

    pub const fn is_risk(self) -> bool {
        matches!(self, Self::Approval | Self::Patch | Self::Failure)
    }

    pub const fn owns_internal_rail(self) -> bool {
        matches!(
            self,
            Self::Thinking
                | Self::Tool
                | Self::ToolGroup
                | Self::Approval
                | Self::Patch
                | Self::Failure
                | Self::Notice
                | Self::Compact
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::transcript::{FailureAction, FailureKind, FailurePresentation, ToolGroupMember};

    fn block(kind: BlockKind) -> TranscriptBlock {
        let mut block = TranscriptBlock::local_user("cell".into(), String::new());
        block.kind = kind;
        block
    }

    fn member(body_kind: BlockBodyKind, lifecycle: &str) -> ToolGroupMember {
        ToolGroupMember {
            id: "member".into(),
            tool_name: "read".into(),
            title: "src/lib.rs".into(),
            body: String::new(),
            body_kind,
            additions: 0,
            deletions: 0,
            status: String::new(),
            lifecycle: lifecycle.into(),
        }
    }

    #[test]
    fn typed_fields_cover_every_semantic_skeleton() {
        for (block, expected) in [
            (block(BlockKind::User), SemanticCellKind::User),
            (block(BlockKind::Assistant), SemanticCellKind::Assistant),
            (block(BlockKind::Thinking), SemanticCellKind::Thinking),
            (block(BlockKind::Tool), SemanticCellKind::Tool),
            (block(BlockKind::Governance), SemanticCellKind::Approval),
            (block(BlockKind::Status), SemanticCellKind::Notice),
            (block(BlockKind::Diagnostic), SemanticCellKind::Notice),
        ] {
            assert_eq!(SemanticCellKind::from_block(&block), expected);
        }

        let mut group = block(BlockKind::Tool);
        group.tool_members = vec![
            member(BlockBodyKind::Plain, "completed"),
            member(BlockBodyKind::Plain, "completed"),
        ];
        assert_eq!(
            SemanticCellKind::from_block(&group),
            SemanticCellKind::ToolGroup
        );

        let mut patch = block(BlockKind::Tool);
        patch.tool_kind = Some(ToolKind::Patch);
        assert_eq!(
            SemanticCellKind::from_block(&patch),
            SemanticCellKind::Patch
        );

        let mut failure = block(BlockKind::Diagnostic);
        failure.failure = Some(FailurePresentation {
            kind: FailureKind::Failed,
            action: FailureAction::Retry,
            owner: "Carina".into(),
            reason: "Provider unavailable".into(),
            source_event_id: "event-1".into(),
            run_id: "run-1".into(),
            model: "provider/model-1".into(),
            current_model: "provider/model-1".into(),
            retry_root_run_id: "run-1".into(),
            attempt_count: 1,
            focused_action: None,
        });
        assert_eq!(
            SemanticCellKind::from_block(&failure),
            SemanticCellKind::Failure
        );

        failure.failure.as_mut().unwrap().action = FailureAction::Recovering;
        failure.failure.as_mut().unwrap().run_id = "run-2".into();
        assert_eq!(
            SemanticCellKind::from_block(&failure),
            SemanticCellKind::Notice
        );
    }

    #[test]
    fn compact_receipt_identity_is_typed_not_copy() {
        let mut compact = block(BlockKind::Diagnostic);
        compact.id = "compact:evt_receipt".into();
        compact.title = "Context compacted".into();
        compact.body = "Collapsed 12 turns with collapse_only. 84012 → 21004 characters.".into();
        compact.status = "compacted".into();
        assert_eq!(
            SemanticCellKind::from_block(&compact),
            SemanticCellKind::Compact
        );
    }

    #[test]
    fn failure_and_patch_precedence_keep_risk_out_of_routine_tool_shapes() {
        let mut failed_patch_group = block(BlockKind::Tool);
        failed_patch_group.tool_kind = Some(ToolKind::Patch);
        failed_patch_group.tool_members = vec![
            member(BlockBodyKind::Diff, "completed"),
            member(BlockBodyKind::Diff, "failed"),
        ];
        assert_eq!(
            SemanticCellKind::from_block(&failed_patch_group),
            SemanticCellKind::Failure
        );

        failed_patch_group.tool_members[1].lifecycle = "completed".into();
        assert_eq!(
            SemanticCellKind::from_block(&failed_patch_group),
            SemanticCellKind::Patch
        );
    }

    #[test]
    fn visible_copy_cannot_change_the_skeleton() {
        let mut tool = block(BlockKind::Tool);
        tool.title = "approval failed patch question".into();
        tool.body = "diagnostic reasoning assistant".into();
        tool.status = "error".into();
        assert_eq!(SemanticCellKind::from_block(&tool), SemanticCellKind::Tool);
    }

    #[test]
    fn policy_denied_tools_are_not_failure_cells() {
        let mut denied = block(BlockKind::Tool);
        denied.status = "denied".into();
        denied.tool_members = vec![member(BlockBodyKind::Plain, "denied")];
        assert!(denied.tool_members[0].is_policy_denied());
        assert_eq!(
            SemanticCellKind::from_block(&denied),
            SemanticCellKind::Tool
        );
        assert!(!denied.is_live_work());
    }
}
