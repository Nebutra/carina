use std::path::Path;
use std::time::Duration;

use ratatui::Frame;
use ratatui::layout::{Margin, Rect};
use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Paragraph, Wrap};
use unicode_width::UnicodeWidthStr;

use crate::glyphs::Glyphs;
use crate::i18n::{Locale, MessageId, format as tr_format, text as tr};
use crate::rpc::{ModelContextTokens, Session};
use crate::theme::Theme;

pub struct EmptyConversation<'a> {
    pub workspace: &'a Path,
    pub locale: Locale,
}

impl EmptyConversation<'_> {
    pub fn render(&self, frame: &mut Frame<'_>, area: Rect, theme: Theme) {
        let height = area.height.min(4);
        let prompt_area = Rect::new(
            area.x,
            area.bottom().saturating_sub(height),
            area.width,
            height,
        )
        .inner(Margin::new(2, 0));
        let workspace_name = self
            .workspace
            .file_name()
            .and_then(|name| name.to_str())
            .filter(|name| !name.is_empty())
            .unwrap_or_else(|| tr(self.locale, MessageId::WorkspaceFallback));
        frame.render_widget(
            Paragraph::new(vec![
                Line::from(Span::styled(
                    tr_format(
                        self.locale,
                        MessageId::ReadyInWorkspace,
                        &[("workspace", workspace_name)],
                    ),
                    Style::default().fg(theme.text).add_modifier(Modifier::BOLD),
                )),
                Line::from(Span::styled(
                    tr(self.locale, MessageId::EmptyConversationPrompt),
                    Style::default().fg(theme.muted),
                )),
            ])
            .wrap(Wrap { trim: false }),
            prompt_area,
        );
    }
}

pub struct ConversationStatus<'a> {
    pub execution_status: &'a str,
    pub notice: &'a str,
    pub elapsed: Option<Duration>,
    pub activity: Option<&'a str>,
    pub queued_follow_ups: usize,
    pub background_work: bool,
    pub interrupt_key: &'a str,
    pub priority_notice: bool,
    pub no_color: bool,
    pub context: Option<&'a ModelContextTokens>,
    pub locale: Locale,
}

impl ConversationStatus<'_> {
    pub fn render(&self, frame: &mut Frame<'_>, area: Rect, theme: Theme) {
        let sep = theme.glyphs.separator();
        let state = if self.execution_status == "paused" {
            tr(self.locale, MessageId::CurrentExecutionPaused).to_owned()
        } else {
            localized_execution_status(self.locale, self.execution_status)
        };
        let active = matches!(
            self.execution_status,
            "queued"
                | "pending"
                | "running"
                | "in_progress"
                | "working"
                | "waiting_input"
                | "waiting_approval"
                | "awaiting_approval"
                | "blocked_on_approval"
        );
        let mut left = if active {
            let elapsed = self.elapsed.map(fmt_elapsed_compact).unwrap_or_default();
            let spinner = theme
                .glyphs
                .spinner(self.elapsed.unwrap_or_default().as_millis());
            let activity = self
                .activity
                .filter(|value| !value.trim().is_empty())
                .unwrap_or(&state);
            let interrupt = tr_format(
                self.locale,
                if self.background_work {
                    MessageId::BackgroundInterruptHint
                } else {
                    MessageId::InterruptHint
                },
                &[("key", self.interrupt_key)],
            );
            let affordance = if self.queued_follow_ups > 0 {
                let queued = tr_format(
                    self.locale,
                    MessageId::FollowUpsQueued,
                    &[("count", &self.queued_follow_ups.to_string())],
                );
                format!("{interrupt} {} {queued}", theme.glyphs.separator())
            } else {
                interrupt
            };
            if area.width >= 80 {
                format!(
                    "{spinner} {}  {:<11} {} {}",
                    pad_or_truncate_cells(activity, 14, theme.glyphs),
                    elapsed,
                    theme.glyphs.separator(),
                    pad_or_truncate_cells(&affordance, 23, theme.glyphs)
                )
            } else if elapsed.is_empty() {
                format!(
                    "{spinner} {activity}  {} {affordance}",
                    theme.glyphs.separator()
                )
            } else {
                format!(
                    "{spinner} {activity}  {elapsed}  {} {affordance}",
                    theme.glyphs.separator()
                )
            }
        } else {
            state.clone()
        };
        if !active && self.queued_follow_ups > 0 {
            let queued = tr_format(
                self.locale,
                MessageId::FollowUpsQueued,
                &[("count", &self.queued_follow_ups.to_string())],
            );
            left.push_str(&format!("  {} ", theme.glyphs.separator()));
            left.push_str(&queued);
        }
        if self.priority_notice && !self.notice.is_empty() {
            frame.render_widget(
                Paragraph::new(Span::styled(
                    truncate_cells(self.notice, area.width as usize, theme.glyphs),
                    Style::default().fg(theme.danger),
                )),
                area,
            );
            return;
        }
        let measured_context = self
            .context
            .filter(|context| context.available && !context.estimated && context.limit_tokens > 0);
        let metrics = measured_context
            .map(|context| {
                let mut value = format_context_waterline(context, theme.glyphs);
                if context.breakdown.input_tokens > 0 || context.breakdown.output_tokens > 0 {
                    value.push_str("  ");
                    value.push_str(&format_measured_io(context, theme.glyphs));
                }
                value
            })
            .unwrap_or_default();
        if matches!(self.execution_status, "" | "ready" | "completed") && !self.notice.is_empty() {
            left.clear();
        }
        let metrics_separator = if left.is_empty() || metrics.is_empty() {
            String::new()
        } else {
            format!("  {sep} ")
        };
        let notice_separator = if (left.is_empty() && metrics.is_empty()) || self.notice.is_empty()
        {
            ""
        } else {
            "  "
        };
        let available = area.width.saturating_sub(
            (UnicodeWidthStr::width(left.as_str())
                + UnicodeWidthStr::width(metrics_separator.as_str())
                + UnicodeWidthStr::width(metrics.as_str())
                + UnicodeWidthStr::width(notice_separator)) as u16,
        ) as usize;
        let notice = truncate_cells(self.notice, available, theme.glyphs);
        frame.render_widget(
            Paragraph::new(Line::from(vec![
                Span::styled(left, Style::default().fg(theme.muted)),
                Span::raw(metrics_separator),
                Span::styled(
                    metrics,
                    measured_context
                        .map(|context| context_style(context, theme))
                        .unwrap_or_default(),
                ),
                Span::raw(notice_separator),
                Span::styled(notice, Style::default().fg(theme.accent)),
            ])),
            area,
        );
    }
}

