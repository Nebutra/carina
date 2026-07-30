//! Agent / TUI benchmark harness surfaces (#4).
//!
//! Produces capturable resource, startup, and render metrics for at least one
//! scenario. Full competitor matrices live behind `make bench`.

use std::time::{Duration, Instant};

use crate::frame_scheduler::{FrameScheduler, RedrawReason};
use crate::markdown::{self, MarkdownTheme};
use crate::syntax;
use crate::theme::{ColorLevel, Polarity, Theme};
use ratatui::style::Style;

#[derive(Debug, Clone, PartialEq)]
pub struct BenchScenarioResult {
    pub name: String,
    pub render_p50_ms: f64,
    pub render_p95_ms: f64,
    pub render_p99_ms: f64,
    pub frames: u64,
    pub coalesced: u64,
    pub highlight_tokens: usize,
    pub notes: String,
}

/// Run an offscreen render scenario over a synthetic transcript (#4).
pub fn run_render_scenario(iterations: usize) -> BenchScenarioResult {
    let theme = Theme::new(Polarity::Dark, ColorLevel::TrueColor);
    let md_theme = MarkdownTheme {
        glyphs: crate::glyphs::Glyphs::detect(),
        text: Style::default().fg(theme.text),
        accent: Style::default().fg(theme.accent),
        muted: Style::default().fg(theme.muted),
        code: Style::default().fg(theme.gray_bright),
        quote: Style::default().fg(theme.muted),
        link: Style::default().fg(theme.accent),
        headings: [
            Style::default().fg(theme.accent),
            Style::default().fg(theme.accent),
            Style::default().fg(theme.accent),
            Style::default().fg(theme.accent),
            Style::default().fg(theme.accent),
            Style::default().fg(theme.accent),
        ],
        code_keyword: Style::default().fg(theme.accent),
        code_string: Style::default().fg(theme.gray_bright),
        code_comment: Style::default().fg(theme.muted),
        code_number: Style::default().fg(theme.gray_bright),
        code_type: Style::default().fg(theme.accent),
    };
    let body = SAMPLE_TRANSCRIPT;
    let mut samples = Vec::with_capacity(iterations);
    let mut tokens = 0usize;
    for _ in 0..iterations.max(1) {
        let start = Instant::now();
        let _lines = markdown::render(body, 100, md_theme);
        tokens = syntax::highlight("rust", SAMPLE_CODE).len();
        samples.push(start.elapsed());
    }
    samples.sort();
    let mut scheduler = FrameScheduler::new(Instant::now());
    for i in 0..iterations.max(1) {
        scheduler.request(RedrawReason::Stream, Instant::now());
        if i % 3 == 0 {
            let _ = scheduler.try_present(Instant::now(), || Ok::<bool, ()>(true));
        }
    }
    BenchScenarioResult {
        name: "synthetic_transcript_render".into(),
        render_p50_ms: percentile_ms(&samples, 50),
        render_p95_ms: percentile_ms(&samples, 95),
        render_p99_ms: percentile_ms(&samples, 99),
        frames: scheduler.stats().presented(),
        coalesced: scheduler.stats().coalesced(),
        highlight_tokens: tokens,
        notes: "offscreen; not a PTY first-frame measurement".into(),
    }
}

pub fn format_result_jsonl(result: &BenchScenarioResult) -> String {
    format!(
        r#"{{"name":"{}","render_p50_ms":{:.3},"render_p95_ms":{:.3},"render_p99_ms":{:.3},"frames":{},"coalesced":{},"highlight_tokens":{},"notes":"{}"}}"#,
        result.name,
        result.render_p50_ms,
        result.render_p95_ms,
        result.render_p99_ms,
        result.frames,
        result.coalesced,
        result.highlight_tokens,
        result.notes
    )
}

fn percentile_ms(samples: &[Duration], pct: usize) -> f64 {
    if samples.is_empty() {
        return 0.0;
    }
    let index = ((samples.len() * pct).div_ceil(100)).saturating_sub(1);
    samples[index.min(samples.len() - 1)].as_secs_f64() * 1000.0
}

const SAMPLE_CODE: &str = r#"
fn main() {
    let answer = 42;
    println!("hello {}", answer);
}
"#;

const SAMPLE_TRANSCRIPT: &str = r#"
# Assistant reply

Here is a plan:

1. Open the file
2. Apply the patch

```rust
fn main() {
    let answer = 42;
    println!("hello {}", answer);
}
```

And a short note about **trade-offs**.
"#;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn render_scenario_produces_nonzero_metrics() {
        // Issue #4: harness must produce capturable render metrics.
        let result = run_render_scenario(8);
        assert_eq!(result.name, "synthetic_transcript_render");
        assert!(result.highlight_tokens > 0);
        assert!(result.render_p95_ms >= result.render_p50_ms);
        let line = format_result_jsonl(&result);
        assert!(line.contains("render_p95_ms"));
        assert!(line.contains("synthetic_transcript_render"));
    }
}
