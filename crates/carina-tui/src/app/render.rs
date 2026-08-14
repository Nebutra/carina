use std::borrow::Cow;
use std::cmp::Reverse;
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
use unicode_segmentation::UnicodeSegmentation;
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};
use xai_ratatui_textarea::wrapping::{RtOptions, word_wrap_line};

use super::{
    App, Focus, LOCALES, Phase, ScreenMode, TranscriptHeightCache, TranscriptHeightCacheEntry,
    TranscriptRenderCache, TranscriptRenderCacheEntry, TranscriptScrollAnchor,
};
use crate::component::{Action, ComponentId, HitRegion, InteractionMap};
use crate::conversation::{
    ChromeCapability, ChromeSlotKind, ChromeTone, ComposerChrome, ComposerChromeInput,
    EmptyConversation, conversation_title, localized_execution_status,
};
use crate::density::DensityMode;
use crate::file_viewer::{FileViewerLoad, selection_hint};
use crate::glyphs::{GlyphMode, GlyphPreference, GlyphSource, Glyphs};
use crate::history_search::HistoryMode;
use crate::i18n::{
    Locale, MessageId, count as tr_count, format as tr_format, localize_operator_failure_reason,
    model_health_reason_label, model_health_status_label, text as tr,
};
use crate::layout_contract;
use crate::markdown::{
    MarkdownTheme, StyledPrefix, render_prefixed as render_markdown_prefixed, wrap_styled_lines,
};
use crate::overlay::{
    ApprovalScope, ChangesFocus, Overlay, PRODUCT_MENU_ITEMS, PlanReviewOverlay,
    ProductMenuOverlay, SettingsOverlay, SettingsPage,
};
use crate::patch_review::{PatchDisposition, PatchReview};
use crate::prerequisite::{BrowserLayout, PickerWindow, PrerequisiteLayout};
use crate::product_header::{
    ConversationHeader, ConversationHeaderAction, ConversationHeaderTone, ProductHeader,
};
use crate::product_projection::{
    ActivityKind, AgentCategory, ChangeKind, FileChangeKind, ProductStatus,
};
use crate::rpc::ModelProvider;
use crate::semantic_cell::SemanticCellKind;
use crate::session_browser::{ConversationImportSource, ConversationImportStage, SessionScope};
use crate::tool_projection::{
    TodoItem, visible_group_members_with_limits, visible_output_lines_with_limits,
};
use crate::transcript::{
    BlockBodyKind, BlockKind, FailureAction, FailurePresentation, FailureRecoveryAction,
    ToolGroupMember, TranscriptBlock, UserBlockKind,
};

