//! Product-wide terminal geometry shared by setup and conversation surfaces.

use ratatui::layout::{Margin, Position, Rect};

pub const PRODUCT_MAX_WIDTH: u16 = 180;
pub const TRANSCRIPT_READING_MAX_WIDTH: u16 = 120;

pub const SHORT_TERMINAL_ROWS: u16 = 16;
pub const AUTO_COMPACT_MAX_ROWS: u16 = 20;
const _: () = assert!(SHORT_TERMINAL_ROWS < AUTO_COMPACT_MAX_ROWS);

pub const COMPACT_HEADER_HEIGHT: u16 = 3;
pub const EXPANDED_HEADER_HEIGHT: u16 = 6;

const NARROW_MARGIN: u16 = 1;
const REGULAR_MARGIN: u16 = 2;
const WIDE_MARGIN: u16 = 3;
const REGULAR_CONTENT_MIN_WIDTH: u16 = 68;
const WIDE_CONTENT_MIN_WIDTH: u16 = 114;

const BROWSER_LIST_MIN_WIDTH: u16 = 60;
pub const COLUMN_GAP: u16 = 2;
pub const BROWSER_DETAIL_MIN_WIDTH: u16 = 32;
pub const BROWSER_TWO_COLUMN_MIN_WIDTH: u16 =
    BROWSER_LIST_MIN_WIDTH + COLUMN_GAP + BROWSER_DETAIL_MIN_WIDTH;

const HEADER_MARK_WIDTH: u16 = 13;
const HEADER_MARK_GAP: u16 = 4;
const HEADER_METADATA_MIN_WIDTH: u16 = 58;
const HEADER_ACTION_MIN_WIDTH: u16 = 20;
const HEADER_TRAILING_GUTTER: u16 = 1;
pub const EXPANDED_HEADER_MIN_WIDTH: u16 = HEADER_MARK_WIDTH
    + HEADER_MARK_GAP
    + HEADER_METADATA_MIN_WIDTH
    + HEADER_ACTION_MIN_WIDTH
    + HEADER_TRAILING_GUTTER;
const EXPANDED_BODY_MIN_HEIGHT: u16 = 24;
pub const EXPANDED_HEADER_MIN_TERMINAL_HEIGHT: u16 =
    EXPANDED_HEADER_HEIGHT + EXPANDED_BODY_MIN_HEIGHT;

pub const BROWSER_TITLE_HEIGHT: u16 = 4;
pub const BROWSER_MIN_BODY_HEIGHT: u16 = 3;
pub const COMPACT_DETAIL_MIN_HEIGHT: u16 = 8;
pub const COMPACT_DETAIL_HEIGHT: u16 = 2;
pub const COMPACT_DETAIL_LIST_MIN_HEIGHT: u16 = 6;

pub const SCENE_HEADER_GAP: u16 = 1;
pub const SCENE_CONTENT_MIN_HEIGHT: u16 = 6;
pub const SCENE_FOOTER_HEIGHT: u16 = 2;

/// One content row plus a bottom hairline. Conversation chrome intentionally
/// stays independent from the denser setup header.
pub const CONVERSATION_HEADER_HEIGHT: u16 = 2;
pub const PRODUCT_MENU_WIDTH: u16 = 28;
pub const PRODUCT_MENU_HEIGHT: u16 = 7;
pub const PRODUCT_MENU_SEPARATOR_ROWS: u16 = 1;
pub const CONVERSATION_MIN_TRANSCRIPT_HEIGHT: u16 = 4;
pub const COMPOSER_MIN_HEIGHT: u16 = 1;
pub const COMPOSER_MAX_HEIGHT: u16 = 5;
pub const COMPOSER_PROMPT_COLUMNS: u16 = 2;
pub const COMPOSER_CHROME_ROWS: u16 = 1;
pub const COMPOSER_CHROME_HEIGHT: u16 = 2;
pub const TRANSCRIPT_HORIZONTAL_INSET: u16 = 1;
pub const TRANSCRIPT_SCROLLBAR_GAP: u16 = 1;
pub const TRANSCRIPT_SCROLLBAR_WIDTH: u16 = 1;
pub const TRANSCRIPT_ROLE_MARK_WIDTH: usize = 2;
pub const TRANSCRIPT_ROLE_LABEL_WIDTH: usize = 6;
/// Imported history uses its full source marker on the first visual row while
/// wrapped rows retain the same compact semantic rail as native messages.
pub const TRANSCRIPT_IMPORTED_ROLE_LABEL_MAX_WIDTH: usize = 21;
pub const TRANSCRIPT_ROLE_GAP_WIDTH: usize = 2;
pub const TRANSCRIPT_ROLE_PREFIX_WIDTH: usize =
    TRANSCRIPT_ROLE_MARK_WIDTH + TRANSCRIPT_ROLE_LABEL_WIDTH + TRANSCRIPT_ROLE_GAP_WIDTH;
