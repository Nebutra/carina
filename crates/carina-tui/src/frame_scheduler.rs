use std::collections::BTreeMap;
use std::time::{Duration, Instant};

pub const DEFAULT_MIN_DRAW_INTERVAL: Duration = Duration::from_millis(16);
pub const SLOW_TICK_INTERVAL: Duration = Duration::from_millis(83);
pub const RESIZE_DEBOUNCE: Duration = Duration::from_millis(75);
pub const SPINNER_DIVISOR: u64 = 4;
pub const MONITOR_PULSE_DIVISOR: u64 = 8;
pub const TITLE_SPINNER_DIVISOR: u64 = 8;

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

    pub fn debug_report(&self) -> serde_json::Value {
        let requested = self
            .requested
            .iter()
            .map(|(reason, count)| (format!("{reason:?}").to_ascii_lowercase(), *count))
            .collect::<BTreeMap<_, _>>();
        serde_json::json!({
            "requested": requested,
            "coalesced": self.coalesced,
            "presented": self.presented,
            "render_schedule_p95_us": self.p95_latency().as_micros(),
            "render_p95_us": percentile_95(&self.render_micros),
        })
    }
}

#[derive(Debug, Clone)]
pub struct FrameScheduler {
    min_draw_interval: Duration,
    dirty_since: Option<Instant>,
    scheduled_at: Option<Instant>,
    last_draw_at: Option<Instant>,
    resize_at: Option<Instant>,
    tick_demand: TickDemand,
    next_tick_at: Option<Instant>,
    tick_sequence: u64,
    stats: FrameStats,
}

impl FrameScheduler {
    pub fn new(now: Instant) -> Self {
        let mut scheduler = Self {
            min_draw_interval: min_draw_interval(),
            dirty_since: None,
            scheduled_at: None,
            last_draw_at: None,
            resize_at: None,
            tick_demand: TickDemand::None,
            next_tick_at: None,
            tick_sequence: 0,
            stats: FrameStats {
                requested: BTreeMap::new(),
                coalesced: 0,
                presented: 0,
                latency_micros: Vec::new(),
                render_micros: Vec::new(),
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
        if self.tick_demand == demand {
            return;
        }
        self.tick_demand = demand;
        self.next_tick_at = tick_interval(demand).map(|interval| now + interval);
    }

    pub fn advance_tick(&mut self, now: Instant) -> bool {
        let Some(interval) = tick_interval(self.tick_demand) else {
            self.next_tick_at = None;
            return false;
        };
        if self.next_tick_at.is_none_or(|deadline| deadline > now) {
            return false;
        }
        self.tick_sequence = self.tick_sequence.wrapping_add(1);
        self.next_tick_at = Some(now + interval);
        if self.spinner_tick() {
            self.request(RedrawReason::Animation, now);
        }
        true
    }

    pub fn spinner_tick(&self) -> bool {
        self.tick_sequence.is_multiple_of(SPINNER_DIVISOR)
    }

    pub fn monitor_tick(&self) -> bool {
        self.tick_sequence.is_multiple_of(MONITOR_PULSE_DIVISOR)
    }

    pub fn title_tick(&self) -> bool {
        self.tick_sequence.is_multiple_of(TITLE_SPINNER_DIVISOR)
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
        TickDemand::Fast => Some(DEFAULT_MIN_DRAW_INTERVAL),
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
        assert_eq!(scheduler.deadline(), Some(late + SLOW_TICK_INTERVAL));
    }

    #[test]
    fn animation_ticks_are_independent_and_divided_before_redraw() {
        let start = Instant::now();
        let mut scheduler = FrameScheduler::new(start);
        scheduler.presented(start);
        scheduler.set_tick_demand(TickDemand::Fast, start);

        for tick in 1..SPINNER_DIVISOR {
            let now = start + DEFAULT_MIN_DRAW_INTERVAL * tick as u32;
            assert!(scheduler.advance_tick(now));
            assert!(!scheduler.should_present(now));
        }
        let now = start + DEFAULT_MIN_DRAW_INTERVAL * SPINNER_DIVISOR as u32;
        assert!(scheduler.advance_tick(now));
        assert!(scheduler.should_present(now));
        assert_eq!(scheduler.stats().requested(RedrawReason::Animation), 1);
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
}
