//! Terminal-native semantic styles.
//!
//! All product colors are owned here. Renderers consume semantic fields or
//! helpers so capability and terminal-polarity changes remain centralized.
#![allow(clippy::disallowed_methods)] // This module is the sole palette owner.

use std::env;
use std::io::IsTerminal;

use crate::glyphs::Glyphs;
use ratatui::style::{Color, Modifier, Style};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Polarity {
    Dark,
    Light,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum ColorLevel {
    None,
    Basic,
    Ansi256,
    TrueColor,
}

impl ColorLevel {
    pub fn detect() -> Self {
        Self::detect_from(
            env::var_os("NO_COLOR").is_some(),
            std::io::stdout().is_terminal(),
            &env::var("TERM").unwrap_or_default(),
            env::var("COLORTERM").ok().as_deref(),
            env::var("TERM_PROGRAM").ok().as_deref(),
            env::var_os("WT_SESSION").is_some(),
        )
    }

    fn detect_from(
        no_color: bool,
        is_tty: bool,
        term: &str,
        colorterm: Option<&str>,
        program: Option<&str>,
        windows_terminal: bool,
    ) -> Self {
        if no_color {
            return Self::None;
        }
        if !is_tty {
            return Self::TrueColor;
        }
        let term = term.to_ascii_lowercase();
        let program = program.unwrap_or_default().to_ascii_lowercase();
        let truecolor_host = windows_terminal
            || matches!(
                program.as_str(),
                "ghostty" | "iterm.app" | "wezterm" | "vscode"
            )
            || colorterm.is_some_and(|value| {
                matches!(value.to_ascii_lowercase().as_str(), "truecolor" | "24bit")
            });
        if truecolor_host {
            return Self::TrueColor;
        }
        if term.contains("256color") {
            // Multiplexers and remote shells commonly strip COLORTERM. A known
            // truecolor host is promoted above; otherwise retain the honest tier.
            return Self::Ansi256;
        }
        Self::Basic
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ThemePreference {
    Auto,
    Dark,
    Light,
}

impl ThemePreference {
    pub fn from_env() -> Self {
        Self::parse(&env::var("CARINA_THEME").unwrap_or_default())
    }

    fn parse(value: &str) -> Self {
        match value.to_ascii_lowercase().as_str() {
            "dark" => Self::Dark,
            "light" => Self::Light,
            _ => Self::Auto,
        }
    }
}

#[derive(Debug, Clone, Copy)]
pub struct Theme {
    pub no_color: bool,
    pub level: ColorLevel,
    pub polarity: Polarity,
    pub glyphs: Glyphs,
    pub background: Color,
    pub surface: Color,
    pub raised: Color,
    pub border: Color,
    pub text: Color,
    pub gray_dim: Color,
    pub gray: Color,
    pub gray_bright: Color,
    pub muted: Color,
    pub brand: Color,
    pub accent: Color,
    pub success: Color,
    pub warning: Color,
    pub danger: Color,
    pub user_message_bg: Color,
    pub selection_bg: Color,
    pub code_fg: Color,
    pub code_bg: Color,
    pub diff_add_fg: Color,
    pub diff_add_bg: Color,
    pub diff_add_gutter: Color,
    pub diff_remove_fg: Color,
    pub diff_remove_bg: Color,
    pub diff_remove_gutter: Color,
    pub thinking: Color,
    pub tool_pending: Color,
    pub tool_success: Color,
    pub tool_error: Color,
    pub spinner: Color,
    pub link: Color,
    pub markdown_heading: [(Color, Modifier); 6],
}

impl Theme {
    /// Compatibility constructor used by deterministic component tests.
    pub fn carina(no_color: bool) -> Self {
        let level = if no_color {
            ColorLevel::None
        } else {
            ColorLevel::TrueColor
        };
        Self::new(Polarity::Dark, level)
    }

    pub fn detected(background: Option<[u8; 3]>) -> Self {
        let preference = ThemePreference::from_env();
        let polarity = match preference {
            ThemePreference::Dark => Polarity::Dark,
            ThemePreference::Light => Polarity::Light,
            ThemePreference::Auto => background
                .map(classify_polarity)
                .or_else(polarity_from_colorfgbg)
                .unwrap_or(Polarity::Dark),
        };
        Self::new(polarity, ColorLevel::detect())
    }

    pub fn new(polarity: Polarity, level: ColorLevel) -> Self {
        let p = match polarity {
            Polarity::Dark => Palette::dark(),
            Polarity::Light => Palette::light(),
        };
        let mut theme = Self {
            no_color: level == ColorLevel::None,
            level,
            polarity,
            glyphs: Glyphs::detect(),
            background: Color::Reset,
            surface: rgb(p.surface),
            raised: rgb(p.raised),
            border: rgb(p.border),
            text: Color::Reset,
            gray_dim: rgb(p.gray_dim),
            gray: rgb(p.gray),
            gray_bright: rgb(p.gray_bright),
            muted: rgb(p.gray),
            brand: rgb(p.brand),
            accent: rgb(p.accent),
            success: rgb(p.success),
            warning: rgb(p.warning),
            danger: rgb(p.danger),
            user_message_bg: rgb(p.user_message_bg),
            selection_bg: rgb(p.selection_bg),
            code_fg: rgb(p.code_fg),
            code_bg: rgb(p.code_bg),
            diff_add_fg: rgb(p.diff_add_fg),
            diff_add_bg: rgb(p.diff_add_bg),
            diff_add_gutter: rgb(p.diff_add_gutter),
            diff_remove_fg: rgb(p.diff_remove_fg),
            diff_remove_bg: rgb(p.diff_remove_bg),
            diff_remove_gutter: rgb(p.diff_remove_gutter),
            thinking: rgb(p.gray_dim),
            tool_pending: rgb(p.warning),
            tool_success: rgb(p.success),
            tool_error: rgb(p.danger),
            spinner: rgb(p.accent),
            link: rgb(p.link),
            markdown_heading: [
                (rgb(p.accent), Modifier::BOLD | Modifier::UNDERLINED),
                (rgb(p.accent), Modifier::BOLD),
                (Color::Reset, Modifier::BOLD),
                (rgb(p.gray_bright), Modifier::BOLD),
                (rgb(p.gray_bright), Modifier::ITALIC),
                (rgb(p.gray), Modifier::ITALIC),
            ],
        };
        theme.quantize_all();
        theme
    }

    fn quantize_all(&mut self) {
        let level = self.level;
        for color in [
            &mut self.background,
            &mut self.surface,
            &mut self.raised,
            &mut self.border,
            &mut self.text,
            &mut self.gray_dim,
            &mut self.gray,
            &mut self.gray_bright,
            &mut self.muted,
            &mut self.brand,
            &mut self.accent,
            &mut self.success,
            &mut self.warning,
            &mut self.danger,
            &mut self.user_message_bg,
            &mut self.selection_bg,
            &mut self.code_fg,
            &mut self.code_bg,
            &mut self.diff_add_fg,
            &mut self.diff_add_bg,
            &mut self.diff_add_gutter,
            &mut self.diff_remove_fg,
            &mut self.diff_remove_bg,
            &mut self.diff_remove_gutter,
            &mut self.thinking,
            &mut self.tool_pending,
            &mut self.tool_success,
            &mut self.tool_error,
            &mut self.spinner,
            &mut self.link,
        ] {
            *color = quantize(*color, level);
        }
        for (color, _) in &mut self.markdown_heading {
            *color = quantize(*color, level);
        }
        if level <= ColorLevel::Basic {
            self.surface = Color::Reset;
            self.raised = Color::Reset;
            self.user_message_bg = Color::Reset;
            self.selection_bg = Color::Reset;
            self.code_bg = Color::Reset;
            self.diff_add_bg = Color::Reset;
            self.diff_add_gutter = Color::Reset;
            self.diff_remove_bg = Color::Reset;
            self.diff_remove_gutter = Color::Reset;
            self.gray_dim = Color::Reset;
            self.gray = Color::Reset;
            self.gray_bright = Color::Reset;
            self.muted = Color::Reset;
        }
    }

    pub fn muted(self) -> Style {
        polarity_safe(self.gray)
    }
    pub fn dim(self) -> Style {
        polarity_safe(self.gray_dim)
    }
    pub fn bright_muted(self) -> Style {
        polarity_safe(self.gray_bright)
    }
    pub fn heading(self, level: usize) -> Style {
        let (color, modifier) = self.markdown_heading[level.saturating_sub(1).min(5)];
        Style::default().fg(color).add_modifier(modifier)
    }
    pub fn diff_add(self) -> Style {
        self.diff_style(self.diff_add_fg, self.diff_add_bg)
    }
    pub fn diff_remove(self) -> Style {
        self.diff_style(self.diff_remove_fg, self.diff_remove_bg)
    }
    fn diff_style(self, fg: Color, bg: Color) -> Style {
        let style = Style::default().fg(fg);
        if self.level <= ColorLevel::Basic {
            style
        } else {
            style.bg(bg)
        }
    }
    pub fn focus(self) -> Style {
        Style::default()
            .fg(self.accent)
            .add_modifier(Modifier::BOLD)
    }
    pub fn selected(self) -> Style {
        if self.no_color || self.level == ColorLevel::Basic {
            Style::default().add_modifier(Modifier::BOLD | Modifier::REVERSED)
        } else {
            Style::default()
                .fg(self.text)
                .bg(self.selection_bg)
                .add_modifier(Modifier::BOLD)
        }
    }
    pub fn hovered(self) -> Style {
        if self.no_color {
            Style::default().add_modifier(Modifier::UNDERLINED)
        } else {
            Style::default()
                .fg(self.accent)
                .add_modifier(Modifier::UNDERLINED)
        }
    }
    pub fn action(self) -> Style {
        Style::default()
            .fg(self.accent)
            .add_modifier(Modifier::BOLD | Modifier::REVERSED)
    }
    pub fn keycap(self) -> Style {
        if self.no_color {
            Style::default().add_modifier(Modifier::BOLD | Modifier::REVERSED)
        } else {
            Style::default()
                .fg(self.accent)
                .add_modifier(Modifier::BOLD)
        }
    }

    pub fn transcript_user(self) -> Style {
        self.bright_muted().add_modifier(Modifier::BOLD)
    }
    pub fn transcript_user_band(self) -> Style {
        match self.user_message_bg {
            Color::Reset => Style::default(),
            bg => Style::default().bg(bg),
        }
    }
    pub fn transcript_assistant(self) -> Style {
        Style::default()
            .fg(self.accent)
            .add_modifier(Modifier::BOLD)
    }
    pub fn transcript_thinking(self) -> Style {
        self.dim().add_modifier(Modifier::DIM | Modifier::ITALIC)
    }
    pub fn transcript_tool(self) -> Style {
        Style::default()
            .fg(self.warning)
            .add_modifier(Modifier::BOLD)
    }
    pub fn transcript_tool_settled(self) -> Style {
        self.bright_muted().add_modifier(Modifier::BOLD)
    }
    pub fn transcript_danger(self) -> Style {
        Style::default()
            .fg(self.danger)
            .add_modifier(Modifier::BOLD)
    }
    pub fn transcript_metadata(self) -> Style {
        self.dim()
    }
    pub fn transcript_added(self) -> Style {
        Style::default().fg(self.accent)
    }
    pub fn transcript_removed(self) -> Style {
        Style::default().fg(self.danger)
    }
    pub fn transcript_link(self) -> Style {
        Style::default()
            .fg(self.accent)
            .add_modifier(Modifier::UNDERLINED)
    }
}

fn polarity_safe(color: Color) -> Style {
    if color == Color::Reset {
        Style::default().add_modifier(Modifier::DIM)
    } else {
        Style::default().fg(color)
    }
}

pub fn polarity_from_colorfgbg() -> Option<Polarity> {
    parse_colorfgbg(&env::var("COLORFGBG").ok()?)
}

fn parse_colorfgbg(value: &str) -> Option<Polarity> {
    let bg = value.rsplit([';', ':']).next()?.parse::<u8>().ok()?;
    Some(if bg < 8 {
        Polarity::Dark
    } else {
        Polarity::Light
    })
}

pub fn classify_polarity([r, g, b]: [u8; 3]) -> Polarity {
    let linear = |v: u8| {
        let v = f64::from(v) / 255.0;
        if v <= 0.04045 {
            v / 12.92
        } else {
            ((v + 0.055) / 1.055).powf(2.4)
        }
    };
    if 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b) > 0.5 {
        Polarity::Light
    } else {
        Polarity::Dark
    }
}

fn rgb([r, g, b]: [u8; 3]) -> Color {
    Color::Rgb(r, g, b)
}

pub fn quantize(color: Color, level: ColorLevel) -> Color {
    let Color::Rgb(r, g, b) = color else {
        return color;
    };
    match level {
        ColorLevel::TrueColor => color,
        ColorLevel::Ansi256 => Color::Indexed(nearest_xterm([r, g, b])),
        ColorLevel::Basic => basic_color([r, g, b]),
        ColorLevel::None => Color::Reset,
    }
}

fn nearest_xterm(target: [u8; 3]) -> u8 {
    let target = lab(target);
    (16_u16..=255)
        .min_by_key(|&index| {
            let candidate = lab(xterm_rgb(index as u8));
            ((target[0] - candidate[0]).powi(2)
                + (target[1] - candidate[1]).powi(2)
                + (target[2] - candidate[2]).powi(2)) as u64
        })
        .unwrap_or(16) as u8
}

fn xterm_rgb(index: u8) -> [u8; 3] {
    if index >= 232 {
        let v = 8 + (index - 232) * 10;
        return [v, v, v];
    }
    let n = index - 16;
    let channel = |v: u8| if v == 0 { 0 } else { 55 + 40 * v };
    [channel(n / 36), channel((n / 6) % 6), channel(n % 6)]
}

fn lab([r, g, b]: [u8; 3]) -> [f64; 3] {
    let linear = |v: u8| {
        let v = f64::from(v) / 255.0;
        if v <= 0.04045 {
            v / 12.92
        } else {
            ((v + 0.055) / 1.055).powf(2.4)
        }
    };
    let (r, g, b) = (linear(r), linear(g), linear(b));
    let (x, y, z) = (
        (0.4124 * r + 0.3576 * g + 0.1805 * b) / 0.95047,
        (0.2126 * r + 0.7152 * g + 0.0722 * b),
        (0.0193 * r + 0.1192 * g + 0.9505 * b) / 1.08883,
    );
    let f = |v: f64| {
        if v > 0.008856 {
            v.cbrt()
        } else {
            7.787 * v + 16.0 / 116.0
        }
    };
    [
        116.0 * f(y) - 16.0,
        500.0 * (f(x) - f(y)),
        200.0 * (f(y) - f(z)),
    ]
}

fn basic_color([r, g, b]: [u8; 3]) -> Color {
    let max = r.max(g).max(b);
    let min = r.min(g).min(b);
    if max.saturating_sub(min) < 35 {
        return Color::Reset;
    }
    if r == max && r.saturating_sub(g) > 70 && g.abs_diff(b) < 35 {
        Color::Red
    } else if r == max && b > g {
        Color::Magenta
    } else if r == max && g > b {
        Color::Yellow
    } else if g == max && g.abs_diff(b) < 35 {
        Color::Cyan
    } else if g == max {
        Color::Green
    } else if r > g {
        Color::Magenta
    } else {
        Color::Cyan
    }
}

struct Palette {
    surface: [u8; 3],
    raised: [u8; 3],
    border: [u8; 3],
    gray_dim: [u8; 3],
    gray: [u8; 3],
    gray_bright: [u8; 3],
    brand: [u8; 3],
    accent: [u8; 3],
    success: [u8; 3],
    warning: [u8; 3],
    danger: [u8; 3],
    user_message_bg: [u8; 3],
    selection_bg: [u8; 3],
    code_fg: [u8; 3],
    code_bg: [u8; 3],
    diff_add_fg: [u8; 3],
    diff_add_bg: [u8; 3],
    diff_add_gutter: [u8; 3],
    diff_remove_fg: [u8; 3],
    diff_remove_bg: [u8; 3],
    diff_remove_gutter: [u8; 3],
    link: [u8; 3],
}
impl Palette {
    fn dark() -> Self {
        Self {
            surface: [20, 27, 29],
            raised: [28, 37, 39],
            border: [52, 65, 68],
            gray_dim: [90, 101, 99],
            gray: [150, 160, 156],
            gray_bright: [195, 202, 198],
            brand: [222, 133, 155],
            accent: [142, 219, 210],
            success: [104, 210, 163],
            warning: [232, 168, 95],
            danger: [255, 124, 120],
            user_message_bg: [38, 43, 44],
            selection_bg: [36, 61, 62],
            code_fg: [220, 225, 222],
            code_bg: [27, 32, 34],
            diff_add_fg: [133, 225, 170],
            diff_add_bg: [6, 56, 6],
            diff_add_gutter: [18, 92, 34],
            diff_remove_fg: [255, 156, 151],
            diff_remove_bg: [66, 14, 20],
            diff_remove_gutter: [108, 28, 35],
            link: [120, 191, 242],
        }
    }
    fn light() -> Self {
        Self {
            surface: [246, 247, 245],
            raised: [235, 238, 235],
            border: [186, 194, 190],
            gray_dim: [115, 123, 120],
            gray: [83, 94, 90],
            gray_bright: [52, 64, 60],
            brand: [128, 43, 67],
            accent: [0, 95, 135],
            success: [26, 127, 73],
            warning: [145, 83, 0],
            danger: [190, 45, 41],
            user_message_bg: [244, 244, 242],
            selection_bg: [215, 234, 232],
            code_fg: [36, 45, 42],
            code_bg: [242, 244, 242],
            diff_add_fg: [17, 99, 54],
            diff_add_bg: [218, 251, 225],
            diff_add_gutter: [172, 238, 187],
            diff_remove_fg: [177, 35, 31],
            diff_remove_bg: [255, 235, 233],
            diff_remove_gutter: [255, 206, 203],
            link: [0, 95, 135],
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn channels(color: Color) -> [u8; 3] {
        match color {
            Color::Rgb(r, g, b) => [r, g, b],
            other => panic!("expected RGB, got {other:?}"),
        }
    }
    fn luminance([r, g, b]: [u8; 3]) -> f64 {
        let c = |v: u8| {
            let v = f64::from(v) / 255.0;
            if v <= 0.04045 {
                v / 12.92
            } else {
                ((v + 0.055) / 1.055).powf(2.4)
            }
        };
        0.2126 * c(r) + 0.7152 * c(g) + 0.0722 * c(b)
    }
    fn contrast(left: [u8; 3], right: [u8; 3]) -> f64 {
        let (a, b) = (luminance(left), luminance(right));
        (a.max(b) + 0.05) / (a.min(b) + 0.05)
    }

    #[test]
    fn colorfgbg_classifies_last_field() {
        assert_eq!(parse_colorfgbg("15;0"), Some(Polarity::Dark));
        assert_eq!(parse_colorfgbg("0:15"), Some(Polarity::Light));
    }
    #[test]
    fn explicit_theme_preference_is_case_insensitive_and_safe() {
        assert_eq!(ThemePreference::parse("LIGHT"), ThemePreference::Light);
        assert_eq!(ThemePreference::parse("dark"), ThemePreference::Dark);
        assert_eq!(ThemePreference::parse("unknown"), ThemePreference::Auto);
    }
    #[test]
    fn color_level_detection_covers_every_fallback_and_host_promotion() {
        assert_eq!(
            ColorLevel::detect_from(true, true, "xterm-256color", Some("truecolor"), None, false),
            ColorLevel::None
        );
        assert_eq!(
            ColorLevel::detect_from(false, false, "dumb", None, None, false),
            ColorLevel::TrueColor
        );
        assert_eq!(
            ColorLevel::detect_from(false, true, "tmux-256color", None, Some("ghostty"), false),
            ColorLevel::TrueColor
        );
        assert_eq!(
            ColorLevel::detect_from(false, true, "xterm-256color", None, None, false),
            ColorLevel::Ansi256
        );
        assert_eq!(
            ColorLevel::detect_from(false, true, "xterm", None, None, false),
            ColorLevel::Basic
        );
        assert_eq!(
            ColorLevel::detect_from(false, true, "xterm", None, None, true),
            ColorLevel::TrueColor
        );
    }
    #[test]
    fn polarity_uses_bt709() {
        assert_eq!(classify_polarity([255, 255, 255]), Polarity::Light);
        assert_eq!(classify_polarity([0, 0, 0]), Polarity::Dark);
    }
    #[test]
    fn ansi256_is_perceptual_and_bounded() {
        assert!(matches!(
            quantize(Color::Rgb(35, 40, 50), ColorLevel::Ansi256),
            Color::Indexed(_)
        ));
    }
    #[test]
    fn basic_diff_has_no_background() {
        let t = Theme::new(Polarity::Dark, ColorLevel::Basic);
        assert_eq!(t.diff_add().bg, None);
        assert_eq!(t.diff_remove().bg, None);
    }
    #[test]
    fn basic_muted_is_reset_dim() {
        let t = Theme::new(Polarity::Light, ColorLevel::Basic);
        assert_eq!(t.muted().fg, None);
        assert!(t.muted().add_modifier.contains(Modifier::DIM));
    }
    #[test]
    fn every_polarity_has_all_heading_modifiers() {
        for p in [Polarity::Dark, Polarity::Light] {
            let t = Theme::new(p, ColorLevel::TrueColor);
            assert!(t.heading(1).add_modifier.contains(Modifier::BOLD));
            assert!(t.heading(6).add_modifier.contains(Modifier::ITALIC));
        }
    }

    #[test]
    fn transcript_styles_keep_roles_and_fallbacks_semantic() {
        for polarity in [Polarity::Dark, Polarity::Light] {
            let theme = Theme::new(polarity, ColorLevel::TrueColor);
            assert_eq!(theme.transcript_user().fg, Some(theme.gray_bright));
            assert_eq!(theme.transcript_user_band().bg, Some(theme.user_message_bg));
            assert_eq!(theme.transcript_assistant().fg, Some(theme.accent));
            assert_ne!(theme.transcript_assistant().fg, Some(theme.success));
            assert_eq!(theme.transcript_metadata().fg, Some(theme.gray_dim));
            assert_eq!(theme.transcript_tool().fg, Some(theme.warning));
            assert_eq!(theme.transcript_tool_settled().fg, Some(theme.gray_bright));
            assert!(theme
                .transcript_thinking()
                .add_modifier
                .contains(Modifier::DIM | Modifier::ITALIC));
        }
        let basic = Theme::new(Polarity::Dark, ColorLevel::Basic);
        assert_eq!(basic.transcript_user_band().bg, None);

        let fallback = Theme::new(Polarity::Dark, ColorLevel::None).transcript_thinking();
        assert_eq!(fallback.fg, None);
        assert!(fallback
            .add_modifier
            .contains(Modifier::DIM | Modifier::ITALIC));
    }

    #[test]
    fn transcript_uses_no_more_than_three_saturated_foregrounds() {
        let theme = Theme::new(Polarity::Dark, ColorLevel::TrueColor);
        let mut saturated = [
            theme.transcript_assistant(),
            theme.transcript_tool(),
            theme.transcript_danger(),
            theme.transcript_added(),
            theme.transcript_removed(),
            theme.transcript_link(),
        ]
        .into_iter()
        .filter_map(|style| style.fg)
        .collect::<Vec<_>>();
        saturated.sort_by_key(|color| format!("{color:?}"));
        saturated.dedup();
        assert_eq!(saturated.len(), 3);
    }

    #[test]
    fn truecolor_semantics_meet_contrast_on_both_target_polarities() {
        for polarity in [Polarity::Dark, Polarity::Light] {
            let theme = Theme::new(polarity, ColorLevel::TrueColor);
            let background = match polarity {
                Polarity::Dark => [13, 18, 20],
                Polarity::Light => [250, 251, 249],
            };
            for (name, color) in [
                ("muted", theme.muted),
                ("brand", theme.brand),
                ("accent", theme.accent),
                ("success", theme.success),
                ("warning", theme.warning),
                ("danger", theme.danger),
            ] {
                assert!(
                    contrast(channels(color), background) >= 4.5,
                    "{polarity:?} {name} contrast {}",
                    contrast(channels(color), background)
                );
            }
            let levels = [theme.gray_dim, theme.gray, theme.gray_bright]
                .map(channels)
                .map(luminance);
            assert!(
                (levels[1] - levels[0]).abs() >= 0.04,
                "dim and gray collapsed: {levels:?}"
            );
            assert!(
                (levels[2] - levels[1]).abs() >= 0.04,
                "gray and bright collapsed: {levels:?}"
            );
        }
    }
}
