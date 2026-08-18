use std::time::{Duration, Instant};

use xai_ratatui_textarea::TextAreaState;

use crate::i18n::{MessageId, Notice};

use super::App;

pub(super) const DEFAULT_REWIND_PRIME_WINDOW: Duration = Duration::from_millis(800);
const REWIND_GRACE_ENV: &str = "CARINA_ESC_GRACE_MS";
const MIN_REWIND_GRACE_MS: u64 = 250;
const MAX_REWIND_GRACE_MS: u64 = 2_000;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum RewindEscapeAction {
    Busy,
    Unavailable,
    Prime,
    Open,
}

pub(super) fn rewind_prime_window() -> Duration {
    rewind_prime_window_from(std::env::var(REWIND_GRACE_ENV).ok().as_deref())
}

pub(super) fn rewind_prime_window_from(value: Option<&str>) -> Duration {
    value
        .and_then(|value| value.trim().parse::<u64>().ok())
        .filter(|millis| (MIN_REWIND_GRACE_MS..=MAX_REWIND_GRACE_MS).contains(millis))
        .map(Duration::from_millis)
        .unwrap_or(DEFAULT_REWIND_PRIME_WINDOW)
}

pub(super) fn rewind_escape_action(
    active_run: bool,
    has_eligible_history: bool,
    primed_at: Option<Instant>,
    now: Instant,
    grace: Duration,
) -> RewindEscapeAction {
    if active_run {
        return RewindEscapeAction::Busy;
    }
    if !has_eligible_history {
        return RewindEscapeAction::Unavailable;
    }
    if primed_at.is_some_and(|primed| now.saturating_duration_since(primed) <= grace) {
        RewindEscapeAction::Open
    } else {
        RewindEscapeAction::Prime
    }
}

impl App {
    pub(super) fn remember_submitted_draft(&mut self, prompt: &str) {
        let trimmed = prompt.trim();
        if trimmed.is_empty() {
            return;
        }
        self.last_submitted_draft = Some(trimmed.to_owned());
    }

    pub(super) fn restore_submitted_draft_if_pristine(&mut self) -> bool {
        if !self.composer_is_pristine() {
            return false;
        }
        let Some(draft) = self.last_submitted_draft.take() else {
            return false;
        };
        self.composer.set_text(&draft);
        self.composer.set_cursor(self.composer.text().len());
        self.composer_state = TextAreaState::default();
        self.media.reconcile(&self.composer);
        self.context_completion.update_context(&self.composer);
        true
    }

    pub(super) fn composer_is_pristine(&self) -> bool {
        self.composer.text().trim().is_empty()
            && self.media.is_empty()
            && self.pending_pastes.is_empty()
    }

    pub(super) fn handle_rewind_escape(&mut self) {
        let eligible = self.eligible_history_indices();
        let now = Instant::now();
        match rewind_escape_action(
            self.has_retained_run(),
            !eligible.is_empty(),
            self.rewind_primed_at,
            now,
            self.rewind_prime_window,
        ) {
            RewindEscapeAction::Busy => {
                self.notice = Notice::localized(MessageId::HistoryBusy);
                self.rewind_primed_at = None;
            }
            RewindEscapeAction::Unavailable => {
                self.notice = Notice::localized(MessageId::HistoryNoEarlierPrompt);
                self.rewind_primed_at = None;
            }
            RewindEscapeAction::Prime => {
                self.rewind_primed_at = Some(now);
                self.notice = Notice::localized(MessageId::HistoryPrime);
            }
            RewindEscapeAction::Open => {
                self.history_generation = self.history_generation.saturating_add(1);
                self.history_branch_request_id = None;
                self.history_branch_pending = false;
                self.history_stashed_draft = Some(self.composer.text().to_owned());
                self.history_original_scroll =
                    Some((self.transcript_scroll, self.transcript_follow_bottom));
                self.composer.set_text("");
                self.composer_state = TextAreaState::default();
                self.history_selected = eligible.last().copied();
                self.rewind_primed_at = None;
                self.sync_history_selection();
                self.notice = Notice::localized(MessageId::HistoryChoosePrompt);
            }
        }
    }
}