fn fmt_tokens(tokens: u64) -> String {
    crate::render_contract::format_tokens(tokens)
}

fn format_context_waterline(context: &ModelContextTokens, glyphs: Glyphs) -> String {
    let used = format!("{:>4}", fmt_tokens(context.tokens));
    let limit = format!("{:<4}", fmt_tokens(context.limit_tokens));
    let filled = (usize::from(context.used_percent).min(100) * 8).div_ceil(100);
    format!(
        "{used} / {limit} {} {}{}",
        crate::render_contract::format_percent(f64::from(context.used_percent)),
        glyphs.bar_full().repeat(filled),
        glyphs.bar_empty().repeat(8 - filled)
    )
}

fn format_measured_io(context: &ModelContextTokens, glyphs: Glyphs) -> String {
    format!(
        "{}{:>4} {}{:>4}",
        glyphs.up(),
        fmt_tokens(context.breakdown.input_tokens),
        glyphs.down(),
        fmt_tokens(context.breakdown.output_tokens)
    )
}

fn context_style(context: &ModelContextTokens, theme: Theme) -> Style {
    if theme.no_color {
        theme.muted()
    } else if context.used_percent >= 90 || context.threshold == "critical" {
        Style::default().fg(theme.danger)
    } else if context.used_percent >= 80 || context.threshold == "warning" {
        Style::default().fg(theme.warning)
    } else if context.used_percent >= 50 {
        Style::default().fg(theme.accent)
    } else {
        theme.muted()
    }
}

pub fn fmt_elapsed_compact(elapsed: Duration) -> String {
    crate::render_contract::format_elapsed(elapsed)
}

#[derive(Debug, Default)]
pub struct ExecutionTimer {
    accumulated: Duration,
    running_since: Option<std::time::Instant>,
    observed: bool,
}

impl ExecutionTimer {
    pub fn start_new(&mut self) {
        self.accumulated = Duration::ZERO;
        self.running_since = Some(std::time::Instant::now());
        self.observed = true;
    }

    pub fn resume(&mut self) {
        if self.observed && self.running_since.is_none() {
            self.running_since = Some(std::time::Instant::now());
        } else if !self.observed {
            self.start_new();
        }
    }

    pub fn pause(&mut self) {
        if let Some(started) = self.running_since.take() {
            self.accumulated = self.accumulated.saturating_add(started.elapsed());
        }
    }

    pub fn reset(&mut self) {
        *self = Self::default();
    }

