//! Product-owned terminal glyphs and their compatibility policy.
//!
//! Every renderer chrome mark is selected here so its modern and fallback
//! forms can be width-locked together. Common box/block characters are used
//! only where mainstream monospace fonts cover them; fragile miscellaneous
//! symbols are deliberately avoided. ASCII mode uses familiar punctuation so
//! missing fonts cannot render tofu. Braille animation falls back to a slower
//! one-cell ASCII cycle, and block waterlines fall back to `#`/`.` cells.
//!
//! Outer product chrome is rounded in Unicode mode and plain in ASCII mode.
//! Markdown tables are the sole sharp-junction surface because rounded tee and
//! cross junctions do not exist.

use std::env;

use ratatui::widgets::BorderType;
#[cfg(test)]
use unicode_width::UnicodeWidthStr;

pub const GLYPH_MODE_ENV: &str = "CARINA_TUI_GLYPHS";
pub const ASCII_MODE_ENV: &str = "CARINA_ASCII";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum GlyphMode {
    Unicode,
    Ascii,
}

impl GlyphMode {
    /// Detect glyph support in this precedence order: an explicit
    /// `CARINA_TUI_GLYPHS` override, `CARINA_ASCII`, `NO_COLOR`, then
    /// `TERM=dumb`. All other terminals receive Unicode chrome.
    pub fn detect() -> Self {
        Self::from_environment(
            env::var(GLYPH_MODE_ENV).ok().as_deref(),
            env::var(ASCII_MODE_ENV).ok().as_deref(),
            env::var_os("NO_COLOR").is_some(),
            env::var("TERM").ok().as_deref(),
        )
    }

    pub fn from_environment(
        override_value: Option<&str>,
        ascii_value: Option<&str>,
        no_color: bool,
        term: Option<&str>,
    ) -> Self {
        match override_value
            .map(str::trim)
            .map(str::to_ascii_lowercase)
            .as_deref()
        {
            Some("ascii" | "legacy") => Self::Ascii,
            Some("unicode" | "modern") => Self::Unicode,
            _ if ascii_value.is_some_and(env_flag_enabled) || no_color => Self::Ascii,
            _ if term.is_some_and(|value| value.eq_ignore_ascii_case("dumb")) => Self::Ascii,
            _ => Self::Unicode,
        }
    }
}

