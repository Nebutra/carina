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
use crate::render_contract::truncate_width_with_glyphs;
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ChromeTone {
    Muted,
    Accent,
    Warning,
    Danger,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum ChromeSlotKind {
    Run,
    Queue,
    Hitl,
    Isolation,
    Context,
    ScreenMode,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ChromeCapability {
    OpenQueue,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChromeSlot {
    pub kind: ChromeSlotKind,
    pub text: String,
    pub compact_text: String,
    pub tone: ChromeTone,
    pub capability: Option<ChromeCapability>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LaidOutChromeSlot {
    pub kind: ChromeSlotKind,
    pub text: String,
    pub tone: ChromeTone,
    pub capability: Option<ChromeCapability>,
    pub start: u16,
    pub width: u16,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChromePrimary {
    pub text: String,
    pub tone: ChromeTone,
    pub animated: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ComposerChrome {
    pub primary: Option<ChromePrimary>,
    slots: Vec<ChromeSlot>,
    context_protected: bool,
}

pub struct ComposerChromeInput<'a> {
    pub execution_status: &'a str,
    pub notice: &'a str,
    pub elapsed: Option<Duration>,
    pub activity: Option<&'a str>,
    pub queued_follow_ups: usize,
    pub background_work: bool,
    pub interrupt_key: &'a str,
    pub priority_notice: bool,
    pub context: Option<&'a ModelContextTokens>,
    pub locale: Locale,
    pub screen_mode: &'a str,
    pub hitl: &'a str,
    pub isolation_profile: &'a str,
    pub sandbox_commands: Option<bool>,
}

impl ComposerChrome {
    pub fn project(input: ComposerChromeInput<'_>) -> Self {
        let locale = input.locale;
        let (run_value, run_tone, active) = execution_state(locale, input.execution_status);
        let notice = input.notice.trim();
        let primary = if input.priority_notice && !notice.is_empty() {
            Some(ChromePrimary {
                text: notice.to_owned(),
                tone: ChromeTone::Danger,
                animated: false,
            })
        } else if active {
            let mut parts = Vec::new();
            if let Some(activity) = input.activity.filter(|value| !value.trim().is_empty()) {
                parts.push(activity.trim().to_owned());
            }
            if let Some(elapsed) = input.elapsed {
                parts.push(fmt_elapsed_compact(elapsed));
            }
            parts.push(tr_format(
                locale,
                if input.background_work {
                    MessageId::BackgroundInterruptHint
                } else {
                    MessageId::InterruptHint
                },
                &[("key", input.interrupt_key)],
            ));
            if !notice.is_empty() {
                parts.push(notice.to_owned());
            }
            Some(ChromePrimary {
                text: parts.join("  "),
                tone: ChromeTone::Accent,
                animated: true,
            })
        } else if !notice.is_empty() {
            Some(ChromePrimary {
                text: notice.to_owned(),
                tone: ChromeTone::Warning,
                animated: false,
            })
        } else {
            None
        };

        let mut slots = vec![slot(
            ChromeSlotKind::Run,
            label(locale, MessageId::ChromeRun),
            run_value,
            run_tone,
        )];
        if input.queued_follow_ups > 0 {
            slots.push(slot(
                ChromeSlotKind::Queue,
                label(locale, MessageId::ChromeQueue),
                input.queued_follow_ups.to_string(),
                ChromeTone::Accent,
            ));
        }
        slots.push(hitl_slot(locale, input.hitl));
        slots.push(isolation_slot(
            locale,
            input.isolation_profile,
            input.sandbox_commands,
        ));
        let (context_slot, context_protected) = context_slot(locale, input.context);
        slots.push(context_slot);
        slots.push(screen_mode_slot(locale, input.screen_mode));
        Self {
            primary,
            slots,
            context_protected,
        }
    }

    pub fn visible_slots(&self, width: u16) -> Vec<ChromeSlot> {
        self.slots
            .iter()
            .filter(|slot| match slot.kind {
                ChromeSlotKind::Run
                | ChromeSlotKind::Queue
                | ChromeSlotKind::Hitl
                | ChromeSlotKind::Isolation => true,
                ChromeSlotKind::Context => width >= 80 || self.context_protected,
                ChromeSlotKind::ScreenMode => width >= 120,
            })
            .cloned()
            .collect()
    }

    pub fn layout_slots(&self, width: u16, glyphs: Glyphs) -> Vec<LaidOutChromeSlot> {
        let visible = self.visible_slots(width);
        let separator = format!(" {} ", glyphs.separator());
        let separator_width = UnicodeWidthStr::width(separator.as_str());
        let mut texts = visible
            .iter()
            .map(|slot| slot.text.clone())
            .collect::<Vec<_>>();
        let row_width = |values: &[String]| {
            values
                .iter()
                .map(|value| UnicodeWidthStr::width(value.as_str()))
                .sum::<usize>()
                .saturating_add(separator_width.saturating_mul(values.len().saturating_sub(1)))
        };
        for kind in [
            ChromeSlotKind::Isolation,
            ChromeSlotKind::ScreenMode,
            ChromeSlotKind::Context,
            ChromeSlotKind::Run,
            ChromeSlotKind::Hitl,
        ] {
            if row_width(&texts) <= width as usize {
                break;
            }
            if let Some(index) = visible.iter().position(|slot| slot.kind == kind) {
                texts[index] = visible[index].compact_text.clone();
            }
        }

        let content_budget = (width as usize)
            .saturating_sub(separator_width.saturating_mul(texts.len().saturating_sub(1)));
        let mut text_widths = texts
            .iter()
            .map(|text| UnicodeWidthStr::width(text.as_str()))
            .collect::<Vec<_>>();
        let mut deficit = text_widths
            .iter()
            .sum::<usize>()
            .saturating_sub(content_budget);
        for kind in [
            ChromeSlotKind::Isolation,
            ChromeSlotKind::Run,
            ChromeSlotKind::Hitl,
            ChromeSlotKind::ScreenMode,
            ChromeSlotKind::Queue,
            ChromeSlotKind::Context,
        ] {
            if deficit == 0 {
                break;
            }
            if let Some(index) = visible.iter().position(|slot| slot.kind == kind) {
                let minimum = minimum_slot_width(kind).min(text_widths[index]);
                let reduction = deficit.min(text_widths[index].saturating_sub(minimum));
                text_widths[index] = text_widths[index].saturating_sub(reduction);
                deficit = deficit.saturating_sub(reduction);
            }
        }
        if deficit > 0 {
            for value in &mut text_widths {
                if deficit == 0 {
                    break;
                }
                let reduction = deficit.min(value.saturating_sub(1));
                *value = value.saturating_sub(reduction);
                deficit = deficit.saturating_sub(reduction);
            }
        }

        let mut laid_out = Vec::new();
        let mut used = 0_usize;
        for (index, slot) in visible.iter().enumerate() {
            if index > 0 {
                if used.saturating_add(separator_width) >= width as usize {
                    break;
                }
                used = used.saturating_add(separator_width);
            }
            let text = truncate_width_with_glyphs(&texts[index], text_widths[index], glyphs);
            let text_width = UnicodeWidthStr::width(text.as_str());
            if text_width == 0 {
                break;
            }
            laid_out.push(LaidOutChromeSlot {
                kind: slot.kind,
                text,
                tone: slot.tone,
                capability: slot.capability,
                start: used as u16,
                width: text_width as u16,
            });
            used = used.saturating_add(text_width);
        }
        laid_out
    }
}

fn minimum_slot_width(kind: ChromeSlotKind) -> usize {
    match kind {
        ChromeSlotKind::Run | ChromeSlotKind::ScreenMode => 4,
        ChromeSlotKind::Queue => 5,
        ChromeSlotKind::Hitl | ChromeSlotKind::Context => 3,
        ChromeSlotKind::Isolation => 6,
    }
}

fn slot(kind: ChromeSlotKind, label: &str, value: String, tone: ChromeTone) -> ChromeSlot {
    let compact_text = if kind == ChromeSlotKind::Queue {
        format!("{label} {value}")
    } else {
        value.clone()
    };
    ChromeSlot {
        kind,
        text: format!("{label} {value}"),
        compact_text,
        tone,
        capability: (kind == ChromeSlotKind::Queue).then_some(ChromeCapability::OpenQueue),
    }
}

fn label(locale: Locale, id: MessageId) -> &'static str {
    tr(locale, id)
}

fn execution_state(locale: Locale, status: &str) -> (String, ChromeTone, bool) {
    let normalized = status.trim().to_ascii_lowercase();
    let active = matches!(
        normalized.as_str(),
        "queued" | "pending" | "running" | "in_progress" | "working" | "active"
    );
    let tone = match normalized.as_str() {
        "failed" | "error" => ChromeTone::Danger,
        "waiting_approval"
        | "awaiting_approval"
        | "blocked_on_approval"
        | "degraded"
        | "partial"
        | "partially_complete"
        | "waiting_input"
        | "needs_input"
        | "blocked_on_input" => ChromeTone::Warning,
        "queued" | "pending" | "running" | "in_progress" | "working" | "active" => {
            ChromeTone::Accent
        }
        _ => ChromeTone::Muted,
    };
    let value = match normalized.as_str() {
        "waiting_input" | "needs_input" | "blocked_on_input" => {
            tr(locale, MessageId::ChromeWaitingInput).to_owned()
        }
        ""
        | "ready"
        | "completed"
        | "complete"
        | "done"
        | "idle"
        | "queued"
        | "pending"
        | "running"
        | "in_progress"
        | "working"
        | "active"
        | "waiting_approval"
        | "awaiting_approval"
        | "blocked_on_approval"
        | "paused"
        | "interrupted"
        | "cancelled"
        | "canceled"
        | "failed"
        | "error"
        | "degraded"
        | "partial"
        | "partially_complete" => localized_execution_status(locale, &normalized),
        _ => tr(locale, MessageId::UnknownValue).to_owned(),
    };
    (value, tone, active)
}

fn hitl_slot(locale: Locale, value: &str) -> ChromeSlot {
    let (id, tone) = match value.trim().to_ascii_lowercase().as_str() {
        "ask" => (MessageId::ChromeAsk, ChromeTone::Accent),
        "accept-edits" => (MessageId::ChromeAcceptEdits, ChromeTone::Warning),
        "dont-ask" => (MessageId::ChromeNeverAsk, ChromeTone::Warning),
        "always-approve" => (MessageId::ChromeAlwaysApprove, ChromeTone::Danger),
        _ => (MessageId::UnknownValue, ChromeTone::Muted),
    };
    slot(
        ChromeSlotKind::Hitl,
        label(locale, MessageId::ChromeHitl),
        tr(locale, id).to_owned(),
        tone,
    )
}

fn isolation_slot(locale: Locale, profile: &str, sandbox: Option<bool>) -> ChromeSlot {
    let id = match profile.trim().to_ascii_lowercase().as_str() {
        "read-only" => MessageId::ChromeProfileReadOnly,
        "safe-edit" => MessageId::ChromeProfileSafeEdit,
        "full-workspace" => MessageId::ChromeProfileFullWorkspace,
        "ci-runner" => MessageId::ChromeProfileCiRunner,
        "sandboxed" => MessageId::ChromeProfileSandboxed,
        "trusted-local" => MessageId::ChromeProfileTrusted,
        "enterprise-restricted" => MessageId::ChromeProfileRestricted,
        _ => MessageId::UnknownValue,
    };
    let mut value = tr(locale, id).to_owned();
    let tone = match sandbox {
        Some(true) => {
            value.push_str(" + ");
            value.push_str(tr(locale, MessageId::ChromeSandboxOn));
            ChromeTone::Muted
        }
        Some(false) => {
            value.push_str(" + ");
            value.push_str(tr(locale, MessageId::ChromeSandboxOff));
            ChromeTone::Warning
        }
        None => {
            value.push_str(" + ");
            value.push_str(tr(locale, MessageId::UnknownValue));
            ChromeTone::Muted
        }
    };
    slot(
        ChromeSlotKind::Isolation,
        label(locale, MessageId::ChromeIsolation),
        value,
        tone,
    )
}

fn context_slot(locale: Locale, context: Option<&ModelContextTokens>) -> (ChromeSlot, bool) {
    let context = context.filter(|context| {
        context.limit_tokens > 0
            || context.used_percent > 0
            || matches!(context.threshold.as_str(), "warning" | "critical")
    });
    let (value, tone, protected) = context.map_or_else(
        || {
            (
                tr(locale, MessageId::UnknownValue).to_owned(),
                ChromeTone::Muted,
                false,
            )
        },
        |context| {
            let protected = context.used_percent >= 80
                || matches!(context.threshold.as_str(), "warning" | "critical");
            let tone = if context.used_percent >= 90 || context.threshold == "critical" {
                ChromeTone::Danger
            } else if protected {
                ChromeTone::Warning
            } else {
                ChromeTone::Accent
            };
            let value = if context.estimated || context.estimated_limit {
                format!("~{}%", context.used_percent.min(100))
            } else {
                format!("{}%", context.used_percent.min(100))
            };
            (value, tone, protected)
        },
    );
    (
        slot(
            ChromeSlotKind::Context,
            label(locale, MessageId::ChromeContext),
            value,
            tone,
        ),
        protected,
    )
}

fn screen_mode_slot(locale: Locale, mode: &str) -> ChromeSlot {
    let id = match mode.trim().to_ascii_lowercase().as_str() {
        "minimal" => MessageId::ChromeModeMinimal,
        "inline" => MessageId::ChromeModeInline,
        "fullscreen" => MessageId::ChromeModeFullscreen,
        _ => MessageId::UnknownValue,
    };
    slot(
        ChromeSlotKind::ScreenMode,
        label(locale, MessageId::ChromeMode),
        tr(locale, id).to_owned(),
        ChromeTone::Muted,
    )
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
    let normalized = status.trim().to_ascii_lowercase();
    let id = match normalized.as_str() {
        "" | "ready" | "completed" | "complete" | "done" | "idle" => MessageId::StatusReady,
        "queued" | "pending" => MessageId::StatusQueued,
        "running" | "in_progress" | "working" | "active" => MessageId::StatusRunning,
        "waiting_approval" | "awaiting_approval" | "blocked_on_approval" => {
            MessageId::StatusWaitingApproval
        }
        "paused" | "interrupted" => MessageId::StatusPaused,
        "cancelled" | "canceled" => MessageId::StatusCancelled,
        "failed" | "error" => MessageId::StatusFailed,
        // Partial success / soft failure; not "unknown".
        "degraded" | "partial" | "partially_complete" => MessageId::StatusDegraded,
        // Daemon/session metadata missing a concrete lifecycle: treat as idle ready.
        "unknown" | "none" | "n/a" | "na" => MessageId::StatusReady,
        unknown => return tr_format(locale, MessageId::StatusUnknown, &[("status", unknown)]),
    };
    tr(locale, id).to_owned()
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
            permission_profile: "safe-edit".into(),
            approval_mode: "on_request".into(),
            created_at: String::new(),
            updated_at: String::new(),
            latest_run_id: String::new(),
            latest_run_agent: String::new(),
            latest_run_result_kind: String::new(),
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
        assert_eq!(
            localized_execution_status(Locale::ZhHans, "degraded"),
            "部分完成"
        );
        assert_eq!(localized_execution_status(Locale::En, "unknown"), "ready");
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
    fn composer_chrome_priority_table_is_explicit_at_60_80_and_120_columns() {
        let context = ModelContextTokens {
            used_percent: 12,
            ..ModelContextTokens::default()
        };
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "running",
            notice: "",
            elapsed: Some(Duration::from_secs(62)),
            activity: Some("running tests"),
            queued_follow_ups: 2,
            background_work: false,
            interrupt_key: "Esc",
            priority_notice: false,
            context: Some(&context),
            locale: Locale::En,
            screen_mode: "fullscreen",
            hitl: "ask",
            isolation_profile: "safe-edit",
            sandbox_commands: Some(true),
        });
        let kinds = |width| {
            chrome
                .visible_slots(width)
                .iter()
                .map(|slot| slot.kind)
                .collect::<Vec<_>>()
        };
        assert_eq!(
            kinds(60),
            [
                ChromeSlotKind::Run,
                ChromeSlotKind::Queue,
                ChromeSlotKind::Hitl,
                ChromeSlotKind::Isolation,
            ]
        );
        assert_eq!(
            kinds(80),
            [
                ChromeSlotKind::Run,
                ChromeSlotKind::Queue,
                ChromeSlotKind::Hitl,
                ChromeSlotKind::Isolation,
                ChromeSlotKind::Context,
            ]
        );
        assert_eq!(kinds(120).len(), 6);
        let primary = chrome.primary.as_ref().unwrap();
        assert!(primary.text.contains("running tests"));
        assert!(primary.text.contains("1m02"));
        assert!(primary.text.contains("Esc pause safely"));
        assert!(!primary.text.contains("+2 queued"));
    }

    #[test]
    fn composer_chrome_layout_owns_cjk_cell_geometry_and_compaction() {
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "running",
            notice: "",
            elapsed: None,
            activity: None,
            queued_follow_ups: 2,
            background_work: false,
            interrupt_key: "Esc",
            priority_notice: false,
            context: None,
            locale: Locale::ZhHans,
            screen_mode: "fullscreen",
            hitl: "ask",
            isolation_profile: "safe-edit",
            sandbox_commands: Some(true),
        });
        for mode in [
            crate::glyphs::GlyphMode::Unicode,
            crate::glyphs::GlyphMode::Ascii,
        ] {
            let glyphs = Glyphs::new(mode);
            let slots = chrome.layout_slots(60, glyphs);
            assert_eq!(
                slots.iter().map(|slot| slot.kind).collect::<Vec<_>>(),
                [
                    ChromeSlotKind::Run,
                    ChromeSlotKind::Queue,
                    ChromeSlotKind::Hitl,
                    ChromeSlotKind::Isolation,
                ]
            );
            let separator_width =
                UnicodeWidthStr::width(format!(" {} ", glyphs.separator()).as_str()) as u16;
            let mut expected_start = 0;
            for (index, slot) in slots.iter().enumerate() {
                if index > 0 {
                    expected_start += separator_width;
                }
                assert_eq!(slot.start, expected_start);
                assert_eq!(
                    slot.width,
                    UnicodeWidthStr::width(slot.text.as_str()) as u16
                );
                expected_start += slot.width;
            }
            assert!(expected_start <= 60);
        }
    }

    #[test]
    fn composer_chrome_layout_bounds_every_locale_and_keeps_protected_slots() {
        let context = ModelContextTokens {
            used_percent: 95,
            threshold: "critical".into(),
            ..ModelContextTokens::default()
        };
        for locale in Locale::ALL {
            let chrome = ComposerChrome::project(ComposerChromeInput {
                execution_status: "waiting_approval",
                notice: "",
                elapsed: None,
                activity: None,
                queued_follow_ups: 12,
                background_work: false,
                interrupt_key: "Esc",
                priority_notice: false,
                context: Some(&context),
                locale,
                screen_mode: "fullscreen",
                hitl: "always-approve",
                isolation_profile: "enterprise-restricted",
                sandbox_commands: Some(false),
            });
            for width in [60, 80, 120] {
                let slots =
                    chrome.layout_slots(width, Glyphs::new(crate::glyphs::GlyphMode::Unicode));
                assert!(
                    slots
                        .last()
                        .is_some_and(|slot| slot.start + slot.width <= width),
                    "locale={locale:?} width={width} slots={slots:?}"
                );
                for kind in [
                    ChromeSlotKind::Run,
                    ChromeSlotKind::Queue,
                    ChromeSlotKind::Hitl,
                    ChromeSlotKind::Isolation,
                    ChromeSlotKind::Context,
                ] {
                    assert!(
                        slots.iter().any(|slot| slot.kind == kind),
                        "locale={locale:?} width={width} missing={kind:?} slots={slots:?}"
                    );
                }
                assert_eq!(
                    slots
                        .iter()
                        .any(|slot| slot.kind == ChromeSlotKind::ScreenMode),
                    width == 120
                );
            }
        }
    }

    #[test]
    fn warning_and_critical_context_are_protected_at_60_columns() {
        for (percent, threshold, tone) in [
            (80, "warning", ChromeTone::Warning),
            (95, "critical", ChromeTone::Danger),
        ] {
            let context = ModelContextTokens {
                used_percent: percent,
                threshold: threshold.into(),
                ..ModelContextTokens::default()
            };
            let chrome = ComposerChrome::project(ComposerChromeInput {
                execution_status: "ready",
                notice: "",
                elapsed: None,
                activity: None,
                queued_follow_ups: 2,
                background_work: false,
                interrupt_key: "Esc",
                priority_notice: false,
                context: Some(&context),
                locale: Locale::En,
                screen_mode: "minimal",
                hitl: "ask",
                isolation_profile: "full-workspace",
                sandbox_commands: Some(true),
            });
            let context_slot = chrome
                .layout_slots(60, Glyphs::new(crate::glyphs::GlyphMode::Unicode))
                .into_iter()
                .find(|slot| slot.kind == ChromeSlotKind::Context)
                .unwrap();
            assert_eq!(context_slot.tone, tone);
            assert_eq!(context_slot.text, format!("{percent}%"));
        }
    }

    #[test]
    fn projector_normalizes_policy_values_and_unknowns_before_rendering() {
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "blocked_on_approval",
            notice: "",
            elapsed: None,
            activity: None,
            queued_follow_ups: 0,
            background_work: false,
            interrupt_key: "Esc",
            priority_notice: false,
            context: None,
            locale: Locale::ZhHans,
            screen_mode: "inline",
            hitl: "always-approve",
            isolation_profile: "future-profile",
            sandbox_commands: Some(false),
        });
        let all = chrome.visible_slots(120);
        let hitl = all
            .iter()
            .find(|slot| slot.kind == ChromeSlotKind::Hitl)
            .unwrap();
        assert_eq!(hitl.text, "审批 始终批准");
        assert_eq!(hitl.tone, ChromeTone::Danger);
        let isolation = all
            .iter()
            .find(|slot| slot.kind == ChromeSlotKind::Isolation)
            .unwrap();
        assert!(isolation.text.contains("未知"));
        assert!(!isolation.text.contains("future-profile"));
        assert!(all.iter().any(|slot| slot.text == "界面 行内"));
    }

    #[test]
    fn projector_never_exposes_unknown_run_or_missing_context_as_facts() {
        let context = ModelContextTokens::default();
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "future_wire_state",
            notice: "",
            elapsed: None,
            activity: None,
            queued_follow_ups: 0,
            background_work: false,
            interrupt_key: "Esc",
            priority_notice: false,
            context: Some(&context),
            locale: Locale::En,
            screen_mode: "future-screen",
            hitl: "future-hitl",
            isolation_profile: "future-profile",
            sandbox_commands: None,
        });
        let slots = chrome.visible_slots(120);
        let run = slots
            .iter()
            .find(|slot| slot.kind == ChromeSlotKind::Run)
            .unwrap();
        assert_eq!(run.text, "run unknown");
        assert!(!run.text.contains("future_wire_state"));
        let context = slots
            .iter()
            .find(|slot| slot.kind == ChromeSlotKind::Context)
            .unwrap();
        assert_eq!(context.text, "ctx unknown");
        let isolation = slots
            .iter()
            .find(|slot| slot.kind == ChromeSlotKind::Isolation)
            .unwrap();
        assert_eq!(isolation.text, "isolation unknown + unknown");
    }

    #[test]
    fn ordinary_context_is_accent_and_estimates_use_locale_neutral_geometry() {
        let context = ModelContextTokens {
            estimated: true,
            limit_tokens: 100_000,
            used_percent: 42,
            threshold: "normal".into(),
            ..ModelContextTokens::default()
        };
        let (slot, protected) = context_slot(Locale::Ja, Some(&context));
        assert!(!protected);
        assert_eq!(slot.text, "文脈 ~42%");
        assert_eq!(slot.tone, ChromeTone::Accent);
    }

    #[test]
    fn protected_layout_and_queue_capability_hold_for_every_locale() {
        let context = ModelContextTokens {
            limit_tokens: 100_000,
            used_percent: 95,
            threshold: "critical".into(),
            ..ModelContextTokens::default()
        };
        for locale in Locale::ALL {
            let chrome = ComposerChrome::project(ComposerChromeInput {
                execution_status: "running",
                notice: "",
                elapsed: None,
                activity: None,
                queued_follow_ups: 12,
                background_work: false,
                interrupt_key: "Esc",
                priority_notice: false,
                context: Some(&context),
                locale,
                screen_mode: "fullscreen",
                hitl: "accept-edits",
                isolation_profile: "enterprise-restricted",
                sandbox_commands: Some(false),
            });
            for mode in [
                crate::glyphs::GlyphMode::Unicode,
                crate::glyphs::GlyphMode::Ascii,
            ] {
                let glyphs = Glyphs::new(mode);
                let slots = chrome.layout_slots(60, glyphs);
                assert_eq!(
                    slots.iter().map(|slot| slot.kind).collect::<Vec<_>>(),
                    [
                        ChromeSlotKind::Run,
                        ChromeSlotKind::Queue,
                        ChromeSlotKind::Hitl,
                        ChromeSlotKind::Isolation,
                        ChromeSlotKind::Context,
                    ],
                    "protected slot set changed for {locale:?} in {mode:?}"
                );
                let queue = slots
                    .iter()
                    .find(|slot| slot.kind == ChromeSlotKind::Queue)
                    .unwrap();
                assert_eq!(queue.capability, Some(ChromeCapability::OpenQueue));
                let separator_width =
                    UnicodeWidthStr::width(format!(" {} ", glyphs.separator()).as_str());
                let row_width = slots
                    .iter()
                    .map(|slot| usize::from(slot.width))
                    .sum::<usize>()
                    + separator_width * slots.len().saturating_sub(1);
                assert!(
                    row_width <= 60,
                    "{locale:?} {mode:?} used {row_width} cells"
                );
            }
        }
    }

    #[test]
    fn routine_notice_follows_active_narrative_without_disabling_spinner() {
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "running",
            notice: "Message added",
            elapsed: Some(Duration::from_secs(3)),
            activity: Some("Working"),
            queued_follow_ups: 0,
            background_work: false,
            interrupt_key: "Esc",
            priority_notice: false,
            context: None,
            locale: Locale::En,
            screen_mode: "fullscreen",
            hitl: "ask",
            isolation_profile: "safe-edit",
            sandbox_commands: Some(true),
        });
        let primary = chrome.primary.unwrap();
        assert!(primary.animated);
        assert_eq!(primary.tone, ChromeTone::Accent);
        assert!(primary.text.starts_with("Working  3s  Esc pause safely"));
        assert!(primary.text.ends_with("Message added"));
    }

    #[test]
    fn waiting_for_input_is_localized_and_does_not_animate() {
        for locale in Locale::ALL {
            let chrome = ComposerChrome::project(ComposerChromeInput {
                execution_status: "waiting_input",
                notice: "",
                elapsed: Some(Duration::from_secs(3)),
                activity: Some("stale work"),
                queued_follow_ups: 0,
                background_work: false,
                interrupt_key: "Esc",
                priority_notice: false,
                context: None,
                locale,
                screen_mode: "fullscreen",
                hitl: "ask",
                isolation_profile: "safe-edit",
                sandbox_commands: Some(true),
            });
            assert!(chrome.primary.is_none());
            let run = chrome
                .visible_slots(60)
                .into_iter()
                .find(|slot| slot.kind == ChromeSlotKind::Run)
                .unwrap();
            assert_eq!(run.tone, ChromeTone::Warning);
            assert!(run.text.contains(tr(locale, MessageId::ChromeWaitingInput)));
        }
    }

    #[test]
    fn priority_notice_is_the_only_primary_narrative_owner() {
        let context = ModelContextTokens {
            used_percent: 12,
            ..ModelContextTokens::default()
        };
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "running",
            notice: "Runtime unavailable",
            elapsed: Some(Duration::from_secs(17)),
            activity: Some("stale activity"),
            queued_follow_ups: 0,
            background_work: false,
            interrupt_key: "Ctrl-C",
            priority_notice: true,
            context: Some(&context),
            locale: Locale::En,
            screen_mode: "fullscreen",
            hitl: "ask",
            isolation_profile: "safe-edit",
            sandbox_commands: Some(true),
        });
        assert_eq!(
            chrome.primary,
            Some(ChromePrimary {
                text: "Runtime unavailable".into(),
                tone: ChromeTone::Danger,
                animated: false,
            })
        );
        assert!(
            !chrome
                .visible_slots(120)
                .iter()
                .any(|slot| slot.text.contains("stale activity"))
        );
    }
}
