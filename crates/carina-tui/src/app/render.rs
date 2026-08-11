use std::time::Duration;

#[cfg(test)]
use std::collections::HashMap;

use ratatui::Frame;
use ratatui::layout::{Alignment, Constraint, Direction, Layout, Margin, Position, Rect};
use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{
    Block, Borders, Clear, Paragraph, Scrollbar, ScrollbarOrientation, ScrollbarState,
    StatefulWidgetRef, Wrap,
};
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};
use xai_ratatui_textarea::wrapping::{RtOptions, word_wrap_line};

use super::{
    App, Focus, LOCALES, Phase, ScreenMode, TranscriptHeightCache, TranscriptHeightCacheEntry,
    TranscriptRenderCache, TranscriptRenderCacheEntry,
};
use crate::component::{Action, ComponentId, HitRegion, InteractionMap};
use crate::conversation::{
    ChromeCapability, ChromeSlotKind, ChromeTone, ComposerChrome, ComposerChromeInput,
    EmptyConversation, conversation_title, localized_execution_status,
};
use crate::density::DensityMode;
use crate::file_viewer::{FileViewerLoad, selection_hint};
use crate::glyphs::Glyphs;
use crate::history_search::HistoryMode;
use crate::i18n::{
    Locale, MessageId, count as tr_count, format as tr_format, localize_operator_failure_reason,
    model_health_reason_label, model_health_status_label, text as tr,
};
use crate::layout_contract;
use crate::markdown::{
    MarkdownTheme, StyledPrefix, render_prefixed as render_markdown_prefixed, wrap_styled_lines,
};
use crate::overlay::{ApprovalScope, ChangesFocus, Overlay, PlanReviewOverlay};
use crate::patch_review::{PatchDisposition, PatchReview};
use crate::prerequisite::{BrowserLayout, PickerWindow, PrerequisiteLayout};
use crate::product_header::{HeaderAction, ProductHeader};
use crate::product_projection::{
    ActivityKind, AgentCategory, ChangeKind, FileChangeKind, ProductStatus,
};
use crate::rpc::ModelProvider;
use crate::semantic_cell::SemanticCellKind;
use crate::session_browser::SessionScope;
use crate::tool_projection::{visible_group_members_with_limits, visible_output_lines_with_limits};
use crate::transcript::{
    BlockBodyKind, BlockKind, FailureAction, ToolGroupMember, TranscriptBlock, UserBlockKind,
};

impl App {
    pub(super) fn render(&mut self, frame: &mut Frame<'_>) {
        self.interactions.begin_frame();
        let area = frame.area();
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
        let layout = PrerequisiteLayout::compute(area);
        self.render_scene_header(frame, layout.header);
        match self.phase {
            Phase::Locale => self.render_locale(frame, layout.content),
            Phase::Provider => self.render_providers(frame, layout.content),
            Phase::Credential => self.render_credential(frame, layout.content),
            Phase::Model => self.render_models(frame, layout.content),
            Phase::Session => self.render_sessions(frame, layout.content),
            Phase::Diagnostic => self.render_diagnostic(frame, layout.content),
            Phase::Conversation => unreachable!(),
        }
        self.render_scene_footer(frame, layout.footer);
    }

    fn render_scene_header(&self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let phase = match self.phase {
            Phase::Locale => tr(locale, MessageId::PhaseLanguage),
            Phase::Provider => tr(locale, MessageId::PhaseProvider),
            Phase::Credential => tr(locale, MessageId::PhaseAuthentication),
            Phase::Model => tr(locale, MessageId::PhaseModel),
            Phase::Session => tr(locale, MessageId::PhaseRecovery),
            Phase::Diagnostic => tr(locale, MessageId::PhaseDiagnostics),
            Phase::Conversation => tr(locale, MessageId::PhaseConversation),
        };
        let (provider, model, reasoning) = self.product_header_metadata();
        ProductHeader {
            locale,
            title: None,
            phase: Some(phase),
            provider: &provider,
            model: &model,
            reasoning: &reasoning,
            workspace: &self.options.workspace,
            actions: &[],
        }
        .render(frame, area, self.theme, &mut InteractionMap::default());
    }

    fn render_locale(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let layout = BrowserLayout::compute(area);
        self.render_scene_title(
            frame,
            layout.title,
            tr(locale, MessageId::ChooseLanguage),
            tr(locale, MessageId::ChooseLanguageDetail),
        );
        let list_area = self.render_browser_panel(
            frame,
            layout.list,
            &format!(" {} ", tr(locale, MessageId::Languages)),
        );
        self.render_rows(
            frame,
            list_area,
            LOCALES
                .iter()
                .map(|(id, name)| {
                    format!(
                        "{name:<width$} {id}",
                        width = layout_contract::SLASH_COMMAND_LABEL_WIDTH
                    )
                })
                .collect(),
            self.locale_index,
            500,
            |index| Some(Action::SelectLocale(index)),
        );
        if let Some(detail) = layout.detail {
            let detail = self.render_browser_panel(
                frame,
                detail,
                &format!(" {} ", tr(locale, MessageId::Preview)),
            );
            let (id, name) = LOCALES[self.locale_index];
            frame.render_widget(
                Paragraph::new(vec![
                    Line::from(Span::styled(
                        tr(locale, MessageId::InterfaceLanguage),
                        Style::default()
                            .fg(self.theme.muted)
                            .add_modifier(Modifier::BOLD),
                    )),
                    Line::from(""),
                    Line::from(Span::styled(
                        name,
                        Style::default()
                            .fg(self.theme.text)
                            .add_modifier(Modifier::BOLD),
                    )),
                    Line::from(Span::styled(id, Style::default().fg(self.theme.muted))),
                ]),
                detail,
            );
            let button = Rect::new(
                detail.x,
                detail
                    .y
                    .saturating_add(layout_contract::PROVIDER_DETAIL_CURSOR_OFFSET)
                    .min(detail.bottom().saturating_sub(layout_contract::ROW_HEIGHT)),
                7,
                1,
            );
            let component = ComponentId(954);
            frame.render_widget(
                Paragraph::new(format!(" {} ", tr(locale, MessageId::Apply))).style(
                    if self.interactions.hovered(component) {
                        self.theme.selected()
                    } else {
                        self.theme.focus()
                    },
                ),
                button,
            );
            self.interactions.register(HitRegion {
                component,
                area: button,
                action: Action::SelectLocale(self.locale_index),
            });
        }
    }

    fn render_providers(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let layout = BrowserLayout::compute(area);
        let (list_area, compact_detail) = layout.with_compact_detail();
        self.render_scene_title(
            frame,
            layout.title,
            tr(locale, MessageId::ConnectProvider),
            tr(locale, MessageId::ConnectProviderDetail),
        );
        let visible = self
            .provider_picker
            .visible_indices(&self.inventory.providers);

        let list_panel = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(Style::default().fg(self.theme.border))
            .style(Style::default().fg(self.theme.text))
            .title(Line::from(vec![
                Span::styled(
                    format!(" {} ", tr(locale, MessageId::Providers)),
                    Style::default()
                        .fg(self.theme.text)
                        .add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    format!(" {} of {} ", visible.len(), self.inventory.providers.len()),
                    Style::default().fg(self.theme.muted),
                ),
            ]));
        let list_inner = list_panel.inner(list_area).inner(Margin::new(1, 0));
        frame.render_widget(list_panel, list_area);
        let [search_area, rows_area] = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(layout_contract::CONTROL_HEIGHT),
                Constraint::Min(layout_contract::PROVIDER_ROWS_MIN_HEIGHT),
            ])
            .areas(list_inner);

        let search_component = ComponentId(950);
        let search_focused = self.provider_picker.search_active();
        let search_hovered = self.interactions.hovered(search_component);
        let search_style = if search_focused || search_hovered {
            Style::default().fg(self.theme.text)
        } else {
            self.theme.muted()
        };
        let search_inner = Rect::new(
            search_area.x,
            search_area.y,
            search_area.width,
            layout_contract::ROW_HEIGHT,
        );
        frame.render_widget(Block::default().style(search_style), search_inner);
        let search_text = if self.provider_picker.query().is_empty() && !search_focused {
            Line::from(vec![
                Span::styled("/  ", self.theme.keycap()),
                Span::styled(
                    tr_format(
                        locale,
                        MessageId::SearchProviders,
                        &[("count", &self.inventory.providers.len().to_string())],
                    ),
                    Style::default().fg(self.theme.muted),
                ),
            ])
        } else {
            Line::from(vec![
                Span::styled("/  ", self.theme.focus()),
                Span::styled(
                    self.provider_picker.query(),
                    Style::default().fg(self.theme.text),
                ),
            ])
        };
        frame.render_widget(Paragraph::new(search_text), search_inner);
        frame.render_widget(
            Paragraph::new(self.theme.glyphs.rule().repeat(search_area.width as usize))
                .style(Style::default().fg(self.theme.border)),
            Rect::new(
                search_area.x,
                search_area.y + layout_contract::ROW_HEIGHT,
                search_area.width,
                layout_contract::ROW_HEIGHT,
            ),
        );
        self.interactions.register(HitRegion {
            component: search_component,
            area: search_inner,
            action: Action::FocusProviderSearch,
        });
        if search_focused && search_inner.width > 0 {
            let query = self.provider_picker.query();
            let cursor_offset = (3usize + UnicodeWidthStr::width(query)).min(
                search_inner
                    .width
                    .saturating_sub(layout_contract::ROW_HEIGHT) as usize,
            );
            frame.set_cursor_position(Position::new(
                search_inner.x.saturating_add(cursor_offset as u16),
                search_inner.y,
            ));
        }

        if visible.is_empty() {
            frame.render_widget(
                Paragraph::new(vec![
                    Line::from(Span::styled(
                        tr(locale, MessageId::NoMatchingProviders),
                        Style::default()
                            .fg(self.theme.text)
                            .add_modifier(Modifier::BOLD),
                    )),
                    Line::from(Span::styled(
                        tr(locale, MessageId::NoMatchingProvidersDetail),
                        Style::default().fg(self.theme.muted),
                    )),
                ])
                .block(Block::default().padding(ratatui::widgets::Padding::new(2, 0, 1, 0))),
                rows_area,
            );
        } else {
            self.render_provider_rows(frame, rows_area, &visible);
        }

        if let Some(detail) = layout.detail {
            self.render_provider_detail(frame, detail);
        } else if let Some(detail) = compact_detail {
            self.render_provider_compact_detail(frame, detail);
        }
    }

    fn render_provider_compact_detail(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
        let Some(provider) = self.inventory.providers.get(self.provider_index) else {
            return;
        };
        let import_reviewing = self.provider_import.is_reviewing(&provider.id);
        let validation_elapsed = self.provider_import.validation_elapsed(&provider.id);
        let import_validating = validation_elapsed.is_some();
        let import_failed = self.provider_import.failure(&provider.id).is_some();
        let import_active = import_reviewing || import_validating || import_failed;
        let validation_glyph = validation_spinner(validation_elapsed);
        let is_ccswitch = provider.source_kind == "cc-switch";
        let active_route = provider.source_route == "managed_proxy";
        let execution_ready = self.inventory.is_provider_runnable(provider);
        let has_compatible_models = provider_has_compatible_models(provider);
        let (state_glyph, state, state_color) = if execution_ready && !has_compatible_models {
            (
                self.theme.glyphs.empty(),
                tr(locale, MessageId::Unavailable),
                self.theme.warning,
            )
        } else if execution_ready {
            (
                self.theme.glyphs.ready(),
                tr(locale, MessageId::Ready),
                self.theme.success,
            )
        } else if provider.registered && provider.available {
            (
                self.theme.glyphs.empty(),
                tr(locale, MessageId::RuntimeUnavailable),
                self.theme.warning,
            )
        } else if import_validating {
            (
                validation_glyph,
                tr(locale, MessageId::ValidatingProvider),
                self.theme.accent,
            )
        } else if import_failed {
            ("!", tr(locale, MessageId::ImportFailed), self.theme.danger)
        } else if is_ccswitch && provider.source_importable && import_reviewing {
            (
                self.theme.glyphs.diamond_solid(),
                if active_route {
                    tr(locale, MessageId::ReadyToUse)
                } else {
                    tr(locale, MessageId::ReadyToImport)
                },
                self.theme.accent,
            )
        } else if is_ccswitch && provider.source_importable {
            (
                self.theme.glyphs.diamond_solid(),
                if active_route {
                    tr(locale, MessageId::ActiveRoute)
                } else {
                    tr(locale, MessageId::SavedProfile)
                },
                self.theme.accent,
            )
        } else if is_ccswitch {
            (
                self.theme.glyphs.diamond_open(),
                if provider.source_auth_mode == "cli_oauth" {
                    tr(locale, MessageId::CliOauth)
                } else {
                    tr(locale, MessageId::Unavailable)
                },
                self.theme.muted,
            )
        } else if provider.registered {
            (
                self.theme.glyphs.empty(),
                tr(locale, MessageId::CredentialRequired),
                self.theme.warning,
            )
        } else {
            (
                self.theme.glyphs.unavailable(),
                tr(locale, MessageId::NotRegistered),
                self.theme.muted,
            )
        };
        let action_label = if import_validating {
            tr(locale, MessageId::Validating)
        } else if import_failed {
            tr(locale, MessageId::Retry)
        } else if import_reviewing {
            if active_route {
                tr(locale, MessageId::ConfirmRoute)
            } else {
                tr(locale, MessageId::ConfirmImport)
            }
        } else if execution_ready && !has_compatible_models {
            tr(locale, MessageId::DiagnosticOnly)
        } else if execution_ready {
            tr(locale, MessageId::UseProvider)
        } else if provider.registered && provider.available {
            tr(locale, MessageId::RetryReadiness)
        } else if is_ccswitch && provider.source_importable {
            if active_route {
                tr(locale, MessageId::UseActiveRoute)
            } else {
                tr(locale, MessageId::ImportSavedProfile)
            }
        } else if is_ccswitch {
            tr(locale, MessageId::ViewDetails)
        } else {
            tr(locale, MessageId::Connect)
        };
        let disabled = import_validating
            || (execution_ready && !has_compatible_models)
            || (is_ccswitch && !provider.source_importable && !provider.available);
        let name = if provider.name.is_empty() {
            provider.id.as_str()
        } else {
            provider.name.as_str()
        };
        let panel = Block::default()
            .borders(Borders::TOP)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(Style::default().fg(self.theme.border))
            .style(Style::default().fg(self.theme.text))
            .title(Span::styled(
                format!(" {} ", tr(locale, MessageId::Connection)),
                Style::default()
                    .fg(self.theme.text)
                    .add_modifier(Modifier::BOLD),
            ));
        let inner = panel.inner(area);
        frame.render_widget(panel, area);

        let action_width = label_button_width(action_label).min(inner.width);
        let summary_width = inner
            .width
            .saturating_sub(action_width.saturating_add(layout_contract::BLOCK_PADDING));
        frame.render_widget(
            Paragraph::new(Line::from(vec![
                Span::styled(format!("{state_glyph} "), Style::default().fg(state_color)),
                Span::styled(
                    name,
                    Style::default()
                        .fg(self.theme.text)
                        .add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    format!("  {sep}  {state}"),
                    Style::default().fg(self.theme.muted),
                ),
            ])),
            Rect::new(
                inner.x,
                inner.y,
                summary_width,
                inner.height.min(layout_contract::ROW_HEIGHT),
            ),
        );
        let button = Rect::new(
            inner.right().saturating_sub(action_width),
            inner.y,
            action_width,
            inner.height.min(layout_contract::ROW_HEIGHT),
        );
        let component = ComponentId(951);
        let button_style = if disabled {
            self.theme.muted()
        } else if self.interactions.hovered(component) {
            self.theme.selected()
        } else {
            self.theme.action()
        };
        frame.render_widget(
            Paragraph::new(format!(" {action_label} "))
                .style(button_style)
                .alignment(Alignment::Center),
            button,
        );
        if !disabled {
            self.interactions.register(HitRegion {
                component,
                area: button,
                action: if import_active {
                    Action::ConfirmProviderImport
                } else {
                    Action::SelectProvider(self.provider_index)
                },
            });
        }
    }

    fn render_provider_rows(&mut self, frame: &mut Frame<'_>, area: Rect, visible: &[usize]) {
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
        let content_width = area.width.saturating_sub(layout_contract::ROW_HEIGHT);
        let capacity = (area.height / layout_contract::CONTROL_HEIGHT)
            .max(layout_contract::MIN_DIMENSION) as usize;
        let selected_position = visible
            .iter()
            .position(|index| *index == self.provider_index)
            .unwrap_or(0);
        let window = PickerWindow::around(visible.len(), selected_position, capacity);

        for (slot, index) in visible
            .iter()
            .skip(window.start)
            .take(window.len)
            .enumerate()
        {
            let Some(provider) = self.inventory.providers.get(*index) else {
                continue;
            };
            let row_area = Rect::new(
                area.x,
                area.y.saturating_add((slot as u16).saturating_mul(2)),
                content_width,
                2.min(area.bottom().saturating_sub(area.y + slot as u16 * 2)),
            );
            let component = ComponentId(1_000 + *index as u64);
            let selected = *index == self.provider_index;
            let hovered = self.interactions.hovered(component);
            let row_style = if selected {
                self.theme.selected()
            } else if hovered {
                self.theme.hovered()
            } else {
                Style::default().fg(self.theme.text)
            };
            frame.render_widget(Block::default().style(row_style), row_area);

            let name = if provider.name.is_empty() {
                provider.id.as_str()
            } else {
                provider.name.as_str()
            };
            let execution_ready = self.inventory.is_provider_runnable(provider);
            let has_compatible_models = provider_has_compatible_models(provider);
            let active_route = provider.source_route == "managed_proxy";
            let (state_glyph, state, state_color) = if execution_ready && !has_compatible_models {
                (
                    self.theme.glyphs.empty(),
                    tr(locale, MessageId::Unavailable),
                    self.theme.warning,
                )
            } else if execution_ready {
                (
                    self.theme.glyphs.ready(),
                    tr(locale, MessageId::Ready),
                    self.theme.success,
                )
            } else if provider.registered && provider.available {
                (
                    self.theme.glyphs.empty(),
                    tr(locale, MessageId::RuntimeUnavailable),
                    self.theme.warning,
                )
            } else if provider.source_kind == "cc-switch" && provider.source_importable {
                (
                    self.theme.glyphs.diamond_solid(),
                    if active_route {
                        tr(locale, MessageId::ActiveRoute)
                    } else {
                        tr(locale, MessageId::SavedProfile)
                    },
                    self.theme.accent,
                )
            } else if provider.source_kind == "cc-switch" {
                (
                    self.theme.glyphs.diamond_open(),
                    if provider.source_auth_mode == "cli_oauth" {
                        tr(locale, MessageId::CliOauth)
                    } else {
                        tr(locale, MessageId::Unavailable)
                    },
                    self.theme.muted,
                )
            } else if provider.registered {
                (
                    self.theme.glyphs.empty(),
                    tr(locale, MessageId::CredentialRequired),
                    self.theme.warning,
                )
            } else {
                (
                    self.theme.glyphs.unavailable(),
                    tr(locale, MessageId::NotRegistered),
                    self.theme.muted,
                )
            };
            let marker_style = row_style.patch(Style::default().fg(if selected {
                self.theme.accent
            } else {
                self.theme.border
            }));
            let name_style = row_style
                .patch(Style::default().fg(self.theme.text))
                .add_modifier(if selected {
                    Modifier::BOLD
                } else {
                    Modifier::empty()
                });
            let marker = if selected {
                self.theme.glyphs.selected()
            } else {
                "  "
            };
            // Use terminal cell width, not char count. CJK labels like 就绪
            // are 2 cells each; char-count layout truncated them to the first glyph.
            let state_width = provider_state_column_width(state_glyph, state);
            let show_right_state = row_area.width
                > state_width.saturating_add(layout_contract::PROVIDER_STATE_RESERVE);
            let name_width = if show_right_state {
                row_area.width.saturating_sub(
                    state_width.saturating_add(layout_contract::PROVIDER_STATE_GUTTER),
                )
            } else {
                row_area.width
            };
            frame.render_widget(
                Paragraph::new(Line::from(vec![
                    Span::styled(marker, marker_style),
                    Span::styled(name, name_style),
                ])),
                Rect::new(
                    row_area.x,
                    row_area.y,
                    name_width,
                    layout_contract::ROW_HEIGHT,
                ),
            );
            if show_right_state {
                frame.render_widget(
                    Paragraph::new(Line::from(vec![
                        Span::styled(
                            format!("{state_glyph} "),
                            row_style.patch(Style::default().fg(state_color)),
                        ),
                        Span::styled(
                            state,
                            row_style.patch(Style::default().fg(self.theme.muted)),
                        ),
                    ]))
                    .alignment(Alignment::Right),
                    Rect::new(
                        row_area.right().saturating_sub(
                            state_width.saturating_add(layout_contract::PROVIDER_STATE_GUTTER),
                        ),
                        row_area.y,
                        state_width,
                        1,
                    ),
                );
            }
            if row_area.height > layout_contract::ROW_HEIGHT {
                let model_count = if provider.source_kind == "cc-switch" && !provider.available {
                    provider.models.len()
                } else {
                    provider
                        .models
                        .iter()
                        .filter(|model| model.available)
                        .count()
                };
                let source = if provider.source_app.is_empty() {
                    if provider.source_label.is_empty() {
                        provider.id.as_str()
                    } else {
                        provider.source_label.as_str()
                    }
                } else {
                    source_app_label(&provider.source_app)
                };
                let route_badge = if active_route {
                    format!("  {sep}  {}", tr(locale, MessageId::ActiveViaProxy))
                } else if provider.source_current {
                    format!("  {sep}  {}", tr(locale, MessageId::CurrentProfile))
                } else {
                    String::new()
                };
                frame.render_widget(
                    Paragraph::new(Line::from(vec![
                        Span::styled(
                            if selected {
                                self.theme.glyphs.tree()
                            } else {
                                "  "
                            },
                            marker_style,
                        ),
                        Span::styled(
                            source,
                            row_style.patch(Style::default().fg(self.theme.muted)),
                        ),
                        Span::styled(
                            route_badge,
                            row_style.patch(Style::default().fg(self.theme.accent)),
                        ),
                        Span::styled(
                            format!(
                                "  {sep}  {}",
                                tr_count(
                                    locale,
                                    MessageId::ModelCountOne,
                                    MessageId::ModelCountOther,
                                    model_count
                                )
                            ),
                            row_style.patch(Style::default().fg(self.theme.muted)),
                        ),
                    ])),
                    Rect::new(
                        row_area.x,
                        row_area.y + layout_contract::ROW_HEIGHT,
                        row_area.width,
                        layout_contract::ROW_HEIGHT,
                    ),
                );
            }
            self.interactions.register(HitRegion {
                component,
                area: row_area,
                action: Action::SelectProvider(*index),
            });
        }

        self.render_picker_scrollbar(frame, area, visible.len(), window);
    }

    fn render_picker_scrollbar(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        total: usize,
        window: PickerWindow,
    ) {
        if total <= window.len || area.width == 0 || area.height == 0 {
            return;
        }
        let track_height = area.height as usize;
        let thumb_height =
            ((track_height * window.len) / total).max(layout_contract::MIN_DIMENSION as usize);
        let travel = track_height.saturating_sub(thumb_height);
        let max_start = total
            .saturating_sub(window.len)
            .max(layout_contract::MIN_DIMENSION as usize);
        let thumb_start = window.start.saturating_mul(travel) / max_start;
        for offset in 0..track_height {
            let in_thumb = (thumb_start..thumb_start + thumb_height).contains(&offset);
            frame.render_widget(
                Paragraph::new(if in_thumb {
                    self.theme.glyphs.scroll_thumb()
                } else {
                    self.theme.glyphs.scroll_track()
                })
                .style(Style::default().fg(if in_thumb {
                    self.theme.accent
                } else {
                    self.theme.border
                })),
                Rect::new(
                    area.right().saturating_sub(layout_contract::ROW_HEIGHT),
                    area.y + offset as u16,
                    layout_contract::ROW_HEIGHT,
                    layout_contract::ROW_HEIGHT,
                ),
            );
        }
    }

    fn render_provider_detail(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let Some(provider) = self.inventory.providers.get(self.provider_index) else {
            return;
        };
        let panel = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(Style::default().fg(self.theme.border))
            .style(Style::default().fg(self.theme.text))
            .title(Span::styled(
                format!(" {} ", tr(locale, MessageId::Connection)),
                Style::default()
                    .fg(self.theme.text)
                    .add_modifier(Modifier::BOLD),
            ));
        let inner = panel.inner(area).inner(Margin::new(2, 1));
        frame.render_widget(panel, area);
        let name = if provider.name.is_empty() {
            provider.id.as_str()
        } else {
            provider.name.as_str()
        };
        let import_reviewing = self.provider_import.is_reviewing(&provider.id);
        let validation_elapsed = self.provider_import.validation_elapsed(&provider.id);
        let import_validating = validation_elapsed.is_some();
        let import_failure = self.provider_import.failure(&provider.id);
        let import_active = import_reviewing || import_validating || import_failure.is_some();
        let validation_glyph = validation_spinner(validation_elapsed);
        let is_ccswitch = provider.source_kind == "cc-switch";
        let active_route = provider.source_route == "managed_proxy";
        let execution_ready = self.inventory.is_provider_runnable(provider);
        let has_compatible_models = provider_has_compatible_models(provider);
        let no_models_guidance = tr_format(
            locale,
            MessageId::ProviderNoCompatibleModels,
            &[("provider", name)],
        );
        let (state_glyph, state, state_color, guidance) =
            if execution_ready && !has_compatible_models {
                (
                    self.theme.glyphs.empty(),
                    tr(locale, MessageId::Unavailable),
                    self.theme.warning,
                    no_models_guidance.as_str(),
                )
            } else if execution_ready {
                (
                    self.theme.glyphs.ready(),
                    tr(locale, MessageId::Ready),
                    self.theme.success,
                    tr(locale, MessageId::ProviderAvailableDetail),
                )
            } else if provider.registered && provider.available {
                (
                    self.theme.glyphs.empty(),
                    tr(locale, MessageId::RuntimeUnavailable),
                    self.theme.warning,
                    tr(locale, MessageId::ProviderBackendNotReadyDetail),
                )
            } else if import_validating {
                (
                    validation_glyph,
                    tr(locale, MessageId::ValidatingProvider),
                    self.theme.accent,
                    tr(locale, MessageId::ProviderValidationDetail),
                )
            } else if let Some(message) = import_failure {
                (
                    "!",
                    tr(locale, MessageId::ImportFailed),
                    self.theme.danger,
                    message,
                )
            } else if is_ccswitch && provider.source_importable && import_reviewing {
                (
                    self.theme.glyphs.diamond_solid(),
                    if active_route {
                        tr(locale, MessageId::ReadyToUse)
                    } else {
                        tr(locale, MessageId::ReadyToImport)
                    },
                    self.theme.accent,
                    if active_route {
                        tr(locale, MessageId::ActiveProxyImportDetail)
                    } else {
                        tr(locale, MessageId::SavedProfileImportDetail)
                    },
                )
            } else if is_ccswitch && provider.source_importable {
                (
                    self.theme.glyphs.diamond_solid(),
                    if active_route {
                        tr(locale, MessageId::ActiveRoute)
                    } else {
                        tr(locale, MessageId::SavedProfile)
                    },
                    self.theme.accent,
                    if active_route {
                        tr(locale, MessageId::ActiveProxyReviewDetail)
                    } else {
                        tr(locale, MessageId::SavedProfileReviewDetail)
                    },
                )
            } else if is_ccswitch {
                (
                    self.theme.glyphs.diamond_open(),
                    if provider.source_auth_mode == "cli_oauth" {
                        tr(locale, MessageId::CliOauth)
                    } else {
                        tr(locale, MessageId::Unavailable)
                    },
                    self.theme.muted,
                    provider.source_reason.as_str(),
                )
            } else if provider.registered {
                (
                    self.theme.glyphs.empty(),
                    tr(locale, MessageId::CredentialRequired),
                    self.theme.warning,
                    tr(locale, MessageId::CredentialSafetyDetail),
                )
            } else {
                (
                    self.theme.glyphs.unavailable(),
                    tr(locale, MessageId::NotRegistered),
                    self.theme.muted,
                    tr(locale, MessageId::ProviderConnectDetail),
                )
            };
        let model_count = if is_ccswitch && !provider.available {
            provider.models.len()
        } else {
            provider
                .models
                .iter()
                .filter(|model| model.available)
                .count()
        };
        let action_label = if import_validating {
            tr(locale, MessageId::Validating)
        } else if import_failure.is_some() {
            tr(locale, MessageId::Retry)
        } else if import_reviewing {
            if active_route {
                tr(locale, MessageId::ConfirmRoute)
            } else {
                tr(locale, MessageId::ConfirmImport)
            }
        } else if execution_ready && !has_compatible_models {
            tr(locale, MessageId::DiagnosticOnly)
        } else if execution_ready {
            tr(locale, MessageId::UseProvider)
        } else if provider.registered && provider.available {
            tr(locale, MessageId::RetryReadiness)
        } else if is_ccswitch && provider.source_importable {
            if active_route {
                tr(locale, MessageId::UseActiveRoute)
            } else {
                tr(locale, MessageId::ImportSavedProfile)
            }
        } else if is_ccswitch {
            tr(locale, MessageId::Unavailable)
        } else {
            tr(locale, MessageId::Connect)
        };
        let source_label = if provider.source_label.is_empty() {
            provider.id.as_str()
        } else {
            provider.source_label.as_str()
        };
        let authentication = if provider.available {
            if provider.auth_source.is_empty() {
                tr(locale, MessageId::Configured)
            } else {
                tr(locale, MessageId::CredentialStore)
            }
        } else if is_ccswitch && provider.source_importable {
            tr(locale, MessageId::AvailableAfterConfirmation)
        } else if is_ccswitch {
            tr(locale, MessageId::NotReusable)
        } else {
            tr(locale, MessageId::NotConfigured)
        };
        let mut detail_lines = vec![
            Line::from(vec![
                Span::styled(format!("{state_glyph} "), Style::default().fg(state_color)),
                Span::styled(
                    state,
                    Style::default()
                        .fg(state_color)
                        .add_modifier(Modifier::BOLD),
                ),
            ]),
            Line::from(""),
            Line::from(Span::styled(
                name,
                Style::default()
                    .fg(self.theme.text)
                    .add_modifier(Modifier::BOLD),
            )),
            Line::from(Span::styled(
                source_label,
                Style::default().fg(self.theme.muted),
            )),
            Line::from(""),
            Line::from(Span::styled(
                tr(locale, MessageId::ProviderDetails),
                Style::default()
                    .fg(self.theme.muted)
                    .add_modifier(Modifier::BOLD),
            )),
            Line::from(vec![
                Span::styled(
                    format!(
                        "{:<width$}",
                        tr(locale, MessageId::Models),
                        width = layout_contract::PROVIDER_DETAIL_LABEL_WIDTH
                    ),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(
                    model_count.to_string(),
                    Style::default().fg(self.theme.text),
                ),
            ]),
            Line::from(vec![
                Span::styled(
                    format!(
                        "{:<width$}",
                        tr(locale, MessageId::PhaseAuthentication),
                        width = layout_contract::PROVIDER_DETAIL_LABEL_WIDTH
                    ),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(authentication, Style::default().fg(self.theme.text)),
            ]),
        ];
        if is_ccswitch {
            detail_lines.push(Line::from(vec![
                Span::styled("CC Switch       ", Style::default().fg(self.theme.muted)),
                Span::styled(
                    if active_route {
                        tr(locale, MessageId::CcSwitchActiveProxy)
                    } else if provider.source_current {
                        tr(locale, MessageId::CcSwitchCurrentProfile)
                    } else {
                        tr(locale, MessageId::CcSwitchSavedProfile)
                    },
                    Style::default().fg(self.theme.text),
                ),
            ]));
        }
        detail_lines.push(Line::from(""));
        detail_lines.push(Line::from(Span::styled(
            guidance,
            Style::default().fg(self.theme.muted),
        )));
        let button_y = inner.bottom().saturating_sub(layout_contract::ROW_HEIGHT);
        let detail_area = Rect::new(
            inner.x,
            inner.y,
            inner.width,
            button_y.saturating_sub(inner.y),
        );
        frame.render_widget(
            Paragraph::new(detail_lines).wrap(Wrap { trim: false }),
            detail_area,
        );
        let button = Rect::new(
            inner.x,
            button_y,
            label_button_width(action_label).min(inner.width),
            u16::from(inner.height > 0),
        );
        let component = ComponentId(951);
        let disabled = import_validating
            || (execution_ready && !has_compatible_models)
            || (is_ccswitch && !provider.source_importable && !provider.available);
        let style = if self.interactions.hovered(component) && !self.theme.no_color {
            self.theme.action().add_modifier(Modifier::UNDERLINED)
        } else {
            self.theme.action()
        };
        frame.render_widget(
            Paragraph::new(format!(" {action_label} "))
                .style(style)
                .alignment(Alignment::Center),
            button,
        );
        if !disabled {
            self.interactions.register(HitRegion {
                component,
                area: button,
                action: if import_active {
                    Action::ConfirmProviderImport
                } else {
                    Action::SelectProvider(self.provider_index)
                },
            });
        }
        if import_active {
            let cancel_label = format!(" {} ", tr(locale, MessageId::Cancel));
            let cancel = Rect::new(
                button
                    .right()
                    .saturating_add(layout_contract::BLOCK_PADDING),
                button.y,
                display_cells(&cancel_label),
                button.height,
            );
            let cancel_component = ComponentId(952);
            frame.render_widget(
                Paragraph::new(cancel_label).style(
                    if self.interactions.hovered(cancel_component) {
                        self.theme.hovered()
                    } else {
                        self.theme.muted()
                    },
                ),
                cancel,
            );
            self.interactions.register(HitRegion {
                component: cancel_component,
                area: cancel,
                action: Action::CancelProviderImport,
            });
        }
    }

    fn render_credential(&self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let provider = self
            .inventory
            .providers
            .get(self.provider_index)
            .map(|provider| provider.id.as_str())
            .unwrap_or("provider");
        let [title, form] = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(layout_contract::SECTION_HEADER_HEIGHT),
                Constraint::Min(layout_contract::FORM_MIN_HEIGHT),
            ])
            .areas(area);
        self.render_scene_title(
            frame,
            title,
            &tr_format(
                locale,
                MessageId::AuthenticateProvider,
                &[("provider", provider)],
            ),
            tr(locale, MessageId::CredentialSafetyDetail),
        );
        let input_width = form.width.min(layout_contract::COMPLETION_MAX_WIDTH);
        let input_x = form.x + form.width.saturating_sub(input_width) / 2;
        let input = Rect::new(
            input_x,
            form.y,
            input_width,
            layout_contract::COMPACT_SECTION_HEIGHT.min(form.height),
        );
        let masked = if self.credential_pending {
            tr(locale, MessageId::Validating).to_owned()
        } else {
            "*".repeat(self.credential.chars().count())
        };
        frame.render_widget(
            Paragraph::new(masked).block(
                Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", tr(locale, MessageId::ApiKey))),
            ),
            input,
        );
        if !self.credential_pending
            && input.width >= layout_contract::CONTROL_HEIGHT
            && input.height >= layout_contract::CONTROL_HEIGHT
        {
            frame.set_cursor_position(Position::new(
                input.x
                    + 1
                    + self
                        .credential
                        .chars()
                        .count()
                        .min(input.width.saturating_sub(layout_contract::BLOCK_PADDING) as usize)
                        as u16,
                input.y.saturating_add(layout_contract::ROW_HEIGHT),
            ));
        }
    }

    fn render_models(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let layout = BrowserLayout::compute(area);
        self.render_scene_title(
            frame,
            layout.title,
            tr(locale, MessageId::ChooseModel),
            tr(locale, MessageId::ChooseModelDetail),
        );
        let list_title = format!(" {} ", tr(locale, MessageId::Models));
        let list_area = self.render_browser_panel(frame, layout.list, &list_title);
        let rows = self
            .models
            .iter()
            .map(|model| {
                let mut capabilities = Vec::new();
                if model.reasoning {
                    capabilities.push(tr(locale, MessageId::CapabilityReasoning));
                }
                if model.image_input {
                    capabilities.push(tr(locale, MessageId::Image));
                }
                if model.tool_call {
                    capabilities.push(tr(locale, MessageId::CapabilityTools));
                }
                let display_id = if model.display_id.is_empty() {
                    model.id.as_str()
                } else {
                    model.display_id.as_str()
                };
                let health = model_health_status_label(locale, model.health_status());
                let mut suffix = capabilities.join(" ");
                if !matches!(model.health_status(), "ready" | "") {
                    if !suffix.is_empty() {
                        suffix.push(' ');
                    }
                    suffix.push_str(health);
                }
                format!(
                    "{display_id:<width$} {suffix}",
                    width = layout_contract::MODEL_ID_WIDTH
                )
            })
            .collect();
        self.render_rows(frame, list_area, rows, self.model_index, 2_000, |index| {
            Some(Action::SelectModel(index))
        });
        if let Some(detail) = layout.detail {
            self.render_model_detail(frame, detail);
        }
    }

    fn render_model_detail(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let Some(model) = self.models.get(self.model_index) else {
            return;
        };
        let panel_title = format!(" {} ", tr(locale, MessageId::Capabilities));
        let area = self.render_browser_panel(frame, area, &panel_title);
        let name = if model.name.is_empty() {
            if model.display_id.is_empty() {
                model.id.as_str()
            } else {
                model.display_id.as_str()
            }
        } else {
            model.name.as_str()
        };
        let mut capabilities = Vec::new();
        if model.reasoning {
            capabilities.push(tr(locale, MessageId::CapabilityReasoning));
        }
        if model.image_input {
            capabilities.push(tr(locale, MessageId::CapabilityImages));
        }
        if model.tool_call {
            capabilities.push(tr(locale, MessageId::CapabilityTools));
        }
        let health = model.health_status();
        let health_label = model_health_status_label(locale, health);
        let health_color = model_health_color(self, health);
        let usable = model.is_usable();
        let mut detail_lines = vec![
            Line::from(Span::styled(
                tr(locale, MessageId::SelectedModel),
                Style::default()
                    .fg(self.theme.muted)
                    .add_modifier(Modifier::BOLD),
            )),
            Line::from(""),
            Line::from(Span::styled(
                name,
                Style::default()
                    .fg(self.theme.text)
                    .add_modifier(Modifier::BOLD),
            )),
            Line::from(Span::styled(
                if model.display_id.is_empty() {
                    model.id.as_str()
                } else {
                    model.display_id.as_str()
                },
                Style::default().fg(self.theme.muted),
            )),
            Line::from(""),
            Line::from(vec![
                Span::styled(
                    format!("{}  ", tr(locale, MessageId::Status)),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(health_label, Style::default().fg(health_color)),
            ]),
        ];
        if !usable {
            // Prefer locale copy keyed by status code; fall back to daemon English.
            let reason = model_health_reason_label(locale, health)
                .map(str::to_owned)
                .filter(|value| !value.is_empty())
                .or_else(|| (!model.status_reason.is_empty()).then(|| model.status_reason.clone()));
            if let Some(reason) = reason {
                detail_lines.push(Line::from(Span::styled(
                    reason,
                    Style::default().fg(self.theme.muted),
                )));
            }
        }
        detail_lines.push(Line::from(""));
        detail_lines.push(Line::from(vec![
            Span::styled(
                format!("{}  ", tr(locale, MessageId::Capabilities)),
                Style::default().fg(self.theme.muted),
            ),
            Span::styled(
                if capabilities.is_empty() {
                    tr(locale, MessageId::CapabilityText).into()
                } else {
                    capabilities.join(", ")
                },
                Style::default().fg(self.theme.text),
            ),
        ]));
        frame.render_widget(
            Paragraph::new(detail_lines).wrap(Wrap { trim: false }),
            area,
        );
        // Reasoning effort picker when the model supports discrete levels.
        let efforts = self.model_reasoning_efforts();
        let effort_lines = if efforts.is_empty() || !usable {
            Vec::new()
        } else {
            let effort_label = if self.selected_reasoning_effort.is_empty() {
                tr(locale, MessageId::DefaultReasoning).to_owned()
            } else {
                tr_format(
                    locale,
                    MessageId::ReasoningEffort,
                    &[("effort", self.selected_reasoning_effort.as_str())],
                )
            };
            let levels = efforts
                .iter()
                .map(|effort| {
                    if effort == &self.selected_reasoning_effort {
                        format!("[{effort}]")
                    } else {
                        effort.clone()
                    }
                })
                .collect::<Vec<_>>()
                .join(" ");
            vec![
                Line::from(""),
                Line::from(Span::styled(
                    tr(locale, MessageId::DefaultReasoning),
                    Style::default().fg(self.theme.muted),
                )),
                Line::from(vec![
                    Span::styled(format!(" {effort_label} "), self.theme.focus()),
                    Span::styled(
                        format!("  Tab  {levels}"),
                        Style::default().fg(self.theme.muted),
                    ),
                ]),
            ]
        };
        if !effort_lines.is_empty() {
            let detail_height = area.height.saturating_sub(layout_contract::ROW_HEIGHT);
            if detail_height > 0 {
                let detail = Rect::new(area.x, area.y, area.width, detail_height);
                // Re-paint the capability block was already drawn; append effort
                // lines in the remaining detail space above the action button.
                frame.render_widget(
                    Paragraph::new(effort_lines).wrap(Wrap { trim: false }),
                    Rect::new(
                        detail.x,
                        detail
                            .bottom()
                            .saturating_sub(layout_contract::COMPOSER_CHROME_HEIGHT)
                            .max(detail.y),
                        detail.width,
                        layout_contract::COMPOSER_CHROME_HEIGHT.min(detail.height),
                    ),
                );
            }
        }
        let button_label = if usable {
            tr(locale, MessageId::UseModel)
        } else {
            tr(locale, MessageId::Unavailable)
        };
        let button_width = label_button_width(button_label).min(area.width);
        let button = Rect::new(
            area.x,
            area.y
                .saturating_add(layout_contract::SESSION_ACTION_CURSOR_OFFSET)
                .min(area.bottom().saturating_sub(layout_contract::ROW_HEIGHT)),
            button_width,
            u16::from(area.height > 0),
        );
        let component = ComponentId(952);
        frame.render_widget(
            Paragraph::new(format!(" {button_label} ")).style(if !usable {
                Style::default().fg(self.theme.muted)
            } else if self.interactions.hovered(component) {
                self.theme.selected()
            } else {
                self.theme.focus()
            }),
            button,
        );
        // Always register: unusable models soft-block in select_model_and_continue
        // so the operator still gets an honest status notice.
        self.interactions.register(HitRegion {
            component,
            area: button,
            action: Action::SelectModel(self.model_index),
        });
    }

    fn render_sessions(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let layout = BrowserLayout::compute(area);
        let scope = match self.session_browser.scope() {
            SessionScope::Workspace => tr(locale, MessageId::CurrentWorkspace),
            SessionScope::All => tr(locale, MessageId::AllWorkspaces),
            SessionScope::Archived => tr(locale, MessageId::Archived),
        };
        self.render_scene_title(
            frame,
            layout.title,
            tr(locale, MessageId::Conversations),
            &tr_format(
                locale,
                MessageId::ConversationsBrowseDetail,
                &[("scope", scope)],
            ),
        );
        let list_title = format!(" {} ", tr(locale, MessageId::Conversations));
        let list_area = self.render_browser_panel(frame, layout.list, &list_title);
        let chunks = Layout::vertical([
            Constraint::Length(layout_contract::CONTROL_HEIGHT),
            Constraint::Min(layout_contract::SESSION_LIST_MIN_HEIGHT),
            Constraint::Length(layout_contract::CONTROL_HEIGHT),
        ])
        .split(list_area);
        let search_component = ComponentId(2_990);
        let renaming = self.session_browser.renaming_session_id().is_some();
        let archiving = self.session_browser.archive_confirmation_id().is_some();
        let locked = renaming || archiving;
        let search_style = if locked
            || self.session_browser.search_active()
            || self.interactions.hovered(search_component)
        {
            self.theme.selected()
        } else {
            self.theme.muted()
        };
        let query = self.session_browser.query();
        frame.render_widget(
            Paragraph::new(if renaming {
                tr_format(
                    locale,
                    MessageId::RenameConversation,
                    &[("name", self.session_browser.rename_value())],
                )
            } else if archiving {
                tr(locale, MessageId::ArchiveConversationConfirm).to_owned()
            } else if query.is_empty() {
                tr(locale, MessageId::SearchConversations).to_owned()
            } else {
                format!(" /  {query}")
            })
            .style(search_style),
            chunks[0],
        );
        if !locked {
            self.interactions.register(HitRegion {
                component: search_component,
                area: chunks[0],
                action: Action::FocusSessionSearch,
            });
        }

        let visible = self
            .session_browser
            .visible_indices(&self.sessions, &self.options.workspace);
        let rows = visible
            .iter()
            .enumerate()
            .map(|(visible_index, session_index)| {
                let session = &self.sessions[*session_index];
                let label =
                    conversation_title(Some(session), &self.options.workspace, self.ui_locale());
                let raw_state = if session.execution_status.is_empty() {
                    session.status.as_str()
                } else {
                    session.execution_status.as_str()
                };
                let state = localized_execution_status(self.ui_locale(), raw_state);
                (
                    visible_index,
                    format!(
                        "{label:<width$} {state}",
                        width = layout_contract::SESSION_LABEL_WIDTH
                    ),
                )
            })
            .collect::<Vec<_>>();
        if rows.is_empty() {
            let message = match self.session_browser.scope() {
                SessionScope::Workspace => tr(locale, MessageId::NoWorkspaceConversations),
                SessionScope::All => tr(locale, MessageId::NoActiveConversations),
                SessionScope::Archived => tr(locale, MessageId::NoArchivedConversations),
            };
            frame.render_widget(
                Paragraph::new(message)
                    .style(Style::default().fg(self.theme.muted))
                    .wrap(Wrap { trim: false }),
                chunks[1],
            );
        }
        self.render_indexed_rows(
            frame,
            chunks[1],
            rows,
            self.session_browser.selected_visible(),
            3_000,
            |index| (!locked).then_some(Action::SelectSession(index)),
        );

        let footer = Layout::horizontal([
            Constraint::Length(layout_contract::SESSION_PRIMARY_ACTION_WIDTH),
            Constraint::Length(layout_contract::SESSION_SECONDARY_ACTION_WIDTH),
            Constraint::Min(layout_contract::SESSION_LIST_MIN_HEIGHT),
        ])
        .split(chunks[2]);
        let actions = if renaming {
            vec![
                (
                    tr(locale, MessageId::SaveName),
                    Action::ConfirmRenameSession,
                ),
                (tr(locale, MessageId::Cancel), Action::CancelRenameSession),
            ]
        } else if archiving {
            vec![
                (
                    tr(locale, MessageId::ConfirmArchive),
                    Action::ConfirmArchiveSession,
                ),
                (tr(locale, MessageId::Cancel), Action::CancelArchiveSession),
            ]
        } else if self.session_browser.scope() == SessionScope::Archived {
            vec![
                (
                    tr(locale, MessageId::RestoreSelected),
                    Action::UnarchiveSession,
                ),
                (
                    tr(locale, MessageId::RenameSelected),
                    Action::BeginRenameSession,
                ),
            ]
        } else {
            vec![
                (
                    tr(locale, MessageId::NewConversation),
                    Action::CreateSession,
                ),
                (
                    tr(locale, MessageId::RenameSelected),
                    Action::BeginRenameSession,
                ),
                (
                    tr(locale, MessageId::ArchiveSelected),
                    Action::BeginArchiveSession,
                ),
            ]
        };
        for (index, (label, action)) in actions.into_iter().enumerate() {
            let area = footer[index];
            let component = ComponentId(2_991 + index as u64);
            frame.render_widget(
                Paragraph::new(label).style(if self.interactions.hovered(component) {
                    self.theme.selected()
                } else {
                    self.theme.focus()
                }),
                area,
            );
            if locked || !visible.is_empty() || action == Action::CreateSession {
                self.interactions.register(HitRegion {
                    component,
                    area,
                    action,
                });
            }
        }
        if let Some(detail) = layout.detail {
            self.render_session_detail(frame, detail);
        }
    }

    fn render_session_detail(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let selected = self
            .session_browser
            .selected_index(&self.sessions, &self.options.workspace)
            .and_then(|index| self.sessions.get(index));
        let has_selected = selected.is_some();
        let (title, state, model, workspace, summary) = selected.map_or_else(
            || {
                (
                    tr(locale, MessageId::NoConversationSelected).into(),
                    "idle".to_owned(),
                    self.theme.glyphs.missing().to_owned(),
                    self.theme.glyphs.missing().to_owned(),
                    tr(locale, MessageId::SessionRecoveryGuidance).to_owned(),
                )
            },
            |session| {
                (
                    conversation_title(Some(session), &self.options.workspace, self.ui_locale()),
                    if session.execution_status.is_empty() {
                        localized_execution_status(self.ui_locale(), &session.status)
                    } else {
                        localized_execution_status(self.ui_locale(), &session.execution_status)
                    },
                    if session.next_model.is_empty() {
                        self.theme.glyphs.missing().to_owned()
                    } else {
                        session.next_model.clone()
                    },
                    if session.workspace_root.is_empty() {
                        self.theme.glyphs.missing().to_owned()
                    } else {
                        session.workspace_root.clone()
                    },
                    if session.summary.trim().is_empty() {
                        tr(locale, MessageId::NoSummaryYet).to_owned()
                    } else {
                        session.summary.trim().to_owned()
                    },
                )
            },
        );
        let panel_title = format!(" {} ", tr(locale, MessageId::RecoveryTarget));
        let area = self.render_browser_panel(frame, area, &panel_title);
        let mut lines = vec![
            Line::from(Span::styled(
                tr(locale, MessageId::ConversationPreview),
                Style::default()
                    .fg(self.theme.muted)
                    .add_modifier(Modifier::BOLD),
            )),
            Line::from(""),
            Line::from(Span::styled(
                title,
                Style::default()
                    .fg(self.theme.text)
                    .add_modifier(Modifier::BOLD),
            )),
            Line::from(""),
            Line::from(vec![
                Span::styled(
                    format!("{}  ", tr(locale, MessageId::State)),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(state, Style::default().fg(self.theme.text)),
            ]),
            Line::from(vec![
                Span::styled(
                    format!("{}  ", tr(locale, MessageId::Model)),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(model, Style::default().fg(self.theme.text)),
            ]),
            Line::from(vec![
                Span::styled(
                    format!("{}  ", tr(locale, MessageId::Workspace)),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(workspace, Style::default().fg(self.theme.text)),
            ]),
            Line::from(""),
            Line::from(Span::styled(summary, Style::default().fg(self.theme.text))),
        ];
        if !self.session_browser.preview_lines().is_empty() {
            lines.push(Line::from(""));
            lines.push(Line::from(Span::styled(
                tr(locale, MessageId::RecentMessages),
                Style::default()
                    .fg(self.theme.muted)
                    .add_modifier(Modifier::BOLD),
            )));
            for preview in self.session_browser.preview_lines() {
                lines.push(Line::from(Span::styled(
                    preview.clone(),
                    Style::default().fg(self.theme.text),
                )));
            }
        }
        if !self.session_browser.error().is_empty() {
            lines.push(Line::from(""));
            lines.push(Line::from(Span::styled(
                self.session_browser.error(),
                Style::default().fg(self.theme.danger),
            )));
        } else if self.session_browser.loading_session_id().is_some() {
            lines.push(Line::from(""));
            lines.push(Line::from(Span::styled(
                tr(locale, MessageId::OpeningConversation),
                self.theme.focus(),
            )));
        }
        frame.render_widget(Paragraph::new(lines).wrap(Wrap { trim: false }), area);
        if self.session_browser.renaming_session_id().is_some()
            || self.session_browser.archive_confirmation_id().is_some()
        {
            frame.render_widget(
                Paragraph::new(tr(locale, MessageId::ConfirmCancelHint)).style(self.theme.focus()),
                Rect::new(
                    area.x,
                    area.bottom().saturating_sub(layout_contract::BLOCK_PADDING),
                    area.width,
                    u16::from(area.height > 0),
                ),
            );
            return;
        }
        let action_label = format!(" {} ", tr(locale, MessageId::Open));
        let button = Rect::new(
            area.x,
            area.bottom().saturating_sub(layout_contract::BLOCK_PADDING),
            display_cells(&action_label).min(area.width),
            u16::from(area.height > 0),
        );
        let component = ComponentId(953);
        frame.render_widget(
            Paragraph::new(action_label).style(if self.interactions.hovered(component) {
                self.theme.selected()
            } else {
                self.theme.focus()
            }),
            button,
        );
        if has_selected {
            self.interactions.register(HitRegion {
                component,
                area: button,
                action: Action::SelectSession(self.session_browser.selected_visible()),
            });
        }
    }

    fn render_diagnostic(&self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        self.render_scene_title(
            frame,
            area,
            tr(locale, MessageId::DiagnosticOnly),
            tr(locale, MessageId::DiagnosticNotReady),
        );
        let body = vec![
            Line::from(Span::styled(
                tr_format(
                    locale,
                    MessageId::RuntimeProtocol,
                    &[
                        ("runtime", &self.runtime.runtime_version),
                        ("protocol", &self.runtime.protocol_version),
                    ],
                ),
                Style::default().fg(self.theme.text),
            )),
            Line::from(Span::styled(
                tr_format(
                    locale,
                    MessageId::SocketValue,
                    &[("socket", &self.options.socket.display().to_string())],
                ),
                Style::default().fg(self.theme.text),
            )),
            Line::from(Span::styled(
                tr_format(
                    locale,
                    MessageId::ProvidersCount,
                    &[("count", &self.inventory.providers.len().to_string())],
                ),
                Style::default().fg(self.theme.text),
            )),
            Line::from(""),
            Line::from(Span::styled(
                tr(locale, MessageId::RetryReadinessHint),
                self.theme.focus(),
            )),
            Line::from(Span::styled(
                tr(locale, MessageId::ExitHint),
                Style::default().fg(self.theme.muted),
            )),
        ];
        frame.render_widget(
            Paragraph::new(body).wrap(Wrap { trim: false }),
            area.inner(Margin::new(0, 4)),
        );
    }

    fn render_scene_title(&self, frame: &mut Frame<'_>, area: Rect, title: &str, body: &str) {
        let title_area = Rect::new(
            area.x,
            area.y,
            area.width,
            area.height.min(layout_contract::SECTION_HEADER_HEIGHT),
        );
        frame.render_widget(
            Paragraph::new(vec![
                Line::from(vec![
                    Span::styled(
                        self.theme.glyphs.selected(),
                        Style::default().fg(self.theme.accent),
                    ),
                    Span::styled(
                        title,
                        Style::default()
                            .fg(self.theme.text)
                            .add_modifier(Modifier::BOLD),
                    ),
                ]),
                Line::from(vec![
                    Span::raw("  "),
                    Span::styled(body, Style::default().fg(self.theme.muted)),
                ]),
            ])
            .block(
                Block::default()
                    .borders(Borders::BOTTOM)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(Style::default().fg(self.theme.border)),
            )
            .wrap(Wrap { trim: false }),
            title_area,
        );
    }

    fn render_browser_panel(&self, frame: &mut Frame<'_>, area: Rect, title: &str) -> Rect {
        let panel = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(Style::default().fg(self.theme.border))
            .style(Style::default().fg(self.theme.text))
            .title(Span::styled(
                title,
                Style::default()
                    .fg(self.theme.text)
                    .add_modifier(Modifier::BOLD),
            ));
        let inner = panel.inner(area).inner(Margin::new(1, 1));
        frame.render_widget(panel, area);
        inner
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
        self.render_indexed_rows(
            frame,
            area,
            rows.into_iter().enumerate().collect(),
            selected,
            id_base,
            action,
        );
    }

    fn render_indexed_rows<F>(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        rows: Vec<(usize, String)>,
        selected: usize,
        id_base: u64,
        action: F,
    ) where
        F: Fn(usize) -> Option<Action>,
    {
        let capacity = area.height as usize;
        let selected_position = rows
            .iter()
            .position(|(index, _)| *index == selected)
            .unwrap_or(0);
        let start = selected_position
            .saturating_sub(capacity / 2)
            .min(rows.len().saturating_sub(capacity));
        for (offset, (index, row)) in rows.into_iter().skip(start).take(capacity).enumerate() {
            let row_area = Rect::new(
                area.x,
                area.y + offset as u16,
                area.width,
                layout_contract::ROW_HEIGHT,
            );
            let component = ComponentId(id_base + index as u64);
            let hovered = self.interactions.hovered(component);
            let prefix = if index == selected {
                self.theme.glyphs.selected()
            } else {
                "  "
            };
            let style = if index == selected {
                self.theme.selected()
            } else if hovered {
                self.theme.hovered()
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
        let locale = self.ui_locale();
        let back_label = if self.active_session.is_some() {
            tr(locale, MessageId::Back)
        } else {
            tr(locale, MessageId::Exit)
        };
        let shortcuts: Vec<(&str, &str)> = match self.phase {
            Phase::Locale => vec![
                ("Esc", back_label),
                ("Enter", tr(locale, MessageId::Apply)),
                (
                    self.theme.glyphs.nav_vertical(),
                    tr(locale, MessageId::Navigate),
                ),
            ],
            Phase::Provider
                if matches!(
                    &self.provider_import,
                    super::ProviderImportState::Validating { .. }
                ) =>
            {
                vec![
                    ("Esc", tr(locale, MessageId::Cancel)),
                    ("Enter", tr(locale, MessageId::Validating)),
                ]
            }
            Phase::Provider
                if matches!(
                    &self.provider_import,
                    super::ProviderImportState::Failed { .. }
                ) && self
                    .inventory
                    .providers
                    .get(self.provider_index)
                    .is_some_and(|provider| provider.source_route == "managed_proxy") =>
            {
                vec![
                    ("Esc", tr(locale, MessageId::Cancel)),
                    ("Enter", tr(locale, MessageId::RetryRoute)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::ChooseAnother),
                    ),
                ]
            }
            Phase::Provider
                if matches!(
                    &self.provider_import,
                    super::ProviderImportState::Failed { .. }
                ) =>
            {
                vec![
                    ("Esc", tr(locale, MessageId::Cancel)),
                    ("Enter", tr(locale, MessageId::RetryImport)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::ChooseAnother),
                    ),
                ]
            }
            Phase::Provider
                if matches!(
                    &self.provider_import,
                    super::ProviderImportState::Reviewing { .. }
                ) && self
                    .inventory
                    .providers
                    .get(self.provider_index)
                    .is_some_and(|provider| provider.source_route == "managed_proxy") =>
            {
                vec![
                    ("Esc", tr(locale, MessageId::Cancel)),
                    ("Enter", tr(locale, MessageId::ConfirmRoute)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::ChooseAnother),
                    ),
                ]
            }
            Phase::Provider
                if matches!(
                    &self.provider_import,
                    super::ProviderImportState::Reviewing { .. }
                ) =>
            {
                vec![
                    ("Esc", tr(locale, MessageId::Cancel)),
                    ("Enter", tr(locale, MessageId::ConfirmImport)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::ChooseAnother),
                    ),
                ]
            }
            Phase::Provider
                if self
                    .inventory
                    .providers
                    .get(self.provider_index)
                    .is_some_and(|provider| {
                        provider.registered
                            && provider.available
                            && !self.inventory.is_provider_runnable(provider)
                    }) =>
            {
                vec![
                    ("Esc", back_label),
                    ("Enter", tr(locale, MessageId::RetryReadiness)),
                    ("/", tr(locale, MessageId::Search)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::Navigate),
                    ),
                ]
            }
            Phase::Provider
                if self
                    .inventory
                    .providers
                    .get(self.provider_index)
                    .is_some_and(|provider| {
                        provider.source_kind == "cc-switch"
                            && provider.source_route == "managed_proxy"
                            && provider.source_importable
                    }) =>
            {
                vec![
                    ("Esc", back_label),
                    ("Enter", tr(locale, MessageId::UseActiveRoute)),
                    ("/", tr(locale, MessageId::Search)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::Navigate),
                    ),
                ]
            }
            Phase::Provider
                if self
                    .inventory
                    .providers
                    .get(self.provider_index)
                    .is_some_and(|provider| {
                        provider.source_kind == "cc-switch" && provider.source_importable
                    }) =>
            {
                vec![
                    ("Esc", back_label),
                    ("Enter", tr(locale, MessageId::ImportSavedProfile)),
                    ("/", tr(locale, MessageId::Search)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::Navigate),
                    ),
                ]
            }
            Phase::Provider
                if self
                    .inventory
                    .providers
                    .get(self.provider_index)
                    .is_some_and(|provider| provider.source_kind == "cc-switch") =>
            {
                vec![
                    ("Esc", back_label),
                    ("Enter", tr(locale, MessageId::ViewDetails)),
                    ("/", tr(locale, MessageId::Search)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::Navigate),
                    ),
                ]
            }
            Phase::Provider => vec![
                ("Esc", back_label),
                ("Enter", tr(locale, MessageId::Connect)),
                ("/", tr(locale, MessageId::Search)),
                (
                    self.theme.glyphs.nav_vertical(),
                    tr(locale, MessageId::Navigate),
                ),
            ],
            Phase::Model => {
                let mut shortcuts = vec![
                    ("Esc", tr(locale, MessageId::Back)),
                    ("Enter", tr(locale, MessageId::UseModel)),
                    (
                        self.theme.glyphs.nav_vertical(),
                        tr(locale, MessageId::Navigate),
                    ),
                ];
                if self.model_reasoning_efforts().len() > 1 {
                    shortcuts.push(("Tab", tr(locale, MessageId::DefaultReasoning)));
                }
                shortcuts
            }
            Phase::Session => vec![
                ("/", tr(locale, MessageId::Search)),
                ("Tab", tr(locale, MessageId::Scope)),
                ("N", tr(locale, MessageId::NewConversation)),
                ("Enter", tr(locale, MessageId::Open)),
                ("Esc", tr(locale, MessageId::Back)),
            ],
            Phase::Credential => vec![
                ("Esc", tr(locale, MessageId::Cancel)),
                ("Enter", tr(locale, MessageId::Validate)),
            ],
            Phase::Diagnostic => vec![
                ("Esc", tr(locale, MessageId::Exit)),
                ("Enter", tr(locale, MessageId::Retry)),
            ],
            // Issue #22: discoverable key hints on the conversation surface.
            Phase::Conversation => vec![
                ("?", tr(locale, MessageId::CommandHelp)),
                ("/", tr(locale, MessageId::Commands)),
                (self.keybindings.expand_tools.label(), "expand"),
                (self.keybindings.interrupt.label(), "stop"),
            ],
        };
        let mut spans = Vec::new();
        for (key, label) in &shortcuts {
            spans.push(Span::styled(format!(" {key} "), self.theme.keycap()));
            spans.push(Span::styled(
                format!(" {label}  "),
                Style::default().fg(self.theme.muted),
            ));
        }
        let notice_text = self.notice.render(self.ui_locale());
        let notice = if notice_text.is_empty() {
            Line::default()
        } else {
            Line::from(vec![
                Span::styled(
                    format!("{} ", self.theme.glyphs.ready()),
                    Style::default().fg(self.theme.warning),
                ),
                Span::styled(
                    notice_text.as_ref(),
                    Style::default().fg(self.theme.warning),
                ),
            ])
        };
        frame.render_widget(Paragraph::new(vec![Line::from(spans), notice]), area);
    }

    fn render_conversation(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let content = layout_contract::content(area);
        let composer_height = self
            .composer
            .desired_height(
                content
                    .width
                    .saturating_sub(layout_contract::TRANSCRIPT_HORIZONTAL_INSET * 2),
            )
            .clamp(
                layout_contract::COMPOSER_MIN_HEIGHT,
                layout_contract::COMPOSER_MAX_HEIGHT,
            )
            + layout_contract::COMPOSER_CHROME_HEIGHT;
        // A conversation is an operating surface, not a repeated welcome
        // screen. Keep its context bar compact at every terminal size.
        let header_height = if self.options.screen_mode == Some(ScreenMode::Inline) {
            0
        } else {
            layout_contract::CONVERSATION_HEADER_HEIGHT
        };
        let chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(header_height),
                Constraint::Min(layout_contract::CONVERSATION_MIN_TRANSCRIPT_HEIGHT),
                Constraint::Length(composer_height),
                Constraint::Length(layout_contract::SCENE_FOOTER_HEIGHT),
            ])
            .split(content);
        if header_height > 0 {
            self.render_conversation_header(frame, chunks[0]);
        }
        self.render_transcript(frame, chunks[1]);
        self.render_composer(frame, chunks[2]);
        self.render_composer_chrome(frame, chunks[3]);
        self.render_slash_completion(frame, chunks[1]);
        self.render_context_completion(frame, chunks[1]);
        self.render_prompt_history_search(frame, chunks[1]);
    }

    fn render_conversation_header(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let title = conversation_title(
            self.active_session.as_ref(),
            &self.options.workspace,
            self.ui_locale(),
        );
        let (provider, model, reasoning) = self.product_header_metadata();
        let mut actions = vec![
            HeaderAction {
                label: tr(locale, MessageId::Sessions),
                action: Action::OpenSessions,
                component: ComponentId(9_101),
            },
            HeaderAction {
                label: tr(locale, MessageId::Model),
                action: Action::OpenModels,
                component: ComponentId(9_102),
            },
            HeaderAction {
                label: if self
                    .active_session
                    .as_ref()
                    .is_some_and(|session| session.plan_mode)
                {
                    tr(locale, MessageId::PlanModeShortcut)
                } else {
                    tr(locale, MessageId::BuildModeShortcut)
                },
                action: Action::TogglePlanMode,
                component: ComponentId(9_106),
            },
        ];
        if self
            .active_session
            .as_ref()
            .is_some_and(|session| session.execution_status == "paused")
        {
            actions.push(HeaderAction {
                label: if self.paused_resume_blocker().is_some() {
                    tr(locale, MessageId::Review)
                } else {
                    tr(locale, MessageId::Resume)
                },
                action: Action::ResumePausedExecutionRun,
                component: ComponentId(9_105),
            });
        }
        actions.push(HeaderAction {
            label: tr(locale, MessageId::Settings),
            action: Action::OpenSettings,
            component: ComponentId(9_104),
        });
        ProductHeader {
            locale: self.ui_locale(),
            title: Some(&title),
            phase: None,
            provider: &provider,
            model: &model,
            reasoning: &reasoning,
            workspace: &self.options.workspace,
            actions: &actions,
        }
        .render(frame, area, self.theme, &mut self.interactions);
    }

    fn render_transcript(&mut self, frame: &mut Frame<'_>, area: Rect) {
        self.transcript_area = area;
        if self.transcript_stale {
            let locale = self.ui_locale();
            frame.render_widget(
                Paragraph::new(vec![
                    Line::from(Span::styled(
                        tr(locale, MessageId::ConversationRefreshRequired),
                        self.theme.focus().add_modifier(Modifier::BOLD),
                    )),
                    Line::from(Span::styled(
                        tr(locale, MessageId::ConversationRefreshDetail),
                        Style::default().fg(self.theme.text),
                    )),
                    Line::from(Span::styled(
                        tr(locale, MessageId::ConversationRefreshRecovery),
                        Style::default().fg(self.theme.warning),
                    )),
                ]),
                area,
            );
            return;
        }
        if self.blocks.is_empty() {
            EmptyConversation {
                workspace: &self.options.workspace,
                locale: self.ui_locale(),
            }
            .render(frame, area, self.theme);
            return;
        }
        let committed = self.scrollback.committed_prefix_len(&self.blocks);
        let projected_blocks = self.blocks[committed..]
            .iter()
            .cloned()
            .map(|mut block| {
                block.expanded = self.effective_block_expanded(&block);
                block
            })
            .collect::<Vec<_>>();
        let visible_blocks = projected_blocks.as_slice();
        let locale = self.ui_locale();
        let selection_anchor = self
            .history_selected
            .and_then(|index| index.checked_sub(committed));
        let transcript_content = layout_contract::transcript_content(area);
        let block_width = transcript_content.width;
        let transcript_x = transcript_content.x;
        let content_width = block_width.max(layout_contract::MIN_DIMENSION);
        let layout = TranscriptLayout::new(
            visible_blocks,
            TranscriptViewport {
                locale,
                density: self.density,
                content_width,
                tool_expand_key: self.keybindings.expand_tools.label(),
                height: area.height as usize,
                requested_scroll: self.transcript_scroll,
                follow_bottom: self.transcript_follow_bottom,
                anchor: selection_anchor,
            },
            &mut self.transcript_height_cache,
        );
        self.transcript_scroll = layout.viewport_start;
        self.transcript_max_scroll = layout.max_scroll;
        let viewport_end = layout.viewport_start.saturating_add(area.height as usize);

        for item in &layout.items {
            let block_end = item.top.saturating_add(item.height);
            let visible_top = item.top.max(layout.viewport_start);
            let visible_bottom = block_end.min(viewport_end);
            if visible_top >= visible_bottom {
                continue;
            }
            let relative_index = item.index;
            let index = committed + relative_index;
            let block = &projected_blocks[relative_index];
            let cell_kind = SemanticCellKind::from_block(block);
            let block_area = Rect::new(
                transcript_x,
                area.y.saturating_add(
                    visible_top
                        .saturating_sub(layout.viewport_start)
                        .min(u16::MAX as usize) as u16,
                ),
                block_width,
                visible_bottom
                    .saturating_sub(visible_top)
                    .min(u16::MAX as usize) as u16,
            );
            let component = ComponentId::stable("transcript", &block.id);
            let hovered = self.interactions.hovered(component);
            let dimmed = selection_anchor.is_some_and(|anchor| relative_index > anchor);
            let mut label_style = match cell_kind {
                SemanticCellKind::User => self.theme.transcript_user(),
                SemanticCellKind::Assistant
                    if block.assistant_phase
                        == Some(crate::rpc::AssistantMessagePhase::FinalAnswer) =>
                {
                    self.theme
                        .transcript_assistant()
                        .add_modifier(Modifier::BOLD)
                }
                SemanticCellKind::Assistant => self.theme.transcript_metadata(),
                SemanticCellKind::Thinking => self.theme.transcript_thinking(),
                SemanticCellKind::Tool | SemanticCellKind::ToolGroup
                    if !block.tool_members.is_empty()
                        && block.tool_members.iter().all(|member| !member.is_running()) =>
                {
                    self.theme.transcript_tool_settled()
                }
                SemanticCellKind::Tool | SemanticCellKind::ToolGroup | SemanticCellKind::Patch => {
                    self.theme.transcript_tool()
                }
                SemanticCellKind::Approval | SemanticCellKind::Failure => {
                    self.theme.transcript_danger()
                }
                SemanticCellKind::Notice => self
                    .theme
                    .transcript_metadata()
                    .add_modifier(Modifier::BOLD),
            };
            if dimmed {
                label_style = self.theme.transcript_metadata();
            }
            let commentary = block.kind == BlockKind::Assistant
                && block.assistant_phase == Some(crate::rpc::AssistantMessagePhase::Commentary);
            let text_style = if dimmed || commentary {
                self.theme.transcript_metadata()
            } else {
                match cell_kind {
                    SemanticCellKind::Thinking => self.theme.transcript_thinking(),
                    SemanticCellKind::Notice => self.theme.transcript_metadata(),
                    _ => Style::default().fg(self.theme.text),
                }
            };
            let accent = label_style.fg.unwrap_or(self.theme.text);
            let transcript_styles = TranscriptStyles {
                glyphs: self.theme.glyphs,
                label: label_style,
                text: text_style,
                metadata: self.theme.transcript_metadata(),
                added: self.theme.transcript_added(),
                removed: self.theme.transcript_removed(),
                code: Style::default()
                    .fg(self.theme.code_fg)
                    .bg(self.theme.code_bg),
                link: self.theme.transcript_link(),
                headings: std::array::from_fn(|index| self.theme.heading(index + 1)),
            };
            let lines = if dimmed {
                transcript_lines_with_tool_key_and_density(
                    block,
                    locale,
                    transcript_styles,
                    content_width,
                    self.keybindings.expand_tools.label(),
                    self.density,
                )
            } else {
                cached_transcript_lines_with_tool_key_and_density(
                    &mut self.transcript_render_cache,
                    block,
                    locale,
                    transcript_styles,
                    content_width,
                    self.keybindings.expand_tools.label(),
                    self.density,
                )
            };
            let style = if block.selected {
                self.theme.selected().add_modifier(Modifier::BOLD)
            } else if block.kind == BlockKind::User
                && self.theme.level > crate::theme::ColorLevel::Basic
            {
                Style::default().bg(self.theme.user_message_bg)
            } else {
                Style::default()
            };
            // Chrome lives in the outer gutter and never changes the content
            // width. Use a blank cell when inactive: Color::Reset is the
            // terminal foreground, so recoloring the rail to it does not hide
            // the glyph on a terminal-owned background.
            let outer_chrome_owned = !cell_kind.owns_internal_rail();
            let chrome_visible =
                block.selected || outer_chrome_owned && (hovered || block.is_collapsible());
            let chrome_glyph = if chrome_visible {
                self.theme.glyphs.scroll_track()
            } else {
                " "
            };
            let chrome_area = Rect::new(
                block_area
                    .x
                    .saturating_sub(layout_contract::TRANSCRIPT_HORIZONTAL_INSET),
                block_area.y,
                layout_contract::TRANSCRIPT_HORIZONTAL_INSET,
                block_area.height,
            );
            frame.render_widget(
                Paragraph::new(vec![
                    Line::styled(
                        chrome_glyph,
                        if chrome_visible {
                            Style::default().fg(accent)
                        } else {
                            Style::default()
                        },
                    );
                    chrome_area.height as usize
                ]),
                chrome_area,
            );
            frame.render_widget(
                Paragraph::new(lines)
                    .style(style)
                    .wrap(Wrap { trim: false })
                    .scroll((
                        visible_top.saturating_sub(item.top).min(u16::MAX as usize) as u16,
                        0,
                    )),
                block_area,
            );
            if let Some(action) =
                transcript_click_action(block, index, self.history_selected.is_some())
            {
                let action_area = if matches!(&action, Action::ToggleBlock(id) if id == &block.id) {
                    visible_transcript_rect(
                        area,
                        block_width,
                        layout.viewport_start,
                        viewport_end,
                        item.top,
                        item.top.saturating_add(item.header_height),
                    )
                } else {
                    Some(block_area)
                };
                if let Some(action_area) = action_area {
                    self.interactions.register(HitRegion {
                        component,
                        area: action_area,
                        action,
                    });
                }
            }
        }

        if layout.total_height > area.height as usize {
            let mut scrollbar_state = ScrollbarState::new(layout.total_height)
                .viewport_content_length(area.height as usize)
                .position(layout.viewport_start);
            frame.render_stateful_widget(
                Scrollbar::new(ScrollbarOrientation::VerticalRight)
                    .thumb_style(self.theme.focus())
                    .track_style(Style::default().fg(self.theme.border))
                    .begin_symbol(Some(self.theme.glyphs.up()))
                    .end_symbol(Some(self.theme.glyphs.down())),
                area,
                &mut scrollbar_state,
            );
        }
    }

    fn render_composer(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let focused = self.focus == Focus::Composer;
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
        let title = self
            .media
            .attachment_for_preview(&self.composer)
            .map(|attachment| {
                let state = match &attachment.state {
                    crate::media::MediaState::Pending => tr(locale, MessageId::StatusRunning),
                    crate::media::MediaState::Ready(_) => tr(locale, MessageId::Ready),
                    crate::media::MediaState::Failed(_) => tr(locale, MessageId::StatusFailed),
                };
                format!(
                    " {} {sep} {} {sep} {} {sep} {state} ",
                    tr(locale, MessageId::Image),
                    attachment.label,
                    human_bytes(attachment.bytes as usize)
                )
            })
            .unwrap_or_else(|| format!(" {} ", tr(locale, MessageId::Message)));
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(if focused {
                self.theme.focus()
            } else {
                Style::default().fg(self.theme.border)
            })
            .title(title);
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
        for row in inner.y..inner.bottom() {
            for column in inner.x..inner.right() {
                if let Some(element) = self
                    .composer
                    .element_at_screen(column, row, inner, self.composer_state)
                    .filter(|element| element.kind == crate::media::IMAGE_ELEMENT_KIND)
                {
                    self.interactions.register(HitRegion {
                        component: ComponentId(9_001),
                        area: Rect::new(
                            column,
                            row,
                            layout_contract::ROW_HEIGHT,
                            layout_contract::ROW_HEIGHT,
                        ),
                        action: Action::PreviewMedia(element.id),
                    });
                }
            }
        }
        if focused
            && self.overlays.active().is_none()
            && self.history_search.is_none()
            && let Some((x, y)) = self
                .composer
                .cursor_pos_with_state(inner, self.composer_state)
        {
            frame.set_cursor_position(Position::new(x, y));
        }
        self.render_media_preview(frame, inner, focused);
    }

    fn render_media_preview(&mut self, frame: &mut Frame<'_>, composer: Rect, focused: bool) {
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
        self.media_preview_placement = None;
        if !focused
            || self.overlays.active().is_some()
            || self.history_search.is_some()
            || !self.slash_suggestions().is_empty()
        {
            return;
        }
        let Some(attachment) = self.media.attachment_for_preview(&self.composer).cloned() else {
            return;
        };
        let cursor = self
            .composer
            .cursor_pos_with_state(composer, self.composer_state)
            .map(|(x, y)| Position::new(x, y))
            .unwrap_or(Position::new(composer.x, composer.y));
        let (preview_width, preview_height) = if self.graphics_enabled {
            media_preview_size(
                &attachment.path,
                layout_contract::MEDIA_PREVIEW_WIDTH,
                layout_contract::MEDIA_PREVIEW_HEIGHT,
            )
            .unwrap_or((
                layout_contract::MEDIA_PREVIEW_WIDTH,
                layout_contract::MEDIA_PREVIEW_HEIGHT,
            ))
        } else {
            (
                layout_contract::MEDIA_PREVIEW_WIDTH,
                layout_contract::MEDIA_PREVIEW_HEIGHT,
            )
        };
        let Some(area) =
            media_preview_rect(self.transcript_area, cursor, preview_width, preview_height)
        else {
            return;
        };
        frame.render_widget(Clear, area);
        let state = match &attachment.state {
            crate::media::MediaState::Pending => tr(locale, MessageId::StatusRunning),
            crate::media::MediaState::Ready(_) => tr(locale, MessageId::Ready),
            crate::media::MediaState::Failed(_) => tr(locale, MessageId::StatusFailed),
        };
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(self.theme.focus())
            .style(Style::default())
            .title(format!(
                " {} {sep} {} ",
                tr(locale, MessageId::Image),
                attachment.label
            ));
        let inner = block.inner(area);
        frame.render_widget(block, area);
        if inner.height == 0 {
            return;
        }
        let meta_y = inner.bottom().saturating_sub(layout_contract::ROW_HEIGHT);
        frame.render_widget(
            Paragraph::new(format!(
                "{} {sep} {} {sep} {state}",
                attachment.media_type.trim_start_matches("image/"),
                human_bytes(attachment.bytes as usize),
            ))
            .style(Style::default().fg(self.theme.muted)),
            Rect::new(inner.x, meta_y, inner.width, layout_contract::ROW_HEIGHT),
        );
        if matches!(attachment.state, crate::media::MediaState::Failed(_)) {
            let retry = tr(self.ui_locale(), MessageId::Retry);
            let retry_width = UnicodeWidthStr::width(retry).min(inner.width as usize) as u16;
            let retry_area = Rect::new(
                inner.right().saturating_sub(retry_width),
                meta_y,
                retry_width,
                layout_contract::ROW_HEIGHT,
            );
            frame.render_widget(
                Paragraph::new(retry).style(self.theme.focus().add_modifier(Modifier::BOLD)),
                retry_area,
            );
            self.interactions.register(HitRegion {
                component: ComponentId(9_002),
                area: retry_area,
                action: Action::RetryMedia(attachment.element_id),
            });
        }
        let image_area = Rect::new(
            inner.x,
            inner.y,
            inner.width,
            inner.height.saturating_sub(layout_contract::ROW_HEIGHT),
        );
        if self.graphics_enabled
            && image_area.width >= layout_contract::MEDIA_GRAPHICS_MIN_WIDTH
            && image_area.height >= layout_contract::MEDIA_GRAPHICS_MIN_HEIGHT
        {
            self.media_preview_placement = Some(crate::terminal_graphics::MediaPreviewPlacement {
                path: attachment.path,
                owner_id: attachment.generation,
                area: image_area,
            });
        } else if image_area.height > 0 {
            frame.render_widget(
                Paragraph::new(vec![
                    Line::from(format!(
                        "{:<width$}{}",
                        tr(locale, MessageId::FormatLabel),
                        attachment.media_type,
                        width = layout_contract::MEDIA_STATUS_LABEL_WIDTH
                    )),
                    Line::from(format!(
                        "{:<width$}{}",
                        tr(locale, MessageId::SizeLabel),
                        human_bytes(attachment.bytes as usize),
                        width = layout_contract::MEDIA_STATUS_LABEL_WIDTH
                    )),
                    Line::from(format!(
                        "{:<width$}{state}",
                        tr(locale, MessageId::Status),
                        width = layout_contract::MEDIA_STATUS_LABEL_WIDTH
                    )),
                ])
                .style(Style::default().fg(self.theme.text)),
                image_area,
            );
        }
    }

    fn render_slash_completion(&mut self, frame: &mut Frame<'_>, transcript: Rect) {
        let suggestions = self.slash_suggestions();
        if suggestions.is_empty() || transcript.height < layout_contract::COMPLETION_MIN_HEIGHT {
            return;
        }
        let locale = self.ui_locale();
        let descriptions = suggestions
            .iter()
            .map(|command| {
                let mut description = match &command.description {
                    crate::command::SuggestionDescription::Localized(id) => {
                        tr(locale, *id).to_owned()
                    }
                    crate::command::SuggestionDescription::Plain(value) => {
                        let argument_hint = crate::command::argument_hint(command);
                        let description = if argument_hint.is_empty() {
                            value.clone()
                        } else {
                            format!("{value} {argument_hint}")
                        };
                        self.theme
                            .glyphs
                            .sanitize_free_text(std::borrow::Cow::Owned(description))
                            .into_owned()
                    }
                };
                if let Some(reason) = command.unavailable_reason {
                    description = format!(
                        "{description} {} {}",
                        self.theme.glyphs.separator(),
                        tr(locale, reason)
                    );
                }
                description
            })
            .collect::<Vec<_>>();
        let height =
            (suggestions.len() as u16 + 2).min(layout_contract::COMPLETION_MAX_ROWS as u16);
        let width = suggestions
            .iter()
            .zip(&descriptions)
            .map(|(command, description)| {
                let source = self
                    .theme
                    .glyphs
                    .sanitize_free_text(std::borrow::Cow::Borrowed(command.source.as_str()));
                UnicodeWidthStr::width(command.name.as_str())
                    + UnicodeWidthStr::width(description.as_str())
                    + UnicodeWidthStr::width(source.as_ref())
                    + 8
            })
            .max()
            .unwrap_or(layout_contract::SLASH_COMPLETION_MIN_WIDTH)
            .clamp(
                layout_contract::SLASH_COMPLETION_MIN_WIDTH,
                transcript
                    .width
                    .saturating_sub(layout_contract::BLOCK_PADDING) as usize,
            ) as u16;
        let area = Rect::new(
            transcript.x + layout_contract::ROW_HEIGHT,
            transcript.bottom().saturating_sub(height),
            width,
            height,
        );
        frame.render_widget(Clear, area);
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(self.theme.focus())
            .title(format!(
                " {} ",
                if self.command_registry_stale {
                    format!(
                        "{} {} {}",
                        tr(locale, MessageId::Commands),
                        self.theme.glyphs.separator(),
                        tr(locale, MessageId::CommandRegistryStale)
                    )
                } else {
                    tr(locale, MessageId::Commands).to_owned()
                }
            ))
            .style(Style::default());
        let inner = block.inner(area);
        frame.render_widget(block, area);
        let visible_rows = inner.height as usize;
        let start = self
            .slash_selected
            .saturating_sub(visible_rows.saturating_sub(layout_contract::MIN_DIMENSION as usize));
        for (row_index, (index, (command, description))) in suggestions
            .iter()
            .zip(descriptions)
            .enumerate()
            .skip(start)
            .take(visible_rows)
            .enumerate()
        {
            let source = self
                .theme
                .glyphs
                .sanitize_free_text(std::borrow::Cow::Borrowed(command.source.as_str()));
            let row = Rect::new(
                inner.x,
                inner.y + row_index as u16,
                inner.width,
                layout_contract::ROW_HEIGHT,
            );
            let component = ComponentId(9_200 + index as u64);
            let style = if index == self.slash_selected || self.interactions.hovered(component) {
                self.theme.selected()
            } else if command.unavailable_reason.is_some() {
                Style::default().fg(self.theme.muted)
            } else {
                Style::default().fg(self.theme.text)
            };
            frame.render_widget(
                Paragraph::new(Line::from(vec![
                    Span::styled(
                        format!(
                            "{:<width$}",
                            command.name,
                            width = layout_contract::SLASH_COMMAND_LABEL_WIDTH
                        ),
                        Style::default().fg(self.theme.accent),
                    ),
                    Span::raw(if command.source == "carina" {
                        description
                    } else {
                        format!("{description} {} {}", self.theme.glyphs.separator(), source)
                    }),
                ]))
                .style(style),
                row,
            );
            self.interactions.register(HitRegion {
                component,
                area: row,
                action: Action::SelectSlashCommand {
                    id: command.id.clone(),
                    registry_revision: command.registry_revision.clone(),
                },
            });
        }
    }

    fn render_context_completion(&mut self, frame: &mut Frame<'_>, transcript: Rect) {
        if !self.context_completion.is_open()
            || transcript.height < layout_contract::COMPLETION_MIN_HEIGHT
            || transcript.width < layout_contract::COMPLETION_MIN_WIDTH
        {
            return;
        }
        self.interactions.begin_overlay();
        let locale = self.ui_locale();
        let result_rows = self.context_completion.results().len().clamp(
            layout_contract::COMPLETION_MIN_ROWS,
            layout_contract::COMPLETION_MAX_ROWS,
        ) as u16;
        let height = (result_rows + layout_contract::COMPACT_SECTION_HEIGHT).min(transcript.height);
        let width = transcript
            .width
            .saturating_sub(layout_contract::TRANSCRIPT_HORIZONTAL_INSET * 2)
            .min(layout_contract::COMPLETION_MAX_WIDTH);
        let area = Rect::new(
            transcript.x + layout_contract::ROW_HEIGHT,
            transcript.bottom().saturating_sub(height),
            width,
            height,
        );
        frame.render_widget(Clear, area);
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(self.theme.focus())
            .title(format!(" {} ", tr(locale, MessageId::WorkspaceFiles)))
            .style(Style::default());
        let inner = block.inner(area);
        frame.render_widget(block, area);

        let status = if self.context_completion.is_loading() {
            tr(locale, MessageId::ScanningWorkspace).to_owned()
        } else if let Some(error) = self.context_completion.error() {
            tr_format(locale, MessageId::WorkspaceFilesFailed, &[("error", error)])
        } else if self.context_completion.results().is_empty() {
            tr_format(
                locale,
                MessageId::NoWorkspaceFileMatches,
                &[("query", self.context_completion.query())],
            )
        } else {
            tr_format(
                locale,
                MessageId::WorkspaceFileMatches,
                &[
                    ("query", self.context_completion.query()),
                    (
                        "count",
                        &self.context_completion.results().len().to_string(),
                    ),
                ],
            )
        };
        frame.render_widget(
            Paragraph::new(truncate_cells(&status, inner.width as usize)).style(
                Style::default().fg(if self.context_completion.error().is_some() {
                    self.theme.warning
                } else {
                    self.theme.muted
                }),
            ),
            Rect::new(inner.x, inner.y, inner.width, layout_contract::ROW_HEIGHT),
        );
        let rows = Rect::new(
            inner.x,
            inner.y.saturating_add(layout_contract::ROW_HEIGHT),
            inner.width,
            inner.height.saturating_sub(layout_contract::ROW_HEIGHT),
        );
        let selected = self.context_completion.selected();
        let start = selected.saturating_sub(rows.height as usize / 2).min(
            self.context_completion
                .results()
                .len()
                .saturating_sub(rows.height as usize),
        );
        for (index, candidate) in self
            .context_completion
            .results()
            .iter()
            .enumerate()
            .skip(start)
            .take(rows.height as usize)
        {
            let row = Rect::new(
                rows.x,
                rows.y + (index - start) as u16,
                rows.width,
                layout_contract::ROW_HEIGHT,
            );
            let component = ComponentId(9_500 + index as u64);
            let active = index == selected || self.interactions.hovered(component);
            let prefix = if index == selected {
                self.theme.glyphs.selected()
            } else {
                "  "
            };
            let language = if candidate.language.is_empty() {
                String::new()
            } else {
                format!("  {}", candidate.language)
            };
            let available = row
                .width
                .saturating_sub(prefix.width() as u16)
                .saturating_sub(language.width() as u16) as usize;
            frame.render_widget(
                Paragraph::new(Line::from(vec![
                    Span::styled(
                        format!("{prefix}{}", truncate_cells(&candidate.path, available)),
                        if active {
                            self.theme.selected()
                        } else {
                            Style::default().fg(self.theme.text)
                        },
                    ),
                    Span::styled(language, Style::default().fg(self.theme.muted)),
                ]))
                .style(if active {
                    self.theme.selected()
                } else {
                    Style::default()
                }),
                row,
            );
            self.interactions.register(HitRegion {
                component,
                area: row,
                action: Action::SelectFileCandidate(index),
            });
        }
    }

    fn render_prompt_history_search(&mut self, frame: &mut Frame<'_>, transcript: Rect) {
        let Some(search) = self.history_search.as_ref() else {
            return;
        };
        if transcript.height < layout_contract::HISTORY_COMPLETION_MIN_HEIGHT
            || transcript.width < layout_contract::COMPLETION_MIN_WIDTH
        {
            return;
        }
        self.interactions.begin_overlay();
        let locale = self.ui_locale();
        let result_rows = search.matches().len().clamp(
            layout_contract::COMPLETION_MIN_ROWS,
            layout_contract::COMPLETION_MAX_ROWS,
        ) as u16;
        let height = (result_rows + 4).min(transcript.height);
        let width = transcript
            .width
            .saturating_sub(layout_contract::TRANSCRIPT_HORIZONTAL_INSET * 2)
            .min(layout_contract::COMPLETION_MAX_WIDTH);
        let area = Rect::new(
            transcript.x + 1,
            transcript.bottom().saturating_sub(height),
            width,
            height,
        );
        frame.render_widget(Clear, area);
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(self.theme.focus())
            .title(format!(" {} ", tr(locale, MessageId::PromptHistory)))
            .style(Style::default());
        let inner = block.inner(area);
        frame.render_widget(block, area);

        let query_area = Rect::new(inner.x, inner.y, inner.width, layout_contract::ROW_HEIGHT);
        if search.mode() == HistoryMode::Browse {
            frame.render_widget(
                Paragraph::new(truncate_cells(
                    tr(locale, MessageId::PromptHistoryBrowseHint),
                    inner.width as usize,
                ))
                .style(Style::default().fg(self.theme.muted)),
                query_area,
            );
        } else {
            let query_prefix = format!("{}  ", tr(locale, MessageId::Search));
            let query = truncate_cells(
                search.query(),
                inner
                    .width
                    .saturating_sub(UnicodeWidthStr::width(query_prefix.as_str()) as u16)
                    as usize,
            );
            frame.render_widget(
                Paragraph::new(Line::from(vec![
                    Span::styled(query_prefix, Style::default().fg(self.theme.muted)),
                    Span::styled(query, Style::default().fg(self.theme.text)),
                ])),
                query_area,
            );
        }
        let separator = Rect::new(
            inner.x,
            inner.y.saturating_add(layout_contract::ROW_HEIGHT),
            inner.width,
            layout_contract::ROW_HEIGHT,
        );
        if search.persistent_unavailable() {
            frame.render_widget(
                Paragraph::new(truncate_cells(
                    tr(locale, MessageId::WorkspaceHistoryUnavailable),
                    separator.width as usize,
                ))
                .style(Style::default().fg(self.theme.warning)),
                separator,
            );
        } else {
            frame.render_widget(
                Paragraph::new(self.theme.glyphs.rule().repeat(inner.width as usize))
                    .style(Style::default().fg(self.theme.border)),
                separator,
            );
        }

        let rows_area = Rect::new(
            inner.x,
            inner.y.saturating_add(layout_contract::BLOCK_PADDING),
            inner.width,
            inner.height.saturating_sub(layout_contract::BLOCK_PADDING),
        );
        if search.matches().is_empty() {
            frame.render_widget(
                Paragraph::new(format!("  {}", tr(locale, MessageId::NoMatchingPrompts)))
                    .style(Style::default().fg(self.theme.muted)),
                rows_area,
            );
        } else {
            let selected = search.selected();
            let start = selected.saturating_sub(rows_area.height as usize / 2).min(
                search
                    .matches()
                    .len()
                    .saturating_sub(rows_area.height as usize),
            );
            for (index, entry) in search
                .matches()
                .iter()
                .enumerate()
                .skip(start)
                .take(rows_area.height as usize)
            {
                let row = Rect::new(
                    rows_area.x,
                    rows_area.y + (index - start) as u16,
                    rows_area.width,
                    layout_contract::ROW_HEIGHT,
                );
                let component = ComponentId(9_400 + index as u64);
                let selected = index == selected;
                let style = if selected || self.interactions.hovered(component) {
                    self.theme.selected()
                } else {
                    Style::default().fg(self.theme.text)
                };
                let one_line = entry.text.split_whitespace().collect::<Vec<_>>().join(" ");
                let prefix = if selected {
                    self.theme.glyphs.selected()
                } else {
                    "  "
                };
                frame.render_widget(
                    Paragraph::new(format!(
                        "{prefix}{}",
                        truncate_cells(
                            &one_line,
                            row.width.saturating_sub(layout_contract::BLOCK_PADDING) as usize,
                        )
                    ))
                    .style(style),
                    row,
                );
                self.interactions.register(HitRegion {
                    component,
                    area: row,
                    action: Action::SelectPromptHistory(index),
                });
            }
        }

        if self.overlays.active().is_none() && search.mode() == HistoryMode::Search {
            let query_prefix = format!("{}  ", tr(locale, MessageId::Search));
            let query = truncate_cells(
                search.query(),
                inner
                    .width
                    .saturating_sub(UnicodeWidthStr::width(query_prefix.as_str()) as u16)
                    as usize,
            );
            let query_width = UnicodeWidthStr::width(query.as_str()) as u16;
            frame.set_cursor_position(Position::new(
                query_area
                    .x
                    .saturating_add(UnicodeWidthStr::width(query_prefix.as_str()) as u16)
                    .saturating_add(query_width)
                    .min(
                        query_area
                            .right()
                            .saturating_sub(layout_contract::ROW_HEIGHT),
                    ),
                query_area.y,
            ));
        }
    }

    fn render_composer_chrome(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let notice = self.notice.render(self.ui_locale());
        let session = self.active_session.as_ref();
        let config = self.security_context.as_ref();
        let hitl = config
            .map(|value| value.approval_mode.as_str())
            .filter(|value| !value.is_empty())
            .unwrap_or("unknown");
        let profile = session
            .map(|value| value.permission_profile.as_str())
            .filter(|value| !value.is_empty())
            .or_else(|| {
                config
                    .map(|value| value.permission_profile.as_str())
                    .filter(|value| !value.is_empty())
            })
            .unwrap_or("unknown");
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: &self.execution_status,
            notice: notice.as_ref(),
            elapsed: self.execution_timer.elapsed(),
            activity: self.execution_activity.as_deref(),
            queued_follow_ups: self.queued_prompts.len(),
            background_work: self
                .context_summary
                .as_ref()
                .is_some_and(|summary| summary.task.mode == "background"),
            interrupt_key: self.keybindings.interrupt.label(),
            priority_notice: self.notice.is_localized(MessageId::RuntimeUnavailable)
                || self.notice.is_localized(MessageId::EventStreamInterrupted),
            context: self
                .context_summary
                .as_ref()
                .map(|summary| &summary.model_context_tokens),
            locale: self.ui_locale(),
            screen_mode: self.options.screen_mode.unwrap_or_default().as_arg(),
            hitl,
            isolation_profile: profile,
            sandbox_commands: config.map(|value| value.sandbox_commands),
        });
        if area.height == 0 || area.width == 0 {
            return;
        }

        let mut primary_spans = Vec::new();
        if let Some(primary) = &chrome.primary {
            if primary.animated {
                primary_spans.push(Span::styled(
                    format!(
                        "{} ",
                        self.theme.glyphs.activity_spinner(
                            self.execution_timer
                                .elapsed()
                                .unwrap_or_default()
                                .as_millis()
                        )
                    ),
                    chrome_tone_style(primary.tone, self.theme),
                ));
            }
            let prefix_width = primary_spans
                .iter()
                .map(|span| UnicodeWidthStr::width(span.content.as_ref()))
                .sum::<usize>();
            primary_spans.push(Span::styled(
                truncate_cells(
                    &primary.text,
                    (area.width as usize).saturating_sub(prefix_width),
                ),
                chrome_tone_style(primary.tone, self.theme),
            ));
        }

        let slots = chrome.layout_slots(area.width, self.theme.glyphs);
        let separator = format!(" {} ", self.theme.glyphs.separator());
        let mut slot_spans = Vec::new();
        let slot_row = area
            .y
            .saturating_add(1)
            .min(area.bottom().saturating_sub(1));
        for (index, slot) in slots.iter().enumerate() {
            if index > 0 {
                slot_spans.push(Span::styled(separator.clone(), self.theme.muted()));
            }
            let component = ComponentId::stable("composer-chrome", chrome_slot_key(slot.kind));
            let action = slot.capability.map(chrome_capability_action);
            let style = if action.is_some() && self.interactions.hovered(component) {
                self.theme.hovered()
            } else {
                chrome_tone_style(slot.tone, self.theme)
            };
            slot_spans.push(Span::styled(slot.text.as_str(), style));
            if let Some(action) = action {
                self.interactions.register(HitRegion {
                    component,
                    area: Rect::new(area.x.saturating_add(slot.start), slot_row, slot.width, 1),
                    action,
                });
            }
        }
        frame.render_widget(
            Paragraph::new(vec![Line::from(primary_spans), Line::from(slot_spans)]),
            area,
        );
    }

    fn product_header_metadata(&self) -> (String, String, String) {
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
        let selected_model = self
            .models
            .iter()
            .find(|model| model.id == self.selected_model)
            .or_else(|| {
                self.inventory
                    .providers
                    .iter()
                    .flat_map(|provider| provider.models.iter())
                    .find(|model| model.id == self.selected_model)
            });
        let provider = if self.phase == Phase::Conversation {
            self.inventory.providers.iter().find(|provider| {
                provider
                    .models
                    .iter()
                    .any(|model| model.id == self.selected_model)
            })
        } else {
            self.inventory.providers.get(self.provider_index)
        };
        let provider_label = provider
            .map(|provider| {
                let name = if provider.name.trim().is_empty() {
                    tr(locale, MessageId::Provider)
                } else {
                    provider.name.trim()
                };
                if provider.source_kind == "cc-switch" {
                    if provider.source_app.is_empty() {
                        format!("{name}  {sep}  CC Switch")
                    } else {
                        format!(
                            "{name}  {sep}  CC Switch / {}",
                            source_app_label(&provider.source_app)
                        )
                    }
                } else if provider.source_auth_mode == "cli_oauth" {
                    format!("{name}  {sep}  CLI OAuth")
                } else if provider.registered && provider.available {
                    format!("{name}  {sep}  Direct API")
                } else {
                    name.to_owned()
                }
            })
            .unwrap_or_else(|| tr(locale, MessageId::ProviderNotSelected).to_owned());
        let model_label = selected_model
            .map(|model| {
                first_non_empty(
                    &model.display_id,
                    &model.name,
                    model
                        .id
                        .rsplit('/')
                        .next()
                        .unwrap_or_else(|| tr(locale, MessageId::Model)),
                )
                .to_owned()
            })
            .filter(|model| !model.is_empty())
            .unwrap_or_else(|| tr(locale, MessageId::ModelNotSelected).to_owned());
        let selected_effort = self.selected_reasoning_effort.trim();
        let session_effort = self
            .active_session
            .as_ref()
            .filter(|session| {
                session.next_model.is_empty() || session.next_model == self.selected_model
            })
            .map(|session| session.next_reasoning_effort.trim())
            .filter(|effort| !effort.is_empty());
        let default_effort = selected_model
            .map(|model| model.default_reasoning_effort.trim())
            .filter(|effort| !effort.is_empty());
        let reasoning_label = if selected_model.is_none() && selected_effort.is_empty() {
            tr(locale, MessageId::ReasoningPending).to_owned()
        } else {
            (!selected_effort.is_empty())
                .then_some(selected_effort)
                .or(session_effort)
                .or(default_effort)
                .map(|effort| tr_format(locale, MessageId::ReasoningEffort, &[("effort", effort)]))
                .unwrap_or_else(|| {
                    if selected_model.is_some_and(|model| model.reasoning) {
                        tr(locale, MessageId::DefaultReasoning).to_owned()
                    } else {
                        tr(locale, MessageId::ReasoningOff).to_owned()
                    }
                })
        };
        (provider_label, model_label, reasoning_label)
    }

    fn render_overlay(&mut self, frame: &mut Frame<'_>, area: Rect, overlay: &Overlay) {
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
        match overlay {
            Overlay::Approval(approval) => {
                let approval_title = format!(
                    " {}  1/{} ",
                    tr(locale, MessageId::ApprovalRequired),
                    self.overlays
                        .governance_queue_len()
                        .max(layout_contract::MIN_DIMENSION as usize)
                );
                let popup = centered(
                    area,
                    layout_contract::APPROVAL_POPUP.0,
                    layout_contract::APPROVAL_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let inner = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(Style::default().fg(self.theme.danger))
                    .title(approval_title.clone())
                    .inner(popup);
                frame.render_widget(
                    Block::default()
                        .borders(Borders::ALL)
                        .border_type(self.theme.glyphs.outer_border_type())
                        .border_style(Style::default().fg(self.theme.danger))
                        .title(approval_title)
                        .style(Style::default()),
                    popup,
                );
                let chunks = Layout::default()
                    .direction(Direction::Vertical)
                    .constraints([
                        Constraint::Length(layout_contract::REVIEW_SUMMARY_HEIGHT),
                        Constraint::Min(layout_contract::APPROVAL_BODY_MIN_HEIGHT),
                        Constraint::Length(layout_contract::CONTROL_HEIGHT),
                        Constraint::Length(layout_contract::CONTROL_HEIGHT),
                    ])
                    .split(inner);
                frame.render_widget(
                    Paragraph::new(vec![
                        Line::from(Span::styled(
                            first_non_empty(
                                &approval.label,
                                &approval.capability,
                                tr(locale, MessageId::GovernedAction),
                            ),
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
                    tr(locale, MessageId::ApprovalNoDiff)
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
                                .border_type(self.theme.glyphs.outer_border_type())
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
                            self.theme.muted()
                        };
                        let label = match item {
                            ApprovalScope::Once => tr(locale, MessageId::ApprovalScopeOnce),
                            ApprovalScope::Session => tr(locale, MessageId::ApprovalScopeSession),
                            ApprovalScope::Project => tr(locale, MessageId::ApprovalScopeProject),
                        };
                        Span::styled(format!(" {label} "), style)
                    })
                    .collect::<Vec<_>>();
                frame.render_widget(Paragraph::new(Line::from(scope)), chunks[2]);
                self.render_modal_buttons(
                    frame,
                    chunks[3],
                    &[
                        (tr(locale, MessageId::Allow), Action::ApprovalAllow, 10_001),
                        (tr(locale, MessageId::Deny), Action::ApprovalDeny, 10_002),
                    ],
                );
                if !approval.error.is_empty() {
                    frame.render_widget(
                        Paragraph::new(approval.error.as_str())
                            .style(Style::default().fg(self.theme.danger)),
                        Rect::new(
                            inner.x,
                            inner.bottom().saturating_sub(layout_contract::ROW_HEIGHT),
                            inner.width,
                            layout_contract::ROW_HEIGHT,
                        ),
                    );
                }
            }
            Overlay::Question(question) => {
                let popup = centered(
                    area,
                    layout_contract::QUESTION_POPUP.0,
                    layout_contract::QUESTION_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", tr(locale, MessageId::QuestionTitle)))
                    .style(Style::default());
                let inner = block.inner(popup);
                frame.render_widget(block, popup);
                let chunks = Layout::default()
                    .direction(Direction::Vertical)
                    .constraints([
                        Constraint::Length(layout_contract::SECTION_HEADER_HEIGHT),
                        Constraint::Min(layout_contract::QUESTION_BODY_MIN_HEIGHT),
                        Constraint::Length(layout_contract::CONTROL_HEIGHT),
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
                        .border_type(self.theme.glyphs.outer_border_type())
                        .border_style(self.theme.focus())
                        .title(format!(" {} ", tr(locale, MessageId::Answer)));
                    let input_inner = input.inner(chunks[1]);
                    frame.render_widget(input, chunks[1]);
                    frame.render_widget(
                        Paragraph::new(question.input.as_str())
                            .style(Style::default().fg(self.theme.text)),
                        input_inner,
                    );
                    frame.set_cursor_position(Position::new(
                        input_inner.x
                            + question.input.chars().count().min(
                                input_inner
                                    .width
                                    .saturating_sub(layout_contract::ROW_HEIGHT)
                                    as usize,
                            ) as u16,
                        input_inner.y,
                    ));
                } else {
                    for (index, option) in question
                        .options
                        .iter()
                        .enumerate()
                        .take(chunks[1].height as usize)
                    {
                        let row = Rect::new(
                            chunks[1].x,
                            chunks[1].y + index as u16,
                            chunks[1].width,
                            layout_contract::ROW_HEIGHT,
                        );
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
                        tr(locale, MessageId::QuestionHint)
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
            Overlay::PlanReview(review) => {
                let maximum_popup = bottom_sheet(area, 96, 28);
                let plan_width = maximum_popup
                    .width
                    .saturating_sub(layout_contract::PANEL_PADDING)
                    .max(layout_contract::MIN_DIMENSION);
                let line_groups = self.plan_review_line_groups(review, plan_width);
                let total_visual_rows = line_groups.iter().map(Vec::len).sum::<usize>();
                let popup = bottom_sheet(area, 96, plan_review_popup_height(total_visual_rows));
                let clear = Rect::new(
                    popup.x.saturating_sub(1),
                    popup.y,
                    popup
                        .width
                        .saturating_add(2)
                        .min(area.right().saturating_sub(popup.x.saturating_sub(1))),
                    popup.height,
                );
                frame.render_widget(Clear, clear);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", tr(locale, MessageId::PlanReview)))
                    .style(Style::default());
                let inner = block.inner(popup);
                frame.render_widget(block, popup);
                let chunks = Layout::default()
                    .direction(Direction::Vertical)
                    .constraints([
                        Constraint::Length(layout_contract::COMPACT_SECTION_HEIGHT + 1),
                        Constraint::Min(layout_contract::QUESTION_BODY_MIN_HEIGHT),
                        Constraint::Length(layout_contract::CONTROL_HEIGHT),
                        Constraint::Length(layout_contract::ROW_HEIGHT + 1),
                    ])
                    .split(inner.inner(Margin::new(1, 0)));
                let comment_count = tr_count(
                    locale,
                    MessageId::PlanCommentCountOne,
                    MessageId::PlanCommentCountOther,
                    review.comment_count(),
                );
                frame.render_widget(
                    Paragraph::new(vec![
                        Line::from(Span::styled(
                            tr(locale, MessageId::PlanReadyDecision),
                            self.theme.focus().add_modifier(Modifier::BOLD),
                        )),
                        Line::from(Span::styled(
                            tr(locale, MessageId::PlanDecisionDetail),
                            Style::default().fg(self.theme.muted),
                        )),
                        Line::from(Span::styled(
                            comment_count,
                            Style::default().fg(self.theme.muted),
                        )),
                    ])
                    .wrap(Wrap { trim: false }),
                    chunks[0],
                );
                let plan_block = Block::default()
                    .borders(Borders::TOP | Borders::BOTTOM)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(Style::default().fg(self.theme.border))
                    .title(format!(" {} ", tr(locale, MessageId::PlanCandidate)));
                let plan_body = plan_block.inner(chunks[1]);
                frame.render_widget(plan_block, chunks[1]);
                let body_height = usize::from(plan_body.height);
                let cursor = review.cursor.min(line_groups.len().saturating_sub(1));
                let mut start = if review.commenting {
                    review.scroll.min(line_groups.len().saturating_sub(1))
                } else {
                    review.scroll.min(cursor)
                };
                let mut cursor_rows = line_groups
                    .get(start..=cursor)
                    .unwrap_or_default()
                    .iter()
                    .map(Vec::len)
                    .sum::<usize>();
                while cursor_rows > body_height && start < cursor {
                    cursor_rows = cursor_rows.saturating_sub(line_groups[start].len());
                    start += 1;
                }
                let rendered = line_groups
                    .iter()
                    .skip(start)
                    .flatten()
                    .take(body_height)
                    .cloned()
                    .collect::<Vec<_>>();
                frame.render_widget(
                    Paragraph::new(rendered).style(Style::default().fg(self.theme.text)),
                    plan_body,
                );
                if total_visual_rows > body_height {
                    let mut scrollbar_state = ScrollbarState::new(review.line_count())
                        .viewport_content_length(body_height.min(review.line_count()))
                        .position(review.cursor);
                    frame.render_stateful_widget(
                        Scrollbar::new(ScrollbarOrientation::VerticalRight)
                            .thumb_style(self.theme.focus())
                            .track_style(Style::default().fg(self.theme.border))
                            .begin_symbol(Some(self.theme.glyphs.up()))
                            .end_symbol(Some(self.theme.glyphs.down())),
                        plan_body,
                        &mut scrollbar_state,
                    );
                }
                if review.resolving {
                    frame.render_widget(
                        Paragraph::new(tr(locale, MessageId::PlanApproving))
                            .style(Style::default().fg(self.theme.accent)),
                        chunks[2],
                    );
                } else if review.commenting {
                    let range = review
                        .comment_range()
                        .map(|(start, end)| {
                            if start == end {
                                format!("L{}", start + 1)
                            } else {
                                format!("L{}-L{}", start + 1, end + 1)
                            }
                        })
                        .unwrap_or_else(|| "L1".to_owned());
                    let prompt = tr_format(
                        locale,
                        MessageId::PlanCommentPrompt,
                        &[("range", range.as_str())],
                    );
                    frame.render_widget(
                        Paragraph::new(vec![
                            Line::from(Span::styled(prompt, Style::default().fg(self.theme.muted))),
                            Line::from(vec![
                                Span::styled(self.theme.glyphs.prompt(), self.theme.focus()),
                                Span::styled(review.comment_draft.clone(), self.theme.selected()),
                            ]),
                        ]),
                        chunks[2],
                    );
                } else {
                    self.render_modal_buttons(
                        frame,
                        chunks[2],
                        &[
                            (tr(locale, MessageId::Approve), Action::ApprovePlan, 12_801),
                            (tr(locale, MessageId::Revise), Action::RevisePlan, 12_802),
                            (
                                tr(locale, MessageId::Comment),
                                Action::BeginPlanComment,
                                12_803,
                            ),
                            (
                                tr(locale, MessageId::CloseReview),
                                Action::CancelPlan,
                                12_804,
                            ),
                        ],
                    );
                }
                frame.render_widget(
                    Paragraph::new(if review.error.is_empty() {
                        crate::help::plan_review_hint_line(locale)
                    } else {
                        review.error.clone()
                    })
                    .style(Style::default().fg(if review.error.is_empty() {
                        self.theme.muted
                    } else {
                        self.theme.danger
                    }))
                    .wrap(Wrap { trim: false }),
                    chunks[3],
                );
            }
            Overlay::Settings(settings) => {
                let popup = centered(
                    area,
                    layout_contract::SETTINGS_POPUP.0,
                    layout_contract::SETTINGS_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", tr(locale, MessageId::Settings)))
                    .style(Style::default());
                let inner = block.inner(popup);
                frame.render_widget(block, popup);
                let selected_model_label = self
                    .models
                    .iter()
                    .find(|model| model.id == self.selected_model)
                    .map(|model| {
                        if model.display_id.is_empty() {
                            model.id.as_str()
                        } else {
                            model.display_id.as_str()
                        }
                    })
                    .unwrap_or(self.selected_model.as_str());
                let selected_model_label = if selected_model_label.trim().is_empty() {
                    tr(locale, MessageId::NotSet)
                } else {
                    selected_model_label
                };
                self.render_rows(
                    frame,
                    inner.inner(Margin::new(1, 1)),
                    vec![
                        format!(
                            "{}  {sep}  {}",
                            tr(locale, MessageId::PhaseLanguage),
                            self.options
                                .locale
                                .as_deref()
                                .unwrap_or(tr(locale, MessageId::NotSet))
                        ),
                        self.inventory
                            .providers
                            .get(self.provider_index)
                            .map(|provider| {
                                let name = if provider.name.is_empty() {
                                    provider.id.as_str()
                                } else {
                                    provider.name.as_str()
                                };
                                format!("{}  {sep}  {name}", tr(locale, MessageId::Provider))
                            })
                            .unwrap_or_else(|| {
                                format!(
                                    "{}  {sep}  {}",
                                    tr(locale, MessageId::Provider),
                                    tr(locale, MessageId::NotSet)
                                )
                            }),
                        format!(
                            "{}  {sep}  {}",
                            tr(locale, MessageId::Model),
                            selected_model_label
                        ),
                        if self
                            .active_session
                            .as_ref()
                            .is_some_and(|session| session.plan_mode)
                        {
                            format!(
                                "{}  {sep}  {}",
                                tr(locale, MessageId::Mode),
                                tr(locale, MessageId::ModePlanDetail)
                            )
                        } else {
                            format!(
                                "{}  {sep}  {}",
                                tr(locale, MessageId::Mode),
                                tr(locale, MessageId::ModeBuildDetail)
                            )
                        },
                        format!(
                            "{}  {sep}  {}",
                            tr(locale, MessageId::Density),
                            tr(
                                locale,
                                match self.density {
                                    DensityMode::Compact => MessageId::DensityCompact,
                                    DensityMode::Comfortable => MessageId::DensityComfortable,
                                }
                            )
                        ),
                        format!(
                            "{}  {sep}  {}",
                            tr(locale, MessageId::Status),
                            tr(locale, MessageId::StatusDetail)
                        ),
                        format!(
                            "{}  {sep}  {}",
                            tr(locale, MessageId::Sessions),
                            tr(locale, MessageId::SessionsDetail)
                        ),
                        if self.paused_resume_blocker().is_some() {
                            format!(
                                "{}  {sep}  {}",
                                tr(locale, MessageId::Review),
                                tr(locale, MessageId::ContinuityRequired)
                            )
                        } else if self
                            .active_session
                            .as_ref()
                            .is_some_and(|session| session.execution_status == "paused")
                        {
                            format!(
                                "{}  {sep}  {}",
                                tr(locale, MessageId::Resume),
                                tr(locale, MessageId::ResumePausedDetail)
                            )
                        } else {
                            format!(
                                "{}  {sep}  {}",
                                tr(locale, MessageId::Resume),
                                tr(locale, MessageId::NoPausedExecution)
                            )
                        },
                        tr(locale, MessageId::Close).into(),
                    ],
                    settings.selected,
                    12_000,
                    super::settings_action,
                );
            }
            Overlay::Doctor(doctor) => {
                let popup = centered(
                    area,
                    layout_contract::AGENTS_POPUP.0,
                    layout_contract::AGENTS_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(" Doctor ")
                    .style(Style::default());
                let inner = block.inner(popup).inner(Margin::new(2, 1));
                frame.render_widget(block, popup);
                let lines = doctor
                    .render_lines()
                    .into_iter()
                    .map(|line| {
                        Line::from(Span::styled(line, Style::default().fg(self.theme.text)))
                    })
                    .collect::<Vec<_>>();
                let max_scroll = match lines.len() {
                    0 => 0,
                    n => n - 1,
                };
                let scroll = doctor.scroll.min(max_scroll);
                frame.render_widget(
                    Paragraph::new(lines)
                        .style(Style::default().fg(self.theme.text))
                        .scroll((scroll as u16, 0))
                        .wrap(Wrap { trim: false }),
                    inner,
                );
            }
            Overlay::Help(help) => {
                // Issue #22: real /help surface from the command + keybinding registry.
                let commands = crate::command::palette_matching(
                    "/",
                    self.active_run_id.is_some(),
                    &self.command_registry.commands,
                    &self.command_registry.revision,
                    &self.command_mru,
                )
                .into_iter()
                .map(|command| crate::help::HelpEntry {
                    key: command.name,
                    description: match command.description {
                        crate::command::SuggestionDescription::Localized(id) => {
                            tr(self.ui_locale(), id).to_owned()
                        }
                        crate::command::SuggestionDescription::Plain(value) => self
                            .theme
                            .glyphs
                            .sanitize_free_text(std::borrow::Cow::Borrowed(value.as_str()))
                            .into_owned(),
                    },
                })
                .collect();
                let surface =
                    crate::help::build_help_surface(self.keybindings, commands, self.ui_locale());
                let popup = centered(
                    area,
                    layout_contract::APPROVAL_POPUP.0,
                    layout_contract::APPROVAL_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", surface.title))
                    .style(Style::default());
                let inner = block.inner(popup).inner(Margin::new(2, 1));
                frame.render_widget(block, popup);
                let mut lines = Vec::new();
                lines.push(Line::from(Span::styled(
                    "Shortcuts",
                    Style::default()
                        .fg(self.theme.accent)
                        .add_modifier(Modifier::BOLD),
                )));
                for entry in &surface.shortcuts {
                    lines.push(Line::from(vec![
                        Span::styled(pad_key_label(&entry.key, 14), self.theme.keycap()),
                        Span::styled(
                            entry.description.clone(),
                            Style::default().fg(self.theme.text),
                        ),
                    ]));
                }
                lines.push(Line::from(""));
                lines.push(Line::from(Span::styled(
                    "Commands",
                    Style::default()
                        .fg(self.theme.accent)
                        .add_modifier(Modifier::BOLD),
                )));
                for entry in &surface.commands {
                    lines.push(Line::from(vec![
                        Span::styled(
                            pad_key_label(&entry.key, 14),
                            Style::default().fg(self.theme.accent),
                        ),
                        Span::styled(
                            entry.description.clone(),
                            Style::default().fg(self.theme.text),
                        ),
                    ]));
                }
                lines.push(Line::from(""));
                lines.push(Line::from(Span::styled(
                    surface.footer.clone(),
                    Style::default().fg(self.theme.muted),
                )));
                let max_scroll = match lines.len() {
                    0 => 0,
                    n => n - 1,
                };
                let scroll = help.scroll.min(max_scroll);
                frame.render_widget(
                    Paragraph::new(lines)
                        .style(Style::default().fg(self.theme.text))
                        .scroll((scroll as u16, 0))
                        .wrap(Wrap { trim: false }),
                    inner,
                );
            }
            Overlay::Status(status) => {
                let popup = centered(
                    area,
                    layout_contract::CHECKPOINT_POPUP.0,
                    layout_contract::CHECKPOINT_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", tr(locale, MessageId::Status)))
                    .style(Style::default());
                let inner = block.inner(popup).inner(Margin::new(2, 1));
                frame.render_widget(block, popup);

                let projection = &status.projection;
                let engine = if projection
                    .runtime
                    .context_engine
                    .effective_engine
                    .is_empty()
                {
                    tr(locale, MessageId::DefaultValue)
                } else {
                    projection.runtime.context_engine.effective_engine.as_str()
                };
                let phase = if projection.runtime.context_engine.phase.is_empty() {
                    tr(locale, MessageId::UnknownValue)
                } else {
                    projection.runtime.context_engine.phase.as_str()
                };
                let rollback = if projection.review.rollback.available {
                    tr_format(
                        locale,
                        MessageId::RollbackAvailable,
                        &[(
                            "count",
                            &projection.review.rollback.patch_ids.len().to_string(),
                        )],
                    )
                } else {
                    tr(locale, MessageId::RollbackUnavailable).into()
                };
                let heading = Style::default()
                    .fg(self.theme.accent)
                    .add_modifier(Modifier::BOLD);
                let body = vec![
                    Line::from(Span::styled(tr(locale, MessageId::Runtime), heading)),
                    Line::from(tr_format(
                        locale,
                        MessageId::RuntimeSummary,
                        &[
                            ("phase", phase),
                            ("engine", engine),
                            ("queued", &projection.runtime.queued_executions.to_string()),
                            ("workers", &projection.runtime.active_workers.to_string()),
                            ("uptime", &projection.runtime.uptime_seconds.to_string()),
                        ],
                    )),
                    Line::from(""),
                    Line::from(Span::styled(tr(locale, MessageId::Agents), heading)),
                    Line::from(tr_format(
                        locale,
                        MessageId::AgentsSummary,
                        &[
                            ("active", &projection.active_agents().to_string()),
                            ("input", &projection.agents.needs_input.len().to_string()),
                            ("completed", &projection.agents.completed.len().to_string()),
                        ],
                    )),
                    Line::from(""),
                    Line::from(Span::styled(tr(locale, MessageId::Usage), heading)),
                    Line::from(tr_format(
                        locale,
                        MessageId::UsageSummary,
                        &[
                            ("input", &projection.usage.input_tokens.to_string()),
                            ("output", &projection.usage.output_tokens.to_string()),
                            ("cache", &projection.usage.cache_read_tokens.to_string()),
                            ("cost", &format!("{:.4}", projection.usage.cost_usd)),
                        ],
                    )),
                    Line::from(""),
                    Line::from(Span::styled(
                        tr(locale, MessageId::CurrentConversation),
                        heading,
                    )),
                    Line::from(tr_format(
                        locale,
                        MessageId::ReviewSummary,
                        &[
                            (
                                "state",
                                if projection.review.state.is_empty() {
                                    tr(locale, MessageId::NoActiveReview)
                                } else {
                                    projection.review.state.as_str()
                                },
                            ),
                            ("changes", &projection.review.changes.len().to_string()),
                            ("checks", &projection.review.checks.len().to_string()),
                            (
                                "diagnostics",
                                &projection.review.diagnostics.len().to_string(),
                            ),
                        ],
                    )),
                    Line::from(tr_format(
                        locale,
                        MessageId::ArtifactsRollback,
                        &[
                            ("count", &projection.review.artifact_ids.len().to_string()),
                            ("rollback", &rollback),
                        ],
                    )),
                    Line::from(""),
                    Line::from(Span::styled(
                        tr(locale, MessageId::StatusHint),
                        Style::default().fg(self.theme.muted),
                    )),
                ];
                frame.render_widget(
                    Paragraph::new(body)
                        .style(Style::default().fg(self.theme.text))
                        .wrap(Wrap { trim: false }),
                    inner,
                );
                self.render_modal_buttons(
                    frame,
                    Rect::new(
                        inner.x,
                        inner.bottom().saturating_sub(layout_contract::ROW_HEIGHT),
                        inner.width,
                        layout_contract::ROW_HEIGHT,
                    ),
                    &[
                        (tr(locale, MessageId::Agents), Action::OpenAgents, 12_100),
                        (tr(locale, MessageId::Changes), Action::OpenChanges, 12_101),
                        (tr(locale, MessageId::Refresh), Action::OpenStatus, 12_102),
                        (tr(locale, MessageId::Close), Action::CloseOverlay, 12_103),
                    ],
                );
            }
            Overlay::Context(context) => {
                let popup = centered(
                    area,
                    layout_contract::CHECKPOINT_POPUP.0,
                    layout_contract::CHECKPOINT_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", context_title(locale)))
                    .style(Style::default());
                let inner = block.inner(popup).inner(Margin::new(2, 1));
                frame.render_widget(block, popup);
                let tokens = &context.model_context_tokens;
                let policy = &context.compaction_policy;
                let estimate = if tokens.estimated || tokens.estimated_limit {
                    " est."
                } else {
                    ""
                };
                let mut body = vec![
                    Line::from(Span::styled(
                        format!(
                            "ctx  {} / {}  {}{}",
                            crate::render_contract::format_tokens(tokens.tokens),
                            crate::render_contract::format_tokens(tokens.limit_tokens),
                            crate::render_contract::format_percent(f64::from(tokens.used_percent)),
                            estimate
                        ),
                        context_style_for_percent(
                            tokens.used_percent,
                            &tokens.threshold,
                            self.theme,
                        ),
                    )),
                    Line::from(format!(
                        "window {}  {}  reserve {}  {}  trigger {}",
                        crate::render_contract::format_tokens(policy.window_tokens),
                        self.theme.glyphs.separator(),
                        crate::render_contract::format_tokens(policy.reserve_tokens),
                        self.theme.glyphs.separator(),
                        crate::render_contract::format_tokens(policy.trigger_tokens),
                    )),
                    Line::from(format!(
                        "policy {}  {}  source {}",
                        fallback_label(&policy.policy_version),
                        self.theme.glyphs.separator(),
                        fallback_label(&policy.metadata_source),
                    )),
                    Line::from(""),
                    Line::from(format!(
                        "checkpoint  {}  {} bytes  {} turns  {} compactions",
                        if context.checkpoint.available {
                            "ready"
                        } else {
                            "unavailable"
                        },
                        context.checkpoint.transcript_bytes,
                        context.checkpoint.turn_count,
                        context.checkpoint.compaction_count,
                    )),
                ];
                if let Some(receipt) = &context.recent_receipt {
                    body.extend([
                        Line::from(""),
                        Line::from(format!(
                            "receipt  pressure {:.0}%  removed {}  verbatim {}",
                            receipt.pressure_before * 100.0,
                            receipt.removed_turns,
                            receipt.kept_turn_indices.len(),
                        )),
                        Line::from(format!(
                            "hash  pre {}  summary {}  kept {}",
                            short_hash(&receipt.preimage_sha256),
                            short_hash(&receipt.summary_sha256),
                            short_hash(&receipt.kept_sha256),
                        )),
                        Line::from(format!("key files  {}", receipt.key_files.join(", "))),
                    ]);
                }
                body.push(Line::from(""));
                body.push(Line::from(Span::styled(
                    "R refresh  Enter/Esc close",
                    Style::default().fg(self.theme.muted),
                )));
                frame.render_widget(
                    Paragraph::new(body)
                        .style(Style::default().fg(self.theme.text))
                        .wrap(Wrap { trim: false }),
                    inner,
                );
            }
            Overlay::Agents(overlay) => {
                let popup = centered(
                    area,
                    layout_contract::AGENTS_POPUP.0,
                    layout_contract::AGENTS_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", tr(locale, MessageId::AgentDashboard)))
                    .style(Style::default());
                let inner = block.inner(popup).inner(Margin::new(1, 1));
                frame.render_widget(block, popup);
                let agents: Vec<_> = overlay
                    .projection
                    .agents
                    .needs_input
                    .iter()
                    .chain(overlay.projection.agents.working.iter())
                    .chain(overlay.projection.agents.completed.iter())
                    .collect();
                let columns = Layout::default()
                    .direction(if inner.width >= layout_contract::SPLIT_PANE_MIN_WIDTH {
                        Direction::Horizontal
                    } else {
                        Direction::Vertical
                    })
                    .constraints(if inner.width >= layout_contract::SPLIT_PANE_MIN_WIDTH {
                        vec![Constraint::Percentage(44), Constraint::Percentage(56)]
                    } else {
                        vec![Constraint::Percentage(55), Constraint::Percentage(45)]
                    })
                    .split(inner);
                if agents.is_empty() {
                    frame.render_widget(
                        Paragraph::new(if overlay.load.loading {
                            tr(locale, MessageId::ExecutionWorking)
                        } else {
                            tr(locale, MessageId::NoDelegatedAgents)
                        }),
                        columns[0],
                    );
                } else {
                    for (index, agent) in agents.iter().take(columns[0].height as usize).enumerate()
                    {
                        let row = Rect::new(
                            columns[0].x,
                            columns[0].y + index as u16,
                            columns[0].width,
                            1,
                        );
                        let category = localized_agent_category(locale, agent.product_category());
                        let label = if !agent.summary.is_empty() {
                            agent.summary.as_str()
                        } else if !agent.prompt.is_empty() {
                            agent.prompt.as_str()
                        } else {
                            agent.agent.as_str()
                        };
                        let style = if index == overlay.selected {
                            self.theme.selected()
                        } else {
                            Style::default().fg(self.theme.text)
                        };
                        frame.render_widget(
                            Paragraph::new(format!(
                                "{category:<width$} {label}",
                                width = layout_contract::AGENT_CATEGORY_WIDTH
                            ))
                            .style(style),
                            row,
                        );
                        self.interactions.register(HitRegion {
                            component: ComponentId(12_200 + index as u64),
                            area: row,
                            action: Action::SelectAgent(index),
                        });
                    }
                }
                if let Some(agent) = agents.get(overlay.selected) {
                    let title = if agent.agent.is_empty() {
                        tr(locale, MessageId::Agent)
                    } else {
                        agent.agent.as_str()
                    };
                    let mut details = vec![
                        Line::from(Span::styled(
                            title,
                            Style::default()
                                .fg(self.theme.accent)
                                .add_modifier(Modifier::BOLD),
                        )),
                        Line::from(format!(
                            "{}  {}",
                            tr(locale, MessageId::Status),
                            localized_product_status(locale, agent.product_status())
                        )),
                        Line::from(format!("{}  {}", tr(locale, MessageId::Model), agent.model)),
                        Line::from(format!(
                            "{}  {}",
                            tr(locale, MessageId::Workspace),
                            agent.workspace
                        )),
                        Line::from(""),
                        Line::from(Span::styled(
                            tr(locale, MessageId::Request),
                            Style::default().fg(self.theme.muted),
                        )),
                        Line::from(agent.prompt.as_str()),
                        Line::from(""),
                        Line::from(Span::styled(
                            tr(locale, MessageId::Result),
                            Style::default().fg(self.theme.muted),
                        )),
                        Line::from(agent.summary.as_str()),
                    ];
                    if let Some(recap) = &overlay.recap {
                        details.push(Line::from(""));
                        details.push(Line::from(Span::styled(
                            tr(locale, MessageId::RecentActivity),
                            Style::default().fg(self.theme.muted),
                        )));
                        for event in recap.recent_events.iter().rev().take(6).rev() {
                            let kind =
                                localized_activity_kind(locale, event.product_activity_kind());
                            let state = localized_product_status(locale, event.product_status());
                            details.push(Line::from(format!(
                                "  {kind:<width$} {state}",
                                width = layout_contract::ACTIVITY_KIND_WIDTH
                            )));
                        }
                    }
                    if overlay.recap_load.loading {
                        details.push(Line::from(""));
                        details.push(Line::from(Span::styled(
                            tr(locale, MessageId::ExecutionWorking),
                            Style::default().fg(self.theme.muted),
                        )));
                    } else if !overlay.recap_load.error.is_empty() {
                        details.push(Line::from(""));
                        details.push(Line::from(Span::styled(
                            tr_format(
                                locale,
                                MessageId::LoadAgentsFailed,
                                &[("error", overlay.recap_load.error.as_str())],
                            ),
                            Style::default().fg(self.theme.danger),
                        )));
                    }
                    if !overlay.load.error.is_empty() {
                        details.push(Line::from(""));
                        details.push(Line::from(Span::styled(
                            overlay.load.error.as_str(),
                            Style::default().fg(self.theme.danger),
                        )));
                    }
                    details.push(Line::from(""));
                    details.push(if overlay.confirm_stop {
                        Line::from(Span::styled(
                            tr(locale, MessageId::StopAgentHint),
                            Style::default().fg(self.theme.danger),
                        ))
                    } else {
                        Line::from(Span::styled(
                            tr(locale, MessageId::AgentDashboardHint),
                            Style::default().fg(self.theme.muted),
                        ))
                    });
                    frame.render_widget(
                        Paragraph::new(details).wrap(Wrap { trim: false }),
                        columns[1],
                    );
                } else if !overlay.load.error.is_empty() {
                    frame.render_widget(
                        Paragraph::new(tr_format(
                            locale,
                            MessageId::LoadAgentsFailed,
                            &[("error", overlay.load.error.as_str())],
                        ))
                        .style(Style::default().fg(self.theme.danger))
                        .wrap(Wrap { trim: false }),
                        columns[1],
                    );
                }
                if overlay.load.loading {
                    frame.render_widget(
                        Paragraph::new(tr(locale, MessageId::ExecutionWorking))
                            .style(Style::default().fg(self.theme.muted)),
                        Rect::new(
                            popup.x + 2,
                            popup.bottom().saturating_sub(layout_contract::ROW_HEIGHT),
                            popup.width.saturating_sub(layout_contract::PANEL_PADDING),
                            1,
                        ),
                    );
                }
            }
            Overlay::FileViewer(viewer) => {
                let popup = centered(
                    area,
                    layout_contract::FILE_VIEWER_POPUP.0,
                    layout_contract::FILE_VIEWER_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" @{} ", viewer.path))
                    .style(Style::default());
                let inner = block.inner(popup).inner(Margin::new(1, 0));
                frame.render_widget(block, popup);
                let chunks = Layout::default()
                    .direction(Direction::Vertical)
                    .constraints([
                        Constraint::Length(layout_contract::ROW_HEIGHT),
                        Constraint::Min(layout_contract::MIN_DIMENSION),
                        Constraint::Length(layout_contract::ROW_HEIGHT),
                    ])
                    .split(inner);
                let search = viewer
                    .search
                    .as_ref()
                    .map(|query| {
                        tr_format(locale, MessageId::FileSearch, &[("query", query.as_str())])
                    })
                    .unwrap_or_else(|| {
                        let source = if viewer.hash.is_empty() {
                            tr(locale, MessageId::WorkspaceFile).to_owned()
                        } else {
                            format!("sha256 {}", &viewer.hash[..viewer.hash.len().min(10)])
                        };
                        tr_format(
                            locale,
                            MessageId::FileLinesSummary,
                            &[
                                ("count", &viewer.lines.len().to_string()),
                                ("source", &source),
                            ],
                        )
                    });
                frame.render_widget(
                    Paragraph::new(truncate_cells(&search, chunks[0].width as usize))
                        .style(Style::default().fg(self.theme.muted)),
                    chunks[0],
                );
                match &viewer.load {
                    FileViewerLoad::Loading => {
                        frame.render_widget(
                            Paragraph::new(tr(locale, MessageId::FilePreviewLoading))
                                .style(self.theme.focus()),
                            chunks[1],
                        );
                    }
                    FileViewerLoad::Failed(error) => {
                        frame.render_widget(
                            Paragraph::new(vec![
                                Line::from(Span::styled(
                                    tr(locale, MessageId::FilePreviewUnavailable),
                                    Style::default()
                                        .fg(self.theme.danger)
                                        .add_modifier(Modifier::BOLD),
                                )),
                                Line::from(""),
                                Line::from(error.clone()),
                                Line::from(""),
                                Line::from(tr(locale, MessageId::RetryCancelHint)),
                            ])
                            .wrap(Wrap { trim: false }),
                            chunks[1],
                        );
                    }
                    FileViewerLoad::Ready => {
                        let digits = viewer.lines.len().max(1).to_string().width();
                        for (index, content) in viewer
                            .lines
                            .iter()
                            .enumerate()
                            .skip(viewer.scroll)
                            .take(chunks[1].height as usize)
                        {
                            let row = Rect::new(
                                chunks[1].x,
                                chunks[1].y + (index - viewer.scroll) as u16,
                                chunks[1].width,
                                1,
                            );
                            let component = ComponentId(9_700 + index as u64);
                            let active = index == viewer.cursor;
                            let selected = viewer.selected_line(index);
                            let marker = if active {
                                self.theme.glyphs.selected_cell()
                            } else if selected {
                                self.theme.glyphs.scroll_track()
                            } else {
                                " "
                            };
                            let prefix = format!(
                                "{marker} {:>digits$} {} ",
                                index + 1,
                                self.theme.glyphs.scroll_track()
                            );
                            let available =
                                row.width.saturating_sub(prefix.width() as u16) as usize;
                            let style = if active || self.interactions.hovered(component) {
                                self.theme.selected()
                            } else if selected {
                                Style::default().fg(self.theme.accent)
                            } else {
                                Style::default().fg(self.theme.text)
                            };
                            frame.render_widget(
                                Paragraph::new(Line::from(vec![
                                    Span::styled(prefix, Style::default().fg(self.theme.muted)),
                                    Span::styled(truncate_cells(content, available), style),
                                ]))
                                .style(if active {
                                    self.theme.selected()
                                } else {
                                    Style::default()
                                }),
                                row,
                            );
                            self.interactions.register(HitRegion {
                                component,
                                area: row,
                                action: Action::SelectFileViewerLine(index),
                            });
                        }
                    }
                }
                let attach_label = format!(" {} ", tr(locale, MessageId::AttachRange));
                let button_width = if viewer.load == FileViewerLoad::Ready {
                    attach_label.width() as u16
                } else {
                    0
                };
                let hint_area = Rect::new(
                    chunks[2].x,
                    chunks[2].y,
                    chunks[2].width.saturating_sub(button_width),
                    chunks[2].height,
                );
                let hint = if viewer.load == FileViewerLoad::Ready {
                    selection_hint(
                        viewer,
                        if hint_area.width < layout_contract::COMPACT_HINT_MAX_WIDTH {
                            tr(locale, MessageId::FileSelectionHintCompact)
                        } else {
                            tr(locale, MessageId::FileSelectionHint)
                        },
                    )
                } else {
                    Line::from(tr(locale, MessageId::CancelHint))
                };
                frame.render_widget(
                    Paragraph::new(hint).style(Style::default().fg(self.theme.muted)),
                    hint_area,
                );
                if viewer.load == FileViewerLoad::Ready {
                    let label = attach_label;
                    let button = Rect::new(
                        chunks[2].right().saturating_sub(button_width),
                        chunks[2].y,
                        button_width,
                        1,
                    );
                    let component = ComponentId(9_799);
                    frame.render_widget(
                        Paragraph::new(label).style(if self.interactions.hovered(component) {
                            self.theme.selected()
                        } else {
                            self.theme.focus()
                        }),
                        button,
                    );
                    self.interactions.register(HitRegion {
                        component,
                        area: button,
                        action: Action::ConfirmFileViewer,
                    });
                }
            }
            Overlay::Queue(queue) => {
                let popup = centered(area, 72, 22);
                frame.render_widget(Clear, popup);
                let title = format!(" {} ", tr(locale, MessageId::QueueTitle));
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(if queue.items.is_empty() {
                        Style::default().fg(self.theme.muted)
                    } else {
                        Style::default().fg(self.theme.warning)
                    })
                    .title(title)
                    .style(Style::default());
                let inner = block.inner(popup).inner(Margin::new(1, 1));
                frame.render_widget(block, popup);
                let chunks = Layout::default()
                    .direction(Direction::Vertical)
                    .constraints([
                        Constraint::Length(layout_contract::CONTROL_HEIGHT),
                        Constraint::Min(layout_contract::MIN_DIMENSION),
                        Constraint::Length(layout_contract::ROW_HEIGHT),
                    ])
                    .split(inner);
                let header = if queue.load.loading {
                    tr(locale, MessageId::Validating).to_owned()
                } else if !queue.error.is_empty() {
                    queue.error.clone()
                } else {
                    format!(
                        "{}  {}  n={}",
                        queue.run_id,
                        if queue.soft_interrupt_pending {
                            "soft-interrupt"
                        } else {
                            "idle-control"
                        },
                        queue.items.len()
                    )
                };
                frame.render_widget(
                    Paragraph::new(header)
                        .style(Style::default().fg(self.theme.muted))
                        .wrap(Wrap { trim: false }),
                    chunks[0],
                );
                if queue.items.is_empty() && !queue.load.loading {
                    frame.render_widget(
                        Paragraph::new(tr(locale, MessageId::QueueEmpty))
                            .style(Style::default().fg(self.theme.muted)),
                        chunks[1],
                    );
                } else {
                    let mut lines = Vec::new();
                    for (index, item) in queue.items.iter().enumerate() {
                        let marker = if index == queue.selected {
                            self.theme.glyphs.selected_cell()
                        } else {
                            " "
                        };
                        let priority = if item.priority.is_empty() {
                            "normal"
                        } else {
                            item.priority.as_str()
                        };
                        let row =
                            format!("{marker} [{priority}] {}  {}", item.steer_id, item.preview);
                        let style = if index == queue.selected {
                            self.theme.selected()
                        } else {
                            Style::default().fg(self.theme.text)
                        };
                        lines.push(Line::from(Span::styled(
                            truncate_cells(&row, chunks[1].width as usize),
                            style,
                        )));
                    }
                    frame.render_widget(Paragraph::new(lines), chunks[1]);
                }
                frame.render_widget(
                    Paragraph::new(tr(locale, MessageId::QueueDropHint))
                        .style(Style::default().fg(self.theme.muted)),
                    chunks[2],
                );
            }
            Overlay::Changes(overlay) => {
                if self.options.screen_mode == Some(ScreenMode::Fullscreen)
                    && !overlay.projection.patches.is_empty()
                {
                    self.render_fullscreen_patch_review(frame, area, overlay);
                    return;
                }
                let popup = centered(
                    area,
                    layout_contract::CHANGES_POPUP.0,
                    layout_contract::CHANGES_POPUP.1,
                );
                frame.render_widget(Clear, popup);
                let block = Block::default()
                    .borders(Borders::ALL)
                    .border_type(self.theme.glyphs.outer_border_type())
                    .border_style(self.theme.focus())
                    .title(format!(" {} ", tr(locale, MessageId::ChangesWorkbench)))
                    .style(Style::default());
                let inner = block.inner(popup).inner(Margin::new(1, 1));
                frame.render_widget(block, popup);
                let columns = Layout::default()
                    .direction(if inner.width >= layout_contract::SPLIT_PANE_MIN_WIDTH {
                        Direction::Horizontal
                    } else {
                        Direction::Vertical
                    })
                    .constraints(if inner.width >= layout_contract::SPLIT_PANE_MIN_WIDTH {
                        vec![Constraint::Percentage(45), Constraint::Percentage(55)]
                    } else {
                        vec![Constraint::Percentage(55), Constraint::Percentage(45)]
                    })
                    .split(inner);
                if !overlay.projection.patches.is_empty() {
                    self.render_workspace_patches(frame, &columns, overlay);
                    return;
                }
                if !overlay.projection.workspace_diff.files.is_empty() {
                    self.render_workspace_changes(frame, &columns, overlay);
                    return;
                }
                let changes = &overlay.projection.review.changes;
                if changes.is_empty() {
                    frame.render_widget(
                        Paragraph::new(if overlay.load.loading {
                            tr(locale, MessageId::ScanningWorkspace)
                        } else {
                            tr(locale, MessageId::NoFileChanges)
                        }),
                        columns[0],
                    );
                }
                for (index, change) in changes.iter().take(columns[0].height as usize).enumerate() {
                    let row = Rect::new(
                        columns[0].x,
                        columns[0].y + index as u16,
                        columns[0].width,
                        1,
                    );
                    let file = change
                        .details
                        .affected_files
                        .first()
                        .or_else(|| change.details.failed_files.first())
                        .cloned()
                        .unwrap_or_else(|| localized_change_kind(locale, change.product_kind()));
                    let style = if index == overlay.selected {
                        self.theme.selected()
                    } else {
                        Style::default().fg(self.theme.text)
                    };
                    frame.render_widget(
                        Paragraph::new(format!(
                            "{:<width$} {}",
                            localized_product_status(locale, change.product_status()),
                            file,
                            width = layout_contract::CHANGE_KIND_WIDTH
                        ))
                        .style(style),
                        row,
                    );
                    self.interactions.register(HitRegion {
                        component: ComponentId(12_400 + index as u64),
                        area: row,
                        action: Action::SelectChange(index),
                    });
                }
                if let Some(change) = changes.get(overlay.selected) {
                    let mut details = vec![
                        Line::from(Span::styled(
                            localized_change_kind(locale, change.product_kind()),
                            Style::default()
                                .fg(self.theme.accent)
                                .add_modifier(Modifier::BOLD),
                        )),
                        Line::from(format!(
                            "{}  {}",
                            tr(locale, MessageId::Status),
                            localized_product_status(locale, change.product_status())
                        )),
                        Line::from(format!(
                            "{}  {}",
                            tr(locale, MessageId::Reason),
                            change.details.reason
                        )),
                    ];
                    for file in change
                        .details
                        .affected_files
                        .iter()
                        .chain(change.details.active_files.iter())
                    {
                        details.push(Line::from(format!("  {file}")));
                    }
                    for file in &change.details.failed_files {
                        details.push(Line::from(Span::styled(
                            format!(
                                "  {}",
                                tr_format(locale, MessageId::FailedFile, &[("file", file)])
                            ),
                            Style::default().fg(self.theme.danger),
                        )));
                    }
                    if !change.details.error.is_empty() {
                        details.push(Line::from(""));
                        details.push(Line::from(Span::styled(
                            change.details.error.as_str(),
                            Style::default().fg(self.theme.danger),
                        )));
                    }
                    details.push(Line::from(""));
                    details.push(Line::from(format!(
                        "{}  {}",
                        tr(locale, MessageId::Rollback),
                        if overlay.projection.review.rollback.available {
                            tr(locale, MessageId::StatusReady)
                        } else {
                            tr(locale, MessageId::NotAvailable)
                        }
                    )));
                    frame.render_widget(
                        Paragraph::new(details).wrap(Wrap { trim: false }),
                        columns[1],
                    );
                } else if !overlay.load.error.is_empty() {
                    frame.render_widget(
                        Paragraph::new(tr_format(
                            locale,
                            MessageId::LoadChangesFailed,
                            &[("error", overlay.load.error.as_str())],
                        ))
                        .style(Style::default().fg(self.theme.danger))
                        .wrap(Wrap { trim: false }),
                        columns[1],
                    );
                }
                if overlay.load.loading {
                    frame.render_widget(
                        Paragraph::new(tr(locale, MessageId::ScanningWorkspace))
                            .style(Style::default().fg(self.theme.muted)),
                        Rect::new(
                            popup.x + layout_contract::BLOCK_PADDING,
                            popup.bottom().saturating_sub(layout_contract::ROW_HEIGHT),
                            popup.width.saturating_sub(layout_contract::PANEL_PADDING),
                            layout_contract::ROW_HEIGHT,
                        ),
                    );
                }
            }
        }
    }

    fn plan_review_line_groups(
        &self,
        review: &PlanReviewOverlay,
        width: u16,
    ) -> Vec<Vec<Line<'static>>> {
        let line_number_width = review.line_count().max(1).to_string().len();
        let mut line_groups = Vec::with_capacity(review.line_count());
        for (index, logical) in review.lines().iter().enumerate() {
            let selected = index == review.cursor;
            let marked = review.line_is_marked(index);
            let row_style = if selected {
                self.theme.selected()
            } else if marked {
                Style::default().fg(self.theme.warning)
            } else {
                Style::default().fg(self.theme.text)
            };
            let cursor = if selected {
                self.theme.glyphs.selected_cell()
            } else {
                " "
            };
            let comment = if review.line_has_comments(index) {
                self.theme.glyphs.bullet()
            } else {
                " "
            };
            let initial_indent = Line::from(vec![
                Span::styled(cursor.to_owned(), row_style),
                Span::styled(
                    format!(
                        " {:>width$}",
                        format!("L{}", index + 1),
                        width = line_number_width + 1
                    ),
                    if selected || marked {
                        row_style
                    } else {
                        Style::default().fg(self.theme.muted)
                    },
                ),
                Span::styled(
                    format!(" {comment} "),
                    if review.line_has_comments(index) {
                        Style::default().fg(self.theme.warning)
                    } else {
                        row_style
                    },
                ),
            ]);
            let subsequent_indent =
                Line::from(Span::styled(" ".repeat(initial_indent.width()), row_style));
            let logical_line = Line::from(Span::styled(logical.clone(), row_style));
            let lines = word_wrap_line(
                &logical_line,
                RtOptions::new(usize::from(width.max(1)))
                    .initial_indent(initial_indent)
                    .subsequent_indent(subsequent_indent),
            );
            line_groups.push(
                lines
                    .into_iter()
                    .map(|line| {
                        Line::from(
                            line.spans
                                .into_iter()
                                .map(|span| Span::styled(span.content.into_owned(), span.style))
                                .collect::<Vec<_>>(),
                        )
                        .style(row_style)
                    })
                    .collect(),
            );
        }
        line_groups
    }

    fn render_fullscreen_patch_review(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        overlay: &crate::overlay::ChangesOverlay,
    ) {
        let locale = self.ui_locale();
        let popup = layout_contract::canvas(area);
        frame.render_widget(Clear, popup);
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(self.theme.focus())
            .title(format!(" {} ", tr(locale, MessageId::ChangesWorkbench)));
        let inner = block.inner(popup).inner(Margin::new(1, 0));
        frame.render_widget(block, popup);
        if inner.width == 0 || inner.height == 0 {
            return;
        }

        let regions = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(layout_contract::PATCH_REVIEW_SUMMARY_HEIGHT.min(inner.height)),
                Constraint::Min(layout_contract::ROW_HEIGHT),
                Constraint::Length(
                    layout_contract::PATCH_REVIEW_FOOTER_HEIGHT
                        .min(inner.height.saturating_sub(layout_contract::ROW_HEIGHT)),
                ),
            ])
            .split(inner);
        let (selected_patch_index, selected_file_index) = patch_review_render_selection(overlay);
        let selected_patch = overlay.projection.patches.get(selected_patch_index);
        let selected_review = overlay.patch_reviews.get(selected_patch_index);
        self.render_patch_review_summary(frame, regions[0], selected_patch, selected_review);

        if regions[1].width >= layout_contract::PATCH_REVIEW_THREE_PANE_MIN_WIDTH {
            let columns = Layout::default()
                .direction(Direction::Horizontal)
                .constraints([
                    Constraint::Percentage(19),
                    Constraint::Length(layout_contract::CONTROL_HEIGHT),
                    Constraint::Percentage(29),
                    Constraint::Length(layout_contract::CONTROL_HEIGHT),
                    Constraint::Min(24),
                ])
                .split(regions[1]);
            self.render_patch_review_transactions(frame, columns[0], overlay, selected_patch_index);
            self.render_patch_review_divider(frame, columns[1]);
            self.render_patch_review_files(
                frame,
                columns[2],
                overlay,
                selected_review,
                selected_file_index,
            );
            self.render_patch_review_divider(frame, columns[3]);
            self.render_patch_review_detail(
                frame,
                columns[4],
                overlay,
                selected_review,
                selected_file_index,
            );
        } else {
            self.render_stacked_patch_review(
                frame,
                regions[1],
                overlay,
                selected_review,
                selected_patch_index,
                selected_file_index,
            );
        }
        let rollback_ready = selected_patch.is_some_and(|patch| {
            !patch.rollback_pointer.is_empty()
                && !matches!(patch.status.as_str(), "rolled_back" | "failed" | "proposed")
        });
        self.render_patch_review_footer(
            frame,
            regions[2],
            overlay.confirm_rollback,
            rollback_ready,
        );
    }

    fn render_stacked_patch_review(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        overlay: &crate::overlay::ChangesOverlay,
        review: Option<&PatchReview>,
        selected_patch: usize,
        selected_file: usize,
    ) {
        let list_height = layout_contract::PATCH_REVIEW_STACKED_LIST_HEIGHT
            .min(area.height.saturating_sub(3).max(1));
        if area.width >= layout_contract::SPLIT_PANE_MIN_WIDTH {
            let rows = Layout::default()
                .direction(Direction::Vertical)
                .constraints([Constraint::Length(list_height), Constraint::Min(3)])
                .split(area);
            let lists = Layout::default()
                .direction(Direction::Horizontal)
                .constraints([
                    Constraint::Percentage(40),
                    Constraint::Length(layout_contract::ROW_HEIGHT),
                    Constraint::Min(24),
                ])
                .split(rows[0]);
            self.render_patch_review_transactions(frame, lists[0], overlay, selected_patch);
            self.render_patch_review_divider(frame, lists[1]);
            self.render_patch_review_files(frame, lists[2], overlay, review, selected_file);
            self.render_patch_review_detail(frame, rows[1], overlay, review, selected_file);
        } else {
            let detail_height = 3.min(area.height);
            let lists_height = area.height.saturating_sub(detail_height);
            let transaction_height = 3.min(lists_height);
            let file_height = lists_height.saturating_sub(transaction_height);
            let rows = Layout::default()
                .direction(Direction::Vertical)
                .constraints([
                    Constraint::Length(transaction_height),
                    Constraint::Length(file_height),
                    Constraint::Length(detail_height),
                ])
                .split(area);
            self.render_patch_review_transactions(frame, rows[0], overlay, selected_patch);
            self.render_patch_review_files(frame, rows[1], overlay, review, selected_file);
            self.render_patch_review_detail(frame, rows[2], overlay, review, selected_file);
        }
    }

    fn render_patch_review_summary(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        patch: Option<&crate::rpc::WorkspacePatch>,
        review: Option<&PatchReview>,
    ) {
        let locale = self.ui_locale();
        let Some(patch) = patch else {
            frame.render_widget(
                Paragraph::new(tr(locale, MessageId::NoFileChanges))
                    .style(Style::default().fg(self.theme.muted)),
                area,
            );
            return;
        };
        let disposition = review
            .map(|review| review.disposition)
            .unwrap_or(PatchDisposition::Unknown);
        let (status, status_style) = self.patch_disposition(disposition);
        let (additions, deletions, file_count) = review.map_or((0, 0, 0), |review| {
            (review.additions, review.deletions, review.files.len())
        });
        let verify = patch
            .verify_result
            .as_ref()
            .map(|result| result.status.as_str())
            .filter(|value| !value.is_empty())
            .unwrap_or({
                if patch.test_status.is_empty() {
                    "-"
                } else {
                    patch.test_status.as_str()
                }
            });
        let verify = tr(locale, patch_verification_message(verify));
        let actor = if patch.actor.is_empty() {
            "-"
        } else {
            patch.actor.as_str()
        };
        let transaction = if patch.transaction_id.is_empty() {
            "-"
        } else {
            patch.transaction_id.as_str()
        };
        let files = tr_format(
            locale,
            MessageId::PatchFilesCount,
            &[("count", &file_count.to_string())],
        );
        let stats = format!("+{additions}  -{deletions}  {files}");
        let separator = format!("  {}  ", self.theme.glyphs.separator());
        let first_line = if area.width < layout_contract::SPLIT_PANE_MIN_WIDTH {
            Line::from(vec![
                Span::styled(status, status_style),
                Span::styled(format!("  {stats}"), Style::default().fg(self.theme.muted)),
            ])
        } else {
            let reserved = UnicodeWidthStr::width(separator.as_str())
                + UnicodeWidthStr::width(status)
                + 2
                + UnicodeWidthStr::width(stats.as_str());
            Line::from(vec![
                Span::styled(
                    self.truncate_patch_review(
                        &patch.patch_id,
                        (area.width as usize).saturating_sub(reserved),
                    ),
                    Style::default()
                        .fg(self.theme.text)
                        .add_modifier(Modifier::BOLD),
                ),
                Span::styled(separator.clone(), Style::default().fg(self.theme.muted)),
                Span::styled(status, status_style),
                Span::styled(format!("  {stats}"), Style::default().fg(self.theme.muted)),
            ])
        };
        let mut metadata = vec![
            Span::styled(
                format!("{}  ", tr(locale, MessageId::PatchTransaction)),
                Style::default().fg(self.theme.muted),
            ),
            Span::styled(transaction.to_owned(), Style::default().fg(self.theme.text)),
        ];
        if area.width >= 100 {
            metadata.extend([
                Span::styled(separator.clone(), Style::default().fg(self.theme.muted)),
                Span::styled(
                    format!("{}  ", tr(locale, MessageId::PatchActor)),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(actor.to_owned(), Style::default().fg(self.theme.text)),
            ]);
        }
        if area.width >= layout_contract::SPLIT_PANE_MIN_WIDTH {
            metadata.extend([
                Span::styled(separator, Style::default().fg(self.theme.muted)),
                Span::styled(
                    format!("{}  ", tr(locale, MessageId::PatchVerify)),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(verify, Style::default().fg(self.theme.text)),
            ]);
        }
        frame.render_widget(Paragraph::new(vec![first_line, Line::from(metadata)]), area);
    }

    fn render_patch_review_transactions(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        overlay: &crate::overlay::ChangesOverlay,
        selected_patch: usize,
    ) {
        let locale = self.ui_locale();
        let regions = patch_review_section(area);
        self.render_patch_review_heading(
            frame,
            regions[0],
            tr(locale, MessageId::PatchTransaction),
            overlay.focus == ChangesFocus::Transactions,
        );
        let visible = regions[1].height as usize;
        let start =
            selection_window_start(selected_patch, overlay.projection.patches.len(), visible);
        for (visual_index, (index, patch)) in overlay
            .projection
            .patches
            .iter()
            .enumerate()
            .skip(start)
            .take(visible)
            .enumerate()
        {
            let row = Rect::new(
                regions[1].x,
                regions[1].y + visual_index as u16,
                regions[1].width,
                layout_contract::ROW_HEIGHT,
            );
            let disposition = overlay
                .patch_reviews
                .get(index)
                .map(|review| review.disposition)
                .unwrap_or(PatchDisposition::Unknown);
            let (status, _) = self.patch_disposition(disposition);
            let status = if disposition == PatchDisposition::Pending {
                tr(locale, MessageId::PatchPending)
            } else {
                status
            };
            let marker = if index == selected_patch {
                self.theme.glyphs.selected()
            } else {
                "  "
            };
            let available = row.width.saturating_sub(2) as usize;
            let identity_width = available.saturating_sub(
                UnicodeWidthStr::width(marker) + UnicodeWidthStr::width(status) + 2,
            );
            let identity = self.truncate_patch_reference(&patch.patch_id, identity_width);
            let summary = format!("{marker}{status}  {identity}");
            let style = if index == selected_patch && overlay.focus == ChangesFocus::Transactions {
                self.theme.selected()
            } else if index == selected_patch {
                Style::default()
                    .fg(self.theme.text)
                    .add_modifier(Modifier::BOLD)
            } else {
                Style::default().fg(self.theme.text)
            };
            frame.render_widget(Paragraph::new(summary).style(style), row);
            if !overlay.confirm_rollback {
                self.interactions.register(HitRegion {
                    component: ComponentId(12_600 + index as u64),
                    area: row,
                    action: Action::SelectChange(index),
                });
            }
        }
    }

    fn render_patch_review_files(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        overlay: &crate::overlay::ChangesOverlay,
        review: Option<&PatchReview>,
        selected_file: usize,
    ) {
        let locale = self.ui_locale();
        let regions = patch_review_section(area);
        self.render_patch_review_heading(
            frame,
            regions[0],
            tr(locale, MessageId::WorkspaceFiles),
            overlay.focus == ChangesFocus::Files,
        );
        let Some(review) = review else {
            return;
        };
        let visible = regions[1].height as usize;
        let start = selection_window_start(selected_file, review.files.len(), visible);
        for (visual_index, (index, file)) in review
            .files
            .iter()
            .enumerate()
            .skip(start)
            .take(visible)
            .enumerate()
        {
            let row = Rect::new(
                regions[1].x,
                regions[1].y + visual_index as u16,
                regions[1].width,
                layout_contract::ROW_HEIGHT,
            );
            let marker = if index == selected_file {
                self.theme.glyphs.selected_cell()
            } else {
                self.theme.glyphs.scroll_track()
            };
            let hunk_stats = format!("{}/{}@@", file.attributed_hunks, file.hunk_count);
            let counts = format!("+{} -{} {hunk_stats}", file.additions, file.deletions);
            let reserved = UnicodeWidthStr::width(counts.as_str()) + 7;
            let path_width = (row.width as usize).saturating_sub(reserved);
            let mut path = self.truncate_patch_reference(&file.path, path_width);
            path.push_str(
                &" ".repeat(path_width.saturating_sub(UnicodeWidthStr::width(path.as_str()))),
            );
            let line = Line::from(vec![
                Span::styled(
                    format!("{marker} {} ", file.kind.marker()),
                    Style::default().fg(self.theme.muted),
                ),
                Span::styled(path, Style::default().fg(self.theme.text)),
                Span::styled("  ", Style::default()),
                Span::styled(
                    format!("+{}", file.additions),
                    self.theme.transcript_added(),
                ),
                Span::styled(" ", Style::default()),
                Span::styled(
                    format!("-{}", file.deletions),
                    self.theme.transcript_removed(),
                ),
                Span::styled(format!(" {hunk_stats}"), self.theme.transcript_metadata()),
            ]);
            let style = if index == selected_file && overlay.focus == ChangesFocus::Files {
                self.theme.selected()
            } else if index == selected_file {
                Style::default().add_modifier(Modifier::BOLD)
            } else {
                Style::default()
            };
            frame.render_widget(Paragraph::new(line).style(style), row);
            if !overlay.confirm_rollback {
                self.interactions.register(HitRegion {
                    component: ComponentId(12_800 + index as u64),
                    area: row,
                    action: Action::SelectPatchReviewFile(index),
                });
            }
        }
    }

    fn render_patch_review_detail(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        overlay: &crate::overlay::ChangesOverlay,
        review: Option<&PatchReview>,
        selected_file: usize,
    ) {
        let locale = self.ui_locale();
        let Some(review) = review else {
            return;
        };
        let Some(file) = review.files.get(selected_file) else {
            return;
        };
        let regions = patch_review_section(area);
        let stats = format!(
            "+{} -{}  {}/{} @@",
            file.additions, file.deletions, file.attributed_hunks, file.hunk_count
        );
        let heading_path_width = regions[0]
            .width
            .saturating_sub(UnicodeWidthStr::width(stats.as_str()) as u16)
            .saturating_sub(layout_contract::PANEL_PADDING)
            as usize;
        let heading = format!(
            "{}  {stats}",
            self.truncate_patch_reference(&file.path, heading_path_width)
        );
        self.render_patch_review_heading(frame, regions[0], &heading, false);
        let mut body = regions[1];
        if overlay.confirm_rollback {
            let preview = overlay.rollback_preview.as_ref();
            let preview_height = preview
                .map_or(2, |preview| preview.files.len().saturating_add(1))
                .min(body.height as usize) as u16;
            let rows = Layout::default()
                .direction(Direction::Vertical)
                .constraints([
                    Constraint::Length(preview_height),
                    Constraint::Min(layout_contract::ROW_HEIGHT),
                ])
                .split(body);
            let mut lines = vec![Line::from(Span::styled(
                tr_format(
                    locale,
                    MessageId::PatchRollbackImpact,
                    &[(
                        "count",
                        &preview
                            .map_or(review.files.len(), |preview| preview.file_count)
                            .to_string(),
                    )],
                ),
                Style::default()
                    .fg(self.theme.warning)
                    .add_modifier(Modifier::BOLD),
            ))];
            if let Some(preview) = preview {
                for preview_file in preview
                    .files
                    .iter()
                    .take(rows[0].height.saturating_sub(1) as usize)
                {
                    let action = match preview_file.action.as_str() {
                        "restore" => tr(locale, MessageId::PatchRestore),
                        "delete" => tr(locale, MessageId::PatchDelete),
                        _ => tr(locale, MessageId::UnknownValue),
                    };
                    lines.push(Line::from(self.truncate_patch_review(
                        &format!("  {action}  {}", preview_file.path),
                        rows[0].width as usize,
                    )));
                }
            }
            frame.render_widget(Paragraph::new(lines), rows[0]);
            body = rows[1];
        } else if !overlay.rollback_error.is_empty() && body.height > 0 {
            frame.render_widget(
                Paragraph::new(
                    self.truncate_patch_review(&overlay.rollback_error, body.width as usize),
                )
                .style(Style::default().fg(self.theme.danger)),
                Rect::new(body.x, body.y, body.width, layout_contract::ROW_HEIGHT),
            );
            body.y = body.y.saturating_add(layout_contract::ROW_HEIGHT);
            body.height = body.height.saturating_sub(layout_contract::ROW_HEIGHT);
        }

        let reserve_continuation = usize::from(
            file.window.omitted_lines > 0 || file.window.hard_capped || review.source_hard_capped,
        );
        let visible = (body.height as usize).saturating_sub(reserve_continuation);
        let styles = TranscriptStyles {
            text: Style::default().fg(self.theme.text),
            metadata: self.theme.transcript_metadata(),
            added: self.theme.transcript_added(),
            removed: self.theme.transcript_removed(),
            ..TranscriptStyles::default()
        };
        let lines = file
            .window
            .lines
            .iter()
            .filter(|line| line.kind != crate::diff_render::DiffLineKind::Meta)
            .skip(overlay.scroll)
            .take(visible)
            .map(|line| styled_projected_diff_line(line, styles))
            .collect::<Vec<_>>();
        frame.render_widget(Paragraph::new(lines), body);
        if reserve_continuation > 0 && body.height > 0 {
            let row = Rect::new(
                body.x,
                body.bottom().saturating_sub(layout_contract::ROW_HEIGHT),
                body.width,
                layout_contract::ROW_HEIGHT,
            );
            let continuation = if file.window.omitted_exact && review.source_omitted_exact {
                tr_format(
                    locale,
                    MessageId::PatchReviewContinues,
                    &[(
                        "count",
                        &(file.window.omitted_lines + review.source_omitted_lines).to_string(),
                    )],
                )
            } else {
                tr(locale, MessageId::PatchReviewHardCapped).to_owned()
            };
            frame.render_widget(
                Paragraph::new(self.truncate_patch_review(&continuation, row.width as usize))
                    .style(Style::default().fg(self.theme.muted)),
                row,
            );
        }
    }

    fn render_patch_review_heading(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        title: &str,
        focused: bool,
    ) {
        let marker = if focused {
            self.theme.glyphs.selected_cell()
        } else {
            " "
        };
        let style = if focused {
            self.theme.focus()
        } else {
            Style::default()
                .fg(self.theme.muted)
                .add_modifier(Modifier::BOLD)
        };
        frame.render_widget(
            Paragraph::new(format!(
                "{marker} {}",
                self.truncate_patch_review(title, area.width.saturating_sub(2) as usize)
            ))
            .style(style),
            area,
        );
    }

    fn truncate_patch_review(&self, value: &str, max_width: usize) -> String {
        crate::render_contract::truncate_width_with_glyphs(value, max_width, self.theme.glyphs)
    }

    fn truncate_patch_reference(&self, value: &str, max_width: usize) -> String {
        if UnicodeWidthStr::width(value) <= max_width {
            return value.to_owned();
        }
        let ellipsis = self.theme.glyphs.ellipsis();
        if max_width < UnicodeWidthStr::width(ellipsis) {
            return String::new();
        }
        let content_width = max_width.saturating_sub(UnicodeWidthStr::width(ellipsis));
        let mut suffix = String::new();
        let mut suffix_width = 0;
        for character in value.chars().rev() {
            let width = UnicodeWidthChar::width(character).unwrap_or_default();
            if suffix_width + width > content_width {
                break;
            }
            suffix.insert(0, character);
            suffix_width += width;
        }
        format!("{ellipsis}{suffix}")
    }

    fn render_patch_review_divider(&self, frame: &mut Frame<'_>, area: Rect) {
        let line = Line::from(Span::styled(
            self.theme.glyphs.scroll_track(),
            Style::default().fg(self.theme.border),
        ));
        frame.render_widget(
            Paragraph::new(std::iter::repeat_n(line, area.height as usize).collect::<Vec<_>>()),
            area,
        );
    }

    fn render_patch_review_footer(
        &self,
        frame: &mut Frame<'_>,
        area: Rect,
        confirming: bool,
        rollback_ready: bool,
    ) {
        let locale = self.ui_locale();
        let mut shortcuts = if confirming {
            vec![
                ("Enter", tr(locale, MessageId::Rollback)),
                ("Esc", tr(locale, MessageId::Cancel)),
            ]
        } else {
            let mut shortcuts = vec![
                ("Tab", tr(locale, MessageId::Navigate)),
                ("R", tr(locale, MessageId::Refresh)),
                ("Esc", tr(locale, MessageId::Close)),
            ];
            if rollback_ready {
                shortcuts.insert(1, ("B", tr(locale, MessageId::Rollback)));
            }
            shortcuts
        };
        let shortcuts_width = |items: &[(&str, &str)]| {
            items
                .iter()
                .map(|(key, label)| {
                    UnicodeWidthStr::width(*key) + UnicodeWidthStr::width(*label) + 5
                })
                .sum::<usize>()
        };
        if shortcuts_width(&shortcuts) > area.width as usize {
            shortcuts = if confirming {
                vec![("Esc", tr(locale, MessageId::Cancel))]
            } else if rollback_ready {
                vec![
                    ("B", tr(locale, MessageId::Rollback)),
                    ("Esc", tr(locale, MessageId::Close)),
                ]
            } else {
                vec![
                    ("R", tr(locale, MessageId::Refresh)),
                    ("Esc", tr(locale, MessageId::Close)),
                ]
            };
        }
        let mut spans = Vec::new();
        for (key, label) in shortcuts {
            spans.push(Span::styled(format!(" {key} "), self.theme.keycap()));
            spans.push(Span::styled(
                format!(" {label}  "),
                Style::default().fg(self.theme.muted),
            ));
        }
        frame.render_widget(Paragraph::new(Line::from(spans)), area);
    }

    fn patch_disposition(&self, disposition: PatchDisposition) -> (&'static str, Style) {
        let locale = self.ui_locale();
        match disposition {
            PatchDisposition::Accepted => (
                tr(locale, MessageId::PatchAccepted),
                Style::default()
                    .fg(self.theme.accent)
                    .add_modifier(Modifier::BOLD),
            ),
            PatchDisposition::Rejected => (
                tr(locale, MessageId::PatchRejected),
                Style::default()
                    .fg(self.theme.danger)
                    .add_modifier(Modifier::BOLD),
            ),
            PatchDisposition::Applied => (
                tr(locale, MessageId::PatchApplied),
                Style::default()
                    .fg(self.theme.accent)
                    .add_modifier(Modifier::BOLD),
            ),
            PatchDisposition::Pending => (
                tr(locale, MessageId::PatchPendingReview),
                Style::default()
                    .fg(self.theme.warning)
                    .add_modifier(Modifier::BOLD),
            ),
            PatchDisposition::Failed => (
                tr(locale, MessageId::StatusFailed),
                Style::default()
                    .fg(self.theme.danger)
                    .add_modifier(Modifier::BOLD),
            ),
            PatchDisposition::Unknown => (
                tr(locale, MessageId::UnknownValue),
                Style::default().fg(self.theme.muted),
            ),
        }
    }

    fn render_workspace_patches(
        &mut self,
        frame: &mut Frame<'_>,
        columns: &[Rect],
        overlay: &crate::overlay::ChangesOverlay,
    ) {
        let locale = self.ui_locale();
        let patches = &overlay.projection.patches;
        for (index, patch) in patches.iter().take(columns[0].height as usize).enumerate() {
            let row = Rect::new(
                columns[0].x,
                columns[0].y + index as u16,
                columns[0].width,
                layout_contract::ROW_HEIGHT,
            );
            let file = patch
                .affected_files
                .first()
                .map(String::as_str)
                .unwrap_or("-");
            let extra = match patch.affected_files.len() {
                0 => 0,
                count => count - 1,
            };
            let summary = if extra == 0 {
                format!("{}  {file}", patch.status)
            } else {
                format!("{}  {file} +{extra}", patch.status)
            };
            let style = if index == overlay.selected {
                self.theme.selected()
            } else {
                Style::default().fg(self.theme.text)
            };
            frame.render_widget(
                Paragraph::new(truncate_cells(&summary, columns[0].width as usize)).style(style),
                row,
            );
            self.interactions.register(HitRegion {
                component: ComponentId(12_600 + index as u64),
                area: row,
                action: Action::SelectChange(index),
            });
        }

        let Some(patch) = patches.get(overlay.selected) else {
            return;
        };
        let rollback_ready = !patch.rollback_pointer.is_empty()
            && !matches!(patch.status.as_str(), "rolled_back" | "failed" | "proposed");
        let mut details = vec![
            Line::from(Span::styled(
                patch.patch_id.as_str(),
                Style::default()
                    .fg(self.theme.accent)
                    .add_modifier(Modifier::BOLD),
            )),
            Line::from(format!(
                "{}  {}",
                tr(locale, MessageId::Status),
                patch.status
            )),
            Line::from(format!(
                "{}  {}",
                tr(locale, MessageId::PatchActor),
                if patch.actor.is_empty() {
                    "-"
                } else {
                    &patch.actor
                }
            )),
            Line::from(format!(
                "{}  {}",
                tr(locale, MessageId::PatchTransaction),
                patch.transaction_id
            )),
            Line::from(format!(
                "{}  {}  {}",
                tr(locale, MessageId::PatchApproval),
                patch.approval_status,
                patch.approval_id
            )),
            Line::from(format!(
                "{}  {}",
                tr(locale, MessageId::PatchVerify),
                patch
                    .verify_result
                    .as_ref()
                    .map(|result| result.status.as_str())
                    .unwrap_or(&patch.test_status)
            )),
            Line::from(format!(
                "{}  {}",
                tr(locale, MessageId::PatchAudit),
                patch.audit_event_ids.join(", ")
            )),
            Line::from(format!(
                "{}  {}",
                tr(locale, MessageId::Reason),
                patch.reason
            )),
            Line::from(""),
        ];
        for file in &patch.affected_files {
            details.push(Line::from(format!(
                "  {} {file}",
                self.theme.glyphs.scroll_track()
            )));
        }
        for hunk in &patch.hunk_attributions {
            details.push(Line::from(format!(
                "  @@ {}:{}  {}  {}  {}",
                hunk.file, hunk.hunk_index, hunk.actor, hunk.approval_id, hunk.verify_result
            )));
        }
        details.push(Line::from(""));
        if overlay.confirm_rollback {
            let preview = overlay.rollback_preview.as_ref();
            details.push(Line::from(Span::styled(
                tr_format(
                    locale,
                    MessageId::PatchRollbackPreview,
                    &[(
                        "count",
                        &preview
                            .map_or(patch.affected_files.len(), |preview| preview.file_count)
                            .to_string(),
                    )],
                ),
                Style::default()
                    .fg(self.theme.warning)
                    .add_modifier(Modifier::BOLD),
            )));
            if let Some(preview) = preview {
                for file in &preview.files {
                    let action = match file.action.as_str() {
                        "restore" => tr(locale, MessageId::PatchRestore),
                        "delete" => tr(locale, MessageId::PatchDelete),
                        _ => tr(locale, MessageId::UnknownValue),
                    };
                    details.push(Line::from(format!("  {action}  {}", file.path)));
                }
            }
        } else if !overlay.rollback_error.is_empty() {
            details.push(Line::from(Span::styled(
                overlay.rollback_error.as_str(),
                Style::default().fg(self.theme.danger),
            )));
        } else {
            details.push(Line::from(format!(
                "{}  {}  | {}",
                tr(locale, MessageId::Rollback),
                if rollback_ready {
                    tr(locale, MessageId::StatusReady)
                } else {
                    tr(locale, MessageId::NotAvailable)
                },
                tr(locale, MessageId::PatchRollbackHint)
            )));
        }

        let meta_height = (details.len() as u16).min(
            columns[1]
                .height
                .saturating_sub(layout_contract::ROW_HEIGHT),
        );
        let chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(meta_height),
                Constraint::Min(layout_contract::ROW_HEIGHT),
            ])
            .split(columns[1]);
        frame.render_widget(
            Paragraph::new(details).wrap(Wrap { trim: false }),
            chunks[0],
        );
        if let Some(review) = overlay.patch_reviews.get(overlay.selected) {
            let visible = chunks[1].height as usize;
            let lines = review
                .files
                .iter()
                .flat_map(|file| file.window.lines.iter())
                .skip(overlay.scroll)
                .take(visible)
                .map(|line| {
                    styled_compact_projected_diff_line(
                        line,
                        Style::default().fg(self.theme.text),
                        Style::default().fg(self.theme.success),
                        Style::default().fg(self.theme.danger),
                        Style::default().fg(self.theme.warning),
                    )
                })
                .collect::<Vec<_>>();
            frame.render_widget(Paragraph::new(lines).wrap(Wrap { trim: false }), chunks[1]);
        }
    }

    fn render_workspace_changes(
        &mut self,
        frame: &mut Frame<'_>,
        columns: &[Rect],
        overlay: &crate::overlay::ChangesOverlay,
    ) {
        let sep = self.theme.glyphs.separator();
        let locale = self.ui_locale();
        let report = &overlay.projection.workspace_diff;
        for (index, file) in report
            .files
            .iter()
            .take(columns[0].height as usize)
            .enumerate()
        {
            let row = Rect::new(
                columns[0].x,
                columns[0].y + index as u16,
                columns[0].width,
                layout_contract::ROW_HEIGHT,
            );
            let marker = if file.binary {
                tr(locale, MessageId::Binary).to_owned()
            } else {
                localized_file_change_kind(locale, file.product_kind())
            };
            let style = if index == overlay.selected {
                self.theme.selected()
            } else {
                Style::default().fg(self.theme.text)
            };
            frame.render_widget(
                Paragraph::new(format!(
                    "{marker:<width$} {}",
                    file.path,
                    width = layout_contract::CHANGE_MARKER_WIDTH
                ))
                .style(style),
                row,
            );
            self.interactions.register(HitRegion {
                component: ComponentId(12_400 + index as u64),
                area: row,
                action: Action::SelectChange(index),
            });
        }

        let Some(file) = report.files.get(overlay.selected) else {
            return;
        };
        let header = Rect::new(
            columns[1].x,
            columns[1].y,
            columns[1].width,
            layout_contract::CONTROL_HEIGHT,
        );
        frame.render_widget(
            Paragraph::new(vec![
                Line::from(Span::styled(
                    file.path.as_str(),
                    Style::default()
                        .fg(self.theme.accent)
                        .add_modifier(Modifier::BOLD),
                )),
                Line::from({
                    let mut flags = Vec::new();
                    if file.untracked {
                        flags.push(tr(locale, MessageId::NewFile));
                    }
                    if file.binary {
                        flags.push(tr(locale, MessageId::Binary));
                    }
                    if file.truncated || report.truncated {
                        flags.push(tr(locale, MessageId::Truncated));
                    }
                    let flags = if flags.is_empty() {
                        String::new()
                    } else {
                        format!(" {sep} {}", flags.join(&format!(" {sep} ")))
                    };
                    tr_format(
                        locale,
                        MessageId::BytesSummary,
                        &[("bytes", &file.bytes.to_string()), ("flags", &flags)],
                    )
                }),
            ]),
            header,
        );
        let body = Rect::new(
            columns[1].x,
            columns[1].y.saturating_add(layout_contract::CONTROL_HEIGHT),
            columns[1].width,
            columns[1]
                .height
                .saturating_sub(layout_contract::COMPACT_SECTION_HEIGHT),
        );
        let lines = if file.binary {
            vec![Line::from(tr(locale, MessageId::BinaryContentUnavailable))]
        } else if file.diff.is_empty() {
            vec![Line::from(tr(locale, MessageId::NoTextDiff))]
        } else {
            file.diff
                .lines()
                .map(|line| {
                    let style = if line.starts_with("+++") || line.starts_with("---") {
                        Style::default().fg(self.theme.accent)
                    } else if line.starts_with('+') {
                        Style::default().fg(self.theme.success)
                    } else if line.starts_with('-') {
                        Style::default().fg(self.theme.danger)
                    } else if line.starts_with("@@") {
                        Style::default().fg(self.theme.warning)
                    } else {
                        Style::default().fg(self.theme.text)
                    };
                    Line::from(Span::styled(line, style))
                })
                .collect()
        };
        frame.render_widget(
            Paragraph::new(lines)
                .wrap(Wrap { trim: false })
                .scroll((overlay.scroll.min(u16::MAX as usize) as u16, 0)),
            body,
        );
        let (footer, style) = if overlay.load.loading {
            (
                tr(locale, MessageId::ScanningWorkspace).to_owned(),
                Style::default().fg(self.theme.muted),
            )
        } else if !overlay.load.error.is_empty() {
            (
                tr_format(
                    locale,
                    MessageId::LoadChangesFailed,
                    &[("error", overlay.load.error.as_str())],
                ),
                Style::default().fg(self.theme.danger),
            )
        } else {
            (
                tr(locale, MessageId::ChangesNavigationHint).to_owned(),
                Style::default().fg(self.theme.muted),
            )
        };
        frame.render_widget(
            Paragraph::new(footer).style(style),
            Rect::new(
                columns[1].x,
                columns[1]
                    .bottom()
                    .saturating_sub(layout_contract::ROW_HEIGHT),
                columns[1].width,
                layout_contract::ROW_HEIGHT,
            ),
        );
    }

    fn render_modal_buttons(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        buttons: &[(&str, Action, u64)],
    ) {
        let mut x = area.x;
        for (label, action, id) in buttons {
            let rect = modal_button_rect(area, x, label);
            let component = ComponentId(*id);
            let style = if self.interactions.hovered(component) {
                self.theme.selected()
            } else {
                Style::default().fg(self.theme.accent)
            };
            frame.render_widget(Paragraph::new(format!("[ {label} ]")).style(style), rect);
            self.interactions.register(HitRegion {
                component,
                area: rect,
                action: action.clone(),
            });
            x = x.saturating_add(rect.width + layout_contract::ROW_HEIGHT);
        }
    }
}

fn model_health_color(app: &App, status: &str) -> ratatui::style::Color {
    match status {
        "ready" | "" => app.theme.success,
        "probing" => app.theme.warning,
        "rate_limited" | "circuit_open" | "auth_error" | "unavailable" => app.theme.danger,
        _ => app.theme.muted,
    }
}

/// Terminal cell width for layout (CJK is typically 2 cells per character).
fn display_cells(value: &str) -> u16 {
    UnicodeWidthStr::width(value).min(u16::MAX as usize) as u16
}

/// Right-hand provider status column: glyph + gap + label + pad, in cells.
fn provider_state_column_width(glyph: &str, state: &str) -> u16 {
    display_cells(glyph)
        .saturating_add(layout_contract::LABEL_CELL_PAD)
        .saturating_add(display_cells(state))
        .saturating_add(layout_contract::LABEL_CELL_PAD)
        .max(layout_contract::PANEL_PADDING)
}

fn provider_has_compatible_models(provider: &ModelProvider) -> bool {
    provider.models.iter().any(|model| model.available)
}

/// Padded action button width for ` {label} `.
fn label_button_width(label: &str) -> u16 {
    display_cells(label).saturating_add(layout_contract::ACTION_BUTTON_PAD)
}

#[cfg(test)]
mod layout_width_tests {
    use super::{display_cells, label_button_width, provider_state_column_width};

    #[test]
    fn cjk_ready_state_is_wider_than_char_count() {
        // 「就绪」 is 2 chars but 4 terminal cells; char-count layout was clipping to 「就」.
        assert_eq!(display_cells("就绪"), 4);
        assert!(display_cells("就绪") > "就绪".chars().count() as u16);
        // Use plain ASCII stand-ins; product glyph literals are owned by glyphs.rs.
        let width = provider_state_column_width("*", "就绪");
        assert!(
            width >= 6,
            "status column must fit glyph + CJK label, got {width}"
        );
        assert!(width > ("就绪".chars().count() + 3) as u16);
    }

    #[test]
    fn ascii_ready_stays_compact() {
        assert_eq!(display_cells("Ready"), 5);
        let width = provider_state_column_width("*", "Ready");
        assert!(width >= 7);
    }

    #[test]
    fn chinese_action_buttons_use_cell_width() {
        assert_eq!(label_button_width("使用模型"), 10); // 4 chars × 2 + 2 pad
        assert_eq!(label_button_width("Use model"), 11); // 9 + 2
    }
}

fn modal_button_rect(area: Rect, x: u16, label: &str) -> Rect {
    let visible_width = label
        .width()
        .saturating_add(layout_contract::PANEL_PADDING as usize)
        .min(u16::MAX as usize) as u16;
    Rect::new(
        x,
        area.y,
        visible_width.min(area.right().saturating_sub(x)),
        u16::from(area.height > 0),
    )
}

fn localized_product_status(locale: Locale, status: ProductStatus<'_>) -> String {
    let id = match status {
        ProductStatus::Ready => MessageId::StatusReady,
        ProductStatus::Queued => MessageId::StatusQueued,
        ProductStatus::Running => MessageId::StatusRunning,
        ProductStatus::WaitingApproval | ProductStatus::WaitingInput => {
            MessageId::StatusWaitingApproval
        }
        ProductStatus::Paused => MessageId::StatusPaused,
        ProductStatus::Cancelled => MessageId::StatusCancelled,
        ProductStatus::Failed => MessageId::StatusFailed,
        ProductStatus::Unknown(raw) => {
            return tr_format(locale, MessageId::StatusUnknown, &[("status", raw)]);
        }
    };
    tr(locale, id).to_owned()
}

fn localized_agent_category(locale: Locale, category: AgentCategory<'_>) -> String {
    let id = match category {
        AgentCategory::NeedsInput => MessageId::AnswerRequired,
        AgentCategory::Working => MessageId::ExecutionWorking,
        AgentCategory::Completed => MessageId::Done,
        AgentCategory::Unknown(_) => MessageId::UnknownValue,
    };
    tr(locale, id).to_owned()
}

fn localized_activity_kind(locale: Locale, kind: ActivityKind<'_>) -> String {
    let id = match kind {
        ActivityKind::Message => MessageId::Message,
        ActivityKind::Tool => MessageId::TranscriptAction,
        ActivityKind::Change => MessageId::Changes,
        ActivityKind::Governance => MessageId::Review,
        ActivityKind::Execution => MessageId::ExecutionStatus,
        ActivityKind::Unknown(_) => MessageId::TranscriptStatus,
    };
    tr(locale, id).to_owned()
}

fn localized_change_kind(locale: Locale, kind: ChangeKind<'_>) -> String {
    let id = match kind {
        ChangeKind::File => MessageId::WorkspaceFile,
        ChangeKind::Turn => MessageId::Changes,
        ChangeKind::Unknown(_) => MessageId::Changes,
    };
    tr(locale, id).to_owned()
}

fn localized_file_change_kind(locale: Locale, kind: FileChangeKind<'_>) -> String {
    let id = match kind {
        FileChangeKind::Added => MessageId::NewFile,
        FileChangeKind::Modified | FileChangeKind::Renamed | FileChangeKind::Deleted => {
            MessageId::Changes
        }
        FileChangeKind::Unmerged => MessageId::StatusFailed,
        FileChangeKind::Unknown(_) => MessageId::UnknownValue,
    };
    tr(locale, id).to_owned()
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct TranscriptLayoutItem {
    index: usize,
    top: usize,
    height: usize,
    header_height: usize,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct TranscriptLayout {
    items: Vec<TranscriptLayoutItem>,
    total_height: usize,
    viewport_start: usize,
    max_scroll: usize,
}

#[derive(Debug, Clone, Copy)]
struct TranscriptViewport {
    locale: Locale,
    density: DensityMode,
    content_width: u16,
    tool_expand_key: &'static str,
    height: usize,
    requested_scroll: usize,
    follow_bottom: bool,
    anchor: Option<usize>,
}

#[derive(Debug, Clone, Copy, Default)]
struct TranscriptStyles {
    glyphs: Glyphs,
    label: Style,
    text: Style,
    metadata: Style,
    added: Style,
    removed: Style,
    code: Style,
    link: Style,
    headings: [Style; 6],
}

fn truncate_cells(value: &str, max_width: usize) -> String {
    crate::render_contract::truncate_width(value, max_width)
}

impl TranscriptLayout {
    fn new(
        blocks: &[TranscriptBlock],
        viewport: TranscriptViewport,
        height_cache: &mut TranscriptHeightCache,
    ) -> Self {
        let TranscriptViewport {
            locale,
            density,
            content_width,
            tool_expand_key,
            height: viewport_height,
            requested_scroll,
            follow_bottom,
            anchor,
        } = viewport;
        let mut items = Vec::with_capacity(blocks.len());
        let mut top = 0_usize;
        let mut previous_visible_index = None;
        for (index, block) in blocks.iter().enumerate() {
            let (height, header_height) = if transcript_block_hidden(block) {
                (0, 0)
            } else {
                match height_cache.get(&block.id) {
                    Some(entry)
                        if entry.revision == block.layout_revision
                            && entry.width == content_width
                            && entry.locale == locale
                            && entry.density == density
                            && entry.expand_key == tool_expand_key =>
                    {
                        (entry.height, entry.header_height)
                    }
                    _ => {
                        let (height, header_height) =
                            transcript_block_height_with_tool_key_and_density(
                                block,
                                locale,
                                content_width,
                                tool_expand_key,
                                density,
                            );
                        height_cache.insert(
                            block.id.clone(),
                            TranscriptHeightCacheEntry {
                                revision: block.layout_revision,
                                width: content_width,
                                locale,
                                density,
                                expand_key: tool_expand_key,
                                height,
                                header_height,
                            },
                        );
                        (height, header_height)
                    }
                }
            };
            if height > 0 {
                if let Some(previous_index) = previous_visible_index {
                    top = top.saturating_add(transcript_gap_for_density(
                        &blocks[previous_index],
                        block,
                        density,
                    ));
                }
                previous_visible_index = Some(index);
            };
            items.push(TranscriptLayoutItem {
                index,
                top,
                height,
                header_height,
            });
            top = top.saturating_add(height);
        }
        if previous_visible_index.is_some() {
            top = top.saturating_add(density.profile().final_gap);
        }
        let max_scroll = top.saturating_sub(viewport_height);
        let viewport_start = anchor
            .and_then(|index| items.get(index))
            .map(|item| {
                item.top
                    .saturating_sub(viewport_height.saturating_div(3))
                    .min(max_scroll)
            })
            .unwrap_or_else(|| {
                if follow_bottom {
                    max_scroll
                } else {
                    requested_scroll.min(max_scroll)
                }
            });
        Self {
            items,
            total_height: top,
            viewport_start,
            max_scroll,
        }
    }
}

#[cfg(test)]
fn transcript_block_height_with_tool_key(
    block: &TranscriptBlock,
    locale: Locale,
    content_width: u16,
    tool_expand_key: &'static str,
) -> (usize, usize) {
    transcript_block_height_with_tool_key_and_density(
        block,
        locale,
        content_width,
        tool_expand_key,
        DensityMode::Compact,
    )
}

fn transcript_block_height_with_tool_key_and_density(
    block: &TranscriptBlock,
    locale: Locale,
    content_width: u16,
    tool_expand_key: &'static str,
    density: DensityMode,
) -> (usize, usize) {
    let lines = transcript_lines_with_tool_key_and_density(
        block,
        locale,
        TranscriptStyles::default(),
        content_width,
        tool_expand_key,
        density,
    );
    let header_height = Paragraph::new(vec![lines[0].clone()])
        .wrap(Wrap { trim: false })
        .line_count(content_width)
        .max(layout_contract::MIN_DIMENSION as usize);
    let height = Paragraph::new(lines)
        .wrap(Wrap { trim: false })
        .line_count(content_width)
        .max(layout_contract::MIN_DIMENSION as usize);
    (height, header_height)
}

fn transcript_block_hidden(block: &TranscriptBlock) -> bool {
    block.tool_members.is_empty()
        && block.title.trim().is_empty()
        && block.body.trim().is_empty()
        && block.status.trim().is_empty()
}

fn chrome_slot_key(kind: ChromeSlotKind) -> &'static str {
    match kind {
        ChromeSlotKind::Run => "run",
        ChromeSlotKind::Queue => "queue",
        ChromeSlotKind::Hitl => "hitl",
        ChromeSlotKind::Isolation => "isolation",
        ChromeSlotKind::Context => "context",
        ChromeSlotKind::ScreenMode => "screen-mode",
    }
}

fn chrome_capability_action(capability: ChromeCapability) -> Action {
    match capability {
        ChromeCapability::OpenQueue => Action::OpenQueue,
    }
}

fn chrome_tone_style(tone: ChromeTone, theme: crate::theme::Theme) -> Style {
    match tone {
        ChromeTone::Muted => theme.muted(),
        ChromeTone::Accent => Style::default().fg(theme.accent),
        ChromeTone::Warning => Style::default().fg(theme.warning),
        ChromeTone::Danger => Style::default().fg(theme.danger),
    }
}

fn context_title(locale: Locale) -> &'static str {
    match locale {
        Locale::En => "Context",
        Locale::ZhHans => "上下文",
        Locale::ZhHant => "上下文",
        Locale::Ja => "コンテキスト",
        Locale::Ko => "컨텍스트",
        Locale::Es => "Contexto",
        Locale::Fr => "Contexte",
    }
}

fn context_style_for_percent(percent: u8, threshold: &str, theme: crate::theme::Theme) -> Style {
    if theme.no_color {
        theme.muted()
    } else if percent >= 90 || threshold == "critical" {
        Style::default().fg(theme.danger)
    } else if percent >= 80 || threshold == "warning" {
        Style::default().fg(theme.warning)
    } else {
        theme.muted()
    }
}

fn fallback_label(value: &str) -> &str {
    if value.trim().is_empty() {
        "unknown"
    } else {
        value
    }
}

fn short_hash(value: &str) -> &str {
    value.get(..12).unwrap_or(value)
}

#[cfg(test)]
fn transcript_block_height(
    block: &TranscriptBlock,
    locale: Locale,
    content_width: u16,
) -> (usize, usize) {
    transcript_block_height_with_tool_key(
        block,
        locale,
        content_width,
        crate::keybinding::KeyBindings::default()
            .expand_tools
            .label(),
    )
}

#[cfg(test)]
fn transcript_lines(
    block: &TranscriptBlock,
    locale: Locale,
    styles: TranscriptStyles,
    content_width: u16,
) -> Vec<Line<'static>> {
    transcript_lines_with_tool_key(
        block,
        locale,
        styles,
        content_width,
        crate::keybinding::KeyBindings::default()
            .expand_tools
            .label(),
    )
}

#[cfg(test)]
fn transcript_lines_with_tool_key(
    block: &TranscriptBlock,
    locale: Locale,
    styles: TranscriptStyles,
    content_width: u16,
    tool_expand_key: &'static str,
) -> Vec<Line<'static>> {
    transcript_lines_with_tool_key_and_density(
        block,
        locale,
        styles,
        content_width,
        tool_expand_key,
        DensityMode::Compact,
    )
}

fn transcript_lines_with_tool_key_and_density(
    block: &TranscriptBlock,
    locale: Locale,
    styles: TranscriptStyles,
    content_width: u16,
    tool_expand_key: &'static str,
    density: DensityMode,
) -> Vec<Line<'static>> {
    let TranscriptStyles {
        glyphs,
        label: label_style,
        text: text_style,
        metadata: metadata_style,
        added: added_style,
        removed: removed_style,
        code: code_style,
        link: link_style,
        headings,
    } = styles;
    let cell_kind = SemanticCellKind::from_block(block);
    if cell_kind == SemanticCellKind::User {
        let marker = if block.user_kind == UserBlockKind::Steer {
            glyphs.steer()
        } else {
            glyphs.role_prefix()
        };
        let prefix = transcript_role_prefix(
            marker,
            tr(locale, MessageId::TranscriptYou),
            label_style,
            metadata_style,
        );
        let mut body = block.body.split('\n');
        let mut first = Vec::new();
        if block.user_kind == UserBlockKind::Steer {
            first.push(Span::styled(
                format!("{}  ", tr(locale, MessageId::TranscriptYouSteered)),
                metadata_style,
            ));
        }
        first.push(Span::styled(
            body.next().unwrap_or_default().to_owned(),
            text_style,
        ));
        if !block.status.is_empty() {
            first.push(Span::styled(format!("  {}", block.status), metadata_style));
        }
        let logical_lines = std::iter::once(Line::from(first))
            .chain(body.map(|line| Line::from(Span::styled(line.to_owned(), text_style))));
        return wrap_styled_lines(logical_lines, usize::from(content_width), &prefix);
    }
    if cell_kind == SemanticCellKind::Assistant {
        let prefix = transcript_role_prefix(
            glyphs.role_prefix(),
            tr(locale, MessageId::TranscriptCarina),
            label_style,
            metadata_style,
        );
        return render_markdown_prefixed(
            &block.body,
            content_width,
            MarkdownTheme {
                glyphs,
                text: text_style,
                accent: label_style,
                muted: metadata_style,
                code: code_style,
                quote: label_style,
                link: link_style,
                headings,
                code_keyword: label_style,
                code_string: code_style,
                code_comment: metadata_style,
                code_number: code_style,
                code_type: label_style,
            },
            &prefix,
        );
    }
    if let Some(failure) = &block.failure {
        let action = match failure.action {
            FailureAction::Retry | FailureAction::RunAgain => {
                format!("[Alt-R] {}  [Alt-C] ID", tr(locale, MessageId::Retry))
            }
            FailureAction::Recovering => tr(locale, MessageId::ExecutionWorking).to_owned(),
            FailureAction::Disabled => "[Alt-C] ID".into(),
        };
        let logical_lines = [
            Line::from(vec![
                Span::styled(glyphs.failure_prefix(), removed_style),
                Span::styled(
                    block.title.clone(),
                    removed_style.add_modifier(Modifier::BOLD),
                ),
            ]),
            Line::from(vec![
                Span::styled(glyphs.tool_gutter(), metadata_style),
                Span::styled(
                    format!("{}  ", tr(locale, MessageId::Agent)),
                    metadata_style,
                ),
                Span::styled(failure.owner.clone(), text_style),
            ]),
            Line::from(vec![
                Span::styled(glyphs.tool_gutter(), metadata_style),
                Span::styled(
                    format!("{}  ", tr(locale, MessageId::Reason)),
                    metadata_style,
                ),
                Span::styled(
                    localize_operator_failure_reason(locale, &failure.reason),
                    metadata_style,
                ),
            ]),
            Line::from(vec![
                Span::styled(glyphs.tool_gutter(), metadata_style),
                Span::styled(action, label_style),
            ]),
        ];
        return wrap_styled_lines(
            logical_lines,
            usize::from(content_width),
            &StyledPrefix::default(),
        );
    }
    let label = transcript_label(locale, block.kind);
    let marker = if block.is_collapsible() {
        if block.expanded {
            glyphs.disclosure_open()
        } else {
            glyphs.disclosure_closed()
        }
    } else {
        match cell_kind {
            SemanticCellKind::Approval => glyphs.warning_prefix(),
            SemanticCellKind::Failure => glyphs.failure_prefix(),
            SemanticCellKind::Thinking
            | SemanticCellKind::Tool
            | SemanticCellKind::ToolGroup
            | SemanticCellKind::Patch
            | SemanticCellKind::Notice => glyphs.bullet_prefix(),
            SemanticCellKind::User | SemanticCellKind::Assistant => "",
        }
    };
    let label = if block.kind == BlockKind::Tool {
        ""
    } else {
        label
    };
    let block_title = block.localized_title(locale);
    let block_status = block.localized_status(locale);
    // When intent is primary, show technical target as dim secondary text on the
    // same row (member title holds the path/command for single-tool blocks).
    let dim_target = tool_row_dim_target(block, &block_title);
    let mut title_spans = vec![
        Span::styled(format!("{marker}{label}"), label_style),
        Span::styled(
            format!(
                "{}{}",
                if label.is_empty() { "" } else { "  " },
                block_title
            ),
            text_style,
        ),
    ];
    if let Some(target) = dim_target {
        title_spans.push(Span::styled(format!("  {target}"), metadata_style));
    }
    title_spans.push(Span::styled(
        if block.additions == 0 {
            String::new()
        } else {
            format!("  +{}", block.additions)
        },
        added_style,
    ));
    title_spans.push(Span::styled(
        if block.deletions == 0 {
            String::new()
        } else {
            format!(" -{}", block.deletions)
        },
        removed_style,
    ));
    title_spans.push(Span::styled(
        if block_status.is_empty() {
            String::new()
        } else {
            format!("  {block_status}")
        },
        metadata_style,
    ));
    let mut lines = vec![Line::from(title_spans)];
    let tool_context = ToolLineContext {
        locale,
        density,
        styles,
        content_width,
        expand_key: tool_expand_key,
    };
    if block.kind == BlockKind::Tool && block.tool_members.len() > 1 {
        lines.extend(tool_group_lines(block, tool_context));
    } else if block.kind == BlockKind::Tool && !block.body.is_empty() {
        lines.extend(tool_detail_lines(
            &block.body,
            block.body_kind,
            block.expanded,
            tool_context,
            false,
        ));
    } else if block.expanded && !block.body.is_empty() {
        lines.extend(block.body.split('\n').map(|line| {
            let mut detail = styled_detail_line(line, block.body_kind, styles);
            detail
                .spans
                .insert(0, Span::styled(glyphs.tool_gutter(), metadata_style));
            detail
        }));
    }
    lines
}

#[derive(Debug, Clone, Copy)]
struct ToolLineContext {
    locale: Locale,
    density: DensityMode,
    styles: TranscriptStyles,
    content_width: u16,
    expand_key: &'static str,
}

/// Dim secondary target for intent-first single-tool rows.
///
/// When the title already embeds the technical target (no intent), returns None
/// to avoid duplicating path/command after the primary text.
fn tool_row_dim_target(block: &TranscriptBlock, title: &str) -> Option<String> {
    if block.kind != BlockKind::Tool || block.tool_members.len() != 1 {
        return None;
    }
    let target = block.tool_members[0].title.trim();
    if target.is_empty() {
        return None;
    }
    if title.contains(target) {
        return None;
    }
    Some(target.to_owned())
}

fn tool_group_lines(block: &TranscriptBlock, context: ToolLineContext) -> Vec<Line<'static>> {
    let profile = context.density.profile();
    let visibility = visible_group_members_with_limits(
        block.tool_members.len(),
        block.expanded,
        profile.collapsed_group_members,
        usize::MAX,
    );
    let mut lines = Vec::new();
    for member in block.tool_members.iter().take(visibility.visible) {
        lines.push(tool_group_member_line(member, context.styles));
        if !member.body.is_empty() {
            lines.extend(tool_detail_lines(
                &member.body,
                member.body_kind,
                block.expanded,
                context,
                true,
            ));
        }
    }
    if visibility.omitted > 0 {
        lines.push(tool_omission_line(
            visibility.omitted,
            MessageId::ToolCallOmitted,
            MessageId::ToolCallsOmitted,
            context,
            false,
        ));
    }
    lines
}

fn tool_group_member_line(member: &ToolGroupMember, styles: TranscriptStyles) -> Line<'static> {
    let (marker, marker_style) = if member.is_failure() {
        (styles.glyphs.failure(), styles.removed)
    } else if member.is_running() {
        (styles.glyphs.ready(), styles.label)
    } else {
        ("", styles.metadata)
    };
    let marker = if marker.is_empty() {
        String::new()
    } else {
        format!("{marker} ")
    };
    Line::from(vec![
        Span::styled(styles.glyphs.tool_gutter(), styles.metadata),
        Span::styled(marker, marker_style),
        Span::styled(member.title.clone(), styles.text),
        Span::styled(
            if member.additions == 0 {
                String::new()
            } else {
                format!("  +{}", member.additions)
            },
            styles.added,
        ),
        Span::styled(
            if member.deletions == 0 {
                String::new()
            } else {
                format!(" -{}", member.deletions)
            },
            styles.removed,
        ),
        Span::styled(
            if member.status.is_empty() {
                String::new()
            } else {
                format!("  {}", member.status)
            },
            styles.metadata,
        ),
    ])
}

fn tool_detail_lines(
    body: &str,
    body_kind: BlockBodyKind,
    expanded: bool,
    context: ToolLineContext,
    nested: bool,
) -> Vec<Line<'static>> {
    let prefix = if nested {
        StyledPrefix::repeating(vec![
            Span::styled(context.styles.glyphs.tool_gutter(), context.styles.metadata),
            Span::raw("  "),
        ])
    } else {
        StyledPrefix::repeating(vec![Span::styled(
            context.styles.glyphs.tool_gutter(),
            context.styles.metadata,
        )])
    };
    if body_kind == BlockBodyKind::Diff {
        if !expanded {
            return collapsed_edit_card_lines(body, context, nested);
        }
        let rendered = styled_diff_lines(body, context.styles, true);
        let mut lines =
            wrap_styled_lines(rendered.lines, usize::from(context.content_width), &prefix);
        if rendered.omitted_lines > 0 {
            let count = rendered.visible_lines.to_string();
            let bytes = rendered.total_bytes.to_string();
            let hint = tr_format(
                context.locale,
                MessageId::DiffReviewContinues,
                &[("count", count.as_str()), ("bytes", bytes.as_str())],
            );
            lines.push(tool_hint_line(hint, context, nested));
        }
        return lines;
    }
    let body_lines = body.split('\n').collect::<Vec<_>>();
    let profile = context.density.profile();
    let visibility = visible_output_lines_with_limits(
        body_lines.len(),
        expanded,
        profile.collapsed_output_lines,
        usize::MAX,
    );
    let mut lines = wrap_styled_lines(
        body_lines
            .into_iter()
            .take(visibility.visible)
            .map(|line| styled_detail_line(line, body_kind, context.styles)),
        usize::from(context.content_width),
        &prefix,
    );
    if visibility.omitted > 0 {
        lines.push(tool_omission_line(
            visibility.omitted,
            MessageId::ToolLineOmitted,
            MessageId::ToolLinesOmitted,
            context,
            nested,
        ));
    }
    lines
}

fn styled_detail_line(
    line: &str,
    body_kind: BlockBodyKind,
    styles: TranscriptStyles,
) -> Line<'static> {
    let style = match body_kind {
        BlockBodyKind::Diff if line.starts_with('+') && !line.starts_with("+++") => styles.added,
        BlockBodyKind::Diff if line.starts_with('-') && !line.starts_with("---") => styles.removed,
        BlockBodyKind::Diff
            if line.starts_with("@@") || line.starts_with("+++") || line.starts_with("---") =>
        {
            styles.metadata
        }
        BlockBodyKind::CodeIntelligence
            if line.contains("precision:")
                || line.contains("confidence=")
                || line.contains("degraded") =>
        {
            styles.metadata
        }
        BlockBodyKind::CodeIntelligence if looks_like_code_location(line) => styles.label,
        _ => styles.text,
    };
    Line::from(Span::styled(line.to_owned(), style))
}

struct StyledDiffWindow {
    lines: Vec<Line<'static>>,
    visible_lines: usize,
    omitted_lines: usize,
    total_bytes: usize,
}

/// Collapsed edit card: path + stats, create-only preview without green `+` noise.
fn collapsed_edit_card_lines(
    body: &str,
    context: ToolLineContext,
    nested: bool,
) -> Vec<Line<'static>> {
    use crate::diff_render::{
        create_file_preview_lines, is_create_only_diff, primary_new_path,
        unified_diff_add_delete_counts,
    };

    let gutter = if nested {
        format!("{}  ", context.styles.glyphs.tool_gutter())
    } else {
        context.styles.glyphs.tool_gutter().to_owned()
    };
    let (adds, deletes) = unified_diff_add_delete_counts(body);
    let path = primary_new_path(body).unwrap_or_default();
    let mut lines = Vec::new();

    if !path.is_empty() {
        lines.push(Line::from(vec![
            Span::styled(gutter.clone(), context.styles.metadata),
            Span::styled(path, context.styles.text),
        ]));
    }

    let mut stats = String::new();
    if adds > 0 {
        stats.push_str(&format!("+{adds}"));
    }
    if deletes > 0 {
        if !stats.is_empty() {
            stats.push(' ');
        }
        stats.push_str(&format!("-{deletes}"));
    }
    if is_create_only_diff(body) {
        if !stats.is_empty() {
            stats.push_str(&format!(" {} ", context.styles.glyphs.separator()));
        }
        stats.push_str("new file");
    }
    if !stats.is_empty() {
        lines.push(Line::from(vec![
            Span::styled(gutter.clone(), context.styles.metadata),
            Span::styled(stats, context.styles.metadata),
        ]));
    }

    if is_create_only_diff(body) {
        let preview = create_file_preview_lines(
            body,
            context.density.profile().collapsed_create_preview_lines,
        );
        let total_add = adds;
        for (index, line) in preview.iter().enumerate() {
            let number = format!("{:>4} ", index + 1);
            lines.push(Line::from(vec![
                Span::styled(gutter.clone(), context.styles.metadata),
                Span::styled(number, context.styles.metadata),
                Span::styled(line.clone(), context.styles.text),
            ]));
        }
        let remaining = total_add.saturating_sub(preview.len());
        if remaining > 0 {
            lines.push(tool_omission_line(
                remaining,
                MessageId::ToolLineOmitted,
                MessageId::ToolLinesOmitted,
                context,
                nested,
            ));
        } else if !preview.is_empty() {
            // Expand still available via disclosure even when fully previewed.
        }
    } else {
        let omitted = body.lines().count().max(1);
        lines.push(tool_omission_line(
            omitted,
            MessageId::ToolLineOmitted,
            MessageId::ToolLinesOmitted,
            context,
            nested,
        ));
    }
    lines
}

/// Number and emphasize only the current bounded review window.
fn styled_diff_lines(body: &str, styles: TranscriptStyles, expanded: bool) -> StyledDiffWindow {
    use crate::diff_render::{
        DiffLineKind, is_create_only_diff, render_unified_diff_progressive,
        render_unified_diff_window,
    };
    use ratatui::style::Modifier;

    let create_only = is_create_only_diff(body);
    let rendered = if expanded {
        render_unified_diff_progressive(body)
    } else {
        render_unified_diff_window(body, 0)
    };
    let visible_lines = rendered.lines.len();
    let lines = rendered
        .lines
        .into_iter()
        .map(|line| {
            // Create-only expanded views read as file content, not green + noise.
            let base = match line.kind {
                DiffLineKind::Add if create_only => styles.text,
                DiffLineKind::Add => styles.added,
                DiffLineKind::Remove => styles.removed,
                DiffLineKind::Hunk | DiffLineKind::Meta => styles.metadata,
                DiffLineKind::Context | DiffLineKind::Header => styles.text,
            };
            let mut spans = Vec::new();
            if !matches!(line.kind, DiffLineKind::Meta | DiffLineKind::Hunk) {
                if create_only && matches!(line.kind, DiffLineKind::Add | DiffLineKind::Context) {
                    let new = line
                        .new_no
                        .map(|n| format!("{n:>4} "))
                        .unwrap_or_else(|| "     ".into());
                    spans.push(Span::styled(new, styles.metadata));
                } else {
                    let old = line
                        .old_no
                        .map(|n| format!("{n:>4}"))
                        .unwrap_or_else(|| "    ".into());
                    let new = line
                        .new_no
                        .map(|n| format!("{n:>4}"))
                        .unwrap_or_else(|| "    ".into());
                    spans.push(Span::styled(format!("{old}|{new} "), styles.metadata));
                    spans.push(Span::styled(format!("{} ", line.marker), base));
                }
            }
            for part in line.spans {
                let style = if part.emphasize {
                    base.add_modifier(Modifier::BOLD | Modifier::UNDERLINED)
                } else {
                    base
                };
                spans.push(Span::styled(part.text, style));
            }
            Line::from(spans)
        })
        .collect();
    StyledDiffWindow {
        lines,
        visible_lines,
        omitted_lines: rendered.omitted_lines,
        total_bytes: rendered.total_bytes,
    }
}

fn patch_review_section(area: Rect) -> [Rect; 2] {
    let rows = Layout::default()
        .direction(Direction::Vertical)
        .constraints([
            Constraint::Length(layout_contract::ROW_HEIGHT.min(area.height)),
            Constraint::Min(layout_contract::ROW_HEIGHT),
        ])
        .split(area);
    [rows[0], rows[1]]
}

fn patch_review_render_selection(overlay: &crate::overlay::ChangesOverlay) -> (usize, usize) {
    let patch_index = overlay
        .confirm_rollback
        .then_some(())
        .and(overlay.rollback_preview.as_ref())
        .and_then(|preview| {
            overlay.projection.patches.iter().position(|patch| {
                patch.patch_id == preview.patch_id && patch.transaction_id == preview.transaction_id
            })
        })
        .unwrap_or(overlay.selected);
    let file_index = if patch_index == overlay.selected {
        overlay.selected_file
    } else {
        0
    };
    (patch_index, file_index)
}

fn selection_window_start(selected: usize, total: usize, visible: usize) -> usize {
    if visible == 0 || total <= visible {
        return 0;
    }
    selected
        .min(total.saturating_sub(1))
        .saturating_add(1)
        .saturating_sub(visible)
}

fn patch_verification_message(status: &str) -> MessageId {
    match status.trim().to_ascii_lowercase().as_str() {
        "verified" | "passed" | "success" | "ok" => MessageId::PatchVerificationPassed,
        "failed" | "error" | "mismatch" => MessageId::StatusFailed,
        "pending" | "queued" | "running" => MessageId::StatusQueued,
        "" | "-" => MessageId::NotAvailable,
        _ => MessageId::UnknownValue,
    }
}

fn styled_projected_diff_line(
    line: &crate::diff_render::DiffLine,
    styles: TranscriptStyles,
) -> Line<'static> {
    use crate::diff_render::DiffLineKind;

    let base = match line.kind {
        DiffLineKind::Add => styles.added,
        DiffLineKind::Remove => styles.removed,
        DiffLineKind::Hunk | DiffLineKind::Meta => styles.metadata,
        DiffLineKind::Context | DiffLineKind::Header => styles.text,
    };
    let mut spans = Vec::new();
    if !matches!(line.kind, DiffLineKind::Meta | DiffLineKind::Hunk) {
        let old = line
            .old_no
            .map(|number| format!("{number:>4}"))
            .unwrap_or_else(|| "    ".into());
        let new = line
            .new_no
            .map(|number| format!("{number:>4}"))
            .unwrap_or_else(|| "    ".into());
        spans.push(Span::styled(format!("{old}|{new} "), styles.metadata));
        spans.push(Span::styled(format!("{} ", line.marker), base));
    }
    for part in &line.spans {
        let style = if part.emphasize {
            base.add_modifier(Modifier::BOLD | Modifier::UNDERLINED)
        } else {
            base
        };
        spans.push(Span::styled(part.text.clone(), style));
    }
    Line::from(spans)
}

fn styled_compact_projected_diff_line(
    line: &crate::diff_render::DiffLine,
    text: Style,
    added: Style,
    removed: Style,
    hunk: Style,
) -> Line<'static> {
    use crate::diff_render::DiffLineKind;

    let base = match line.kind {
        DiffLineKind::Add => added,
        DiffLineKind::Remove => removed,
        DiffLineKind::Hunk => hunk,
        DiffLineKind::Meta | DiffLineKind::Context | DiffLineKind::Header => text,
    };
    let mut spans = Vec::new();
    if matches!(
        line.kind,
        DiffLineKind::Add | DiffLineKind::Remove | DiffLineKind::Context
    ) {
        spans.push(Span::styled(line.marker.to_string(), base));
    }
    spans.extend(line.spans.iter().map(|part| {
        let style = if part.emphasize {
            base.add_modifier(Modifier::BOLD | Modifier::UNDERLINED)
        } else {
            base
        };
        Span::styled(part.text.clone(), style)
    }));
    Line::from(spans)
}

fn pad_key_label(key: &str, width: usize) -> String {
    let mut out = format!(" {key} ");
    while unicode_width::UnicodeWidthStr::width(out.as_str()) < width {
        out.push(' ');
    }
    out
}

fn tool_omission_line(
    omitted: usize,
    singular: MessageId,
    plural: MessageId,
    context: ToolLineContext,
    nested: bool,
) -> Line<'static> {
    let id = if omitted == 1 { singular } else { plural };
    let count = omitted.to_string();
    let hint = tr_format(
        context.locale,
        id,
        &[("count", count.as_str()), ("key", context.expand_key)],
    );
    tool_hint_line(hint, context, nested)
}

fn tool_hint_line(hint: String, context: ToolLineContext, nested: bool) -> Line<'static> {
    let gutter = if nested {
        format!("{}  ", context.styles.glyphs.tool_gutter())
    } else {
        context.styles.glyphs.tool_gutter().to_owned()
    };
    let gutter_width = UnicodeWidthStr::width(gutter.as_str());
    let width = usize::from(context.content_width);
    if gutter_width >= width {
        return Line::from(Span::styled(
            truncate_cells(&hint, width),
            context.styles.metadata,
        ));
    }
    Line::from(vec![
        Span::styled(gutter, context.styles.metadata),
        Span::styled(
            truncate_cells(&hint, width.saturating_sub(gutter_width)),
            context.styles.metadata,
        ),
    ])
}

fn transcript_role_prefix(
    marker: &'static str,
    label: &'static str,
    label_style: Style,
    continuation_style: Style,
) -> StyledPrefix {
    let label_width = UnicodeWidthStr::width(label);
    let padded_label = format!(
        "{label}{}{}",
        " ".repeat(layout_contract::TRANSCRIPT_ROLE_LABEL_WIDTH.saturating_sub(label_width)),
        " ".repeat(layout_contract::TRANSCRIPT_ROLE_GAP_WIDTH)
    );
    let prefix = StyledPrefix::hanging(
        vec![
            Span::styled(marker, label_style),
            Span::styled(padded_label, label_style),
        ],
        continuation_style,
    );
    debug_assert_eq!(
        prefix.width(),
        layout_contract::TRANSCRIPT_ROLE_PREFIX_WIDTH
    );
    prefix
}

#[cfg(test)]
fn cached_transcript_lines_with_tool_key(
    cache: &mut TranscriptRenderCache,
    block: &TranscriptBlock,
    locale: Locale,
    styles: TranscriptStyles,
    content_width: u16,
    tool_expand_key: &'static str,
) -> Vec<Line<'static>> {
    cached_transcript_lines_with_tool_key_and_density(
        cache,
        block,
        locale,
        styles,
        content_width,
        tool_expand_key,
        DensityMode::Compact,
    )
}

fn cached_transcript_lines_with_tool_key_and_density(
    cache: &mut TranscriptRenderCache,
    block: &TranscriptBlock,
    locale: Locale,
    styles: TranscriptStyles,
    content_width: u16,
    tool_expand_key: &'static str,
    density: DensityMode,
) -> Vec<Line<'static>> {
    if let Some(entry) = cache.get(&block.id)
        && entry.revision == block.layout_revision
        && entry.width == content_width
        && entry.locale == locale
        && entry.density == density
        && entry.expand_key == tool_expand_key
    {
        return entry.lines.clone();
    }
    let lines = transcript_lines_with_tool_key_and_density(
        block,
        locale,
        styles,
        content_width,
        tool_expand_key,
        density,
    );
    cache.insert(
        block.id.clone(),
        TranscriptRenderCacheEntry {
            revision: block.layout_revision,
            width: content_width,
            locale,
            density,
            expand_key: tool_expand_key,
            lines: lines.clone(),
        },
    );
    lines
}

#[cfg(test)]
fn cached_transcript_lines(
    cache: &mut TranscriptRenderCache,
    block: &TranscriptBlock,
    locale: Locale,
    styles: TranscriptStyles,
    content_width: u16,
) -> Vec<Line<'static>> {
    cached_transcript_lines_with_tool_key(
        cache,
        block,
        locale,
        styles,
        content_width,
        crate::keybinding::KeyBindings::default()
            .expand_tools
            .label(),
    )
}

fn looks_like_code_location(line: &str) -> bool {
    line.trim_start().split(':').any(|part| {
        part.split('-')
            .next()
            .is_some_and(|value| !value.is_empty() && value.chars().all(|ch| ch.is_ascii_digit()))
    })
}

fn transcript_label(locale: Locale, kind: BlockKind) -> &'static str {
    match kind {
        BlockKind::User => tr(locale, MessageId::TranscriptYou),
        BlockKind::Assistant => tr(locale, MessageId::TranscriptCarina),
        BlockKind::Thinking => tr(locale, MessageId::TranscriptThinking),
        BlockKind::Tool => "",
        BlockKind::Governance => tr(locale, MessageId::TranscriptAction),
        BlockKind::Status => tr(locale, MessageId::TranscriptStatus),
        BlockKind::Diagnostic => tr(locale, MessageId::TranscriptDiagnostic),
    }
}

fn transcript_gap_for_density(
    previous: &TranscriptBlock,
    next: &TranscriptBlock,
    density: DensityMode,
) -> usize {
    let related = matches!(
        (previous.kind, next.kind),
        (BlockKind::Tool, BlockKind::Tool)
            | (BlockKind::Tool, BlockKind::Thinking)
            | (BlockKind::Thinking, BlockKind::Tool)
    );
    if related {
        density.profile().related_gap
    } else {
        density.profile().block_gap
    }
}

#[cfg(test)]
fn transcript_gap(previous: &TranscriptBlock, next: &TranscriptBlock) -> usize {
    transcript_gap_for_density(previous, next, DensityMode::Compact)
}

fn transcript_click_action(
    block: &TranscriptBlock,
    index: usize,
    history_active: bool,
) -> Option<Action> {
    if history_active {
        return (block.kind == BlockKind::User && block.branchable)
            .then_some(Action::SelectHistory(index));
    }
    if let Some(failure) = &block.failure {
        return match failure.action {
            FailureAction::Retry | FailureAction::RunAgain => {
                Some(Action::RetryExecution(failure.run_id.clone()))
            }
            FailureAction::Recovering | FailureAction::Disabled => Some(Action::CopyFailureId(
                if failure.source_event_id.is_empty() {
                    failure.run_id.clone()
                } else {
                    failure.source_event_id.clone()
                },
            )),
        };
    }
    block
        .is_collapsible()
        .then(|| Action::ToggleBlock(block.id.clone()))
}

fn visible_transcript_rect(
    area: Rect,
    block_width: u16,
    viewport_start: usize,
    viewport_end: usize,
    range_start: usize,
    range_end: usize,
) -> Option<Rect> {
    let visible_top = range_start.max(viewport_start);
    let visible_bottom = range_end.min(viewport_end);
    (visible_top < visible_bottom).then(|| {
        Rect::new(
            area.x.saturating_add(layout_contract::ROW_HEIGHT),
            area.y.saturating_add(
                visible_top
                    .saturating_sub(viewport_start)
                    .min(u16::MAX as usize) as u16,
            ),
            block_width,
            visible_bottom
                .saturating_sub(visible_top)
                .min(u16::MAX as usize) as u16,
        )
    })
}

fn centered(area: Rect, max_width: u16, max_height: u16) -> Rect {
    let width = area
        .width
        .min(max_width)
        .max(layout_contract::MIN_DIMENSION);
    let height = area
        .height
        .min(max_height)
        .max(layout_contract::MIN_DIMENSION);
    Rect::new(
        area.x + area.width.saturating_sub(width) / 2,
        area.y + area.height.saturating_sub(height) / 2,
        width,
        height,
    )
}

fn validation_spinner(elapsed: Option<Duration>) -> &'static str {
    crate::render_contract::status_spinner(elapsed.unwrap_or_default(), false)
}

fn bottom_sheet(area: Rect, max_width: u16, max_height: u16) -> Rect {
    let width = area
        .width
        .saturating_sub(layout_contract::BLOCK_PADDING)
        .min(max_width)
        .max(layout_contract::MIN_DIMENSION);
    let height = area
        .height
        .saturating_sub(layout_contract::COMPACT_SECTION_HEIGHT)
        .min(max_height)
        .max(layout_contract::MIN_DIMENSION);
    Rect::new(
        area.x + area.width.saturating_sub(width) / 2,
        area.bottom()
            .saturating_sub(height)
            .saturating_sub(layout_contract::ROW_HEIGHT),
        width,
        height,
    )
}

fn plan_review_popup_height(visual_rows: usize) -> u16 {
    const MAX_HEIGHT: u16 = 28;
    const PLAN_FRAME_HEIGHT: u16 = 2;

    let fixed_chrome = layout_contract::BLOCK_PADDING
        + layout_contract::COMPACT_SECTION_HEIGHT
        + layout_contract::ROW_HEIGHT
        + layout_contract::CONTROL_HEIGHT
        + layout_contract::ROW_HEIGHT
        + layout_contract::ROW_HEIGHT
        + PLAN_FRAME_HEIGHT;
    let minimum_body = layout_contract::QUESTION_BODY_MIN_HEIGHT
        .saturating_sub(PLAN_FRAME_HEIGHT)
        .max(layout_contract::MIN_DIMENSION);
    let maximum_body = MAX_HEIGHT.saturating_sub(fixed_chrome);
    let body = u16::try_from(visual_rows)
        .unwrap_or(u16::MAX)
        .clamp(minimum_body, maximum_body);
    fixed_chrome + body
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

fn source_app_label(app: &str) -> &str {
    match app {
        "claude" => "Claude Code",
        "codex" => "Codex",
        "gemini" => "Gemini",
        other => other,
    }
}

fn human_bytes(bytes: usize) -> String {
    crate::render_contract::format_bytes(bytes as u64)
}

fn media_preview_rect(
    bounds: Rect,
    cursor: Position,
    preferred_width: u16,
    preferred_height: u16,
) -> Option<Rect> {
    if bounds.width < layout_contract::MEDIA_PREVIEW_BOUNDS_MIN_WIDTH
        || bounds.height < layout_contract::MEDIA_PREVIEW_BOUNDS_MIN_HEIGHT
    {
        return None;
    }
    // The preferred dimensions already include the border and metadata row.
    // Growing either axis independently here would distort the bitmap placed
    // into the inner rectangle, especially for portrait images.
    let width = preferred_width
        .min(bounds.width)
        .max(layout_contract::MEDIA_PREVIEW_MIN_WIDTH);
    let height = preferred_height
        .min(bounds.height)
        .max(layout_contract::MEDIA_PREVIEW_MIN_HEIGHT);
    let max_x = bounds.right().saturating_sub(width);
    let x = cursor.x.saturating_sub(width / 3).clamp(bounds.x, max_x);
    let y = bounds.bottom().saturating_sub(height);
    Some(Rect::new(x, y, width, height))
}

fn media_preview_size(
    path: &std::path::Path,
    max_width: u16,
    max_height: u16,
) -> Option<(u16, u16)> {
    let (image_width, image_height) = image::image_dimensions(path).ok()?;
    let terminal = crossterm::terminal::window_size().ok().filter(|terminal| {
        terminal.columns > 0 && terminal.rows > 0 && terminal.width > 0 && terminal.height > 0
    });
    // Pixel geometry is not exposed by every PTY/terminal. A 1:2 cell is a
    // conservative fallback and still preserves the source aspect ratio;
    // falling back to a fixed 42x12 rectangle visibly stretches most images.
    let (terminal_width_px, terminal_height_px, terminal_columns, terminal_rows) = terminal
        .map(|terminal| {
            (
                terminal.width,
                terminal.height,
                terminal.columns,
                terminal.rows,
            )
        })
        .unwrap_or((1, 2, 1, 1));
    Some(fit_media_preview_size(
        max_width,
        max_height,
        image_width,
        image_height,
        terminal_width_px,
        terminal_height_px,
        terminal_columns,
        terminal_rows,
    ))
}

#[allow(clippy::too_many_arguments)]
fn fit_media_preview_size(
    max_width: u16,
    max_height: u16,
    image_width: u32,
    image_height: u32,
    terminal_width_px: u16,
    terminal_height_px: u16,
    terminal_columns: u16,
    terminal_rows: u16,
) -> (u16, u16) {
    let max_image_width = max_width
        .saturating_sub(layout_contract::BLOCK_PADDING)
        .max(layout_contract::MIN_DIMENSION);
    let max_image_height = max_height
        .saturating_sub(layout_contract::MEDIA_METADATA_HEIGHT)
        .max(layout_contract::MIN_DIMENSION);
    let cell_width = f64::from(terminal_width_px)
        / f64::from(terminal_columns.max(layout_contract::MIN_DIMENSION));
    let cell_height = f64::from(terminal_height_px)
        / f64::from(terminal_rows.max(layout_contract::MIN_DIMENSION));
    let target_cell_ratio = f64::from(image_width)
        / f64::from(image_height.max(u32::from(layout_contract::MIN_DIMENSION)))
        * cell_height
        / cell_width;
    let mut image_height_cells = max_image_height;
    let mut image_width_cells = (f64::from(image_height_cells) * target_cell_ratio).round() as u16;
    if image_width_cells > max_image_width {
        image_width_cells = max_image_width;
        image_height_cells = (f64::from(image_width_cells) / target_cell_ratio).round() as u16;
    }
    (
        image_width_cells
            .max(layout_contract::MEDIA_GRAPHICS_MIN_WIDTH)
            .saturating_add(layout_contract::BLOCK_PADDING)
            .min(max_width),
        image_height_cells
            .max(layout_contract::MEDIA_GRAPHICS_MIN_HEIGHT)
            .saturating_add(layout_contract::MEDIA_METADATA_HEIGHT)
            .min(max_height),
    )
}

#[cfg(test)]
mod transcript_tests {
    use super::*;
    use crate::overlay::PlanReviewOverlay;
    use crossterm::event::{Event, KeyCode, KeyEvent, KeyModifiers, MouseEvent, MouseEventKind};
    use ratatui::style::Color;

    fn production_render_app() -> (App, std::path::PathBuf, std::thread::JoinHandle<()>) {
        use std::io::{BufRead, BufReader, Write};
        use std::sync::atomic::{AtomicU64, Ordering};

        static NEXT_ID: AtomicU64 = AtomicU64::new(1);
        let root = std::path::PathBuf::from(format!(
            "/tmp/cpr-{}-{}",
            std::process::id(),
            NEXT_ID.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&root).unwrap();
        let socket = root.join("daemon.sock");
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            for (method, result) in [
                (
                    "runtime.initialize",
                    serde_json::json!({
                        "runtime_version": "test",
                        "protocol_version": "1.3.0",
                        "projection_version": "1.0.0",
                        "capabilities": {"rpc_methods": [
                            "execution.start", "execution.retry", "model.list", "session.create",
                            "session.events.stream", "session.list"
                        ]}
                    }),
                ),
                (
                    "model.list",
                    serde_json::json!({
                        "default_model": "test/model",
                        "reasoner": {"available": true},
                        "providers": [{
                            "id": "test", "registered": true, "available": true,
                            "models": [{"id": "test/model", "available": true}]
                        }]
                    }),
                ),
                ("session.list", serde_json::json!([])),
            ] {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let request: serde_json::Value = serde_json::from_str(&line).unwrap();
                assert_eq!(request["method"], method);
                writeln!(
                    stream,
                    "{}",
                    serde_json::json!({
                        "jsonrpc": "2.0",
                        "id": request["id"],
                        "result": result
                    })
                )
                .unwrap();
                stream.flush().unwrap();
            }
        });
        let mut app = App::bootstrap(super::super::Options {
            socket,
            workspace: root.clone(),
            session_id: None,
            locale: None,
            locale_path: None,
            density: DensityMode::Compact,
            density_path: None,
            carina_bin: None,
            no_alt_screen: true,
            screen_mode: None,
            screen_handoff: None,
            alt_screen: super::super::AltScreenPolicy::Never,
            scrollback_wrap: crate::native_scrollback::ScrollbackWrap::PreWrap,
        })
        .unwrap();
        app.phase = Phase::Conversation;
        (app, root, server)
    }

    fn patch_review_fixture(
        width: u16,
        height: u16,
        locale: Locale,
        screen_mode: ScreenMode,
    ) -> (String, Vec<(Position, Action)>) {
        patch_review_fixture_variant(
            width,
            height,
            locale,
            screen_mode,
            crate::glyphs::GlyphMode::Unicode,
            crate::theme::ColorLevel::TrueColor,
            false,
        )
    }

    fn patch_review_fixture_variant(
        width: u16,
        height: u16,
        locale: Locale,
        screen_mode: ScreenMode,
        glyph_mode: crate::glyphs::GlyphMode,
        color_level: crate::theme::ColorLevel,
        confirm_rollback: bool,
    ) -> (String, Vec<(Position, Action)>) {
        use crate::overlay::{ChangesFocus, ChangesOverlay, RetainedLoad};
        use crate::product_projection::ProductProjection;
        use crate::rpc::{
            PatchHunkAttribution, PatchRollbackFile, PatchRollbackPreview, PatchVerifyResult,
            WorkspacePatch,
        };

        let (mut app, root, server) = production_render_app();
        app.options.locale = Some(locale.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|candidate| *candidate == locale)
            .unwrap_or_default();
        app.options.screen_mode = Some(screen_mode);
        app.theme = crate::theme::Theme::new(crate::theme::Polarity::Dark, color_level);
        app.theme.glyphs = Glyphs::new(glyph_mode);
        let primary = WorkspacePatch {
            patch_id: "patch-authz-boundary-0184".into(),
            transaction_id: "tx-workspace-0184".into(),
            session_id: "session-7".into(),
            actor: "carina/patch-agent".into(),
            status: "verified".into(),
            affected_files: vec![
                "crates/carina-kernel/src/runtime/patch.rs".into(),
                "crates/carina-tui/src/app/review.rs".into(),
                "docs/product/patch-review.md".into(),
            ],
            diff: concat!(
                "diff --git a/crates/carina-kernel/src/runtime/patch.rs b/crates/carina-kernel/src/runtime/patch.rs\n",
                "--- a/crates/carina-kernel/src/runtime/patch.rs\n",
                "+++ b/crates/carina-kernel/src/runtime/patch.rs\n",
                "@@ -41,5 +41,7 @@ pub fn authorize_patch(request: Request) -> Decision {\n",
                "-    policy.check(request.capability)\n",
                "+    policy.check(request.capability, request.workspace)\n",
                "+        .with_transaction(request.transaction_id)\n",
                " }\n",
                "diff --git a/crates/carina-tui/src/app/review.rs b/crates/carina-tui/src/app/review.rs\n",
                "--- /dev/null\n",
                "+++ b/crates/carina-tui/src/app/review.rs\n",
                "@@ -0,0 +1,4 @@\n",
                "+pub struct ReviewIdentity {\n",
                "+    pub patch_id: String,\n",
                "+    pub transaction_id: String,\n",
                "+}\n",
                "diff --git a/docs/product/patch-review.md b/docs/product/patch-review.md\n",
                "--- a/docs/product/patch-review.md\n",
                "+++ b/docs/product/patch-review.md\n",
                "@@ -8,3 +8,4 @@ Rollback contract\n",
                "-Confirmation follows the selected patch.\n",
                "+Confirmation retains the previewed patch identity.\n",
                "+The workspace hash must remain unchanged.\n"
            )
            .into(),
            approval_status: "approved".into(),
            approval_id: "approval-44".into(),
            test_status: "passed".into(),
            verify_result: Some(PatchVerifyResult {
                status: "passed".into(),
                expected_hash: "f9a7".into(),
                actual_hash: "f9a7".into(),
            }),
            hunk_attributions: vec![
                PatchHunkAttribution {
                    file: "crates/carina-kernel/src/runtime/patch.rs".into(),
                    hunk_index: 0,
                    actor: "carina/patch-agent".into(),
                    transaction_id: "tx-workspace-0184".into(),
                    approval_id: "approval-44".into(),
                    verify_result: "passed".into(),
                    ..PatchHunkAttribution::default()
                },
                PatchHunkAttribution {
                    file: "crates/carina-tui/src/app/review.rs".into(),
                    hunk_index: 0,
                    actor: "carina/patch-agent".into(),
                    transaction_id: "tx-workspace-0184".into(),
                    approval_id: "approval-44".into(),
                    verify_result: "passed".into(),
                    ..PatchHunkAttribution::default()
                },
            ],
            rollback_pointer: "rollback-0184".into(),
            ..WorkspacePatch::default()
        };
        let pending = WorkspacePatch {
            patch_id: "patch-docs-0185".into(),
            transaction_id: "tx-workspace-0185".into(),
            session_id: "session-7".into(),
            actor: "carina/docs-agent".into(),
            status: "proposed".into(),
            affected_files: vec!["docs/product/runtime.md".into()],
            diff: concat!(
                "diff --git a/docs/product/runtime.md b/docs/product/runtime.md\n",
                "--- a/docs/product/runtime.md\n",
                "+++ b/docs/product/runtime.md\n",
                "@@ -1 +1,2 @@\n",
                " Runtime contract\n",
                "+Review remains bounded and auditable.\n"
            )
            .into(),
            ..WorkspacePatch::default()
        };
        let patches = vec![primary, pending];
        let patch_reviews = crate::patch_review::project_patch_reviews(&patches);
        app.overlays
            .replace(Overlay::Changes(Box::new(ChangesOverlay {
                projection: ProductProjection {
                    patches,
                    ..ProductProjection::default()
                },
                patch_reviews,
                // Confirmation deliberately simulates later selection movement.
                selected: usize::from(confirm_rollback),
                selected_file: 0,
                focus: ChangesFocus::Files,
                scroll: 0,
                load: RetainedLoad::default(),
                confirm_rollback,
                rollback_preview: confirm_rollback.then(|| PatchRollbackPreview {
                    patch_id: "patch-authz-boundary-0184".into(),
                    transaction_id: "tx-workspace-0184".into(),
                    can_rollback: true,
                    workspace_unchanged: true,
                    files: vec![
                        PatchRollbackFile {
                            path: "crates/carina-kernel/src/runtime/patch.rs".into(),
                            action: "restore".into(),
                        },
                        PatchRollbackFile {
                            path: "crates/carina-tui/src/app/review.rs".into(),
                            action: "delete".into(),
                        },
                        PatchRollbackFile {
                            path: "docs/product/patch-review.md".into(),
                            action: "restore".into(),
                        },
                    ],
                    file_count: 3,
                    ..PatchRollbackPreview::default()
                }),
                rollback_error: String::new(),
            })));
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, height)).unwrap();
        terminal
            .draw(|frame| {
                let overlay = app.overlays.active().cloned().unwrap();
                app.interactions.begin_frame();
                app.interactions.begin_overlay();
                app.render_overlay(frame, frame.area(), &overlay);
            })
            .unwrap();
        let mut actions = Vec::new();
        for y in 0..height {
            for x in 0..width {
                let position = Position::new(x, y);
                if let Some(action) = app.interactions.action_at(position) {
                    actions.push((position, action));
                }
            }
        }
        let rendered = rendered_frame_contract(terminal.backend().buffer());
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
        (rendered, actions)
    }

    #[test]
    fn fullscreen_patch_review_goldens_cover_en_and_zh_hans_at_product_widths() {
        for (locale_name, locale) in [("en", Locale::En), ("zh_hans", Locale::ZhHans)] {
            for (width, height) in [(120, 34), (160, 40)] {
                let (rendered, actions) =
                    patch_review_fixture(width, height, locale, ScreenMode::Fullscreen);
                assert!(
                    actions
                        .iter()
                        .any(|(_, action)| *action == Action::SelectChange(0))
                );
                assert!(
                    actions
                        .iter()
                        .any(|(_, action)| *action == Action::SelectPatchReviewFile(0))
                );
                insta::assert_snapshot!(
                    format!("fullscreen_patch_review_{locale_name}_{width}"),
                    rendered
                );
            }
        }
    }

    #[test]
    fn fullscreen_patch_review_rollback_preview_has_a_stable_confirmation_state() {
        let (rendered, actions) = patch_review_fixture_variant(
            120,
            34,
            Locale::En,
            ScreenMode::Fullscreen,
            crate::glyphs::GlyphMode::Unicode,
            crate::theme::ColorLevel::TrueColor,
            true,
        );
        assert!(rendered.contains("Rollback affects 3 files."));
        assert!(rendered.contains("restore  crates/carina-kernel/src/runtime/patch.rs"));
        let summary = rendered
            .lines()
            .find(|line| line.contains("Accepted") && line.contains("3 files"))
            .expect("previewed transaction summary");
        assert!(summary.contains("patch-authz-boundary-0184"));
        assert!(actions.iter().all(|(_, action)| {
            !matches!(
                action,
                Action::SelectChange(_) | Action::SelectPatchReviewFile(_)
            )
        }));
        insta::assert_snapshot!("fullscreen_patch_review_rollback_en_120", rendered);
    }

    #[test]
    fn patch_review_list_window_keeps_the_selected_identity_visible() {
        assert_eq!(selection_window_start(0, 10, 3), 0);
        assert_eq!(selection_window_start(2, 10, 3), 0);
        assert_eq!(selection_window_start(3, 10, 3), 1);
        assert_eq!(selection_window_start(9, 10, 3), 7);
        assert_eq!(selection_window_start(99, 10, 3), 7);
        assert_eq!(selection_window_start(4, 10, 0), 0);
    }

    #[test]
    fn fullscreen_patch_review_ascii_no_color_preserves_pointer_geometry() {
        for locale in [Locale::En, Locale::ZhHans] {
            let (_, unicode_actions) = patch_review_fixture_variant(
                120,
                34,
                locale,
                ScreenMode::Fullscreen,
                crate::glyphs::GlyphMode::Unicode,
                crate::theme::ColorLevel::TrueColor,
                false,
            );
            let (ascii, ascii_actions) = patch_review_fixture_variant(
                120,
                34,
                locale,
                ScreenMode::Fullscreen,
                crate::glyphs::GlyphMode::Ascii,
                crate::theme::ColorLevel::None,
                false,
            );
            for action in [Action::SelectChange(0), Action::SelectPatchReviewFile(0)] {
                let positions = |actions: &[(Position, Action)]| {
                    actions
                        .iter()
                        .filter_map(|(position, candidate)| {
                            (*candidate == action).then_some(*position)
                        })
                        .collect::<Vec<_>>()
                };
                assert_eq!(positions(&unicode_actions), positions(&ascii_actions));
            }
            assert!(!ascii.contains("Rgb("));
            assert!(!ascii.contains("Indexed("));
        }
    }

    #[test]
    fn fullscreen_patch_review_resize_fallbacks_remain_contained_and_interactive() {
        for (width, height) in [(120, 26), (80, 24), (48, 16)] {
            let (rendered, actions) =
                patch_review_fixture(width, height, Locale::En, ScreenMode::Fullscreen);
            assert!(rendered.starts_with(&format!("size={width}x{height}\n")));
            assert!(rendered.contains("Changes workbench"));
            assert!(rendered.contains("@@ -41"));
            assert!(rendered.contains("Esc"));
            assert!(rendered.contains("Close"));
            assert!(
                actions
                    .iter()
                    .all(|(position, _)| position.x < width && position.y < height)
            );
            assert!(
                actions
                    .iter()
                    .any(|(_, action)| *action == Action::SelectPatchReviewFile(0))
            );
        }
    }

    #[test]
    fn minimal_patch_review_keeps_the_compact_overlay_contract() {
        let (rendered, actions) = patch_review_fixture(120, 34, Locale::En, ScreenMode::Minimal);
        assert!(rendered.contains("Changes workbench"));
        assert!(!rendered.contains("Workspace files"));
        assert!(
            actions
                .iter()
                .any(|(_, action)| *action == Action::SelectChange(0))
        );
        assert!(
            !actions
                .iter()
                .any(|(_, action)| matches!(action, Action::SelectPatchReviewFile(_)))
        );
    }

    fn render_composer_chrome_fixture(
        width: u16,
        locale: Locale,
        state: &str,
        glyph_mode: crate::glyphs::GlyphMode,
        color_level: crate::theme::ColorLevel,
    ) -> (String, Option<Position>) {
        let (mut app, root, server) = production_render_app();
        app.options.locale = Some(locale.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|candidate| *candidate == locale)
            .unwrap_or_default();
        app.options.screen_mode = Some(ScreenMode::Fullscreen);
        app.theme = crate::theme::Theme::new(crate::theme::Polarity::Dark, color_level);
        app.theme.glyphs = Glyphs::new(glyph_mode);
        app.security_context = Some(crate::rpc::EffectiveConfig {
            approval_mode: "ask".into(),
            sandbox_commands: true,
            permission_profile: "safe-edit".into(),
            ..crate::rpc::EffectiveConfig::default()
        });
        app.context_summary = Some(crate::rpc::ContextSummary {
            model_context_tokens: crate::rpc::ModelContextTokens {
                available: true,
                used_percent: 42,
                limit_tokens: 100_000,
                ..crate::rpc::ModelContextTokens::default()
            },
            ..crate::rpc::ContextSummary::default()
        });
        app.execution_status = match state {
            "idle" => "ready",
            "approval" => "waiting_approval",
            _ => "running",
        }
        .into();
        app.execution_activity = match (locale, state) {
            (_, "idle" | "approval") => None,
            (Locale::ZhHans, "running") => Some("正在运行聚焦检查".into()),
            (Locale::ZhHans, "queued") => Some("正在整理后续工作".into()),
            (_, "running") => Some("Running focused checks".into()),
            (_, "queued") => Some("Preparing follow-up work".into()),
            _ => None,
        };
        if state == "queued" {
            app.queued_prompts.push_back("next task".into());
            app.queued_prompts.push_back("then summarize".into());
        }
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 2)).unwrap();
        terminal
            .draw(|frame| {
                app.interactions.begin_frame();
                app.render_composer_chrome(frame, frame.area());
            })
            .unwrap();
        let queue_position = (0..width).find_map(|x| {
            let position = Position::new(x, 1);
            (app.interactions.action_at(position) == Some(Action::OpenQueue)).then_some(position)
        });
        let rendered = rendered_frame_contract(terminal.backend().buffer());
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
        (rendered, queue_position)
    }

    #[test]
    fn composer_chrome_production_goldens_cover_idle_running_queue_and_approval() {
        for (locale_name, locale) in [("en", Locale::En), ("zh_hans", Locale::ZhHans)] {
            for width in [80, 120] {
                for state in ["idle", "running", "queued", "approval"] {
                    let (rendered, queue_position) = render_composer_chrome_fixture(
                        width,
                        locale,
                        state,
                        crate::glyphs::GlyphMode::Unicode,
                        crate::theme::ColorLevel::TrueColor,
                    );
                    assert!(rendered.lines().any(|line| !line.trim().is_empty()));
                    assert_eq!(queue_position.is_some(), state == "queued");
                    insta::assert_snapshot!(
                        format!("composer_chrome_{locale_name}_{state}_{width}"),
                        rendered
                    );
                }
            }
        }
    }

    #[test]
    fn composer_chrome_ascii_no_color_keeps_queue_hit_geometry() {
        for locale in [Locale::En, Locale::ZhHans] {
            let (unicode, unicode_queue) = render_composer_chrome_fixture(
                60,
                locale,
                "queued",
                crate::glyphs::GlyphMode::Unicode,
                crate::theme::ColorLevel::TrueColor,
            );
            let (ascii, ascii_queue) = render_composer_chrome_fixture(
                60,
                locale,
                "queued",
                crate::glyphs::GlyphMode::Ascii,
                crate::theme::ColorLevel::None,
            );
            assert_eq!(unicode_queue, ascii_queue);
            let queue_anchor = if locale == Locale::ZhHans {
                "队列 2"
            } else {
                "queue 2"
            };
            assert_eq!(
                anchor_position(&unicode, queue_anchor),
                anchor_position(&ascii, queue_anchor)
            );
            assert!(!ascii.contains("Rgb("));
            assert!(!ascii.contains("Indexed("));
        }
    }

    #[test]
    fn composer_chrome_polarity_and_screen_mode_keep_queue_action_geometry() {
        let mut expected = None;
        for polarity in [crate::theme::Polarity::Dark, crate::theme::Polarity::Light] {
            for screen_mode in [ScreenMode::Minimal, ScreenMode::Fullscreen] {
                let (mut app, root, server) = production_render_app();
                app.theme = crate::theme::Theme::new(polarity, crate::theme::ColorLevel::TrueColor);
                app.options.screen_mode = Some(screen_mode);
                app.execution_status = "running".into();
                app.queued_prompts.push_back("next task".into());
                let mut terminal =
                    ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 2)).unwrap();
                terminal
                    .draw(|frame| {
                        app.interactions.begin_frame();
                        app.render_composer_chrome(frame, frame.area());
                    })
                    .unwrap();
                let queue_cells = (0..120)
                    .filter_map(|x| {
                        let position = Position::new(x, 1);
                        (app.interactions.action_at(position) == Some(Action::OpenQueue))
                            .then_some(position)
                    })
                    .collect::<Vec<_>>();
                assert!(!queue_cells.is_empty());
                if let Some(expected) = &expected {
                    assert_eq!(&queue_cells, expected);
                } else {
                    expected = Some(queue_cells);
                }
                server.join().unwrap();
                std::fs::remove_dir_all(root).unwrap();
            }
        }
    }

    #[test]
    fn queue_action_preserves_an_unsent_pointer_draft() {
        let (mut app, root, server) = production_render_app();
        app.active_run_id = Some("run_stale".into());
        app.composer.set_text("keep this draft");
        app.apply_action(Action::OpenQueue);
        assert_eq!(app.composer.text(), "keep this draft");
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn composer_chrome_queue_hover_changes_style_not_geometry_or_action() {
        for color_level in [
            crate::theme::ColorLevel::TrueColor,
            crate::theme::ColorLevel::None,
        ] {
            let (mut app, root, server) = production_render_app();
            app.theme = crate::theme::Theme::new(crate::theme::Polarity::Dark, color_level);
            app.execution_status = "running".into();
            app.queued_prompts.push_back("next task".into());
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 2)).unwrap();
            terminal
                .draw(|frame| {
                    app.interactions.begin_frame();
                    app.render_composer_chrome(frame, frame.area());
                })
                .unwrap();
            let before = rendered_frame_contract(terminal.backend().buffer());
            let queue_position = (0..80)
                .find_map(|x| {
                    let position = Position::new(x, 1);
                    (app.interactions.action_at(position) == Some(Action::OpenQueue))
                        .then_some(position)
                })
                .expect("queued work should register a queue hit region");
            let before_anchor = anchor_position(&before, "queue 1");
            assert!(app.interactions.update_hover(queue_position));

            terminal
                .draw(|frame| {
                    app.interactions.begin_frame();
                    app.render_composer_chrome(frame, frame.area());
                })
                .unwrap();
            let after = rendered_frame_contract(terminal.backend().buffer());
            assert_eq!(anchor_position(&after, "queue 1"), before_anchor);
            assert_eq!(
                app.interactions.action_at(queue_position),
                Some(Action::OpenQueue)
            );
            assert_ne!(
                after, before,
                "hover should change style in every color mode"
            );

            server.join().unwrap();
            std::fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn execution_and_notice_changes_do_not_move_the_composer_or_chrome() {
        let (mut app, root, server) = production_render_app();
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 28)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let idle_composer = app.composer_area;
        app.execution_status = "running".into();
        app.execution_activity = Some("Running checks".into());
        terminal.draw(|frame| app.render(frame)).unwrap();
        assert_eq!(app.composer_area, idle_composer);
        app.notice = crate::i18n::Notice::localized(MessageId::RuntimeUnavailable);
        terminal.draw(|frame| app.render(frame)).unwrap();
        assert_eq!(app.composer_area, idle_composer);
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn production_transcript_renderer_bounds_ultrawide_reading_column() {
        for (width, height) in [(200, 50), (244, 71)] {
            let (mut app, root, server) = production_render_app();
            let mut answer = block(
                BlockKind::Assistant,
                "PRODUCTION-RENDER-ANCHOR deliberate reading width",
            );
            answer.assistant_phase = Some(crate::rpc::AssistantMessagePhase::FinalAnswer);
            app.blocks = vec![answer];
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, height)).unwrap();
            terminal
                .draw(|frame| app.render_transcript(frame, frame.area()))
                .unwrap();
            let buffer = terminal.backend().buffer();
            let content = crate::layout_contract::transcript_content(buffer.area);
            assert_eq!(
                content.width,
                crate::layout_contract::TRANSCRIPT_READING_MAX_WIDTH
            );
            assert!(content.x > 0);
            let row = (0..width)
                .map(|x| buffer[(x, 0)].symbol())
                .collect::<String>();
            assert!(row.contains("PRODUCTION-RENDER-ANCHOR"));
            let rail_x = content.x.saturating_sub(layout_contract::ROW_HEIGHT);
            assert!(row[..usize::from(rail_x)].trim().is_empty());
            server.join().unwrap();
            std::fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn production_tool_detail_has_one_rail_owner() {
        let (mut app, root, server) = production_render_app();
        let mut tool = block(BlockKind::Tool, "SINGLE-RAIL-DETAIL");
        tool.collapsible = true;
        tool.expanded = true;
        app.blocks = vec![tool];
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 10)).unwrap();
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let buffer = terminal.backend().buffer();
        let detail_row = (0..buffer.area.width)
            .map(|x| buffer[(x, 1)].symbol())
            .collect::<String>();
        assert!(detail_row.contains("SINGLE-RAIL-DETAIL"));
        assert_eq!(
            detail_row.matches(app.theme.glyphs.tool_gutter()).count(),
            1
        );
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn prompt_history_visible_copy_is_owned_by_i18n() {
        let sources = [include_str!("render.rs"), include_str!("mod.rs")].join("\n");
        for (prefix, suffix) in [
            ("Prompt ", "history"),
            ("Up/Down browse  ", "Enter edit  Esc restore draft"),
            ("Workspace history ", "unavailable"),
            ("No matching ", "prompts"),
            ("No prompt history ", "in this workspace yet"),
        ] {
            let raw_copy = format!("{prefix}{suffix}");
            assert!(
                !sources.contains(&raw_copy),
                "Prompt History copy must use MessageId instead of {raw_copy:?}"
            );
        }
    }

    #[test]
    fn renderer_layout_does_not_measure_visible_strings_by_bytes() {
        let renderer = include_str!("render.rs")
            .split("#[cfg(test)]")
            .next()
            .expect("render module has production source before its tests");
        for forbidden in [
            "label.len()",
            "prefix.len()",
            "language.len()",
            "to_string().len()",
        ] {
            assert!(
                !renderer.contains(forbidden),
                "terminal layout must use display width, not {forbidden}"
            );
        }
    }

    #[test]
    fn media_preview_stays_above_the_composer_cursor() {
        let bounds = Rect::new(0, 5, 80, 20);
        let cursor = Position::new(70, 27);
        let preview = media_preview_rect(bounds, cursor, 42, 12).unwrap();
        assert!(bounds.contains(Position::new(preview.x, preview.y)));
        assert!(preview.right() <= bounds.right());
        assert!(preview.bottom() <= bounds.bottom());
        assert!(!preview.contains(cursor));
    }

    #[test]
    fn media_preview_preserves_square_pixels_in_tall_terminal_cells() {
        let (width, height) = fit_media_preview_size(42, 12, 1254, 1254, 1000, 800, 100, 40);
        assert_eq!((width, height), (20, 12));
        let image_width_px = f64::from(width - 2) * 10.0;
        let image_height_px = f64::from(height - 3) * 20.0;
        assert_eq!(image_width_px, image_height_px);
    }

    #[test]
    fn media_preview_preserves_portrait_ratio_without_minimum_width_stretch() {
        let size = fit_media_preview_size(42, 12, 500, 2_000, 1, 2, 1, 1);
        assert_eq!(size, (7, 12));

        let preview =
            media_preview_rect(Rect::new(0, 0, 80, 30), Position::new(40, 29), 7, 12).unwrap();
        assert_eq!((preview.width, preview.height), (7, 12));
    }

    #[test]
    fn media_preview_fallback_geometry_keeps_landscape_images_landscape() {
        let (width, height) = fit_media_preview_size(42, 12, 2_000, 1_000, 1, 2, 1, 1);
        assert_eq!((width, height), (38, 12));
        assert!(width > height);
    }

    fn block(kind: BlockKind, body: &str) -> TranscriptBlock {
        TranscriptBlock {
            id: "block".into(),
            run_id: "task".into(),
            kind,
            assistant_phase: (kind == BlockKind::Assistant)
                .then_some(crate::rpc::AssistantMessagePhase::Commentary),
            user_kind: UserBlockKind::Prompt,
            tool_kind: None,
            tool_members: Vec::new(),
            failure: None,
            title: "Result".into(),
            body: body.into(),
            body_kind: BlockBodyKind::Plain,
            additions: 0,
            deletions: 0,
            status: "complete".into(),
            collapsible: matches!(
                kind,
                BlockKind::Thinking | BlockKind::Tool | BlockKind::Diagnostic
            ),
            expanded: true,
            selected: false,
            source_prompt: body.into(),
            branchable: kind == BlockKind::User,
            layout_revision: 1,
        }
    }

    fn tool_member(
        id: &str,
        title: &str,
        status: &str,
        lifecycle: &str,
        body: &str,
    ) -> ToolGroupMember {
        ToolGroupMember {
            id: format!("tool:{id}"),
            tool_name: "read".into(),
            title: title.into(),
            body: body.into(),
            body_kind: BlockBodyKind::Plain,
            additions: 0,
            deletions: 0,
            status: status.into(),
            lifecycle: lifecycle.into(),
        }
    }

    fn read_group(members: Vec<ToolGroupMember>, expanded: bool) -> TranscriptBlock {
        let mut group = block(BlockKind::Tool, "");
        group.id = members
            .first()
            .map(|member| member.id.clone())
            .unwrap_or_else(|| "tool:empty".into());
        group.tool_kind = Some(crate::tool_projection::ToolKind::Read);
        group.tool_members = members;
        group.expanded = expanded;
        group
    }

    fn plain(lines: &[Line<'_>]) -> Vec<String> {
        lines
            .iter()
            .map(|line| {
                line.spans
                    .iter()
                    .map(|span| span.content.as_ref())
                    .collect()
            })
            .collect()
    }

    fn rendered_frame_text(buffer: &ratatui::buffer::Buffer) -> String {
        let area = buffer.area;
        let mut rows = Vec::with_capacity(area.height as usize);
        for y in area.y..area.bottom() {
            let mut row = String::new();
            let mut x = area.x;
            while x < area.right() {
                let symbol = buffer[(x, y)].symbol();
                row.push_str(symbol);
                x = x.saturating_add(UnicodeWidthStr::width(symbol).max(1) as u16);
            }
            rows.push(row.trim_end().to_owned());
        }
        while rows.last().is_some_and(String::is_empty) {
            rows.pop();
        }
        format!("size={}x{}\n{}\n", area.width, area.height, rows.join("\n"))
    }

    fn rendered_frame_contract(buffer: &ratatui::buffer::Buffer) -> String {
        let mut output = rendered_frame_text(buffer);
        output.push_str("styles:\n");
        let area = buffer.area;
        for y in area.y..area.bottom() {
            let mut run: Option<(u16, u16, (Color, Color, Modifier))> = None;
            let mut x = area.x;
            while x < area.right() {
                let cell = &buffer[(x, y)];
                let symbol_width = UnicodeWidthStr::width(cell.symbol()).max(1) as u16;
                let end = x.saturating_add(symbol_width).min(area.right());
                let style = (cell.fg, cell.bg, cell.modifier);
                let is_default = style == (Color::Reset, Color::Reset, Modifier::empty());
                match (run, is_default) {
                    (Some((start, run_end, active)), true) => {
                        output.push_str(&format!("({start},{y})..({run_end},{y}) {active:?}\n"));
                        run = None;
                    }
                    (Some((start, run_end, active)), false) if active != style || run_end != x => {
                        output.push_str(&format!("({start},{y})..({run_end},{y}) {active:?}\n"));
                        run = Some((x, end, style));
                    }
                    (Some((start, _, active)), false) => run = Some((start, end, active)),
                    (None, false) => run = Some((x, end, style)),
                    _ => {}
                }
                x = end;
            }
            if let Some((start, run_end, active)) = run {
                output.push_str(&format!("({start},{y})..({run_end},{y}) {active:?}\n"));
            }
        }
        output
    }

    fn semantic_gallery_blocks(locale: Locale) -> Vec<TranscriptBlock> {
        let zh = locale == Locale::ZhHans;
        let mut user = block(
            BlockKind::User,
            if zh {
                "重构会话记录，但不能隐藏风险。"
            } else {
                "Refactor the transcript without hiding risk."
            },
        );
        user.id = "gallery:user".into();
        user.status.clear();

        let mut assistant = block(
            BlockKind::Assistant,
            if zh {
                "我会让常规操作保持安静，同时让可审阅的改动清晰可见，并在窄屏和宽屏上保留批准、补丁与失败证据。"
            } else {
                "I will keep routine work quiet and reviewable changes visible across every supported terminal width."
            },
        );
        assistant.id = "gallery:assistant".into();
        assistant.status.clear();
        assistant.assistant_phase = Some(crate::rpc::AssistantMessagePhase::FinalAnswer);

        let mut thinking = block(
            BlockKind::Thinking,
            if zh {
                "正在映射生命周期所有权与渲染路径。"
            } else {
                "Mapping lifecycle owners and render paths."
            },
        );
        thinking.id = "gallery:thinking".into();
        thinking.title = if zh {
            "检查投影边界"
        } else {
            "Inspecting the projection boundary"
        }
        .into();
        thinking.status.clear();
        thinking.collapsible = true;
        thinking.expanded = true;

        let mut tool = block(BlockKind::Tool, "");
        tool.id = "gallery:tool".into();
        tool.tool_kind = Some(crate::tool_projection::ToolKind::Read);
        tool.title = crate::tool_projection::format_tool_title(
            "Read",
            if zh {
                "检查语义单元契约"
            } else {
                "Inspect the semantic cell contract"
            },
        );
        tool.status.clear();
        tool.collapsible = false;
        tool.tool_members = vec![tool_member(
            "gallery:tool",
            "src/semantic_cell.rs",
            "completed",
            "completed",
            "",
        )];

        let mut group = read_group(
            vec![
                tool_member("gallery:group-1", "src/transcript.rs", "", "completed", ""),
                tool_member("gallery:group-2", "src/app/render.rs", "", "completed", ""),
            ],
            true,
        );
        group.id = "gallery:group".into();

        let mut approval = block(BlockKind::Governance, "cargo test --workspace");
        approval.id = "gallery:approval".into();
        approval.title = if zh {
            "Shell 命令需要批准"
        } else {
            "Shell command needs approval"
        }
        .into();
        approval.status = tr(locale, MessageId::StatusWaitingApproval).into();
        approval.collapsible = false;

        let mut patch = block(
            BlockKind::Tool,
            "--- /dev/null\n+++ b/src/semantic_cell.rs\n+pub enum SemanticCellKind {}",
        );
        patch.id = "gallery:patch".into();
        patch.tool_kind = Some(crate::tool_projection::ToolKind::Patch);
        patch.title = crate::tool_projection::format_tool_title(
            "Edit",
            if zh {
                "添加类型化骨架"
            } else {
                "Add typed skeletons"
            },
        );
        patch.body_kind = BlockBodyKind::Diff;
        patch.additions = 1;
        patch.deletions = 0;
        patch.status = "applied".into();
        patch.collapsible = true;
        patch.expanded = false;

        let mut failure = block(BlockKind::Diagnostic, "");
        failure.id = "gallery:failure".into();
        failure.run_id = "run-failure".into();
        failure.title = if zh {
            "响应失败"
        } else {
            "Response failed"
        }
        .into();
        failure.status.clear();
        failure.failure = Some(crate::transcript::FailurePresentation {
            kind: crate::transcript::FailureKind::Failed,
            action: FailureAction::Retry,
            owner: "Carina".into(),
            reason: if zh {
                "模型服务商在返回完整响应前停止了"
            } else {
                "The provider stopped before returning a complete response"
            }
            .into(),
            source_event_id: "event-failure".into(),
            run_id: "run-failure".into(),
        });

        let mut notice = block(
            BlockKind::Diagnostic,
            if zh {
                "较早的常规工具详情已被摘要。"
            } else {
                "Older routine tool detail was summarized."
            },
        );
        notice.id = "gallery:notice".into();
        notice.title = if zh {
            "上下文已压缩"
        } else {
            "Context compacted"
        }
        .into();
        notice.status.clear();
        notice.collapsible = false;

        vec![
            user, assistant, thinking, tool, group, approval, patch, failure, notice,
        ]
    }

    #[derive(Clone, Copy)]
    struct VisualFixture {
        width: u16,
        locale: Locale,
        density: DensityMode,
        glyph_mode: crate::glyphs::GlyphMode,
        polarity: crate::theme::Polarity,
        color_level: crate::theme::ColorLevel,
        selected: bool,
    }

    fn render_semantic_gallery(fixture: VisualFixture) -> String {
        let (mut app, root, server) = production_render_app();
        app.options.locale = Some(fixture.locale.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|locale| *locale == fixture.locale)
            .unwrap_or_default();
        app.theme = crate::theme::Theme::new(fixture.polarity, fixture.color_level);
        app.theme.glyphs = Glyphs::new(fixture.glyph_mode);
        app.density = fixture.density;
        app.blocks = semantic_gallery_blocks(fixture.locale);
        if fixture.selected {
            app.blocks[1].selected = true;
        }
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(fixture.width, 40)).unwrap();
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let rendered = rendered_frame_contract(terminal.backend().buffer());
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
        rendered
    }

    fn gallery_risk_anchors(locale: Locale) -> [&'static str; 3] {
        if locale == Locale::ZhHans {
            ["Shell 命令需要批准", "添加类型化骨架", "响应失败"]
        } else {
            [
                "Shell command needs approval",
                "Add typed skeletons",
                "Response failed",
            ]
        }
    }

    fn gallery_geometry_anchors(locale: Locale) -> [&'static str; 4] {
        if locale == Locale::ZhHans {
            [
                "我会让常规操作保持安静",
                "检查投影边界",
                "Shell 命令需要批准",
                "响应失败",
            ]
        } else {
            [
                "I will keep routine work quiet",
                "Inspecting the projection boundary",
                "Shell command needs approval",
                "Response failed",
            ]
        }
    }

    fn gallery_wrap_anchor(locale: Locale) -> &'static str {
        if locale == Locale::ZhHans {
            "失败证据。"
        } else {
            "terminal width."
        }
    }

    #[test]
    fn semantic_cell_gallery_120_uses_production_renderer() {
        let blocks = semantic_gallery_blocks(Locale::En);
        assert_eq!(
            blocks
                .iter()
                .map(SemanticCellKind::from_block)
                .collect::<Vec<_>>(),
            vec![
                SemanticCellKind::User,
                SemanticCellKind::Assistant,
                SemanticCellKind::Thinking,
                SemanticCellKind::Tool,
                SemanticCellKind::ToolGroup,
                SemanticCellKind::Approval,
                SemanticCellKind::Patch,
                SemanticCellKind::Failure,
                SemanticCellKind::Notice,
            ]
        );

        let (mut app, root, server) = production_render_app();
        app.theme.glyphs = Glyphs::new(crate::glyphs::GlyphMode::Unicode);
        app.blocks = blocks;
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 40)).unwrap();
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        insta::assert_snapshot!(
            "semantic_cell_gallery_120",
            rendered_frame_text(terminal.backend().buffer())
        );
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    fn visible_contract(contract: &str) -> &str {
        contract.split("styles:\n").next().unwrap_or(contract)
    }

    fn anchor_position(contract: &str, anchor: &str) -> (usize, usize) {
        visible_contract(contract)
            .lines()
            .enumerate()
            .find_map(|(row, line)| {
                line.find(anchor)
                    .map(|byte| (row, UnicodeWidthStr::width(&line[..byte])))
            })
            .unwrap_or_else(|| panic!("missing visual-density anchor {anchor:?}"))
    }

    fn plan_review_fixture(locale: Locale) -> PlanReviewOverlay {
        let summary = if locale == Locale::ZhHans {
            [
                "1. 检查会话投影边界与计划身份。",
                "2. 保持中文批注归属，即使这一条计划内容很长并且在八十列终端中需要自然换行，也不能改变逻辑行锚点。",
                "3. 验证无颜色和 ASCII 回退时的坐标。",
                "4. 读取相关协议与现有测试。",
                "5. 更新审批请求但不绕过治理。",
                "6. 运行聚焦测试与完整质量门禁。",
                "7. 检查重新打开计划的行为。",
                "8. 保留输入框中已有的草稿。",
                "9. 验证鼠标滚轮和键盘导航一致。",
                "10. 检查多语言按钮不会溢出。",
                "11. 确认关闭审阅不会执行计划。",
                "12. 记录最终验证结果。",
            ]
            .join("\n")
        } else {
            [
                "1. Inspect the session projection boundary and Plan identity.",
                "2. Preserve comment ownership across a deliberately long terminal line that wraps at eighty columns without changing its logical line anchor.",
                "3. Verify coordinates in no-color and ASCII fallback modes.",
                "4. Read the relevant protocol and existing tests.",
                "5. Update approval requests without bypassing governance.",
                "6. Run focused tests and the complete quality gates.",
                "7. Check the behavior for reopening the current plan.",
                "8. Preserve any draft that already exists in the composer.",
                "9. Verify mouse-wheel and keyboard navigation parity.",
                "10. Check that localized actions remain inside the popup.",
                "11. Confirm closing the review executes nothing.",
                "12. Record the final verification outcome.",
            ]
            .join("\n")
        };
        let mut review = PlanReviewOverlay::new("run-plan-review", summary);
        review.cursor = 1;
        review.toggle_mark();
        review.move_down(8);
        review.begin_comment();
        review.append_comment(if locale == Locale::ZhHans {
            "这里需要补充失败路径。"
        } else {
            "Add the failure path here."
        });
        assert!(review.commit_comment());
        review.cursor = 1;
        review.clamp(8);
        review
    }

    fn render_plan_review(
        width: u16,
        locale: Locale,
        glyph_mode: crate::glyphs::GlyphMode,
        polarity: crate::theme::Polarity,
        color_level: crate::theme::ColorLevel,
        selected_line: usize,
    ) -> String {
        let (mut app, root, server) = production_render_app();
        app.options.workspace = std::path::PathBuf::from("/workspace/carina");
        app.options.locale = Some(locale.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|candidate| *candidate == locale)
            .unwrap_or_default();
        app.theme = crate::theme::Theme::new(polarity, color_level);
        app.theme.glyphs = Glyphs::new(glyph_mode);
        let mut review = plan_review_fixture(locale);
        review.cursor = selected_line.min(review.line_count().saturating_sub(1));
        review.clamp(8);
        app.overlays.replace(Overlay::PlanReview(review));
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 30)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let rendered = rendered_frame_contract(terminal.backend().buffer());
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
        rendered
    }

    #[test]
    fn plan_review_visual_matrix_owns_wrapping_comments_and_actions() {
        for (locale_name, locale, anchor) in [
            ("en", Locale::En, "Preserve comment ownership"),
            ("zh_hans", Locale::ZhHans, "保持中文批注归属"),
        ] {
            let mut anchor_rows = Vec::new();
            for width in [80, 120, 160] {
                let rendered = render_plan_review(
                    width,
                    locale,
                    crate::glyphs::GlyphMode::Unicode,
                    crate::theme::Polarity::Dark,
                    crate::theme::ColorLevel::TrueColor,
                    1,
                );
                for required in [
                    tr(locale, MessageId::PlanReview),
                    tr(locale, MessageId::Approve),
                    tr(locale, MessageId::Revise),
                    tr(locale, MessageId::Comment),
                    tr(locale, MessageId::CloseReview),
                    anchor,
                ] {
                    assert!(
                        visible_contract(&rendered).contains(required),
                        "{width}-column {locale_name} Plan Review cropped {required:?}"
                    );
                }
                assert!(
                    visible_contract(&rendered)
                        .contains(Glyphs::new(crate::glyphs::GlyphMode::Unicode).bullet())
                );
                anchor_rows.push(anchor_position(&rendered, anchor).0);
                insta::assert_snapshot!(format!("plan_review_{locale_name}_{width}"), rendered);
            }
            assert_eq!(anchor_rows[1], anchor_rows[2]);
        }
    }

    #[test]
    fn plan_review_fallback_axes_preserve_terminal_cell_anchors() {
        for width in [80, 120, 160] {
            for (locale, content_anchor) in [
                (Locale::En, "Preserve comment ownership"),
                (Locale::ZhHans, "保持中文批注归属"),
            ] {
                let baseline = render_plan_review(
                    width,
                    locale,
                    crate::glyphs::GlyphMode::Unicode,
                    crate::theme::Polarity::Dark,
                    crate::theme::ColorLevel::TrueColor,
                    1,
                );
                let anchors = [
                    content_anchor,
                    tr(locale, MessageId::Approve),
                    tr(locale, MessageId::Comment),
                    tr(locale, MessageId::CloseReview),
                ];
                for (glyph_mode, polarity, color_level) in [
                    (
                        crate::glyphs::GlyphMode::Ascii,
                        crate::theme::Polarity::Dark,
                        crate::theme::ColorLevel::None,
                    ),
                    (
                        crate::glyphs::GlyphMode::Unicode,
                        crate::theme::Polarity::Dark,
                        crate::theme::ColorLevel::Basic,
                    ),
                    (
                        crate::glyphs::GlyphMode::Unicode,
                        crate::theme::Polarity::Dark,
                        crate::theme::ColorLevel::Ansi256,
                    ),
                    (
                        crate::glyphs::GlyphMode::Unicode,
                        crate::theme::Polarity::Light,
                        crate::theme::ColorLevel::TrueColor,
                    ),
                ] {
                    let fallback =
                        render_plan_review(width, locale, glyph_mode, polarity, color_level, 1);
                    for anchor in anchors {
                        assert_eq!(
                            anchor_position(&baseline, anchor),
                            anchor_position(&fallback, anchor),
                            "{width}-column {locale:?} fallback moved {anchor:?}"
                        );
                    }
                }
                let other_selection = render_plan_review(
                    width,
                    locale,
                    crate::glyphs::GlyphMode::Unicode,
                    crate::theme::Polarity::Dark,
                    crate::theme::ColorLevel::TrueColor,
                    0,
                );
                for anchor in anchors {
                    assert_eq!(
                        anchor_position(&baseline, anchor),
                        anchor_position(&other_selection, anchor),
                        "{width}-column {locale:?} selection moved {anchor:?}"
                    );
                }
            }
        }
    }

    #[test]
    fn short_plan_review_hugs_its_content_without_losing_actions() {
        let (mut app, root, server) = production_render_app();
        app.options.workspace = std::path::PathBuf::from("/workspace/carina");
        app.overlays
            .replace(Overlay::PlanReview(PlanReviewOverlay::new(
                "run-short-plan",
                "Verify provider recovery before applying changes.",
            )));

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 40)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let rendered = rendered_frame_text(terminal.backend().buffer());
        let title_row = anchor_position(&rendered, tr(Locale::En, MessageId::PlanReview)).0;
        let hint_row = anchor_position(&rendered, "A Approve S Revise").0;

        assert!(rendered.contains("Verify provider recovery before applying changes."));
        assert!(rendered.contains("[ Approve ]"));
        assert!(rendered.contains("[ Close review ]"));
        assert!(title_row >= 20, "short review should remain a bottom sheet");
        assert!(
            hint_row.saturating_sub(title_row) <= 13,
            "short review should not reserve the 28-row long-plan height"
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn plan_review_owns_paste_comment_navigation_and_revision_draft_state() {
        let (mut app, root, server) = production_render_app();
        app.composer.set_text("Keep this operator draft.  ");
        let summary = "inspect\nchange\nverify\ndocument\nship\nreview\npolish\ntest\npackage\nrelease\nobserve\nreport";
        let mut review = PlanReviewOverlay::new("run-plan-input", summary);
        review.move_down(3);
        assert!(review.begin_comment());
        app.overlays.replace(Overlay::PlanReview(review));

        app.handle_event(Event::Paste("补充 failure path\r\n再验证".into()))
            .unwrap();
        let Some(Overlay::PlanReview(review)) = app.overlays.active() else {
            panic!("Plan Review must retain paste ownership");
        };
        assert_eq!(review.comment_draft, "补充 failure path\n再验证");
        assert_eq!(review.cursor, 1);
        assert_eq!(app.composer.text(), "Keep this operator draft.  ");
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 30)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let comment_frame = rendered_frame_text(terminal.backend().buffer());
        assert!(comment_frame.contains("Comment on L2"));
        assert!(comment_frame.contains("补充 failure path"));
        assert!(!comment_frame.contains("[ Approve ]"));

        app.handle_overlay_key(KeyEvent::new(KeyCode::Down, KeyModifiers::NONE));
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::ScrollDown,
            column: 40,
            row: 10,
            modifiers: KeyModifiers::NONE,
        });
        let Some(Overlay::PlanReview(review)) = app.overlays.active() else {
            unreachable!();
        };
        assert_eq!(review.cursor, 1, "comment editing freezes its line anchor");
        assert_eq!(review.comment_range(), Some((1, 1)));
        assert_eq!(
            review.scroll, 4,
            "navigation and wheel input scroll the owned review body"
        );

        app.handle_overlay_key(KeyEvent::new(KeyCode::Enter, KeyModifiers::NONE));
        assert!(app.notice.is_localized(MessageId::PlanCommentSaved));
        let Some(Overlay::PlanReview(review)) = app.overlays.active() else {
            unreachable!();
        };
        assert_eq!(review.comment_count(), 1);
        assert_eq!(review.summary, summary);

        app.handle_event(Event::Paste("must-not-reach-the-composer".into()))
            .unwrap();
        assert_eq!(
            app.composer.text(),
            "Keep this operator draft.  ",
            "a non-commenting Plan Review must absorb paste instead of mutating the hidden composer"
        );

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::ScrollDown,
            column: 40,
            row: 10,
            modifiers: KeyModifiers::NONE,
        });
        let Some(Overlay::PlanReview(review)) = app.overlays.active() else {
            unreachable!();
        };
        assert_eq!(review.cursor, 4);

        app.revise_plan();
        assert!(app.overlays.active().is_none());
        assert!(
            app.active_run_id.is_none(),
            "revision never submits automatically"
        );
        assert!(
            app.composer
                .text()
                .starts_with("Keep this operator draft.  \n\n"),
            "revision preserves the operator draft byte-for-byte before appending its seed"
        );
        assert!(
            app.composer
                .text()
                .contains(tr(Locale::En, MessageId::PlanRevisionSeed))
        );
        assert!(app.composer.text().contains("L2"));
        assert!(app.composer.text().contains("补充 failure path"));

        app.composer.set_text("");
        let retained_element =
            app.composer
                .insert_element("[image:review]", crate::media::IMAGE_ELEMENT_KIND, None);
        app.overlays
            .replace(Overlay::PlanReview(PlanReviewOverlay::new(
                "run-plan-element",
                summary,
            )));
        app.revise_plan();
        assert!(
            app.composer
                .elements()
                .iter()
                .any(|element| element.id == retained_element),
            "revision insertion must preserve atomic composer elements"
        );
        assert!(app.composer.text().starts_with("[image:review]\n\n"));
        assert!(
            app.composer
                .text()
                .ends_with(tr(Locale::En, MessageId::PlanRevisionSeed))
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn view_plan_reopens_only_the_idle_latest_typed_plan() {
        let (mut app, root, server) = production_render_app();
        let plan = crate::rpc::Session {
            session_id: "sess-view-plan".into(),
            name: String::new(),
            workspace_id: "ws".into(),
            workspace_root: root.display().to_string(),
            status: "active".into(),
            next_model: "provider/model".into(),
            next_reasoning_effort: "high".into(),
            plan_mode: true,
            permission_profile: "safe-edit".into(),
            approval_mode: "on_request".into(),
            created_at: String::new(),
            updated_at: String::new(),
            latest_run_id: "run-latest-plan".into(),
            latest_run_agent: "plan".into(),
            latest_run_result_kind: "plan".into(),
            execution_status: "completed".into(),
            summary: "Inspect\nChange\nVerify".into(),
            continuity: None,
        };
        app.active_session = Some(plan.clone());

        app.open_plan_review();
        let Some(Overlay::PlanReview(review)) = app.overlays.active() else {
            panic!("an idle completed typed Plan must reopen");
        };
        assert_eq!(review.run_id, "run-latest-plan");
        app.cancel_plan();
        assert!(app.overlays.active().is_none());
        assert!(
            app.active_session
                .as_ref()
                .is_some_and(|session| session.plan_mode)
        );

        app.open_plan_review();
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::PlanReview(_))
        ));
        app.cancel_plan();

        app.active_run_id = Some("run-active".into());
        app.open_plan_review();
        assert!(app.overlays.active().is_none());
        assert!(app.notice.is_localized(MessageId::PlanReviewUnavailable));

        app.active_run_id = None;
        for unavailable in [
            {
                let mut session = plan.clone();
                session.latest_run_result_kind = "answer".into();
                session
            },
            {
                let mut session = plan.clone();
                session.latest_run_id.clear();
                session
            },
            {
                let mut session = plan.clone();
                session.execution_status = "running".into();
                session
            },
            {
                let mut session = plan;
                session.latest_run_agent = "build".into();
                session
            },
        ] {
            app.active_session = Some(unavailable.clone());
            app.open_plan_review();
            assert!(app.overlays.active().is_none());
            assert!(app.notice.is_localized(MessageId::PlanReviewUnavailable));
        }

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn runnable_provider_without_models_is_visibly_unavailable_and_disabled() {
        let (mut app, root, server) = production_render_app();
        app.phase = Phase::Provider;
        app.inventory.reasoner.available = true;
        let provider = &mut app.inventory.providers[0];
        provider.registered = true;
        provider.available = true;
        provider.models.clear();
        app.models.clear();

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 40)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let rendered = rendered_frame_text(terminal.backend().buffer());
        let no_models = tr_format(
            Locale::En,
            MessageId::ProviderNoCompatibleModels,
            &[("provider", "test")],
        );
        let diagnostic_only = tr(Locale::En, MessageId::DiagnosticOnly);

        assert!(rendered.contains(&no_models), "{rendered}");
        assert!(rendered.contains(diagnostic_only));
        assert!(!rendered.contains("Use provider"));
        assert!(!rendered.contains("● Ready"));
        let (row, column) = anchor_position(&rendered, diagnostic_only);
        assert_eq!(
            app.interactions
                .action_at(Position::new(column as u16, row as u16)),
            None,
            "a zero-model provider must not expose a runnable action"
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn plan_review_keeps_the_selected_logical_line_visible_after_visual_wrapping() {
        let (mut app, root, server) = production_render_app();
        app.options.workspace = std::path::PathBuf::from("/workspace/carina");
        let mut lines = (1..12)
            .map(|index| {
                format!(
                    "line {index} contains deliberately extended review prose that occupies multiple visual rows on an eighty-column terminal before the selected line"
                )
            })
            .collect::<Vec<_>>();
        lines.push("selected terminal anchor remains visible".into());
        let mut review = PlanReviewOverlay::new("run-wrapped-cursor", lines.join("\n"));
        review.end(8);
        app.overlays.replace(Overlay::PlanReview(review));

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 30)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let rendered = rendered_frame_text(terminal.backend().buffer());
        assert!(rendered.contains("selected terminal anchor remains visible"));

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn visual_density_matrix_uses_production_renderer_and_style_runs() {
        for (locale_name, locale) in [("en", Locale::En), ("zh_hans", Locale::ZhHans)] {
            let mut wrap_rows = Vec::new();
            for width in [80, 120, 160] {
                let rendered = render_semantic_gallery(VisualFixture {
                    width,
                    locale,
                    density: DensityMode::Compact,
                    glyph_mode: crate::glyphs::GlyphMode::Unicode,
                    polarity: crate::theme::Polarity::Dark,
                    color_level: crate::theme::ColorLevel::TrueColor,
                    selected: false,
                });
                for anchor in gallery_risk_anchors(locale) {
                    assert!(
                        visible_contract(&rendered).contains(anchor),
                        "{width}-column {locale_name} fixture cropped {anchor:?}"
                    );
                }
                assert!(
                    rendered.contains("Rgb("),
                    "the owned snapshot must include per-cell style evidence"
                );
                wrap_rows.push((
                    width,
                    anchor_position(&rendered, gallery_wrap_anchor(locale)).0,
                ));
                insta::assert_snapshot!(format!("visual_density_{locale_name}_{width}"), rendered);
            }
            assert!(
                wrap_rows[0].1 > wrap_rows[1].1,
                "80-column {locale_name} fixture must wrap content that fits at 120 columns"
            );
            assert_eq!(
                wrap_rows[1].1, wrap_rows[2].1,
                "the 120-column reading measure must stay stable on an ultrawide terminal"
            );
        }
    }

    #[test]
    fn visual_density_comfortable_matrix_uses_the_same_production_tree() {
        for (locale_name, locale) in [("en", Locale::En), ("zh_hans", Locale::ZhHans)] {
            let mut wrap_rows = Vec::new();
            for width in [80, 120, 160] {
                let rendered = render_semantic_gallery(VisualFixture {
                    width,
                    locale,
                    density: DensityMode::Comfortable,
                    glyph_mode: crate::glyphs::GlyphMode::Unicode,
                    polarity: crate::theme::Polarity::Dark,
                    color_level: crate::theme::ColorLevel::TrueColor,
                    selected: false,
                });
                for anchor in gallery_risk_anchors(locale) {
                    assert!(visible_contract(&rendered).contains(anchor));
                }
                assert!(rendered.contains("Rgb("));
                wrap_rows.push(anchor_position(&rendered, gallery_wrap_anchor(locale)).0);
                insta::assert_snapshot!(
                    format!("visual_density_comfortable_{locale_name}_{width}"),
                    rendered
                );
            }
            assert!(wrap_rows[0] > wrap_rows[1]);
            assert_eq!(wrap_rows[1], wrap_rows[2]);
        }
    }

    #[test]
    fn visual_density_switch_preserves_product_and_disclosure_state() {
        let (mut app, root, server) = production_render_app();
        let config = root.join("config.json");
        app.options.density_path = Some(config.clone());
        app.options.screen_mode = Some(ScreenMode::Inline);
        app.composer.set_text("retain this draft");
        app.history_selected = Some(0);
        let mut tool = block(BlockKind::Tool, "detail");
        tool.id = "density:tool".into();
        tool.collapsible = true;
        tool.expanded = false;
        tool.selected = true;
        app.blocks = vec![tool];
        let original_id = app.blocks[0].id.clone();

        assert!(!app.effective_block_expanded(&app.blocks[0]));
        app.apply_action(Action::ToggleDensity);
        assert_eq!(app.density, DensityMode::Comfortable);
        assert!(app.effective_block_expanded(&app.blocks[0]));
        assert_eq!(app.composer.text(), "retain this draft");
        assert_eq!(app.history_selected, Some(0));
        assert_eq!(app.blocks[0].id, original_id);
        assert!(app.blocks[0].selected);
        assert_eq!(app.options.screen_mode, Some(ScreenMode::Inline));
        assert_eq!(
            super::super::load_density(Some(&config)).unwrap(),
            DensityMode::Comfortable
        );

        app.apply_action(Action::ToggleBlock("density:tool".into()));
        assert!(!app.effective_block_expanded(&app.blocks[0]));
        app.apply_action(Action::ToggleDensity);
        assert_eq!(app.density, DensityMode::Compact);
        assert!(!app.effective_block_expanded(&app.blocks[0]));

        app.options.density_path = Some(root.clone());
        app.apply_action(Action::ToggleDensity);
        assert_eq!(app.density, DensityMode::Compact);
        assert!(app.notice.is_localized(MessageId::DensityPersistFailed));

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn visual_density_comfortable_fallbacks_preserve_information_and_geometry() {
        for locale in [Locale::En, Locale::ZhHans] {
            for width in [80, 120, 160] {
                let baseline_fixture = VisualFixture {
                    width,
                    locale,
                    density: DensityMode::Comfortable,
                    glyph_mode: crate::glyphs::GlyphMode::Unicode,
                    polarity: crate::theme::Polarity::Dark,
                    color_level: crate::theme::ColorLevel::TrueColor,
                    selected: false,
                };
                let baseline = render_semantic_gallery(baseline_fixture);
                let ascii_no_color = render_semantic_gallery(VisualFixture {
                    glyph_mode: crate::glyphs::GlyphMode::Ascii,
                    color_level: crate::theme::ColorLevel::None,
                    ..baseline_fixture
                });
                for anchor in gallery_risk_anchors(locale) {
                    assert!(visible_contract(&ascii_no_color).contains(anchor));
                }
                assert!(!ascii_no_color.contains("Rgb("));
                assert!(!ascii_no_color.contains("Indexed("));
                for anchor in gallery_geometry_anchors(locale) {
                    assert_eq!(
                        anchor_position(&baseline, anchor),
                        anchor_position(&ascii_no_color, anchor),
                        "{width}-column comfortable/{locale:?} fallback shifted {anchor:?}"
                    );
                }
                let selected = render_semantic_gallery(VisualFixture {
                    selected: true,
                    ..baseline_fixture
                });
                for anchor in gallery_geometry_anchors(locale) {
                    assert_eq!(
                        anchor_position(&baseline, anchor),
                        anchor_position(&selected, anchor),
                        "{width}-column comfortable/{locale:?} selection shifted {anchor:?}"
                    );
                }
                if locale == Locale::En {
                    for (polarity, color_level) in [
                        (
                            crate::theme::Polarity::Dark,
                            crate::theme::ColorLevel::Basic,
                        ),
                        (
                            crate::theme::Polarity::Dark,
                            crate::theme::ColorLevel::Ansi256,
                        ),
                        (
                            crate::theme::Polarity::Light,
                            crate::theme::ColorLevel::TrueColor,
                        ),
                    ] {
                        let rendered = render_semantic_gallery(VisualFixture {
                            polarity,
                            color_level,
                            ..baseline_fixture
                        });
                        for anchor in gallery_risk_anchors(locale) {
                            assert!(visible_contract(&rendered).contains(anchor));
                        }
                        for anchor in gallery_geometry_anchors(locale) {
                            assert_eq!(
                                anchor_position(&baseline, anchor),
                                anchor_position(&rendered, anchor),
                                "{width}-column comfortable/{polarity:?}/{color_level:?} shifted {anchor:?}"
                            );
                        }
                    }
                }
            }
        }
    }

    #[test]
    fn visual_density_fallbacks_and_selection_preserve_information_and_geometry() {
        for locale in [Locale::En, Locale::ZhHans] {
            for width in [80, 120, 160] {
                let baseline_fixture = VisualFixture {
                    width,
                    locale,
                    density: DensityMode::Compact,
                    glyph_mode: crate::glyphs::GlyphMode::Unicode,
                    polarity: crate::theme::Polarity::Dark,
                    color_level: crate::theme::ColorLevel::TrueColor,
                    selected: false,
                };
                let baseline = render_semantic_gallery(baseline_fixture);
                let ascii_no_color = render_semantic_gallery(VisualFixture {
                    glyph_mode: crate::glyphs::GlyphMode::Ascii,
                    color_level: crate::theme::ColorLevel::None,
                    ..baseline_fixture
                });
                let visible = visible_contract(&ascii_no_color);
                for anchor in gallery_risk_anchors(locale) {
                    assert!(visible.contains(anchor));
                }
                assert!(visible.contains("! "));
                assert!(visible.contains("x "));
                assert!(!visible.contains('\u{fffd}'));
                assert!(!ascii_no_color.contains("Rgb("));
                assert!(!ascii_no_color.contains("Indexed("));

                for anchor in gallery_geometry_anchors(locale) {
                    assert_eq!(
                        anchor_position(&baseline, anchor),
                        anchor_position(&ascii_no_color, anchor),
                        "{width}-column {locale:?} ASCII/NO_COLOR shifted {anchor:?}"
                    );
                }

                let selected = render_semantic_gallery(VisualFixture {
                    selected: true,
                    ..baseline_fixture
                });
                for anchor in gallery_geometry_anchors(locale) {
                    assert_eq!(
                        anchor_position(&baseline, anchor),
                        anchor_position(&selected, anchor),
                        "{width}-column {locale:?} selection chrome shifted {anchor:?}"
                    );
                }

                if locale == Locale::En {
                    for (polarity, color_level) in [
                        (
                            crate::theme::Polarity::Dark,
                            crate::theme::ColorLevel::Basic,
                        ),
                        (
                            crate::theme::Polarity::Dark,
                            crate::theme::ColorLevel::Ansi256,
                        ),
                        (
                            crate::theme::Polarity::Light,
                            crate::theme::ColorLevel::TrueColor,
                        ),
                    ] {
                        let rendered = render_semantic_gallery(VisualFixture {
                            polarity,
                            color_level,
                            ..baseline_fixture
                        });
                        for anchor in gallery_risk_anchors(locale) {
                            assert!(visible_contract(&rendered).contains(anchor));
                        }
                        for anchor in gallery_geometry_anchors(locale) {
                            assert_eq!(
                                anchor_position(&baseline, anchor),
                                anchor_position(&rendered, anchor),
                                "{width}-column {polarity:?}/{color_level:?} shifted {anchor:?}"
                            );
                        }
                        assert!(!visible_contract(&rendered).contains('\u{fffd}'));
                        match color_level {
                            crate::theme::ColorLevel::Basic => {
                                assert!(!rendered.contains("Rgb("));
                                assert!(!rendered.contains("Indexed("));
                            }
                            crate::theme::ColorLevel::Ansi256 => {
                                assert!(rendered.contains("Indexed("));
                                assert!(!rendered.contains("Rgb("));
                            }
                            crate::theme::ColorLevel::TrueColor => {
                                assert!(rendered.contains("Rgb("));
                            }
                            crate::theme::ColorLevel::None => unreachable!(),
                        }
                    }
                }
            }
        }
    }

    #[test]
    fn cjk_modal_button_hit_region_matches_visible_cells() {
        let area = Rect::new(10, 4, 30, 1);
        let rect = modal_button_rect(area, area.x, "取消");
        assert_eq!(rect.width, 8, "[ 取消 ] occupies eight terminal cells");

        let mut interactions = InteractionMap::default();
        interactions.begin_frame();
        interactions.register(HitRegion {
            component: ComponentId(24),
            area: rect,
            action: Action::CloseOverlay,
        });
        assert_eq!(
            interactions.action_at(Position::new(rect.right() - 1, rect.y)),
            Some(Action::CloseOverlay)
        );
        assert_eq!(
            interactions.action_at(Position::new(rect.right(), rect.y)),
            None,
            "the first blank cell after the visible button must not be clickable"
        );
    }

    #[test]
    fn cjk_composer_and_overlay_titles_stay_inside_owned_cells() {
        for width in 1..16 {
            let composer = truncate_cells("继续检查工作区状态", width);
            assert!(UnicodeWidthStr::width(composer.as_str()) <= width);
        }

        let mut terminal = ratatui::Terminal::new(ratatui::backend::TestBackend::new(12, 3))
            .expect("test terminal");
        terminal
            .draw(|frame| {
                frame.render_widget(
                    Paragraph::new("").block(
                        Block::default()
                            .borders(Borders::ALL)
                            .border_type(Glyphs::default().outer_border_type())
                            .title(" 需要审批的长标题 "),
                    ),
                    frame.area(),
                );
            })
            .expect("render narrow overlay title");
        assert_eq!(terminal.backend().buffer().area.width, 12);
    }

    #[test]
    fn chinese_steer_keeps_semantic_marker_and_localized_title() {
        let block =
            TranscriptBlock::local_steer("steer-1".into(), "run-1".into(), "继续检查".into());
        let lines = transcript_lines(&block, Locale::ZhHans, TranscriptStyles::default(), 80);
        let visible = plain(&lines);
        assert_eq!(block.user_kind, UserBlockKind::Steer);
        assert!(visible[0].starts_with(&format!(
            "{}你      你追加了指令  继续检查",
            Glyphs::default().steer()
        )));
        assert!(visible[0].ends_with("  queued"));
    }

    #[test]
    fn failure_cell_is_ascii_safe_and_wraps_cjk_at_supported_widths() {
        let mut failure = block(BlockKind::Diagnostic, "");
        failure.id = "failure:run-1".into();
        failure.run_id = "run-1".into();
        failure.title = "Execution failed".into();
        failure.failure = Some(crate::transcript::FailurePresentation {
            kind: crate::transcript::FailureKind::Failed,
            action: FailureAction::Retry,
            owner: "构建代理".into(),
            reason: "服务暂时不可用，请检查配置后重试".into(),
            source_event_id: "event-1".into(),
            run_id: "run-1".into(),
        });
        let styles = TranscriptStyles {
            glyphs: Glyphs::new(crate::glyphs::GlyphMode::Ascii),
            ..TranscriptStyles::default()
        };
        for width in [80, 120, 160] {
            let lines = transcript_lines(&failure, Locale::ZhHans, styles, width);
            let visible = plain(&lines);
            assert!(visible[0].starts_with(styles.glyphs.failure_prefix()));
            assert!(
                visible[1..]
                    .iter()
                    .all(|line| line.starts_with(styles.glyphs.tool_gutter()))
            );
            assert!(
                visible
                    .iter()
                    .all(|line| UnicodeWidthStr::width(line.as_str()) <= width as usize)
            );
            assert!(visible.iter().any(|line| line.contains("[Alt-R] 重试")));
            assert!(visible.iter().all(|line| !line.contains('✗')));
        }
    }

    #[test]
    fn every_locale_keeps_equal_user_and_assistant_role_geometry() {
        let glyphs = Glyphs::default();
        for locale in Locale::ALL {
            for message in [MessageId::TranscriptYou, MessageId::TranscriptCarina] {
                let label = tr(locale, message);
                assert!(!label.is_empty());
                assert!(
                    UnicodeWidthStr::width(label) <= layout_contract::TRANSCRIPT_ROLE_LABEL_WIDTH,
                    "{locale:?} {message:?} is wider than the owned role field"
                );
                assert_eq!(
                    transcript_role_prefix(
                        glyphs.role_prefix(),
                        label,
                        Style::default(),
                        Style::default(),
                    )
                    .width(),
                    layout_contract::TRANSCRIPT_ROLE_PREFIX_WIDTH
                );
            }
        }

        for locale in [Locale::En, Locale::Es, Locale::Fr] {
            for message in [
                MessageId::TranscriptYou,
                MessageId::TranscriptCarina,
                MessageId::TranscriptThinking,
                MessageId::TranscriptAction,
                MessageId::TranscriptStatus,
                MessageId::TranscriptDiagnostic,
            ] {
                let label = tr(locale, message);
                assert_ne!(
                    label,
                    label.to_uppercase(),
                    "{locale:?} still shouts {label}"
                );
            }
        }
    }

    #[test]
    fn user_and_assistant_share_the_same_hanging_indent_snapshot() {
        let mut user = block(BlockKind::User, "abcdefghijklmno");
        user.status.clear();
        let mut assistant = block(BlockKind::Assistant, "abcdefghijklmno");
        assistant.status.clear();
        let user = plain(&transcript_lines(
            &user,
            Locale::En,
            TranscriptStyles::default(),
            18,
        ));
        let assistant = plain(&transcript_lines(
            &assistant,
            Locale::En,
            TranscriptStyles::default(),
            18,
        ));

        assert!(user[1].starts_with(&" ".repeat(10)));
        assert!(assistant[1].starts_with(&" ".repeat(10)));
        insta::assert_snapshot!(
            [user.join("\n"), assistant.join("\n")].join("\n\n"),
            @r###"
        • You     abcdefgh
                  ijklmno

        • Carina  abcdefgh
                  ijklmno
        "###
        );
    }

    fn viewport(
        content_width: u16,
        height: usize,
        requested_scroll: usize,
        follow_bottom: bool,
    ) -> TranscriptViewport {
        TranscriptViewport {
            locale: Locale::En,
            density: DensityMode::Compact,
            content_width,
            tool_expand_key: crate::keybinding::KeyBindings::default()
                .expand_tools
                .label(),
            height,
            requested_scroll,
            follow_bottom,
            anchor: None,
        }
    }

    #[test]
    fn long_assistant_content_is_never_limited_to_six_lines() {
        let body = (1..=10)
            .map(|line| format!("line {line}"))
            .collect::<Vec<_>>()
            .join("\n");
        let block = block(BlockKind::Assistant, &body);
        let lines = transcript_lines(&block, Locale::En, TranscriptStyles::default(), 100);
        let (height, _) = transcript_block_height(&block, Locale::En, 100);

        assert_eq!(lines.len(), 10);
        let visible = plain(&lines);
        assert_eq!(
            visible[0],
            format!("{}Carina  line 1", Glyphs::default().role_prefix())
        );
        assert_eq!(visible.last().unwrap(), "          line 10");
        assert_eq!(height, 10);
    }

    #[test]
    fn cjk_and_long_lines_contribute_their_wrapped_visual_rows() {
        let block = block(
            BlockKind::Assistant,
            "这是一个会在窄终端中换行的完整中文回答",
        );
        let (wide_height, _) = transcript_block_height(&block, Locale::En, 80);
        let (narrow_height, _) = transcript_block_height(&block, Locale::En, 12);

        assert!(narrow_height > wide_height);
    }

    #[test]
    fn bottom_follow_reaches_the_last_visual_row() {
        let block = block(
            BlockKind::Assistant,
            &(1..=20)
                .map(|line| format!("row {line}"))
                .collect::<Vec<_>>()
                .join("\n"),
        );
        let layout = TranscriptLayout::new(&[block], viewport(80, 6, 0, true), &mut HashMap::new());

        assert!(layout.max_scroll > 6);
        assert_eq!(layout.viewport_start, layout.max_scroll);
        assert_eq!(layout.viewport_start + 6, layout.total_height);
    }

    #[test]
    fn new_output_preserves_a_reader_scrolled_into_history() {
        let first = block(BlockKind::Assistant, "one\ntwo\nthree\nfour\nfive\nsix");
        let initial = TranscriptLayout::new(
            std::slice::from_ref(&first),
            viewport(80, 3, 2, false),
            &mut HashMap::new(),
        );
        let appended = TranscriptLayout::new(
            &[first, block(BlockKind::Assistant, "new output")],
            viewport(80, 3, initial.viewport_start, false),
            &mut HashMap::new(),
        );

        assert_eq!(initial.viewport_start, 2);
        assert_eq!(appended.viewport_start, 2);
    }

    #[test]
    fn transcript_height_cache_reuses_stable_blocks_and_invalidates_density_and_revisions() {
        let mut stable = block(BlockKind::Assistant, "one\ntwo");
        stable.id = "stable".into();
        let expand_key = crate::keybinding::KeyBindings::default()
            .expand_tools
            .label();
        let mut cache = HashMap::from([(
            "stable".into(),
            TranscriptHeightCacheEntry {
                revision: 1,
                width: 80,
                locale: Locale::En,
                density: DensityMode::Compact,
                expand_key,
                height: 7,
                header_height: 3,
            },
        )]);

        let cached =
            TranscriptLayout::new(&[stable.clone()], viewport(80, 20, 0, false), &mut cache);
        assert_eq!(cached.items[0].height, 7);
        assert_eq!(cached.items[0].header_height, 3);

        let mut comfortable_viewport = viewport(80, 20, 0, false);
        comfortable_viewport.density = DensityMode::Comfortable;
        let density_refreshed = TranscriptLayout::new(
            std::slice::from_ref(&stable),
            comfortable_viewport,
            &mut cache,
        );
        assert_ne!(density_refreshed.items[0].height, 7);
        assert_eq!(cache["stable"].density, DensityMode::Comfortable);

        stable.layout_revision += 1;
        let refreshed = TranscriptLayout::new(&[stable], viewport(80, 20, 0, false), &mut cache);
        assert_ne!(refreshed.items[0].height, 7);
        assert_ne!(refreshed.items[0].header_height, 3);
    }

    #[test]
    fn transcript_render_cache_reuses_stable_rows_and_invalidates_density_and_revisions() {
        let mut stable = block(BlockKind::Assistant, "current body");
        stable.id = "stable".into();
        let expand_key = crate::keybinding::KeyBindings::default()
            .expand_tools
            .label();
        let mut cache = HashMap::from([(
            "stable".into(),
            TranscriptRenderCacheEntry {
                revision: 1,
                width: 80,
                locale: Locale::En,
                density: DensityMode::Compact,
                expand_key,
                lines: vec![Line::from("cached rows")],
            },
        )]);

        let cached = cached_transcript_lines(
            &mut cache,
            &stable,
            Locale::En,
            TranscriptStyles::default(),
            80,
        );
        assert_eq!(cached[0].spans[0].content, "cached rows");

        let density_refreshed = cached_transcript_lines_with_tool_key_and_density(
            &mut cache,
            &stable,
            Locale::En,
            TranscriptStyles::default(),
            80,
            expand_key,
            DensityMode::Comfortable,
        );
        assert_ne!(density_refreshed[0].spans[0].content, "cached rows");
        assert_eq!(cache["stable"].density, DensityMode::Comfortable);

        stable.layout_revision += 1;
        let refreshed = cached_transcript_lines(
            &mut cache,
            &stable,
            Locale::En,
            TranscriptStyles::default(),
            80,
        );
        assert_ne!(refreshed[0].spans[0].content, "cached rows");
        assert!(
            refreshed[0]
                .spans
                .iter()
                .any(|span| span.content.contains("current body"))
        );
    }

    #[test]
    fn only_semantic_detail_components_have_collapse_actions() {
        let user = block(BlockKind::User, "hello");
        let assistant = block(BlockKind::Assistant, "hello");
        let tool = block(BlockKind::Tool, "output");
        let thinking = block(BlockKind::Thinking, "reasoning");

        assert_eq!(transcript_click_action(&user, 0, false), None);
        assert_eq!(transcript_click_action(&assistant, 1, false), None);
        assert_eq!(
            transcript_click_action(&tool, 2, false),
            Some(Action::ToggleBlock("block".into()))
        );
        assert_eq!(
            transcript_click_action(&thinking, 3, false),
            Some(Action::ToggleBlock("block".into()))
        );
        assert_eq!(
            transcript_click_action(&user, 0, true),
            Some(Action::SelectHistory(0))
        );
        assert_eq!(transcript_click_action(&tool, 2, true), None);
    }

    #[test]
    fn compact_tool_receipt_has_no_disclosure_or_empty_body_row() {
        let mut read = block(BlockKind::Tool, "");
        read.title = "Read  src/lib.rs".into();
        read.status.clear();
        read.collapsible = false;
        let lines = transcript_lines(&read, Locale::En, TranscriptStyles::default(), 80);

        assert_eq!(lines.len(), 1);
        assert!(
            !lines[0].spans[0]
                .content
                .contains(Glyphs::default().disclosure_closed())
        );
        assert!(
            !lines[0].spans[0]
                .content
                .contains(Glyphs::default().disclosure_open())
        );
        let visible = lines[0]
            .spans
            .iter()
            .map(|span| span.content.as_ref())
            .collect::<String>();
        assert!(visible.starts_with(Glyphs::default().bullet()));
        assert!(visible.ends_with("Read  src/lib.rs"));
        assert_eq!(transcript_click_action(&read, 0, false), None);
    }

    #[test]
    fn five_reads_collapse_to_one_title_line_with_path_preview() {
        let members = (1..=5)
            .map(|index| {
                tool_member(
                    &format!("read-{index}"),
                    &format!("src/file-{index}.rs"),
                    "",
                    "completed",
                    "",
                )
            })
            .collect();
        let group = read_group(members, false);
        let lines = plain(&transcript_lines_with_tool_key(
            &group,
            Locale::En,
            TranscriptStyles::default(),
            80,
            "F12",
        ));

        // Dialogue-first: one title line; disclosure is the expand hatch.
        insta::assert_snapshot!(lines.join("\n"), @r"
▸ Read ×5 · src/file-1.rs, src/file-2.rs, …
");
        assert_eq!(lines.len(), 1);
        assert!(!lines[0].contains("F12"));
        assert!(!lines.join("\n").contains("src/file-3.rs"));
    }

    #[test]
    fn expanded_read_group_restores_every_member_path() {
        let members = (1..=7)
            .map(|index| {
                tool_member(
                    &format!("read-{index}"),
                    &format!("src/file-{index}.rs"),
                    "",
                    "completed",
                    "",
                )
            })
            .collect();
        let lines = plain(&transcript_lines_with_tool_key(
            &read_group(members, true),
            Locale::En,
            TranscriptStyles::default(),
            80,
            "F12",
        ));

        insta::assert_snapshot!(lines.join("\n"), @r"
▾ Read ×7 · src/file-1.rs, src/file-2.rs, …
│ src/file-1.rs
│ src/file-2.rs
│ src/file-3.rs
│ src/file-4.rs
│ src/file-5.rs
│ src/file-6.rs
│ src/file-7.rs
");
        assert!(lines.iter().any(|line| line.contains("src/file-7.rs")));
    }

    #[test]
    fn failed_and_running_group_snapshots_keep_member_lifecycle_detail() {
        let running = read_group(
            vec![
                tool_member("read-1", "src/ready.rs", "", "completed", ""),
                tool_member("read-2", "src/running.rs", "pending", "pending", ""),
            ],
            false,
        );
        // Collapsed running group: title only (paths in preview).
        insta::assert_snapshot!(plain(&transcript_lines(
            &running,
            Locale::En,
            TranscriptStyles::default(),
            80,
        )).join("\n"), @r"
▸ Reading ×2 · src/ready.rs, src/running.rs
");

        let failed = read_group(
            vec![
                tool_member("read-1", "src/ready.rs", "", "completed", ""),
                tool_member(
                    "read-2",
                    "src/missing.rs",
                    "failed",
                    "failed",
                    "permission denied",
                ),
            ],
            true,
        );
        insta::assert_snapshot!(plain(&transcript_lines(
            &failed,
            Locale::En,
            TranscriptStyles::default(),
            80,
        )).join("\n"), @r"
▾ Read ×2 · src/ready.rs, src/missing.rs  1 failed
│ src/ready.rs
│ ✗ src/missing.rs  failed
│   permission denied
");
    }

    #[test]
    fn collapsed_long_output_counts_omitted_lines_without_a_second_hint_row() {
        let mut tool = block(
            BlockKind::Tool,
            &(1..=7)
                .map(|index| format!("output {index}"))
                .collect::<Vec<_>>()
                .join("\n"),
        );
        tool.expanded = false;
        let collapsed = plain(&transcript_lines_with_tool_key(
            &tool,
            Locale::En,
            TranscriptStyles::default(),
            80,
            "F12",
        ));
        insta::assert_snapshot!(collapsed.join("\n"), @r"
▸ Result  complete
│ ... 7 lines omitted · F12 expand
");
        assert_eq!(
            collapsed.iter().filter(|line| line.contains("F12")).count(),
            1
        );
        let narrow = transcript_lines_with_tool_key(
            &tool,
            Locale::En,
            TranscriptStyles::default(),
            18,
            "F12",
        );
        assert_eq!(
            Paragraph::new(narrow.clone())
                .wrap(Wrap { trim: false })
                .line_count(18),
            narrow.len(),
            "the escape hint must truncate inside its row instead of wrapping"
        );

        tool.expanded = true;
        let expanded = plain(&transcript_lines_with_tool_key(
            &tool,
            Locale::En,
            TranscriptStyles::default(),
            80,
            "F12",
        ));
        assert!(expanded[0].starts_with(Glyphs::default().disclosure_open()));
        assert_eq!(expanded.len(), 8);
        assert!(!expanded.join("\n").contains("omitted"));
    }

    #[test]
    fn expanded_large_cjk_diff_is_progressive_and_keeps_one_bounded_escape_row() {
        let body = (0..=crate::diff_render::DIFF_PROGRESSIVE_LINE_LIMIT)
            .map(|index| format!("+第 {index} 行"))
            .collect::<Vec<_>>()
            .join("\n");
        let context = ToolLineContext {
            locale: Locale::ZhHans,
            density: DensityMode::Compact,
            styles: TranscriptStyles::default(),
            content_width: 80,
            expand_key: "F12",
        };
        let lines = tool_detail_lines(&body, BlockBodyKind::Diff, true, context, false);
        let visible = plain(&lines);
        assert_eq!(
            visible.len(),
            crate::diff_render::DIFF_PROGRESSIVE_LINE_LIMIT + 1
        );
        assert!(visible.last().unwrap().contains("/changes"));

        let narrow = tool_detail_lines(
            &body,
            BlockBodyKind::Diff,
            true,
            ToolLineContext {
                content_width: 18,
                ..context
            },
            false,
        );
        assert_eq!(
            Paragraph::new(narrow.clone())
                .wrap(Wrap { trim: false })
                .line_count(18),
            narrow.len(),
            "CJK diff and its escape hatch must stay inside owned rows"
        );
    }

    #[test]
    fn expanded_edit_group_renders_each_reviewable_diff_and_stats() {
        let mut group = block(BlockKind::Tool, "");
        group.tool_kind = Some(crate::tool_projection::ToolKind::Patch);
        group.status.clear();
        group.expanded = true;
        group.additions = 2;
        group.deletions = 2;
        group.tool_members = [
            ("one", "src/one.rs", "old", "new"),
            ("two", "src/two.rs", "before", "after"),
        ]
        .into_iter()
        .map(|(id, path, old, new)| ToolGroupMember {
            id: format!("tool:{id}"),
            tool_name: "patch".into(),
            title: path.into(),
            body: format!("--- a/{path}\n+++ b/{path}\n-{old}\n+{new}"),
            body_kind: BlockBodyKind::Diff,
            additions: 1,
            deletions: 1,
            status: String::new(),
            lifecycle: "completed".into(),
        })
        .collect();

        assert!(!transcript_block_hidden(&group));

        insta::assert_snapshot!(plain(&transcript_lines(
            &group,
            Locale::En,
            TranscriptStyles::default(),
            80,
        )).join("\n"), @r"
▾ Edited ×2 · src/one.rs, src/two.rs  +2 -2
│ src/one.rs  +1 -1
│   --- a/src/one.rs
│   +++ b/src/one.rs
│       |     - old
│       |     + new
│ src/two.rs  +1 -1
│   --- a/src/two.rs
│   +++ b/src/two.rs
│       |     - before
│       |     + after
");
    }

    #[test]
    fn edit_diff_pairs_add_remove_markers_with_semantic_colors() {
        // Issue #19: numbered unified diff + semantic add/remove palette.
        let mut edit = block(
            BlockKind::Tool,
            "--- a/src/lib.rs\n+++ b/src/lib.rs\n@@ -1 +1 @@\n-old\n+new",
        );
        edit.title = "Edit  src/lib.rs".into();
        edit.body_kind = BlockBodyKind::Diff;
        edit.expanded = true;
        edit.additions = 1;
        edit.deletions = 1;
        let theme = crate::theme::Theme::new(
            crate::theme::Polarity::Dark,
            crate::theme::ColorLevel::Basic,
        );
        let added = theme.transcript_added();
        let removed = theme.transcript_removed();
        let metadata = theme.transcript_metadata();

        let lines = transcript_lines(
            &edit,
            Locale::En,
            TranscriptStyles {
                metadata,
                added,
                removed,
                ..TranscriptStyles::default()
            },
            80,
        );

        let plain = plain(&lines).join("\n");
        assert!(
            plain.contains("old") && plain.contains("new"),
            "diff body missing: {plain}"
        );
        assert!(plain.contains('|'), "expected line-number gutter: {plain}");
        assert_eq!(lines[0].spans[2].content, "  +1");
        assert_eq!(lines[0].spans[2].style, added);
        assert_eq!(lines[0].spans[3].content, " -1");
        assert_eq!(lines[0].spans[3].style, removed);
        // Body rows use the semantic palette for add/remove markers.
        let body_styles: Vec<_> = lines
            .iter()
            .skip(1)
            .flat_map(|line| line.spans.iter().map(|s| s.style))
            .collect();
        assert!(
            body_styles
                .iter()
                .any(|s| *s == removed || s.fg == removed.fg),
            "missing remove style"
        );
        assert!(
            body_styles.iter().any(|s| *s == added || s.fg == added.fg),
            "missing add style"
        );
    }

    #[test]
    fn expanded_tool_body_uses_one_gutter_for_source_and_wrapped_rows() {
        let mut tool = block(BlockKind::Tool, "https://example.com/one/two/three");
        tool.status.clear();
        let lines = transcript_lines(&tool, Locale::En, TranscriptStyles::default(), 14);
        let visible = plain(&lines);
        let gutter = Glyphs::default().tool_gutter();

        assert!(visible.len() > 2);
        assert!(visible.iter().skip(1).all(|line| line.starts_with(gutter)));
        let restored = visible
            .iter()
            .skip(1)
            .map(|line| line.strip_prefix(gutter).expect("tool gutter"))
            .collect::<String>();
        assert_eq!(restored, tool.body);
        assert_eq!(UnicodeWidthStr::width(gutter), 2);
    }

    #[test]
    fn thinking_body_is_dim_and_italic_while_metadata_stays_quiet() {
        let theme = crate::theme::Theme::new(
            crate::theme::Polarity::Dark,
            crate::theme::ColorLevel::TrueColor,
        );
        let thinking = block(BlockKind::Thinking, "Inspecting the renderer");
        let lines = transcript_lines(
            &thinking,
            Locale::En,
            TranscriptStyles {
                label: theme.transcript_thinking(),
                text: theme.transcript_thinking(),
                metadata: theme.transcript_metadata(),
                ..TranscriptStyles::default()
            },
            80,
        );

        assert!(
            lines[0].spans[0]
                .style
                .add_modifier
                .contains(Modifier::DIM | Modifier::ITALIC)
        );
        assert!(lines[1].spans.iter().any(|span| {
            span.style
                .add_modifier
                .contains(Modifier::DIM | Modifier::ITALIC)
        }));
        assert_eq!(
            lines[0].spans.last().unwrap().style,
            theme.transcript_metadata()
        );
    }

    #[test]
    fn collapsed_tool_hides_detail_and_keeps_one_expand_hint() {
        let mut tool = block(BlockKind::Tool, "one\ntwo\nthree");
        tool.expanded = false;
        let (height, header_height) = transcript_block_height(&tool, Locale::En, 80);

        assert_eq!(height, header_height + 1);
        assert!(
            plain(&transcript_lines(
                &tool,
                Locale::En,
                TranscriptStyles::default(),
                80,
            ))
            .join("\n")
            .contains("omitted")
        );
        for hidden in ["one", "two", "three"] {
            assert!(
                !plain(&transcript_lines(
                    &tool,
                    Locale::En,
                    TranscriptStyles::default(),
                    80,
                ))
                .join("\n")
                .contains(hidden)
            );
        }
    }

    #[test]
    fn transcript_gaps_follow_visible_adjacency_through_hidden_blocks() {
        let first = block(BlockKind::Assistant, "answer");
        let mut hidden = block(BlockKind::Status, "");
        hidden.title.clear();
        hidden.status.clear();
        let last = block(BlockKind::Tool, "details");
        let layout = TranscriptLayout::new(
            &[first, hidden, last],
            viewport(80, 20, 0, false),
            &mut HashMap::new(),
        );

        assert_eq!(layout.items[1].height, 0);
        assert_eq!(layout.items[2].top, layout.items[0].height + 1);
        assert_eq!(
            layout.total_height,
            layout.items[2].top + layout.items[2].height + layout_contract::TRANSCRIPT_FINAL_GAP
        );
    }

    #[test]
    fn only_related_operational_blocks_remove_the_gap() {
        let mut first = block(BlockKind::Tool, "one");
        let mut second = block(BlockKind::Thinking, "two");
        first.collapsible = true;
        first.expanded = false;
        second.collapsible = true;
        second.expanded = false;
        assert_eq!(
            transcript_gap(&first, &second),
            layout_contract::TRANSCRIPT_RELATED_GAP
        );

        second.expanded = true;
        assert_eq!(
            transcript_gap(&first, &second),
            layout_contract::TRANSCRIPT_RELATED_GAP
        );
        second.expanded = false;
        second.collapsible = false;
        assert_eq!(
            transcript_gap(&first, &second),
            layout_contract::TRANSCRIPT_RELATED_GAP
        );

        second.kind = BlockKind::Assistant;
        assert_eq!(
            transcript_gap(&first, &second),
            layout_contract::TRANSCRIPT_BLOCK_GAP
        );
    }

    #[test]
    fn transcript_selection_chrome_never_changes_content_geometry() {
        let transcript = Rect::new(3, 2, 42, 4);
        let block_width = transcript
            .width
            .saturating_sub(layout_contract::TRANSCRIPT_HORIZONTAL_INSET * 2);
        let content = Rect::new(
            transcript.x + layout_contract::TRANSCRIPT_HORIZONTAL_INSET,
            transcript.y,
            block_width,
            transcript.height,
        );
        let chrome = Rect::new(
            content.x - layout_contract::TRANSCRIPT_HORIZONTAL_INSET,
            content.y,
            layout_contract::TRANSCRIPT_HORIZONTAL_INSET,
            content.height,
        );
        assert_eq!(content, Rect::new(4, 2, 40, 4));
        assert_eq!(chrome, Rect::new(3, 2, 1, 4));
        assert_eq!(
            content.width, block_width,
            "selection does not reduce wrap width"
        );
    }
}