fn env_flag_enabled(value: &str) -> bool {
    matches!(
        value.trim().to_ascii_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

#[derive(Debug, Clone, Copy)]
struct GlyphPair {
    unicode: &'static str,
    ascii: &'static str,
    #[cfg(test)]
    width: usize,
}

impl GlyphPair {
    const fn new(unicode: &'static str, ascii: &'static str, _width: usize) -> Self {
        Self {
            unicode,
            ascii,
            #[cfg(test)]
            width: _width,
        }
    }

    fn get(self, mode: GlyphMode) -> &'static str {
        match mode {
            GlyphMode::Unicode => self.unicode,
            GlyphMode::Ascii => self.ascii,
        }
    }
}

// Prompt and state marks favor broadly covered shapes; punctuation fallbacks
// preserve their exact footprint when a terminal cannot render them.
const PROMPT: GlyphPair = GlyphPair::new("❯ ", "> ", 2);
const SUCCESS: GlyphPair = GlyphPair::new("✓", "+", 1);
const FAILURE: GlyphPair = GlyphPair::new("✗", "x", 1);
const WARNING: GlyphPair = GlyphPair::new("!", "!", 1);
const BULLET: GlyphPair = GlyphPair::new("•", "*", 1);
const BULLET_PREFIX: GlyphPair = GlyphPair::new("• ", "* ", 2);
const READY: GlyphPair = GlyphPair::new("●", "*", 1);
const EMPTY: GlyphPair = GlyphPair::new("○", "o", 1);
const DIAMOND_SOLID: GlyphPair = GlyphPair::new("◆", "#", 1);
const DIAMOND_OPEN: GlyphPair = GlyphPair::new("◇", "+", 1);
const UNAVAILABLE: GlyphPair = GlyphPair::new("–", "-", 1);
const MISSING: GlyphPair = GlyphPair::new("—", "-", 1);

// Selection, hierarchy, and navigation marks include any following space in
// the contract so changing modes never moves adjacent labels.
const SELECTED: GlyphPair = GlyphPair::new("▌ ", "> ", 2);
const SELECTED_CELL: GlyphPair = GlyphPair::new("▌", ">", 1);
const TREE: GlyphPair = GlyphPair::new("│ ", "| ", 2);
const SCROLL_TRACK: GlyphPair = GlyphPair::new("│", "|", 1);
const SCROLL_THUMB: GlyphPair = GlyphPair::new("┃", "#", 1);
const RULE: GlyphPair = GlyphPair::new("─", "-", 1);
const SEPARATOR: GlyphPair = GlyphPair::new("·", "|", 1);
const DISCLOSURE_CLOSED: GlyphPair = GlyphPair::new("▸ ", "> ", 2);
const DISCLOSURE_OPEN: GlyphPair = GlyphPair::new("▾ ", "v ", 2);
const STEER: GlyphPair = GlyphPair::new("↳ ", "> ", 2);
const UP: GlyphPair = GlyphPair::new("↑", "^", 1);
const DOWN: GlyphPair = GlyphPair::new("↓", "v", 1);
const NAV_VERTICAL: GlyphPair = GlyphPair::new("↑↓", "^v", 2);

// Dense block and prose marks degrade to conservative ASCII characters found
// in every terminal font, including legacy Windows consoles.
const BAR_FULL: GlyphPair = GlyphPair::new("█", "#", 1);
const BAR_EMPTY: GlyphPair = GlyphPair::new("░", ".", 1);
const QUOTE: GlyphPair = GlyphPair::new("│", "|", 1);
const ELLIPSIS: GlyphPair = GlyphPair::new("…", ".", 1);

#[derive(Debug, Clone, Copy)]
pub struct TableGlyphs {
    pub top: (char, char, char),
    pub middle: (char, char, char),
    pub bottom: (char, char, char),
    pub vertical: char,
}

const UNICODE_TABLE: TableGlyphs = TableGlyphs {
    top: ('┌', '┬', '┐'),
    middle: ('├', '┼', '┤'),
    bottom: ('└', '┴', '┘'),
    vertical: '│',
};
const ASCII_TABLE: TableGlyphs = TableGlyphs {
    top: ('+', '+', '+'),
    middle: ('+', '+', '+'),
    bottom: ('+', '+', '+'),
    vertical: '|',
};

// Braille gives smooth motion in modern fonts. The slower ASCII cycle uses
// only one-cell frames, preventing animation-induced label movement.
const SPINNER: [GlyphPair; 8] = [
    GlyphPair::new("⠋", "|", 1),
    GlyphPair::new("⠙", "/", 1),
    GlyphPair::new("⠹", "-", 1),
    GlyphPair::new("⠸", "\\", 1),
    GlyphPair::new("⠼", "|", 1),
    GlyphPair::new("⠴", "/", 1),
    GlyphPair::new("⠦", "-", 1),
    GlyphPair::new("⠧", "\\", 1),
];

#[cfg(test)]
const ALL: &[GlyphPair] = &[
    PROMPT,
    SUCCESS,
    FAILURE,
    WARNING,
    BULLET,
    BULLET_PREFIX,
    READY,
    EMPTY,
    DIAMOND_SOLID,
    DIAMOND_OPEN,
    UNAVAILABLE,
    MISSING,
    SELECTED,
    SELECTED_CELL,
    TREE,
    SCROLL_TRACK,
    SCROLL_THUMB,
    RULE,
    SEPARATOR,
    DISCLOSURE_CLOSED,
    DISCLOSURE_OPEN,
    STEER,
    UP,
    DOWN,
    NAV_VERTICAL,
    BAR_FULL,
    BAR_EMPTY,
    QUOTE,
    ELLIPSIS,
    SPINNER[0],
    SPINNER[1],
    SPINNER[2],
    SPINNER[3],
    SPINNER[4],
    SPINNER[5],
    SPINNER[6],
    SPINNER[7],
];

#[derive(Debug, Clone, Copy)]
pub struct Glyphs {
    pub mode: GlyphMode,
}

impl Glyphs {
    pub fn detect() -> Self {
        Self {
            mode: GlyphMode::detect(),
        }
    }
    pub const fn new(mode: GlyphMode) -> Self {
        Self { mode }
    }
    fn get(self, pair: GlyphPair) -> &'static str {
        pair.get(self.mode)
    }
    pub fn prompt(self) -> &'static str {
        self.get(PROMPT)
    }
    pub fn success(self) -> &'static str {
        self.get(SUCCESS)
    }
    pub fn failure(self) -> &'static str {
        self.get(FAILURE)
    }
    pub fn warning(self) -> &'static str {
        self.get(WARNING)
    }
    pub fn bullet(self) -> &'static str {
        self.get(BULLET)
    }
    pub fn bullet_prefix(self) -> &'static str {
        self.get(BULLET_PREFIX)
    }
    pub fn role_prefix(self) -> &'static str {
        self.get(BULLET_PREFIX)
    }
    pub fn ready(self) -> &'static str {
        self.get(READY)
    }
    pub fn empty(self) -> &'static str {
        self.get(EMPTY)
    }
    pub fn diamond_solid(self) -> &'static str {
        self.get(DIAMOND_SOLID)
    }
    pub fn diamond_open(self) -> &'static str {
        self.get(DIAMOND_OPEN)
    }
    pub fn unavailable(self) -> &'static str {
        self.get(UNAVAILABLE)
    }
    pub fn missing(self) -> &'static str {
        self.get(MISSING)
    }
    pub fn selected(self) -> &'static str {
        self.get(SELECTED)
    }
    pub fn selected_cell(self) -> &'static str {
        self.get(SELECTED_CELL)
    }
    pub fn tree(self) -> &'static str {
        self.get(TREE)
    }
    pub fn tool_gutter(self) -> &'static str {
        self.get(TREE)
    }
    pub fn scroll_track(self) -> &'static str {
        self.get(SCROLL_TRACK)
    }
    pub fn scroll_thumb(self) -> &'static str {
        self.get(SCROLL_THUMB)
    }
    pub fn rule(self) -> &'static str {
        self.get(RULE)
    }
    pub fn separator(self) -> &'static str {
        self.get(SEPARATOR)
    }
    pub fn disclosure_closed(self) -> &'static str {
        self.get(DISCLOSURE_CLOSED)
    }
    pub fn disclosure_open(self) -> &'static str {
        self.get(DISCLOSURE_OPEN)
    }
    pub fn steer(self) -> &'static str {
        self.get(STEER)
    }
    pub fn up(self) -> &'static str {
        self.get(UP)
    }
    pub fn down(self) -> &'static str {
        self.get(DOWN)
    }
    pub fn nav_vertical(self) -> &'static str {
        self.get(NAV_VERTICAL)
    }
    pub fn bar_full(self) -> &'static str {
        self.get(BAR_FULL)
    }
    pub fn bar_empty(self) -> &'static str {
        self.get(BAR_EMPTY)
    }
    pub fn quote(self) -> &'static str {
        self.get(QUOTE)
    }
    pub fn ellipsis(self) -> &'static str {
        self.get(ELLIPSIS)
    }
    /// Outer panels are rounded in Unicode mode and plain in ASCII mode.
    /// Markdown tables separately use [`Self::table`] because junctions are sharp.
    pub fn outer_border_type(self) -> BorderType {
        match self.mode {
            GlyphMode::Unicode => BorderType::Rounded,
            GlyphMode::Ascii => BorderType::Plain,
        }
    }
    pub fn spinner(self, elapsed_ms: u128) -> &'static str {
        let period = if self.mode == GlyphMode::Ascii {
            480
        } else {
            125
        };
        self.get(SPINNER[(elapsed_ms / period) as usize % SPINNER.len()])
    }
    pub fn table(self) -> TableGlyphs {
        match self.mode {
            GlyphMode::Unicode => UNICODE_TABLE,
            GlyphMode::Ascii => ASCII_TABLE,
        }
    }

    /// Sanitize symbols embedded in free-flowing notices at their single view
    /// boundary. Structured chrome must use the semantic accessors above.
    pub fn sanitize_free_text<'a>(
        self,
        value: std::borrow::Cow<'a, str>,
    ) -> std::borrow::Cow<'a, str> {
        if self.mode == GlyphMode::Unicode || !value.chars().any(|ch| matches!(ch, '✓' | '✗' | '⚠'))
        {
            return value;
        }
        std::borrow::Cow::Owned(value.replace('✓', "+").replace('✗', "x").replace('⚠', "!"))
    }
}