impl App {
    pub(super) fn render(&mut self, frame: &mut Frame<'_>) {
        self.interactions.begin_frame();
        self.product_menu_anchor = None;
        let area = frame.area();
        match self.phase {
            Phase::Conversation => self.render_conversation(frame, area),
            _ => self.render_scene(frame, area),
        }
        if matches!(self.overlays.active(), Some(Overlay::ProductMenu(_)))
            && self.product_menu_anchor.is_none()
        {
            self.overlays.resolve_active();
        }
        if let Some(overlay) = self.overlays.active().cloned() {
            self.interactions.begin_overlay();
            self.render_overlay(frame, area, &overlay);
        }
        if self.interactions.finish_frame() {
            self.dirty = true;
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
        let validation_glyph = validation_spinner(validation_elapsed, self.theme.glyphs);
        let is_ccswitch = provider.source_kind == "cc-switch";
        let is_grok_build = provider.source_kind == "grok-build";
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
        } else if is_grok_build {
            if provider.source_action == "disabled" {
                (
                    self.theme.glyphs.unavailable(),
                    grok_build_state_label(locale, &provider.source_action),
                    self.theme.muted,
                )
            } else {
                (
                    self.theme.glyphs.diamond_open(),
                    grok_build_state_label(locale, &provider.source_action),
                    self.theme.warning,
                )
            }
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
        } else if is_grok_build && provider.source_action == "disabled" {
            tr(locale, MessageId::GrokBuildDisabled)
        } else if is_grok_build {
            tr(locale, MessageId::Retry)
        } else {
            tr(locale, MessageId::Connect)
        };
        let disabled = import_validating
            || (execution_ready && !has_compatible_models)
            || (is_grok_build && provider.source_action == "disabled")
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
            let is_grok_build = provider.source_kind == "grok-build";
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
            } else if is_grok_build {
                if provider.source_action == "disabled" {
                    (
                        self.theme.glyphs.unavailable(),
                        grok_build_state_label(locale, &provider.source_action),
                        self.theme.muted,
                    )
                } else {
                    (
                        self.theme.glyphs.diamond_open(),
                        grok_build_state_label(locale, &provider.source_action),
                        self.theme.warning,
                    )
                }
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
                let route_badge = if is_grok_build && provider.source_current {
                    format!("  {sep}  {}", tr(locale, MessageId::GrokBuildSubscription))
                } else if active_route {
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
        let validation_glyph = validation_spinner(validation_elapsed, self.theme.glyphs);
        let is_ccswitch = provider.source_kind == "cc-switch";
        let is_grok_build = provider.source_kind == "grok-build";
        let is_xai = provider.id == "xai";
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
                    if is_grok_build {
                        tr(locale, MessageId::GrokBuildReadyDetail)
                    } else if is_xai {
                        tr(locale, MessageId::XaiApiBillingDetail)
                    } else {
                        tr(locale, MessageId::ProviderAvailableDetail)
                    },
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
            } else if is_grok_build {
                if provider.source_action == "disabled" {
                    (
                        self.theme.glyphs.unavailable(),
                        grok_build_state_label(locale, &provider.source_action),
                        self.theme.muted,
                        tr(locale, grok_build_guidance(&provider.source_action)),
                    )
                } else {
                    (
                        self.theme.glyphs.diamond_open(),
                        grok_build_state_label(locale, &provider.source_action),
                        self.theme.warning,
                        tr(locale, grok_build_guidance(&provider.source_action)),
                    )
                }
            } else if provider.registered {
                (
                    self.theme.glyphs.empty(),
                    tr(locale, MessageId::CredentialRequired),
                    self.theme.warning,
                    if is_xai {
                        tr(locale, MessageId::XaiApiBillingDetail)
                    } else {
                        tr(locale, MessageId::CredentialSafetyDetail)
                    },
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
        } else if is_grok_build && provider.source_action == "disabled" {
            tr(locale, MessageId::GrokBuildDisabled)
        } else if is_grok_build {
            tr(locale, MessageId::Retry)
        } else {
            tr(locale, MessageId::Connect)
        };
        let source_label = if provider.source_label.is_empty() {
            provider.id.as_str()
        } else {
            provider.source_label.as_str()
        };
        let authentication = if is_grok_build {
            tr(locale, MessageId::GrokBuildCliSession)
        } else if provider.available {
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
            || (is_grok_build && provider.source_action == "disabled")
            || (is_ccswitch && !provider.source_importable && !provider.available);
        let style = if disabled {
            self.theme.muted()
        } else if self.interactions.hovered(component) && !self.theme.no_color {
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
        if self.session_browser.conversation_import().is_open() {
            self.render_conversation_import(frame, area);
            return;
        }
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
                    tr(locale, MessageId::ImportConversations),
                    Action::OpenConversationImport,
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

    fn render_conversation_import(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let layout = BrowserLayout::compute(area);
        let import = self.session_browser.conversation_import();
        let source = match import.source() {
            ConversationImportSource::All => tr(locale, MessageId::ConversationImportAllSources),
            ConversationImportSource::ClaudeCode => "Claude Code",
            ConversationImportSource::Codex => "Codex",
        };
        let scope = if import.all_workspaces() {
            tr(locale, MessageId::AllWorkspaces)
        } else {
            tr(locale, MessageId::CurrentWorkspace)
        };
        self.render_scene_title(
            frame,
            layout.title,
            tr(locale, MessageId::ImportConversations),
            &tr_format(
                locale,
                MessageId::ConversationImportBrowseDetail,
                &[("source", source), ("scope", scope)],
            ),
        );
        let list_title = format!(" {} ", tr(locale, MessageId::ImportConversations));
        let list_area = self.render_browser_panel(frame, layout.list, &list_title);
        let chunks = Layout::vertical([
            Constraint::Length(2),
            Constraint::Min(layout_contract::SESSION_LIST_MIN_HEIGHT),
            Constraint::Length(layout_contract::CONTROL_HEIGHT),
        ])
        .split(list_area);
        frame.render_widget(
            Paragraph::new(tr(locale, MessageId::ConversationImportCopySemantics))
                .style(Style::default().fg(self.theme.muted))
                .wrap(Wrap { trim: false }),
            chunks[0],
        );

        match import.stage() {
            ConversationImportStage::Discovering => {
                frame.render_widget(
                    Paragraph::new(tr(locale, MessageId::ConversationImportScanning))
                        .style(self.theme.focus()),
                    chunks[1],
                );
                self.render_conversation_import_footer(
                    frame,
                    chunks[2],
                    &[(MessageId::Cancel, Action::CancelConversationImport)],
                );
            }
            ConversationImportStage::Selecting => {
                let discovery_failed = !import.error().is_empty();
                let rows = import
                    .candidates()
                    .iter()
                    .enumerate()
                    .map(|(index, candidate)| {
                        let checked = if import.is_selected(index) {
                            "[x]"
                        } else {
                            "[ ]"
                        };
                        let source = conversation_import_source_label(&candidate.source);
                        let state = if !candidate.importable {
                            tr(locale, MessageId::ConversationImportUnavailable).to_owned()
                        } else if candidate.new_messages == 0 {
                            tr(locale, MessageId::ConversationImportUpToDate).to_owned()
                        } else {
                            tr_format(
                                locale,
                                MessageId::ConversationImportNewMessages,
                                &[("count", &candidate.new_messages.to_string())],
                            )
                        };
                        let title_width = usize::from(chunks[1].width).saturating_sub(30).max(12);
                        let title =
                            truncate_cells(&candidate.title, title_width, self.theme.glyphs);
                        (index, format!("{checked} {source:<11} {title}  {state}"))
                    })
                    .collect::<Vec<_>>();
                if rows.is_empty() && !discovery_failed {
                    frame.render_widget(
                        Paragraph::new(tr(locale, MessageId::ConversationImportNoneFound))
                            .style(Style::default().fg(self.theme.muted))
                            .wrap(Wrap { trim: false }),
                        chunks[1],
                    );
                }
                self.render_indexed_rows(
                    frame,
                    chunks[1],
                    rows,
                    import.selected_visible(),
                    3_300,
                    |index| Some(Action::SelectConversationImport(index)),
                );
                let actions = if discovery_failed {
                    vec![
                        (
                            MessageId::ConversationImportCheckAgain,
                            Action::RetryConversationImport,
                        ),
                        (MessageId::Cancel, Action::CancelConversationImport),
                    ]
                } else {
                    vec![
                        (
                            MessageId::ConversationImportReview,
                            Action::ReviewConversationImport,
                        ),
                        (
                            MessageId::ConversationImportSelectAll,
                            Action::ToggleConversationImportAll,
                        ),
                        (
                            MessageId::ConversationImportSource,
                            Action::CycleConversationImportSource,
                        ),
                        (MessageId::Cancel, Action::CancelConversationImport),
                    ]
                };
                self.render_conversation_import_footer(frame, chunks[2], &actions);
            }
            ConversationImportStage::Confirming => {
                let count = import.selected_count().to_string();
                let messages = import.selected_message_count().to_string();
                let mut lines = vec![
                    Line::from(Span::styled(
                        tr(locale, MessageId::ConversationImportConfirmTitle),
                        self.theme.focus().add_modifier(Modifier::BOLD),
                    )),
                    Line::from(""),
                    Line::from(tr_format(
                        locale,
                        MessageId::ConversationImportConfirmDetail,
                        &[("count", &count), ("messages", &messages)],
                    )),
                ];
                if let Some((first, remaining)) = import.selected_target_summary() {
                    let suffix = if remaining == 0 {
                        String::new()
                    } else {
                        format!(" (+{remaining})")
                    };
                    lines.push(Line::from(format!(
                        "{}  {first}{suffix}",
                        tr(locale, MessageId::ConversationImportTargetWorkspace)
                    )));
                }
                lines.extend([
                    Line::from(""),
                    Line::from(Span::styled(
                        tr(locale, MessageId::ConversationImportCopySemantics),
                        Style::default().fg(self.theme.muted),
                    )),
                ]);
                frame.render_widget(Paragraph::new(lines).wrap(Wrap { trim: false }), chunks[1]);
                self.render_conversation_import_footer(
                    frame,
                    chunks[2],
                    &[
                        (MessageId::ConfirmImport, Action::ConfirmConversationImport),
                        (MessageId::Cancel, Action::CancelConversationImport),
                    ],
                );
            }
            ConversationImportStage::Applying => {
                frame.render_widget(
                    Paragraph::new(tr(locale, MessageId::ConversationImportApplying))
                        .style(self.theme.focus()),
                    chunks[1],
                );
            }
            ConversationImportStage::Results => {
                let rows = import
                    .receipts()
                    .iter()
                    .enumerate()
                    .map(|(index, receipt)| {
                        let source = conversation_import_source_label(&receipt.source);
                        let status = match receipt.status.as_str() {
                            "imported" => tr_format(
                                locale,
                                MessageId::ConversationImportResultImported,
                                &[("count", &receipt.imported_messages.to_string())],
                            ),
                            "updated" => tr_format(
                                locale,
                                MessageId::ConversationImportResultUpdated,
                                &[("count", &receipt.imported_messages.to_string())],
                            ),
                            "up_to_date" => {
                                tr(locale, MessageId::ConversationImportUpToDate).to_owned()
                            }
                            _ => tr_format(
                                locale,
                                MessageId::ConversationImportResultFailed,
                                &[("error", &receipt.error)],
                            ),
                        };
                        (index, format!("{source:<11} {status}"))
                    })
                    .collect::<Vec<_>>();
                self.render_indexed_rows(
                    frame,
                    chunks[1],
                    rows,
                    import.result_selected(),
                    3_500,
                    |index| Some(Action::SelectConversationImportResult(index)),
                );
                self.render_conversation_import_footer(
                    frame,
                    chunks[2],
                    &[
                        (MessageId::Open, Action::OpenConversationImportResult),
                        (
                            MessageId::ConversationImportCheckAgain,
                            Action::RetryConversationImport,
                        ),
                        (MessageId::Done, Action::CancelConversationImport),
                    ],
                );
            }
            ConversationImportStage::Closed => {}
        }

        if let Some(detail) = layout.detail {
            self.render_conversation_import_detail(frame, detail);
        }
    }

    fn render_conversation_import_footer(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        actions: &[(MessageId, Action)],
    ) {
        let locale = self.ui_locale();
        let constraints = actions
            .iter()
            .map(|(label, _)| Constraint::Length(label_button_width(tr(locale, *label)).min(24)))
            .collect::<Vec<_>>();
        let areas = Layout::horizontal(constraints).split(area);
        for (index, ((label, action), area)) in actions.iter().zip(areas.iter()).enumerate() {
            let component = ComponentId(3_200 + index as u64);
            frame.render_widget(
                Paragraph::new(tr(locale, *label)).style(if self.interactions.hovered(component) {
                    self.theme.selected()
                } else {
                    self.theme.focus()
                }),
                *area,
            );
            self.interactions.register(HitRegion {
                component,
                area: *area,
                action: action.clone(),
            });
        }
    }

    fn render_conversation_import_detail(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let import = self.session_browser.conversation_import();
        let area = self.render_browser_panel(
            frame,
            area,
            &format!(" {} ", tr(locale, MessageId::ConversationPreview)),
        );
        let mut lines = Vec::new();
        if import.stage() == ConversationImportStage::Selecting
            && let Some(candidate) = import.candidates().get(import.selected_visible())
        {
            lines.extend([
                Line::from(Span::styled(
                    candidate.title.clone(),
                    self.theme.focus().add_modifier(Modifier::BOLD),
                )),
                Line::from(""),
                Line::from(format!(
                    "{}  {}",
                    tr(locale, MessageId::Provider),
                    conversation_import_source_label(&candidate.source)
                )),
                Line::from(format!(
                    "{}  {}",
                    tr(locale, MessageId::ConversationImportSourceWorkspace),
                    candidate.workspace_root
                )),
                Line::from(format!(
                    "{}  {}",
                    tr(locale, MessageId::ConversationImportTargetWorkspace),
                    if candidate.target_workspace.is_empty() {
                        tr(locale, MessageId::ConversationImportUnavailable)
                    } else {
                        candidate.target_workspace.as_str()
                    }
                )),
                Line::from(tr_format(
                    locale,
                    MessageId::ConversationImportMessageCount,
                    &[("count", &candidate.message_count.to_string())],
                )),
            ]);
            if !candidate.updated_at.is_empty() {
                lines.push(Line::from(tr_format(
                    locale,
                    MessageId::ConversationImportUpdatedAt,
                    &[("time", &candidate.updated_at)],
                )));
            }
            if !candidate.import_error.is_empty() {
                lines.push(Line::from(""));
                lines.push(Line::from(Span::styled(
                    candidate.import_error.clone(),
                    Style::default().fg(self.theme.danger),
                )));
            }
        }
        if !import.error().is_empty() {
            lines.push(Line::from(""));
            lines.push(Line::from(Span::styled(
                import.error().to_owned(),
                Style::default().fg(self.theme.danger),
            )));
        }
        if let Some(warning) = import.warnings().first() {
            lines.push(Line::from(""));
            lines.push(Line::from(Span::styled(
                warning.clone(),
                Style::default().fg(self.theme.warning),
            )));
        }
        frame.render_widget(Paragraph::new(lines).wrap(Wrap { trim: false }), area);
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
                let back_label = tr(locale, self.model_back_destination().message_id());
                let mut shortcuts = vec![
                    ("Esc", back_label),
                    ("P", tr(locale, MessageId::Provider)),
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
            Phase::Session => {
                let import = self.session_browser.conversation_import();
                match import.stage() {
                    ConversationImportStage::Closed => vec![
                        ("/", tr(locale, MessageId::Search)),
                        ("Tab", tr(locale, MessageId::Scope)),
                        ("N", tr(locale, MessageId::NewConversation)),
                        ("I", tr(locale, MessageId::ImportConversations)),
                        ("Enter", tr(locale, MessageId::Open)),
                        ("Esc", tr(locale, MessageId::Back)),
                    ],
                    ConversationImportStage::Selecting if !import.error().is_empty() => vec![
                        ("R", tr(locale, MessageId::ConversationImportCheckAgain)),
                        ("Esc", tr(locale, MessageId::Cancel)),
                    ],
                    ConversationImportStage::Selecting if area.width < 100 => vec![
                        (
                            "Space",
                            tr(locale, MessageId::ConversationImportToggleSelection),
                        ),
                        ("Enter", tr(locale, MessageId::ConversationImportReview)),
                        ("Esc", tr(locale, MessageId::Cancel)),
                    ],
                    ConversationImportStage::Selecting => vec![
                        (
                            "Space",
                            tr(locale, MessageId::ConversationImportToggleSelection),
                        ),
                        ("A", tr(locale, MessageId::ConversationImportSelectAll)),
                        ("S", tr(locale, MessageId::ConversationImportSource)),
                        ("Tab", tr(locale, MessageId::Scope)),
                        ("Enter", tr(locale, MessageId::ConversationImportReview)),
                        ("Esc", tr(locale, MessageId::Cancel)),
                    ],
                    ConversationImportStage::Confirming => vec![
                        ("Enter", tr(locale, MessageId::ConfirmImport)),
                        ("Esc", tr(locale, MessageId::Back)),
                    ],
                    ConversationImportStage::Results => vec![
                        ("Enter", tr(locale, MessageId::Open)),
                        ("R", tr(locale, MessageId::ConversationImportCheckAgain)),
                        ("Esc", tr(locale, MessageId::Done)),
                    ],
                    ConversationImportStage::Discovering | ConversationImportStage::Applying => {
                        vec![("Esc", tr(locale, MessageId::Cancel))]
                    }
                }
            }
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
        let notice_text = self.notice.render(self.ui_locale(), self.theme.glyphs);
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
        let reading_axis = layout_contract::transcript_content(content);
        let chrome = self.composer_chrome();
        let chrome_height = chrome.row_count(reading_axis.width);
        // A short terminal drops the identity bar before it compresses the
        // minimum transcript or the input track.
        let header_height = if self.options.screen_mode == Some(ScreenMode::Inline)
            || content.height
                < layout_contract::CONVERSATION_HEADER_HEIGHT
                    + layout_contract::CONVERSATION_MIN_TRANSCRIPT_HEIGHT
                    + chrome_height
                    + layout_contract::COMPOSER_MIN_HEIGHT
                    + layout_contract::COMPOSER_CHROME_ROWS
        {
            0
        } else {
            layout_contract::CONVERSATION_HEADER_HEIGHT
        };
        let preferred_composer_height = self
            .composer
            .desired_height(
                reading_axis
                    .width
                    .saturating_sub(layout_contract::COMPOSER_PROMPT_COLUMNS),
            )
            .clamp(
                layout_contract::COMPOSER_MIN_HEIGHT,
                layout_contract::COMPOSER_MAX_HEIGHT,
            )
            + layout_contract::COMPOSER_CHROME_ROWS;
        let composer_height = preferred_composer_height.min(
            content
                .height
                .saturating_sub(
                    header_height
                        + chrome_height
                        + layout_contract::CONVERSATION_MIN_TRANSCRIPT_HEIGHT,
                )
                .max(layout_contract::COMPOSER_MIN_HEIGHT + layout_contract::COMPOSER_CHROME_ROWS),
        );
        // A conversation is an operating surface, not a repeated welcome
        // screen. Keep its context bar compact at every terminal size.
        let chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(header_height),
                Constraint::Min(layout_contract::CONVERSATION_MIN_TRANSCRIPT_HEIGHT),
                Constraint::Length(chrome_height),
                Constraint::Length(composer_height),
            ])
            .split(content);
        if header_height > 0 {
            self.render_conversation_header(frame, chunks[0]);
        }
        self.render_transcript(frame, chunks[1]);
        self.render_composer_chrome_projection(
            frame,
            reading_axis.intersection(chunks[2]),
            &chrome,
        );
        self.render_composer(frame, reading_axis.intersection(chunks[3]));
        let completion_axis = reading_axis.intersection(chunks[1]);
        self.render_slash_completion(frame, completion_axis);
        self.render_context_completion(frame, completion_axis);
        self.render_prompt_history_search(frame, completion_axis);
    }

    fn render_conversation_header(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let locale = self.ui_locale();
        let title = conversation_title(
            self.active_session.as_ref(),
            &self.options.workspace,
            self.ui_locale(),
        );
        let (_, model, reasoning) = self.product_header_metadata();
        let model_route = format!("{model}  {}  {reasoning}", self.theme.glyphs.separator());
        let mode = if self
            .active_session
            .as_ref()
            .is_some_and(|session| session.plan_mode)
        {
            tr(locale, MessageId::ModePlanLabel)
        } else {
            tr(locale, MessageId::ModeBuildLabel)
        };
        let mut actions = Vec::new();
        if self
            .active_session
            .as_ref()
            .is_some_and(|session| session.execution_status == "paused")
        {
            actions.push(ConversationHeaderAction {
                label: if self.paused_resume_blocker().is_some() {
                    tr(locale, MessageId::Review)
                } else {
                    tr(locale, MessageId::Resume)
                },
                action: Action::ResumePausedExecutionRun,
                component: ComponentId(9_105),
                tone: ConversationHeaderTone::Blocking,
            });
        }
        actions.push(ConversationHeaderAction {
            label: mode,
            action: Action::TogglePlanMode,
            component: ComponentId(9_106),
            tone: ConversationHeaderTone::Active,
        });
        actions.push(ConversationHeaderAction {
            label: &model_route,
            action: Action::OpenModels,
            component: ComponentId(9_102),
            tone: ConversationHeaderTone::Muted,
        });
        let product_menu_anchor = ConversationHeader {
            title: &title,
            product_menu_open: matches!(self.overlays.active(), Some(Overlay::ProductMenu(_))),
            actions: &actions,
        }
        .render(frame, area, self.theme, &mut self.interactions);
        self.product_menu_anchor = product_menu_anchor;
    }

    fn render_product_menu_overlay(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        menu: &ProductMenuOverlay,
    ) {
        let Some(anchor) = self.product_menu_anchor else {
            return;
        };
        let width = layout_contract::PRODUCT_MENU_WIDTH.min(area.width);
        let popup_y = anchor
            .bottom()
            .saturating_add(layout_contract::PRODUCT_MENU_SEPARATOR_ROWS);
        let height = layout_contract::PRODUCT_MENU_HEIGHT.min(
            area.bottom()
                .saturating_sub(popup_y)
                .max(layout_contract::ROW_HEIGHT),
        );
        let x = anchor.x.min(area.right().saturating_sub(width)).max(area.x);
        let popup = Rect::new(x, popup_y, width, height);

        for (index, backdrop) in outside_rects(area, popup).into_iter().enumerate() {
            self.interactions.register(HitRegion {
                component: ComponentId::stable("product-menu-backdrop", &index.to_string()),
                area: backdrop,
                action: Action::CloseOverlay,
            });
        }
        for (index, border) in border_rects(popup).into_iter().enumerate() {
            self.interactions.register(HitRegion {
                component: ComponentId::stable("product-menu-border", &index.to_string()),
                area: border,
                action: Action::ToggleProductMenu,
            });
        }
        self.interactions.register(HitRegion {
            component: ComponentId::stable("conversation-header", "product-menu"),
            area: anchor,
            action: Action::ToggleProductMenu,
        });

        frame.render_widget(Clear, popup);
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(self.theme.focus())
            .title(format!(
                " {} ",
                tr(self.ui_locale(), MessageId::ProductMenu)
            ))
            .style(Style::default());
        let inner = block.inner(popup);
        frame.render_widget(block, popup);

        for (index, item) in PRODUCT_MENU_ITEMS.iter().enumerate() {
            if index >= inner.height as usize {
                break;
            }
            let row = Rect::new(
                inner.x,
                inner.y + index as u16,
                inner.width,
                layout_contract::ROW_HEIGHT,
            );
            let component = ComponentId::stable("product-menu-item", item.id);
            let selected = index == menu.selected;
            let style = if selected {
                self.theme.selected()
            } else if self.interactions.hovered(component) {
                self.theme.hovered()
            } else {
                Style::default().fg(self.theme.text)
            };
            let prefix = if selected {
                self.theme.glyphs.selected()
            } else {
                "  "
            };
            frame.render_widget(
                Paragraph::new(format!("{prefix}{}", tr(self.ui_locale(), item.label)))
                    .style(style),
                row,
            );
            self.interactions.register(HitRegion {
                component,
                area: row,
                action: item.action.clone(),
            });
        }
    }

    fn render_transcript(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let geometry = layout_contract::TranscriptGeometry::compute(area);
        self.transcript_geometry = geometry;
        self.transcript_scrollbar = layout_contract::TranscriptScrollbar::default();
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
                geometry.content,
            );
            return;
        }
        if self.blocks.is_empty() {
            EmptyConversation {
                workspace: &self.options.workspace,
                locale: self.ui_locale(),
            }
            .render(frame, geometry.content, self.theme);
            return;
        }
        let committed = self.scrollback.committed_prefix_len(&self.blocks);
        let projected_blocks = self.blocks[committed..]
            .iter()
            .cloned()
            .map(|mut block| {
                block.expanded = self.effective_block_expanded(&block);
                if let Some(failure) = block.failure.as_mut() {
                    failure.current_model.clone_from(&self.selected_model);
                    failure.focused_action = self
                        .failure_action_focus
                        .as_ref()
                        .filter(|focus| focus.block_id == block.id)
                        .map(|focus| focus.selected)
                        .or_else(|| {
                            failure
                                .available_actions(&self.selected_model)
                                .into_iter()
                                .find(|action| {
                                    self.interactions
                                        .hovered(failure_action_component(&block.id, *action))
                                })
                        });
                }
                block
            })
            .collect::<Vec<_>>();
        let visible_blocks = projected_blocks.as_slice();
        let locale = self.ui_locale();
        let selection_anchor = self
            .history_selected
            .and_then(|index| index.checked_sub(committed));
        let block_width = geometry.content.width;
        let transcript_x = geometry.content.x;
        let content_width = block_width.max(layout_contract::MIN_DIMENSION);
        let expanded_output_budget = expanded_tool_output_budget(geometry.content.height as usize);
        let transcript_viewport = TranscriptViewport {
            locale,
            density: self.density,
            glyph_mode: self.theme.glyphs.mode,
            content_width,
            tool_expand_key: self.keybindings.expand_tools.label(),
            tool_inspect_key: self.keybindings.inspect_tool_output.label(),
            expanded_output_budget,
            height: geometry.content.height as usize,
            requested_scroll: self.transcript_scroll,
            follow_bottom: self.transcript_follow_bottom,
            selection_anchor,
            scroll_anchor: self.transcript_anchor.clone(),
        };
        let layout = TranscriptLayout::new(
            visible_blocks,
            transcript_viewport.clone(),
            &mut self.transcript_height_cache,
        );
        self.bounded_tool_output_blocks.clear();
        let mut visible_tool_output_candidates = Vec::new();
        self.transcript_scroll = layout.viewport_start;
        self.transcript_max_scroll = layout.max_scroll;
        if selection_anchor.is_none() {
            self.transcript_anchor = layout.capture_anchor(visible_blocks, transcript_viewport);
        }
        let viewport_end = layout
            .viewport_start
            .saturating_add(geometry.content.height as usize);

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
                geometry.content.y.saturating_add(
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
            let render_options = TranscriptRenderOptions {
                locale,
                density: self.density,
                glyph_mode: self.theme.glyphs.mode,
                content_width,
                tool_expand_key: self.keybindings.expand_tools.label(),
                tool_inspect_key: self.keybindings.inspect_tool_output.label(),
                expanded_output_budget,
            };
            let mut lines = if dimmed || block.failure.is_some() {
                transcript_lines_with_tool_key_and_density(
                    block,
                    locale,
                    transcript_styles,
                    content_width,
                    render_options,
                )
            } else {
                cached_transcript_lines_with_tool_key_and_density(
                    &mut self.transcript_render_cache,
                    block,
                    locale,
                    transcript_styles,
                    content_width,
                    render_options,
                )
            };
            let tool_output_component = ComponentId::stable("tool-output-open", &block.id);
            let tool_output_receipt_line = bounded_tool_output_receipt_line(
                block,
                ToolLineContext {
                    locale,
                    density: self.density,
                    styles: transcript_styles,
                    content_width,
                    expand_key: self.keybindings.expand_tools.label(),
                    inspect_key: self.keybindings.inspect_tool_output.label(),
                    expanded_output_budget,
                },
            );
            let artifact_header_opens_tool_output = tool_output_receipt_line.is_none()
                && block.kind == BlockKind::Tool
                && super::tool_component_call_ids(block)
                    .iter()
                    .any(|call_id| self.tool_artifact_refs.contains_key(*call_id));
            if let Some(receipt_line) = tool_output_receipt_line
                && self.interactions.hovered(tool_output_component)
                && let Some(line) = lines.get_mut(receipt_line)
            {
                for span in &mut line.spans {
                    span.style = span.style.patch(self.theme.hovered());
                }
            }
            if artifact_header_opens_tool_output
                && self.interactions.hovered(tool_output_component)
                && let Some(header) = lines.first_mut()
            {
                for span in &mut header.spans {
                    span.style = span.style.patch(self.theme.hovered());
                }
            }
            if hovered
                && block.is_collapsible()
                && block.failure.is_none()
                && self.history_selected.is_none()
                && let Some(header) = lines.first_mut()
            {
                let hover_style = self.theme.hovered();
                for span in &mut header.spans {
                    span.style = span.style.patch(hover_style);
                }
            }
            let style = if block.selected {
                self.theme.selected().add_modifier(Modifier::BOLD)
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
            let rendered_line_count = lines.len();
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
            if let Some(failure) = block.failure.as_ref() {
                let action_line_top = item
                    .top
                    .saturating_add(rendered_line_count.saturating_sub(1));
                if action_line_top >= layout.viewport_start && action_line_top < viewport_end {
                    let y = geometry.content.y.saturating_add(
                        action_line_top
                            .saturating_sub(layout.viewport_start)
                            .min(u16::MAX as usize) as u16,
                    );
                    let gutter_width = UnicodeWidthStr::width(self.theme.glyphs.tool_gutter());
                    for action in failure_action_layout(locale, failure, block.expanded) {
                        let start = gutter_width.saturating_add(action.start_cell);
                        if start >= usize::from(block_width) {
                            continue;
                        }
                        let width = action
                            .width
                            .min(usize::from(block_width).saturating_sub(start));
                        let Some(action_value) = Self::failure_action_for(block, action.action)
                        else {
                            continue;
                        };
                        self.interactions.register(HitRegion {
                            component: failure_action_component(&block.id, action.action),
                            area: Rect::new(
                                transcript_x.saturating_add(start.min(u16::MAX as usize) as u16),
                                y,
                                width.min(u16::MAX as usize) as u16,
                                1,
                            ),
                            action: action_value,
                        });
                    }
                }
            }
            if let Some(action) =
                transcript_click_action(block, index, self.history_selected.is_some())
            {
                let action_area = if matches!(&action, Action::ToggleBlock(id) if id == &block.id) {
                    visible_transcript_rect(
                        geometry.content,
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
            if let Some(line_index) = tool_output_receipt_line {
                let receipt_top = item
                    .top
                    .saturating_add(item.header_height)
                    .saturating_add(line_index.saturating_sub(1));
                if let Some(action_area) = visible_transcript_rect(
                    geometry.content,
                    block_width,
                    layout.viewport_start,
                    viewport_end,
                    receipt_top,
                    receipt_top.saturating_add(1),
                ) {
                    visible_tool_output_candidates.push(VisibleToolOutputCandidate {
                        block_id: block.id.clone(),
                        hovered: self.interactions.hovered(tool_output_component),
                        selected: block.selected,
                        receipt_top,
                    });
                    self.interactions.register(HitRegion {
                        component: tool_output_component,
                        area: action_area,
                        action: Action::OpenToolOutput(block.id.clone()),
                    });
                }
            } else if artifact_header_opens_tool_output
                && let Some(action_area) = visible_transcript_rect(
                    geometry.content,
                    block_width,
                    layout.viewport_start,
                    viewport_end,
                    item.top,
                    item.top.saturating_add(item.header_height),
                )
            {
                visible_tool_output_candidates.push(VisibleToolOutputCandidate {
                    block_id: block.id.clone(),
                    hovered: self.interactions.hovered(tool_output_component),
                    selected: block.selected,
                    receipt_top: item.top,
                });
                self.interactions.register(HitRegion {
                    component: tool_output_component,
                    area: action_area,
                    action: Action::OpenToolOutput(block.id.clone()),
                });
            }
        }
        prioritize_visible_tool_output_candidates(
            &mut visible_tool_output_candidates,
            layout.viewport_start,
            geometry.content.height as usize,
        );
        self.bounded_tool_output_blocks = visible_tool_output_candidates
            .into_iter()
            .map(|candidate| candidate.block_id)
            .collect();

        self.transcript_scrollbar = layout_contract::TranscriptScrollbar::compute(
            geometry.scrollbar,
            layout.total_height,
            geometry.content.height as usize,
            layout.viewport_start,
        );
        if self.transcript_scrollbar.is_active() {
            let scrollbar = self.transcript_scrollbar;
            if scrollbar.start.height > 0 {
                frame.render_widget(
                    Paragraph::new(self.theme.glyphs.up()).style(self.theme.muted()),
                    scrollbar.start,
                );
            }
            frame.render_widget(
                Paragraph::new(vec![
                    Line::styled(
                        self.theme.glyphs.scroll_track(),
                        self.theme.muted()
                    );
                    scrollbar.track.height as usize
                ]),
                scrollbar.track,
            );
            frame.render_widget(
                Paragraph::new(vec![
                    Line::styled(
                        self.theme.glyphs.scroll_thumb(),
                        self.theme.focus()
                    );
                    scrollbar.thumb.height as usize
                ]),
                scrollbar.thumb,
            );
            if scrollbar.end.height > 0 {
                frame.render_widget(
                    Paragraph::new(self.theme.glyphs.down()).style(self.theme.muted()),
                    scrollbar.end,
                );
            }
        }
    }

    fn render_composer(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let focused = self.focus == Focus::Composer;
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
        let attachment_title =
            self.media
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
                });
        let component = ComponentId(9_000);
        let interactive = focused || self.interactions.hovered(component);
        let mut block = Block::default()
            .borders(Borders::TOP)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(if interactive {
                self.theme.focus()
            } else {
                Style::default().fg(self.theme.border)
            });
        if let Some(title) = attachment_title {
            block = block.title(title).title_alignment(Alignment::Right);
        }
        let inner = block.inner(area);
        frame.render_widget(block, area);
        let prompt_width = layout_contract::COMPOSER_PROMPT_COLUMNS.min(inner.width);
        let input = Rect::new(
            inner.x.saturating_add(prompt_width),
            inner.y,
            inner.width.saturating_sub(prompt_width),
            inner.height,
        );
        if inner.height > 0 && prompt_width > 0 {
            let prompt_style = if focused {
                if self.theme.no_color {
                    self.theme.selected()
                } else {
                    self.theme.focus()
                }
            } else if self.interactions.hovered(component) {
                self.theme.hovered()
            } else {
                self.theme.muted()
            };
            frame.render_widget(
                Paragraph::new(self.theme.glyphs.prompt()).style(prompt_style),
                Rect::new(inner.x, inner.y, prompt_width, 1),
            );
        }
        StatefulWidgetRef::render_ref(
            &&self.composer,
            input,
            frame.buffer_mut(),
            &mut self.composer_state,
        );
        self.composer_area = input;
        self.interactions.register(HitRegion {
            component,
            area,
            action: Action::FocusComposer,
        });
        for row in input.y..input.bottom() {
            for column in input.x..input.right() {
                if let Some(element) = self
                    .composer
                    .element_at_screen(column, row, input, self.composer_state)
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
                .cursor_pos_with_state(input, self.composer_state)
        {
            frame.set_cursor_position(Position::new(x, y));
        }
        self.render_media_preview(frame, input, focused);
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
        let Some(area) = media_preview_rect(
            self.transcript_geometry.viewport,
            cursor,
            preview_width,
            preview_height,
        ) else {
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
            Paragraph::new(truncate_cells(
                &status,
                inner.width as usize,
                self.theme.glyphs,
            ))
            .style(Style::default().fg(
                if self.context_completion.error().is_some() {
                    self.theme.warning
                } else {
                    self.theme.muted
                },
            )),
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
                        format!(
                            "{prefix}{}",
                            truncate_cells(&candidate.path, available, self.theme.glyphs)
                        ),
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
                    self.theme.glyphs,
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
                self.theme.glyphs,
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
                    self.theme.glyphs,
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
                            self.theme.glyphs,
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
                self.theme.glyphs,
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

    fn composer_chrome(&self) -> ComposerChrome {
        let notice = self.notice.render(self.ui_locale(), self.theme.glyphs);
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
        let activity = self.execution_activity.current();
        ComposerChrome::project(ComposerChromeInput {
            execution_status: &self.execution_status,
            notice: notice.as_ref(),
            elapsed: self.execution_timer.elapsed(),
            activity: activity.map(|(label, _)| label),
            activity_elapsed: activity.map(|(_, elapsed)| elapsed),
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
        })
    }

    #[cfg(test)]
    fn render_composer_chrome(&mut self, frame: &mut Frame<'_>, area: Rect) {
        let chrome = self.composer_chrome();
        self.render_composer_chrome_projection(frame, area, &chrome);
    }

    fn render_composer_chrome_projection(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        chrome: &ComposerChrome,
    ) {
        if area.height == 0 || area.width == 0 {
            return;
        }

        let activity = self.execution_activity.current();

        let mut primary_spans = Vec::new();
        if let Some(primary) = &chrome.primary {
            if primary.animated {
                primary_spans.push(Span::styled(
                    format!(
                        "{} ",
                        self.theme.glyphs.activity_spinner(
                            activity
                                .map(|(_, elapsed)| elapsed)
                                .or_else(|| self.execution_timer.elapsed())
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
                    self.theme.glyphs,
                ),
                chrome_tone_style(primary.tone, self.theme),
            ));
        }

        let slots = chrome.layout_slots(area.width, self.theme.glyphs);
        let separator = format!(" {} ", self.theme.glyphs.separator());
        let mut slot_spans = Vec::new();
        let slot_row = area.y.saturating_add(u16::from(chrome.primary.is_some()));
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
        let mut rows = Vec::with_capacity(2);
        if chrome.primary.is_some() {
            rows.push(Line::from(primary_spans));
        }
        if !slots.is_empty() {
            rows.push(Line::from(slot_spans));
        }
        frame.render_widget(Paragraph::new(rows), area);
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
        let selected_provider = if self.phase == Phase::Conversation {
            self.inventory.providers.iter().find(|provider| {
                provider
                    .models
                    .iter()
                    .any(|model| model.id == self.selected_model)
            })
        } else {
            self.inventory.providers.get(self.provider_index)
        };
        let selected_provider_label = selected_provider
            .map(|provider| self.provider_display_label(provider))
            .unwrap_or_else(|| tr(locale, MessageId::ProviderNotSelected).to_owned());
        let next_model_label = selected_model
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
        let next_reasoning_label = if selected_model.is_none() && selected_effort.is_empty() {
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
        if self.phase != Phase::Conversation || self.active_run_presentation.run_id.is_empty() {
            return (
                selected_provider_label,
                next_model_label,
                next_reasoning_label,
            );
        }

        let running_model = self.active_run_presentation.actual_model().trim();
        let run_state_label = if matches!(self.execution_status.as_str(), "paused" | "interrupted")
        {
            tr(locale, MessageId::StatusPaused)
        } else {
            tr(locale, MessageId::StatusRunning)
        };
        let running_provider = self.active_run_presentation.provider.trim();
        let running_provider_label = if !running_provider.is_empty() {
            self.inventory
                .providers
                .iter()
                .find(|provider| {
                    provider.id == running_provider
                        || provider.name.eq_ignore_ascii_case(running_provider)
                })
                .map(|provider| self.provider_display_label(provider))
                .or_else(|| Some(running_provider.to_owned()))
        } else if !running_model.is_empty() {
            self.provider_for_model(running_model)
                .map(|provider| self.provider_display_label(provider))
        } else {
            None
        };
        let provider_owns_next_label = running_provider_label.is_none();
        let provider_label = running_provider_label.unwrap_or_else(|| {
            format!(
                "{} {selected_provider_label}",
                tr(locale, MessageId::NextRun)
            )
        });
        let model_label = if running_model.is_empty() {
            if provider_owns_next_label {
                next_model_label
            } else {
                format!("{} {next_model_label}", tr(locale, MessageId::NextRun))
            }
        } else {
            let running_label = self.model_display_label(running_model);
            if running_model == self.selected_model {
                format!("{run_state_label} {running_label}")
            } else if provider_owns_next_label {
                format!("{run_state_label} {running_label}  {sep}  {next_model_label}")
            } else {
                format!(
                    "{} {running_label}  {sep}  {} {next_model_label}",
                    run_state_label,
                    tr(locale, MessageId::NextRun)
                )
            }
        };

        let running_effort = self
            .active_run_presentation
            .actual_reasoning_effort()
            .trim();
        let next_effort = (!selected_effort.is_empty())
            .then_some(selected_effort)
            .or(session_effort)
            .or(default_effort)
            .unwrap_or_default();
        let next_reasoning_value = if next_effort.is_empty() {
            next_reasoning_label.as_str()
        } else {
            next_effort
        };
        let reasoning_label = if running_effort.is_empty() {
            next_reasoning_value.to_owned()
        } else if !next_effort.is_empty() && running_effort != next_effort {
            format!("{running_effort}  {sep}  {next_reasoning_value}")
        } else {
            running_effort.to_owned()
        };
        (provider_label, model_label, reasoning_label)
    }

    fn provider_for_model(&self, model_id: &str) -> Option<&ModelProvider> {
        self.inventory
            .providers
            .iter()
            .find(|provider| provider.models.iter().any(|model| model.id == model_id))
    }

    fn provider_display_label(&self, provider: &ModelProvider) -> String {
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
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
    }

    fn model_display_label(&self, model_id: &str) -> String {
        self.models
            .iter()
            .chain(
                self.inventory
                    .providers
                    .iter()
                    .flat_map(|provider| provider.models.iter()),
            )
            .find(|model| model.id == model_id)
            .map(|model| {
                first_non_empty(
                    &model.display_id,
                    &model.name,
                    model.id.rsplit('/').next().unwrap_or(model_id),
                )
                .to_owned()
            })
            .filter(|label| !label.is_empty())
            .unwrap_or_else(|| model_id.to_owned())
    }

    fn render_overlay(&mut self, frame: &mut Frame<'_>, area: Rect, overlay: &Overlay) {
        let locale = self.ui_locale();
        match overlay {
            Overlay::ProductMenu(menu) => self.render_product_menu_overlay(frame, area, menu),
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
                frame.render_widget(
                    Clear,
                    Rect::new(
                        area.x,
                        popup.bottom(),
                        area.width,
                        area.bottom().saturating_sub(popup.bottom()),
                    ),
                );
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
            Overlay::Settings(settings) => self.render_settings_overlay(frame, area, settings),
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
                    tr(self.ui_locale(), MessageId::Shortcuts),
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
                    tr(self.ui_locale(), MessageId::Commands),
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
                    Paragraph::new(truncate_cells(
                        &search,
                        chunks[0].width as usize,
                        self.theme.glyphs,
                    ))
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
                                    Span::styled(
                                        truncate_cells(content, available, self.theme.glyphs),
                                        style,
                                    ),
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
            Overlay::ToolOutput(output) => {
                self.render_tool_output_overlay(frame, area, output);
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
                            truncate_cells(&row, chunks[1].width as usize, self.theme.glyphs),
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

    fn render_tool_output_overlay(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        overlay: &crate::overlay::ToolOutputOverlay,
    ) {
        let locale = self.ui_locale();
        frame.render_widget(Clear, area);
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(self.theme.focus())
            .title(format!(" {} ", tr(locale, MessageId::ToolOutputTitle)));
        let inner = block.inner(area).inner(Margin::new(1, 0));
        frame.render_widget(block, area);
        let layout = tool_output_overlay_layout(inner);

        let resolved = self
            .blocks
            .iter()
            .position(|block| block.id == overlay.block_id)
            .map(|index| {
                let block = &self.blocks[index];
                let prompt = nearest_owning_prompt(&self.blocks, index, &block.run_id);
                (block, prompt, complete_tool_output(block))
            });
        let mut retained_status = None;
        let mut row_position = None;
        if let Some((block, prompt, output)) = resolved {
            retained_status = self.tool_output_retained_status(block);
            let title = block.localized_title(locale);
            let context = vec![
                Line::from(vec![
                    Span::styled(
                        tr(locale, MessageId::ToolOutputPrompt),
                        self.theme.transcript_metadata(),
                    ),
                    Span::styled(
                        format!("  {title}"),
                        self.theme.focus().add_modifier(Modifier::BOLD),
                    ),
                ]),
                Line::from(Span::styled(
                    truncate_cells(prompt, layout.context.width as usize, self.theme.glyphs),
                    Style::default().fg(self.theme.text),
                )),
            ];
            if layout.context.height > 0 {
                frame.render_widget(
                    Paragraph::new(context).wrap(Wrap { trim: false }).block(
                        Block::default()
                            .borders(Borders::BOTTOM)
                            .border_type(self.theme.glyphs.outer_border_type())
                            .border_style(Style::default().fg(self.theme.border)),
                    ),
                    layout.context,
                );
            }
            let window = tool_output_visual_window(
                output.as_ref(),
                layout.body.width as usize,
                layout.body.height as usize,
                overlay.scroll,
            );
            self.tool_output_max_scroll = window.max_scroll;
            self.sync_tool_output_scroll(&overlay.block_id, window.scroll);
            row_position = Some(tool_output_row_position(
                &window,
                layout.body.height as usize,
            ));
            frame.render_widget(
                Paragraph::new(window.lines).style(Style::default().fg(self.theme.text)),
                layout.body,
            );
        } else {
            self.tool_output_max_scroll = 0;
            self.sync_tool_output_scroll(&overlay.block_id, 0);
            frame.render_widget(
                Paragraph::new(tr(locale, MessageId::ToolOutputMissing))
                    .style(Style::default().fg(self.theme.muted)),
                layout.body,
            );
        }
        if layout.footer.height > 0 {
            match retained_status {
                Some(ToolOutputRetainedStatus::Loading) => frame.render_widget(
                    Paragraph::new(tr(locale, MessageId::ToolOutputLoading))
                        .style(Style::default().fg(self.theme.muted)),
                    layout.footer,
                ),
                Some(ToolOutputRetainedStatus::Failed(error)) => frame.render_widget(
                    Paragraph::new(tr_format(
                        locale,
                        MessageId::ToolOutputLoadFailed,
                        &[("error", error.as_str())],
                    ))
                    .style(Style::default().fg(self.theme.danger)),
                    layout.footer,
                ),
                None => frame.render_widget(
                    Paragraph::new(tool_output_footer_line(
                        tr(locale, MessageId::ToolOutputHint),
                        row_position.as_deref(),
                        layout.footer.width as usize,
                    ))
                    .style(Style::default().fg(self.theme.muted)),
                    layout.footer,
                ),
            }
        }
    }

    fn tool_output_retained_status(
        &self,
        block: &TranscriptBlock,
    ) -> Option<ToolOutputRetainedStatus> {
        let mut error = None;
        for id in std::iter::once(block.id.as_str())
            .chain(block.tool_members.iter().map(|member| member.id.as_str()))
        {
            let call_id = id.strip_prefix("tool:").unwrap_or(id);
            let Some(load) = self.tool_artifact_loads.get(call_id) else {
                continue;
            };
            if load.loading {
                return Some(ToolOutputRetainedStatus::Loading);
            }
            if error.is_none() && !load.error.is_empty() {
                error = Some(load.error.clone());
            }
        }
        error.map(ToolOutputRetainedStatus::Failed)
    }

    fn sync_tool_output_scroll(&mut self, block_id: &str, scroll: usize) {
        if let Some(Overlay::ToolOutput(active)) = self.overlays.active_mut()
            && active.block_id == block_id
        {
            active.scroll = scroll;
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

    fn render_settings_overlay(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        settings: &SettingsOverlay,
    ) {
        let locale = self.ui_locale();
        let popup = centered(
            area,
            layout_contract::SETTINGS_POPUP.0,
            layout_contract::SETTINGS_POPUP.1,
        );
        let backdrop_y = popup.y.saturating_add(layout_contract::ROW_HEIGHT);
        frame.render_widget(
            Clear,
            Rect::new(
                area.x,
                backdrop_y,
                area.width,
                area.bottom().saturating_sub(backdrop_y),
            ),
        );
        frame.render_widget(Clear, popup);
        let title = match settings.page {
            SettingsPage::Root => tr(locale, MessageId::Settings).to_owned(),
            SettingsPage::Symbols => format!(
                "{} / {}",
                tr(locale, MessageId::Settings),
                tr(locale, MessageId::Symbols)
            ),
        };
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(self.theme.glyphs.outer_border_type())
            .border_style(self.theme.focus())
            .title(format!(" {title} "))
            .style(Style::default());
        let inner = block.inner(popup);
        frame.render_widget(block, popup);
        match settings.page {
            SettingsPage::Root => self.render_settings_root(frame, inner, settings),
            SettingsPage::Symbols => self.render_symbols_settings(frame, inner, settings),
        }
    }

    fn render_settings_root(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        settings: &SettingsOverlay,
    ) {
        let locale = self.ui_locale();
        let sep = self.theme.glyphs.separator();
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
        let symbol_preference = glyph_preference_label(locale, self.glyph_preference);
        let symbol_mode = glyph_mode_label(locale, self.glyph_resolution.mode);
        let symbol_value = if self.glyph_preference == GlyphPreference::Auto
            || self.glyph_resolution.source.is_environment_override()
        {
            format!("{symbol_preference} ({symbol_mode})")
        } else {
            symbol_preference.to_owned()
        };
        self.render_rows(
            frame,
            area.inner(Margin::new(1, 1)),
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
                format!("{}  {sep}  {symbol_value}", tr(locale, MessageId::Symbols)),
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

    fn render_symbols_settings(
        &mut self,
        frame: &mut Frame<'_>,
        area: Rect,
        settings: &SettingsOverlay,
    ) {
        let locale = self.ui_locale();
        let content = area.inner(Margin::new(1, 0));
        if content.height == 0 || content.width == 0 {
            return;
        }
        let compact = content.height <= 14;
        let guidance_height = if compact { 1 } else { 2 }.min(content.height);
        frame.render_widget(
            Paragraph::new(tr(locale, MessageId::SymbolsPreviewGuidance))
                .style(Style::default().fg(self.theme.muted))
                .wrap(Wrap { trim: false }),
            Rect::new(content.x, content.y, content.width, guidance_height),
        );

        let choices_y = content.y.saturating_add(guidance_height);
        let choices = Rect::new(
            content.x,
            choices_y,
            content.width,
            4.min(content.bottom().saturating_sub(choices_y)),
        );
        let detail_width = usize::from(choices.width).saturating_sub(
            self.theme.glyphs.selected().width() + "1  ".width() + 12 + "  ".width(),
        );
        self.render_rows(
            frame,
            choices,
            GlyphPreference::ALL
                .into_iter()
                .enumerate()
                .map(|(index, preference)| {
                    format!(
                        "{}  {}  {}",
                        index + 1,
                        pad_cells(
                            glyph_preference_label(locale, preference),
                            12,
                            self.theme.glyphs,
                        ),
                        truncate_cells(
                            glyph_preference_detail(locale, preference),
                            detail_width,
                            self.theme.glyphs,
                        )
                    )
                })
                .collect(),
            settings.symbol_selected,
            12_500,
            symbol_preview_action,
        );

        let sample_y = if compact {
            choices.bottom()
        } else {
            choices.bottom().saturating_add(1)
        }
        .min(content.bottom());
        if sample_y < content.bottom().saturating_sub(2) {
            let sample = Line::from(vec![
                Span::styled(
                    format!(
                        "{} {}",
                        self.theme.glyphs.success(),
                        tr(locale, MessageId::Done)
                    ),
                    Style::default().fg(self.theme.success),
                ),
                Span::styled("   ", Style::default()),
                Span::styled(
                    format!(
                        "{} {}",
                        self.theme.glyphs.ready(),
                        tr(locale, MessageId::ExecutionWorking)
                    ),
                    Style::default().fg(self.theme.accent),
                ),
                Span::styled("   ", Style::default()),
                Span::styled(
                    format!(
                        "{} {}",
                        self.theme.glyphs.warning(),
                        tr(locale, MessageId::Review)
                    ),
                    Style::default().fg(self.theme.warning),
                ),
                Span::styled("   ", Style::default()),
                Span::styled(
                    format!(
                        "{} {}",
                        self.theme.glyphs.failure(),
                        tr(locale, MessageId::StatusFailed)
                    ),
                    Style::default().fg(self.theme.danger),
                ),
            ]);
            frame.render_widget(
                Paragraph::new(sample),
                Rect::new(content.x, sample_y, content.width, 1),
            );
            if !compact && sample_y + 1 < content.bottom().saturating_sub(2) {
                frame.render_widget(
                    Paragraph::new(format!(
                        "{}{}  {}{}  {}{}  {}{}  {}{}",
                        self.theme.glyphs.selected(),
                        tr(locale, MessageId::Navigate),
                        self.theme.glyphs.disclosure_closed(),
                        tr(locale, MessageId::Close),
                        self.theme.glyphs.disclosure_open(),
                        tr(locale, MessageId::Open),
                        self.theme.glyphs.tree(),
                        tr(locale, MessageId::Status),
                        self.theme.glyphs.status_spinner(0),
                        tr(locale, MessageId::ExecutionWorking),
                    ))
                    .style(Style::default().fg(self.theme.muted)),
                    Rect::new(content.x, sample_y + 1, content.width, 1),
                );
            }
        }

        let footer_y = content.bottom().saturating_sub(1);
        let info_y = if compact {
            sample_y.saturating_add(1).min(footer_y)
        } else {
            sample_y.saturating_add(2).min(footer_y)
        };
        let info_height = footer_y.saturating_sub(info_y);
        if info_height > 0 {
            let preference = settings.symbol_preference();
            let mode = self.glyph_resolution.mode;
            let mut info = vec![Line::from(vec![
                Span::styled(
                    tr_format(
                        locale,
                        MessageId::SymbolsRequested,
                        &[("preference", glyph_preference_label(locale, preference))],
                    ),
                    Style::default().fg(self.theme.text),
                ),
                Span::styled(
                    format!("  {}  ", self.theme.glyphs.separator()),
                    Style::default().fg(self.theme.border),
                ),
                Span::styled(
                    tr_format(
                        locale,
                        MessageId::SymbolsEffective,
                        &[("mode", glyph_mode_label(locale, mode))],
                    ),
                    Style::default().fg(self.theme.accent),
                ),
            ])];
            if self.glyph_resolution.source.is_environment_override() {
                info.push(Line::from(Span::styled(
                    tr_format(
                        locale,
                        MessageId::SymbolsEnvironmentOverride,
                        &[("mode", glyph_mode_label(locale, mode))],
                    ),
                    Style::default().fg(self.theme.warning),
                )));
            } else if self.glyph_resolution.source == GlyphSource::AutomaticLegacyTerminal {
                info.push(Line::from(Span::styled(
                    tr(locale, MessageId::SymbolsAutoFallback),
                    Style::default().fg(self.theme.warning),
                )));
            }
            frame.render_widget(
                Paragraph::new(info)
                    .wrap(Wrap { trim: false })
                    .style(Style::default().fg(self.theme.muted)),
                Rect::new(content.x, info_y, content.width, info_height),
            );
        }

        self.render_modal_buttons(
            frame,
            Rect::new(content.x, footer_y, content.width, 1),
            &[
                (
                    tr(locale, MessageId::SymbolsApplyHint),
                    Action::ApplyGlyphPreference,
                    12_600,
                ),
                (
                    tr(locale, MessageId::SymbolsKeepCurrentHint),
                    Action::CancelGlyphPreview,
                    12_601,
                ),
            ],
        );
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
                Paragraph::new(truncate_cells(
                    &summary,
                    columns[0].width as usize,
                    self.theme.glyphs,
                ))
                .style(style),
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

fn glyph_preference_label(locale: Locale, preference: GlyphPreference) -> &'static str {
    tr(
        locale,
        match preference {
            GlyphPreference::Auto => MessageId::SymbolsAutomatic,
            GlyphPreference::Unicode => MessageId::SymbolsUnicode,
            GlyphPreference::Nerd => MessageId::SymbolsNerdFont,
            GlyphPreference::Ascii => MessageId::SymbolsAscii,
        },
    )
}

fn glyph_preference_detail(locale: Locale, preference: GlyphPreference) -> &'static str {
    tr(
        locale,
        match preference {
            GlyphPreference::Auto => MessageId::SymbolsAutomaticDetail,
            GlyphPreference::Unicode => MessageId::SymbolsUnicodeDetail,
            GlyphPreference::Nerd => MessageId::SymbolsNerdFontDetail,
            GlyphPreference::Ascii => MessageId::SymbolsAsciiDetail,
        },
    )
}

fn glyph_mode_label(locale: Locale, mode: GlyphMode) -> &'static str {
    tr(
        locale,
        match mode {
            GlyphMode::Unicode => MessageId::SymbolsUnicode,
            GlyphMode::Nerd => MessageId::SymbolsNerdFont,
            GlyphMode::Ascii => MessageId::SymbolsAscii,
        },
    )
}

fn symbol_preview_action(index: usize) -> Option<Action> {
    GlyphPreference::ALL
        .get(index)
        .copied()
        .map(Action::PreviewGlyphPreference)
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

fn grok_build_state_label(locale: Locale, action: &str) -> &'static str {
    tr(
        locale,
        match action {
            "login_cli" => MessageId::GrokBuildSignInRequired,
            "update_cli" => MessageId::GrokBuildUpdateRequired,
            "disabled" => MessageId::GrokBuildDisabled,
            _ => MessageId::GrokBuildCheckFailed,
        },
    )
}

fn grok_build_guidance(action: &str) -> MessageId {
    match action {
        "login_cli" => MessageId::GrokBuildLoginDetail,
        "update_cli" => MessageId::GrokBuildUpdateDetail,
        "disabled" => MessageId::GrokBuildDisabledDetail,
        _ => MessageId::GrokBuildRetryDetail,
    }
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

#[derive(Debug, Clone)]
struct TranscriptViewport {
    locale: Locale,
    density: DensityMode,
    glyph_mode: GlyphMode,
    content_width: u16,
    tool_expand_key: &'static str,
    tool_inspect_key: &'static str,
    expanded_output_budget: usize,
    height: usize,
    requested_scroll: usize,
    follow_bottom: bool,
    selection_anchor: Option<usize>,
    scroll_anchor: Option<TranscriptScrollAnchor>,
}

#[derive(Debug, Clone, Copy)]
struct TranscriptRenderOptions {
    locale: Locale,
    density: DensityMode,
    glyph_mode: GlyphMode,
    content_width: u16,
    tool_expand_key: &'static str,
    tool_inspect_key: &'static str,
    expanded_output_budget: usize,
}

impl TranscriptRenderOptions {
    fn styles(self) -> TranscriptStyles {
        TranscriptStyles {
            glyphs: Glyphs::new(self.glyph_mode),
            ..TranscriptStyles::default()
        }
    }
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

fn truncate_cells(value: &str, max_width: usize, glyphs: Glyphs) -> String {
    crate::render_contract::truncate_width_with_glyphs(value, max_width, glyphs)
}

fn conversation_import_source_label(source: &str) -> &str {
    match source {
        "claude-code" => "Claude Code",
        "codex" => "Codex",
        _ => source,
    }
}

fn pad_cells(value: &str, width: usize, glyphs: Glyphs) -> String {
    let mut out = truncate_cells(value, width, glyphs);
    let used = UnicodeWidthStr::width(out.as_str());
    out.push_str(&" ".repeat(width.saturating_sub(used)));
    out
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
            glyph_mode,
            content_width,
            tool_expand_key,
            tool_inspect_key,
            expanded_output_budget,
            height: viewport_height,
            requested_scroll,
            follow_bottom,
            selection_anchor,
            scroll_anchor,
        } = viewport;
        let render_options = TranscriptRenderOptions {
            locale,
            density,
            glyph_mode,
            content_width,
            tool_expand_key,
            tool_inspect_key,
            expanded_output_budget,
        };
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
                            && entry.glyph_mode == glyph_mode
                            && entry.expand_key == tool_expand_key
                            && entry.inspect_key == tool_inspect_key
                            && entry.expanded_output_budget == expanded_output_budget =>
                    {
                        (entry.height, entry.header_height)
                    }
                    _ => {
                        let (height, header_height) =
                            transcript_block_height_with_tool_key_and_density(
                                block,
                                render_options,
                            );
                        height_cache.insert(
                            block.id.clone(),
                            TranscriptHeightCacheEntry {
                                revision: block.layout_revision,
                                width: content_width,
                                locale,
                                density,
                                glyph_mode,
                                expand_key: tool_expand_key,
                                inspect_key: tool_inspect_key,
                                expanded_output_budget,
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
        let viewport_start = selection_anchor
            .and_then(|index| items.get(index))
            .map(|item| {
                item.top
                    .saturating_sub(viewport_height.saturating_div(3))
                    .min(max_scroll)
            })
            .or_else(|| {
                (!follow_bottom)
                    .then_some(scroll_anchor)
                    .flatten()
                    .and_then(|anchor| {
                        let exact_index =
                            blocks.iter().position(|block| block.id == anchor.block_id);
                        let index = exact_index
                            .unwrap_or(anchor.block_index.min(blocks.len().saturating_sub(1)));
                        let block = blocks.get(index)?;
                        let item = items.get(index)?;
                        let row = if exact_index.is_some() {
                            transcript_row_for_anchor(
                                block,
                                render_options,
                                anchor.logical_line,
                                anchor.sub_rows,
                            )
                        } else {
                            0
                        };
                        Some(item.top.saturating_add(row).min(max_scroll))
                    })
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

    fn capture_anchor(
        &self,
        blocks: &[TranscriptBlock],
        viewport: TranscriptViewport,
    ) -> Option<TranscriptScrollAnchor> {
        if viewport.follow_bottom || self.items.is_empty() {
            return None;
        }
        let item = self
            .items
            .iter()
            .rev()
            .find(|item| item.height > 0 && item.top <= self.viewport_start)?;
        let block = blocks.get(item.index)?;
        let rows_into_block = self
            .viewport_start
            .saturating_sub(item.top)
            .min(item.height.saturating_sub(1));
        let (logical_line, sub_rows) = transcript_anchor_for_row(
            block,
            TranscriptRenderOptions {
                locale: viewport.locale,
                density: viewport.density,
                glyph_mode: viewport.glyph_mode,
                content_width: viewport.content_width,
                tool_expand_key: viewport.tool_expand_key,
                tool_inspect_key: viewport.tool_inspect_key,
                expanded_output_budget: viewport.expanded_output_budget,
            },
            rows_into_block,
        );
        Some(TranscriptScrollAnchor {
            block_id: block.id.clone(),
            block_index: item.index,
            logical_line,
            sub_rows,
        })
    }
}

fn transcript_logical_line_rows(
    block: &TranscriptBlock,
    options: TranscriptRenderOptions,
) -> Vec<usize> {
    transcript_lines_with_tool_key_and_density(
        block,
        options.locale,
        options.styles(),
        layout_contract::TRANSCRIPT_READING_MAX_WIDTH,
        options,
    )
    .into_iter()
    .map(|line| {
        Paragraph::new(vec![line])
            .wrap(Wrap { trim: false })
            .line_count(options.content_width)
            .max(1)
    })
    .collect()
}

fn transcript_anchor_for_row(
    block: &TranscriptBlock,
    options: TranscriptRenderOptions,
    row: usize,
) -> (usize, usize) {
    let rows = transcript_logical_line_rows(block, options);
    let mut start = 0_usize;
    for (logical_line, height) in rows.iter().copied().enumerate() {
        if row < start.saturating_add(height) {
            return (logical_line, row.saturating_sub(start));
        }
        start = start.saturating_add(height);
    }
    (
        rows.len().saturating_sub(1),
        rows.last().copied().unwrap_or(1).saturating_sub(1),
    )
}

fn transcript_row_for_anchor(
    block: &TranscriptBlock,
    options: TranscriptRenderOptions,
    logical_line: usize,
    sub_rows: usize,
) -> usize {
    let rows = transcript_logical_line_rows(block, options);
    let line = logical_line.min(rows.len().saturating_sub(1));
    let line_start = rows.iter().take(line).sum::<usize>();
    line_start.saturating_add(sub_rows.min(rows.get(line).copied().unwrap_or(1).saturating_sub(1)))
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
        TranscriptRenderOptions {
            locale,
            density: DensityMode::Compact,
            glyph_mode: GlyphMode::Unicode,
            content_width,
            tool_expand_key,
            tool_inspect_key: crate::keybinding::KeyBindings::default()
                .inspect_tool_output
                .label(),
            expanded_output_budget: layout_contract::TOOL_OUTPUT_MAX_BUDGET,
        },
    )
}

fn transcript_block_height_with_tool_key_and_density(
    block: &TranscriptBlock,
    options: TranscriptRenderOptions,
) -> (usize, usize) {
    let lines = transcript_lines_with_tool_key_and_density(
        block,
        options.locale,
        options.styles(),
        options.content_width,
        options,
    );
    let header_height = Paragraph::new(vec![lines[0].clone()])
        .wrap(Wrap { trim: false })
        .line_count(options.content_width)
        .max(layout_contract::MIN_DIMENSION as usize);
    let height = Paragraph::new(lines)
        .wrap(Wrap { trim: false })
        .line_count(options.content_width)
        .max(layout_contract::MIN_DIMENSION as usize);
    (height, header_height)
}

fn transcript_block_hidden(block: &TranscriptBlock) -> bool {
    block.tool_members.is_empty()
        && block.todo_items.is_empty()
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
        TranscriptRenderOptions {
            locale,
            density: DensityMode::Compact,
            glyph_mode: styles.glyphs.mode,
            content_width,
            tool_expand_key,
            tool_inspect_key: crate::keybinding::KeyBindings::default()
                .inspect_tool_output
                .label(),
            expanded_output_budget: layout_contract::TOOL_OUTPUT_MAX_BUDGET,
        },
    )
}

fn transcript_lines_with_tool_key_and_density(
    block: &TranscriptBlock,
    locale: Locale,
    styles: TranscriptStyles,
    content_width: u16,
    options: TranscriptRenderOptions,
) -> Vec<Line<'static>> {
    let TranscriptRenderOptions {
        density,
        tool_expand_key,
        tool_inspect_key,
        expanded_output_budget,
        ..
    } = options;
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
        let role_label = imported_transcript_role_label(block)
            .unwrap_or_else(|| tr(locale, MessageId::TranscriptYou));
        let prefix = transcript_role_prefix_with_width(
            marker,
            role_label,
            label_style,
            metadata_style,
            content_width,
            imported_transcript_role_width(block),
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
        let role_label = imported_transcript_role_label(block)
            .unwrap_or_else(|| tr(locale, MessageId::TranscriptCarina));
        let prefix = transcript_role_prefix_with_width(
            glyphs.role_prefix(),
            role_label,
            label_style,
            metadata_style,
            content_width,
            imported_transcript_role_width(block),
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
        let count = failure.attempt_count.max(1).to_string();
        let attempt = tr_format(
            locale,
            if failure.attempt_count.max(1) == 1 {
                MessageId::FailureAttemptOne
            } else {
                MessageId::FailureAttemptOther
            },
            &[("count", count.as_str())],
        );
        let model = if failure.model.trim().is_empty() {
            tr(locale, MessageId::UnknownValue).to_owned()
        } else {
            failure.model.clone()
        };
        let separator = format!("  {}  ", glyphs.separator());
        let mut logical_lines = vec![
            Line::from(vec![
                Span::styled(glyphs.failure_prefix(), removed_style),
                Span::styled(
                    block.localized_title(locale),
                    removed_style.add_modifier(Modifier::BOLD),
                ),
                Span::styled(separator.clone(), metadata_style),
                Span::styled(model, text_style),
                Span::styled(separator, metadata_style),
                Span::styled(attempt, metadata_style),
            ]),
            Line::from(vec![
                Span::styled(glyphs.tool_gutter(), metadata_style),
                Span::styled(
                    localize_operator_failure_reason(locale, &failure.reason),
                    text_style,
                ),
            ]),
        ];
        if block.expanded {
            if !failure.owner.is_empty() {
                logical_lines.push(Line::from(vec![
                    Span::styled(glyphs.tool_gutter(), metadata_style),
                    Span::styled(
                        format!("{}  ", tr(locale, MessageId::Agent)),
                        metadata_style,
                    ),
                    Span::styled(failure.owner.clone(), text_style),
                ]));
            }
            for (label, value) in [
                (
                    MessageId::FailureRootRun,
                    failure.retry_root_run_id.as_str(),
                ),
                (MessageId::FailureLatestRun, failure.run_id.as_str()),
                (MessageId::FailureEventId, failure.source_event_id.as_str()),
            ] {
                if value.is_empty() {
                    continue;
                }
                logical_lines.push(Line::from(vec![
                    Span::styled(glyphs.tool_gutter(), metadata_style),
                    Span::styled(format!("{}  ", tr(locale, label)), metadata_style),
                    Span::styled(value.to_owned(), code_style),
                ]));
            }
        }
        let mut action_line = vec![Span::styled(glyphs.tool_gutter(), metadata_style)];
        if failure.action == FailureAction::Recovering {
            action_line.push(Span::styled(
                tr(locale, MessageId::ExecutionWorking),
                metadata_style.add_modifier(Modifier::BOLD),
            ));
        }
        for action in failure_action_layout(locale, failure, block.expanded) {
            if action_line.len() > 1 {
                action_line.push(Span::styled("  ", metadata_style));
            }
            let base_style = match action.action {
                FailureRecoveryAction::RetryCurrent => link_style.add_modifier(Modifier::BOLD),
                FailureRecoveryAction::ReplayOriginal => text_style.add_modifier(Modifier::BOLD),
                FailureRecoveryAction::Details | FailureRecoveryAction::CopyId => metadata_style,
            };
            let style = if failure.focused_action == Some(action.action) {
                base_style.add_modifier(Modifier::BOLD | Modifier::REVERSED)
            } else {
                base_style
            };
            action_line.push(Span::styled(action.label, style));
        }
        logical_lines.push(Line::from(action_line));
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
        inspect_key: tool_inspect_key,
        expanded_output_budget,
    };
    if block.kind == BlockKind::Tool && block.tool_members.len() > 1 {
        lines.extend(tool_group_lines(block, tool_context));
    } else if block.kind == BlockKind::Tool
        && block.tool_kind == Some(crate::tool_projection::ToolKind::Todo)
        && !block.todo_items.is_empty()
    {
        lines.extend(todo_item_lines(&block.todo_items, tool_context));
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

fn todo_item_lines(items: &[TodoItem], context: ToolLineContext) -> Vec<Line<'static>> {
    let prefix = StyledPrefix::repeating(vec![Span::styled(
        context.styles.glyphs.tool_gutter(),
        context.styles.metadata,
    )]);
    let logical = items
        .iter()
        .filter(|item| !item.content.trim().is_empty())
        .map(|item| {
            let (marker, style) = match item.status.as_str() {
                "completed" | "done" => (context.styles.glyphs.success(), context.styles.added),
                "in_progress" | "running" | "active" => {
                    (context.styles.glyphs.ready(), context.styles.label)
                }
                "pending" | "queued" | "todo" | "" => {
                    (context.styles.glyphs.empty(), context.styles.metadata)
                }
                _ => (context.styles.glyphs.bullet(), context.styles.metadata),
            };
            Line::from(vec![
                Span::styled(format!("{marker} "), style),
                Span::styled(item.content.trim().to_owned(), context.styles.text),
            ])
        });
    wrap_styled_lines(logical, usize::from(context.content_width), &prefix)
}

#[derive(Debug, Clone, Copy)]
struct ToolLineContext {
    locale: Locale,
    density: DensityMode,
    styles: TranscriptStyles,
    content_width: u16,
    expand_key: &'static str,
    inspect_key: &'static str,
    expanded_output_budget: usize,
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
        lines.extend(tool_group_member_lines(
            member,
            context,
            block.tool_kind == Some(crate::tool_projection::ToolKind::Explore),
        ));
        if !member.body.is_empty() {
            lines.extend(tool_detail_lines(
                &member.body,
                member.body_kind,
                block.expanded,
                ToolLineContext {
                    expanded_output_budget: if block.expanded {
                        usize::MAX
                    } else {
                        context.expanded_output_budget
                    },
                    ..context
                },
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
    if block.expanded
        && block
            .tool_members
            .iter()
            .filter(|member| !member.body.is_empty())
            .all(|member| member.body_kind != BlockBodyKind::Diff)
    {
        lines = bounded_plain_visual_rows(lines, context, false);
    }
    lines
}

fn tool_group_member_lines(
    member: &ToolGroupMember,
    context: ToolLineContext,
    show_kind: bool,
) -> Vec<Line<'static>> {
    let styles = context.styles;
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
    let title = if show_kind {
        crate::tool_projection::format_tool_title(
            crate::tool_projection::ToolKind::from_name(&member.tool_name).label(),
            &member.title,
        )
    } else {
        member.title.clone()
    };
    let line = Line::from(vec![
        Span::styled(marker, marker_style),
        Span::styled(title, styles.text),
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
    ]);
    wrap_styled_lines(
        [line],
        usize::from(context.content_width),
        &StyledPrefix::repeating(vec![Span::styled(
            styles.glyphs.tool_gutter(),
            styles.metadata,
        )]),
    )
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
    if expanded {
        lines = bounded_plain_visual_rows(lines, context, nested);
    }
    lines
}

fn expanded_tool_output_budget(viewport_height: usize) -> usize {
    viewport_height.saturating_sub(2).clamp(
        layout_contract::TOOL_OUTPUT_MIN_BUDGET,
        layout_contract::TOOL_OUTPUT_MAX_BUDGET,
    )
}

fn bounded_plain_visual_rows(
    lines: Vec<Line<'static>>,
    context: ToolLineContext,
    nested: bool,
) -> Vec<Line<'static>> {
    let budget = context.expanded_output_budget;
    let Some(head) = bounded_plain_receipt_index(lines.len(), budget) else {
        return lines;
    };
    let budget = budget.max(layout_contract::TOOL_OUTPUT_MIN_BUDGET);
    let retained = budget.saturating_sub(1).max(2);
    let tail = retained.saturating_sub(head);
    let omitted = lines.len().saturating_sub(head + tail);
    let mut bounded = Vec::with_capacity(head + 1 + tail);
    bounded.extend(lines.iter().take(head).cloned());
    bounded.push(tool_omission_line(
        omitted,
        MessageId::ToolVisualRowOmitted,
        MessageId::ToolVisualRowsOmitted,
        ToolLineContext {
            expand_key: context.inspect_key,
            ..context
        },
        nested,
    ));
    bounded.extend(lines.into_iter().skip(head + omitted));
    bounded
}

fn bounded_plain_receipt_index(total_rows: usize, budget: usize) -> Option<usize> {
    let budget = budget.max(layout_contract::TOOL_OUTPUT_MIN_BUDGET);
    (total_rows > budget).then(|| budget.saturating_sub(1).max(2).div_ceil(2))
}

fn bounded_tool_output_receipt_line(
    block: &TranscriptBlock,
    context: ToolLineContext,
) -> Option<usize> {
    if block.kind != BlockKind::Tool || !block.expanded {
        return None;
    }
    let unbounded = ToolLineContext {
        expanded_output_budget: usize::MAX,
        ..context
    };
    let detail_rows = if block.tool_members.len() > 1 {
        if block
            .tool_members
            .iter()
            .filter(|member| !member.body.is_empty())
            .any(|member| member.body_kind == BlockBodyKind::Diff)
        {
            return None;
        }
        let mut rows = Vec::new();
        for member in &block.tool_members {
            rows.extend(tool_group_member_lines(
                member,
                unbounded,
                block.tool_kind == Some(crate::tool_projection::ToolKind::Explore),
            ));
            if !member.body.is_empty() {
                rows.extend(unbounded_plain_tool_detail_lines(
                    &member.body,
                    member.body_kind,
                    unbounded,
                    true,
                ));
            }
        }
        rows.len()
    } else if !block.body.is_empty() && block.body_kind != BlockBodyKind::Diff {
        unbounded_plain_tool_detail_lines(&block.body, block.body_kind, unbounded, false).len()
    } else {
        return None;
    };
    bounded_plain_receipt_index(detail_rows, context.expanded_output_budget)
        .map(|detail_line| detail_line + 1)
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct VisibleToolOutputCandidate {
    block_id: String,
    hovered: bool,
    selected: bool,
    receipt_top: usize,
}

fn prioritize_visible_tool_output_candidates(
    candidates: &mut [VisibleToolOutputCandidate],
    viewport_start: usize,
    viewport_height: usize,
) {
    let reading_focus = viewport_start.saturating_add(viewport_height.saturating_sub(1) / 2);
    candidates.sort_by_key(|candidate| {
        (
            candidate.hovered,
            candidate.selected,
            Reverse(candidate.receipt_top.abs_diff(reading_focus)),
            candidate.receipt_top,
        )
    });
}

fn unbounded_plain_tool_detail_lines(
    body: &str,
    body_kind: BlockBodyKind,
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
    wrap_styled_lines(
        body.split('\n')
            .map(|line| styled_detail_line(line, body_kind, context.styles)),
        usize::from(context.content_width),
        &prefix,
    )
}

fn nearest_owning_prompt<'a>(
    blocks: &'a [TranscriptBlock],
    block_index: usize,
    run_id: &str,
) -> &'a str {
    let preceding = &blocks[..block_index];
    preceding
        .iter()
        .rev()
        .find(|block| block.kind == BlockKind::User && !run_id.is_empty() && block.run_id == run_id)
        .or_else(|| {
            preceding
                .iter()
                .rev()
                .find(|block| block.kind == BlockKind::User)
        })
        .map(|block| block.body.as_str())
        .unwrap_or_default()
}

fn complete_tool_output(block: &TranscriptBlock) -> Cow<'_, str> {
    if block.tool_members.len() > 1 {
        return Cow::Owned(
            block
                .tool_members
                .iter()
                .filter(|member| !member.body.is_empty())
                .map(|member| {
                    if member.title.is_empty() {
                        member.body.clone()
                    } else {
                        format!("{}\n{}", member.title, member.body)
                    }
                })
                .collect::<Vec<_>>()
                .join("\n\n"),
        );
    }
    Cow::Borrowed(&block.body)
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum ToolOutputRetainedStatus {
    Loading,
    Failed(String),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct ToolOutputOverlayLayout {
    context: Rect,
    body: Rect,
    footer: Rect,
}

fn tool_output_overlay_layout(inner: Rect) -> ToolOutputOverlayLayout {
    let body_height = u16::from(inner.height > 0);
    let footer_height = u16::from(inner.height > body_height);
    let context_height = inner
        .height
        .saturating_sub(body_height + footer_height)
        .min(layout_contract::TOOL_OUTPUT_CONTEXT_HEIGHT);
    let body_height = inner.height.saturating_sub(context_height + footer_height);
    let context = Rect::new(inner.x, inner.y, inner.width, context_height);
    let body = Rect::new(inner.x, context.bottom(), inner.width, body_height);
    let footer = Rect::new(inner.x, body.bottom(), inner.width, footer_height);
    ToolOutputOverlayLayout {
        context,
        body,
        footer,
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ToolOutputVisualWindow {
    lines: Vec<Line<'static>>,
    total_rows: usize,
    max_scroll: usize,
    scroll: usize,
}

fn tool_output_row_position(window: &ToolOutputVisualWindow, visible_height: usize) -> String {
    let first = window.scroll.saturating_add(1).min(window.total_rows);
    let last = window
        .scroll
        .saturating_add(visible_height)
        .min(window.total_rows);
    format!("{first}-{last} / {}", window.total_rows)
}

fn tool_output_footer_line(hint: &str, row_position: Option<&str>, width: usize) -> Line<'static> {
    let Some(row_position) = row_position else {
        return Line::from(hint.to_owned());
    };
    let hint_width = UnicodeWidthStr::width(hint);
    let position_width = UnicodeWidthStr::width(row_position);
    if hint_width.saturating_add(position_width).saturating_add(2) <= width {
        return Line::from(format!(
            "{hint}{}{row_position}",
            " ".repeat(width - hint_width - position_width)
        ));
    }
    if position_width <= width {
        return Line::from(format!(
            "{}{row_position}",
            " ".repeat(width - position_width)
        ));
    }
    Line::from(hint.to_owned())
}

fn tool_output_visual_window(
    output: &str,
    width: usize,
    height: usize,
    requested_scroll: usize,
) -> ToolOutputVisualWindow {
    let width = width.max(1);
    let total_rows = output
        .split('\n')
        .map(|line| tool_output_logical_row_count(line, width))
        .sum::<usize>()
        .max(1);
    let max_scroll = total_rows.saturating_sub(height.max(1));
    let scroll = requested_scroll.min(max_scroll);
    let window_end = scroll.saturating_add(height);
    let mut lines = Vec::with_capacity(height);
    let mut line_top = 0_usize;
    for logical in output.split('\n') {
        let row_count = tool_output_logical_row_count(logical, width);
        let line_bottom = line_top.saturating_add(row_count);
        if line_bottom > scroll && line_top < window_end {
            let start = scroll.saturating_sub(line_top);
            let end = window_end.min(line_bottom).saturating_sub(line_top);
            lines.extend(tool_output_logical_window(logical, width, start, end));
        }
        line_top = line_bottom;
        if line_top >= window_end && lines.len() >= height {
            break;
        }
    }
    ToolOutputVisualWindow {
        lines,
        total_rows,
        max_scroll,
        scroll,
    }
}

fn tool_output_logical_row_count(line: &str, width: usize) -> usize {
    let width = width.max(1);
    if line.is_ascii() {
        return line.len().max(1).div_ceil(width);
    }
    let mut rows = 1_usize;
    let mut used = 0_usize;
    for grapheme in line.graphemes(true) {
        let grapheme_width = UnicodeWidthStr::width(grapheme);
        if used > 0 && used.saturating_add(grapheme_width) > width {
            rows = rows.saturating_add(1);
            used = 0;
        }
        used = if grapheme_width > width {
            width
        } else {
            used.saturating_add(grapheme_width)
        };
    }
    rows
}

fn tool_output_logical_window(
    line: &str,
    width: usize,
    start: usize,
    end: usize,
) -> Vec<Line<'static>> {
    if start >= end {
        return Vec::new();
    }
    let width = width.max(1);
    if line.is_ascii() {
        return (start..end)
            .map(|row| {
                let byte_start = row.saturating_mul(width).min(line.len());
                let byte_end = byte_start.saturating_add(width).min(line.len());
                Line::from(line[byte_start..byte_end].to_owned())
            })
            .collect();
    }
    let mut rows = Vec::with_capacity(end.saturating_sub(start));
    let mut current = String::new();
    let mut used = 0_usize;
    let mut row = 0_usize;
    for grapheme in line.graphemes(true) {
        let grapheme_width = UnicodeWidthStr::width(grapheme);
        if used > 0 && used.saturating_add(grapheme_width) > width {
            if row >= start && row < end {
                rows.push(Line::from(std::mem::take(&mut current)));
            } else {
                current.clear();
            }
            row = row.saturating_add(1);
            if row >= end {
                return rows;
            }
            used = 0;
        }
        if row >= start {
            current.push_str(grapheme);
        }
        used = if grapheme_width > width {
            width
        } else {
            used.saturating_add(grapheme_width)
        };
    }
    if row >= start && row < end {
        rows.push(Line::from(current));
    }
    rows
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
            truncate_cells(&hint, width, context.styles.glyphs),
            context.styles.metadata,
        ));
    }
    Line::from(vec![
        Span::styled(gutter, context.styles.metadata),
        Span::styled(
            truncate_cells(
                &hint,
                width.saturating_sub(gutter_width),
                context.styles.glyphs,
            ),
            context.styles.metadata,
        ),
    ])
}

#[cfg(test)]
#[cfg(test)]
fn transcript_role_prefix(
    marker: &'static str,
    label: &str,
    label_style: Style,
    continuation_style: Style,
    content_width: u16,
) -> StyledPrefix {
    transcript_role_prefix_with_width(
        marker,
        label,
        label_style,
        continuation_style,
        content_width,
        layout_contract::TRANSCRIPT_ROLE_LABEL_WIDTH,
    )
}

fn transcript_role_prefix_with_width(
    marker: &'static str,
    label: &str,
    label_style: Style,
    continuation_style: Style,
    content_width: u16,
    label_field_width: usize,
) -> StyledPrefix {
    let label = truncate_cells(label, label_field_width, Glyphs::new(GlyphMode::Unicode));
    let label_width = UnicodeWidthStr::width(label.as_str());
    let padded_label = format!(
        "{label}{}{}",
        " ".repeat(label_field_width.saturating_sub(label_width)),
        " ".repeat(layout_contract::TRANSCRIPT_ROLE_GAP_WIDTH)
    );
    let continuation_width = if content_width < layout_contract::TRANSCRIPT_FULL_ROLE_MIN_WIDTH {
        layout_contract::TRANSCRIPT_ROLE_MARK_WIDTH
    } else {
        layout_contract::TRANSCRIPT_ROLE_PREFIX_WIDTH
    };
    let prefix = StyledPrefix::hanging_with_width(
        vec![
            Span::styled(marker, label_style),
            Span::styled(padded_label, label_style),
        ],
        continuation_width,
        continuation_style,
    );
    debug_assert_eq!(
        prefix.width(),
        layout_contract::TRANSCRIPT_ROLE_MARK_WIDTH
            + label_field_width
            + layout_contract::TRANSCRIPT_ROLE_GAP_WIDTH
    );
    prefix
}

fn imported_transcript_role_label(block: &TranscriptBlock) -> Option<&str> {
    matches!(block.kind, BlockKind::User | BlockKind::Assistant)
        .then_some(block.title.as_str())
        .filter(|title| *title == "Imported conversation" || title.ends_with(" import"))
}

fn imported_transcript_role_width(block: &TranscriptBlock) -> usize {
    imported_transcript_role_label(block)
        .map(UnicodeWidthStr::width)
        .unwrap_or(layout_contract::TRANSCRIPT_ROLE_LABEL_WIDTH)
        .clamp(
            layout_contract::TRANSCRIPT_ROLE_LABEL_WIDTH,
            layout_contract::TRANSCRIPT_IMPORTED_ROLE_LABEL_MAX_WIDTH,
        )
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
        TranscriptRenderOptions {
            locale,
            density: DensityMode::Compact,
            glyph_mode: styles.glyphs.mode,
            content_width,
            tool_expand_key,
            tool_inspect_key: crate::keybinding::KeyBindings::default()
                .inspect_tool_output
                .label(),
            expanded_output_budget: layout_contract::TOOL_OUTPUT_MAX_BUDGET,
        },
    )
}

fn cached_transcript_lines_with_tool_key_and_density(
    cache: &mut TranscriptRenderCache,
    block: &TranscriptBlock,
    locale: Locale,
    styles: TranscriptStyles,
    content_width: u16,
    options: TranscriptRenderOptions,
) -> Vec<Line<'static>> {
    let TranscriptRenderOptions {
        density,
        tool_expand_key,
        tool_inspect_key,
        expanded_output_budget,
        ..
    } = options;
    if let Some(entry) = cache.get(&block.id)
        && entry.revision == block.layout_revision
        && entry.width == content_width
        && entry.locale == locale
        && entry.density == density
        && entry.glyph_mode == styles.glyphs.mode
        && entry.expand_key == tool_expand_key
        && entry.inspect_key == tool_inspect_key
        && entry.expanded_output_budget == expanded_output_budget
    {
        return entry.lines.clone();
    }
    let lines =
        transcript_lines_with_tool_key_and_density(block, locale, styles, content_width, options);
    cache.insert(
        block.id.clone(),
        TranscriptRenderCacheEntry {
            revision: block.layout_revision,
            width: content_width,
            locale,
            density,
            glyph_mode: styles.glyphs.mode,
            expand_key: tool_expand_key,
            inspect_key: tool_inspect_key,
            expanded_output_budget,
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
    if block.failure.is_some() {
        return None;
    }
    block
        .is_collapsible()
        .then(|| Action::ToggleBlock(block.id.clone()))
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct FailureActionLayout {
    action: FailureRecoveryAction,
    label: String,
    start_cell: usize,
    width: usize,
}

fn failure_action_label(
    locale: Locale,
    action: FailureRecoveryAction,
    expanded: bool,
) -> &'static str {
    let id = match action {
        FailureRecoveryAction::RetryCurrent => MessageId::RetryCurrentModel,
        FailureRecoveryAction::ReplayOriginal => MessageId::ReplayOriginalRoute,
        FailureRecoveryAction::Details if expanded => MessageId::FailureHideDetails,
        FailureRecoveryAction::Details => MessageId::FailureDetails,
        FailureRecoveryAction::CopyId => MessageId::CopyFailureId,
    };
    tr(locale, id)
}

fn failure_action_layout(
    locale: Locale,
    failure: &FailurePresentation,
    expanded: bool,
) -> Vec<FailureActionLayout> {
    let mut cursor = if failure.action == FailureAction::Recovering {
        UnicodeWidthStr::width(tr(locale, MessageId::ExecutionWorking)).saturating_add(2)
    } else {
        0
    };
    failure
        .available_actions(&failure.current_model)
        .into_iter()
        .map(|action| {
            let label = failure_action_label(locale, action, expanded).to_owned();
            let width = UnicodeWidthStr::width(label.as_str());
            let start_cell = cursor;
            cursor = cursor.saturating_add(width).saturating_add(2);
            FailureActionLayout {
                action,
                label,
                start_cell,
                width,
            }
        })
        .collect()
}

fn failure_action_component(block_id: &str, action: FailureRecoveryAction) -> ComponentId {
    let suffix = match action {
        FailureRecoveryAction::RetryCurrent => "retry-current",
        FailureRecoveryAction::ReplayOriginal => "replay-original",
        FailureRecoveryAction::Details => "details",
        FailureRecoveryAction::CopyId => "copy-id",
    };
    ComponentId::stable("failure-action", &format!("{block_id}:{suffix}"))
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
            area.x,
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

fn outside_rects(area: Rect, popup: Rect) -> [Rect; 4] {
    [
        Rect::new(area.x, area.y, area.width, popup.y.saturating_sub(area.y)),
        Rect::new(
            area.x,
            popup.bottom(),
            area.width,
            area.bottom().saturating_sub(popup.bottom()),
        ),
        Rect::new(
            area.x,
            popup.y,
            popup.x.saturating_sub(area.x),
            popup.height,
        ),
        Rect::new(
            popup.right(),
            popup.y,
            area.right().saturating_sub(popup.right()),
            popup.height,
        ),
    ]
}

fn border_rects(area: Rect) -> [Rect; 4] {
    [
        Rect::new(area.x, area.y, area.width, area.height.min(1)),
        Rect::new(
            area.x,
            area.bottom().saturating_sub(1),
            area.width,
            area.height.min(1),
        ),
        Rect::new(area.x, area.y, area.width.min(1), area.height),
        Rect::new(
            area.right().saturating_sub(1),
            area.y,
            area.width.min(1),
            area.height,
        ),
    ]
}

fn validation_spinner(elapsed: Option<Duration>, glyphs: Glyphs) -> &'static str {
    crate::render_contract::status_spinner(elapsed.unwrap_or_default(), glyphs)
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
        "grok" => "Grok Build",
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
    use crate::history_search::HistorySearchState;
    use crate::overlay::PlanReviewOverlay;
    use crate::rpc::{ConversationImportCandidate, ConversationImportDiscovery, Session};
    use crossterm::event::{
        Event, KeyCode, KeyEvent, KeyModifiers, MouseButton, MouseEvent, MouseEventKind,
    };
    use ratatui::style::Color;

    #[test]
    fn disabled_grok_build_has_explicit_status_and_guidance() {
        for locale in Locale::ALL {
            assert_eq!(
                grok_build_state_label(locale, "disabled"),
                tr(locale, MessageId::GrokBuildDisabled)
            );
            assert_eq!(
                grok_build_guidance("disabled"),
                MessageId::GrokBuildDisabledDetail
            );
        }
    }

    fn production_render_app() -> (App, std::path::PathBuf, std::thread::JoinHandle<()>) {
        production_render_app_with_model_refreshes(0)
    }

    fn production_render_app_with_model_refreshes(
        model_refreshes: usize,
    ) -> (App, std::path::PathBuf, std::thread::JoinHandle<()>) {
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
            for _ in 0..model_refreshes {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let request: serde_json::Value = serde_json::from_str(&line).unwrap();
                assert_eq!(request["method"], "model.list");
                writeln!(
                    stream,
                    "{}",
                    serde_json::json!({
                        "jsonrpc": "2.0",
                        "id": request["id"],
                        "result": {
                            "default_model": "test/model",
                            "reasoner": {"available": true},
                            "providers": [{
                                "id": "test",
                                "registered": true,
                                "available": true,
                                "models": [{"id": "test/model", "available": true}]
                            }]
                        }
                    })
                )
                .unwrap();
                stream.flush().unwrap();
            }
        });
        let mut app = App::bootstrap(super::super::Options {
            socket,
            workspace: root.clone(),
            runtime_expectation: None,
            session_id: None,
            locale: None,
            locale_path: None,
            density: DensityMode::Compact,
            density_path: None,
            glyph_preference: crate::glyphs::GlyphPreference::Auto,
            glyphs_path: None,
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

    #[test]
    fn model_picker_names_and_follows_its_back_destination() {
        let (mut onboarding, root, server) = production_render_app();
        onboarding.phase = Phase::Model;
        onboarding.options.locale = Some(Locale::ZhHans.product_id().into());
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 30)).unwrap();
        terminal.draw(|frame| onboarding.render(frame)).unwrap();
        let rendered = rendered_frame_text(terminal.backend().buffer());
        assert!(rendered.contains(tr(Locale::ZhHans, MessageId::BackToProvider)));
        assert!(!rendered.contains(tr(Locale::ZhHans, MessageId::BackToConversation)));
        assert!(rendered.contains(&format!("P  {}", tr(Locale::ZhHans, MessageId::Provider))));
        onboarding.focus = Focus::Composer;
        for modifiers in [
            KeyModifiers::CONTROL,
            KeyModifiers::ALT,
            KeyModifiers::SUPER,
            KeyModifiers::CONTROL | KeyModifiers::SHIFT,
        ] {
            onboarding
                .handle_key(KeyEvent::new(KeyCode::Char('p'), modifiers))
                .unwrap();
            assert_eq!(onboarding.phase, Phase::Model);
            assert_eq!(onboarding.focus, Focus::Composer);
        }
        onboarding
            .handle_key(KeyEvent::new(KeyCode::Char('p'), KeyModifiers::NONE))
            .unwrap();
        assert_eq!(onboarding.phase, Phase::Provider);
        assert_eq!(onboarding.focus, Focus::Scene);
        onboarding.phase = Phase::Model;
        onboarding
            .handle_key(KeyEvent::new(KeyCode::Esc, KeyModifiers::NONE))
            .unwrap();
        assert_eq!(onboarding.phase, Phase::Provider);
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();

        let (mut conversation, root, server) = production_render_app_with_model_refreshes(1);
        conversation.phase = Phase::Model;
        conversation.options.locale = Some(Locale::ZhHans.product_id().into());
        conversation.active_session = Some(
            serde_json::from_value(serde_json::json!({
                "session_id": "sess-model-picker",
                "workspace_root": conversation.options.workspace,
                "status": "active",
                "next_model": "test/model",
                "execution_status": "ready"
            }))
            .unwrap(),
        );
        terminal.draw(|frame| conversation.render(frame)).unwrap();
        let rendered = rendered_frame_text(terminal.backend().buffer());
        assert!(rendered.contains(tr(Locale::ZhHans, MessageId::BackToConversation)));
        assert!(!rendered.contains(tr(Locale::ZhHans, MessageId::BackToProvider)));
        assert!(rendered.contains(&format!("P  {}", tr(Locale::ZhHans, MessageId::Provider))));
        conversation.focus = Focus::Composer;
        conversation
            .handle_key(KeyEvent::new(KeyCode::Char('P'), KeyModifiers::SHIFT))
            .unwrap();
        assert_eq!(conversation.phase, Phase::Provider);
        assert_eq!(conversation.focus, Focus::Scene);
        conversation.phase = Phase::Model;
        conversation
            .handle_key(KeyEvent::new(KeyCode::Esc, KeyModifiers::NONE))
            .unwrap();
        assert_eq!(conversation.phase, Phase::Conversation);
        assert_eq!(conversation.focus, Focus::Composer);
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    fn header_provider(id: &str, name: &str, model_id: &str, model_name: &str) -> ModelProvider {
        serde_json::from_value(serde_json::json!({
            "id": id,
            "name": name,
            "registered": true,
            "available": true,
            "models": [{
                "id": model_id,
                "display_id": model_name,
                "available": true,
                "reasoning": true
            }]
        }))
        .unwrap()
    }

    fn model_preference_session(revision: u64) -> Session {
        Session {
            session_id: "sess-model-preference".into(),
            name: String::new(),
            workspace_id: "ws".into(),
            workspace_root: "/tmp/model-preference".into(),
            status: "active".into(),
            next_model: "provider-a/model-a".into(),
            next_reasoning_effort: "medium".into(),
            model_preference_revision: revision,
            plan_mode: false,
            permission_profile: "safe-edit".into(),
            approval_mode: "on_request".into(),
            created_at: String::new(),
            updated_at: String::new(),
            latest_run_id: String::new(),
            latest_run_agent: String::new(),
            latest_run_result_kind: String::new(),
            execution_status: "ready".into(),
            summary: String::new(),
            continuity: None,
        }
    }

    fn prepare_model_preference_app(app: &mut App, revision: u64) {
        let mut providers = vec![
            header_provider("provider-a", "Provider A", "provider-a/model-a", "Model A"),
            header_provider("provider-b", "Provider B", "provider-b/model-b", "Model B"),
        ];
        for provider in &mut providers {
            provider.models[0].reasoning_efforts = vec!["medium".into(), "high".into()];
            provider.models[0].default_reasoning_effort = "medium".into();
        }
        app.inventory.providers = providers;
        app.models = app.inventory.available_models();
        let session = model_preference_session(revision);
        app.selected_model = session.next_model.clone();
        app.selected_reasoning_effort = session.next_reasoning_effort.clone();
        app.command_registry_session = session.session_id.clone();
        app.sessions = vec![session.clone()];
        app.active_session = Some(session);
    }

    fn model_preference_selection(revision: u64) -> crate::rpc::SessionModelSelection {
        crate::rpc::SessionModelSelection {
            session_id: "sess-model-preference".into(),
            next_model: "provider-b/model-b".into(),
            next_reasoning_effort: "high".into(),
            model_preference_revision: revision,
        }
    }

    fn count_label(value: &str, label: &str) -> usize {
        value.match_indices(label).count()
    }

    fn render_conversation_import_fixture(width: u16, locale: Locale) -> String {
        let (mut app, root, server) = production_render_app();
        app.theme.glyphs = app
            .theme
            .glyphs
            .with_mode(crate::glyphs::GlyphMode::Unicode);
        app.phase = Phase::Session;
        app.options.workspace = std::path::PathBuf::from("<workspace>");
        app.options.locale = Some(locale.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|candidate| *candidate == locale)
            .unwrap_or_default();
        let generation = app.session_browser.conversation_import_mut().begin();
        app.session_browser
            .conversation_import_mut()
            .apply_discovery(
                generation,
                Ok(ConversationImportDiscovery {
                    conversations: vec![
                        ConversationImportCandidate {
                            source: "claude-code".into(),
                            id: "claude-review-parser".into(),
                            path: "/history/claude/review-parser.jsonl".into(),
                            workspace_root: "<workspace>".into(),
                            target_workspace: "<workspace>".into(),
                            title: "Review the bounded conversation parser".into(),
                            updated_at: "2026-08-13 16:01".into(),
                            message_count: 7,
                            new_messages: 7,
                            importable: true,
                            ..ConversationImportCandidate::default()
                        },
                        ConversationImportCandidate {
                            source: "codex".into(),
                            id: "codex-audit-chain".into(),
                            path: "/history/codex/audit-chain.jsonl".into(),
                            workspace_root: "<workspace>".into(),
                            target_workspace: "<workspace>".into(),
                            title: "Verify the imported audit chain".into(),
                            updated_at: "2026-08-13 15:42".into(),
                            message_count: 4,
                            imported_messages: 4,
                            importable: true,
                            ..ConversationImportCandidate::default()
                        },
                    ],
                    copy_semantics: "local copy".into(),
                    ..ConversationImportDiscovery::default()
                }),
            );
        app.session_browser
            .conversation_import_mut()
            .toggle_candidate(Some(0));

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 30)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let rendered = rendered_frame_text(terminal.backend().buffer());
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
        rendered
    }

    #[test]
    fn conversation_import_selection_is_clear_at_narrow_and_wide_widths() {
        for (locale_name, locale) in [("en", Locale::En), ("zh_hans", Locale::ZhHans)] {
            for width in [80, 120] {
                let rendered = render_conversation_import_fixture(width, locale);
                assert!(rendered.contains("Carina"));
                assert!(rendered.contains("Claude Code"));
                assert!(rendered.contains("[x]"));
                assert!(rendered.contains(tr(locale, MessageId::ConversationImportUpToDate)));
                if width == 120 {
                    assert!(
                        rendered.contains(tr(locale, MessageId::ConversationImportTargetWorkspace)),
                        "wide import view must state the write target"
                    );
                }
                insta::assert_snapshot!(
                    format!("conversation_import_{locale_name}_{width}"),
                    rendered
                );
            }
        }
    }

    #[test]
    fn production_session_browser_exposes_import_action_with_existing_conversations() {
        let (mut app, root, server) = production_render_app();
        app.phase = Phase::Session;
        app.options.locale = Some(Locale::ZhHans.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|candidate| *candidate == Locale::ZhHans)
            .unwrap_or_default();
        app.sessions = vec![
            serde_json::from_value(serde_json::json!({
                "session_id": "sess-existing",
                "name": "pebble conversation",
                "workspace_id": "workspace-existing",
                "workspace_root": root,
                "status": "active",
                "execution_status": "ready"
            }))
            .unwrap(),
        ];
        app.session_browser
            .open(&app.sessions, &app.options.workspace);

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(160, 44)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let area = terminal.backend().buffer().area;
        let rendered = rendered_frame_text(terminal.backend().buffer());
        assert_eq!(
            count_label(
                &rendered,
                tr(Locale::ZhHans, MessageId::ImportConversations)
            ),
            2,
            "the import action must be visible in both the browser and keyboard footer"
        );

        let import_positions = action_positions(&app, area, Action::OpenConversationImport);
        assert!(
            !import_positions.is_empty(),
            "the visible import label must own a pointer action"
        );
        let trigger = import_positions[0];
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: trigger.x,
            row: trigger.y,
            modifiers: KeyModifiers::NONE,
        });
        assert!(app.session_browser.conversation_import().is_open());
        app.session_browser.conversation_import_mut().close();
        app.handle_key(KeyEvent::new(KeyCode::Char('i'), KeyModifiers::NONE))
            .unwrap();
        assert!(
            app.session_browser.conversation_import().is_open(),
            "the visible I shortcut must open the same import workflow"
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn conversation_import_discovery_failure_exposes_only_recovery_actions() {
        let (mut app, root, server) = production_render_app();
        app.phase = Phase::Session;
        app.options.locale = Some(Locale::ZhHans.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|candidate| *candidate == Locale::ZhHans)
            .unwrap_or_default();
        let generation = app.session_browser.conversation_import_mut().begin();
        app.session_browser
            .conversation_import_mut()
            .apply_discovery(
                generation,
                Err("daemon rejected request: method not found".into()),
            );

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(160, 44)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let area = terminal.backend().buffer().area;
        let rendered = rendered_frame_text(terminal.backend().buffer());
        assert!(rendered.contains("daemon rejected request: method not found"));
        assert!(rendered.contains(tr(Locale::ZhHans, MessageId::ConversationImportCheckAgain)));
        assert!(
            action_positions(&app, area, Action::ReviewConversationImport).is_empty(),
            "a failed discovery cannot offer import review"
        );
        assert!(
            action_positions(&app, area, Action::ToggleConversationImportAll).is_empty(),
            "a failed discovery cannot offer selection actions"
        );

        let retry = action_positions(&app, area, Action::RetryConversationImport);
        assert!(!retry.is_empty(), "the error must expose a retry action");
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: retry[0].x,
            row: retry[0].y,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(
            app.session_browser.conversation_import().stage(),
            ConversationImportStage::Discovering
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn imported_transcript_renders_the_external_source_instead_of_carina() {
        let mut imported_user = block(BlockKind::User, "Inspect the parser.");
        imported_user.title = "Claude Code import".into();
        imported_user.branchable = false;
        let mut imported_assistant = block(BlockKind::Assistant, "The parser is bounded.");
        imported_assistant.title = "Claude Code import".into();

        for imported in [&imported_user, &imported_assistant] {
            let rendered = plain(&transcript_lines_with_tool_key(
                imported,
                Locale::En,
                TranscriptStyles::default(),
                80,
                "Ctrl+O",
            ));
            let first = rendered.first().map(String::as_str).unwrap_or_default();
            assert!(first.starts_with(&format!(
                "{}Claude Code import",
                Glyphs::default().role_prefix()
            )));
            assert!(!first.starts_with(&format!("{}Carina", Glyphs::default().role_prefix())));
        }
    }

    #[test]
    fn model_preference_higher_revision_updates_session_picker_and_header() {
        let (mut app, root, server) = production_render_app();
        prepare_model_preference_app(&mut app, 4);

        assert!(app.apply_model_preference(model_preference_selection(5)));
        let active = app.active_session.as_ref().unwrap();
        assert_eq!(active.model_preference_revision, 5);
        assert_eq!(active.next_model, "provider-b/model-b");
        assert_eq!(app.sessions[0].model_preference_revision, 5);
        assert_eq!(app.selected_model, "provider-b/model-b");
        assert_eq!(app.selected_reasoning_effort, "high");
        let (_, model, reasoning) = app.product_header_metadata();
        assert_eq!(model, "Model B");
        assert!(reasoning.contains("high"));

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn model_preference_stale_and_duplicate_revisions_are_ignored() {
        let (mut app, root, server) = production_render_app();
        prepare_model_preference_app(&mut app, 5);

        for revision in [4, 5] {
            assert!(!app.apply_model_preference(model_preference_selection(revision)));
            assert_eq!(app.selected_model, "provider-a/model-a");
            assert_eq!(
                app.active_session
                    .as_ref()
                    .unwrap()
                    .model_preference_revision,
                5
            );
        }

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn model_preference_live_event_updates_state_without_advancing_durable_cursor() {
        let (mut app, root, server) = production_render_app();
        prepare_model_preference_app(&mut app, 5);
        app.blocks.push(TranscriptBlock::local_user(
            "local-user".into(),
            "keep the transcript".into(),
        ));
        let before = app.blocks.len();
        app.event_cursor = 11;
        let durable_cursor_before = app.event_cursor;
        let payload = serde_json::json!({
            "session_id": "sess-model-preference",
            "next_model": "provider-b/model-b",
            "next_reasoning_effort": "high",
            "model_preference_revision": 6
        })
        .as_object()
        .unwrap()
        .clone()
        .into_iter()
        .collect();
        app.pending_async
            .push_back(super::super::AsyncMessage::Event {
                generation: app.event_generation,
                value: Box::new(Ok(crate::rpc::ReceivedEvent {
                    event: crate::rpc::WireEvent {
                        session_id: "sess-model-preference".into(),
                        kind: "session.model.preference.changed".into(),
                        payload,
                        ..crate::rpc::WireEvent::default()
                    },
                    received_at: std::time::Instant::now(),
                    replayed: false,
                })),
            });

        app.apply_async();
        assert_eq!(app.blocks.len(), before);
        assert_eq!(app.event_cursor, durable_cursor_before);
        assert_eq!(app.selected_model, "provider-b/model-b");
        assert_eq!(
            app.active_session
                .as_ref()
                .unwrap()
                .model_preference_revision,
            6
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn model_preference_conflict_preserves_composer_draft() {
        let (mut app, root, server) = production_render_app();
        prepare_model_preference_app(&mut app, 5);
        app.composer.set_text("keep this exact draft");
        let error = crate::rpc::RpcError::Remote {
            code: -32011,
            message: "model_preference_conflict".into(),
            data: Some(serde_json::json!({
                "current": {
                    "session_id": "sess-model-preference",
                    "next_model": "provider-b/model-b",
                    "next_reasoning_effort": "high",
                    "model_preference_revision": 6
                }
            })),
        };

        assert!(app.reconcile_model_preference_conflict(&error));
        assert_eq!(app.composer.text(), "keep this exact draft");
        assert_eq!(app.selected_model, "provider-b/model-b");
        assert_eq!(
            app.notice.render(Locale::En, app.theme.glyphs),
            tr(Locale::En, MessageId::ModelPreferenceChangedDraftKept)
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn model_preference_pending_submission_freezes_captured_revision() {
        let (mut app, root, server) = production_render_app();
        prepare_model_preference_app(&mut app, 5);
        app.pending_submission = Some(super::super::PendingSubmission {
            session_id: "sess-model-preference".into(),
            prompt: "preserve submission identity".into(),
            model: "provider-a/model-a".into(),
            reasoning_effort: "medium".into(),
            model_preference_revision: 5,
            agent: String::new(),
            locale: "en".into(),
            submission_id: "submission-1".into(),
            media_refs: Vec::new(),
            local_id: "local-1".into(),
        });

        assert!(app.apply_model_preference(model_preference_selection(6)));
        let pending = app.pending_submission.as_ref().unwrap();
        assert_eq!(pending.model_preference_revision, 5);
        assert_eq!(pending.model, "provider-a/model-a");
        assert_eq!(pending.reasoning_effort, "medium");
        assert_eq!(
            app.active_session
                .as_ref()
                .unwrap()
                .model_preference_revision,
            6
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn conversation_header_exposes_reasoning_inside_the_model_route() {
        let (mut app, root, server) = production_render_app();
        app.options.locale = Some(Locale::ZhHans.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|locale| *locale == Locale::ZhHans)
            .unwrap_or_default();
        let mut provider = header_provider(
            "provider-a",
            "Provider A",
            "provider-a/gpt-5.6-sol",
            "gpt-5.6-sol",
        );
        provider.models[0].reasoning_efforts = vec!["low".into(), "high".into()];
        provider.models[0].default_reasoning_effort = "high".into();
        app.inventory.providers = vec![provider];
        app.models = app.inventory.available_models();
        app.selected_model = "provider-a/gpt-5.6-sol".into();
        app.selected_reasoning_effort = "high".into();

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 18)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let route = format!("gpt-5.6-sol  {}  high 推理", app.theme.glyphs.separator());
        let rendered = rendered_frame_text(terminal.backend().buffer());
        assert!(rendered.contains(&route), "{rendered}");
        let model_positions =
            action_positions(&app, terminal.backend().buffer().area, Action::OpenModels);
        assert_eq!(
            model_positions.len(),
            UnicodeWidthStr::width(route.as_str()) + 2,
            "the padded model and effort route must be one hit target"
        );
        assert!(
            model_positions
                .windows(2)
                .all(|pair| pair[0].y == pair[1].y && pair[0].x + 1 == pair[1].x),
            "the combined route must not contain non-clickable gaps"
        );

        let mut narrow =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(30, 18)).unwrap();
        narrow.draw(|frame| app.render(frame)).unwrap();
        let rendered = rendered_frame_text(narrow.backend().buffer());
        assert!(!rendered.contains("gpt-5.6-sol"), "{rendered}");
        assert!(!rendered.contains("推理"), "{rendered}");
        assert!(
            action_positions(&app, narrow.backend().buffer().area, Action::OpenModels).is_empty(),
            "narrow layouts must hide the combined route atomically"
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn running_header_owns_route_labels_once_when_next_preferences_differ() {
        let (mut app, root, server) = production_render_app();
        app.inventory.providers = vec![
            header_provider("provider-a", "Provider A", "provider-a/model-a", "Model A"),
            header_provider("provider-b", "Provider B", "provider-b/model-b", "Model B"),
        ];
        app.models = app.inventory.available_models();
        app.selected_model = "provider-b/model-b".into();
        app.selected_reasoning_effort = "medium".into();
        app.execution_status = "running".into();
        app.active_run_presentation = crate::conversation::ActiveRunPresentation {
            run_id: "run-active".into(),
            effective_model: "provider-a/model-a".into(),
            provider: "provider-a".into(),
            effective_reasoning_effort: "high".into(),
            ..crate::conversation::ActiveRunPresentation::default()
        };

        let (provider, model, reasoning) = app.product_header_metadata();
        let metadata = format!("{provider} | {model} | {reasoning}");
        let running = tr(Locale::En, MessageId::StatusRunning);
        let next = tr(Locale::En, MessageId::NextRun);
        assert!(provider.starts_with("Provider A"), "{metadata}");
        assert!(model.contains(&format!("{running} Model A")), "{metadata}");
        assert!(model.contains(&format!("{next} Model B")), "{metadata}");
        assert_eq!(
            reasoning,
            format!("high  {}  medium", app.theme.glyphs.separator())
        );
        assert_eq!(count_label(&metadata, running), 1, "{metadata}");
        assert_eq!(count_label(&metadata, next), 1, "{metadata}");

        for width in [60, 80, 120] {
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 3)).unwrap();
            terminal
                .draw(|frame| {
                    ProductHeader {
                        locale: Locale::En,
                        title: Some("Route truth"),
                        phase: None,
                        provider: &provider,
                        model: &model,
                        reasoning: &reasoning,
                        workspace: &app.options.workspace,
                        actions: &[],
                    }
                    .render(
                        frame,
                        frame.area(),
                        app.theme,
                        &mut InteractionMap::default(),
                    );
                })
                .unwrap();
            let rendered = rendered_frame_text(terminal.backend().buffer());
            let metadata_row = rendered
                .lines()
                .find(|line| line.trim_start().starts_with(running))
                .unwrap_or_default()
                .trim_start();
            assert!(
                metadata_row.starts_with(&format!("{running} Model A")),
                "width={width}\n{rendered}"
            );
            assert!(
                count_label(&rendered, running) <= 1,
                "width={width}\n{rendered}"
            );
            assert!(
                count_label(&rendered, next) <= 1,
                "width={width}\n{rendered}"
            );
        }

        app.selected_reasoning_effort.clear();
        let next_model = app
            .models
            .iter_mut()
            .find(|model| model.id == "provider-b/model-b")
            .expect("next model");
        next_model.reasoning_efforts = vec!["medium".into(), "high".into()];
        next_model.default_reasoning_effort = "medium".into();
        let (_, _, reasoning) = app.product_header_metadata();
        assert_eq!(
            reasoning,
            format!("high  {}  medium", app.theme.glyphs.separator())
        );

        app.models
            .iter_mut()
            .find(|model| model.id == "provider-b/model-b")
            .expect("next model")
            .default_reasoning_effort = "high".into();
        let (_, _, reasoning) = app.product_header_metadata();
        assert_eq!(reasoning, "high");

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn resumed_selection_freezes_the_effort_shown_in_the_header() {
        let (mut app, root, server) = production_render_app();
        let mut provider =
            header_provider("provider-b", "Provider B", "provider-b/model-b", "Model B");
        provider.models[0].reasoning_efforts = vec!["medium".into(), "high".into()];
        provider.models[0].default_reasoning_effort = "medium".into();
        app.inventory.providers = vec![provider];
        app.models = app.inventory.available_models();
        app.selected_model.clear();
        app.selected_reasoning_effort.clear();

        let session = Session {
            session_id: "sess-resumed".into(),
            name: String::new(),
            workspace_id: "ws".into(),
            workspace_root: root.to_string_lossy().into_owned(),
            status: "active".into(),
            next_model: "provider-b/model-b".into(),
            next_reasoning_effort: String::new(),
            model_preference_revision: 0,
            plan_mode: false,
            permission_profile: "safe-edit".into(),
            approval_mode: "on_request".into(),
            created_at: String::new(),
            updated_at: String::new(),
            latest_run_id: String::new(),
            latest_run_agent: String::new(),
            latest_run_result_kind: String::new(),
            execution_status: "ready".into(),
            summary: String::new(),
            continuity: None,
        };
        app.sync_selection_from_session(&session);

        assert_eq!(app.model_index, 0);
        assert_eq!(app.selected_model, "provider-b/model-b");
        assert_eq!(app.selected_reasoning_effort, "medium");
        assert_eq!(app.product_header_metadata().2, "medium reasoning");

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn running_header_infers_provider_and_legacy_truth_marks_next_once() {
        let (mut app, root, server) = production_render_app();
        app.inventory.providers = vec![
            header_provider("provider-a", "Provider A", "provider-a/model-a", "Model A"),
            header_provider("provider-b", "Provider B", "provider-b/model-b", "Model B"),
        ];
        app.models = app.inventory.available_models();
        app.selected_model = "provider-b/model-b".into();
        app.selected_reasoning_effort = "medium".into();
        app.execution_status = "paused".into();
        app.active_run_presentation = crate::conversation::ActiveRunPresentation {
            run_id: "run-paused".into(),
            effective_model: "provider-a/model-a".into(),
            ..crate::conversation::ActiveRunPresentation::default()
        };

        let (provider, model, reasoning) = app.product_header_metadata();
        let paused = tr(Locale::En, MessageId::StatusPaused);
        let next = tr(Locale::En, MessageId::NextRun);
        assert!(provider.starts_with("Provider A"), "{provider}");
        assert!(model.contains(&format!("{paused} Model A")), "{model}");
        assert!(model.contains(&format!("{next} Model B")), "{model}");
        assert_eq!(reasoning, "medium");

        app.active_run_presentation = crate::conversation::ActiveRunPresentation {
            run_id: "run-legacy".into(),
            ..crate::conversation::ActiveRunPresentation::default()
        };
        let (provider, model, reasoning) = app.product_header_metadata();
        let metadata = format!("{provider} | {model} | {reasoning}");
        assert!(
            provider.starts_with(&format!("{next} Provider B")),
            "{metadata}"
        );
        assert_eq!(model, "Model B");
        assert_eq!(reasoning, "medium");
        assert_eq!(count_label(&metadata, next), 1, "{metadata}");

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
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
        app.execution_activity.clear();
        if let Some(activity) = match (locale, state) {
            (_, "idle" | "approval") => None,
            (Locale::ZhHans, "running") => Some("正在运行聚焦检查"),
            (Locale::ZhHans, "queued") => Some("正在整理后续工作"),
            (_, "running") => Some("Running focused checks"),
            (_, "queued") => Some("Preparing follow-up work"),
            _ => None,
        } {
            app.execution_activity.set_single(activity);
        }
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
                    assert_eq!(
                        rendered.lines().any(|line| {
                            !line.trim().is_empty()
                                && !line.starts_with("size=")
                                && line.trim() != "styles:"
                        }),
                        state != "idle"
                    );
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
    fn transcript_disclosure_hover_styles_only_the_clickable_header() {
        for glyph_mode in [
            crate::glyphs::GlyphMode::Unicode,
            crate::glyphs::GlyphMode::Ascii,
        ] {
            for color_level in [
                crate::theme::ColorLevel::TrueColor,
                crate::theme::ColorLevel::None,
            ] {
                for expanded in [false, true] {
                    let (mut app, root, server) = production_render_app();
                    app.theme = crate::theme::Theme::new(crate::theme::Polarity::Dark, color_level);
                    app.theme.glyphs = Glyphs::new(glyph_mode);
                    let mut group = read_group(
                        vec![
                            tool_member(
                                "hover-1",
                                "src/transcript.rs",
                                "",
                                "completed",
                                "first detail",
                            ),
                            tool_member(
                                "hover-2",
                                "src/app/render.rs",
                                "",
                                "completed",
                                "second detail",
                            ),
                        ],
                        expanded,
                    );
                    group.id = "hover-disclosure".into();
                    app.blocks = vec![group];

                    let mut terminal =
                        ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 12)).unwrap();
                    terminal
                        .draw(|frame| {
                            app.interactions.begin_frame();
                            app.render_transcript(frame, frame.area());
                        })
                        .unwrap();
                    let before_buffer = terminal.backend().buffer().clone();
                    let before = rendered_frame_contract(&before_buffer);
                    let action = Action::ToggleBlock("hover-disclosure".into());
                    let before_targets = action_positions(&app, before_buffer.area, action.clone());
                    let hover_position = before_targets
                        .first()
                        .copied()
                        .expect("disclosure header should be clickable");
                    assert!(app.interactions.update_hover(hover_position));

                    terminal
                        .draw(|frame| {
                            app.interactions.begin_frame();
                            app.render_transcript(frame, frame.area());
                        })
                        .unwrap();
                    let after_buffer = terminal.backend().buffer();
                    let after = rendered_frame_contract(after_buffer);
                    let after_targets = action_positions(&app, after_buffer.area, action.clone());

                    assert_eq!(visible_contract(&after), visible_contract(&before));
                    assert_eq!(after_targets, before_targets);
                    assert_eq!(
                        app.interactions.action_at(hover_position),
                        Some(action.clone())
                    );

                    let mut changed = Vec::new();
                    for row in before_buffer.area.y..before_buffer.area.bottom() {
                        for column in before_buffer.area.x..before_buffer.area.right() {
                            let position = Position::new(column, row);
                            let before_cell = &before_buffer[position];
                            let after_cell = &after_buffer[position];
                            if (before_cell.fg, before_cell.bg, before_cell.modifier)
                                != (after_cell.fg, after_cell.bg, after_cell.modifier)
                            {
                                changed.push(position);
                            }
                        }
                    }
                    assert!(!changed.is_empty(), "hover must produce visible feedback");
                    assert!(
                        changed
                            .iter()
                            .all(|position| before_targets.contains(position)),
                        "hover styling escaped the disclosure hit region: {changed:?}"
                    );

                    server.join().unwrap();
                    std::fs::remove_dir_all(root).unwrap();
                }
            }
        }
    }

    #[test]
    fn transcript_disclosure_hover_survives_reentry_and_disclosure_reflow() {
        let (mut app, root, server) = production_render_app();
        let mut group = read_group(
            vec![
                tool_member(
                    "hover-repeat-1",
                    "src/transcript.rs",
                    "",
                    "completed",
                    "first detail",
                ),
                tool_member(
                    "hover-repeat-2",
                    "src/app/render.rs",
                    "",
                    "completed",
                    "second detail",
                ),
            ],
            false,
        );
        group.id = "hover-repeat".into();
        app.blocks = vec![group];
        let action = Action::ToggleBlock("hover-repeat".into());
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 18)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let target = action_positions(&app, terminal.backend().buffer().area, action.clone())[0];
        let outside = Position::new(0, 0);
        assert_ne!(app.interactions.action_at(outside), Some(action.clone()));

        let move_pointer = |app: &mut App, position: Position| {
            app.handle_mouse(MouseEvent {
                kind: MouseEventKind::Moved,
                column: position.x,
                row: position.y,
                modifiers: KeyModifiers::NONE,
            });
        };

        move_pointer(&mut app, target);
        assert!(app.dirty);
        terminal.draw(|frame| app.render(frame)).unwrap();
        let first_hover = rendered_frame_contract(terminal.backend().buffer());

        move_pointer(&mut app, outside);
        assert!(app.dirty);
        terminal.draw(|frame| app.render(frame)).unwrap();
        let left = rendered_frame_contract(terminal.backend().buffer());
        assert_ne!(left, first_hover);

        app.dirty = false;
        move_pointer(&mut app, target);
        assert!(app.dirty, "re-entry must request another frame");
        terminal.draw(|frame| app.render(frame)).unwrap();
        assert_eq!(
            rendered_frame_contract(terminal.backend().buffer()),
            first_hover
        );

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: target.x,
            row: target.y,
            modifiers: KeyModifiers::NONE,
        });
        assert!(app.effective_block_expanded(&app.blocks[0]));
        terminal.draw(|frame| app.render(frame)).unwrap();

        move_pointer(&mut app, outside);
        terminal.draw(|frame| app.render(frame)).unwrap();
        let expanded_target =
            action_positions(&app, terminal.backend().buffer().area, action.clone())[0];
        app.dirty = false;
        move_pointer(&mut app, expanded_target);
        assert!(app.dirty, "re-entry after disclosure must request a frame");
        terminal.draw(|frame| app.render(frame)).unwrap();
        let component = ComponentId::stable("transcript", "hover-repeat");
        assert!(app.interactions.hovered(component));
        assert_eq!(
            app.interactions.action_at(expanded_target),
            Some(action.clone())
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn conversation_shell_matrix_keeps_a_quiet_header_and_prompt_aligned_composer() {
        for locale in [Locale::En, Locale::ZhHans] {
            for glyph_mode in [
                crate::glyphs::GlyphMode::Unicode,
                crate::glyphs::GlyphMode::Ascii,
            ] {
                for color_level in [
                    crate::theme::ColorLevel::TrueColor,
                    crate::theme::ColorLevel::None,
                ] {
                    let (mut app, root, server) = production_render_app();
                    app.options.locale = Some(locale.product_id().into());
                    app.theme = crate::theme::Theme::new(crate::theme::Polarity::Dark, color_level);
                    app.theme.glyphs = Glyphs::new(glyph_mode);
                    app.focus = Focus::Composer;

                    for width in [60, 80, 120, 160, 180] {
                        let mut terminal =
                            ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 18))
                                .unwrap();
                        terminal.draw(|frame| app.render(frame)).unwrap();
                        let buffer = terminal.backend().buffer();
                        let rendered = rendered_frame_text(buffer);
                        let header = rendered.lines().nth(1).unwrap_or_default();
                        assert!(header.contains("Carina"), "width={width}\n{rendered}");
                        assert!(
                            header.contains(tr(locale, MessageId::ModeBuildLabel)),
                            "width={width}\n{rendered}"
                        );
                        assert!(
                            header.contains(tr(locale, MessageId::ReasoningOff)),
                            "width={width}\n{rendered}"
                        );
                        for repeated in ["⇧Tab", "Direct API"] {
                            assert!(
                                !header.contains(repeated),
                                "width={width} repeated {repeated:?}\n{rendered}"
                            );
                        }
                        assert!(
                            action_positions(&app, buffer.area, Action::OpenSessions).is_empty(),
                            "width={width}"
                        );
                        assert!(
                            action_positions(&app, buffer.area, Action::OpenSettings).is_empty(),
                            "width={width}"
                        );

                        let reading_axis = layout_contract::transcript_content(buffer.area);
                        assert_eq!(app.composer_area.x, reading_axis.x + 2, "width={width}");
                        assert_eq!(
                            app.composer_area.width,
                            reading_axis.width.saturating_sub(2),
                            "width={width}"
                        );
                        assert_eq!(app.composer_area.height, 1, "width={width}");
                        assert_eq!(app.composer_area.bottom(), buffer.area.bottom());
                        let prompt = Position::new(reading_axis.x, app.composer_area.y);
                        assert_eq!(
                            app.interactions.action_at(prompt),
                            Some(Action::FocusComposer),
                            "width={width}"
                        );
                        assert_eq!(
                            app.interactions.action_at(app.composer_area.as_position()),
                            Some(Action::FocusComposer),
                            "width={width}"
                        );
                        if color_level == crate::theme::ColorLevel::None {
                            let modifier = buffer[prompt].modifier;
                            assert!(modifier.contains(Modifier::BOLD), "width={width}");
                            assert!(modifier.contains(Modifier::REVERSED), "width={width}");
                        }
                        terminal
                            .backend_mut()
                            .assert_cursor_position(app.composer_area.as_position());
                    }

                    server.join().unwrap();
                    std::fs::remove_dir_all(root).unwrap();
                }
            }
        }
    }

    #[test]
    fn composer_pointer_places_cursor_and_completes_drag_selection() {
        let (mut app, root, server) = production_render_app();
        app.composer.set_text("alpha beta gamma");
        app.focus = Focus::Composer;
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 18)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let input = app.composer_area;

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: input.x + 6,
            row: input.y,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(app.composer.cursor(), 6);

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Drag(MouseButton::Left),
            column: input.x + 10,
            row: input.y,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(app.composer.selection_range(), Some(6..10));
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Up(MouseButton::Left),
            column: input.x + 10,
            row: input.y,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(app.composer.selection_range(), Some(6..10));

        let cursor_before_prompt_click = app.composer.cursor();
        app.focus = Focus::Scene;
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: input.x - 1,
            row: input.y,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(app.focus, Focus::Composer);
        assert_eq!(app.composer.cursor(), cursor_before_prompt_click);

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn slash_completion_inner_text_aligns_with_the_composer_on_wide_terminals() {
        for width in [120, 160, 180] {
            let (mut app, root, server) = production_render_app();
            app.composer.set_text("/");
            app.focus = Focus::Composer;
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 24)).unwrap();
            terminal.draw(|frame| app.render(frame)).unwrap();

            let first_suggestion = (0..app.composer_area.y)
                .flat_map(|y| (0..width).map(move |x| Position::new(x, y)))
                .filter(|position| {
                    matches!(
                        app.interactions.action_at(*position),
                        Some(Action::SelectSlashCommand { .. })
                    )
                })
                .min_by_key(|position| (position.y, position.x))
                .expect("slash completion row");
            assert_eq!(first_suggestion.x, app.composer_area.x, "width={width}");

            server.join().unwrap();
            std::fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn context_and_history_completion_rows_align_with_the_composer() {
        for width in [120, 160, 180] {
            let (mut app, root, server) = production_render_app();
            app.composer.set_text("@src");
            app.composer.set_cursor(app.composer.text().len());
            assert!(app.context_completion.update_context(&app.composer));
            let generation = app.context_completion.begin_load("session".into());
            assert!(app.context_completion.apply_load(
                generation,
                "session",
                Ok(vec![crate::rpc::WorkspaceFile {
                    path: "src/app/render.rs".into(),
                    language: "rust".into(),
                    size: 42,
                    binary: false,
                    large: false,
                    mtime: 0,
                }]),
            ));
            app.focus = Focus::Composer;
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 24)).unwrap();
            terminal.draw(|frame| app.render(frame)).unwrap();
            let context_row = (0..app.composer_area.y)
                .flat_map(|y| (0..width).map(move |x| Position::new(x, y)))
                .find(|position| {
                    app.interactions.action_at(*position) == Some(Action::SelectFileCandidate(0))
                })
                .expect("context completion row");
            assert_eq!(context_row.x, app.composer_area.x, "width={width}");

            app.context_completion.dismiss(&app.composer);
            app.history_search = Some(HistorySearchState::activate(
                vec!["inspect shell geometry".into()],
                String::new(),
                false,
            ));
            terminal.draw(|frame| app.render(frame)).unwrap();
            let history_row = (0..app.composer_area.y)
                .flat_map(|y| (0..width).map(move |x| Position::new(x, y)))
                .find(|position| {
                    app.interactions.action_at(*position) == Some(Action::SelectPromptHistory(0))
                })
                .expect("history completion row");
            assert_eq!(history_row.x, app.composer_area.x, "width={width}");

            server.join().unwrap();
            std::fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn conversation_shell_handles_extreme_terminal_widths_without_overflow() {
        for width in 1..=12 {
            let (mut app, root, server) = production_render_app();
            app.composer.set_text("narrow");
            app.focus = Focus::Composer;
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 8)).unwrap();
            terminal.draw(|frame| app.render(frame)).unwrap();
            let area = terminal.backend().buffer().area;
            assert!(app.composer_area.right() <= area.right(), "width={width}");
            assert!(app.composer_area.bottom() <= area.bottom(), "width={width}");
            assert!(
                app.transcript_geometry.viewport.right() <= area.right(),
                "width={width}"
            );
            assert!(
                app.transcript_geometry.viewport.bottom() <= area.bottom(),
                "width={width}"
            );

            server.join().unwrap();
            std::fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn short_conversation_preserves_transcript_before_growing_the_composer() {
        for height in [8, 12, 16] {
            let (mut app, root, server) = production_render_app();
            app.composer.set_text("one\ntwo\nthree\nfour\nfive\nsix");
            app.focus = Focus::Composer;
            app.execution_status = "running".into();
            app.execution_activity.set_single("Running checks");
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, height)).unwrap();
            terminal.draw(|frame| app.render(frame)).unwrap();

            assert!(
                app.transcript_geometry.viewport.height
                    >= layout_contract::CONVERSATION_MIN_TRANSCRIPT_HEIGHT,
                "height={height} transcript={:?} composer={:?}",
                app.transcript_geometry.viewport,
                app.composer_area
            );
            assert_eq!(app.composer_area.bottom(), height);
            assert!(
                app.composer_area.height <= layout_contract::COMPOSER_MAX_HEIGHT,
                "height={height} composer={:?}",
                app.composer_area
            );
            if height == 8 {
                assert_eq!(app.transcript_geometry.viewport.y, 0);
                assert_eq!(app.composer_area.height, 1);
            }

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
        app.execution_activity.set_single("Running checks");
        terminal.draw(|frame| app.render(frame)).unwrap();
        assert_eq!(app.composer_area, idle_composer);
        app.notice = crate::i18n::Notice::localized(MessageId::RuntimeUnavailable);
        terminal.draw(|frame| app.render(frame)).unwrap();
        assert_eq!(app.composer_area, idle_composer);
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn state_derived_chrome_keeps_the_composer_bottom_anchored() {
        for width in [80, 120, 160] {
            let (mut app, root, server) = production_render_app();
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 28)).unwrap();

            app.execution_status = "ready".into();
            let idle_chrome_rows = app.composer_chrome().row_count(width);
            terminal.draw(|frame| app.render(frame)).unwrap();
            let idle_composer = app.composer_area;
            let idle_transcript = app.transcript_geometry.viewport;

            app.execution_status = "running".into();
            app.execution_activity.set_single("Running checks");
            let running_chrome_rows = app.composer_chrome().row_count(width);
            terminal.draw(|frame| app.render(frame)).unwrap();
            let running_transcript = app.transcript_geometry.viewport;
            assert_eq!(app.composer_area, idle_composer);
            assert_eq!(running_transcript.x, idle_transcript.x);
            assert_eq!(running_transcript.width, idle_transcript.width);
            assert_eq!(
                idle_transcript
                    .bottom()
                    .saturating_sub(running_transcript.bottom()),
                running_chrome_rows.saturating_sub(idle_chrome_rows)
            );

            app.execution_status = "waiting_approval".into();
            app.execution_activity.clear();
            let waiting_chrome_rows = app.composer_chrome().row_count(width);
            terminal.draw(|frame| app.render(frame)).unwrap();
            assert_eq!(app.composer_area, idle_composer);
            assert_eq!(
                idle_transcript
                    .bottom()
                    .saturating_sub(app.transcript_geometry.viewport.bottom()),
                waiting_chrome_rows.saturating_sub(idle_chrome_rows)
            );

            server.join().unwrap();
            std::fs::remove_dir_all(root).unwrap();
        }
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
    fn transcript_mouse_scroll_is_owned_by_content_not_the_outer_canvas() {
        let (mut app, root, server) = production_render_app();
        app.blocks = vec![block(
            BlockKind::Assistant,
            &(1..=80)
                .map(|line| format!("row {line}"))
                .collect::<Vec<_>>()
                .join("\n"),
        )];
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(160, 12)).unwrap();
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let middle = app.transcript_max_scroll / 2;
        app.set_transcript_scroll(middle);
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let geometry = app.transcript_geometry;

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::ScrollDown,
            column: geometry.viewport.x,
            row: geometry.content.y,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(app.transcript_scroll, middle);

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::ScrollDown,
            column: geometry.content.x,
            row: geometry.content.y,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(app.transcript_scroll, middle + 3);

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn transcript_scrollbar_pointer_uses_the_rendered_track_and_drag_capture() {
        let (mut app, root, server) = production_render_app();
        app.blocks = vec![block(
            BlockKind::Assistant,
            &(1..=80)
                .map(|line| format!("row {line}"))
                .collect::<Vec<_>>()
                .join("\n"),
        )];
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(160, 12)).unwrap();
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        app.set_transcript_scroll(0);
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();

        let scrollbar = app.transcript_scrollbar;
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: scrollbar.end.x,
            row: scrollbar.end.y,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(app.transcript_scroll, 3);

        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let thumb = app.transcript_scrollbar.thumb.as_position();
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: thumb.x,
            row: thumb.y,
            modifiers: KeyModifiers::NONE,
        });
        assert!(app.transcript_scrollbar_interaction.is_dragging());
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Drag(MouseButton::Left),
            column: thumb.x,
            row: u16::MAX,
            modifiers: KeyModifiers::NONE,
        });
        assert_eq!(app.transcript_scroll, app.transcript_max_scroll);
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Up(MouseButton::Left),
            column: thumb.x,
            row: u16::MAX,
            modifiers: KeyModifiers::NONE,
        });
        assert!(!app.transcript_scrollbar_interaction.is_dragging());

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
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
            todo_items: Vec::new(),
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

    fn scrollable_blocks() -> Vec<TranscriptBlock> {
        let mut tool = block(
            BlockKind::Tool,
            "first line\nsecond line\nthird line\nfourth line",
        );
        tool.id = "geometry-tool".into();
        let mut blocks = vec![tool];
        blocks.extend((0..80).map(|index| {
            let mut block = block(
                BlockKind::Assistant,
                &format!("response {index}\ncontinued evidence {index}"),
            );
            block.id = format!("response-{index}");
            block
        }));
        blocks
    }

    fn action_positions(app: &App, area: Rect, action: Action) -> Vec<Position> {
        (area.y..area.bottom())
            .flat_map(|row| (area.x..area.right()).map(move |column| Position::new(column, row)))
            .filter(|position| app.interactions.action_at(*position) == Some(action.clone()))
            .collect()
    }

    #[test]
    fn product_menu_pointer_contract_opens_routes_and_closes_outside() {
        let (mut app, root, server) = production_render_app();
        let draft = "keep this draft";
        app.composer.set_text(draft);
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 32)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let trigger = action_positions(
            &app,
            terminal.backend().buffer().area,
            Action::ToggleProductMenu,
        )[0];

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: trigger.x,
            row: trigger.y,
            modifiers: KeyModifiers::NONE,
        });
        terminal.draw(|frame| app.render(frame)).unwrap();
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ProductMenu(_))
        ));
        assert_eq!(app.composer.text(), draft);
        for action in [
            Action::CreateSession,
            Action::OpenSessions,
            Action::OpenStatus,
            Action::OpenSettings,
            Action::OpenHelp,
        ] {
            assert!(!action_positions(&app, terminal.backend().buffer().area, action).is_empty());
        }
        assert!(
            action_positions(
                &app,
                terminal.backend().buffer().area,
                Action::FocusComposer,
            )
            .is_empty(),
            "the open menu must hide background hit regions"
        );

        let trigger_open = action_positions(
            &app,
            terminal.backend().buffer().area,
            Action::ToggleProductMenu,
        )[0];
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: trigger_open.x,
            row: trigger_open.y,
            modifiers: KeyModifiers::NONE,
        });
        assert!(app.overlays.active().is_none());

        app.apply_action(Action::ToggleProductMenu);
        terminal.draw(|frame| app.render(frame)).unwrap();
        let outside = Position::new(119, 31);
        assert_eq!(
            app.interactions.action_at(outside),
            Some(Action::CloseOverlay)
        );
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: outside.x,
            row: outside.y,
            modifiers: KeyModifiers::NONE,
        });
        assert!(app.overlays.active().is_none());
        assert_eq!(app.composer.text(), draft);

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn product_menu_keyboard_navigation_uses_the_same_typed_actions() {
        let (mut app, root, server) = production_render_app();
        app.apply_action(Action::ToggleProductMenu);
        app.handle_overlay_key(KeyEvent::from(KeyCode::End));
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ProductMenu(ProductMenuOverlay { selected: 4 }))
        ));
        app.handle_overlay_key(KeyEvent::from(KeyCode::Enter));
        assert!(matches!(app.overlays.active(), Some(Overlay::Help(_))));

        app.close_top_non_governance();
        app.apply_action(Action::ToggleProductMenu);
        app.handle_overlay_key(KeyEvent::from(KeyCode::Esc));
        assert!(app.overlays.active().is_none());

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn product_menu_goldens_cover_locale_and_glyph_fallback() {
        for (locale_name, locale) in [("en", Locale::En), ("zh_hans", Locale::ZhHans)] {
            for (glyph_name, glyph_mode) in [
                ("unicode", crate::glyphs::GlyphMode::Unicode),
                ("ascii", crate::glyphs::GlyphMode::Ascii),
            ] {
                let (mut app, root, server) = production_render_app();
                app.options.workspace = std::path::PathBuf::from("/tmp/product-menu");
                app.options.locale = Some(locale.product_id().into());
                app.theme = crate::theme::Theme::new(
                    crate::theme::Polarity::Dark,
                    crate::theme::ColorLevel::TrueColor,
                );
                app.theme.glyphs = app.theme.glyphs.with_mode(glyph_mode);
                app.apply_action(Action::ToggleProductMenu);
                let mut terminal =
                    ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 24)).unwrap();
                terminal.draw(|frame| app.render(frame)).unwrap();
                insta::assert_snapshot!(
                    format!("product_menu_{locale_name}_{glyph_name}"),
                    rendered_frame_contract(terminal.backend().buffer())
                );

                server.join().unwrap();
                std::fs::remove_dir_all(root).unwrap();
            }
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

        let mut group = exploration_group(false);
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
        failure.collapsible = true;
        failure.expanded = false;
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
            model: "provider/original-model".into(),
            current_model: "provider/current-model".into(),
            retry_root_run_id: "run-failure".into(),
            attempt_count: 2,
            focused_action: None,
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
            ["Shell 命令需要批准", "添加类型化骨架", "执行失败"]
        } else {
            [
                "Shell command needs approval",
                "Add typed skeletons",
                "Execution failed",
            ]
        }
    }

    fn gallery_geometry_anchors(locale: Locale) -> [&'static str; 4] {
        if locale == Locale::ZhHans {
            [
                "我会让常规操作保持安静",
                "检查投影边界",
                "Shell 命令需要批准",
                "执行失败",
            ]
        } else {
            [
                "I will keep routine work quiet",
                "Inspecting the projection boundary",
                "Shell command needs approval",
                "Execution failed",
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

    fn exploration_group(expanded: bool) -> TranscriptBlock {
        let members = [
            ("read", "read", "README.md", ""),
            ("list", "list", "workspace", ""),
            (
                "map",
                "code.map",
                "core modules",
                "symbol graph\nentry -> runtime",
            ),
            ("search", "search", "runtime/go", "main.go:42"),
            ("read-again", "read", "README.md", ""),
        ]
        .into_iter()
        .map(|(id, tool_name, title, body)| ToolGroupMember {
            id: format!("tool:{id}"),
            tool_name: tool_name.into(),
            title: title.into(),
            body: body.into(),
            body_kind: if tool_name.starts_with("code.") {
                BlockBodyKind::CodeIntelligence
            } else {
                BlockBodyKind::Plain
            },
            additions: 0,
            deletions: 0,
            status: String::new(),
            lifecycle: "completed".into(),
        })
        .collect();
        let mut group = read_group(members, expanded);
        group.id = "exploration:group".into();
        group.run_id = "run-explore".into();
        group.tool_kind = Some(crate::tool_projection::ToolKind::Explore);
        group.collapsible = true;
        group
    }

    #[test]
    fn exploration_group_is_quiet_by_default_and_complete_when_expanded() {
        for width in [80, 120, 160] {
            let collapsed = transcript_lines(
                &exploration_group(false),
                Locale::En,
                TranscriptStyles::default(),
                width,
            );
            let collapsed = plain(&collapsed).join("\n");
            assert!(collapsed.contains("Explored ×5 · README.md, workspace, …"));
            assert_eq!(collapsed.matches("README.md").count(), 1);
            assert!(!collapsed.contains("runtime/go"));

            let expanded = transcript_lines(
                &exploration_group(true),
                Locale::En,
                TranscriptStyles::default(),
                width,
            );
            let expanded = plain(&expanded).join("\n");
            for evidence in [
                "Read",
                "List",
                "Map",
                "Search",
                "symbol graph",
                "main.go:42",
            ] {
                assert!(
                    expanded.contains(evidence),
                    "{width}-column expansion lost {evidence:?}:\n{expanded}"
                );
            }
            assert_eq!(expanded.matches("README.md").count(), 3);
        }
    }

    fn visible_contract(contract: &str) -> &str {
        contract.split("styles:\n").next().unwrap_or(contract)
    }

    fn normalized_visible_copy(contract: &str) -> String {
        visible_contract(contract)
            .lines()
            .map(|line| {
                line.trim().trim_matches(|character| {
                    matches!(character, '│' | '┃' | '|' | '╎' | '┆' | '┊')
                })
            })
            .flat_map(str::chars)
            .filter(|character| !character.is_whitespace())
            .collect()
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

    #[allow(clippy::too_many_arguments)]
    fn render_symbols_preview(
        width: u16,
        height: u16,
        locale: Locale,
        preference: GlyphPreference,
        glyph_mode: GlyphMode,
        source: GlyphSource,
        polarity: crate::theme::Polarity,
        color_level: crate::theme::ColorLevel,
    ) -> (String, Vec<Position>) {
        let (mut app, root, server) = production_render_app();
        app.options.workspace = std::path::PathBuf::from("/workspace/carina");
        app.options.locale = Some(locale.product_id().into());
        app.locale_index = Locale::ALL
            .iter()
            .position(|candidate| *candidate == locale)
            .unwrap_or_default();
        app.glyph_preference = preference;
        app.glyph_resolution = crate::glyphs::ResolvedGlyphs {
            preference,
            mode: glyph_mode,
            source,
        };
        app.theme = crate::theme::Theme::new(polarity, color_level);
        app.theme.glyphs = Glyphs::new(glyph_mode);
        app.overlays
            .replace(Overlay::Settings(SettingsOverlay::symbols(preference)));

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, height)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let rendered = rendered_frame_contract(terminal.backend().buffer());
        let actions = GlyphPreference::ALL
            .into_iter()
            .map(Action::PreviewGlyphPreference)
            .chain([Action::ApplyGlyphPreference, Action::CancelGlyphPreview])
            .map(|action| {
                (0..height)
                    .flat_map(|row| (0..width).map(move |column| Position::new(column, row)))
                    .find(|position| app.interactions.action_at(*position) == Some(action.clone()))
                    .unwrap_or_else(|| panic!("missing Symbols action {action:?}"))
            })
            .collect();
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
        (rendered, actions)
    }

    fn assert_symbols_color_contract(contract: &str, level: crate::theme::ColorLevel) {
        match level {
            crate::theme::ColorLevel::TrueColor => assert!(contract.contains("Rgb(")),
            crate::theme::ColorLevel::Ansi256 => {
                assert!(contract.contains("Indexed("));
                assert!(!contract.contains("Rgb("));
            }
            crate::theme::ColorLevel::Basic => {
                assert!(!contract.contains("Rgb("));
                assert!(!contract.contains("Indexed("));
                assert!(
                    ["Red", "Green", "Yellow", "Blue", "Magenta", "Cyan"]
                        .into_iter()
                        .any(|name| contract.contains(name))
                );
            }
            crate::theme::ColorLevel::None => {
                for encoded in [
                    "Rgb(", "Indexed(", "Black", "Red", "Green", "Yellow", "Blue", "Magenta",
                    "Cyan", "Gray", "White",
                ] {
                    assert!(
                        !contract.contains(encoded),
                        "no-color Symbols surface leaked {encoded}"
                    );
                }
            }
        }
    }

    #[test]
    fn symbols_preview_goldens_cover_en_and_zh_hans_at_product_widths() {
        for (locale_name, locale) in [("en", Locale::En), ("zh_hans", Locale::ZhHans)] {
            for width in [80, 120, 160] {
                let (rendered, _) = render_symbols_preview(
                    width,
                    24,
                    locale,
                    GlyphPreference::Auto,
                    GlyphMode::Unicode,
                    GlyphSource::AutomaticDefault,
                    crate::theme::Polarity::Dark,
                    crate::theme::ColorLevel::TrueColor,
                );
                for required in [
                    tr(locale, MessageId::Symbols),
                    tr(locale, MessageId::SymbolsAutomatic),
                    tr(locale, MessageId::SymbolsUnicode),
                    tr(locale, MessageId::SymbolsNerdFont),
                    tr(locale, MessageId::SymbolsAscii),
                    tr(locale, MessageId::SymbolsApplyHint),
                    tr(locale, MessageId::SymbolsKeepCurrentHint),
                ] {
                    assert!(
                        visible_contract(&rendered).contains(required),
                        "{width}-column {locale_name} Symbols preview cropped {required:?}"
                    );
                }
                assert!(rendered.contains("Rgb("));
                insta::assert_snapshot!(format!("symbols_preview_{locale_name}_{width}"), rendered);
            }
        }
    }

    #[test]
    fn short_symbols_preview_keeps_choices_sample_information_and_actions() {
        for locale in [Locale::En, Locale::ZhHans] {
            let (rendered, actions) = render_symbols_preview(
                80,
                16,
                locale,
                GlyphPreference::Auto,
                GlyphMode::Unicode,
                GlyphSource::AutomaticDefault,
                crate::theme::Polarity::Dark,
                crate::theme::ColorLevel::TrueColor,
            );
            let visible = visible_contract(&rendered);
            for required in [
                tr(locale, MessageId::SymbolsAutomatic),
                tr(locale, MessageId::SymbolsUnicode),
                tr(locale, MessageId::SymbolsNerdFont),
                tr(locale, MessageId::SymbolsAscii),
                tr(locale, MessageId::Done),
                tr(locale, MessageId::ExecutionWorking),
                tr(locale, MessageId::SymbolsApplyHint),
                tr(locale, MessageId::SymbolsKeepCurrentHint),
            ] {
                assert!(
                    visible.contains(required),
                    "short Symbols lost {required:?}"
                );
            }
            let requested = tr_format(
                locale,
                MessageId::SymbolsRequested,
                &[(
                    "preference",
                    glyph_preference_label(locale, GlyphPreference::Auto),
                )],
            );
            let effective = tr_format(
                locale,
                MessageId::SymbolsEffective,
                &[("mode", glyph_mode_label(locale, GlyphMode::Unicode))],
            );
            assert!(visible.contains(&requested));
            assert!(visible.contains(&effective));
            assert_eq!(actions.len(), 6);
            assert!(
                !visible.contains(&format!(
                    "{}{}",
                    Glyphs::new(GlyphMode::Unicode).disclosure_closed(),
                    tr(locale, MessageId::Close)
                )),
                "short mode must collapse the secondary sample line first"
            );
        }
    }

    #[test]
    fn symbols_preview_discloses_environment_and_automatic_fallback_owners() {
        for locale in [Locale::En, Locale::ZhHans] {
            let (environment, _) = render_symbols_preview(
                80,
                24,
                locale,
                GlyphPreference::Nerd,
                GlyphMode::Ascii,
                GlyphSource::EnvironmentOverride,
                crate::theme::Polarity::Dark,
                crate::theme::ColorLevel::TrueColor,
            );
            let environment_notice = tr_format(
                locale,
                MessageId::SymbolsEnvironmentOverride,
                &[("mode", glyph_mode_label(locale, GlyphMode::Ascii))],
            );
            let visible_environment = normalized_visible_copy(&environment);
            let environment_notice = environment_notice
                .chars()
                .filter(|character| !character.is_whitespace())
                .collect::<String>();
            assert!(
                visible_environment.contains(&environment_notice),
                "expected {environment_notice:?} in:\n{environment}"
            );
            assert!(visible_contract(&environment).contains(&tr_format(
                locale,
                MessageId::SymbolsRequested,
                &[(
                    "preference",
                    glyph_preference_label(locale, GlyphPreference::Nerd),
                )],
            )));

            let (automatic, _) = render_symbols_preview(
                80,
                24,
                locale,
                GlyphPreference::Auto,
                GlyphMode::Ascii,
                GlyphSource::AutomaticLegacyTerminal,
                crate::theme::Polarity::Dark,
                crate::theme::ColorLevel::TrueColor,
            );
            let visible_automatic = normalized_visible_copy(&automatic);
            let fallback = tr(locale, MessageId::SymbolsAutoFallback)
                .chars()
                .filter(|character| !character.is_whitespace())
                .collect::<String>();
            assert!(visible_automatic.contains(&fallback));
        }
    }

    #[test]
    fn symbols_preview_matrix_preserves_information_and_action_geometry() {
        for width in [80, 120, 160] {
            for locale in [Locale::En, Locale::ZhHans] {
                let (_, baseline_actions) = render_symbols_preview(
                    width,
                    24,
                    locale,
                    GlyphPreference::Unicode,
                    GlyphMode::Unicode,
                    GlyphSource::PersistedPreference,
                    crate::theme::Polarity::Dark,
                    crate::theme::ColorLevel::TrueColor,
                );
                for (preference, mode) in [
                    (GlyphPreference::Unicode, GlyphMode::Unicode),
                    (GlyphPreference::Nerd, GlyphMode::Nerd),
                    (GlyphPreference::Ascii, GlyphMode::Ascii),
                ] {
                    for (polarity, color_level) in [
                        (
                            crate::theme::Polarity::Dark,
                            crate::theme::ColorLevel::TrueColor,
                        ),
                        (
                            crate::theme::Polarity::Dark,
                            crate::theme::ColorLevel::Ansi256,
                        ),
                        (
                            crate::theme::Polarity::Dark,
                            crate::theme::ColorLevel::Basic,
                        ),
                        (crate::theme::Polarity::Dark, crate::theme::ColorLevel::None),
                        (
                            crate::theme::Polarity::Light,
                            crate::theme::ColorLevel::TrueColor,
                        ),
                    ] {
                        let (rendered, actions) = render_symbols_preview(
                            width,
                            24,
                            locale,
                            preference,
                            mode,
                            GlyphSource::PersistedPreference,
                            polarity,
                            color_level,
                        );
                        let visible = visible_contract(&rendered);
                        assert!(!visible.contains('\u{fffd}'));
                        assert!(
                            visible
                                .chars()
                                .all(|character| { character == '\n' || !character.is_control() })
                        );
                        for required in [
                            tr(locale, MessageId::SymbolsAutomatic),
                            tr(locale, MessageId::SymbolsUnicode),
                            tr(locale, MessageId::SymbolsNerdFont),
                            tr(locale, MessageId::SymbolsAscii),
                            tr(locale, MessageId::SymbolsAutomaticDetail),
                            tr(locale, MessageId::SymbolsUnicodeDetail),
                            tr(locale, MessageId::SymbolsNerdFontDetail),
                            tr(locale, MessageId::SymbolsAsciiDetail),
                        ] {
                            assert!(visible.contains(required), "missing {required:?}");
                        }
                        let requested = tr_format(
                            locale,
                            MessageId::SymbolsRequested,
                            &[("preference", glyph_preference_label(locale, preference))],
                        );
                        let effective = tr_format(
                            locale,
                            MessageId::SymbolsEffective,
                            &[("mode", glyph_mode_label(locale, mode))],
                        );
                        assert!(visible.contains(&requested), "missing {requested:?}");
                        assert!(visible.contains(&effective), "missing {effective:?}");
                        assert_eq!(
                            actions, baseline_actions,
                            "{width}-column {locale:?} {mode:?}/{polarity:?}/{color_level:?} moved Symbols actions"
                        );
                        assert_symbols_color_contract(&rendered, color_level);
                    }
                }
            }
        }
    }

    #[test]
    fn symbols_preview_is_live_reversible_and_persists_only_on_apply() {
        let (mut app, root, server) = production_render_app();
        let config = root.join("config.json");
        let original_config = br#"{"max_concurrent_tasks":4,"tui_locale":"en"}"#;
        std::fs::write(&config, original_config).unwrap();
        app.options.glyphs_path = Some(config.clone());
        app.options.screen_mode = Some(ScreenMode::Inline);
        app.glyph_preference = GlyphPreference::Unicode;
        app.apply_glyph_resolution(crate::glyphs::ResolvedGlyphs {
            preference: GlyphPreference::Unicode,
            mode: GlyphMode::Unicode,
            source: GlyphSource::PersistedPreference,
        });
        app.composer.set_text("retain this operator draft");
        app.history_selected = Some(0);
        app.tool_disclosure_overrides
            .insert("symbols:todo".into(), true);
        let mut todo = block(BlockKind::Tool, "");
        todo.id = "symbols:todo".into();
        todo.tool_kind = Some(crate::tool_projection::ToolKind::Todo);
        todo.title = crate::tool_projection::format_tool_title("Plan", "release checks");
        todo.todo_items = vec![
            crate::tool_projection::TodoItem {
                content: "Keep retained semantic state glyph-free".into(),
                status: "completed".into(),
            },
            crate::tool_projection::TodoItem {
                content: "Redraw existing rows live".into(),
                status: "in_progress".into(),
            },
        ];
        app.blocks = vec![todo];

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 20)).unwrap();
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let unicode_frame = rendered_frame_text(terminal.backend().buffer());
        assert!(unicode_frame.contains(Glyphs::new(GlyphMode::Unicode).success()));
        assert!(!app.transcript_height_cache.is_empty());
        assert!(!app.transcript_render_cache.is_empty());

        assert_eq!(app.handle_slash_command("/symbols"), Some(true));
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::Settings(SettingsOverlay {
                page: SettingsPage::Symbols,
                ..
            }))
        ));
        app.apply_action(Action::PreviewGlyphPreference(GlyphPreference::Nerd));
        assert_eq!(app.glyph_preference, GlyphPreference::Unicode);
        assert_eq!(app.glyph_resolution.mode, GlyphMode::Nerd);
        assert_eq!(app.theme.glyphs.mode, GlyphMode::Nerd);
        assert!(app.transcript_height_cache.is_empty());
        assert!(app.transcript_render_cache.is_empty());
        assert_eq!(app.composer.text(), "retain this operator draft");
        assert_eq!(app.history_selected, Some(0));
        assert_eq!(app.options.screen_mode, Some(ScreenMode::Inline));
        assert_eq!(
            app.tool_disclosure_overrides.get("symbols:todo"),
            Some(&true)
        );

        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let nerd_frame = rendered_frame_text(terminal.backend().buffer());
        assert!(nerd_frame.contains(Glyphs::new(GlyphMode::Nerd).success()));
        assert!(!nerd_frame.contains(Glyphs::new(GlyphMode::Unicode).success()));

        app.apply_action(Action::CancelGlyphPreview);
        assert_eq!(app.glyph_preference, GlyphPreference::Unicode);
        assert_eq!(app.glyph_resolution.mode, GlyphMode::Unicode);
        assert_eq!(std::fs::read(&config).unwrap(), original_config);
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::Settings(SettingsOverlay {
                page: SettingsPage::Root,
                ..
            }))
        ));

