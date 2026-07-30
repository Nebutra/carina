//! Inline media document projection for the composer (Trellis media child).
//!
//! One layout map owns text and media chip geometry so caret motion, hit testing,
//! and wrapping share a single source of truth.

use unicode_segmentation::UnicodeSegmentation;
use unicode_width::UnicodeWidthStr;
use xai_ratatui_textarea::{ElementId, TextArea};

use crate::media::IMAGE_ELEMENT_KIND;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DocumentNodeKind {
    Text,
    Media,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DocumentNode {
    pub kind: DocumentNodeKind,
    pub text: String,
    pub element_id: Option<ElementId>,
    /// Inclusive start byte offset in the composer text.
    pub start: usize,
    /// Exclusive end byte offset.
    pub end: usize,
    /// Display width in terminal cells.
    pub width: usize,
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct DocumentLayoutMap {
    pub nodes: Vec<DocumentNode>,
    pub total_width: usize,
    pub grapheme_count: usize,
}

impl DocumentLayoutMap {
    /// Project the current textarea into an ordered text/media node list.
    pub fn from_textarea(textarea: &TextArea) -> Self {
        let text = textarea.text();
        let mut media: Vec<_> = textarea
            .elements()
            .iter()
            .filter(|element| element.kind == IMAGE_ELEMENT_KIND)
            .map(|element| (element.range.start, element.range.end, element.id))
            .collect();
        media.sort_by_key(|(start, _, _)| *start);

        let mut nodes = Vec::new();
        let mut cursor = 0usize;
        for (start, end, id) in media {
            let start = start.min(text.len());
            let end = end.min(text.len()).max(start);
            if cursor < start {
                push_text_node(&mut nodes, text, cursor, start);
            }
            let chip = text.get(start..end).unwrap_or("");
            nodes.push(DocumentNode {
                kind: DocumentNodeKind::Media,
                text: chip.to_owned(),
                element_id: Some(id),
                start,
                end,
                width: UnicodeWidthStr::width(chip).max(1),
            });
            cursor = end;
        }
        if cursor < text.len() {
            push_text_node(&mut nodes, text, cursor, text.len());
        }
        let total_width = nodes.iter().map(|node| node.width).sum();
        let grapheme_count = nodes
            .iter()
            .map(|node| node.text.graphemes(true).count())
            .sum();
        Self {
            nodes,
            total_width,
            grapheme_count,
        }
    }

    /// Byte offset of the node boundary at or after `byte_offset` when moving by
    /// one atomic step (media chips are atomic).
    pub fn step_right(&self, byte_offset: usize) -> usize {
        for node in &self.nodes {
            if byte_offset < node.start {
                return node.start;
            }
            if byte_offset >= node.start && byte_offset < node.end {
                if node.kind == DocumentNodeKind::Media {
                    return node.end;
                }
                // Grapheme step inside text.
                let rel = byte_offset - node.start;
                let rest = &node.text[rel.min(node.text.len())..];
                if let Some(g) = rest.graphemes(true).next() {
                    return byte_offset + g.len();
                }
                return node.end;
            }
        }
        self.nodes.last().map(|node| node.end).unwrap_or(0)
    }

    pub fn step_left(&self, byte_offset: usize) -> usize {
        for node in self.nodes.iter().rev() {
            if byte_offset > node.end {
                return node.end;
            }
            if byte_offset > node.start && byte_offset <= node.end {
                if node.kind == DocumentNodeKind::Media {
                    return node.start;
                }
                let rel = byte_offset - node.start;
                let head = &node.text[..rel.min(node.text.len())];
                if let Some(g) = head.graphemes(true).next_back() {
                    return byte_offset - g.len();
                }
                return node.start;
            }
        }
        0
    }

    pub fn media_count(&self) -> usize {
        self.nodes
            .iter()
            .filter(|node| node.kind == DocumentNodeKind::Media)
            .count()
    }

    pub fn hit_test(&self, cell_column: usize) -> Option<&DocumentNode> {
        let mut col = 0usize;
        for node in &self.nodes {
            let next = col + node.width;
            if cell_column >= col && cell_column < next {
                return Some(node);
            }
            col = next;
        }
        None
    }
}

fn push_text_node(nodes: &mut Vec<DocumentNode>, text: &str, start: usize, end: usize) {
    if start >= end {
        return;
    }
    let slice = text.get(start..end).unwrap_or("");
    if slice.is_empty() {
        return;
    }
    nodes.push(DocumentNode {
        kind: DocumentNodeKind::Text,
        text: slice.to_owned(),
        element_id: None,
        start,
        end,
        width: UnicodeWidthStr::width(slice),
    });
}

/// Degraded narrow-terminal layout: keep document ownership, collapse chip
/// labels to a single cell marker width for measurement only.
pub fn degraded_chip_width(terminal_width: u16) -> usize {
    if terminal_width < 40 {
        1
    } else if terminal_width < 60 {
        4
    } else {
        12
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::media::{IMAGE_ELEMENT_KIND, MediaChipLabels, MediaComposer, MediaSourceLabel};
    use std::path::PathBuf;
    use xai_ratatui_textarea::{ElementKind, TextArea};

    fn with_chip(prefix: &str, suffix: &str) -> TextArea {
        let mut area = TextArea::new();
        let mut media = MediaComposer::default();
        area.insert_str(prefix);
        media
            .insert_pending(
                &mut area,
                PathBuf::from("/tmp/layout-image.png"),
                "image/png".into(),
                16,
                MediaSourceLabel::User(Some("shot.png".into())),
                MediaChipLabels::default(),
            )
            .unwrap();
        area.insert_str(suffix);
        area
    }

    #[test]
    fn layout_map_keeps_media_atomic_between_text() {
        let area = with_chip("hello ", " world");
        let map = DocumentLayoutMap::from_textarea(&area);
        assert_eq!(map.media_count(), 1);
        assert!(map.nodes.iter().any(|n| n.kind == DocumentNodeKind::Text));
        let media = map
            .nodes
            .iter()
            .find(|n| n.kind == DocumentNodeKind::Media)
            .expect("media");
        // Stepping from inside the chip lands on the end boundary.
        assert_eq!(map.step_right(media.start), media.end);
        assert_eq!(map.step_left(media.end), media.start);
    }

    #[test]
    fn cjk_grapheme_steps_do_not_split_wide_characters() {
        let mut area = TextArea::new();
        area.insert_str("你好世界");
        let map = DocumentLayoutMap::from_textarea(&area);
        assert_eq!(map.grapheme_count, 4);
        let mut offset = 0;
        let mut steps = 0;
        while offset < area.text().len() {
            let next = map.step_right(offset);
            assert!(next > offset);
            offset = next;
            steps += 1;
        }
        assert_eq!(steps, 4);
    }

    #[test]
    fn hit_test_resolves_media_by_cell_column() {
        let area = with_chip("ab", "cd");
        let map = DocumentLayoutMap::from_textarea(&area);
        // "ab" is width 2; next cells belong to the chip.
        let hit = map.hit_test(2).expect("chip hit");
        assert_eq!(hit.kind, DocumentNodeKind::Media);
    }

    #[test]
    fn degraded_width_shrinks_on_tiny_terminals() {
        assert_eq!(degraded_chip_width(20), 1);
        assert_eq!(degraded_chip_width(50), 4);
        assert_eq!(degraded_chip_width(100), 12);
    }

    #[test]
    fn image_element_kind_matches_media_module() {
        assert_eq!(IMAGE_ELEMENT_KIND, ElementKind(3));
    }
}
