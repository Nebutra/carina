//! SPIKE: unified diff -> colored ratatui Lines (manual spans, no ansi-to-tui).
//! The daemon's PatchTransaction carries a plain unified diff, so per-line
//! prefix coloring is enough; ansi-to-tui would only be needed for
//! pre-colored `git diff --color` output.

use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};

pub fn colorize<'a>(diff: &'a str) -> Vec<Line<'a>> {
    diff.lines()
        .map(|l| {
            let style = if l.starts_with("+++") || l.starts_with("---") {
                Style::new().fg(Color::White).add_modifier(Modifier::BOLD)
            } else if l.starts_with("@@") {
                Style::new().fg(Color::Cyan)
            } else if l.starts_with('+') {
                Style::new().fg(Color::Green)
            } else if l.starts_with('-') {
                Style::new().fg(Color::Red)
            } else if l.starts_with("diff ") || l.starts_with("index ") {
                Style::new().fg(Color::Yellow)
            } else {
                Style::new().fg(Color::Gray)
            };
            Line::from(Span::styled(l, style))
        })
        .collect()
}