/// Below this content width, continuation rows keep only the semantic rail so
/// role labels do not consume a disproportionate share of the reading measure.
pub const TRANSCRIPT_FULL_ROLE_MIN_WIDTH: u16 = 72;
pub const TRANSCRIPT_TOOL_GUTTER_WIDTH: usize = 2;
/// Fixed tool-kind label field for intent-first rows (matches role label width).
pub const TRANSCRIPT_TOOL_LABEL_WIDTH: usize = 6;
pub const TRANSCRIPT_RELATED_GAP: usize = 0;
pub const TRANSCRIPT_BLOCK_GAP: usize = 1;
pub const TRANSCRIPT_FINAL_GAP: usize = TRANSCRIPT_BLOCK_GAP;
const _: () = assert!(TRANSCRIPT_RELATED_GAP < TRANSCRIPT_BLOCK_GAP);

pub const COMPLETION_MAX_WIDTH: u16 = 88;
pub const COMPLETION_MIN_ROWS: usize = 1;
pub const COMPLETION_MAX_ROWS: usize = 8;
pub const COMPLETION_MIN_WIDTH: u16 = 24;
pub const COMPLETION_MIN_HEIGHT: u16 = 4;
pub const HISTORY_COMPLETION_MIN_HEIGHT: u16 = 5;

pub const APPROVAL_POPUP: (u16, u16) = (78, 26);
pub const QUESTION_POPUP: (u16, u16) = (72, 22);
pub const SETTINGS_POPUP: (u16, u16) = (62, 20);
pub const CHECKPOINT_POPUP: (u16, u16) = (76, 24);
pub const AGENTS_POPUP: (u16, u16) = (104, 32);
pub const FILE_VIEWER_POPUP: (u16, u16) = (110, 36);
pub const TOOL_OUTPUT_CONTEXT_HEIGHT: u16 = 4;
pub const TOOL_OUTPUT_FOOTER_HEIGHT: u16 = 1;
pub const TOOL_OUTPUT_MAX_BUDGET: usize = 24;
pub const TOOL_OUTPUT_MIN_BUDGET: usize = 3;
pub const CHANGES_POPUP: (u16, u16) = (104, 32);
pub const SPLIT_PANE_MIN_WIDTH: u16 = 72;
// A 120-column terminal leaves 110 review cells after product margins/chrome.
pub const PATCH_REVIEW_THREE_PANE_MIN_WIDTH: u16 = 108;
pub const PATCH_REVIEW_SUMMARY_HEIGHT: u16 = 3;
pub const PATCH_REVIEW_FOOTER_HEIGHT: u16 = 2;
pub const PATCH_REVIEW_STACKED_LIST_HEIGHT: u16 = 8;

