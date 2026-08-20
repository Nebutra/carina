//! Product-owned terminal glyphs and their compatibility policy.
//!
//! Every renderer chrome mark is selected here so its modern and fallback
//! forms can be width-locked together. Common box/block characters are used
//! only where mainstream monospace fonts cover them; fragile miscellaneous
//! symbols are deliberately avoided. ASCII mode uses familiar punctuation so
//! missing fonts cannot render tofu. Braille animation falls back to a
//! one-cell ASCII cycle, and block waterlines fall back to `#`/`.` cells.
//!
//! `GlyphPreference` is persisted product state, while `GlyphMode` is the
//! effective render tier. Auto mode never guesses whether a Nerd Font is
//! installed. Outer product chrome is rounded in Unicode/Nerd modes and plain
//! in ASCII mode. Markdown tables are the sole sharp-junction surface because
//! rounded tee and cross junctions do not exist.

use std::env;
use std::fmt;
use std::time::Duration;

use ratatui::widgets::BorderType;
#[cfg(test)]
use unicode_width::UnicodeWidthStr;

use crate::frame_scheduler::{
    ACTIVITY_TICK_INTERVAL, STATUS_TICK_INTERVAL, reduced_motion_enabled,
};

pub const GLYPH_MODE_ENV: &str = "CARINA_TUI_GLYPHS";
pub const ASCII_MODE_ENV: &str = "CARINA_ASCII";

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash)]
pub enum GlyphPreference {
    #[default]
    Auto,
    Unicode,
    Nerd,
    Ascii,
}

impl GlyphPreference {
    pub const ALL: [Self; 4] = [Self::Auto, Self::Unicode, Self::Nerd, Self::Ascii];

    pub fn parse(value: &str) -> Option<Self> {
        match value.trim().to_ascii_lowercase().as_str() {
            "auto" => Some(Self::Auto),
            "unicode" => Some(Self::Unicode),
            "nerd" => Some(Self::Nerd),
            "ascii" => Some(Self::Ascii),
            _ => None,
        }
    }

    pub const fn as_config_value(self) -> &'static str {
        match self {
            Self::Auto => "auto",
            Self::Unicode => "unicode",
            Self::Nerd => "nerd",
            Self::Ascii => "ascii",
        }
    }

    pub const fn next(self) -> Self {
        match self {
            Self::Auto => Self::Unicode,
            Self::Unicode => Self::Nerd,
            Self::Nerd => Self::Ascii,
            Self::Ascii => Self::Auto,
        }
    }

    pub fn resolve(
        self,
        environment: GlyphEnvironment<'_>,
    ) -> Result<ResolvedGlyphs, GlyphResolutionError> {
        resolve_glyphs(self, environment)
    }
}

impl fmt::Display for GlyphPreference {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_config_value())
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum GlyphMode {
    Unicode,
    Nerd,
    Ascii,
}

impl GlyphMode {
    pub const ALL: [Self; 3] = [Self::Unicode, Self::Nerd, Self::Ascii];

    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Unicode => "unicode",
            Self::Nerd => "nerd",
            Self::Ascii => "ascii",
        }
    }

    /// Compatibility entry point for composition code that has not yet
    /// adopted persisted preferences. New code should call
    /// [`GlyphPreference::resolve`] and handle the diagnostic.
    pub fn detect() -> Self {
        match ResolvedGlyphs::detect(GlyphPreference::Auto) {
            Ok(resolved) => resolved.mode,
            Err(error) => {
                eprintln!("carina: {error}");
                Self::Ascii
            }
        }
    }

    /// Compatibility adapter retained while callers move to the typed
    /// resolver. `NO_COLOR` is deliberately ignored: color and font coverage
    /// are independent terminal capabilities.
    pub fn from_environment(
        override_value: Option<&str>,
        ascii_value: Option<&str>,
        _no_color: bool,
        term: Option<&str>,
    ) -> Self {
        GlyphPreference::Auto
            .resolve(GlyphEnvironment {
                override_value,
                ascii_value,
                term,
                native_windows: false,
                modern_windows_terminal: false,
            })
            .map_or(Self::Ascii, |resolved| resolved.mode)
    }
}

impl fmt::Display for GlyphMode {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct GlyphEnvironment<'a> {
    pub override_value: Option<&'a str>,
    pub ascii_value: Option<&'a str>,
    pub term: Option<&'a str>,
    pub native_windows: bool,
    pub modern_windows_terminal: bool,
}

