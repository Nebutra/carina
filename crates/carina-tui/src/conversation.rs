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
use crate::rpc::{ExecutionRun, LiveActivityUpdate, ModelContextTokens, Session, WireEvent};
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FooterMode {
    PriorityNotice,
    Running,
    Queued,
    WaitingApproval,
    WaitingInput,
    Paused,
    Failed,
    Notice,
    Idle,
    Unknown,
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
    pub mode: FooterMode,
    slots: Vec<ChromeSlot>,
    context_protected: bool,
}

pub struct ComposerChromeInput<'a> {
    pub execution_status: &'a str,
    pub notice: &'a str,
    pub elapsed: Option<Duration>,
    pub activity: Option<&'a str>,
    pub activity_elapsed: Option<Duration>,
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
        let mode = footer_mode(
            input.execution_status,
            input.priority_notice,
            !input.notice.trim().is_empty(),
        );
        let (mut run_value, run_tone, active) = execution_state(locale, input.execution_status);
        if active && let Some(elapsed) = input.elapsed {
            run_value.push(' ');
            run_value.push_str(&fmt_elapsed_compact(elapsed));
        }
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
                let activity = input.activity_elapsed.map_or_else(
                    || activity.trim().to_owned(),
                    |elapsed| format!("{} {}", activity.trim(), fmt_elapsed_compact(elapsed)),
                );
                parts.push(activity);
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
            mode,
            slots,
            context_protected,
        }
    }

    pub fn visible_slots(&self, width: u16) -> Vec<ChromeSlot> {
        self.slots
            .iter()
            .filter(|slot| self.slot_visible(slot.kind, width))
            .cloned()
            .collect()
    }

    pub fn row_count(&self, width: u16) -> u16 {
        u16::from(self.primary.is_some()) + u16::from(!self.visible_slots(width).is_empty())
    }

    fn slot_visible(&self, kind: ChromeSlotKind, _width: u16) -> bool {
        if kind == ChromeSlotKind::Queue {
            return true;
        }
        if kind == ChromeSlotKind::Context && self.context_protected {
            return true;
        }
        match self.mode {
            FooterMode::Running | FooterMode::Queued => kind == ChromeSlotKind::Run,
            FooterMode::WaitingApproval
            | FooterMode::WaitingInput
            | FooterMode::Paused
            | FooterMode::Failed
            | FooterMode::Unknown => kind == ChromeSlotKind::Run,
            FooterMode::PriorityNotice | FooterMode::Notice => false,
            FooterMode::Idle => false,
        }
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
    let active = execution_status_animates(&normalized);
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

fn footer_mode(status: &str, priority_notice: bool, has_notice: bool) -> FooterMode {
    if priority_notice && has_notice {
        return FooterMode::PriorityNotice;
    }
    match status.trim().to_ascii_lowercase().as_str() {
        "queued" | "pending" => FooterMode::Queued,
        "running" | "in_progress" | "working" | "active" => FooterMode::Running,
        "waiting_approval" | "awaiting_approval" | "blocked_on_approval" => {
            FooterMode::WaitingApproval
        }
        "waiting_input" | "needs_input" | "blocked_on_input" => FooterMode::WaitingInput,
        "paused" | "interrupted" => FooterMode::Paused,
        "failed" | "error" | "degraded" | "partial" | "partially_complete" => FooterMode::Failed,
        "" | "ready" | "completed" | "complete" | "done" | "idle" | "cancelled" | "canceled"
        | "unknown" | "none" | "n/a" | "na" => {
            if has_notice {
                FooterMode::Notice
            } else {
                FooterMode::Idle
            }
        }
        _ if has_notice => FooterMode::Notice,
        _ => FooterMode::Unknown,
    }
}

pub(crate) fn execution_status_animates(status: &str) -> bool {
    matches!(
        status.trim().to_ascii_lowercase().as_str(),
        "queued" | "pending" | "running" | "in_progress" | "working" | "active"
    )
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

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ActiveRunPresentation {
    pub run_id: String,
    pub requested_model: String,
    pub effective_model: String,
    pub provider: String,
    pub requested_reasoning_effort: String,
    pub effective_reasoning_effort: String,
}

impl ActiveRunPresentation {
    pub fn from_execution(execution: &ExecutionRun) -> Self {
        let mut presentation = Self {
            run_id: execution.run_id.clone(),
            ..Self::default()
        };
        presentation.merge(
            &execution.requested_model,
            first_non_empty_value(&execution.effective_model, &execution.model),
            "",
            &execution.requested_reasoning_effort,
            &execution.effective_reasoning_effort,
        );
        presentation
    }

    pub fn seed_request(&mut self, model: &str, reasoning_effort: &str) {
        merge_non_empty(&mut self.requested_model, model);
        merge_non_empty(&mut self.requested_reasoning_effort, reasoning_effort);
    }

    pub fn apply_event(&mut self, event: &WireEvent) -> bool {
        if event.run_id.trim().is_empty() || event.run_id != self.run_id {
            return false;
        }
        let text = |key: &str| {
            event
                .payload
                .get(key)
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default()
        };
        let before = self.clone();
        match event.kind.as_str() {
            "ExecutionQueued" => self.merge(
                text("requested_model"),
                first_non_empty_value(text("effective_model"), text("model")),
                text("provider"),
                text("requested_reasoning_effort"),
                text("effective_reasoning_effort"),
            ),
            "ModelRequested" => self.merge(
                text("model"),
                "",
                text("provider"),
                text("reasoning_effort"),
                "",
            ),
            "RoutingDecision" => self.merge(
                text("requested_model"),
                "",
                text("provider"),
                text("requested_reasoning_effort"),
                text("effective_reasoning_effort"),
            ),
            "RoutingOutcome" => self.merge(
                text("requested_model"),
                text("model"),
                text("provider"),
                text("requested_reasoning_effort"),
                text("effective_reasoning_effort"),
            ),
            _ => {}
        }
        *self != before
    }

    pub fn actual_model(&self) -> &str {
        first_non_empty_value(&self.effective_model, &self.requested_model)
    }

    pub fn actual_reasoning_effort(&self) -> &str {
        first_non_empty_value(
            &self.effective_reasoning_effort,
            &self.requested_reasoning_effort,
        )
    }

    fn merge(
        &mut self,
        requested_model: &str,
        effective_model: &str,
        provider: &str,
        requested_effort: &str,
        effective_effort: &str,
    ) {
        merge_non_empty(&mut self.requested_model, requested_model);
        merge_non_empty(&mut self.effective_model, effective_model);
        merge_non_empty(&mut self.provider, provider);
        merge_non_empty(&mut self.requested_reasoning_effort, requested_effort);
        merge_non_empty(&mut self.effective_reasoning_effort, effective_effort);
    }
}

fn merge_non_empty(target: &mut String, value: &str) {
    let value = value.trim();
    if !value.is_empty() {
        target.clear();
        target.push_str(value);
    }
}

fn first_non_empty_value<'a>(first: &'a str, second: &'a str) -> &'a str {
    if first.trim().is_empty() {
        second
    } else {
        first
    }
}

#[derive(Debug, Default)]
pub struct ExecutionTimer {
    accumulated: Duration,
    running_since: Option<std::time::Instant>,
    observed: bool,
}

#[derive(Debug, Default)]
pub struct ExecutionActivityTracker {
    active: Vec<ExecutionActivity>,
}

#[derive(Debug)]
struct ExecutionActivity {
    key: String,
    label: String,
    started_at: std::time::Instant,
}

impl ExecutionActivityTracker {
    pub fn apply(&mut self, update: LiveActivityUpdate) -> bool {
        match update {
            LiveActivityUpdate::Started { key, label } => {
                self.active.retain(|activity| activity.key != key);
                self.active.push(ExecutionActivity {
                    key,
                    label,
                    started_at: std::time::Instant::now(),
                });
                true
            }
            LiveActivityUpdate::Finished { key } => {
                let previous_len = self.active.len();
                self.active.retain(|activity| activity.key != key);
                self.active.len() != previous_len
            }
        }
    }

    pub fn clear(&mut self) {
        self.active.clear();
    }

    pub fn current(&self) -> Option<(&str, Duration)> {
        self.active
            .last()
            .map(|activity| (activity.label.as_str(), activity.started_at.elapsed()))
    }

    #[cfg(test)]
    pub fn set_single(&mut self, label: impl Into<String>) {
        self.active = vec![ExecutionActivity {
            key: "test".into(),
            label: label.into(),
            started_at: std::time::Instant::now(),
        }];
    }

    #[cfg(test)]
    fn set_single_elapsed(&mut self, label: impl Into<String>, elapsed: Duration) {
        self.active = vec![ExecutionActivity {
            key: "test".into(),
            label: label.into(),
            started_at: std::time::Instant::now()
                .checked_sub(elapsed)
                .unwrap_or_else(std::time::Instant::now),
        }];
    }
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
    if let Some(value) = session.and_then(|session| non_empty(&session.name)) {
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
            model_preference_revision: 0,
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
    fn conversation_title_uses_stable_name_and_never_latest_assistant_summary() {
        let mut value = session();
        value.summary = "Refactor provider setup\ninternal detail".into();
        assert_eq!(
            conversation_title(Some(&value), Path::new("/tmp/carina"), Locale::En),
            "carina conversation"
        );
        value.name = "Provider onboarding".into();
        assert_eq!(
            conversation_title(Some(&value), Path::new("/tmp/carina"), Locale::En),
            "Provider onboarding"
        );
    }

    #[test]
    fn active_run_presentation_merges_requested_and_effective_route_truth() {
        let execution: ExecutionRun = serde_json::from_value(serde_json::json!({
            "run_id": "run-1",
            "session_id": "session-1",
            "status": "queued",
            "requested_model": "provider/requested",
            "requested_reasoning_effort": "high"
        }))
        .unwrap();
        let mut presentation = ActiveRunPresentation::from_execution(&execution);
        assert_eq!(presentation.actual_model(), "provider/requested");

        let outcome: WireEvent = serde_json::from_value(serde_json::json!({
            "type": "RoutingOutcome",
            "run_id": "run-1",
            "payload": {
                "requested_model": "provider/requested",
                "provider": "provider",
                "model": "provider/effective",
                "effective_reasoning_effort": "medium"
            }
        }))
        .unwrap();
        assert!(presentation.apply_event(&outcome));
        assert_eq!(presentation.actual_model(), "provider/effective");
        assert_eq!(presentation.provider, "provider");
        assert_eq!(presentation.actual_reasoning_effort(), "medium");

        let empty_started: WireEvent = serde_json::from_value(serde_json::json!({
            "type": "ExecutionStarted",
            "run_id": "run-1",
            "payload": {}
        }))
        .unwrap();
        assert!(!presentation.apply_event(&empty_started));
        assert_eq!(presentation.actual_model(), "provider/effective");

        let stale: WireEvent = serde_json::from_value(serde_json::json!({
            "type": "RoutingOutcome",
            "run_id": "run-old",
            "payload": {"model": "provider/stale"}
        }))
        .unwrap();
        assert!(!presentation.apply_event(&stale));
        assert_eq!(presentation.actual_model(), "provider/effective");
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
                    UnicodeWidthStr::width(glyphs.activity_spinner(elapsed.as_millis())),
                    1
                );
            }
        }
    }

    #[test]
    fn activity_tracker_restores_the_still_running_parallel_tool() {
        let mut tracker = ExecutionActivityTracker::default();
        tracker.apply(LiveActivityUpdate::Started {
            key: "tool:map".into(),
            label: "code.map".into(),
        });
        tracker.apply(LiveActivityUpdate::Started {
            key: "tool:list".into(),
            label: "list".into(),
        });
        assert_eq!(tracker.current().map(|(label, _)| label), Some("list"));

        assert!(tracker.apply(LiveActivityUpdate::Finished {
            key: "tool:list".into(),
        }));
        assert_eq!(tracker.current().map(|(label, _)| label), Some("code.map"));

        assert!(tracker.apply(LiveActivityUpdate::Finished {
            key: "tool:map".into(),
        }));
        assert_eq!(tracker.current(), None);
    }

    #[test]
    fn activity_elapsed_is_independent_from_run_elapsed() {
        let mut tracker = ExecutionActivityTracker::default();
        tracker.set_single_elapsed("list", Duration::from_secs(2));
        let (activity, elapsed) = tracker.current().expect("active activity");
        assert_eq!(activity, "list");
        assert!(elapsed >= Duration::from_secs(2));

        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "running",
            notice: "",
            elapsed: Some(Duration::from_secs(707)),
            activity: Some(activity),
            activity_elapsed: Some(elapsed),
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
        let primary = &chrome.primary.as_ref().expect("active primary").text;
        assert!(primary.starts_with("list 2s"), "{primary}");
        assert!(!primary.contains("11m47"), "{primary}");
        assert!(
            chrome
                .visible_slots(120)
                .iter()
                .any(|slot| slot.kind == ChromeSlotKind::Run && slot.text.contains("11m47"))
        );
    }

    #[test]
    fn only_visually_active_execution_states_request_motion() {
        for status in [
            "queued",
            "pending",
            "running",
            "in_progress",
            "working",
            "active",
        ] {
            assert!(execution_status_animates(status), "status={status}");
        }
        for status in [
            "",
            "ready",
            "waiting_approval",
            "waiting_input",
            "paused",
            "completed",
            "failed",
            "future_state",
        ] {
            assert!(!execution_status_animates(status), "status={status}");
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
            activity_elapsed: Some(Duration::from_secs(4)),
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
        for width in [60, 80, 120, 160] {
            assert_eq!(kinds(width), [ChromeSlotKind::Run, ChromeSlotKind::Queue]);
        }
        let primary = chrome.primary.as_ref().unwrap();
        assert!(primary.text.contains("running tests"));
        assert!(primary.text.contains("4s"));
        assert!(!primary.text.contains("1m02"));
        assert!(primary.text.contains("Esc pause safely"));
        assert!(!primary.text.contains("+2 queued"));
        assert!(
            chrome
                .visible_slots(120)
                .iter()
                .any(|slot| slot.kind == ChromeSlotKind::Run && slot.text.contains("1m02"))
        );
    }

    #[test]
    fn composer_chrome_layout_owns_cjk_cell_geometry_and_compaction() {
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "running",
            notice: "",
            elapsed: None,
            activity: None,
            activity_elapsed: None,
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
                [ChromeSlotKind::Run, ChromeSlotKind::Queue]
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
                activity_elapsed: None,
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
                    ChromeSlotKind::Context,
                ] {
                    assert!(
                        slots.iter().any(|slot| slot.kind == kind),
                        "locale={locale:?} width={width} missing={kind:?} slots={slots:?}"
                    );
                }
                assert!(!slots.iter().any(|slot| matches!(
                    slot.kind,
                    ChromeSlotKind::Hitl | ChromeSlotKind::Isolation | ChromeSlotKind::ScreenMode
                )));
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
                activity_elapsed: None,
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
            assert_eq!(context_slot.text, format!("ctx {percent}%"));
        }
    }

    #[test]
    fn idle_footer_is_quiet_at_every_width() {
        let context = ModelContextTokens {
            used_percent: 42,
            ..ModelContextTokens::default()
        };
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "ready",
            notice: "",
            elapsed: None,
            activity: None,
            activity_elapsed: None,
            queued_follow_ups: 0,
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
        assert!(kinds(60).is_empty());
        assert!(kinds(80).is_empty());
        assert!(kinds(120).is_empty());
        assert!(kinds(160).is_empty());
        assert_eq!(chrome.row_count(60), 0);
        assert_eq!(chrome.row_count(80), 0);
        assert_eq!(chrome.row_count(120), 0);
        assert_eq!(chrome.row_count(160), 0);
    }

    #[test]
    fn chrome_height_is_derived_from_visible_state_rows() {
        let project = |status: &str, notice: &str, width: u16| {
            ComposerChrome::project(ComposerChromeInput {
                execution_status: status,
                notice,
                elapsed: None,
                activity: None,
                activity_elapsed: None,
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
            })
            .row_count(width)
        };

        assert_eq!(project("ready", "", 80), 0);
        assert_eq!(project("running", "", 80), 2);
        assert_eq!(project("waiting_approval", "", 80), 1);
        assert_eq!(project("failed", "", 80), 1);
        assert_eq!(project("ready", "Message added", 60), 1);
        assert_eq!(project("ready", "Message added", 80), 1);
    }

    #[test]
    fn projector_normalizes_policy_values_and_unknowns_before_rendering() {
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "blocked_on_approval",
            notice: "",
            elapsed: None,
            activity: None,
            activity_elapsed: None,
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
        assert_eq!(all.len(), 1);
        assert_eq!(all[0].kind, ChromeSlotKind::Run);
        assert_eq!(all[0].tone, ChromeTone::Warning);
    }

    #[test]
    fn projector_never_exposes_unknown_run_or_missing_context_as_facts() {
        let context = ModelContextTokens::default();
        let chrome = ComposerChrome::project(ComposerChromeInput {
            execution_status: "future_wire_state",
            notice: "",
            elapsed: None,
            activity: None,
            activity_elapsed: None,
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
        assert_eq!(slots.len(), 1);
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
                activity_elapsed: None,
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
            activity_elapsed: Some(Duration::from_secs(1)),
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
        assert!(primary.text.starts_with("Working 1s  Esc pause safely"));
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
                activity_elapsed: Some(Duration::from_secs(1)),
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
            activity_elapsed: Some(Duration::from_secs(1)),
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