pub const MEDIA_PREVIEW_WIDTH: u16 = 42;
pub const MEDIA_PREVIEW_HEIGHT: u16 = 12;
pub const HEADER_LABEL_WIDTH: u16 = 11;
pub const HEADER_LABEL_TEXT_WIDTH: usize = HEADER_LABEL_WIDTH as usize - 1;
pub const SLASH_COMMAND_LABEL_WIDTH: usize = 14;
pub const AGENT_CATEGORY_WIDTH: usize = 12;
pub const CHANGE_MARKER_WIDTH: usize = 8;
pub const ROW_HEIGHT: u16 = 1;
pub const CONTROL_HEIGHT: u16 = 2;
pub const COMPACT_SECTION_HEIGHT: u16 = 3;
pub const SECTION_HEADER_HEIGHT: u16 = 4;
pub const REVIEW_SUMMARY_HEIGHT: u16 = 5;
pub const PROVIDER_ROWS_MIN_HEIGHT: u16 = 2;
pub const FORM_MIN_HEIGHT: u16 = 3;
pub const SESSION_LIST_MIN_HEIGHT: u16 = 1;
pub const SESSION_PRIMARY_ACTION_WIDTH: u16 = 22;
pub const SESSION_SECONDARY_ACTION_WIDTH: u16 = 18;
pub const MIN_DIMENSION: u16 = 1;
pub const BLOCK_PADDING: u16 = 2;
pub const PANEL_PADDING: u16 = 4;
pub const PROVIDER_DETAIL_CURSOR_OFFSET: u16 = 6;
pub const PROVIDER_STATE_RESERVE: u16 = 24;
/// Cell gap between provider name column and right-hand status column.
pub const PROVIDER_STATE_GUTTER: u16 = 1;
/// Cell padding around glyph/label inside the status column (and button chrome).
pub const LABEL_CELL_PAD: u16 = 1;
pub const ACTION_BUTTON_PAD: u16 = 2;
pub const PROVIDER_DETAIL_LABEL_WIDTH: usize = 16;
pub const MODEL_ID_WIDTH: usize = 38;
pub const SESSION_LABEL_WIDTH: usize = 34;
pub const SESSION_ACTION_CURSOR_OFFSET: u16 = 8;
pub const MEDIA_STATUS_LABEL_WIDTH: usize = 12;
pub const SLASH_COMPLETION_MIN_WIDTH: usize = 32;
pub const APPROVAL_BODY_MIN_HEIGHT: u16 = 4;
pub const QUESTION_BODY_MIN_HEIGHT: u16 = 5;
pub const ACTIVITY_KIND_WIDTH: usize = 22;
pub const COMPACT_HINT_MAX_WIDTH: u16 = 60;
pub const CHANGE_KIND_WIDTH: usize = 10;
pub const MEDIA_GRAPHICS_MIN_WIDTH: u16 = 4;
pub const MEDIA_GRAPHICS_MIN_HEIGHT: u16 = 2;
pub const MEDIA_PREVIEW_BOUNDS_MIN_WIDTH: u16 = 24;
pub const MEDIA_PREVIEW_BOUNDS_MIN_HEIGHT: u16 = 6;
pub const MEDIA_PREVIEW_MIN_WIDTH: u16 = 6;
pub const MEDIA_PREVIEW_MIN_HEIGHT: u16 = 5;
pub const MEDIA_METADATA_HEIGHT: u16 = 3;

pub fn horizontal_margin(width: u16) -> u16 {
    if width >= WIDE_CONTENT_MIN_WIDTH + WIDE_MARGIN * 2 {
        WIDE_MARGIN
    } else if width >= REGULAR_CONTENT_MIN_WIDTH + REGULAR_MARGIN * 2 {
        REGULAR_MARGIN
    } else {
        NARROW_MARGIN
    }
}

pub fn vertical_margin(height: u16) -> u16 {
    u16::from(!effective_compact(false, height))
}

pub fn canvas(area: Rect) -> Rect {
    let inset = area.inner(Margin::new(
        horizontal_margin(area.width),
        vertical_margin(area.height),
    ));
    centered_width(inset, PRODUCT_MAX_WIDTH)
}

pub fn content(area: Rect) -> Rect {
    centered_width(area, PRODUCT_MAX_WIDTH)
}

