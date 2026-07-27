use ratatui::Frame;
use ratatui::layout::{Constraint, Direction, Layout, Margin, Position, Rect};
use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, Borders, Clear, Paragraph, StatefulWidgetRef, Wrap};

use super::{App, Focus, LOCALES, Phase};
use crate::component::{Action, ComponentId, HitRegion};
use crate::overlay::{ApprovalScope, CheckpointStep, Overlay};
use crate::transcript::BlockKind;

impl App {
    pub(super) fn render(&mut self, frame: &mut Frame<'_>) {
        self.interactions.begin_frame();
        let area = frame.area();
        frame.render_widget(
            Block::default().style(Style::default().bg(self.theme.background)),
            area,
        );
        match self.phase {
            Phase::Conversation => self.render_conversation(frame, area),
            _ => self.render_scene(frame, area),
        }
        if let Some(overlay) = self.overlays.active().cloned() {
            self.interactions.begin_overlay();
            self.render_overlay(frame, area, &overlay);
        }
    }

    fn render_scene(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let width = area.width.min(104);
        let height = area.height.saturating_sub(2).min(34);
        let shell = Rect::new(
            area.x + area.width.saturating_sub(width) / 2,
            area.y + area.height.saturating_sub(height) / 2,
            width,
            height,
        );
        let chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(4),
                Constraint::Min(8),
                Constraint::Length(3),
            ])
            .split(shell);
        self.render_header(frame, chunks[0]);
        let body = if chunks[1].width >= 68 {
            let columns = Layout::default()
                .direction(Direction::Horizontal)
                .constraints([
                    Constraint::Length(22),
                    Constraint::Length(3),
                    Constraint::Min(36),
                ])
                .split(chunks[1]);
            self.render_journey_rail(frame, columns[0]);
            columns[2]
        } else {
            chunks[1]
        };
        match self.phase {
            Phase::Locale => self.render_locale(frame, body),
            Phase::Provider => self.render_providers(frame, body),
            Phase::Credential => self.render_credential(frame, body),
            Phase::Model => self.render_models(frame, body),
            Phase::Session => self.render_sessions(frame, body),
            Phase::Diagnostic => self.render_diagnostic(frame, body),
            Phase::Conversation => unreachable!(),
        }
        self.render_scene_footer(frame, chunks[2]);
    }

    fn render_journey_rail(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let stages = [
            ("1", "Language", Phase::Locale, Action::OpenLocale, true),
            (
                "2",
                "Provider",
                Phase::Provider,
                Action::OpenProvider,
                self.options.locale.is_some(),
            ),
            (
                "3",
                "Model",
                Phase::Model,
                Action::OpenModels,
                self.inventory.has_runnable_provider(),
            ),
            (
                "4",
                "Conversation",
                Phase::Session,
                Action::OpenSessions,
                !self.models.is_empty(),
            ),
        ];
        frame.render_widget(
            Paragraph::new("SETUP").style(
                Style::default()
                    .fg(self.theme.muted)
                    .add_modifier(Modifier::BOLD),
            ),
            Rect::new(area.x, area.y, area.width, 1),
        );
        for (index, (number, label, phase, action, enabled)) in stages.into_iter().enumerate() {
            let row = Rect::new(area.x, area.y + 2 + index as u16 * 2, area.width, 1);
            let component = ComponentId(300 + index as u64);
            let active = self.phase == phase
                || (phase == Phase::Provider && self.phase == Phase::Credential);
            let hovered = enabled && self.interactions.hovered(component);
            let glyph = if active {
                "●"
            } else if enabled {
                "○"
            } else {
                "·"
            };
            let style = if active || hovered {
                self.theme.selected()
            } else if enabled {
                Style::default().fg(self.theme.text)
            } else {
                Style::default().fg(self.theme.muted)
            };
            frame.render_widget(
                Paragraph::new(format!("{glyph} {number}  {label}")).style(style),
                row,
            );
            if enabled {
                self.interactions.register(HitRegion {
                    component,
                    area: row,
                    action,
                });
            }
        }
        frame.render_widget(
            Paragraph::new("Prerequisites are verified before the composer becomes available.")
                .style(Style::default().fg(self.theme.muted))
                .wrap(Wrap { trim: false }),
            Rect::new(
                area.x,
                area.bottom().saturating_sub(5),
                area.width,
                5.min(area.height),
            ),
        );
    }

    fn render_header(&self, frame: &mut Frame<'_>, area: Rect) {
        let line = Line::from(vec![
            Span::styled(
                "CARINA",
                Style::default()
                    .fg(self.theme.brand)
                    .add_modifier(Modifier::BOLD),
            ),
            Span::styled("  agent workspace", Style::default().fg(self.theme.muted)),
        ]);
        let meta = Line::from(vec![
            Span::styled(
                self.options.workspace.display().to_string(),
                Style::default().fg(self.theme.text),
            ),
            Span::styled(
                format!(
                    "  runtime {} / protocol {}",
                    self.runtime.runtime_version, self.runtime.protocol_version
                ),
                Style::default().fg(self.theme.muted),
            ),
        ]);
        frame.render_widget(Paragraph::new(vec![line, meta]), area);
    }

    fn render_locale(&mut self, frame: &mut Frame<'_>, area: Rect) {
        self.render_scene_title(
            frame,
            area,
            "Choose language",
            "This choice is saved and remains available in settings.",
        );
        self.render_rows(
            frame,
            area.inner(Margin::new(0, 3)),
            LOCALES
                .iter()
                .map(|(id, name)| format!("{name:<14} {id}"))
                .collect(),
            self.locale_index,
            500,
            |_| None,
        );
    }

    fn render_providers(&mut self, frame: &mut Frame<'_>, area: Rect) {
        self.render_scene_title(
            frame,
            area,
            "Connect a provider",
            "Provider identity comes before model selection and conversation.",
        );
        let rows = self
            .inventory
            .providers
            .iter()
            .map(|provider| {
                let name = if provider.name.is_empty() {
                    provider.id.as_str()
                } else {
                    provider.name.as_str()
                };
                let state = if provider.available {
                    "ready"
                } else if provider.registered {
                    "credential required"
                } else {
                    "not registered"
                };
                format!("{name:<24} {state}")
            })
            .collect();
        self.render_rows(
            frame,
            area.inner(Margin::new(0, 3)),
            rows,
            self.provider_index,
            1_000,
            |index| Some(Action::SelectProvider(index)),
        );
    }

    fn render_credential(&self, frame: &mut Frame<'_>, area: Rect) {
        let provider = self
            .inventory
            .providers
            .get(self.provider_index)
            .map(|provider| provider.id.as_str())
            .unwrap_or("provider");
        self.render_scene_title(
            frame,
            area,
            &format!("Authenticate {provider}"),
            "The key is validated before it is stored. It is never shown or placed in argv.",
        );
        let input = Rect::new(
            area.x,
            area.y.saturating_add(5),
            area.width,
            3.min(area.height.saturating_sub(5)),
        );
        let masked = if self.credential_pending {
            "validating...".to_owned()
        } else {
            "*".repeat(self.credential.chars().count())
        };
        frame.render_widget(
            Paragraph::new(masked).block(
                Block::default()
                    .borders(Borders::ALL)
                    .border_style(self.theme.focus())
                    .title(" API key "),
            ),
            input,
        );
        if !self.credential_pending {
            frame.set_cursor_position(Position::new(
                input.x
                    + 1
                    + self
                        .credential
                        .chars()
                        .count()
                        .min(input.width as usize - 2) as u16,
                input.y + 1,
            ));
        }
    }

    fn render_models(&mut self, frame: &mut Frame<'_>, area: Rect) {
        self.render_scene_title(
            frame,
            area,
            "Choose model",
            "Only runnable providers and compatible text models are listed.",
        );
        let rows = self
            .models
            .iter()
            .map(|model| {
                let mut capabilities = Vec::new();
                if model.reasoning {
                    capabilities.push("reasoning");
                }
                if model.image_input {
                    capabilities.push("image");
                }
                if model.tool_call {
                    capabilities.push("tools");
                }
                format!("{:<38} {}", model.id, capabilities.join(" "))
            })
            .collect();
        self.render_rows(
            frame,
            area.inner(Margin::new(0, 3)),
            rows,
            self.model_index,
            2_000,
            |index| Some(Action::SelectModel(index)),
        );
    }

    fn render_sessions(&mut self, frame: &mut Frame<'_>, area: Rect) {
        self.render_scene_title(
            frame,
            area,
            "Open a conversation",
            "Resume existing work or start a new governed session.",
        );
        let mut rows = self
            .sessions
            .iter()
            .map(|session| {
                let label = if session.name.is_empty() {
                    session.session_id.as_str()
                } else {
                    session.name.as_str()
                };
                format!("{label:<38} {}", session.status)
            })
            .collect::<Vec<_>>();
        rows.push("+ New session".into());
        self.render_rows(
            frame,
            area.inner(Margin::new(0, 3)),
            rows,
            self.session_index,
            3_000,
            |index| Some(Action::SelectSession(index)),
        );
    }

    fn render_diagnostic(&self, frame: &mut Frame<'_>, area: Rect) {
        self.render_scene_title(
            frame,
            area,
            "Diagnostic only",
            "Carina is not ready for tasks. Repair provider/runtime readiness or exit.",
        );
        let body = vec![
            Line::from(Span::styled(
                format!("Socket: {}", self.options.socket.display()),
                Style::default().fg(self.theme.text),
            )),
            Line::from(Span::styled(
                format!("Providers: {}", self.inventory.providers.len()),
                Style::default().fg(self.theme.text),
            )),
            Line::from(""),
            Line::from(Span::styled(
                "Enter / r  retry readiness",
                self.theme.focus(),
            )),
            Line::from(Span::styled(
                "Esc / q    exit",
                Style::default().fg(self.theme.muted),
            )),
        ];
        frame.render_widget(
            Paragraph::new(body).wrap(Wrap { trim: false }),
            area.inner(Margin::new(0, 4)),
        );
    }

    fn render_scene_title(&self, frame: &mut Frame<'_>, area: Rect, title: &str, body: &str) {
        let title_area = Rect::new(area.x, area.y, area.width, area.height.min(3));
        frame.render_widget(
            Paragraph::new(vec![
                Line::from(Span::styled(title, self.theme.focus())),
                Line::from(Span::styled(body, Style::default().fg(self.theme.muted))),
            ])
            .wrap(Wrap { trim: false }),
            title_area,
        );
    }

    fn render_rows<F>(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        rows: Vec<String>,
        selected: usize,
        id_base: u64,
        action: F,
    ) where
        F: Fn(usize) -> Option<Action>,
    {
        for (index, row) in rows.into_iter().enumerate().take(area.height as usize) {
            let row_area = Rect::new(area.x, area.y + index as u16, area.width, 1);
            let component = ComponentId(id_base + index as u64);
            let hovered = self.interactions.hovered(component);
            let prefix = if index == selected { "> " } else { "  " };
            let style = if index == selected {
                self.theme.selected()
            } else if hovered {
                Style::default().fg(self.theme.accent).bg(self.theme.raised)
            } else {
                Style::default().fg(self.theme.text)
            };
            frame.render_widget(
                Paragraph::new(format!("{prefix}{row}")).style(style),
                row_area,
            );
            if let Some(action) = action(index) {
                self.interactions.register(HitRegion {
                    component,
                    area: row_area,
                    action,
                });
            }
        }
    }

    fn render_scene_footer(&self, frame: &mut Frame<'_>, area: Rect) {
        let help = match self.phase {
            Phase::Locale | Phase::Provider | Phase::Model | Phase::Session => {
                "Up/Down select  Enter continue  Esc back/exit"
            }
            Phase::Credential => "Enter validate  Esc cancel",
            Phase::Diagnostic => "Enter retry  Esc exit",
            Phase::Conversation => "",
        };
        frame.render_widget(
            Paragraph::new(vec![
                Line::from(Span::styled(help, Style::default().fg(self.theme.muted))),
                Line::from(Span::styled(
                    self.notice.as_str(),
                    Style::default().fg(self.theme.warning),
                )),
            ]),
            area,
        );
    }

    fn render_conversation(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let composer_height = self
            .composer
            .desired_height(area.width.saturating_sub(4))
            .clamp(1, 5)
            + 2;
        let chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(3),
                Constraint::Min(4),
                Constraint::Length(composer_height),
                Constraint::Length(1),
            ])
            .split(area);
        self.render_conversation_header(frame, chunks[0]);
        self.render_transcript(frame, chunks[1]);
        self.render_composer(frame, chunks[2]);
        self.render_status(frame, chunks[3]);
    }

    fn render_conversation_header(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let session = self.active_session.as_ref();
        let title = session
            .filter(|session| !session.name.is_empty())
            .map(|session| session.name.as_str())
            .or_else(|| session.map(|session| session.session_id.as_str()))
            .unwrap_or("conversation");
        frame.render_widget(
            Paragraph::new(vec![
                Line::from(vec![
                    Span::styled(
                        "CARINA",
                        Style::default()
                            .fg(self.theme.brand)
                            .add_modifier(Modifier::BOLD),
                    ),
                    Span::styled(format!("  {title}"), Style::default().fg(self.theme.text)),
                ]),
                Line::from(Span::styled(
                    self.options.workspace.display().to_string(),
                    Style::default().fg(self.theme.muted),
                )),
            ])
            .block(
                Block::default()
                    .borders(Borders::BOTTOM)
                    .border_style(Style::default().fg(self.theme.border)),
            ),
            area,
        );
        let mut labels = vec![
            ("Sessions", Action::OpenSessions, 9_101_u64),
            ("Model", Action::OpenModels, 9_102_u64),
            ("Checkpoints", Action::OpenCheckpoints, 9_103_u64),
        ];
        if self
            .active_session
            .as_ref()
            .is_some_and(|session| session.task_status == "paused")
        {
            labels.push((
                if self.paused_resume_blocker().is_some() {
                    "Review"
                } else {
                    "Resume"
                },
                Action::ResumePausedTask,
                9_105_u64,
            ));
        }
        labels.push(("Settings", Action::OpenSettings, 9_104_u64));
        let mut right = area.right().saturating_sub(1);
        for (label, action, id) in labels.into_iter().rev() {
            let width = label.len() as u16 + 2;
            let x = right.saturating_sub(width);
            if x <= area.x.saturating_add(18) {
                break;
            }
            let rect = Rect::new(x, area.y, width, 1);
            let component = ComponentId(id);
            let style = if self.interactions.hovered(component) {
                self.theme.selected()
            } else {
                Style::default().fg(self.theme.muted)
            };
            frame.render_widget(Paragraph::new(format!(" {label} ")).style(style), rect);
            self.interactions.register(HitRegion {
                component,
                area: rect,
                action,
            });
            right = x.saturating_sub(1);
        }
    }

    fn render_transcript(&mut self, frame: &mut Frame<'_>, area: Rect) {
        self.transcript_area = area;
        if self.blocks.is_empty() {
            frame.render_widget(
                Paragraph::new("Start a task from the composer below.")
                    .style(Style::default().fg(self.theme.muted)),
                area.inner(Margin::new(2, 1)),
            );
            return;
        }
        let focus = self
            .transcript_scroll
            .min(self.blocks.len().saturating_sub(1));
        let checkpoint_anchor = match self.overlays.active() {
            Some(Overlay::Checkpoint(checkpoint)) => {
                checkpoint.selected_checkpoint().and_then(|item| {
                    self.blocks
                        .iter()
                        .rposition(|block| block.task_id == item.task_id)
                })
            }
            _ => None,
        };
        let selection_anchor = self.history_selected.or(checkpoint_anchor);
        let mut start = focus;
        let mut reserved = 0_u16;
        while start > 0 {
            let candidate = &self.blocks[start - 1];
            let body = if candidate.expanded {
                candidate.body.lines().count().clamp(1, 6) as u16
            } else {
                0
            };
            let height = 2 + body + 1;
            if reserved.saturating_add(height) > area.height.saturating_sub(2) {
                break;
            }
            reserved = reserved.saturating_add(height);
            start -= 1;
        }
        let mut y = area.y;
        for index in start..self.blocks.len() {
            if y >= area.bottom() {
                break;
            }
            let block = &self.blocks[index];
            let body_lines = if block.expanded {
                block.body.lines().count().clamp(1, 6) as u16
            } else {
                0
            };
            let height = (2 + body_lines).min(area.bottom().saturating_sub(y));
            if height == 0 {
                break;
            }
            let block_area = Rect::new(area.x + 1, y, area.width.saturating_sub(2), height);
            let component = ComponentId(4_000 + index as u64);
            let hovered = self.interactions.hovered(component);
            let dimmed = selection_anchor.is_some_and(|anchor| index > anchor);
            let (mut accent, label) = match block.kind {
                BlockKind::User => (self.theme.accent, "YOU"),
                BlockKind::Assistant => (self.theme.success, "CARINA"),
                BlockKind::Thinking => (self.theme.muted, "THINKING"),
                BlockKind::Tool => (self.theme.warning, "TOOL"),
                BlockKind::Governance => (self.theme.danger, "ACTION"),
                BlockKind::Status => (self.theme.muted, "STATUS"),
                BlockKind::Diagnostic => (self.theme.danger, "DIAGNOSTIC"),
            };
            if dimmed {
                accent = self.theme.muted;
            }
            let text_color = if dimmed {
                self.theme.muted
            } else {
                self.theme.text
            };
            let mut lines = vec![Line::from(vec![
                Span::styled(
                    label,
                    Style::default().fg(accent).add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    format!("  {}", block.title),
                    Style::default().fg(text_color),
                ),
                Span::styled(
                    format!("  {}", block.status),
                    Style::default().fg(self.theme.muted),
                ),
            ])];
            if block.expanded {
                lines.extend(
                    block.body.lines().take(6).map(|line| {
                        Line::from(Span::styled(line, Style::default().fg(text_color)))
                    }),
                );
            }
            let style = if block.selected || checkpoint_anchor == Some(index) {
                self.theme.selected().add_modifier(Modifier::BOLD)
            } else if hovered {
                Style::default().bg(self.theme.raised)
            } else {
                Style::default().bg(self.theme.background)
            };
            frame.render_widget(
                Paragraph::new(lines)
                    .style(style)
                    .wrap(Wrap { trim: false })
                    .block(
                        Block::default()
                            .borders(Borders::LEFT)
                            .border_style(Style::default().fg(accent)),
                    ),
                block_area,
            );
            self.interactions.register(HitRegion {
                component,
                area: block_area,
                action: if self.history_selected.is_some()
                    && block.kind == BlockKind::User
                    && block.branchable
                {
                    Action::SelectHistory(index)
                } else {
                    Action::ToggleBlock(index)
                },
            });
            let gap = if matches!(
                block.kind,
                BlockKind::Thinking | BlockKind::Tool | BlockKind::Status
            ) {
                0
            } else {
                1
            };
            y = y.saturating_add(height + gap);
        }
    }

    fn render_composer(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let focused = self.focus == Focus::Composer;
        let block = Block::default()
            .borders(Borders::ALL)
            .border_style(if focused {
                self.theme.focus()
            } else {
                Style::default().fg(self.theme.border)
            })
            .title(" Message ");
        let inner = block.inner(area);
        frame.render_widget(block, area);
        StatefulWidgetRef::render_ref(
            &&self.composer,
            inner,
            frame.buffer_mut(),
            &mut self.composer_state,
        );
        self.composer_area = inner;
        self.interactions.register(HitRegion {
            component: ComponentId(9_000),
            area,
            action: Action::FocusComposer,
        });
        if focused
            && self.overlays.active().is_none()
            && let Some((x, y)) = self
                .composer
                .cursor_pos_with_state(inner, self.composer_state)
        {
            frame.set_cursor_position(Position::new(x, y));
        }
    }

    fn render_status(&self, frame: &mut Frame<'_>, area: Rect) {
        let session = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.as_str())
            .unwrap_or("-");
        let left = format!("{}  {}  {}", self.task_status, self.selected_model, session);
        let right = if self.notice.is_empty() {
            "Enter submit  Ctrl+C cancel/exit  double-Esc edit history  Ctrl+, settings"
        } else {
            self.notice.as_str()
        };
        let available = area.width.saturating_sub(left.len() as u16 + 2) as usize;
        let right = if right.len() > available {
            &right[..right.floor_char_boundary(available)]
        } else {
            right
        };
        frame.render_widget(
            Paragraph::new(Line::from(vec![
                Span::styled(left, Style::default().fg(self.theme.muted)),
                Span::raw("  "),
                Span::styled(right, Style::default().fg(self.theme.accent)),
            ])),
            area,
        );
    }

    fn render_overlay(&mut self, frame: &mut Frame<'_>, area: Rect, overlay: &Overlay) {
        match overlay {
            Overlay::Approval(approval) => {
                let popup = centered(area, 78, 26);
                frame.render_widget(Clear, popup);
                let inner = Block::default()
                    .borders(Borders::ALL)
                    .border_style(Style::default().fg(self.theme.danger))
                    .title(" Approval required ")
                    .inner(popup);
                frame.render_widget(
                    Block::default()
                        .borders(Borders::ALL)
                        .border_style(Style::default().fg(self.theme.danger))
                        .title(" Approval required ")
                        .style(Style::default().bg(self.theme.raised)),
                    popup,
                );
                let chunks = Layout::default()
                    .direction(Direction::Vertical)
                    .constraints([
                        Constraint::Length(5),
                        Constraint::Min(4),
                        Constraint::Length(2),
                        Constraint::Length(2),
                    ])
                    .split(inner);
                frame.render_widget(
                    Paragraph::new(vec![
                        Line::from(Span::styled(
                            first_non_empty(&approval.label, &approval.capability, "Action"),
                            self.theme.focus().add_modifier(Modifier::BOLD),
                        )),
                        Line::from(Span::styled(
                            approval.resource.as_str(),
                            Style::default().fg(self.theme.text),
                        )),
                        Line::from(Span::styled(
                            approval.reason.as_str(),
                            Style::default().fg(self.theme.muted),
                        )),
                    ])
                    .wrap(Wrap { trim: false }),
                    chunks[0],
                );
                let review = if approval.diff.is_empty() {
                    "No diff is attached. Review the capability and resource above."
                } else {
                    approval.diff.as_str()
                };
                frame.render_widget(
                    Paragraph::new(review)
                        .scroll((approval.scroll.min(u16::MAX as usize) as u16, 0))
                        .wrap(Wrap { trim: false })
                        .block(
                            Block::default()
                                .borders(Borders::TOP)
                                .border_style(Style::default().fg(self.theme.border)),
                        ),
                    chunks[1],
                );
                let scope = ApprovalScope::ALL
                    .into_iter()
                    .map(|item| {
                        let style = if item == approval.scope {
                            self.theme.selected()
                        } else {
                            Style::default().fg(self.theme.muted)
                        };
                        Span::styled(format!(" {} ", item.label()), style)
                    })
                    .collect::<Vec<_>>();
                frame.render_widget(Paragraph::new(Line::from(scope)), chunks[2]);
                self.render_modal_buttons(
                    frame,
                    chunks[3],
                    &[
                        ("Allow", Action::ApprovalAllow, 10_001),
                        ("Deny", Action::ApprovalDeny, 10_002),
                    ],
                );
                if !approval.error.is_empty() {
                    frame.render_widget(
                        Paragraph::new(approval.error.as_str())
                            .style(Style::default().fg(self.theme.danger)),
                        Rect::new(inner.x, inner.bottom().saturating_sub(1), inner.width, 1),
                    );
                }
            }
            Overlay::Question(question) => {
                let popup = centered(area, 72, 22);
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_style(self.theme.focus())
                    .title(" Carina needs your input ")
                    .style(Style::default().bg(self.theme.raised));
                let inner = block.inner(popup);
                frame.render_widget(block, popup);
                let chunks = Layout::default()
                    .direction(Direction::Vertical)
                    .constraints([
                        Constraint::Length(4),
                        Constraint::Min(5),
                        Constraint::Length(2),
                    ])
                    .split(inner);
                frame.render_widget(
                    Paragraph::new(question.prompt.as_str())
                        .style(Style::default().fg(self.theme.text))
                        .wrap(Wrap { trim: false }),
                    chunks[0],
                );
                if question.options.is_empty() {
                    let input = Block::default()
                        .borders(Borders::ALL)
                        .border_style(self.theme.focus())
                        .title(" Answer ");
                    let input_inner = input.inner(chunks[1]);
                    frame.render_widget(input, chunks[1]);
                    frame.render_widget(
                        Paragraph::new(question.input.as_str())
                            .style(Style::default().fg(self.theme.text)),
                        input_inner,
                    );
                    frame.set_cursor_position(Position::new(
                        input_inner.x
                            + question
                                .input
                                .chars()
                                .count()
                                .min(input_inner.width.saturating_sub(1) as usize)
                                as u16,
                        input_inner.y,
                    ));
                } else {
                    for (index, option) in question
                        .options
                        .iter()
                        .enumerate()
                        .take(chunks[1].height as usize)
                    {
                        let row =
                            Rect::new(chunks[1].x, chunks[1].y + index as u16, chunks[1].width, 1);
                        let component = ComponentId(11_000 + index as u64);
                        let style =
                            if index == question.selected || self.interactions.hovered(component) {
                                self.theme.selected()
                            } else {
                                Style::default().fg(self.theme.text)
                            };
                        let description = if option.description.is_empty() {
                            String::new()
                        } else {
                            format!("  {}", option.description)
                        };
                        frame.render_widget(
                            Paragraph::new(format!(
                                "{} {}{}",
                                if index == question.selected { ">" } else { " " },
                                option.label,
                                description
                            ))
                            .style(style),
                            row,
                        );
                        self.interactions.register(HitRegion {
                            component,
                            area: row,
                            action: Action::QuestionOption(index),
                        });
                    }
                }
                frame.render_widget(
                    Paragraph::new(if question.error.is_empty() {
                        "Enter answer  Ctrl+C cancel task"
                    } else {
                        question.error.as_str()
                    })
                    .style(Style::default().fg(
                        if question.error.is_empty() {
                            self.theme.muted
                        } else {
                            self.theme.danger
                        },
                    )),
                    chunks[2],
                );
            }
            Overlay::Checkpoint(checkpoint) => {
                let popup = bottom_sheet(area, 84, 22);
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_style(self.theme.focus())
                    .title(" Checkpoint recovery ")
                    .style(Style::default().bg(self.theme.raised));
                let inner = block.inner(popup);
                frame.render_widget(block, popup);
                match &checkpoint.step {
                    CheckpointStep::LoadingList => {
                        let chunks = Layout::default()
                            .direction(Direction::Vertical)
                            .constraints([Constraint::Min(3), Constraint::Length(3)])
                            .split(inner);
                        frame.render_widget(
                            Paragraph::new(vec![
                                Line::from(Span::styled(
                                    "Loading checkpoints...",
                                    self.theme.focus().add_modifier(Modifier::BOLD),
                                )),
                                Line::from(Span::styled(
                                    "The conversation remains visible while recovery data loads.",
                                    Style::default().fg(self.theme.muted),
                                )),
                            ]),
                            chunks[0],
                        );
                        self.render_checkpoint_footer(
                            frame,
                            chunks[1],
                            &checkpoint.error,
                            &[("Close", Action::CloseOverlay, 13_899)],
                        );
                    }
                    CheckpointStep::List => {
                        let chunks = Layout::default()
                            .direction(Direction::Vertical)
                            .constraints([
                                Constraint::Length(3),
                                Constraint::Min(5),
                                Constraint::Length(3),
                            ])
                            .split(inner);
                        frame.render_widget(
                            Paragraph::new(
                                "Choose a durable task checkpoint. Preview is required before restore.",
                            )
                            .style(Style::default().fg(self.theme.text))
                            .wrap(Wrap { trim: false }),
                            chunks[0],
                        );
                        if checkpoint.checkpoints.is_empty() {
                            frame.render_widget(
                                Paragraph::new("No checkpoints are available for this session.")
                                    .style(Style::default().fg(self.theme.muted)),
                                chunks[1],
                            );
                        } else {
                            let capacity = chunks[1].height as usize;
                            let selected_visual = checkpoint
                                .checkpoints
                                .len()
                                .saturating_sub(1)
                                .saturating_sub(checkpoint.selected);
                            let window_start =
                                selected_visual.saturating_sub(capacity.saturating_sub(1));
                            for (visual, (index, item)) in checkpoint
                                .checkpoints
                                .iter()
                                .enumerate()
                                .rev()
                                .skip(window_start)
                                .take(capacity)
                                .enumerate()
                            {
                                let row = Rect::new(
                                    chunks[1].x,
                                    chunks[1].y + visual as u16,
                                    chunks[1].width,
                                    1,
                                );
                                let component = ComponentId(13_000 + index as u64);
                                let style = if index == checkpoint.selected
                                    || self.interactions.hovered(component)
                                {
                                    self.theme.selected()
                                } else {
                                    Style::default().fg(self.theme.text)
                                };
                                let summary = if item.summary.is_empty() {
                                    "No summary"
                                } else {
                                    item.summary.as_str()
                                };
                                frame.render_widget(
                                    Paragraph::new(format!(
                                        "{} turn {:>3}  {}  {}",
                                        if index == checkpoint.selected {
                                            ">"
                                        } else {
                                            " "
                                        },
                                        item.turn,
                                        item.checkpoint_id,
                                        summary
                                    ))
                                    .style(style),
                                    row,
                                );
                                self.interactions.register(HitRegion {
                                    component,
                                    area: row,
                                    action: Action::SelectCheckpoint(index),
                                });
                            }
                        }
                        self.render_checkpoint_footer(
                            frame,
                            chunks[2],
                            &checkpoint.error,
                            &[
                                ("Close", Action::CloseOverlay, 13_900),
                                ("Retry", Action::OpenCheckpoints, 13_902),
                                ("Preview", Action::PreviewCheckpoint, 13_901),
                            ],
                        );
                    }
                    CheckpointStep::Previewing { checkpoint_id } => {
                        let chunks = Layout::default()
                            .direction(Direction::Vertical)
                            .constraints([Constraint::Min(3), Constraint::Length(3)])
                            .split(inner);
                        frame.render_widget(
                            Paragraph::new(vec![
                                Line::from(Span::styled(
                                    "Loading restore impact...",
                                    self.theme.focus().add_modifier(Modifier::BOLD),
                                )),
                                Line::from(format!("Checkpoint {checkpoint_id}")),
                            ]),
                            chunks[0],
                        );
                        self.render_checkpoint_footer(
                            frame,
                            chunks[1],
                            &checkpoint.error,
                            &[("Back", Action::CheckpointBack, 13_909)],
                        );
                    }
                    CheckpointStep::Preview(preview)
                    | CheckpointStep::Confirm(preview)
                    | CheckpointStep::Restoring(preview) => {
                        let chunks = Layout::default()
                            .direction(Direction::Vertical)
                            .constraints([
                                Constraint::Length(5),
                                Constraint::Min(7),
                                Constraint::Length(3),
                            ])
                            .split(inner);
                        let title = match checkpoint.step {
                            CheckpointStep::Confirm(_) => "Confirm destructive restore",
                            CheckpointStep::Restoring(_) => "Restoring checkpoint",
                            _ => "Review restore impact",
                        };
                        frame.render_widget(
                            Paragraph::new(vec![
                                Line::from(Span::styled(
                                    title,
                                    self.theme.focus().add_modifier(Modifier::BOLD),
                                )),
                                Line::from(format!(
                                    "{}  turn {}  {} conversation turns",
                                    preview.checkpoint.checkpoint_id,
                                    preview.checkpoint.turn,
                                    preview.conversation_turns
                                )),
                                Line::from(format!(
                                    "After restore: task remains {}",
                                    preview.will_resume
                                )),
                            ]),
                            chunks[0],
                        );
                        let rollback = if preview.rollback_patches.is_empty() {
                            "Full restore: conversation moves back; no later workspace patches"
                        } else {
                            "Full restore: conversation and later workspace patches move back"
                        };
                        let mut lines = vec![
                            Line::from(Span::styled(
                                preview.summary.as_str(),
                                Style::default().fg(self.theme.text),
                            )),
                            Line::from(Span::styled(
                                rollback,
                                Style::default().fg(self.theme.warning),
                            )),
                        ];
                        lines.extend(
                            preview
                                .rollback_patches
                                .iter()
                                .map(|patch| Line::from(format!("  {patch}"))),
                        );
                        if matches!(&checkpoint.step, CheckpointStep::Confirm(_)) {
                            lines.push(Line::from(Span::styled(
                                "This changes the workspace and pauses the restored task.",
                                Style::default()
                                    .fg(self.theme.danger)
                                    .add_modifier(Modifier::BOLD),
                            )));
                        }
                        frame.render_widget(
                            Paragraph::new(lines).wrap(Wrap { trim: false }),
                            chunks[1],
                        );
                        if matches!(&checkpoint.step, CheckpointStep::Restoring(_)) {
                            frame.render_widget(
                                Paragraph::new(
                                    "Restoring... This operation cannot be hidden after it starts.",
                                )
                                .style(Style::default().fg(self.theme.warning)),
                                chunks[2],
                            );
                        } else {
                            let buttons = if matches!(&checkpoint.step, CheckpointStep::Confirm(_))
                            {
                                vec![
                                    ("Back", Action::CheckpointBack, 13_910),
                                    ("Retry", Action::ConfirmCheckpointRestore, 13_914),
                                    ("Confirm restore", Action::ConfirmCheckpointRestore, 13_911),
                                ]
                            } else {
                                vec![
                                    ("Back", Action::CheckpointBack, 13_912),
                                    ("Restore", Action::BeginCheckpointRestore, 13_913),
                                ]
                            };
                            self.render_checkpoint_footer(
                                frame,
                                chunks[2],
                                &checkpoint.error,
                                &buttons,
                            );
                        }
                    }
                    CheckpointStep::Restored(result) | CheckpointStep::Resuming(result) => {
                        let chunks = Layout::default()
                            .direction(Direction::Vertical)
                            .constraints([
                                Constraint::Length(5),
                                Constraint::Min(5),
                                Constraint::Length(3),
                            ])
                            .split(inner);
                        frame.render_widget(
                            Paragraph::new(vec![
                                Line::from(Span::styled(
                                    "Checkpoint restored",
                                    self.theme.focus().add_modifier(Modifier::BOLD),
                                )),
                                Line::from(format!(
                                    "{}  turn {}  task {}",
                                    result.checkpoint_id, result.turn, result.task_id
                                )),
                                Line::from(format!("Task status: {}", result.status)),
                            ]),
                            chunks[0],
                        );
                        let body = if result.rolled_back.is_empty() {
                            "No workspace patches needed rollback.".to_owned()
                        } else {
                            format!(
                                "Rolled back {} patch{}:\n{}",
                                result.rolled_back.len(),
                                if result.rolled_back.len() == 1 {
                                    ""
                                } else {
                                    "es"
                                },
                                result.rolled_back.join("\n")
                            )
                        };
                        frame.render_widget(
                            Paragraph::new(body)
                                .style(Style::default().fg(self.theme.text))
                                .wrap(Wrap { trim: false }),
                            chunks[1],
                        );
                        if matches!(&checkpoint.step, CheckpointStep::Resuming(_)) {
                            frame.render_widget(
                                Paragraph::new("Resuming task...")
                                    .style(Style::default().fg(self.theme.warning)),
                                chunks[2],
                            );
                        } else {
                            self.render_checkpoint_footer(
                                frame,
                                chunks[2],
                                &checkpoint.error,
                                &[
                                    ("Done", Action::CloseOverlay, 13_920),
                                    ("Retry", Action::ResumeRestoredTask, 13_922),
                                    ("Resume task", Action::ResumeRestoredTask, 13_921),
                                ],
                            );
                        }
                    }
                }
            }
            Overlay::Settings(settings) => {
                let popup = centered(area, 54, 16);
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_style(self.theme.focus())
                    .title(" Settings ")
                    .style(Style::default().bg(self.theme.raised));
                let inner = block.inner(popup);
                frame.render_widget(block, popup);
                self.render_rows(
                    frame,
                    inner.inner(Margin::new(1, 1)),
                    vec![
                        format!(
                            "Language        {}",
                            self.options.locale.as_deref().unwrap_or("not set")
                        ),
                        format!("Model           {}", self.selected_model),
                        "Sessions        switch or create".into(),
                        "Checkpoints     preview and restore".into(),
                        if self.paused_resume_blocker().is_some() {
                            "Review          continuity proof required".into()
                        } else if self
                            .active_session
                            .as_ref()
                            .is_some_and(|session| session.task_status == "paused")
                        {
                            "Resume          continue paused task".into()
                        } else {
                            "Resume          no paused task".into()
                        },
                        "Close".into(),
                    ],
                    settings.selected,
                    12_000,
                    |index| match index {
                        0 => Some(Action::OpenLocale),
                        1 => Some(Action::OpenModels),
                        2 => Some(Action::OpenSessions),
                        3 => Some(Action::OpenCheckpoints),
                        4 => Some(Action::ResumePausedTask),
                        5 => Some(Action::CloseOverlay),
                        _ => None,
                    },
                );
            }
        }
    }

    fn render_modal_buttons(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        buttons: &[(&str, Action, u64)],
    ) {
        let mut x = area.x;
        for (label, action, id) in buttons {
            let width = label.len() as u16 + 4;
            let rect = Rect::new(x, area.y, width.min(area.right().saturating_sub(x)), 1);
            let component = ComponentId(*id);
            let style = if self.interactions.hovered(component) {
                self.theme.selected()
            } else {
                Style::default()
                    .fg(self.theme.accent)
                    .bg(self.theme.background)
            };
            frame.render_widget(Paragraph::new(format!("[ {label} ]")).style(style), rect);
            self.interactions.register(HitRegion {
                component,
                area: rect,
                action: action.clone(),
            });
            x = x.saturating_add(width + 1);
        }
    }

    fn render_checkpoint_footer(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        error: &str,
        buttons: &[(&str, Action, u64)],
    ) {
        let button_y = if error.is_empty() {
            area.y
        } else {
            frame.render_widget(
                Paragraph::new(error).style(Style::default().fg(self.theme.danger)),
                Rect::new(area.x, area.y, area.width, 1),
            );
            area.y.saturating_add(1)
        };
        let visible = buttons
            .iter()
            .filter(|(label, _, _)| *label != "Retry" || !error.is_empty())
            .cloned()
            .collect::<Vec<_>>();
        self.render_modal_buttons(
            frame,
            Rect::new(
                area.x,
                button_y,
                area.width,
                area.bottom().saturating_sub(button_y),
            ),
            &visible,
        );
    }
}

fn centered(area: Rect, max_width: u16, max_height: u16) -> Rect {
    let width = area.width.min(max_width).max(1);
    let height = area.height.min(max_height).max(1);
    Rect::new(
        area.x + area.width.saturating_sub(width) / 2,
        area.y + area.height.saturating_sub(height) / 2,
        width,
        height,
    )
}

fn bottom_sheet(area: Rect, max_width: u16, max_height: u16) -> Rect {
    let width = area.width.saturating_sub(2).min(max_width).max(1);
    let height = area.height.saturating_sub(3).min(max_height).max(1);
    Rect::new(
        area.x + area.width.saturating_sub(width) / 2,
        area.bottom().saturating_sub(height).saturating_sub(1),
        width,
        height,
    )
}

fn first_non_empty<'a>(first: &'a str, second: &'a str, fallback: &'a str) -> &'a str {
    if !first.is_empty() {
        first
    } else if !second.is_empty() {
        second
    } else {
        fallback
    }
}
