//! Frame quality hooks: changed-cell ratio and anchor stability (#5).

use std::collections::hash_map::DefaultHasher;
use std::hash::{Hash, Hasher};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AnchorEvent {
    Stable,
    ContentShift,
    InsertPush,
    LargeJump,
    Flicker,
    MassiveRearrange,
}

#[derive(Debug, Clone, Copy, Default)]
pub struct FrameCellStats {
    pub total_cells: u64,
    pub changed_cells: u64,
    pub render_ms: f64,
}

impl FrameCellStats {
    pub fn change_ratio(self) -> f64 {
        if self.total_cells == 0 {
            return 0.0;
        }
        self.changed_cells as f64 / self.total_cells as f64
    }

    pub fn debug_line(self) -> String {
        format!(
            "cells {}/{} ({:.1}%) render_ms={:.2}",
            self.changed_cells,
            self.total_cells,
            self.change_ratio() * 100.0,
            self.render_ms
        )
    }
}

/// Hash visible transcript rows and classify unexpected motion (#5).
#[derive(Debug, Clone, Default)]
pub struct AnchorStabilityRecorder {
    previous: Vec<u64>,
    previous_previous: Vec<u64>,
    events: Vec<AnchorEvent>,
    exclude_expected: bool,
}

impl AnchorStabilityRecorder {
    pub fn new() -> Self {
        Self {
            exclude_expected: true,
            ..Self::default()
        }
    }

    pub fn record_frame(&mut self, visible_lines: &[String], expected_motion: bool) -> AnchorEvent {
        let hashes: Vec<u64> = visible_lines.iter().map(|line| hash_line(line)).collect();
        let event = classify(&self.previous_previous, &self.previous, &hashes, expected_motion);
        if !(self.exclude_expected && expected_motion && matches!(event, AnchorEvent::InsertPush)) {
            self.events.push(event);
        } else {
            self.events.push(AnchorEvent::Stable);
        }
        self.previous_previous = std::mem::take(&mut self.previous);
        self.previous = hashes;
        *self.events.last().unwrap_or(&AnchorEvent::Stable)
    }

    pub fn events(&self) -> &[AnchorEvent] {
        &self.events
    }

    pub fn flicker_count(&self) -> usize {
        self.events
            .iter()
            .filter(|e| matches!(e, AnchorEvent::Flicker))
            .count()
    }

    pub fn reset(&mut self) {
        *self = Self::new();
    }
}

fn hash_line(line: &str) -> u64 {
    let mut hasher = DefaultHasher::new();
    line.hash(&mut hasher);
    hasher.finish()
}

fn classify(
    prev_prev: &[u64],
    prev: &[u64],
    current: &[u64],
    expected_motion: bool,
) -> AnchorEvent {
    if prev.is_empty() {
        return AnchorEvent::Stable;
    }
    if prev == current {
        return AnchorEvent::Stable;
    }
    // A-B-A flicker: current matches frame before previous.
    if !prev_prev.is_empty() && prev_prev == current && prev != current {
        return AnchorEvent::Flicker;
    }
    if expected_motion {
        // New content at the bottom is an expected insert.
        if current.len() >= prev.len()
            && current
                .iter()
                .rev()
                .skip(current.len().saturating_sub(prev.len()))
                .eq(prev.iter().rev().take(prev.len().min(current.len())))
        {
            return AnchorEvent::InsertPush;
        }
        return AnchorEvent::ContentShift;
    }
    let shared = prev.iter().filter(|h| current.contains(h)).count();
    let ratio = if prev.is_empty() {
        0.0
    } else {
        shared as f64 / prev.len() as f64
    };
    if ratio < 0.35 {
        AnchorEvent::MassiveRearrange
    } else if (current.len() as i64 - prev.len() as i64).unsigned_abs() as usize > prev.len() / 2 {
        AnchorEvent::LargeJump
    } else {
        AnchorEvent::ContentShift
    }
}

/// Estimate changed cells between two equal-size character grids.
pub fn count_changed_cells(before: &[String], after: &[String]) -> FrameCellStats {
    let rows = before.len().max(after.len());
    let cols = before
        .iter()
        .chain(after.iter())
        .map(|r| r.chars().count())
        .max()
        .unwrap_or(0);
    let total = (rows * cols) as u64;
    let mut changed = 0u64;
    for row in 0..rows {
        let a = before.get(row).map(String::as_str).unwrap_or("");
        let b = after.get(row).map(String::as_str).unwrap_or("");
        let mut a_chars = a.chars();
        let mut b_chars = b.chars();
        for _ in 0..cols {
            let ac = a_chars.next();
            let bc = b_chars.next();
            if ac != bc {
                changed += 1;
            }
        }
    }
    FrameCellStats {
        total_cells: total,
        changed_cells: changed,
        render_ms: 0.0,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stable_frames_report_stable() {
        let mut rec = AnchorStabilityRecorder::new();
        let lines = vec!["a".into(), "b".into()];
        assert_eq!(rec.record_frame(&lines, false), AnchorEvent::Stable);
        assert_eq!(rec.record_frame(&lines, false), AnchorEvent::Stable);
    }

    #[test]
    fn aba_sequence_is_flicker() {
        // Issue #5: detect isomorphic A-B-A flicker.
        let mut rec = AnchorStabilityRecorder::new();
        let a = vec!["A".into()];
        let b = vec!["B".into()];
        rec.record_frame(&a, false);
        rec.record_frame(&b, false);
        assert_eq!(rec.record_frame(&a, false), AnchorEvent::Flicker);
        assert_eq!(rec.flicker_count(), 1);
    }

    #[test]
    fn changed_cells_ratio_is_honest() {
        let before = vec!["abcd".into()];
        let after = vec!["abXd".into()];
        let stats = count_changed_cells(&before, &after);
        assert_eq!(stats.total_cells, 4);
        assert_eq!(stats.changed_cells, 1);
        assert!((stats.change_ratio() - 0.25).abs() < f64::EPSILON);
        assert!(stats.debug_line().contains("1/4"));
    }
}