    pub fn elapsed(&self) -> Option<Duration> {
        self.observed.then(|| {
            self.accumulated.saturating_add(
                self.running_since
                    .map(|started| started.elapsed())
                    .unwrap_or_default(),
            )
        })
    }
}

pub fn localized_execution_status(locale: Locale, status: &str) -> String {
    let id = match status {
        "" | "ready" | "completed" => MessageId::StatusReady,
        "queued" | "pending" => MessageId::StatusQueued,
        "running" | "in_progress" | "working" => MessageId::StatusRunning,
        "waiting_approval" | "awaiting_approval" | "blocked_on_approval" => {
            MessageId::StatusWaitingApproval
        }
        "paused" => MessageId::StatusPaused,
        "cancelled" | "canceled" => MessageId::StatusCancelled,
        "failed" | "error" => MessageId::StatusFailed,
        unknown => return tr_format(locale, MessageId::StatusUnknown, &[("status", unknown)]),
    };
    tr(locale, id).to_owned()
}

fn truncate_cells(value: &str, max_width: usize, glyphs: Glyphs) -> String {
    crate::render_contract::truncate_width_with_glyphs(value, max_width, glyphs)
}

fn pad_or_truncate_cells(value: &str, width: usize, glyphs: Glyphs) -> String {
    let value = truncate_cells(value, width, glyphs);
    let padding = width.saturating_sub(UnicodeWidthStr::width(value.as_str()));
    format!("{value}{}", " ".repeat(padding))
}

pub fn conversation_title(session: Option<&Session>, workspace: &Path, locale: Locale) -> String {
    if let Some(value) =
        session.and_then(|session| non_empty(&session.name).or_else(|| non_empty(&session.summary)))
    {
        return value
            .lines()
            .next()
            .unwrap_or(value)
            .chars()
            .take(64)
            .collect();
    }
    workspace
        .file_name()
        .and_then(|name| name.to_str())
        .filter(|name| !name.is_empty())
        .map(|name| tr_format(locale, MessageId::ConversationName, &[("workspace", name)]))
        .unwrap_or_else(|| tr(locale, MessageId::NewConversation).to_owned())
}

fn non_empty(value: &str) -> Option<&str> {
    (!value.trim().is_empty()).then(|| value.trim())
}

#[cfg(test)]
mod tests {
    use ratatui::Terminal;
    use ratatui::backend::TestBackend;

    use super::*;

    fn session() -> Session {
        Session {
            session_id: "sess_internal".into(),
            name: String::new(),
            workspace_id: String::new(),
            workspace_root: String::new(),
            status: "active".into(),
            next_model: String::new(),
            next_reasoning_effort: String::new(),
            plan_mode: false,
            created_at: String::new(),
            updated_at: String::new(),
            latest_run_id: String::new(),
            latest_run_agent: String::new(),
            execution_status: String::new(),
            summary: String::new(),
            continuity: None,
        }
    }

    #[test]
    fn conversation_title_never_falls_back_to_session_id() {
        assert_eq!(
            conversation_title(Some(&session()), Path::new("/tmp/carina"), Locale::En),
            "carina conversation"
        );
    }

    #[test]
    fn conversation_title_prefers_human_name_then_summary() {
        let mut value = session();
        value.summary = "Refactor provider setup\ninternal detail".into();
        assert_eq!(
            conversation_title(Some(&value), Path::new("/tmp/carina"), Locale::En),
            "Refactor provider setup"
        );
        value.name = "Provider onboarding".into();
        assert_eq!(
            conversation_title(Some(&value), Path::new("/tmp/carina"), Locale::En),
            "Provider onboarding"
        );
    }

    #[test]
    fn idle_notice_does_not_repeat_ready_and_truncates_by_terminal_cells() {
        let mut terminal = Terminal::new(TestBackend::new(12, 1)).unwrap();
        terminal
            .draw(|frame| {
                ConversationStatus {
                    execution_status: "ready",
                    notice: "中文状态更新",
                    elapsed: None,
                    activity: None,
                    queued_follow_ups: 0,
                    background_work: false,
                    interrupt_key: "Ctrl-C",
                    priority_notice: false,
                    no_color: true,
                    context: None,
                    locale: Locale::ZhHans,
                }
                .render(frame, frame.area(), Theme::carina(true));
            })
            .unwrap();
        let rendered = (0..12)
            .map(|x| terminal.backend().buffer().cell((x, 0)).unwrap().symbol())
            .collect::<String>();
        assert!(!rendered.contains("ready"));
        assert_eq!(
            truncate_cells(
                "中文状态更新",
                11,
                Glyphs::new(crate::glyphs::GlyphMode::Unicode)
            ),
            format!(
                "中文状态更{}",
                Glyphs::new(crate::glyphs::GlyphMode::Unicode).ellipsis()
            )
        );
        assert_eq!(
            UnicodeWidthStr::width(
                truncate_cells(
                    "中文状态更新",
                    11,
                    Glyphs::new(crate::glyphs::GlyphMode::Unicode),
                )
                .as_str(),
            ),
            11
        );
    }

