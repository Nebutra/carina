use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};

use crate::theme::Theme;

pub const MARK_WIDTH_CELLS: u16 = 10;
pub const MARK_HEIGHT_CELLS: u16 = 5;

const MARK: &str =
    include_str!("../../../docs/brand/assets/logo/carina-symbol-terminal-braille.txt");

/// Render the canonical mark as terminal-native Braille. The diagonal light
/// band gives the dense glyph field depth without changing the silhouette.
pub fn lines(theme: Theme) -> Vec<Line<'static>> {
    MARK.lines()
        .enumerate()
        .map(|(y, row)| {
            Line::from(
                row.chars()
                    .enumerate()
                    .map(|(x, glyph)| {
                        let style = if theme.no_color || glyph == '\u{2800}' {
                            Style::default().fg(theme.brand)
                        } else {
                            let diagonal = x as isize + (MARK_HEIGHT_CELLS as usize - y) as isize;
                            match diagonal {
                                8 => Style::default().fg(theme.text).add_modifier(Modifier::BOLD),
                                7 | 9 => Style::default().fg(theme.muted),
                                _ => Style::default().fg(theme.brand),
                            }
                        };
                        Span::styled(glyph.to_string(), style)
                    })
                    .collect::<Vec<_>>(),
            )
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeSet;

    use super::*;
    use unicode_width::UnicodeWidthStr;

    #[test]
    fn generated_mark_has_stable_terminal_dimensions_and_varied_density() {
        let rows = MARK.lines().collect::<Vec<_>>();
        assert_eq!(rows.len(), MARK_HEIGHT_CELLS as usize);
        assert!(
            rows.iter()
                .all(|row| UnicodeWidthStr::width(*row) <= MARK_WIDTH_CELLS as usize)
        );

        let densities = MARK
            .chars()
            .filter(|glyph| ('\u{2801}'..='\u{28ff}').contains(glyph))
            .map(|glyph| (glyph as u32 - 0x2800).count_ones())
            .collect::<BTreeSet<_>>();
        assert!(
            densities.len() >= 4,
            "mark needs contour texture, not one repeated dot glyph"
        );
    }

    #[test]
    fn no_color_retains_every_glyph_without_a_color_only_signal() {
        let rendered = lines(Theme::carina(true));
        assert_eq!(rendered.len(), MARK_HEIGHT_CELLS as usize);
        assert!(rendered.iter().flat_map(|line| &line.spans).all(|span| {
            span.style.fg == Some(Theme::carina(true).brand)
                && !span.style.add_modifier.contains(Modifier::BOLD)
        }));
    }
}
