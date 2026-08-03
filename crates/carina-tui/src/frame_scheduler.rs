use std::collections::{BTreeMap, BTreeSet};
use std::time::{Duration, Instant};

use crate::rpc::FeedbackPhase;

pub const DEFAULT_MIN_DRAW_INTERVAL: Duration = Duration::from_millis(16);
pub const SLOW_TICK_INTERVAL: Duration = Duration::from_millis(83);
pub const SPINNER_INTERVAL: Duration = Duration::from_millis(80);
pub const RESIZE_DEBOUNCE: Duration = Duration::from_millis(75);
pub const REDUCED_MOTION_ENV: &str = "CARINA_REDUCED_MOTION";
const FEEDBACK_SAMPLE_LIMIT: usize = 512;

pub fn reduced_motion_enabled() -> bool {
    reduced_motion_from_environment(std::env::var(REDUCED_MOTION_ENV).ok().as_deref())
}

pub(crate) fn reduced_motion_from_environment(value: Option<&str>) -> bool {
    value.is_some_and(|value| {
        matches!(
            value.trim().to_ascii_lowercase().as_str(),
            "1" | "true" | "yes" | "on"
        )
    })
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FeedbackMarker {
    pub phase: FeedbackPhase,
    pub key: String,
    pub received_at: Instant,
}

impl FeedbackMarker {
    pub fn new(phase: FeedbackPhase, key: impl Into<String>, received_at: Instant) -> Self {
        Self {
            phase,
            key: key.into(),
            received_at,
        }
    }
}

impl FeedbackPhase {
    pub const fn budget(self) -> Duration {
        match self {
            Self::ToolStart => Duration::from_millis(80),
            Self::FirstToken | Self::ApprovalWait | Self::ToolResult | Self::Completion => {
                Duration::from_millis(100)
            }
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum RedrawReason {
    Initial,
    Input,
    Stream,
    AsyncResult,
    Animation,
    Resize,
    Focus,
    Media,
    Recovery,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TickDemand {
    None,
    Slow,
    Fast,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum WaitPlan {
    Block,
    For(Duration),
}

pub fn wait_plan(deadline: Option<Instant>, now: Instant) -> WaitPlan {
    deadline.map_or(WaitPlan::Block, |deadline| {
        WaitPlan::For(deadline.saturating_duration_since(now))
    })
}

#[derive(Debug, Clone)]
pub struct FrameStats {
    requested: BTreeMap<RedrawReason, u64>,
    coalesced: u64,
    presented: u64,
    latency_micros: Vec<u64>,
    render_micros: Vec<u64>,
    feedback_latency_micros: BTreeMap<FeedbackPhase, Vec<u64>>,
}

impl FrameStats {
    pub fn requested(&self, reason: RedrawReason) -> u64 {
        self.requested.get(&reason).copied().unwrap_or(0)
    }

    pub fn coalesced(&self) -> u64 {
        self.coalesced
    }

    pub fn presented(&self) -> u64 {
        self.presented
    }

    pub fn p95_latency(&self) -> Duration {
        if self.latency_micros.is_empty() {
            return Duration::ZERO;
        }
        let mut samples = self.latency_micros.clone();
        samples.sort_unstable();
        let index = ((samples.len() * 95).div_ceil(100)).saturating_sub(1);
        Duration::from_micros(samples[index])
    }

    pub fn feedback_count(&self, phase: FeedbackPhase) -> usize {
        self.feedback_latency_micros.get(&phase).map_or(0, Vec::len)
    }

    pub fn feedback_p95(&self, phase: FeedbackPhase) -> Duration {
        Duration::from_micros(percentile_95(
            self.feedback_latency_micros
                .get(&phase)
                .map_or(&[], Vec::as_slice),
        ))
    }

    pub fn feedback_budget_violations(&self) -> Vec<FeedbackPhase> {
        FeedbackPhase::ALL
            .into_iter()
            .filter(|phase| {
                self.feedback_count(*phase) > 0 && self.feedback_p95(*phase) > phase.budget()
            })
            .collect()
    }

    pub fn debug_report(&self) -> serde_json::Value {
        let requested = self
            .requested
            .iter()
            .map(|(reason, count)| (format!("{reason:?}").to_ascii_lowercase(), *count))
            .collect::<BTreeMap<_, _>>();
        let feedback_latency = FeedbackPhase::ALL
            .into_iter()
            .map(|phase| {
                let count = self.feedback_count(phase);
                let p95 = self.feedback_p95(phase);
                let budget = phase.budget();
                (
                    phase.as_str().to_owned(),
                    serde_json::json!({
                        "count": count,
                        "p95_us": p95.as_micros(),
                        "budget_us": budget.as_micros(),
                        "over_budget": count > 0 && p95 > budget,
                    }),
                )
            })
            .collect::<serde_json::Map<_, _>>();
        serde_json::json!({
            "requested": requested,
            "coalesced": self.coalesced,
            "presented": self.presented,
            "render_schedule_p95_us": self.p95_latency().as_micros(),
            "render_p95_us": percentile_95(&self.render_micros),
            "feedback_latency": feedback_latency,
        })
    }

    fn record_feedback(&mut self, phase: FeedbackPhase, duration: Duration) {
        let samples = self.feedback_latency_micros.entry(phase).or_default();
        if samples.len() == FEEDBACK_SAMPLE_LIMIT {
            samples.remove(0);
        }
        samples.push(duration.as_micros().min(u64::MAX as u128) as u64);
    }
}

#[derive(Debug, Clone)]
pub struct FrameScheduler {
    min_draw_interval: Duration,
    reduced_motion: bool,
    dirty_since: Option<Instant>,
    scheduled_at: Option<Instant>,
    last_draw_at: Option<Instant>,
    resize_at: Option<Instant>,
    tick_demand: TickDemand,
    next_tick_at: Option<Instant>,
    pending_feedback: BTreeMap<(FeedbackPhase, String), Instant>,
    observed_feedback: BTreeSet<(FeedbackPhase, String)>,
    stats: FrameStats,
}

impl FrameScheduler {
    pub fn new(now: Instant) -> Self {
        Self::with_reduced_motion(now, reduced_motion_enabled())
    }

    fn with_reduced_motion(now: Instant, reduced_motion: bool) -> Self {
        let mut scheduler = Self {
            min_draw_interval: min_draw_interval(),
            reduced_motion,
            dirty_since: None,
            scheduled_at: None,
            last_draw_at: None,
            resize_at: None,
            tick_demand: TickDemand::None,
            next_tick_at: None,
            pending_feedback: BTreeMap::new(),
            observed_feedback: BTreeSet::new(),
            stats: FrameStats {
                requested: BTreeMap::new(),
                coalesced: 0,
                presented: 0,
                latency_micros: Vec::new(),
                render_micros: Vec::new(),
                feedback_latency_micros: BTreeMap::new(),
            },
        };
        scheduler.request(RedrawReason::Initial, now);
        scheduler
    }

    pub fn request(&mut self, reason: RedrawReason, now: Instant) {
        *self.stats.requested.entry(reason).or_default() += 1;
        if self.dirty_since.is_some() {
            self.stats.coalesced += 1;
            return;
        }
        self.dirty_since = Some(now);
        self.scheduled_at = Some(self.earliest_draw_at(now));
    }

    pub fn request_resize(&mut self, now: Instant) {
        *self
            .stats
            .requested
            .entry(RedrawReason::Resize)
            .or_default() += 1;
        if self.resize_at.is_some() {
            self.stats.coalesced += 1;
        }
        let deadline = now + RESIZE_DEBOUNCE;
        self.resize_at = Some(deadline);
        self.dirty_since.get_or_insert(now);
        self.scheduled_at = Some(deadline.max(self.earliest_draw_at(now)));
    }

    pub fn resize_due(&mut self, now: Instant) -> bool {
        if self.resize_at.is_some_and(|deadline| deadline <= now) {
            self.resize_at = None;
            true
        } else {
            false
        }
    }

    pub fn set_tick_demand(&mut self, demand: TickDemand, now: Instant) {
        let demand = if self.reduced_motion {
            TickDemand::None
        } else {
            demand
        };
        if self.tick_demand == demand {
            return;
        }
        self.tick_demand = demand;
        self.next_tick_at = tick_interval(demand).map(|interval| now + interval);
    }

    pub fn request_feedback(&mut self, marker: FeedbackMarker) {
        let key = marker.key.trim();
        if key.is_empty() {
            return;
        }
        let identity = (marker.phase, key.to_owned());
        if self.observed_feedback.contains(&identity) {
            return;
        }
        self.pending_feedback
            .entry(identity)
            .and_modify(|received_at| *received_at = (*received_at).min(marker.received_at))
            .or_insert(marker.received_at);
        self.request(RedrawReason::Stream, marker.received_at);
    }

    pub fn advance_tick(&mut self, now: Instant) -> bool {
        let Some(interval) = tick_interval(self.tick_demand) else {
            self.next_tick_at = None;
            return false;
        };
        if self.next_tick_at.is_none_or(|deadline| deadline > now) {
            return false;
        }
        self.next_tick_at = Some(now + interval);
        self.request(RedrawReason::Animation, now);
        true
    }

    pub fn deadline(&self) -> Option<Instant> {
        [self.scheduled_at, self.next_tick_at, self.resize_at]
            .into_iter()
            .flatten()
            .min()
    }

    pub fn should_present(&self, now: Instant) -> bool {
        self.scheduled_at.is_some_and(|deadline| deadline <= now)
    }

    pub fn try_present<E>(
        &mut self,
        now: Instant,
        draw: impl FnOnce() -> Result<bool, E>,
    ) -> Result<bool, E> {
        if !self.should_present(now) {
            return Ok(false);
        }
        let render_started = Instant::now();
        let submitted = draw()?;
        self.record_render_duration(render_started.elapsed());
        if submitted {
            self.presented(now);
        } else {
            self.defer(now);
        }
        Ok(submitted)
    }

    pub fn presented(&mut self, now: Instant) {
        if let Some(requested_at) = self.dirty_since.take() {
            let micros = now.saturating_duration_since(requested_at).as_micros();
            self.stats
                .latency_micros
                .push(micros.min(u64::MAX as u128) as u64);
        }
        self.scheduled_at = None;
        self.last_draw_at = Some(now);
        self.stats.presented += 1;
        for (identity, received_at) in std::mem::take(&mut self.pending_feedback) {
            self.stats
                .record_feedback(identity.0, now.saturating_duration_since(received_at));
            self.observed_feedback.insert(identity);
        }
    }

    pub fn defer(&mut self, now: Instant) {
        self.scheduled_at = Some(now + self.min_draw_interval);
    }

    pub fn record_render_duration(&mut self, duration: Duration) {
        self.stats
            .render_micros
            .push(duration.as_micros().min(u64::MAX as u128) as u64);
    }

    pub fn stats(&self) -> &FrameStats {
        &self.stats
    }

    fn earliest_draw_at(&self, now: Instant) -> Instant {
        self.last_draw_at
            .map_or(now, |last| (last + self.min_draw_interval).max(now))
    }
}

fn tick_interval(demand: TickDemand) -> Option<Duration> {
    match demand {
        TickDemand::None => None,
        TickDemand::Slow => Some(SLOW_TICK_INTERVAL),
        TickDemand::Fast => Some(SPINNER_INTERVAL),
    }
}

fn min_draw_interval() -> Duration {
    std::env::var("CARINA_MIN_DRAW_MS")
        .ok()
        .and_then(|value| value.parse::<u64>().ok())
        .map(|millis| Duration::from_millis(millis.clamp(8, 50)))
        .unwrap_or(DEFAULT_MIN_DRAW_INTERVAL)
}

fn percentile_95(samples: &[u64]) -> u64 {
    if samples.is_empty() {
        return 0;
    }
    let mut samples = samples.to_vec();
    samples.sort_unstable();
    let index = ((samples.len() * 95).div_ceil(100)).saturating_sub(1);
    samples[index]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn requests_coalesce_at_the_earliest_legal_draw_time() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        assert!(scheduler.should_present(start));
        scheduler.presented(start);

        scheduler.request(RedrawReason::Stream, start + Duration::from_millis(2));
        scheduler.request(RedrawReason::Stream, start + Duration::from_millis(3));
        assert!(!scheduler.should_present(start + Duration::from_millis(15)));
        assert!(scheduler.should_present(start + Duration::from_millis(16)));
        scheduler.presented(start + Duration::from_millis(16));

        assert_eq!(scheduler.stats().requested(RedrawReason::Stream), 2);
        assert_eq!(scheduler.stats().coalesced(), 1);
        assert_eq!(scheduler.stats().presented(), 2);
    }

    #[test]
    fn idle_has_no_deadline_and_ticks_do_not_catch_up() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.presented(start);
        assert_eq!(scheduler.deadline(), None);
        assert_eq!(wait_plan(scheduler.deadline(), start), WaitPlan::Block);

        scheduler.set_tick_demand(TickDemand::Slow, start);
        let late = start + Duration::from_secs(2);
        assert!(scheduler.advance_tick(late));
        assert!(scheduler.should_present(late));
        scheduler.presented(late);
        assert_eq!(scheduler.deadline(), Some(late + SLOW_TICK_INTERVAL));
    }

    #[test]
    fn fast_animation_uses_an_explicit_spinner_deadline_without_catch_up() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.presented(start);
        scheduler.set_tick_demand(TickDemand::Fast, start);

        assert_eq!(scheduler.deadline(), Some(start + SPINNER_INTERVAL));
        assert!(!scheduler.advance_tick(start + SPINNER_INTERVAL - Duration::from_millis(1)));

        let first = start + SPINNER_INTERVAL;
        assert!(scheduler.advance_tick(first));
        assert!(scheduler.should_present(first));
        assert_eq!(scheduler.stats().requested(RedrawReason::Animation), 1);
        scheduler.presented(first);

        let late = start + Duration::from_secs(2);
        assert!(scheduler.advance_tick(late));
        assert!(scheduler.should_present(late));
        scheduler.presented(late);
        assert_eq!(scheduler.deadline(), Some(late + SPINNER_INTERVAL));
        assert_eq!(scheduler.stats().requested(RedrawReason::Animation), 2);
    }

    #[test]
    fn reduced_motion_parsing_is_explicit_and_fail_open() {
        for enabled in ["1", " true ", "YES", "On"] {
            assert!(
                reduced_motion_from_environment(Some(enabled)),
                "{enabled:?}"
            );
        }
        for enabled in [None, Some(""), Some("0"), Some("false"), Some("reduce")] {
            assert!(!reduced_motion_from_environment(enabled), "{enabled:?}");
        }
    }

    #[test]
    fn reduced_motion_suppresses_all_decorative_tick_deadlines() {
        let start = Instant::now();
        for demand in [TickDemand::Slow, TickDemand::Fast] {
            let mut scheduler = FrameScheduler::with_reduced_motion(start, true);
            scheduler.presented(start);
            scheduler.set_tick_demand(demand, start);

            assert_eq!(scheduler.deadline(), None);
            assert!(!scheduler.advance_tick(start + Duration::from_secs(1)));
            assert_eq!(scheduler.stats().requested(RedrawReason::Animation), 0);
        }
    }

    #[test]
    fn clearing_tick_demand_freezes_motion_without_blocking_explicit_redraws() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::with_reduced_motion(start, false);
        scheduler.presented(start);
        scheduler.set_tick_demand(TickDemand::Fast, start);
        assert_eq!(scheduler.deadline(), Some(start + SPINNER_INTERVAL));

        // This is the scheduler half of App's FocusLost -> TickDemand::None
        // contract. Explicit focus redraw still proceeds independently.
        scheduler.set_tick_demand(TickDemand::None, start + Duration::from_millis(1));
        assert_eq!(scheduler.deadline(), None);
        scheduler.request(RedrawReason::Focus, start + Duration::from_millis(1));
        assert!(scheduler.should_present(start + DEFAULT_MIN_DRAW_INTERVAL));
    }

    #[test]
    fn resize_is_debounced_from_the_latest_event() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.presented(start);
        scheduler.request_resize(start + Duration::from_millis(1));
        scheduler.request_resize(start + Duration::from_millis(20));
        assert!(!scheduler.should_present(start + Duration::from_millis(94)));
        assert_eq!(
            scheduler.deadline(),
            Some(start + Duration::from_millis(95))
        );
        assert!(!scheduler.resize_due(start + Duration::from_millis(94)));
        assert!(scheduler.resize_due(start + Duration::from_millis(95)));
        assert!(scheduler.should_present(start + Duration::from_millis(95)));
    }

    #[test]
    fn idle_does_not_call_draw_and_bursts_call_it_once() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.presented(start);
        let mut draws = 0;

        assert!(
            !scheduler
                .try_present(start + Duration::from_secs(1), || {
                    draws += 1;
                    Ok::<_, ()>(true)
                })
                .unwrap()
        );
        assert_eq!(draws, 0);

        for offset in 1..=20 {
            scheduler.request(RedrawReason::Stream, start + Duration::from_millis(offset));
        }
        assert!(
            scheduler
                .try_present(start + DEFAULT_MIN_DRAW_INTERVAL, || {
                    draws += 1;
                    Ok::<_, ()>(true)
                })
                .unwrap()
        );
        assert_eq!(draws, 1);
        assert_eq!(scheduler.stats().presented(), 2);
        assert_eq!(scheduler.stats().coalesced(), 19);
    }

    #[test]
    fn rejected_submission_keeps_the_frame_pending() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        assert!(!scheduler.try_present(start, || Ok::<_, ()>(false)).unwrap());
        assert_eq!(scheduler.stats().presented(), 0);
        assert_eq!(
            scheduler.deadline(),
            Some(start + DEFAULT_MIN_DRAW_INTERVAL)
        );
    }

    #[test]
    fn latency_histogram_reports_p95() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.presented(start + Duration::from_millis(7));
        scheduler.record_render_duration(Duration::from_millis(3));
        assert_eq!(scheduler.stats().p95_latency(), Duration::from_millis(7));
        assert_eq!(
            scheduler.stats().debug_report()["render_schedule_p95_us"],
            7_000
        );
        assert_eq!(scheduler.stats().debug_report()["render_p95_us"], 3_000);
    }

    #[test]
    fn feedback_markers_coalesce_deduplicate_and_settle_only_after_submission() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.presented(start);
        scheduler.request_feedback(FeedbackMarker::new(
            FeedbackPhase::FirstToken,
            "run_1",
            start + Duration::from_millis(1),
        ));
        scheduler.request_feedback(FeedbackMarker::new(
            FeedbackPhase::ToolStart,
            "call_1",
            start + Duration::from_millis(2),
        ));
        scheduler.request_feedback(FeedbackMarker::new(
            FeedbackPhase::FirstToken,
            "run_1",
            start + Duration::from_millis(3),
        ));

        let deferred = start + DEFAULT_MIN_DRAW_INTERVAL;
        assert!(
            !scheduler
                .try_present(deferred, || Ok::<_, ()>(false))
                .unwrap()
        );
        assert_eq!(
            scheduler.stats().feedback_count(FeedbackPhase::FirstToken),
            0
        );
        assert_eq!(
            scheduler.stats().feedback_count(FeedbackPhase::ToolStart),
            0
        );

        let submitted = deferred + DEFAULT_MIN_DRAW_INTERVAL;
        assert!(
            scheduler
                .try_present(submitted, || Ok::<_, ()>(true))
                .unwrap()
        );
        assert_eq!(
            scheduler.stats().feedback_count(FeedbackPhase::FirstToken),
            1
        );
        assert_eq!(
            scheduler.stats().feedback_count(FeedbackPhase::ToolStart),
            1
        );
        assert_eq!(
            scheduler.stats().feedback_p95(FeedbackPhase::FirstToken),
            Duration::from_millis(31)
        );
        assert_eq!(
            scheduler.stats().feedback_p95(FeedbackPhase::ToolStart),
            Duration::from_millis(30)
        );

        scheduler.request_feedback(FeedbackMarker::new(
            FeedbackPhase::FirstToken,
            "run_1",
            submitted + Duration::from_millis(1),
        ));
        assert_eq!(scheduler.deadline(), None);
    }

    #[test]
    fn feedback_report_exposes_all_budgets_and_deterministic_violations() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.request_feedback(FeedbackMarker::new(
            FeedbackPhase::ToolStart,
            "call_slow",
            start,
        ));
        scheduler.presented(start + Duration::from_millis(81));

        assert_eq!(
            scheduler.stats().feedback_budget_violations(),
            vec![FeedbackPhase::ToolStart]
        );
        let report = scheduler.stats().debug_report();
        for phase in FeedbackPhase::ALL {
            assert!(report["feedback_latency"].get(phase.as_str()).is_some());
        }
        assert_eq!(report["feedback_latency"]["tool_start"]["count"], 1);
        assert_eq!(
            report["feedback_latency"]["tool_start"]["budget_us"],
            80_000
        );
        assert_eq!(
            report["feedback_latency"]["tool_start"]["over_budget"],
            true
        );
        assert_eq!(
            report["feedback_latency"]["completion"]["over_budget"],
            false
        );
    }

    #[test]
    fn feedback_samples_are_bounded_to_the_latest_window() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.presented(start);
        for index in 0..=FEEDBACK_SAMPLE_LIMIT {
            let received_at = start + Duration::from_millis(index as u64 + 1);
            scheduler.request_feedback(FeedbackMarker::new(
                FeedbackPhase::Completion,
                format!("run_{index}"),
                received_at,
            ));
            scheduler.presented(received_at + Duration::from_millis(1));
        }
        assert_eq!(
            scheduler.stats().feedback_count(FeedbackPhase::Completion),
            FEEDBACK_SAMPLE_LIMIT
        );
    }
}
