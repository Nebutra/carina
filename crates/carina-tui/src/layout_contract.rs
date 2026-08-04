//! Product-wide terminal geometry shared by setup and conversation surfaces.

use ratatui::layout::{Margin, Rect};

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

pub const CONVERSATION_HEADER_HEIGHT: u16 = COMPACT_HEADER_HEIGHT;
pub const CONVERSATION_MIN_TRANSCRIPT_HEIGHT: u16 = 4;
pub const COMPOSER_MIN_HEIGHT: u16 = 1;
pub const COMPOSER_MAX_HEIGHT: u16 = 5;
pub const COMPOSER_CHROME_HEIGHT: u16 = 2;
pub const TRANSCRIPT_HORIZONTAL_INSET: u16 = 1;
pub const TRANSCRIPT_ROLE_MARK_WIDTH: usize = 2;
pub const TRANSCRIPT_ROLE_LABEL_WIDTH: usize = 6;
pub const TRANSCRIPT_ROLE_GAP_WIDTH: usize = 2;
pub const TRANSCRIPT_ROLE_PREFIX_WIDTH: usize =
    TRANSCRIPT_ROLE_MARK_WIDTH + TRANSCRIPT_ROLE_LABEL_WIDTH + TRANSCRIPT_ROLE_GAP_WIDTH;
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
pub const CHANGES_POPUP: (u16, u16) = (104, 32);
pub const SPLIT_PANE_MIN_WIDTH: u16 = 72;

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
    let width = area
        .width
        .saturating_sub(TRANSCRIPT_HORIZONTAL_INSET * 2)
        .min(TRANSCRIPT_READING_MAX_WIDTH);
    Rect::new(
        area.x + area.width.saturating_sub(width) / 2,
        area.y,
        width,
        area.height,
    )
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