    #[test]
    fn paused_conversation_uses_product_copy_instead_of_wire_status() {
        let mut terminal = Terminal::new(TestBackend::new(50, 1)).unwrap();
        terminal
            .draw(|frame| {
                ConversationStatus {
                    execution_status: "paused",
                    notice: "",
                    elapsed: None,
                    activity: None,
                    queued_follow_ups: 0,
                    background_work: false,
                    interrupt_key: "Ctrl-C",
                    priority_notice: false,
                    no_color: true,
                    context: None,
                    locale: Locale::En,
                }
                .render(frame, frame.area(), Theme::carina(true));
            })
            .unwrap();
        let rendered = (0..50)
            .map(|x| terminal.backend().buffer().cell((x, 0)).unwrap().symbol())
            .collect::<String>();
        assert!(rendered.contains("The current execution is paused"));
    }

    #[test]
    fn protocol_statuses_are_mapped_before_rendering() {
        assert_eq!(
            localized_execution_status(Locale::ZhHans, "queued"),
            "已排队"
        );
        assert_eq!(
            localized_execution_status(Locale::Fr, "blocked_on_approval"),
            "en attente d’approbation"
        );
        assert_eq!(
            localized_execution_status(Locale::ZhHans, "future_state"),
            "未知状态 (future_state)"
        );
    }

    #[test]
    fn elapsed_format_is_compact_and_stable_across_units() {
        assert_eq!(fmt_elapsed_compact(Duration::from_secs(0)), "0s");
        assert_eq!(fmt_elapsed_compact(Duration::from_secs(59)), "59s");
        assert_eq!(fmt_elapsed_compact(Duration::from_secs(60)), "1m00");
        assert_eq!(fmt_elapsed_compact(Duration::from_secs(3_599)), "59m59");
        assert_eq!(fmt_elapsed_compact(Duration::from_secs(3_600)), "1h00");
        assert_eq!(fmt_elapsed_compact(Duration::from_secs(90_123)), "25h02");
        for elapsed in [Duration::ZERO, Duration::from_millis(999)] {
            for no_color in [false, true] {
                let glyphs = Theme::carina(no_color).glyphs;
                assert_eq!(
                    UnicodeWidthStr::width(glyphs.spinner(elapsed.as_millis())),
                    1
                );
            }
        }
    }

    #[test]
    fn measured_token_and_context_formats_have_fixed_width() {
        for tokens in [0, 999, 1_000, 9_999, 10_000, 999_999, 1_000_000, u64::MAX] {
            assert!(UnicodeWidthStr::width(fmt_tokens(tokens).as_str()) <= 4);
        }
        for percent in [0, 9, 50, 80, 90, 100] {
            let context = ModelContextTokens {
                available: true,
                tokens: 12_000,
                limit_tokens: 100_000,
                used_percent: percent,
                ..ModelContextTokens::default()
            };
            assert_eq!(
                UnicodeWidthStr::width(
                    format_context_waterline(&context, Theme::carina(false).glyphs).as_str()
                ),
                26
            );
        }
    }

    #[test]
    fn context_pressure_uses_semantic_threshold_styles() {
        let theme = Theme::carina(false);
        let normal = ModelContextTokens {
            used_percent: 12,
            ..ModelContextTokens::default()
        };
        let warning = ModelContextTokens {
            used_percent: 85,
            ..ModelContextTokens::default()
        };
        let critical = ModelContextTokens {
            used_percent: 95,
            ..ModelContextTokens::default()
        };
        assert_eq!(context_style(&normal, theme).fg, Some(theme.muted));
        assert_eq!(context_style(&warning, theme).fg, Some(theme.warning));
        assert_eq!(context_style(&critical, theme).fg, Some(theme.danger));
    }