        app.apply_action(Action::OpenGlyphPreview);
        app.apply_action(Action::PreviewGlyphPreference(GlyphPreference::Nerd));
        assert!(app.close_top_non_governance());
        assert!(app.overlays.active().is_none());
        assert_eq!(app.glyph_preference, GlyphPreference::Unicode);
        assert_eq!(app.glyph_resolution.mode, GlyphMode::Unicode);
        assert_eq!(std::fs::read(&config).unwrap(), original_config);

        app.apply_action(Action::OpenGlyphPreview);
        app.apply_action(Action::PreviewGlyphPreference(GlyphPreference::Ascii));
        app.apply_action(Action::ApplyGlyphPreference);
        assert_eq!(app.glyph_preference, GlyphPreference::Ascii);
        assert_eq!(app.glyph_resolution.mode, GlyphMode::Ascii);
        assert_eq!(app.theme.glyphs.mode, GlyphMode::Ascii);
        assert!(app.notice.is_localized(MessageId::SymbolsApplied));
        let persisted: serde_json::Value =
            serde_json::from_slice(&std::fs::read(&config).unwrap()).unwrap();
        assert_eq!(persisted["max_concurrent_tasks"], 4);
        assert_eq!(persisted["tui_locale"], "en");
        assert_eq!(persisted["tui_glyphs"], "ascii");
        assert_eq!(app.composer.text(), "retain this operator draft");
        assert_eq!(app.blocks[0].todo_items.len(), 2);