pub fn transcript_content(area: Rect) -> Rect {
    let scrollbar_gap = u16::from(
        area.width
            >= TRANSCRIPT_READING_MAX_WIDTH
                + TRANSCRIPT_HORIZONTAL_INSET
                + TRANSCRIPT_SCROLLBAR_GAP
                + TRANSCRIPT_SCROLLBAR_WIDTH,
    ) * TRANSCRIPT_SCROLLBAR_GAP;
    let width = area
        .width
        .saturating_sub(TRANSCRIPT_HORIZONTAL_INSET + scrollbar_gap + TRANSCRIPT_SCROLLBAR_WIDTH)
        .min(TRANSCRIPT_READING_MAX_WIDTH);
    Rect::new(
        area.x + area.width.saturating_sub(width) / 2,
        area.y,
        width,
        area.height,
    )
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct TranscriptGeometry {
    pub viewport: Rect,
    pub leading_chrome: Rect,
    pub content: Rect,
    pub scrollbar_gap: Rect,
    pub scrollbar: Rect,
}

impl TranscriptGeometry {
    pub fn compute(viewport: Rect) -> Self {
        let content = transcript_content(viewport);
        let leading_chrome = Rect::new(
            content.x.saturating_sub(TRANSCRIPT_HORIZONTAL_INSET),
            content.y,
            TRANSCRIPT_HORIZONTAL_INSET.min(content.x.saturating_sub(viewport.x)),
            content.height,
        );
        let available_after_content = viewport.right().saturating_sub(content.right());
        let scrollbar_gap_width =
            if available_after_content >= TRANSCRIPT_SCROLLBAR_GAP + TRANSCRIPT_SCROLLBAR_WIDTH {
                TRANSCRIPT_SCROLLBAR_GAP
            } else {
                0
            };
        let scrollbar_gap = Rect::new(
            content.right(),
            content.y,
            scrollbar_gap_width,
            content.height,
        );
        let scrollbar = Rect::new(
            scrollbar_gap.right(),
            content.y,
            TRANSCRIPT_SCROLLBAR_WIDTH.min(viewport.right().saturating_sub(scrollbar_gap.right())),
            content.height,
        );
        Self {
            viewport,
            leading_chrome,
            content,
            scrollbar_gap,
            scrollbar,
        }
    }
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct TranscriptScrollbar {
    pub area: Rect,
    pub start: Rect,
    pub track: Rect,
    pub thumb: Rect,
    pub end: Rect,
    pub content_len: usize,
    pub viewport_len: usize,
    pub offset: usize,
    pub max_offset: usize,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct TranscriptScrollbarInteraction {
    grab_row: Option<u16>,
}

impl TranscriptScrollbarInteraction {
    pub const fn is_dragging(self) -> bool {
        self.grab_row.is_some()
    }

    pub fn release(&mut self) -> bool {
        let captured = self.is_dragging();
        self.grab_row = None;
        captured
    }
}

impl TranscriptScrollbar {
    pub fn compute(area: Rect, content_len: usize, viewport_len: usize, offset: usize) -> Self {
        let viewport_len = viewport_len.min(content_len);
        let max_offset = content_len.saturating_sub(viewport_len);
        let offset = offset.min(max_offset);
        if area.width == 0 || area.height == 0 || max_offset == 0 {
            return Self {
                area,
                content_len,
                viewport_len,
                offset,
                max_offset,
                ..Self::default()
            };
        }

        let (start, track, end) = if area.height >= 3 {
            (
                Rect::new(area.x, area.y, area.width, 1),
                Rect::new(area.x, area.y + 1, area.width, area.height - 2),
                Rect::new(area.x, area.bottom() - 1, area.width, 1),
            )
        } else {
            (Rect::default(), area, Rect::default())
        };
        let track_len = usize::from(track.height);
        let thumb_len = track_len
            .saturating_mul(viewport_len)
            .checked_div(content_len.max(1))
            .unwrap_or_default()
            .max(1)
            .min(track_len);
        let thumb_travel = track_len.saturating_sub(thumb_len);
        let thumb_start = thumb_travel
            .saturating_mul(offset)
            .checked_div(max_offset)
            .unwrap_or_default();
        let thumb = Rect::new(
            track.x,
            track
                .y
                .saturating_add(thumb_start.min(u16::MAX as usize) as u16),
            track.width,
            thumb_len.min(u16::MAX as usize) as u16,
        );
        Self {
            area,
            start,
            track,
            thumb,
            end,
            content_len,
            viewport_len,
            offset,
            max_offset,
        }
    }

    pub const fn is_active(self) -> bool {
        self.max_offset > 0 && self.track.height > 0
    }

    pub fn pointer_down(
        self,
        position: Position,
        interaction: &mut TranscriptScrollbarInteraction,
    ) -> (bool, Option<usize>) {
        if !self.is_active() || !self.area.contains(position) {
            return (false, None);
        }
        if self.start.contains(position) {
            interaction.release();
            return (true, Some(self.offset.saturating_sub(3)));
        }
        if self.end.contains(position) {
            interaction.release();
            return (
                true,
                Some(self.offset.saturating_add(3).min(self.max_offset)),
            );
        }
        if self.thumb.contains(position) {
            interaction.grab_row = Some(position.y.saturating_sub(self.thumb.y));
            return (true, None);
        }
        if self.track.contains(position) {
            interaction.release();
            let pointer_row = usize::from(position.y.saturating_sub(self.track.y));
            let thumb_len = usize::from(self.thumb.height);
            let thumb_start = pointer_row.saturating_sub(thumb_len / 2);
            return (true, Some(self.offset_for_thumb_start(thumb_start)));
        }
        (true, None)
    }

    pub fn pointer_drag(
        self,
        row: u16,
        interaction: TranscriptScrollbarInteraction,
    ) -> Option<usize> {
        let grab_row = usize::from(interaction.grab_row?);
        if !self.is_active() {
            return None;
        }
        let last_row = self.track.bottom().saturating_sub(1);
        let pointer_row = usize::from(row.clamp(self.track.y, last_row) - self.track.y);
        Some(self.offset_for_thumb_start(pointer_row.saturating_sub(grab_row)))
    }

    fn offset_for_thumb_start(self, thumb_start: usize) -> usize {
        let thumb_travel = usize::from(self.track.height.saturating_sub(self.thumb.height));
        self.max_offset
            .saturating_mul(thumb_start.min(thumb_travel))
            .checked_div(thumb_travel)
            .unwrap_or_default()
    }
}

pub fn centered_width(area: Rect, max_width: u16) -> Rect {
    let width = area.width.min(max_width);
    Rect::new(
        area.x + area.width.saturating_sub(width) / 2,
        area.y,
        width,
        area.height,
    )
}

pub fn expanded_header(area: Rect) -> bool {
    area.width >= EXPANDED_HEADER_MIN_WIDTH && area.height >= EXPANDED_HEADER_MIN_TERMINAL_HEIGHT
}

pub fn browser_has_detail(width: u16) -> bool {
    width >= BROWSER_TWO_COLUMN_MIN_WIDTH
}

pub const fn effective_compact(user_compact: bool, rows: u16) -> bool {
    user_compact || rows <= AUTO_COMPACT_MAX_ROWS
}

pub const fn short_terminal(rows: u16) -> bool {
    rows <= SHORT_TERMINAL_ROWS
}

pub const fn scene_footer_height(rows: u16) -> u16 {
    if short_terminal(rows) {
        0
    } else {
        SCENE_FOOTER_HEIGHT
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn canvas_and_reading_content_share_one_bounded_contract() {
        let area = Rect::new(0, 0, 244, 71);
        let canvas = canvas(area);
        let content = content(canvas);
        assert_eq!(canvas, Rect::new(32, 1, PRODUCT_MAX_WIDTH, 69));
        assert_eq!(content, canvas);
    }

    #[test]
    fn transcript_geometry_keeps_content_and_scrollbar_on_one_reading_axis() {
        for (width, content_x, gap_width) in [
            (40, 1, 0),
            (80, 1, 0),
            (120, 1, 0),
            (160, 20, 1),
            (244, 62, 1),
        ] {
            let geometry = TranscriptGeometry::compute(Rect::new(0, 3, width, 20));
            assert_eq!(geometry.content.x, content_x);
            assert_eq!(geometry.leading_chrome.right(), geometry.content.x);
            assert_eq!(geometry.scrollbar_gap.x, geometry.content.right());
            assert_eq!(geometry.scrollbar_gap.width, gap_width);
            assert_eq!(geometry.scrollbar.x, geometry.scrollbar_gap.right());
            assert_eq!(geometry.scrollbar.width, 1);
            assert_eq!(geometry.scrollbar.y, geometry.content.y);
            assert_eq!(geometry.scrollbar.height, geometry.content.height);
            assert!(geometry.content.right() <= geometry.viewport.right());
            assert!(geometry.scrollbar.right() <= geometry.viewport.right());
            assert!(geometry.viewport.contains(geometry.content.as_position()));
            assert!(geometry.viewport.contains(geometry.scrollbar.as_position()));
        }
    }

    #[test]
    fn transcript_scrollbar_owns_render_and_pointer_geometry() {
        let scrollbar = TranscriptScrollbar::compute(Rect::new(141, 3, 1, 12), 120, 20, 50);
        assert!(scrollbar.is_active());
        assert_eq!(scrollbar.start, Rect::new(141, 3, 1, 1));
        assert_eq!(scrollbar.track, Rect::new(141, 4, 1, 10));
        assert_eq!(scrollbar.end, Rect::new(141, 14, 1, 1));
        assert!(scrollbar.track.contains(scrollbar.thumb.as_position()));

        let mut interaction = TranscriptScrollbarInteraction::default();
        let (handled, next) = scrollbar.pointer_down(Position::new(141, 3), &mut interaction);
        assert!(handled);
        assert_eq!(next, Some(47));
        let (handled, next) = scrollbar.pointer_down(Position::new(141, 14), &mut interaction);
        assert!(handled);
        assert_eq!(next, Some(53));

        let (handled, next) =
            scrollbar.pointer_down(scrollbar.thumb.as_position(), &mut interaction);
        assert!(handled);
        assert_eq!(next, None);
        assert!(interaction.is_dragging());
        assert_eq!(scrollbar.pointer_drag(0, interaction), Some(0));
        assert_eq!(
            scrollbar.pointer_drag(u16::MAX, interaction),
            Some(scrollbar.max_offset)
        );
        assert!(interaction.release());
        assert!(!interaction.is_dragging());
    }

    #[test]
    fn transcript_scrollbar_track_clicks_jump_and_clamp() {
        let scrollbar = TranscriptScrollbar::compute(Rect::new(10, 5, 1, 20), 500, 40, 230);
        let mut interaction = TranscriptScrollbarInteraction::default();
        let (_, top) = scrollbar.pointer_down(scrollbar.track.as_position(), &mut interaction);
        let (_, middle) = scrollbar.pointer_down(
            Position::new(
                scrollbar.track.x,
                scrollbar.track.y + scrollbar.track.height / 2,
            ),
            &mut interaction,
        );
        let (_, bottom) = scrollbar.pointer_down(
            Position::new(scrollbar.track.x, scrollbar.track.bottom() - 1),
            &mut interaction,
        );
        assert_eq!(top, Some(0));
        assert!(middle.is_some_and(|offset| offset > 0 && offset < scrollbar.max_offset));
        assert_eq!(bottom, Some(scrollbar.max_offset));

        let inactive = TranscriptScrollbar::compute(Rect::new(10, 5, 1, 20), 20, 40, 0);
        let (handled, next) = inactive.pointer_down(Position::new(10, 10), &mut interaction);
        assert!(!handled);
        assert_eq!(next, None);
    }

    #[test]
    fn compact_is_derived_from_user_choice_and_current_rows_only() {
        assert!(effective_compact(false, AUTO_COMPACT_MAX_ROWS));
        assert!(!effective_compact(false, AUTO_COMPACT_MAX_ROWS + 1));
        assert!(effective_compact(true, AUTO_COMPACT_MAX_ROWS + 1));
        assert_eq!(scene_footer_height(SHORT_TERMINAL_ROWS), 0);
        assert_eq!(
            scene_footer_height(SHORT_TERMINAL_ROWS + 1),
            SCENE_FOOTER_HEIGHT
        );
    }

    #[test]
    fn compact_breakpoints_are_derived_from_owned_geometry() {
        assert_eq!(BROWSER_TWO_COLUMN_MIN_WIDTH, 94);
        assert_eq!(EXPANDED_HEADER_MIN_WIDTH, 96);
        assert!(!browser_has_detail(BROWSER_TWO_COLUMN_MIN_WIDTH - 1));
        assert!(browser_has_detail(BROWSER_TWO_COLUMN_MIN_WIDTH));
    }

    #[test]
    fn transcript_indents_are_relational_and_width_locked() {
        assert_eq!(TRANSCRIPT_ROLE_PREFIX_WIDTH, 10);
        assert_eq!(TRANSCRIPT_FULL_ROLE_MIN_WIDTH, 72);
        assert_eq!(TRANSCRIPT_TOOL_GUTTER_WIDTH, TRANSCRIPT_ROLE_MARK_WIDTH);
        assert_eq!(TRANSCRIPT_FINAL_GAP, TRANSCRIPT_BLOCK_GAP);
        assert_eq!(
            transcript_content(Rect::new(10, 3, PRODUCT_MAX_WIDTH, 20)),
            Rect::new(40, 3, TRANSCRIPT_READING_MAX_WIDTH, 20)
        );
    }

    #[test]
    fn product_surfaces_do_not_reintroduce_bare_layout_breakpoints() {
        let sources = [
            ("app/render.rs", include_str!("app/render.rs")),
            ("prerequisite.rs", include_str!("prerequisite.rs")),
            ("product_header.rs", include_str!("product_header.rs")),
        ];
        for (path, source) in sources {
            let production = source.split("#[cfg(test)]").next().unwrap_or(source);
            for forbidden in [
                ".min(180)",
                ".min(200)",
                ">= 120",
                ">= 96",
                "< 96",
                ".clamp(1, 5)",
                "centered(area, 78, 26)",
                "centered(area, 76, 24)",
                "centered(area, 104, 32)",
                ".min(88)",
                ".clamp(1, 8)",
                "{category:<12}",
            ] {
                assert!(
                    !production.contains(forbidden),
                    "{path} contains bare product layout number {forbidden:?}"
                );
            }
            for bare_length in 0..=9 {
                let forbidden = format!("Constraint::Length({bare_length})");
                assert!(
                    !production.contains(&forbidden),
                    "{path} contains bare layout constraint {forbidden:?}"
                );
            }
            if path != "app/render.rs" {
                continue;
            }
            for (line_number, line) in production.lines().enumerate() {
                let data_format_only =
                    line.contains("viewer.hash") || line.contains("viewer.lines");
                if data_format_only {
                    continue;
                }
                for number in 1..=200 {
                    for forbidden in [
                        format!(".min({number})"),
                        format!(".max({number})"),
                        format!(".clamp({number},"),
                        format!("saturating_add({number})"),
                        format!("saturating_sub({number})"),
                        format!("Constraint::Min({number})"),
                        format!(":<{number}"),
                    ] {
                        assert!(
                            !line.contains(&forbidden),
                            "{path}:{} contains bare layout number {forbidden:?}",
                            line_number + 1
                        );
                    }
                }
            }
        }
    }
}