    #[test]
    fn active_status_keeps_interrupt_and_queue_affordances_visible() {
        let context = ModelContextTokens {
            available: true,
            tokens: 12_000,
            limit_tokens: 100_000,
            remaining_tokens: 88_000,
            used_percent: 12,
            measurement: "latest completed provider request".into(),
            breakdown: crate::rpc::ModelTokenBreakdown {
                input_tokens: 12_000,
                output_tokens: 1_400,
                ..crate::rpc::ModelTokenBreakdown::default()
            },
            ..ModelContextTokens::default()
        };
        let mut terminal = Terminal::new(TestBackend::new(160, 1)).unwrap();
        terminal
            .draw(|frame| {
                ConversationStatus {
                    execution_status: "running",
                    notice: "",
                    elapsed: Some(Duration::from_secs(62)),
                    activity: Some("running tests"),
                    queued_follow_ups: 2,
                    background_work: false,
                    interrupt_key: "Ctrl-C",
                    priority_notice: false,
                    no_color: true,
                    context: Some(&context),
                    locale: Locale::En,
                }
                .render(frame, frame.area(), Theme::carina(true));
            })
            .unwrap();
        let rendered = (0..160)
            .map(|x| terminal.backend().buffer().cell((x, 0)).unwrap().symbol())
            .collect::<String>();
        assert!(rendered.contains("running tests"));
        assert!(rendered.contains("1m02"));
        assert!(rendered.contains("Ctrl-C stop"));
        assert!(rendered.contains("+2 queued"));
        let glyphs = Theme::carina(true).glyphs;
        assert!(rendered.contains(&format_context_waterline(&context, glyphs)));
        assert!(rendered.contains(&format_measured_io(&context, glyphs)));
    }

    #[test]
    fn priority_notice_displaces_stale_activity_and_metrics() {
        let context = ModelContextTokens {
            available: true,
            tokens: 12_000,
            limit_tokens: 100_000,
            used_percent: 12,
            ..ModelContextTokens::default()
        };
        let mut terminal = Terminal::new(TestBackend::new(40, 1)).unwrap();
        terminal
            .draw(|frame| {
                ConversationStatus {
                    execution_status: "running",
                    notice: "Runtime unavailable",
                    elapsed: Some(Duration::from_secs(17)),
                    activity: Some("running"),
                    queued_follow_ups: 0,
                    background_work: false,
                    interrupt_key: "Ctrl-C",
                    priority_notice: true,
                    no_color: false,
                    context: Some(&context),
                    locale: Locale::En,
                }
                .render(frame, frame.area(), Theme::carina(false));
            })
            .unwrap();
        let rendered = (0..40)
            .map(|x| terminal.backend().buffer().cell((x, 0)).unwrap().symbol())
            .collect::<String>();
        assert!(rendered.contains("Runtime unavailable"));
        assert!(!rendered.contains("12K"));
        assert!(!rendered.contains("running"));
    }

    #[test]
    fn daemon_owned_background_mode_changes_the_interrupt_affordance() {
        let mut terminal = Terminal::new(TestBackend::new(120, 1)).unwrap();
        terminal
            .draw(|frame| {
                ConversationStatus {
                    execution_status: "running",
                    notice: "",
                    elapsed: Some(Duration::from_secs(5)),
                    activity: Some("monitoring"),
                    queued_follow_ups: 0,
                    background_work: true,
                    interrupt_key: "F12",
                    priority_notice: false,
                    no_color: true,
                    context: None,
                    locale: Locale::En,
                }
                .render(frame, frame.area(), Theme::carina(true));
            })
            .unwrap();
        let rendered = (0..120)
            .map(|x| terminal.backend().buffer().cell((x, 0)).unwrap().symbol())
            .collect::<String>();
        assert!(rendered.contains("F12 stop background"));
    }

    #[test]
    fn estimated_context_is_not_presented_as_exact_usage() {
        let context = ModelContextTokens {
            available: false,
            estimated: true,
            tokens: 42,
            limit_tokens: 100,
            used_percent: 42,
            ..ModelContextTokens::default()
        };
        let mut terminal = Terminal::new(TestBackend::new(80, 1)).unwrap();
        terminal
            .draw(|frame| {
                ConversationStatus {
                    execution_status: "running",
                    notice: "",
                    elapsed: Some(Duration::ZERO),
                    activity: None,
                    queued_follow_ups: 0,
                    background_work: false,
                    interrupt_key: "Ctrl-C",
                    priority_notice: false,
                    no_color: true,
                    context: Some(&context),
                    locale: Locale::En,
                }
                .render(frame, frame.area(), Theme::carina(true));
            })
            .unwrap();
        let rendered = (0..80)
            .map(|x| terminal.backend().buffer().cell((x, 0)).unwrap().symbol())
            .collect::<String>();
        assert!(!rendered.contains("42 / 100"));
    }
}
