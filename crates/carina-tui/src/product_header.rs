use std::path::Path;

use ratatui::Frame;
use ratatui::layout::Rect;
use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, Borders, Paragraph};
use unicode_width::UnicodeWidthStr;

use crate::component::{Action, ComponentId, HitRegion, InteractionMap};
use crate::i18n::{Locale, MessageId, text};
use crate::layout_contract as layout;
use crate::terminal_logo::{MARK_HEIGHT_CELLS, MARK_WIDTH_CELLS, lines as terminal_logo_lines};
use crate::theme::Theme;

const COMPACT_NAME: &str = "Carina";

#[derive(Debug, Clone)]
pub struct HeaderAction {
    pub label: &'static str,
    pub action: Action,
    pub component: ComponentId,
}

pub struct ProductHeader<'a> {
    pub locale: Locale,
    pub title: Option<&'a str>,
    pub phase: Option<&'a str>,
    pub provider: &'a str,
    pub model: &'a str,
    pub reasoning: &'a str,
    pub workspace: &'a Path,
    pub actions: &'a [HeaderAction],
}

pub fn product_header_height(area: Rect) -> u16 {
    if layout::expanded_header(area) {
        layout::EXPANDED_HEADER_HEIGHT
    } else {
        layout::COMPACT_HEADER_HEIGHT
    }
}

