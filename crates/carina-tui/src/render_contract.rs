//! Stable terminal-rendering contracts shared by product components and tests.

use std::time::Duration;

use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

pub fn status_spinner(elapsed: Duration, no_color: bool) -> &'static str {
    let _ = no_color;
    crate::glyphs::Glyphs::detect().status_spinner(elapsed.as_millis())
}

/// A compact token count that never exceeds four terminal cells.
pub fn format_tokens(value: u64) -> String {
    match value {
        0..=999 => value.to_string(),
        1_000..=9_949 => format!("{:.1}K", value as f64 / 1_000.0),
        9_950..=999_999 => format!("{}K", ((value + 500) / 1_000).min(999)),
        1_000_000..=9_949_999 => format!("{:.1}M", value as f64 / 1_000_000.0),
        _ => format!("{}M", ((value / 1_000_000).min(999))),
    }
}

/// A right-aligned five-cell percentage.
pub fn format_percent(value: f64) -> String {
    let value = value.clamp(0.0, 100.0);
    if value >= 99.95 {
        "MAX %".into()
    } else if value >= 10.0 {
        format!("{value:>4.1}%")
    } else {
        format!("{value:>4.2}%")
    }
}

/// Compact elapsed time with a stable maximum width of five cells.
pub fn format_elapsed(value: Duration) -> String {
    let seconds = value.as_secs();
    if seconds < 60 {
        format!("{seconds}s")
    } else if seconds < 3_600 {
        format!("{}m{:02}", seconds / 60, seconds % 60)
    } else {
        format!("{}h{:02}", (seconds / 3_600).min(99), (seconds / 60) % 60)
    }
}

/// Compact byte count with a stable maximum width of five cells.
pub fn format_bytes(value: u64) -> String {
    const UNITS: [&str; 4] = ["B", "K", "M", "G"];
    let mut amount = value as f64;
    let mut unit = 0;
    while amount >= 999.5 && unit < UNITS.len() - 1 {
        amount /= 1_024.0;
        unit += 1;
    }
    amount = amount.min(999.0);
    if unit == 0 {
        format!("{}B", value.min(999))
    } else if amount < 10.0 {
        format!("{amount:.1}{}", UNITS[unit])
    } else {
        format!("{amount:.0}{}", UNITS[unit])
    }
}

/// Truncate without splitting a wide character and keep the result within `max_width` cells.
pub fn truncate_width(value: &str, max_width: usize) -> String {
    truncate_width_with_glyphs(value, max_width, crate::glyphs::Glyphs::detect())
}

pub fn truncate_width_with_glyphs(
    value: &str,
    max_width: usize,
    glyphs: crate::glyphs::Glyphs,
) -> String {
    if UnicodeWidthStr::width(value) <= max_width {
        return value.to_owned();
    }
    if max_width == 0 {
        return String::new();
    }
    let content_width = max_width.saturating_sub(1);
    let mut output = String::new();
    let mut width = 0;
    for character in value.chars() {
        let character_width = UnicodeWidthChar::width(character).unwrap_or_default();
        if width + character_width > content_width {
            break;
        }
        output.push(character);
        width += character_width;
    }
    output.push_str(glyphs.ellipsis());
    output
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn formatters_keep_their_width_contracts() {
        for value in [0, 9, 999, 1_000, 9_949, 9_950, 999_999, 1_000_000, u64::MAX] {
            assert!(UnicodeWidthStr::width(format_tokens(value).as_str()) <= 4);
        }
        for value in [0.0, 5.12, 20.1, 99.9, 100.0] {
            assert_eq!(UnicodeWidthStr::width(format_percent(value).as_str()), 5);
        }
        for value in [0, 59, 60, 3_599, 3_600, u64::MAX] {
            assert!(
                UnicodeWidthStr::width(format_elapsed(Duration::from_secs(value)).as_str()) <= 5
            );
        }
        for value in [0, 999, 1_024, 10_240, 1_048_576, u64::MAX] {
            assert!(UnicodeWidthStr::width(format_bytes(value).as_str()) <= 5);
        }
    }

    #[test]
    fn cjk_truncation_never_exceeds_requested_cells() {
        for input in [
            "workspace/项目/源代码.rs",
            "模型 · claude-sonnet",
            "emoji 🚀 中文",
        ] {
            for width in 0..20 {
                assert!(UnicodeWidthStr::width(truncate_width(input, width).as_str()) <= width);
            }
        }
    }

    #[test]
    fn truncation_uses_the_callers_glyph_mode() {
        let unicode = crate::glyphs::Glyphs::new(crate::glyphs::GlyphMode::Unicode);
        let ascii = crate::glyphs::Glyphs::new(crate::glyphs::GlyphMode::Ascii);
        assert_eq!(truncate_width_with_glyphs("abcdef", 4, unicode), "abc…");
        assert_eq!(truncate_width_with_glyphs("abcdef", 4, ascii), "abc.");
    }
}