        app.options.glyphs_path = Some(root.clone());
        app.apply_action(Action::OpenGlyphPreview);
        app.apply_action(Action::PreviewGlyphPreference(GlyphPreference::Unicode));
        app.apply_action(Action::ApplyGlyphPreference);
        assert_eq!(
            app.glyph_preference,
            GlyphPreference::Ascii,
            "failed persistence must not commit the previewed preference"
        );
        assert!(app.notice.is_localized(MessageId::SymbolsPersistFailed));
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::Settings(SettingsOverlay {
                page: SettingsPage::Symbols,
                ..
            }))
        ));

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
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
            model_preference_revision: 0,
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
    fn disabled_grok_build_is_not_rendered_as_a_failed_probe() {
        let (mut app, root, server) = production_render_app();
        app.phase = Phase::Provider;
        let provider = &mut app.inventory.providers[0];
        provider.id = "grok-build".into();
        provider.name = "Grok Build".into();
        provider.registered = false;
        provider.available = false;
        provider.source_kind = "grok-build".into();
        provider.source_action = "disabled".into();
        provider.models.clear();

        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 40)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let rendered = rendered_frame_text(terminal.backend().buffer());

        assert!(
            rendered.contains(tr(Locale::En, MessageId::GrokBuildDisabled)),
            "{rendered}"
        );
        assert!(
            rendered.contains("Enable Grok Build in Carina's"),
            "{rendered}"
        );
        assert!(
            !rendered.contains(tr(Locale::En, MessageId::GrokBuildCheckFailed)),
            "{rendered}"
        );
        let (row, line) = rendered
            .lines()
            .enumerate()
            .filter(|(_, line)| line.contains("Disabled"))
            .last()
            .expect("disabled action label");
        let byte = line.find("Disabled").expect("disabled action column");
        let column = UnicodeWidthStr::width(&line[..byte]);
        assert_eq!(
            app.interactions
                .action_at(Position::new(column as u16, row as u16)),
            None,
            "disabled Grok Build must not expose a retry action"
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
        assert!(!app.effective_block_expanded(&app.blocks[0]));
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
        assert!(app.effective_block_expanded(&app.blocks[0]));
        app.apply_action(Action::ToggleDensity);
        assert_eq!(app.density, DensityMode::Compact);
        assert!(app.effective_block_expanded(&app.blocks[0]));

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
            let composer = truncate_cells("继续检查工作区状态", width, Glyphs::default());
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
            model: "provider/original-model".into(),
            current_model: "provider/current-model".into(),
            retry_root_run_id: "run-1".into(),
            attempt_count: 2,
            focused_action: None,
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
            assert!(visible.iter().any(|line| line.contains("用当前模型重试")));
            assert!(visible.iter().any(|line| line.contains("重放原配置")));
            assert!(visible.iter().all(|line| !line.contains("Alt-")));
            assert!(visible.iter().all(|line| !line.contains('✗')));
        }
    }

    #[test]
    fn failure_action_rows_fit_the_supported_narrow_width_in_every_locale() {
        let failure = FailurePresentation {
            kind: crate::transcript::FailureKind::Failed,
            action: FailureAction::Retry,
            owner: "Carina".into(),
            reason: "Provider unavailable".into(),
            source_event_id: "event-1".into(),
            run_id: "run-2".into(),
            model: "provider/original-model".into(),
            current_model: "provider/current-model".into(),
            retry_root_run_id: "run-1".into(),
            attempt_count: 2,
            focused_action: None,
        };
        for locale in Locale::ALL {
            let actions = failure_action_layout(locale, &failure, false);
            let width = actions
                .last()
                .map(|action| action.start_cell + action.width)
                .unwrap_or_default();
            assert!(
                width + UnicodeWidthStr::width(Glyphs::default().tool_gutter()) <= 80,
                "failure actions overflow at 80 columns for {locale:?}: {actions:?}"
            );
        }
    }

    #[test]
    fn failure_actions_use_primary_secondary_and_tertiary_semantic_styles() {
        let theme = crate::theme::Theme::new(
            crate::theme::Polarity::Dark,
            crate::theme::ColorLevel::TrueColor,
        );
        let mut block = actionable_failure_block();
        block.failure.as_mut().expect("failure").current_model = "provider/current-model".into();
        let styles = TranscriptStyles {
            label: theme.transcript_danger(),
            text: Style::default().fg(theme.text),
            metadata: theme.transcript_metadata(),
            link: theme.transcript_link(),
            ..TranscriptStyles::default()
        };
        let action_styles = |block: &TranscriptBlock| {
            let lines = transcript_lines(block, Locale::En, styles, 120);
            let action_line = lines.last().expect("failure action line");
            action_line
                .spans
                .iter()
                .filter(|span| !span.content.trim().is_empty())
                .map(|span| (span.content.to_string(), span.style))
                .collect::<Vec<_>>()
        };

        let actions = action_styles(&block);
        let style_for = |label: &str| {
            actions
                .iter()
                .find(|(content, _)| content.contains(label))
                .map(|(_, style)| *style)
                .unwrap_or_else(|| panic!("missing action {label:?}: {actions:?}"))
        };
        assert_eq!(style_for("Retry current").fg, Some(theme.accent));
        assert_eq!(style_for("Replay original").fg, Some(theme.text));
        assert_eq!(style_for("Details").fg, theme.transcript_metadata().fg);
        assert_eq!(style_for("Copy ID").fg, theme.transcript_metadata().fg);
        assert_ne!(style_for("Retry current").fg, Some(theme.danger));

        block.failure.as_mut().expect("failure").focused_action =
            Some(FailureRecoveryAction::Details);
        let focused = action_styles(&block);
        let details = focused
            .iter()
            .find(|(content, _)| content.contains("Details"))
            .expect("focused details");
        assert!(details.1.add_modifier.contains(Modifier::REVERSED));
    }

    #[test]
    fn failure_lifecycle_clears_a_stale_collapsed_disclosure_override() {
        let (mut app, root, server) = production_render_app();
        let mut group = read_group(
            vec![
                tool_member("read", "README.md", "", "completed", ""),
                tool_member("search", "runtime", "failed", "failed", "permission denied"),
            ],
            true,
        );
        group.id = "tool:risk-group".into();
        app.blocks = vec![group];
        app.tool_disclosure_overrides
            .insert("tool:risk-group".into(), false);
        assert!(!app.effective_block_expanded(&app.blocks[0]));

        assert!(app.reconcile_mandatory_disclosures());
        assert!(app.effective_block_expanded(&app.blocks[0]));
        assert!(
            !app.tool_disclosure_overrides
                .contains_key("tool:risk-group")
        );

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    fn actionable_failure_block() -> TranscriptBlock {
        let mut block = block(BlockKind::Diagnostic, "");
        block.id = "failure:run-root".into();
        block.run_id = "run-latest".into();
        block.collapsible = true;
        block.expanded = false;
        block.failure = Some(FailurePresentation {
            kind: crate::transcript::FailureKind::Failed,
            action: FailureAction::Retry,
            owner: "build".into(),
            reason: "The provider stopped before returning a complete response".into(),
            source_event_id: "event-latest".into(),
            run_id: "run-latest".into(),
            model: "provider/original-model".into(),
            current_model: String::new(),
            retry_root_run_id: "run-root".into(),
            attempt_count: 2,
            focused_action: None,
        });
        block
    }

    #[test]
    fn failure_actions_have_independent_pointer_targets_at_product_widths() {
        for locale in [Locale::En, Locale::ZhHans] {
            for width in [80, 120, 160, 180] {
                let (mut app, root, server) = production_render_app();
                app.options.locale = Some(locale.product_id().into());
                app.selected_model = "provider/current-model".into();
                app.blocks = vec![actionable_failure_block()];
                let mut terminal =
                    ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 24)).unwrap();
                terminal.draw(|frame| app.render(frame)).unwrap();

                for expected in [
                    Action::RetryExecution {
                        run_id: "run-latest".into(),
                        routing: crate::rpc::RetryRouting::Current,
                    },
                    Action::RetryExecution {
                        run_id: "run-latest".into(),
                        routing: crate::rpc::RetryRouting::Original,
                    },
                    Action::ToggleBlock("failure:run-root".into()),
                    Action::CopyFailureId("event-latest".into()),
                ] {
                    let found = (0..24).any(|row| {
                        (0..width).any(|column| {
                            app.interactions.action_at(Position::new(column, row))
                                == Some(expected.clone())
                        })
                    });
                    assert!(
                        found,
                        "missing {expected:?} at {width} columns for {locale:?}"
                    );
                }

                let rendered = rendered_frame_text(terminal.backend().buffer());
                let title = tr(locale, MessageId::FailureTitleFailed);
                let (row, column) = anchor_position(&rendered, title);
                assert_eq!(
                    app.interactions
                        .action_at(Position::new(column as u16, row as u16)),
                    None,
                    "failure card body must not behave like an invisible retry button"
                );
                server.join().unwrap();
                std::fs::remove_dir_all(root).unwrap();
            }
        }
    }

    #[test]
    fn transcript_actions_share_unicode_and_ascii_reading_coordinates() {
        for width in [120, 160, 180] {
            let mut baseline = None;
            for glyph_mode in [GlyphMode::Unicode, GlyphMode::Ascii] {
                let (mut app, root, server) = production_render_app();
                app.theme.glyphs = Glyphs::new(glyph_mode);
                app.blocks = vec![actionable_failure_block()];
                let mut terminal =
                    ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 24)).unwrap();
                terminal.draw(|frame| app.render(frame)).unwrap();

                let action = Action::RetryExecution {
                    run_id: "run-latest".into(),
                    routing: crate::rpc::RetryRouting::Current,
                };
                let positions = action_positions(&app, Rect::new(0, 0, width, 24), action);
                assert!(!positions.is_empty(), "width={width} glyphs={glyph_mode:?}");
                assert!(positions.iter().all(|position| {
                    app.transcript_geometry.content.contains(*position)
                        && position.x < app.transcript_geometry.scrollbar.x
                }));
                if let Some(expected) = &baseline {
                    assert_eq!(&positions, expected, "width={width}");
                } else {
                    baseline = Some(positions);
                }

                server.join().unwrap();
                std::fs::remove_dir_all(root).unwrap();
            }
        }
    }

    #[test]
    fn transcript_wheel_scrollbar_and_page_keys_use_the_rendered_content_geometry() {
        for width in [120, 160, 180] {
            let (mut app, root, server) = production_render_app();
            app.blocks = scrollable_blocks();
            let mut terminal =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, 28)).unwrap();
            terminal.draw(|frame| app.render(frame)).unwrap();

            let geometry = app.transcript_geometry;
            assert_eq!(geometry.content.y, geometry.scrollbar.y);
            assert_eq!(geometry.content.height, geometry.scrollbar.height);
            assert!(geometry.content.right() <= geometry.viewport.right());
            assert!(geometry.scrollbar.right() <= geometry.viewport.right());
            assert!(app.transcript_scrollbar.is_active(), "width={width}");

            let initial_scroll = app.transcript_scroll;
            let outer = Position::new(geometry.viewport.x, geometry.content.y);
            assert!(!geometry.content.contains(outer), "width={width}");
            app.handle_mouse(MouseEvent {
                kind: MouseEventKind::ScrollUp,
                column: outer.x,
                row: outer.y,
                modifiers: KeyModifiers::NONE,
            });
            assert_eq!(app.transcript_scroll, initial_scroll, "width={width}");

            let content = geometry.content.as_position();
            app.handle_mouse(MouseEvent {
                kind: MouseEventKind::ScrollUp,
                column: content.x,
                row: content.y,
                modifiers: KeyModifiers::NONE,
            });
            assert_eq!(
                app.transcript_scroll,
                initial_scroll.saturating_sub(3),
                "width={width}"
            );

            let page_size = usize::from(geometry.content.height.max(1));
            let before_page = app.transcript_scroll;
            app.handle_conversation_key(KeyEvent::new(KeyCode::PageUp, KeyModifiers::NONE))
                .unwrap();
            assert_eq!(
                app.transcript_scroll,
                before_page.saturating_sub(page_size),
                "width={width}"
            );
            app.handle_conversation_key(KeyEvent::new(KeyCode::PageDown, KeyModifiers::NONE))
                .unwrap();
            assert_eq!(app.transcript_scroll, before_page, "width={width}");

            let scrollbar = app.transcript_scrollbar;
            app.handle_mouse(MouseEvent {
                kind: MouseEventKind::Down(MouseButton::Left),
                column: scrollbar.thumb.x,
                row: scrollbar.thumb.y,
                modifiers: KeyModifiers::NONE,
            });
            assert!(app.transcript_scrollbar_interaction.is_dragging());
            app.handle_mouse(MouseEvent {
                kind: MouseEventKind::Drag(MouseButton::Left),
                column: scrollbar.track.x,
                row: scrollbar.track.bottom().saturating_sub(1),
                modifiers: KeyModifiers::NONE,
            });
            assert_eq!(app.transcript_scroll, app.transcript_max_scroll);
            app.handle_mouse(MouseEvent {
                kind: MouseEventKind::Up(MouseButton::Left),
                column: scrollbar.track.x,
                row: scrollbar.track.bottom().saturating_sub(1),
                modifiers: KeyModifiers::NONE,
            });
            assert!(!app.transcript_scrollbar_interaction.is_dragging());

            server.join().unwrap();
            std::fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn failure_keyboard_focus_preserves_drafts_and_uses_typed_actions() {
        let (mut app, root, server) = production_render_app();
        app.selected_model = "provider/current-model".into();
        app.blocks = vec![actionable_failure_block()];

        assert!(app.handle_failure_action_key(KeyEvent::new(KeyCode::Tab, KeyModifiers::NONE)));
        assert_eq!(
            app.failure_action_focus,
            Some(super::super::FailureActionFocus {
                block_id: "failure:run-root".into(),
                selected: FailureRecoveryAction::RetryCurrent,
            })
        );
        assert!(app.handle_failure_action_key(KeyEvent::new(KeyCode::Right, KeyModifiers::NONE)));
        assert_eq!(
            app.failure_action_focus
                .as_ref()
                .map(|focus| focus.selected),
            Some(FailureRecoveryAction::ReplayOriginal)
        );
        assert!(app.handle_failure_action_key(KeyEvent::new(KeyCode::Right, KeyModifiers::NONE)));
        assert!(app.handle_failure_action_key(KeyEvent::new(KeyCode::Enter, KeyModifiers::NONE)));
        assert!(app.effective_block_expanded(&app.blocks[0]));
        assert_eq!(
            app.failure_action_focus
                .as_ref()
                .map(|focus| focus.selected),
            Some(FailureRecoveryAction::Details)
        );
        assert!(app.handle_failure_action_key(KeyEvent::new(KeyCode::Esc, KeyModifiers::NONE)));
        assert!(app.failure_action_focus.is_none());
        assert_eq!(app.focus, Focus::Composer);

        app.composer.set_text("keep this exact draft");
        assert!(!app.handle_failure_action_key(KeyEvent::new(KeyCode::Tab, KeyModifiers::NONE)));
        assert_eq!(app.composer.text(), "keep this exact draft");
        assert!(app.failure_action_focus.is_none());

        app.composer.set_text("");
        app.active_run_id = Some("run-active".into());
        assert!(!app.handle_failure_action_key(KeyEvent::new(KeyCode::Tab, KeyModifiers::NONE)));
        assert!(app.failure_action_focus.is_none());

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
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
                        layout_contract::TRANSCRIPT_FULL_ROLE_MIN_WIDTH,
                    )
                    .width(),
                    layout_contract::TRANSCRIPT_ROLE_PREFIX_WIDTH
                );
                assert_eq!(
                    transcript_role_prefix(
                        glyphs.role_prefix(),
                        label,
                        Style::default(),
                        Style::default(),
                        layout_contract::TRANSCRIPT_FULL_ROLE_MIN_WIDTH - 1,
                    )
                    .continuation_width(),
                    layout_contract::TRANSCRIPT_ROLE_MARK_WIDTH
                );
                assert_eq!(
                    transcript_role_prefix(
                        glyphs.role_prefix(),
                        label,
                        Style::default(),
                        Style::default(),
                        layout_contract::TRANSCRIPT_FULL_ROLE_MIN_WIDTH,
                    )
                    .continuation_width(),
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
    fn narrow_user_and_assistant_share_the_same_semantic_rail_snapshot() {
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

        assert!(user[1].starts_with(&" ".repeat(2)));
        assert!(!user[1].starts_with(&" ".repeat(10)));
        assert!(assistant[1].starts_with(&" ".repeat(2)));
        assert!(!assistant[1].starts_with(&" ".repeat(10)));
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

    #[test]
    fn wide_user_and_assistant_keep_the_full_hanging_indent() {
        let body = "x".repeat(80);
        for kind in [BlockKind::User, BlockKind::Assistant] {
            let mut message = block(kind, &body);
            message.status.clear();
            let lines = plain(&transcript_lines(
                &message,
                Locale::En,
                TranscriptStyles::default(),
                layout_contract::TRANSCRIPT_FULL_ROLE_MIN_WIDTH,
            ));

            assert!(lines.len() > 1);
            assert!(lines[1].starts_with(&" ".repeat(10)));
        }
    }

    #[test]
    fn production_role_geometry_tracks_the_responsive_width_matrix() {
        let body = "x".repeat(180);
        for width in [60, 71, 72, 80, 120] {
            let expected_continuation = if width < layout_contract::TRANSCRIPT_FULL_ROLE_MIN_WIDTH {
                layout_contract::TRANSCRIPT_ROLE_MARK_WIDTH
            } else {
                layout_contract::TRANSCRIPT_ROLE_PREFIX_WIDTH
            };
            let mut rendered = Vec::new();
            for kind in [BlockKind::User, BlockKind::Assistant] {
                let mut message = block(kind, &body);
                message.status.clear();
                let lines = plain(&transcript_lines(
                    &message,
                    Locale::En,
                    TranscriptStyles::default(),
                    width,
                ));

                assert!(lines.len() > 1, "width={width} kind={kind:?}");
                assert!(
                    lines
                        .iter()
                        .all(|line| UnicodeWidthStr::width(line.as_str()) <= usize::from(width)),
                    "width={width} kind={kind:?} lines={lines:?}"
                );
                assert_eq!(
                    lines[1].chars().take_while(|ch| *ch == ' ').count(),
                    expected_continuation,
                    "width={width} kind={kind:?}"
                );
                rendered.push(lines);
            }
            assert_eq!(rendered[0].len(), rendered[1].len(), "width={width}");
        }
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
            glyph_mode: GlyphMode::Unicode,
            content_width,
            tool_expand_key: crate::keybinding::KeyBindings::default()
                .expand_tools
                .label(),
            tool_inspect_key: crate::keybinding::KeyBindings::default()
                .inspect_tool_output
                .label(),
            expanded_output_budget: expanded_tool_output_budget(height),
            height,
            requested_scroll,
            follow_bottom,
            selection_anchor: None,
            scroll_anchor: None,
        }
    }

    fn anchored_viewport(
        content_width: u16,
        height: usize,
        anchor: TranscriptScrollAnchor,
    ) -> TranscriptViewport {
        TranscriptViewport {
            scroll_anchor: Some(anchor),
            ..viewport(content_width, height, 0, false)
        }
    }

    fn captured_anchor(
        blocks: &[TranscriptBlock],
        viewport: TranscriptViewport,
    ) -> TranscriptScrollAnchor {
        let layout = TranscriptLayout::new(blocks, viewport.clone(), &mut HashMap::new());
        layout.capture_anchor(blocks, viewport).unwrap()
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
    fn width_reflow_preserves_the_anchored_logical_line() {
        let mut answer = block(
            BlockKind::Assistant,
            &(1..=18)
                .map(|line| format!("logical line {line}: {}", "detail ".repeat(12)))
                .collect::<Vec<_>>()
                .join("\n"),
        );
        answer.id = "answer".into();
        let blocks = [answer];
        let before = captured_anchor(&blocks, viewport(40, 6, 31, false));
        let after = captured_anchor(&blocks, anchored_viewport(100, 6, before.clone()));

        assert_eq!(after.block_id, before.block_id);
        assert_eq!(after.logical_line, before.logical_line);
    }

    #[test]
    fn role_breakpoint_reflow_preserves_the_anchored_logical_line() {
        let mut answer = block(
            BlockKind::Assistant,
            &(1..=18)
                .map(|line| format!("logical line {line}: {}", "细节 detail ".repeat(12)))
                .collect::<Vec<_>>()
                .join("\n"),
        );
        answer.id = "responsive-answer".into();
        let blocks = [answer];
        let before_width = layout_contract::TRANSCRIPT_FULL_ROLE_MIN_WIDTH - 1;
        let after_width = layout_contract::TRANSCRIPT_FULL_ROLE_MIN_WIDTH;
        let before = captured_anchor(&blocks, viewport(before_width, 6, 31, false));
        let after = captured_anchor(&blocks, anchored_viewport(after_width, 6, before.clone()));

        assert_eq!(after.block_id, before.block_id);
        assert_eq!(after.logical_line, before.logical_line);
    }

    #[test]
    fn density_and_glyph_reflow_preserve_the_anchored_logical_line() {
        let members = (1..=18)
            .map(|index| {
                tool_member(
                    &format!("read-{index}"),
                    &format!("src/deeply/nested/file-{index}.rs"),
                    "",
                    "completed",
                    "",
                )
            })
            .collect();
        let blocks = [read_group(members, true)];
        let before_viewport = viewport(52, 6, 12, false);
        let before = captured_anchor(&blocks, before_viewport);
        let mut after_viewport = anchored_viewport(52, 6, before.clone());
        after_viewport.density = DensityMode::Comfortable;
        after_viewport.glyph_mode = GlyphMode::Ascii;
        let after = captured_anchor(&blocks, after_viewport);

        assert_eq!(after.block_id, before.block_id);
        assert_eq!(after.logical_line, before.logical_line);
    }

    #[test]
    fn collapsing_content_above_the_viewport_preserves_the_anchor() {
        let members: Vec<_> = (1..=20)
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
        let expanded = read_group(members.clone(), true);
        let mut answer = block(
            BlockKind::Assistant,
            "anchor one\nanchor two\nanchor three\nanchor four",
        );
        answer.id = "answer".into();
        let initial_blocks = [expanded, answer.clone()];
        let initial_layout = TranscriptLayout::new(
            &initial_blocks,
            viewport(80, 3, 0, false),
            &mut HashMap::new(),
        );
        let answer_top = initial_layout.items[1].top;
        let before = captured_anchor(
            &initial_blocks,
            viewport(80, 3, answer_top.saturating_add(1), false),
        );

        let collapsed_blocks = [read_group(members, false), answer];
        let after = captured_anchor(&collapsed_blocks, anchored_viewport(80, 3, before.clone()));
        assert_eq!(after.block_id, "answer");
        assert_eq!(after.logical_line, before.logical_line);
    }

    #[test]
    fn missing_anchor_block_falls_back_to_the_same_block_index() {
        let blocks = [
            {
                let mut block = block(BlockKind::Assistant, "first");
                block.id = "first".into();
                block
            },
            {
                let mut block = block(BlockKind::Assistant, "fallback\nsecond row");
                block.id = "fallback".into();
                block
            },
        ];
        let missing = TranscriptScrollAnchor {
            block_id: "removed".into(),
            block_index: 1,
            logical_line: 99,
            sub_rows: 99,
        };
        let layout = TranscriptLayout::new(
            &blocks,
            anchored_viewport(80, 1, missing),
            &mut HashMap::new(),
        );

        assert_eq!(layout.viewport_start, layout.items[1].top);
        assert_eq!(
            layout
                .capture_anchor(&blocks, viewport(80, 1, layout.viewport_start, false))
                .unwrap()
                .block_id,
            "fallback"
        );
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
    fn new_output_preserves_a_paused_readers_logical_anchor() {
        let mut first = block(BlockKind::Assistant, "one\ntwo\nthree\nfour\nfive\nsix");
        first.id = "first".into();
        let before = captured_anchor(std::slice::from_ref(&first), viewport(40, 3, 2, false));
        let blocks = [first, block(BlockKind::Assistant, "new output")];
        let after = captured_anchor(&blocks, anchored_viewport(40, 3, before.clone()));

        assert_eq!(after, before);
    }

    #[test]
    fn transcript_height_cache_reuses_stable_blocks_and_invalidates_density_and_revisions() {
        let mut stable = block(BlockKind::Assistant, "one\ntwo");
        stable.id = "stable".into();
        let expand_key = crate::keybinding::KeyBindings::default()
            .expand_tools
            .label();
        let inspect_key = crate::keybinding::KeyBindings::default()
            .inspect_tool_output
            .label();
        let mut cache = HashMap::from([(
            "stable".into(),
            TranscriptHeightCacheEntry {
                revision: 1,
                width: 80,
                locale: Locale::En,
                density: DensityMode::Compact,
                glyph_mode: GlyphMode::Unicode,
                expand_key,
                inspect_key,
                expanded_output_budget: expanded_tool_output_budget(20),
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
        let inspect_key = crate::keybinding::KeyBindings::default()
            .inspect_tool_output
            .label();
        let mut cache = HashMap::from([(
            "stable".into(),
            TranscriptRenderCacheEntry {
                revision: 1,
                width: 80,
                locale: Locale::En,
                density: DensityMode::Compact,
                glyph_mode: GlyphMode::Unicode,
                expand_key,
                inspect_key,
                expanded_output_budget: layout_contract::TOOL_OUTPUT_MAX_BUDGET,
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
            TranscriptRenderOptions {
                locale: Locale::En,
                density: DensityMode::Comfortable,
                glyph_mode: GlyphMode::Unicode,
                content_width: 80,
                tool_expand_key: expand_key,
                tool_inspect_key: inspect_key,
                expanded_output_budget: layout_contract::TOOL_OUTPUT_MAX_BUDGET,
            },
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
            inspect_key: "Ctrl-T",
            expanded_output_budget: layout_contract::TOOL_OUTPUT_MAX_BUDGET,
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
    fn expanded_plain_output_keeps_exact_visual_head_tail_receipt() {
        let context = ToolLineContext {
            locale: Locale::En,
            density: DensityMode::Compact,
            styles: TranscriptStyles::default(),
            content_width: 12,
            expand_key: "Ctrl-O",
            inspect_key: "Ctrl-T",
            expanded_output_budget: 5,
        };
        let minified = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ";
        let lines = plain(&tool_detail_lines(
            minified,
            BlockBodyKind::Plain,
            true,
            context,
            false,
        ));
        assert_eq!(lines.len(), 5);
        assert!(lines[2].contains("Ctrl-T"));
        assert!(lines[2].contains('3'), "{lines:?}");
        assert!(lines.first().unwrap().contains("abcdefghij"));
        assert!(lines[3].contains("OPQRSTUVWX"), "{lines:?}");
        assert!(lines.last().unwrap().contains("YZ"), "{lines:?}");

        let multiline = (1..=9)
            .map(|line| format!("row-{line}"))
            .collect::<Vec<_>>()
            .join("\n");
        let lines = plain(&tool_detail_lines(
            &multiline,
            BlockBodyKind::Plain,
            true,
            context,
            false,
        ));
        assert_eq!(lines.len(), 5);
        assert!(lines[2].contains("Ctrl-T"));
        assert!(lines[2].contains('5'), "{lines:?}");
        assert!(lines[0].contains("row-1"));
        assert!(lines[4].contains("row-9"));
    }

    #[test]
    fn expanded_plain_output_budget_handles_cjk_ascii_and_narrow_widths() {
        for glyph_mode in [GlyphMode::Unicode, GlyphMode::Ascii] {
            for width in [8, 18, 80] {
                let context = ToolLineContext {
                    locale: Locale::ZhHans,
                    density: DensityMode::Compact,
                    styles: TranscriptStyles {
                        glyphs: Glyphs::new(glyph_mode),
                        ..TranscriptStyles::default()
                    },
                    content_width: width,
                    expand_key: "Ctrl-O",
                    inspect_key: "Ctrl-T",
                    expanded_output_budget: 6,
                };
                let body = format!("{}{}", "信息熵完整符号图".repeat(12), "ascii".repeat(12));
                let lines = tool_detail_lines(&body, BlockBodyKind::Plain, true, context, false);
                assert!(lines.len() <= 6, "mode={glyph_mode:?} width={width}");
                let mut tool = block(BlockKind::Tool, &body);
                tool.expanded = true;
                let unbounded_rows =
                    unbounded_plain_tool_detail_lines(&body, BlockBodyKind::Plain, context, false)
                        .len();
                assert_eq!(
                    bounded_tool_output_receipt_line(&tool, context),
                    bounded_plain_receipt_index(unbounded_rows, 6).map(|line| line + 1),
                    "mode={glyph_mode:?} width={width}"
                );
                if unbounded_rows > 6 && width > 8 {
                    assert!(plain(&lines).join("\n").contains("Ctrl-T"));
                }
                if width == 8 {
                    assert!(unbounded_rows > 6);
                    assert_eq!(bounded_tool_output_receipt_line(&tool, context), Some(4));
                    assert!(!plain(&lines).join("\n").contains("Ctrl-T"));
                }
            }
        }
    }

    #[test]
    fn expanded_group_bounds_wrapped_member_headers_and_bodies_as_visual_rows() {
        let context = ToolLineContext {
            locale: Locale::En,
            density: DensityMode::Compact,
            styles: TranscriptStyles::default(),
            content_width: 12,
            expand_key: "Ctrl-O",
            inspect_key: "Ctrl-T",
            expanded_output_budget: 6,
        };
        let mut group = read_group(
            vec![
                tool_member(
                    "wrapped-header-one",
                    "src/a/very/long/path/to/the/first/member.rs",
                    "",
                    "completed",
                    "first body row with enough detail to wrap repeatedly",
                ),
                tool_member(
                    "wrapped-header-two",
                    "src/a/very/long/path/to/the/second/member.rs",
                    "",
                    "completed",
                    "second body row with enough detail to wrap repeatedly",
                ),
            ],
            true,
        );
        group.id = "tool:wrapped-group".into();

        let unbounded_context = ToolLineContext {
            expanded_output_budget: usize::MAX,
            ..context
        };
        let unbounded = tool_group_lines(&group, unbounded_context);
        let bounded = tool_group_lines(&group, context);
        let expected_omitted = unbounded
            .len()
            .saturating_sub(context.expanded_output_budget.saturating_sub(1));
        let receipt_index = bounded
            .iter()
            .position(|line| plain(std::slice::from_ref(line))[0].contains("Ctrl-T"))
            .expect("bounded group receipt");

        assert_eq!(bounded.len(), context.expanded_output_budget);
        assert_eq!(
            bounded_tool_output_receipt_line(&group, context),
            Some(receipt_index + 1),
        );
        assert!(
            plain(&bounded)[receipt_index].contains(&expected_omitted.to_string()),
            "expected exact omitted visual-row count {expected_omitted}: {:?}",
            plain(&bounded)
        );
        assert!(bounded.iter().all(|line| {
            Paragraph::new(vec![line.clone()])
                .wrap(Wrap { trim: false })
                .line_count(context.content_width)
                == 1
        }));
    }

    #[test]
    fn ctrl_t_candidates_prioritize_hover_selection_then_nearest_visible_receipt() {
        let mut candidates = vec![
            VisibleToolOutputCandidate {
                block_id: "hovered".into(),
                hovered: true,
                selected: false,
                receipt_top: 90,
            },
            VisibleToolOutputCandidate {
                block_id: "selected".into(),
                hovered: false,
                selected: true,
                receipt_top: 50,
            },
            VisibleToolOutputCandidate {
                block_id: "nearest".into(),
                hovered: false,
                selected: false,
                receipt_top: 14,
            },
            VisibleToolOutputCandidate {
                block_id: "farther".into(),
                hovered: false,
                selected: false,
                receipt_top: 4,
            },
        ];

        prioritize_visible_tool_output_candidates(&mut candidates, 10, 10);

        assert_eq!(
            candidates
                .iter()
                .map(|candidate| candidate.block_id.as_str())
                .collect::<Vec<_>>(),
            ["farther", "nearest", "selected", "hovered"]
        );
    }

    #[test]
    fn ctrl_t_ignores_a_bounded_receipt_below_the_visible_block_header() {
        let (mut app, root, server) = production_render_app();
        let mut first = block(BlockKind::Tool, &"first evidence ".repeat(80));
        first.id = "tool:first-visible-receipt".into();
        first.expanded = true;
        let mut second = block(BlockKind::Tool, &"second evidence ".repeat(80));
        second.id = "tool:second-hidden-receipt".into();
        second.expanded = true;
        app.blocks = vec![first, second];
        app.transcript_follow_bottom = false;
        app.transcript_scroll = 1;
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(40, 10)).unwrap();

        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();

        let first_action = Action::OpenToolOutput("tool:first-visible-receipt".into());
        let second_action = Action::OpenToolOutput("tool:second-hidden-receipt".into());
        let area = Rect::new(0, 0, 40, 10);
        assert!(!action_positions(&app, area, first_action).is_empty());
        assert!(action_positions(&app, area, second_action).is_empty());
        assert_eq!(
            app.bounded_tool_output_blocks,
            ["tool:first-visible-receipt"]
        );

        app.handle_key(KeyEvent::new(KeyCode::Char('t'), KeyModifiers::CONTROL))
            .unwrap();
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output))
                if output.block_id == "tool:first-visible-receipt"
        ));

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn tool_output_overlay_retains_prompt_and_scrolls_without_mutating_disclosure() {
        let (mut app, root, server) = production_render_app();
        let mut user = block(BlockKind::User, "inspect the complete evidence");
        user.id = "user:run-viewer".into();
        user.run_id = "run-viewer".into();
        let body = (1..=80)
            .map(|line| format!("evidence row {line}"))
            .collect::<Vec<_>>()
            .join("\n");
        let mut tool = block(BlockKind::Tool, &body);
        tool.id = "tool:viewer".into();
        tool.run_id = "run-viewer".into();
        tool.expanded = true;
        app.blocks = vec![user, tool];
        app.tool_disclosure_overrides
            .insert("tool:viewer".into(), true);

        app.apply_action(Action::OpenToolOutput("tool:viewer".into()));
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 18)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let initial = rendered_frame_text(terminal.backend().buffer());
        assert!(initial.contains("inspect the complete evidence"));
        assert!(initial.contains("evidence row 1"));
        assert!(app.tool_output_max_scroll > 0);

        app.handle_key(KeyEvent::new(KeyCode::End, KeyModifiers::NONE))
            .unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let end = rendered_frame_text(terminal.backend().buffer());
        assert!(end.contains("inspect the complete evidence"));
        assert!(end.contains("evidence row 80"));
        assert!(end.contains("70-80 / 80"));
        assert!(app.tool_disclosure_overrides["tool:viewer"]);

        app.handle_key(KeyEvent::new(KeyCode::Home, KeyModifiers::NONE))
            .unwrap();
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output)) if output.scroll == 0
        ));
        app.handle_key(KeyEvent::new(KeyCode::PageDown, KeyModifiers::NONE))
            .unwrap();
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output)) if output.scroll == 12
        ));
        app.handle_key(KeyEvent::new(KeyCode::PageUp, KeyModifiers::NONE))
            .unwrap();
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output)) if output.scroll == 0
        ));
        app.handle_key(KeyEvent::new(KeyCode::End, KeyModifiers::NONE))
            .unwrap();
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::ScrollDown,
            column: 40,
            row: 10,
            modifiers: KeyModifiers::NONE,
        });
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output)) if output.scroll == app.tool_output_max_scroll
        ));
        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::ScrollUp,
            column: 40,
            row: 10,
            modifiers: KeyModifiers::NONE,
        });
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output)) if output.scroll < app.tool_output_max_scroll
        ));
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn tool_output_window_reaches_eight_megabyte_minified_tail_beyond_u16_scroll() {
        let output = format!("{}TAIL-EVIDENCE", "x".repeat(8 * 1024 * 1024));

        let window = tool_output_visual_window(&output, 8, 3, usize::MAX);
        let visible = plain(&window.lines).join("");

        assert!(window.total_rows > u16::MAX as usize);
        assert!(window.max_scroll > u16::MAX as usize);
        assert_eq!(window.scroll, window.max_scroll);
        assert!(window.lines.len() <= 3);
        assert!(
            visible.contains("TAIL-EVIDENCE"),
            "tail window: {visible:?}"
        );
    }

    #[test]
    fn tool_output_window_wraps_unicode_by_display_cells() {
        let output = "甲乙A🙂B丙丁";

        let window = tool_output_visual_window(output, 4, 2, usize::MAX);
        let rows = plain(&window.lines);

        assert_eq!(window.total_rows, 3);
        assert_eq!(window.scroll, 1);
        assert_eq!(rows, ["A🙂B", "丙丁"]);
        assert!(
            rows.iter()
                .all(|row| UnicodeWidthStr::width(row.as_str()) <= 4)
        );
    }

    #[test]
    fn tool_output_layout_preserves_a_body_row_on_short_terminals() {
        for height in [0, 1, 2, 4, 8] {
            let inner = Rect::new(3, 7, 20, height);
            let layout = tool_output_overlay_layout(inner);

            assert!(layout.context.bottom() <= inner.bottom());
            assert!(layout.body.bottom() <= inner.bottom());
            assert!(layout.footer.bottom() <= inner.bottom());
            assert_eq!(layout.context.bottom(), layout.body.top());
            assert_eq!(layout.body.bottom(), layout.footer.top());
            assert_eq!(layout.footer.bottom(), inner.bottom());
            assert_eq!(layout.body.height > 0, height > 0);
        }
    }

    #[test]
    fn tool_output_footer_prefers_position_then_hint_as_width_allows() {
        let hint = "Up/Down scroll";

        assert_eq!(
            plain(&[tool_output_footer_line(hint, Some("2-4 / 9"), 28)])[0],
            "Up/Down scroll       2-4 / 9"
        );
        assert_eq!(
            plain(&[tool_output_footer_line(hint, Some("2-4 / 9"), 9)])[0],
            "  2-4 / 9"
        );
        assert_eq!(
            plain(&[tool_output_footer_line(hint, Some("20-40 / 900"), 5)])[0],
            hint
        );
    }

    #[test]
    fn tool_output_resize_clamps_scroll_and_keeps_tail_visible() {
        let (mut app, root, server) = production_render_app();
        let mut tool = block(BlockKind::Tool, &format!("{}TAIL", "x".repeat(2_000)));
        tool.id = "tool:resize-viewer".into();
        app.blocks = vec![tool];
        app.apply_action(Action::OpenToolOutput("tool:resize-viewer".into()));
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(20, 10)).unwrap();

        terminal.draw(|frame| app.render(frame)).unwrap();
        app.handle_key(KeyEvent::new(KeyCode::End, KeyModifiers::NONE))
            .unwrap();
        terminal
            .resize(Rect::new(0, 0, 80, 6))
            .expect("resize terminal");
        terminal.draw(|frame| app.render(frame)).unwrap();

        let rendered = rendered_frame_text(terminal.backend().buffer());
        assert!(rendered.contains("TAIL"), "{rendered}");
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output))
                if output.scroll == app.tool_output_max_scroll
        ));

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn tool_output_loading_and_member_failure_retain_preview() {
        let (mut app, root, server) = production_render_app();
        let mut group = read_group(
            vec![
                tool_member("failed-member", "two.rs", "", "completed", "PREVIEW-TWO"),
                tool_member("loading-member", "one.rs", "", "completed", "PREVIEW-ONE"),
            ],
            true,
        );
        group.id = "exploration:retained-artifacts".into();
        app.blocks = vec![group];
        app.tool_artifact_loads.insert(
            "failed-member".into(),
            crate::overlay::RetainedLoad {
                generation: 1,
                session_id: "session".into(),
                loading: false,
                error: "artifact unavailable".into(),
            },
        );
        app.tool_artifact_loads.insert(
            "loading-member".into(),
            crate::overlay::RetainedLoad::begin(1, "session".into()),
        );
        app.apply_action(Action::OpenToolOutput(
            "exploration:retained-artifacts".into(),
        ));
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(80, 14)).unwrap();

        terminal.draw(|frame| app.render(frame)).unwrap();
        let loading = rendered_frame_text(terminal.backend().buffer());
        assert!(loading.contains("PREVIEW-ONE"));
        assert!(loading.contains("PREVIEW-TWO"));
        assert!(loading.contains(tr(Locale::En, MessageId::ToolOutputLoading)));

        app.tool_artifact_loads.remove("loading-member");
        terminal.draw(|frame| app.render(frame)).unwrap();
        let failed = rendered_frame_text(terminal.backend().buffer());
        assert!(failed.contains("PREVIEW-ONE"));
        assert!(failed.contains("PREVIEW-TWO"));
        assert!(failed.contains("artifact unavailable"));
        let buffer = terminal.backend().buffer();
        assert!((buffer.area.y..buffer.area.bottom()).any(|y| {
            (buffer.area.x..buffer.area.right()).any(|x| {
                let cell = &buffer[(x, y)];
                cell.symbol() != " " && cell.fg == app.theme.danger
            })
        }));

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn truncated_tool_preview_pointer_and_ctrl_t_share_typed_action() {
        let (mut app, root, server) = production_render_app();
        let mut tool = block(BlockKind::Tool, &"minified".repeat(80));
        tool.id = "tool:bounded-entry".into();
        tool.expanded = true;
        app.blocks = vec![tool];
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(40, 12)).unwrap();
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let action = Action::OpenToolOutput("tool:bounded-entry".into());
        let positions = action_positions(&app, terminal.backend().buffer().area, action.clone());
        assert!(!positions.is_empty());

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: positions[0].x,
            row: positions[0].y,
            modifiers: KeyModifiers::NONE,
        });
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(_))
        ));
        app.apply_action(Action::CloseOverlay);
        app.handle_key(KeyEvent::new(KeyCode::Char('t'), KeyModifiers::CONTROL))
            .unwrap();
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output)) if output.block_id == "tool:bounded-entry"
        ));
        assert_eq!(
            app.tool_disclosure_overrides.get("tool:bounded-entry"),
            None
        );
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn empty_historical_artifact_header_is_clickable_and_ctrl_t_starts_loading() {
        let (mut app, root, server) = production_render_app();
        let mut tool = block(BlockKind::Tool, "");
        tool.id = "tool:historical-artifact".into();
        tool.title = "Read  archived.log".into();
        tool.collapsible = false;
        app.blocks = vec![tool];
        app.tool_artifact_refs.insert(
            "historical-artifact".into(),
            crate::rpc::ToolArtifactRef {
                session_id: "session-history".into(),
                run_id: "run-history".into(),
                call_id: "historical-artifact".into(),
                artifact_id: "artifact-history".into(),
            },
        );
        let mut terminal =
            ratatui::Terminal::new(ratatui::backend::TestBackend::new(60, 10)).unwrap();

        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        let action = Action::OpenToolOutput("tool:historical-artifact".into());
        let positions = action_positions(&app, terminal.backend().buffer().area, action.clone());
        assert!(!positions.is_empty());
        assert_eq!(app.bounded_tool_output_blocks, ["tool:historical-artifact"]);

        app.handle_mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: positions[0].x,
            row: positions[0].y,
            modifiers: KeyModifiers::NONE,
        });
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output))
                if output.block_id == "tool:historical-artifact"
        ));
        assert!(
            app.tool_artifact_loads
                .get("historical-artifact")
                .is_some_and(|load| load.loading)
        );

        app.apply_action(Action::CloseOverlay);
        terminal
            .draw(|frame| app.render_transcript(frame, frame.area()))
            .unwrap();
        app.handle_key(KeyEvent::new(KeyCode::Char('t'), KeyModifiers::CONTROL))
            .unwrap();
        assert!(matches!(
            app.overlays.active(),
            Some(Overlay::ToolOutput(output))
                if output.block_id == "tool:historical-artifact"
        ));

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
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