impl Default for Glyphs {
    fn default() -> Self {
        Self::new(GlyphMode::Unicode)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::path::{Path, PathBuf};

    #[test]
    fn every_fallback_has_the_same_display_width() {
        for glyph in ALL {
            assert_eq!(
                UnicodeWidthStr::width(glyph.unicode),
                glyph.width,
                "{:?}",
                glyph.unicode
            );
            assert_eq!(
                UnicodeWidthStr::width(glyph.ascii),
                glyph.width,
                "{:?}",
                glyph.ascii
            );
        }
        for table in [UNICODE_TABLE, ASCII_TABLE] {
            for glyph in [
                table.top.0,
                table.top.1,
                table.top.2,
                table.middle.0,
                table.middle.1,
                table.middle.2,
                table.bottom.0,
                table.bottom.1,
                table.bottom.2,
                table.vertical,
            ] {
                assert_eq!(unicode_width::UnicodeWidthChar::width(glyph), Some(1));
            }
        }
    }

    #[test]
    fn trigger_policy_has_documented_precedence_and_legacy_signals() {
        assert_eq!(
            GlyphMode::from_environment(Some("ascii"), None, false, Some("xterm")),
            GlyphMode::Ascii
        );
        assert_eq!(
            GlyphMode::from_environment(Some("unicode"), Some("1"), true, Some("dumb")),
            GlyphMode::Unicode
        );
        assert_eq!(
            GlyphMode::from_environment(None, None, false, Some("dumb")),
            GlyphMode::Ascii
        );
        assert_eq!(
            GlyphMode::from_environment(None, None, false, Some("xterm-256color")),
            GlyphMode::Unicode
        );
        assert_eq!(
            GlyphMode::from_environment(None, Some("1"), false, Some("xterm")),
            GlyphMode::Ascii
        );
        assert_eq!(
            GlyphMode::from_environment(None, None, true, Some("xterm")),
            GlyphMode::Ascii
        );
    }

    #[test]
    fn animated_and_interactive_variants_never_shift_following_content() {
        for mode in [GlyphMode::Unicode, GlyphMode::Ascii] {
            let glyphs = Glyphs::new(mode);
            let spinner_widths = SPINNER.map(|frame| UnicodeWidthStr::width(frame.get(mode)));
            assert!(spinner_widths.into_iter().all(|width| width == 1));
            assert_eq!(UnicodeWidthStr::width(glyphs.disclosure_open()), 2);
            assert_eq!(UnicodeWidthStr::width(glyphs.disclosure_closed()), 2);
            assert_eq!(UnicodeWidthStr::width(glyphs.role_prefix()), 2);
            assert_eq!(UnicodeWidthStr::width(glyphs.tool_gutter()), 2);
            assert_eq!(
                UnicodeWidthStr::width(glyphs.selected()),
                UnicodeWidthStr::width("  ")
            );
            for status in [
                glyphs.success(),
                glyphs.failure(),
                glyphs.warning(),
                glyphs.ready(),
                glyphs.empty(),
                glyphs.diamond_solid(),
                glyphs.diamond_open(),
            ] {
                assert_eq!(UnicodeWidthStr::width(status), 1);
            }
        }
    }

    #[test]
    fn free_text_fallback_is_one_lazy_funnel() {
        let original = std::borrow::Cow::Borrowed("saved ✓ failed ✗ caution ⚠");
        assert!(matches!(
            Glyphs::new(GlyphMode::Unicode).sanitize_free_text(original.clone()),
            std::borrow::Cow::Borrowed(_)
        ));
        assert_eq!(
            Glyphs::new(GlyphMode::Ascii).sanitize_free_text(original),
            "saved + failed x caution !"
        );
        assert!(matches!(
            Glyphs::new(GlyphMode::Ascii)
                .sanitize_free_text(std::borrow::Cow::Borrowed("plain text")),
            std::borrow::Cow::Borrowed(_)
        ));
    }

    fn rust_sources(root: &Path) -> Vec<PathBuf> {
        let mut sources = Vec::new();
        for entry in fs::read_dir(root).expect("read Rust source directory") {
            let path = entry.expect("read source entry").path();
            if path.is_dir() {
                sources.extend(rust_sources(&path));
            } else if path.extension().is_some_and(|extension| extension == "rs") {
                sources.push(path);
            }
        }
        sources
    }

    fn production_source(source: &str) -> &str {
        source
            .rfind("\n#[cfg(test)]\n")
            .map_or(source, |offset| &source[..offset])
    }

    #[test]
    fn product_glyph_literals_have_one_source_owner() {
        let source_root = Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
        let forbidden = [
            "❯", "✓", "✗", "•", "●", "○", "◆", "◇", "–", "—", "▌", "│", "┃", "─", "·", "▸", "▾",
            "↳", "↑", "↓", "█", "░", "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "┌", "┬", "┐", "├",
            "┼", "┤", "└", "┴", "┘",
        ];
        for path in rust_sources(&source_root) {
            let name = path
                .file_name()
                .and_then(|name| name.to_str())
                .unwrap_or_default();
            if matches!(name, "glyphs.rs" | "i18n.rs" | "terminal_logo.rs") {
                continue;
            }
            let source = fs::read_to_string(&path).expect("read Rust source");
            let production = production_source(&source);
            assert!(
                !production.contains("glyphs.get("),
                "renderers must use semantic glyph functions, found raw lookup in {}",
                path.display()
            );
            for glyph in forbidden {
                assert!(
                    !production.contains(glyph),
                    "product glyph {glyph:?} must be selected through glyphs.rs, found in {}",
                    path.display()
                );
            }
        }
    }

    #[test]
    fn every_product_border_uses_the_shared_policy() {
        let source_root = Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
        for path in rust_sources(&source_root) {
            if path.file_name().is_some_and(|name| name == "glyphs.rs") {
                continue;
            }
            let source = fs::read_to_string(&path).expect("read Rust source");
            let production = production_source(&source);
            for (offset, _) in production.match_indices(".borders(") {
                let tail = &production[offset..];
                let close = tail.find(')').expect("border call closes");
                let continuation = tail[close + 1..].trim_start();
                assert!(
                    continuation.starts_with(".border_type(")
                        && continuation
                            .split_once(')')
                            .is_some_and(|(call, _)| call.contains(".outer_border_type(")),
                    "border at {} byte {offset} bypasses Glyphs::outer_border_type",
                    path.display()
                );
            }
        }
    }
}