impl GlyphEnvironment<'_> {
    pub const fn neutral() -> Self {
        Self {
            override_value: None,
            ascii_value: None,
            term: None,
            native_windows: false,
            modern_windows_terminal: false,
        }
    }
}

impl Default for GlyphEnvironment<'_> {
    fn default() -> Self {
        Self::neutral()
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum GlyphSource {
    EnvironmentOverride,
    LegacyAsciiEnvironment,
    PersistedPreference,
    AutomaticLegacyTerminal,
    AutomaticDefault,
}

impl GlyphSource {
    pub const fn is_environment_override(self) -> bool {
        matches!(
            self,
            Self::EnvironmentOverride | Self::LegacyAsciiEnvironment
        )
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ResolvedGlyphs {
    pub preference: GlyphPreference,
    pub mode: GlyphMode,
    pub source: GlyphSource,
}

pub type GlyphResolution = ResolvedGlyphs;

impl ResolvedGlyphs {
    pub fn detect(preference: GlyphPreference) -> Result<Self, GlyphResolutionError> {
        let override_value = env::var(GLYPH_MODE_ENV).ok();
        let ascii_value = env::var(ASCII_MODE_ENV).ok();
        let term = env::var("TERM").ok();
        preference.resolve(GlyphEnvironment {
            override_value: override_value.as_deref(),
            ascii_value: ascii_value.as_deref(),
            term: term.as_deref(),
            native_windows: cfg!(windows),
            modern_windows_terminal: modern_windows_terminal_detected(),
        })
    }

    pub const fn glyphs(self) -> Glyphs {
        Glyphs::new(self.mode)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GlyphResolutionError {
    value: String,
}

impl GlyphResolutionError {
    pub fn value(&self) -> &str {
        &self.value
    }
}

impl fmt::Display for GlyphResolutionError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            formatter,
            "invalid {GLYPH_MODE_ENV} value {:?}; expected auto, unicode, nerd, or ascii",
            self.value
        )
    }
}

impl std::error::Error for GlyphResolutionError {}

pub fn resolve_glyphs(
    persisted: GlyphPreference,
    environment: GlyphEnvironment<'_>,
) -> Result<ResolvedGlyphs, GlyphResolutionError> {
    if let Some(value) = environment.override_value {
        let preference = GlyphPreference::parse(value).ok_or_else(|| GlyphResolutionError {
            value: value.to_owned(),
        })?;
        return Ok(resolve_preference(
            preference,
            environment,
            GlyphSource::EnvironmentOverride,
        ));
    }
    if environment.ascii_value.is_some_and(env_flag_enabled) {
        return Ok(ResolvedGlyphs {
            preference: GlyphPreference::Ascii,
            mode: GlyphMode::Ascii,
            source: GlyphSource::LegacyAsciiEnvironment,
        });
    }
    Ok(resolve_preference(
        persisted,
        environment,
        GlyphSource::PersistedPreference,
    ))
}

fn resolve_preference(
    preference: GlyphPreference,
    environment: GlyphEnvironment<'_>,
    explicit_source: GlyphSource,
) -> ResolvedGlyphs {
    let (mode, source) = match preference {
        GlyphPreference::Unicode => (GlyphMode::Unicode, explicit_source),
        GlyphPreference::Nerd => (GlyphMode::Nerd, explicit_source),
        GlyphPreference::Ascii => (GlyphMode::Ascii, explicit_source),
        GlyphPreference::Auto => {
            let legacy_terminal = environment
                .term
                .is_some_and(|value| value.eq_ignore_ascii_case("dumb"))
                || (environment.native_windows && !environment.modern_windows_terminal);
            let mode = if legacy_terminal {
                GlyphMode::Ascii
            } else {
                GlyphMode::Unicode
            };
            let source = if explicit_source == GlyphSource::EnvironmentOverride {
                GlyphSource::EnvironmentOverride
            } else if legacy_terminal {
                GlyphSource::AutomaticLegacyTerminal
            } else {
                GlyphSource::AutomaticDefault
            };
            (mode, source)
        }
    };
    ResolvedGlyphs {
        preference,
        mode,
        source,
    }
}

fn modern_windows_terminal_detected() -> bool {
    env::var_os("WT_SESSION").is_some()
        || env::var("TERM_PROGRAM").ok().is_some_and(|value| {
            matches!(
                value.trim().to_ascii_lowercase().as_str(),
                "alacritty" | "ghostty" | "kitty" | "mintty" | "vscode" | "wezterm"
            )
        })
}

fn env_flag_enabled(value: &str) -> bool {
    matches!(
        value.trim().to_ascii_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

#[derive(Debug, Clone, Copy)]
struct GlyphSet {
    unicode: &'static str,
    nerd: &'static str,
    ascii: &'static str,
    #[cfg(test)]
    width: usize,
}

impl GlyphSet {
    const fn new(
        unicode: &'static str,
        nerd: &'static str,
        ascii: &'static str,
        _width: usize,
    ) -> Self {
        Self {
            unicode,
            nerd,
            ascii,
            #[cfg(test)]
            width: _width,
        }
    }

    fn get(self, mode: GlyphMode) -> &'static str {
        match mode {
            GlyphMode::Unicode => self.unicode,
            GlyphMode::Nerd => self.nerd,
            GlyphMode::Ascii => self.ascii,
        }
    }
}

// Prompt and state marks favor broadly covered shapes; punctuation fallbacks
// preserve their exact footprint when a terminal cannot render them.
const PROMPT: GlyphSet = GlyphSet::new("❯ ", "\u{f054} ", "> ", 2);
const SUCCESS: GlyphSet = GlyphSet::new("✓", "\u{f00c}", "+", 1);
const FAILURE: GlyphSet = GlyphSet::new("✗", "\u{f00d}", "x", 1);
const FAILURE_PREFIX: GlyphSet = GlyphSet::new("✗ ", "\u{f00d} ", "x ", 2);
const WARNING: GlyphSet = GlyphSet::new("!", "\u{f071}", "!", 1);
const WARNING_PREFIX: GlyphSet = GlyphSet::new("! ", "\u{f071} ", "! ", 2);
const BULLET: GlyphSet = GlyphSet::new("•", "\u{f111}", "*", 1);
const BULLET_PREFIX: GlyphSet = GlyphSet::new("• ", "\u{f111} ", "* ", 2);
const READY: GlyphSet = GlyphSet::new("●", "\u{f111}", "*", 1);
const EMPTY: GlyphSet = GlyphSet::new("○", "\u{f10c}", "o", 1);
const DIAMOND_SOLID: GlyphSet = GlyphSet::new("◆", "◆", "#", 1);
const DIAMOND_OPEN: GlyphSet = GlyphSet::new("◇", "◇", "+", 1);
const UNAVAILABLE: GlyphSet = GlyphSet::new("–", "–", "-", 1);
const MISSING: GlyphSet = GlyphSet::new("—", "—", "-", 1);

// Selection, hierarchy, and navigation marks include any following space in
// the contract so changing modes never moves adjacent labels.
const SELECTED: GlyphSet = GlyphSet::new("▌ ", "\u{f105} ", "> ", 2);
const SELECTED_CELL: GlyphSet = GlyphSet::new("▌", "\u{f105}", ">", 1);
const TREE: GlyphSet = GlyphSet::new("│ ", "│ ", "| ", 2);
const SCROLL_TRACK: GlyphSet = GlyphSet::new("│", "│", "|", 1);
const SCROLL_THUMB: GlyphSet = GlyphSet::new("┃", "┃", "#", 1);
const ACCENT_RAIL: GlyphSet = GlyphSet::new("┃", "┃", "|", 1);
const RULE: GlyphSet = GlyphSet::new("─", "─", "-", 1);
const SEPARATOR: GlyphSet = GlyphSet::new("·", "·", "|", 1);
const DISCLOSURE_CLOSED: GlyphSet = GlyphSet::new("▸ ", "\u{f054} ", "> ", 2);
const DISCLOSURE_OPEN: GlyphSet = GlyphSet::new("▾ ", "\u{f078} ", "v ", 2);
const STEER: GlyphSet = GlyphSet::new("↳ ", "\u{f064} ", "> ", 2);
const UP: GlyphSet = GlyphSet::new("↑", "\u{f062}", "^", 1);
const DOWN: GlyphSet = GlyphSet::new("↓", "\u{f063}", "v", 1);
const NAV_VERTICAL: GlyphSet = GlyphSet::new("↑↓", "\u{f062}\u{f063}", "^v", 2);

// Dense block and prose marks degrade to conservative ASCII characters found
// in every terminal font, including legacy Windows consoles.
const BAR_FULL: GlyphSet = GlyphSet::new("█", "█", "#", 1);
const BAR_EMPTY: GlyphSet = GlyphSet::new("░", "░", ".", 1);
const QUOTE: GlyphSet = GlyphSet::new("│", "│", "|", 1);
const ELLIPSIS: GlyphSet = GlyphSet::new("…", "…", ".", 1);

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
const SPINNER: [GlyphSet; 8] = [
    GlyphSet::new("⠋", "⠋", "|", 1),
    GlyphSet::new("⠙", "⠙", "/", 1),
    GlyphSet::new("⠹", "⠹", "-", 1),
    GlyphSet::new("⠸", "⠸", "\\", 1),
    GlyphSet::new("⠼", "⠼", "|", 1),
    GlyphSet::new("⠴", "⠴", "/", 1),
    GlyphSet::new("⠦", "⠦", "-", 1),
    GlyphSet::new("⠧", "⠧", "\\", 1),
];

#[cfg(test)]
const ALL: &[GlyphSet] = &[
    PROMPT,
    SUCCESS,
    FAILURE,
    FAILURE_PREFIX,
    WARNING,
    WARNING_PREFIX,
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
    ACCENT_RAIL,
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
    reduced_motion: bool,
}

impl Glyphs {
    pub fn detect() -> Self {
        Self {
            mode: GlyphMode::detect(),
            reduced_motion: reduced_motion_enabled(),
        }
    }
    pub const fn new(mode: GlyphMode) -> Self {
        Self {
            mode,
            reduced_motion: false,
        }
    }
    pub const fn new_with_reduced_motion(mode: GlyphMode, reduced_motion: bool) -> Self {
        Self {
            mode,
            reduced_motion,
        }
    }
    pub const fn with_mode(self, mode: GlyphMode) -> Self {
        Self {
            mode,
            reduced_motion: self.reduced_motion,
        }
    }
    pub const fn reduced_motion(self) -> bool {
        self.reduced_motion
    }
    fn get(self, glyph: GlyphSet) -> &'static str {
        glyph.get(self.mode)
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
    pub fn failure_prefix(self) -> &'static str {
        self.get(FAILURE_PREFIX)
    }
    pub fn warning(self) -> &'static str {
        self.get(WARNING)
    }
    pub fn warning_prefix(self) -> &'static str {
        self.get(WARNING_PREFIX)
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
    pub fn accent_rail(self) -> &'static str {
        self.get(ACCENT_RAIL)
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
    /// Outer panels are rounded in Unicode/Nerd modes and plain in ASCII mode.
    /// Markdown tables separately use [`Self::table`] because junctions are sharp.
    pub fn outer_border_type(self) -> BorderType {
        match self.mode {
            GlyphMode::Unicode | GlyphMode::Nerd => BorderType::Rounded,
            GlyphMode::Ascii => BorderType::Plain,
        }
    }
    pub fn activity_spinner(self, elapsed_ms: u128) -> &'static str {
        self.spinner_for(elapsed_ms, ACTIVITY_TICK_INTERVAL)
    }
    pub fn status_spinner(self, elapsed_ms: u128) -> &'static str {
        self.spinner_for(elapsed_ms, STATUS_TICK_INTERVAL)
    }
    fn spinner_for(self, elapsed_ms: u128, interval: Duration) -> &'static str {
        if self.reduced_motion {
            return self.get(SPINNER[0]);
        }
        let period = interval.as_millis();
        self.get(SPINNER[(elapsed_ms / period) as usize % SPINNER.len()])
    }
    pub fn table(self) -> TableGlyphs {
        match self.mode {
            GlyphMode::Unicode | GlyphMode::Nerd => UNICODE_TABLE,
            GlyphMode::Ascii => ASCII_TABLE,
        }
    }

    /// Sanitize symbols embedded in free-flowing notices at their single view
    /// boundary. Structured chrome must use the semantic accessors above.
    pub fn sanitize_free_text<'a>(
        self,
        value: std::borrow::Cow<'a, str>,
    ) -> std::borrow::Cow<'a, str> {
        if self.mode != GlyphMode::Ascii || !value.chars().any(|ch| matches!(ch, '✓' | '✗' | '⚠'))
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
    use std::time::Duration;

    #[test]
    fn every_tier_has_the_same_declared_display_width() {
        for glyph in ALL {
            assert_eq!(
                UnicodeWidthStr::width(glyph.unicode),
                glyph.width,
                "{:?}",
                glyph.unicode
            );
            assert_eq!(
                UnicodeWidthStr::width(glyph.nerd),
                glyph.width,
                "{:?}",
                glyph.nerd
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
    fn preferences_parse_round_trip_and_cycle_in_product_order() {
        let mut preference = GlyphPreference::Auto;
        for expected in GlyphPreference::ALL {
            assert_eq!(preference, expected);
            assert_eq!(
                GlyphPreference::parse(expected.as_config_value()),
                Some(expected)
            );
            preference = preference.next();
        }
        assert_eq!(preference, GlyphPreference::Auto);
        assert_eq!(
            GlyphPreference::parse(" NERD "),
            Some(GlyphPreference::Nerd)
        );
        for invalid in ["", "modern", "legacy", "powerline", "emoji"] {
            assert_eq!(GlyphPreference::parse(invalid), None, "{invalid:?}");
        }
    }

    fn environment<'a>(
        override_value: Option<&'a str>,
        ascii_value: Option<&'a str>,
        term: Option<&'a str>,
    ) -> GlyphEnvironment<'a> {
        GlyphEnvironment {
            override_value,
            ascii_value,
            term,
            native_windows: false,
            modern_windows_terminal: false,
        }
    }

    #[test]
    fn trigger_policy_has_documented_precedence_and_sources() {
        let overridden = GlyphPreference::Ascii
            .resolve(environment(Some("nerd"), Some("1"), Some("dumb")))
            .unwrap();
        assert_eq!(
            overridden,
            ResolvedGlyphs {
                preference: GlyphPreference::Nerd,
                mode: GlyphMode::Nerd,
                source: GlyphSource::EnvironmentOverride,
            }
        );

        let legacy_ascii = GlyphPreference::Nerd
            .resolve(environment(None, Some("yes"), Some("xterm")))
            .unwrap();
        assert_eq!(legacy_ascii.preference, GlyphPreference::Ascii);
        assert_eq!(legacy_ascii.mode, GlyphMode::Ascii);
        assert_eq!(legacy_ascii.source, GlyphSource::LegacyAsciiEnvironment);

        let persisted = GlyphPreference::Nerd
            .resolve(environment(None, None, Some("dumb")))
            .unwrap();
        assert_eq!(persisted.preference, GlyphPreference::Nerd);
        assert_eq!(persisted.mode, GlyphMode::Nerd);
        assert_eq!(persisted.source, GlyphSource::PersistedPreference);

        let automatic = GlyphPreference::Auto
            .resolve(environment(None, None, Some("xterm-256color")))
            .unwrap();
        assert_eq!(automatic.mode, GlyphMode::Unicode);
        assert_eq!(automatic.source, GlyphSource::AutomaticDefault);

        let dumb = GlyphPreference::Auto
            .resolve(environment(None, None, Some("dumb")))
            .unwrap();
        assert_eq!(dumb.preference, GlyphPreference::Auto);
        assert_eq!(dumb.mode, GlyphMode::Ascii);
        assert_eq!(dumb.source, GlyphSource::AutomaticLegacyTerminal);
    }

    #[test]
    fn every_valid_environment_override_resolves_and_owns_the_result() {
        for (value, preference, mode) in [
            ("auto", GlyphPreference::Auto, GlyphMode::Unicode),
            ("unicode", GlyphPreference::Unicode, GlyphMode::Unicode),
            ("nerd", GlyphPreference::Nerd, GlyphMode::Nerd),
            ("ascii", GlyphPreference::Ascii, GlyphMode::Ascii),
        ] {
            let resolved = GlyphPreference::Ascii
                .resolve(environment(Some(value), Some("1"), Some("xterm")))
                .unwrap();
            assert_eq!(resolved.preference, preference, "{value}");
            assert_eq!(resolved.mode, mode, "{value}");
            assert_eq!(resolved.source, GlyphSource::EnvironmentOverride, "{value}");
        }
    }

    #[test]
    fn only_truthy_legacy_ascii_values_override_the_persisted_preference() {
        for value in ["1", "true", "yes", "on", " TRUE "] {
            let resolved = GlyphPreference::Nerd
                .resolve(environment(None, Some(value), Some("xterm")))
                .unwrap();
            assert_eq!(resolved.mode, GlyphMode::Ascii, "{value:?}");
            assert_eq!(resolved.source, GlyphSource::LegacyAsciiEnvironment);
        }
        for value in ["", "0", "false", "no", "off", "unexpected"] {
            let resolved = GlyphPreference::Nerd
                .resolve(environment(None, Some(value), Some("xterm")))
                .unwrap();
            assert_eq!(resolved.mode, GlyphMode::Nerd, "{value:?}");
            assert_eq!(resolved.source, GlyphSource::PersistedPreference);
        }
    }

    #[test]
    fn invalid_explicit_override_fails_closed_before_lower_precedence_inputs() {
        let error = GlyphPreference::Nerd
            .resolve(environment(Some("unknown"), Some("1"), Some("dumb")))
            .unwrap_err();
        assert_eq!(error.value(), "unknown");
        assert!(error.to_string().contains("auto, unicode, nerd, or ascii"));
    }

    #[test]
    fn explicit_environment_auto_owns_resolution_but_never_selects_nerd() {
        let modern = GlyphPreference::Nerd
            .resolve(environment(Some("auto"), None, Some("xterm")))
            .unwrap();
        assert_eq!(modern.preference, GlyphPreference::Auto);
        assert_eq!(modern.mode, GlyphMode::Unicode);
        assert_eq!(modern.source, GlyphSource::EnvironmentOverride);
        assert!(modern.source.is_environment_override());

        let dumb = GlyphPreference::Nerd
            .resolve(environment(Some("auto"), None, Some("dumb")))
            .unwrap();
        assert_eq!(dumb.mode, GlyphMode::Ascii);
        assert_eq!(dumb.source, GlyphSource::EnvironmentOverride);
    }

    #[test]
    fn auto_is_conservative_only_for_bounded_legacy_windows() {
        let legacy = GlyphPreference::Auto
            .resolve(GlyphEnvironment {
                native_windows: true,
                ..GlyphEnvironment::neutral()
            })
            .unwrap();
        assert_eq!(legacy.mode, GlyphMode::Ascii);
        assert_eq!(legacy.source, GlyphSource::AutomaticLegacyTerminal);

        let modern = GlyphPreference::Auto
            .resolve(GlyphEnvironment {
                native_windows: true,
                modern_windows_terminal: true,
                ..GlyphEnvironment::neutral()
            })
            .unwrap();
        assert_eq!(modern.mode, GlyphMode::Unicode);
        assert_eq!(modern.source, GlyphSource::AutomaticDefault);

        let explicit = GlyphPreference::Unicode
            .resolve(GlyphEnvironment {
                term: Some("dumb"),
                native_windows: true,
                modern_windows_terminal: false,
                ..GlyphEnvironment::neutral()
            })
            .unwrap();
        assert_eq!(explicit.mode, GlyphMode::Unicode);
        assert_eq!(explicit.source, GlyphSource::PersistedPreference);
    }

    #[test]
    fn no_color_is_orthogonal_to_glyph_resolution() {
        assert_eq!(
            GlyphMode::from_environment(None, None, true, Some("xterm-256color")),
            GlyphMode::Unicode
        );
        assert_eq!(
            GlyphMode::from_environment(Some("nerd"), None, true, Some("dumb")),
            GlyphMode::Nerd
        );
    }

    #[test]
    fn animated_and_interactive_variants_never_shift_following_content() {
        for mode in GlyphMode::ALL {
            let glyphs = Glyphs::new(mode);
            let spinner_widths = SPINNER.map(|frame| UnicodeWidthStr::width(frame.get(mode)));
            assert!(spinner_widths.into_iter().all(|width| width == 1));
            for elapsed in [0_u128, ACTIVITY_TICK_INTERVAL.as_millis(), 1_000] {
                let prefix = format!("{} ", glyphs.activity_spinner(elapsed));
                assert_eq!(UnicodeWidthStr::width(prefix.as_str()), 2);
                assert_eq!(
                    UnicodeWidthStr::width(prefix.as_str()),
                    UnicodeWidthStr::width(glyphs.disclosure_open())
                );
            }
            assert_eq!(UnicodeWidthStr::width(glyphs.disclosure_open()), 2);
            assert_eq!(UnicodeWidthStr::width(glyphs.disclosure_closed()), 2);
            assert_eq!(UnicodeWidthStr::width(glyphs.role_prefix()), 2);
            assert_eq!(UnicodeWidthStr::width(glyphs.tool_gutter()), 2);
            assert_eq!(UnicodeWidthStr::width(glyphs.accent_rail()), 1);
            assert_eq!(UnicodeWidthStr::width(glyphs.failure_prefix()), 2);
            assert_eq!(UnicodeWidthStr::width(glyphs.warning_prefix()), 2);
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
    fn spinner_classes_advance_at_their_scheduler_intervals() {
        for mode in GlyphMode::ALL {
            let glyphs = Glyphs::new(mode);
            assert_ne!(
                glyphs.activity_spinner(0),
                glyphs.activity_spinner(ACTIVITY_TICK_INTERVAL.as_millis())
            );
            assert_ne!(
                glyphs.status_spinner(0),
                glyphs.status_spinner(STATUS_TICK_INTERVAL.as_millis())
            );
            assert_eq!(glyphs.status_spinner(0), glyphs.status_spinner(33));
            assert_ne!(glyphs.activity_spinner(0), glyphs.activity_spinner(33));
        }
    }

    #[test]
    fn reduced_motion_spinner_is_static_and_width_stable() {
        for mode in GlyphMode::ALL {
            let glyphs = Glyphs::new_with_reduced_motion(mode, true);
            let first = glyphs.activity_spinner(0);
            assert_eq!(
                first,
                glyphs.activity_spinner(ACTIVITY_TICK_INTERVAL.as_millis())
            );
            assert_eq!(
                first,
                glyphs.status_spinner(STATUS_TICK_INTERVAL.as_millis())
            );
            assert_eq!(
                first,
                glyphs.activity_spinner(Duration::from_secs(30).as_millis())
            );
            assert_eq!(
                first,
                glyphs.status_spinner(Duration::from_secs(30).as_millis())
            );
            assert_eq!(UnicodeWidthStr::width(first), 1);
        }
    }

    #[test]
    fn free_text_fallback_is_one_lazy_funnel() {
        let original = std::borrow::Cow::Borrowed("saved ✓ failed ✗ caution ⚠");
        assert!(matches!(
            Glyphs::new(GlyphMode::Unicode).sanitize_free_text(original.clone()),
            std::borrow::Cow::Borrowed(_)
        ));
        assert!(matches!(
            Glyphs::new(GlyphMode::Nerd).sanitize_free_text(original.clone()),
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

    #[test]
    fn changing_tier_preserves_the_motion_preference() {
        let glyphs =
            Glyphs::new_with_reduced_motion(GlyphMode::Unicode, true).with_mode(GlyphMode::Nerd);
        assert_eq!(glyphs.mode, GlyphMode::Nerd);
        assert!(glyphs.reduced_motion());
        assert_eq!(glyphs.activity_spinner(0), glyphs.activity_spinner(1_000));
    }

    #[test]
    fn nerd_uses_product_border_policy_and_sharp_markdown_tables() {
        let unicode = Glyphs::new(GlyphMode::Unicode);
        let nerd = Glyphs::new(GlyphMode::Nerd);
        let ascii = Glyphs::new(GlyphMode::Ascii);
        assert_eq!(unicode.outer_border_type(), BorderType::Rounded);
        assert_eq!(nerd.outer_border_type(), BorderType::Rounded);
        assert_eq!(ascii.outer_border_type(), BorderType::Plain);
        assert_eq!(nerd.table().middle, ('├', '┼', '┤'));
        assert_eq!(ascii.table().middle, ('+', '+', '+'));
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
            "┼", "┤", "└", "┴", "┘", "\u{f00c}", "\u{f00d}", "\u{f054}", "\u{f062}", "\u{f063}",
            "\u{f064}", "\u{f071}", "\u{f078}", "\u{f105}", "\u{f10c}", "\u{f111}",
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
    fn ambient_glyph_detection_is_limited_to_composition_boundaries() {
        let source_root = Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
        let allowed = ["theme.rs", "bench_harness.rs"];
        for path in rust_sources(&source_root) {
            let name = path
                .file_name()
                .and_then(|name| name.to_str())
                .unwrap_or_default();
            if name == "glyphs.rs" {
                continue;
            }
            let source = fs::read_to_string(&path).expect("read Rust source");
            let production = production_source(&source);
            for detector in ["Glyphs::detect()", "GlyphMode::detect()"] {
                if production.contains(detector) {
                    assert!(
                        allowed.contains(&name),
                        "ambient {detector} bypasses the App-owned effective registry in {}",
                        path.display()
                    );
                }
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
