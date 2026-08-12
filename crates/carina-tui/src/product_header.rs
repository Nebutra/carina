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

#[derive(Debug, Clone)]
pub struct ConversationHeaderAction<'a> {
    pub label: &'a str,
    pub action: Action,
    pub component: ComponentId,
    pub tone: ConversationHeaderTone,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ConversationHeaderTone {
    Muted,
    Active,
    Blocking,
}

pub struct ConversationHeader<'a> {
    pub title: &'a str,
    pub product_menu_open: bool,
    /// Ordered from highest to lowest priority. Higher-priority actions remain
    /// visible as the terminal narrows.
    pub actions: &'a [ConversationHeaderAction<'a>],
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
                    truncate_width(title, available, theme.glyphs),
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
                        theme.glyphs,
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
            self.model,
            theme.glyphs.separator(),
            self.reasoning,
            theme.glyphs.separator(),
            self.provider,
            theme.glyphs.separator(),
            workspace_label(self.workspace, true, self.locale)
        );
        frame.render_widget(
            Paragraph::new(truncate_width(
                &summary,
                area.right().saturating_sub(metadata_x + 1) as usize,
                theme.glyphs,
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
                    truncate_width(value, value_width, theme.glyphs),
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

impl ConversationHeader<'_> {
    pub fn render(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        theme: Theme,
        interactions: &mut InteractionMap,
    ) -> Option<Rect> {
        if area.width == 0 || area.height == 0 {
            return None;
        }
        frame.render_widget(
            Block::default()
                .borders(Borders::BOTTOM)
                .border_type(theme.glyphs.outer_border_type())
                .border_style(Style::default().fg(theme.border)),
            area,
        );

        let product_component = ComponentId::stable("conversation-header", "product-menu");
        let product_label = format!("{COMPACT_NAME} {}", theme.glyphs.disclosure_open());
        let identity_min_width = UnicodeWidthStr::width(product_label.as_str()) as u16;
        let mut right = area.right();
        for action in self.actions {
            let width = UnicodeWidthStr::width(action.label) as u16 + 2;
            let x = right.saturating_sub(width);
            if x <= area.x.saturating_add(identity_min_width + 2) {
                continue;
            }
            let rect = Rect::new(x, area.y, width, 1);
            let base_style = match action.tone {
                ConversationHeaderTone::Muted => theme.muted(),
                ConversationHeaderTone::Active => theme.focus(),
                ConversationHeaderTone::Blocking => Style::default()
                    .fg(theme.warning)
                    .add_modifier(Modifier::BOLD),
            };
            let style = if interactions.hovered(action.component) {
                base_style.patch(theme.hovered())
            } else {
                base_style
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

        let available = right.saturating_sub(area.x) as usize;
        if available == 0 {
            return None;
        }
        let separator = theme.glyphs.separator();
        let suffix = if self.title.trim().is_empty() {
            String::new()
        } else {
            format!("  {separator}  {}", self.title)
        };
        let name_width = UnicodeWidthStr::width(product_label.as_str());
        let product_style = Style::default()
            .fg(theme.brand)
            .add_modifier(Modifier::BOLD);
        let product_style = if self.product_menu_open {
            product_style.patch(theme.focus())
        } else if interactions.hovered(product_component) {
            product_style.patch(theme.hovered())
        } else {
            product_style
        };
        frame.render_widget(
            Paragraph::new(Line::from(vec![
                Span::styled(product_label, product_style),
                Span::styled(
                    truncate_width(&suffix, available.saturating_sub(name_width), theme.glyphs),
                    Style::default().fg(theme.muted),
                ),
            ])),
            Rect::new(area.x, area.y, available as u16, 1),
        );
        let product_area = Rect::new(area.x, area.y, name_width as u16, 1);
        interactions.register(HitRegion {
            component: product_component,
            area: product_area,
            action: Action::ToggleProductMenu,
        });
        Some(product_area)
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

fn truncate_width(value: &str, max_width: usize, glyphs: crate::glyphs::Glyphs) -> String {
    crate::render_contract::truncate_width_with_glyphs(value, max_width, glyphs)
}

#[cfg(test)]
mod tests {
    use ratatui::Terminal;
    use ratatui::backend::TestBackend;
    use ratatui::layout::Position;

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
        let glyphs = crate::glyphs::Glyphs::default();
        assert_eq!(
            truncate_width("工作区/carina", 8, glyphs),
            format!("工作区/{}", glyphs.ellipsis())
        );
        assert_eq!(
            UnicodeWidthStr::width(truncate_width("工作区/carina", 8, glyphs).as_str()),
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

    #[test]
    fn conversation_header_keeps_priority_actions_as_width_narrows() {
        let actions = [
            ConversationHeaderAction {
                label: "Review",
                action: Action::ResumePausedExecutionRun,
                component: ComponentId(51),
                tone: ConversationHeaderTone::Blocking,
            },
            ConversationHeaderAction {
                label: "Build",
                action: Action::TogglePlanMode,
                component: ComponentId(52),
                tone: ConversationHeaderTone::Active,
            },
            ConversationHeaderAction {
                label: "gpt-5.5  ·  high reasoning",
                action: Action::OpenModels,
                component: ComponentId(53),
                tone: ConversationHeaderTone::Muted,
            },
            ConversationHeaderAction {
                label: "Sessions",
                action: Action::OpenSessions,
                component: ComponentId(54),
                tone: ConversationHeaderTone::Muted,
            },
            ConversationHeaderAction {
                label: "Settings",
                action: Action::OpenSettings,
                component: ComponentId(55),
                tone: ConversationHeaderTone::Muted,
            },
        ];

        for (width, expected, hidden) in [
            (
                70,
                vec!["Review", "Build", "gpt-5.5  ·  high reasoning", "Sessions"],
                vec!["Settings"],
            ),
            (
                30,
                vec!["Review", "Build"],
                vec!["gpt-5.5", "high reasoning", "Sessions", "Settings"],
            ),
            (
                20,
                vec!["Review"],
                vec!["Build", "gpt-5.5", "high reasoning", "Sessions", "Settings"],
            ),
        ] {
            let mut terminal = Terminal::new(TestBackend::new(width, 2)).unwrap();
            let mut interactions = InteractionMap::default();
            terminal
                .draw(|frame| {
                    ConversationHeader {
                        title: "Design system convergence",
                        product_menu_open: false,
                        actions: &actions,
                    }
                    .render(
                        frame,
                        frame.area(),
                        Theme::carina(false),
                        &mut interactions,
                    );
                })
                .unwrap();
            let rendered = (0..width)
                .map(|x| terminal.backend().buffer().cell((x, 0)).unwrap().symbol())
                .collect::<String>();
            assert!(rendered.contains("Carina"), "width={width}: {rendered}");
            for label in expected {
                assert!(rendered.contains(label), "width={width}: {rendered}");
            }
            for label in hidden {
                assert!(!rendered.contains(label), "width={width}: {rendered}");
            }
            assert_eq!(
                interactions.action_at(Position::new(width - 2, 0)),
                Some(Action::ResumePausedExecutionRun),
                "the blocking action must own the rightmost priority slot at width={width}"
            );
        }
    }

    #[test]
    fn conversation_header_hover_changes_style_not_copy_or_hit_geometry() {
        for no_color in [false, true] {
            let action = ConversationHeaderAction {
                label: "Build",
                action: Action::TogglePlanMode,
                component: ComponentId(61),
                tone: ConversationHeaderTone::Active,
            };
            let mut terminal = Terminal::new(TestBackend::new(40, 2)).unwrap();
            let mut interactions = InteractionMap::default();
            terminal
                .draw(|frame| {
                    interactions.begin_frame();
                    ConversationHeader {
                        title: "conversation",
                        product_menu_open: false,
                        actions: std::slice::from_ref(&action),
                    }
                    .render(
                        frame,
                        frame.area(),
                        Theme::carina(no_color),
                        &mut interactions,
                    );
                })
                .unwrap();
            let before = terminal.backend().buffer().clone();
            let position = Position::new(36, 0);
            assert_eq!(
                interactions.action_at(position),
                Some(Action::TogglePlanMode)
            );
            assert!(interactions.update_hover(position));

            terminal
                .draw(|frame| {
                    interactions.begin_frame();
                    ConversationHeader {
                        title: "conversation",
                        product_menu_open: false,
                        actions: std::slice::from_ref(&action),
                    }
                    .render(
                        frame,
                        frame.area(),
                        Theme::carina(no_color),
                        &mut interactions,
                    );
                })
                .unwrap();
            let after = terminal.backend().buffer();
            assert_eq!(
                (0..40)
                    .map(|x| before.cell((x, 0)).unwrap().symbol())
                    .collect::<String>(),
                (0..40)
                    .map(|x| after.cell((x, 0)).unwrap().symbol())
                    .collect::<String>()
            );
            assert_eq!(
                interactions.action_at(position),
                Some(Action::TogglePlanMode)
            );
            assert_ne!(
                before.cell((position.x, position.y)).unwrap().modifier,
                after.cell((position.x, position.y)).unwrap().modifier
            );
        }
    }

    #[test]
    fn conversation_product_menu_trigger_is_exact_and_width_locked() {
        for (glyphs, expected) in [
            (crate::glyphs::GlyphMode::Unicode, "Carina ▾ "),
            (crate::glyphs::GlyphMode::Ascii, "Carina v "),
        ] {
            let mut theme = Theme::carina(false);
            theme.glyphs = theme.glyphs.with_mode(glyphs);
            let mut terminal = Terminal::new(TestBackend::new(40, 2)).unwrap();
            let mut interactions = InteractionMap::default();
            let mut anchor = None;
            terminal
                .draw(|frame| {
                    anchor = ConversationHeader {
                        title: "conversation",
                        product_menu_open: false,
                        actions: &[],
                    }
                    .render(frame, frame.area(), theme, &mut interactions);
                })
                .unwrap();
            let anchor = anchor.expect("visible header owns a product menu anchor");
            let rendered = (0..anchor.width)
                .map(|x| terminal.backend().buffer().cell((x, 0)).unwrap().symbol())
                .collect::<String>();
            assert_eq!(rendered, expected);
            for x in anchor.x..anchor.right() {
                assert_eq!(
                    interactions.action_at(Position::new(x, anchor.y)),
                    Some(Action::ToggleProductMenu)
                );
            }
            assert_ne!(
                interactions.action_at(Position::new(anchor.right(), anchor.y)),
                Some(Action::ToggleProductMenu),
                "the conversation title must not inherit the product trigger"
            );
        }
    }
}
