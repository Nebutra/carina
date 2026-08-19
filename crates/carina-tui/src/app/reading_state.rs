use std::collections::BTreeMap;

use crate::native_scrollback::{ScrollbackLedger, ScrollbackStamp};
use crate::semantic_cell::SemanticCellKind;
use crate::transcript::TranscriptBlock;

pub const READING_STATE_VERSION: u8 = 1;
const MAX_DISCLOSURE_OVERRIDES: usize = 256;
const MAX_ID_CHARS: usize = 256;
const MAX_SCROLLBACK_STAMPS: usize = 4_096;
const MAX_POSITION: usize = 1_000_000;

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct ReadingStateEnvelopeV1 {
    pub version: u8,
    pub session_id: String,
    #[serde(default)]
    pub selected_block_id: Option<String>,
    #[serde(default)]
    pub disclosure_overrides: BTreeMap<String, bool>,
    pub follow_bottom: bool,
    #[serde(default)]
    pub top_visible: Option<LogicalTranscriptAnchorV1>,
    #[serde(default)]
    pub committed_scrollback: Vec<ScrollbackStamp>,
}

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct LogicalTranscriptAnchorV1 {
    pub block_id: String,
    pub logical_line: usize,
    pub wrapped_sub_row: usize,
    #[serde(default)]
    pub position_hint: usize,
    #[serde(default)]
    pub previous_block_id: Option<String>,
    #[serde(default)]
    pub next_block_id: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RestoredReading {
    pub selected_index: Option<usize>,
    pub disclosure_overrides: BTreeMap<String, bool>,
    pub follow_bottom: bool,
    pub top_visible: Option<LogicalTranscriptAnchorV1>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ReadingStateError {
    UnknownVersion,
    SessionMismatch,
    Oversized,
    ScrollbackDrift,
}

impl ReadingStateEnvelopeV1 {
    pub fn validate(&self, session_id: &str) -> Result<(), ReadingStateError> {
        if self.version != READING_STATE_VERSION {
            return Err(ReadingStateError::UnknownVersion);
        }
        if self.session_id != session_id || self.session_id.trim().is_empty() {
            return Err(ReadingStateError::SessionMismatch);
        }
        if self.disclosure_overrides.len() > MAX_DISCLOSURE_OVERRIDES
            || self.committed_scrollback.len() > MAX_SCROLLBACK_STAMPS
            || !id_ok(self.selected_block_id.as_deref())
            || self.disclosure_overrides.keys().any(|id| !id_ok(Some(id)))
            || self
                .top_visible
                .as_ref()
                .is_some_and(|anchor| !anchor.is_bounded())
            || self
                .committed_scrollback
                .iter()
                .any(|stamp| !id_ok(Some(&stamp.id)))
        {
            return Err(ReadingStateError::Oversized);
        }
        Ok(())
    }
}

impl LogicalTranscriptAnchorV1 {
    fn is_bounded(&self) -> bool {
        id_ok(Some(&self.block_id))
            && id_ok(self.previous_block_id.as_deref())
            && id_ok(self.next_block_id.as_deref())
            && self.logical_line <= MAX_POSITION
            && self.wrapped_sub_row <= MAX_POSITION
            && self.position_hint <= MAX_POSITION
    }
}

fn id_ok(id: Option<&str>) -> bool {
    id.is_none_or(|value| value.chars().count() <= MAX_ID_CHARS)
}

pub fn capture_reading_state(
    session_id: &str,
    blocks: &[TranscriptBlock],
    selected_index: Option<usize>,
    disclosure_overrides: &std::collections::HashMap<String, bool>,
    follow_bottom: bool,
    top_visible: Option<LogicalTranscriptAnchorV1>,
    committed_scrollback: Vec<ScrollbackStamp>,
) -> ReadingStateEnvelopeV1 {
    let selected_block_id = selected_index
        .and_then(|index| blocks.get(index))
        .map(|block| block.id.clone());
    let mut disclosure = BTreeMap::new();
    for (id, expanded) in disclosure_overrides {
        if disclosure.len() >= MAX_DISCLOSURE_OVERRIDES {
            break;
        }
        if blocks
            .iter()
            .any(|block| block.id == *id && block.is_collapsible())
        {
            disclosure.insert(id.clone(), *expanded);
        }
    }
    ReadingStateEnvelopeV1 {
        version: READING_STATE_VERSION,
        session_id: session_id.to_owned(),
        selected_block_id,
        disclosure_overrides: disclosure,
        follow_bottom,
        top_visible: if follow_bottom { None } else { top_visible },
        committed_scrollback,
    }
}

pub fn restore_reading_state(
    envelope: &ReadingStateEnvelopeV1,
    session_id: &str,
    blocks: &[TranscriptBlock],
    scrollback: &mut ScrollbackLedger,
) -> Result<RestoredReading, ReadingStateError> {
    envelope.validate(session_id)?;
    if !scrollback_prefix_matches(blocks, &envelope.committed_scrollback) {
        return Err(ReadingStateError::ScrollbackDrift);
    }
    scrollback
        .restore_committed_prefix(blocks, envelope.committed_scrollback.clone())
        .map_err(|_| ReadingStateError::ScrollbackDrift)?;
    let disclosure_overrides = surviving_disclosure(blocks, &envelope.disclosure_overrides);
    let selected_index = resolve_selection(blocks, envelope.selected_block_id.as_deref(), envelope);
    let top_visible = if envelope.follow_bottom {
        None
    } else {
        envelope
            .top_visible
            .as_ref()
            .and_then(|anchor| resolve_anchor(blocks, anchor))
    };
    Ok(RestoredReading {
        selected_index,
        disclosure_overrides,
        follow_bottom: envelope.follow_bottom,
        top_visible,
    })
}

pub fn surviving_disclosure(
    blocks: &[TranscriptBlock],
    overrides: &BTreeMap<String, bool>,
) -> BTreeMap<String, bool> {
    overrides
        .iter()
        .filter(|(id, _)| {
            blocks
                .iter()
                .any(|block| block.id == **id && block.is_collapsible())
        })
        .map(|(id, expanded)| (id.clone(), *expanded))
        .collect()
}

pub fn resolve_selection(
    blocks: &[TranscriptBlock],
    selected_block_id: Option<&str>,
    envelope: &ReadingStateEnvelopeV1,
) -> Option<usize> {
    if let Some(id) = selected_block_id
        && let Some(index) = index_of(blocks, id)
    {
        return Some(index);
    }
    envelope
        .top_visible
        .as_ref()
        .and_then(|anchor| resolve_anchor(blocks, anchor))
        .and_then(|anchor| index_of(blocks, &anchor.block_id))
}

pub fn resolve_anchor(
    blocks: &[TranscriptBlock],
    anchor: &LogicalTranscriptAnchorV1,
) -> Option<LogicalTranscriptAnchorV1> {
    if let Some(index) = readable_index(blocks, &anchor.block_id) {
        return Some(clamp_anchor(
            blocks,
            index,
            anchor.logical_line,
            anchor.wrapped_sub_row,
        ));
    }
    if let Some(id) = anchor.next_block_id.as_deref()
        && let Some(index) = readable_index(blocks, id)
    {
        return Some(clamp_anchor(blocks, index, 0, 0));
    }
    if let Some(id) = anchor.previous_block_id.as_deref()
        && let Some(index) = readable_index(blocks, id)
    {
        return Some(clamp_anchor(blocks, index, usize::MAX, 0));
    }
    nearest_semantic_index(blocks, anchor.position_hint)
        .map(|index| clamp_anchor(blocks, index, 0, 0))
}

pub fn neighbors_for(blocks: &[TranscriptBlock], index: usize) -> (Option<String>, Option<String>) {
    let readable: Vec<usize> = (0..blocks.len())
        .filter(|candidate| is_readable(&blocks[*candidate]))
        .collect();
    let position = readable.iter().position(|candidate| *candidate == index);
    let previous = position
        .and_then(|offset| offset.checked_sub(1))
        .and_then(|offset| readable.get(offset))
        .map(|block_index| blocks[*block_index].id.clone());
    let next = position
        .and_then(|offset| readable.get(offset + 1))
        .map(|block_index| blocks[*block_index].id.clone());
    (previous, next)
}

pub fn position_hint_for(blocks: &[TranscriptBlock], index: usize) -> usize {
    semantic_indices(blocks)
        .into_iter()
        .position(|candidate| candidate == index)
        .unwrap_or(index)
}

fn clamp_anchor(
    blocks: &[TranscriptBlock],
    index: usize,
    logical_line: usize,
    wrapped_sub_row: usize,
) -> LogicalTranscriptAnchorV1 {
    let (previous_block_id, next_block_id) = neighbors_for(blocks, index);
    LogicalTranscriptAnchorV1 {
        block_id: blocks[index].id.clone(),
        logical_line,
        wrapped_sub_row,
        position_hint: position_hint_for(blocks, index),
        previous_block_id,
        next_block_id,
    }
}

fn scrollback_prefix_matches(blocks: &[TranscriptBlock], stamps: &[ScrollbackStamp]) -> bool {
    stamps.len() <= blocks.len()
        && stamps
            .iter()
            .zip(blocks)
            .all(|(stamp, block)| stamp.id == block.id && stamp.revision == block.layout_revision)
}

fn index_of(blocks: &[TranscriptBlock], id: &str) -> Option<usize> {
    blocks.iter().position(|block| block.id == id)
}

fn readable_index(blocks: &[TranscriptBlock], id: &str) -> Option<usize> {
    index_of(blocks, id).filter(|index| is_readable(&blocks[*index]))
}

fn nearest_semantic_index(blocks: &[TranscriptBlock], hint: usize) -> Option<usize> {
    let semantic = semantic_indices(blocks);
    if semantic.is_empty() {
        return None;
    }
    let clamped = hint.min(semantic.len().saturating_sub(1));
    semantic.get(clamped).copied()
}

fn semantic_indices(blocks: &[TranscriptBlock]) -> Vec<usize> {
    (0..blocks.len())
        .filter(|index| is_semantic(&blocks[*index]))
        .collect()
}

fn is_readable(block: &TranscriptBlock) -> bool {
    !(block.tool_members.is_empty()
        && block.todo_items.is_empty()
        && block.title.trim().is_empty()
        && block.body.trim().is_empty()
        && block.status.trim().is_empty())
}

fn is_semantic(block: &TranscriptBlock) -> bool {
    is_readable(block)
        && !matches!(
            SemanticCellKind::from_block(block),
            SemanticCellKind::Notice | SemanticCellKind::Approval | SemanticCellKind::Compact
        )
}

#[cfg(test)]
mod tests {
    use super::*;

    fn block(id: &str, body: &str) -> TranscriptBlock {
        let mut block = TranscriptBlock::local_user(id.into(), body.into());
        block.id = id.into();
        block.kind = crate::transcript::BlockKind::Assistant;
        block.body = body.into();
        block
    }

    fn hidden(id: &str) -> TranscriptBlock {
        let mut block = TranscriptBlock::local_user(id.into(), String::new());
        block.id = id.into();
        block.body.clear();
        block.title.clear();
        block.status.clear();
        block
    }

    #[test]
    fn resolve_anchor_uses_exact_then_neighbors_then_hint() {
        let blocks = vec![block("a", "one"), hidden("gap"), block("c", "three")];
        let exact = LogicalTranscriptAnchorV1 {
            block_id: "c".into(),
            logical_line: 2,
            wrapped_sub_row: 1,
            position_hint: 1,
            previous_block_id: Some("a".into()),
            next_block_id: None,
        };
        assert_eq!(resolve_anchor(&blocks, &exact).unwrap().block_id, "c");

        let via_next = LogicalTranscriptAnchorV1 {
            block_id: "missing".into(),
            logical_line: 9,
            wrapped_sub_row: 9,
            position_hint: 0,
            previous_block_id: None,
            next_block_id: Some("c".into()),
        };
        let restored = resolve_anchor(&blocks, &via_next).unwrap();
        assert_eq!(restored.block_id, "c");
        assert_eq!(restored.logical_line, 0);

        let via_prev = LogicalTranscriptAnchorV1 {
            block_id: "missing".into(),
            logical_line: 9,
            wrapped_sub_row: 9,
            position_hint: 0,
            previous_block_id: Some("a".into()),
            next_block_id: None,
        };
        assert_eq!(resolve_anchor(&blocks, &via_prev).unwrap().block_id, "a");

        let via_hint = LogicalTranscriptAnchorV1 {
            block_id: "missing".into(),
            logical_line: 0,
            wrapped_sub_row: 0,
            position_hint: 1,
            previous_block_id: None,
            next_block_id: None,
        };
        assert_eq!(resolve_anchor(&blocks, &via_hint).unwrap().block_id, "c");
    }

    #[test]
    fn restore_filters_stale_disclosure_and_rejects_bad_envelopes() {
        let mut tool = block("tool:1", "body");
        tool.collapsible = true;
        let blocks = vec![block("user:1", "hi"), tool];
        let mut envelope = ReadingStateEnvelopeV1 {
            version: 1,
            session_id: "sess".into(),
            selected_block_id: Some("gone".into()),
            disclosure_overrides: BTreeMap::from([
                ("tool:1".into(), true),
                ("tool:stale".into(), false),
            ]),
            follow_bottom: false,
            top_visible: Some(LogicalTranscriptAnchorV1 {
                block_id: "user:1".into(),
                logical_line: 0,
                wrapped_sub_row: 0,
                position_hint: 0,
                previous_block_id: None,
                next_block_id: Some("tool:1".into()),
            }),
            committed_scrollback: Vec::new(),
        };
        let mut ledger = ScrollbackLedger::default();
        let restored = restore_reading_state(&envelope, "sess", &blocks, &mut ledger).unwrap();
        assert_eq!(restored.disclosure_overrides.len(), 1);
        assert!(restored.disclosure_overrides["tool:1"]);
        assert_eq!(
            restored
                .selected_index
                .and_then(|index| blocks.get(index).map(|block| block.id.as_str())),
            Some("user:1")
        );

        envelope.version = 2;
        assert_eq!(
            restore_reading_state(&envelope, "sess", &blocks, &mut ledger),
            Err(ReadingStateError::UnknownVersion)
        );
        envelope.version = 1;
        envelope.session_id = "other".into();
        assert_eq!(
            restore_reading_state(&envelope, "sess", &blocks, &mut ledger),
            Err(ReadingStateError::SessionMismatch)
        );
    }
}