impl ProductHeader<'_> {
    pub fn render(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        theme: Theme,
        interactions: &mut InteractionMap,
    ) {
        frame.render_widget(
            Block::default()
                .borders(Borders::BOTTOM)
                .border_type(theme.glyphs.outer_border_type())
                .border_style(Style::default().fg(theme.border)),
            area,
        );
        if area.height >= layout::EXPANDED_HEADER_HEIGHT
            && area.width >= layout::EXPANDED_HEADER_MIN_WIDTH
        {
            self.render_expanded(frame, area, theme, interactions);
        } else {
            self.render_compact(frame, area, theme, interactions);
        }
    }

    fn render_expanded(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        theme: Theme,
        interactions: &mut InteractionMap,
    ) {
        let metadata_x = {
            for (row, line) in terminal_logo_lines(theme).into_iter().enumerate() {
                frame.render_widget(
                    Paragraph::new(line),
                    Rect::new(area.x + 1, area.y + row as u16, MARK_WIDTH_CELLS, 1),
                );
            }
            debug_assert_eq!(MARK_HEIGHT_CELLS, 5);
            area.x + MARK_WIDTH_CELLS + 4
        };
        let right_edge = self.render_right_slot(
            frame,
            area,
            metadata_x.saturating_add(20),
            theme,
            interactions,
        );
        let first_width = right_edge.saturating_sub(metadata_x) as usize;
        let mut identity = vec![Span::styled(
            "Carina",
            Style::default().fg(theme.text).add_modifier(Modifier::BOLD),
        )];
        if let Some(title) = self.title.filter(|title| !title.trim().is_empty()) {
            let separator = theme.glyphs.separator();
            let used = format!("Carina  {separator}  ");
            let available = first_width.saturating_sub(UnicodeWidthStr::width(used.as_str()));
            if available > 0 {
                identity.push(Span::styled(
                    format!("  {separator}  "),
                    Style::default().fg(theme.border),
                ));
                identity.push(Span::styled(
                    truncate_width(title, available),
                    Style::default().fg(theme.muted),
                ));
            }
        }
        frame.render_widget(
            Paragraph::new(Line::from(identity)),
            Rect::new(metadata_x, area.y, first_width as u16, 1),
        );

        self.render_metadata_line(
            frame,
            Rect::new(
                metadata_x,
                area.y + 1,
                area.right().saturating_sub(metadata_x + 1),
                1,
            ),
            text(self.locale, MessageId::Provider),
            self.provider,
            theme.accent,
            theme,
        );
        let model_reasoning = format!(
            "{}  {}  {}",
            self.model,
            theme.glyphs.separator(),
            self.reasoning
        );
        self.render_metadata_line(
            frame,
            Rect::new(
                metadata_x,
                area.y + 2,
                area.right().saturating_sub(metadata_x + 1),
                1,
            ),
            text(self.locale, MessageId::Model),
            &model_reasoning,
            theme.text,
            theme,
        );
        self.render_metadata_line(
            frame,
            Rect::new(
                metadata_x,
                area.y + 3,
                area.right().saturating_sub(metadata_x + 1),
                1,
            ),
            text(self.locale, MessageId::Workspace),
            &workspace_label(self.workspace, false, self.locale),
            theme.muted,
            theme,
        );
    }

    fn render_compact(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        theme: Theme,
        interactions: &mut InteractionMap,
    ) {
        let metadata_x = area.x;
        let right_edge = self.render_right_slot(
            frame,
            area,
            metadata_x.saturating_add(20),
            theme,
            interactions,
        );
        let title = self.title.filter(|title| !title.trim().is_empty());
        let suffix = title
            .map(|title| format!("  {}  {title}", theme.glyphs.separator()))
            .unwrap_or_default();
        let identity_width =
            UnicodeWidthStr::width(COMPACT_NAME) + UnicodeWidthStr::width(suffix.as_str());
        frame.render_widget(
            Paragraph::new(Line::from(vec![
                Span::styled(
                    COMPACT_NAME,
                    Style::default()
                        .fg(theme.brand)
                        .add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    truncate_width(
                        &suffix,
                        right_edge
                            .saturating_sub(metadata_x)
                            .saturating_sub(UnicodeWidthStr::width(COMPACT_NAME) as u16)
                            as usize,
                    ),
                    Style::default().fg(theme.muted),
                ),
            ])),
            Rect::new(
                metadata_x,
                area.y,
                right_edge
                    .saturating_sub(metadata_x)
                    .min(identity_width as u16),
                1,
            ),
        );
        let summary = format!(
            "{}  {}  {}  {}  {}  {}  {}",
            self.provider,
            theme.glyphs.separator(),
            self.model,
            theme.glyphs.separator(),
            self.reasoning,
            theme.glyphs.separator(),
            workspace_label(self.workspace, true, self.locale)
        );
        frame.render_widget(
            Paragraph::new(truncate_width(
                &summary,
                area.right().saturating_sub(metadata_x + 1) as usize,
            ))
            .style(Style::default().fg(theme.muted)),
            Rect::new(
                metadata_x,
                area.y + 1,
                area.right().saturating_sub(metadata_x + 1),
                1,
            ),
        );
    }

    fn render_metadata_line(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        label: &str,
        value: &str,
        value_color: ratatui::style::Color,
        theme: Theme,
    ) {
        let label_width = layout::HEADER_LABEL_WIDTH;
        let value_width = area.width.saturating_sub(label_width) as usize;
        frame.render_widget(
            Paragraph::new(Line::from(vec![
                Span::styled(
                    format!("{label:<width$} ", width = layout::HEADER_LABEL_TEXT_WIDTH),
                    Style::default().fg(theme.muted),
                ),
                Span::styled(
                    truncate_width(value, value_width),
                    Style::default().fg(value_color),
                ),
            ])),
            area,
        );
    }

    fn render_right_slot(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        min_x: u16,
        theme: Theme,
        interactions: &mut InteractionMap,
    ) -> u16 {
        let mut right = area.right().saturating_sub(1);
        if self.actions.is_empty() {
            if let Some(phase) = self.phase.filter(|phase| !phase.is_empty()) {
                let width = UnicodeWidthStr::width(phase) as u16;
                if right.saturating_sub(width) > min_x {
                    let x = right.saturating_sub(width);
                    frame.render_widget(
                        Paragraph::new(phase).style(Style::default().fg(theme.muted)),
                        Rect::new(x, area.y, width, 1),
                    );
                    return x.saturating_sub(1);
                }
            }
            return right;
        }
        for action in self.actions.iter().rev() {
            let width = UnicodeWidthStr::width(action.label) as u16 + 2;
            let x = right.saturating_sub(width);
            if x <= min_x {
                break;
            }
            let rect = Rect::new(x, area.y, width, 1);
            let style = if interactions.hovered(action.component) {
                theme.selected()
            } else {
                theme.muted()
            };
            frame.render_widget(
                Paragraph::new(format!(" {} ", action.label)).style(style),
                rect,
            );
            interactions.register(HitRegion {
                component: action.component,
                area: rect,
                action: action.action.clone(),
            });
            right = x.saturating_sub(1);
        }
        right
    }
}

fn workspace_label(workspace: &Path, compact: bool, locale: Locale) -> String {
    if compact {
        return workspace
            .file_name()
            .and_then(|name| name.to_str())
            .filter(|name| !name.is_empty())
            .unwrap_or_else(|| text(locale, MessageId::WorkspaceFallback))
            .to_owned();
    }
    let display = workspace.display().to_string();
    let Some(home) = std::env::var_os("HOME") else {
        return display;
    };
    let home = Path::new(&home);
    let Ok(relative) = workspace.strip_prefix(home) else {
        return display;
    };
    if relative.as_os_str().is_empty() {
        "~".to_owned()
    } else {
        format!("~/{}", relative.display())
    }
}

fn truncate_width(value: &str, max_width: usize) -> String {
    crate::render_contract::truncate_width(value, max_width)
}

#[cfg(test)]
mod tests {
    use ratatui::Terminal;
    use ratatui::backend::TestBackend;

    use super::*;

    #[test]
    fn header_height_preserves_compact_terminals() {
        assert_eq!(product_header_height(Rect::new(0, 0, 120, 40)), 6);
        assert_eq!(product_header_height(Rect::new(0, 0, 95, 40)), 3);
        assert_eq!(product_header_height(Rect::new(0, 0, 120, 24)), 3);
        assert_eq!(product_header_height(Rect::new(0, 0, 40, 10)), 3);
        assert_eq!(product_header_height(Rect::new(0, 0, 60, 24)), 3);
    }

    #[test]
    fn truncation_uses_terminal_cell_width_for_cjk() {
        assert_eq!(
            truncate_width("工作区/carina", 8),
            format!("工作区/{}", crate::glyphs::Glyphs::detect().ellipsis())
        );
        assert_eq!(
            UnicodeWidthStr::width(truncate_width("工作区/carina", 8).as_str()),
            8
        );
    }

    #[test]
    fn compact_workspace_never_exposes_parent_runtime_paths() {
        assert_eq!(
            workspace_label(Path::new("/private/runtime/ws/carina"), true, Locale::En),
            "carina"
        );
    }

    #[test]
    fn visible_header_action_owns_its_full_rendered_hit_region() {
        let mut terminal = Terminal::new(TestBackend::new(120, 40)).unwrap();
        let mut interactions = InteractionMap::default();
        let actions = [HeaderAction {
            label: "Model",
            action: Action::OpenModels,
            component: ComponentId(42),
        }];
        terminal
            .draw(|frame| {
                ProductHeader {
                    locale: Locale::En,
                    title: Some("conversation"),
                    phase: None,
                    provider: "Test",
                    model: "gpt-5.5",
                    reasoning: "high",
                    workspace: Path::new("/tmp/workspace"),
                    actions: &actions,
                }
                .render(
                    frame,
                    Rect::new(0, 0, 120, 6),
                    Theme::carina(false),
                    &mut interactions,
                );
            })
            .unwrap();

        for column in 112..119 {
            assert_eq!(
                interactions.action_at(ratatui::layout::Position::new(column, 0)),
                Some(Action::OpenModels)
            );
        }
    }

    #[test]
    fn expanded_header_contains_terminal_native_braille_mark() {
        let mut terminal = Terminal::new(TestBackend::new(120, 6)).unwrap();
        let mut interactions = InteractionMap::default();
        terminal
            .draw(|frame| {
                ProductHeader {
                    locale: Locale::En,
                    title: None,
                    phase: Some("Conversation"),
                    provider: "TDS / CC Switch",
                    model: "gpt-5.5",
                    reasoning: "high",
                    workspace: Path::new("/workspace/carina"),
                    actions: &[],
                }
                .render(
                    frame,
                    Rect::new(0, 0, 120, 6),
                    Theme::carina(false),
                    &mut interactions,
                );
            })
            .unwrap();
        let buffer = terminal.backend().buffer();
        let rendered = (0..MARK_HEIGHT_CELLS)
            .flat_map(|row| (1..=MARK_WIDTH_CELLS).map(move |column| (column, row)))
            .map(|position| buffer.cell(position).unwrap().symbol())
            .collect::<String>();
        assert!(
            rendered
                .chars()
                .any(|glyph| ('\u{2801}'..='\u{28ff}').contains(&glyph))
        );
        assert!(!rendered.contains('\u{10eeee}'));
        assert!(!rendered.contains("\x1b_G"));
    }

    #[test]
    fn compact_no_color_header_keeps_product_context_and_hides_internal_state() {
        let mut terminal = Terminal::new(TestBackend::new(60, 3)).unwrap();
        let mut interactions = InteractionMap::default();
        terminal
            .draw(|frame| {
                ProductHeader {
                    locale: Locale::En,
                    title: Some("Provider onboarding"),
                    phase: Some("Conversation"),
                    provider: "TDS / CC Switch",
                    model: "gpt-5.5",
                    reasoning: "high reasoning",
                    workspace: Path::new("/private/runtime/ws/carina"),
                    actions: &[],
                }
                .render(
                    frame,
                    Rect::new(0, 0, 60, 3),
                    Theme::carina(true),
                    &mut interactions,
                );
            })
            .unwrap();
        let buffer = terminal.backend().buffer();
        let rendered = (0..3)
            .map(|y| {
                (0..60)
                    .map(|x| buffer.cell((x, y)).unwrap().symbol())
                    .collect::<String>()
            })
            .collect::<Vec<_>>()
            .join("\n");
        assert!(rendered.contains("Carina"));
        assert!(rendered.contains("Provider onboarding"));
        assert!(rendered.contains("TDS / CC Switch"));
        assert!(rendered.contains("gpt-5.5"));
        assert!(rendered.contains("high reasoning"));
        assert!(rendered.contains("carina"));
        assert!(!rendered.contains("private session id"));
        assert!(!rendered.contains("failed"));
        assert!(
            buffer
                .cell((0, 0))
                .unwrap()
                .modifier
                .contains(Modifier::BOLD)
        );
    }
}
