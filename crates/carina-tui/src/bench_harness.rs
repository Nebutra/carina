//! Agent / TUI benchmark harness surfaces (#4).
//!
//! Produces capturable resource, startup, and render metrics for at least one
//! scenario. Full competitor matrices live behind `make bench`.

use std::time::{Duration, Instant};

use crate::conversation::render_startup_conversation;
use crate::frame_scheduler::{FrameScheduler, RedrawReason};
use crate::i18n::Locale;
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
    pub ttff_ms: Option<f64>,
    pub ttfi_ms: Option<f64>,
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
        ttff_ms: None,
        ttfi_ms: None,
        notes: "offscreen; not a PTY first-frame measurement".into(),
    }
}

/// Paint conversation chrome on a PTY and report TTFF (first byte) / TTFI
/// (composer on screen). This is the product first-frame number, not markdown
/// offscreen time.
pub fn run_pty_first_frame() -> BenchScenarioResult {
    #[cfg(unix)]
    {
        match paint_pty_first_frame() {
            Ok(result) => result,
            Err(error) => BenchScenarioResult {
                name: "pty_first_frame".into(),
                render_p50_ms: 0.0,
                render_p95_ms: 0.0,
                render_p99_ms: 0.0,
                frames: 0,
                coalesced: 0,
                highlight_tokens: 0,
                ttff_ms: None,
                ttfi_ms: None,
                notes: format!("pty unavailable: {error}"),
            },
        }
    }
    #[cfg(not(unix))]
    {
        BenchScenarioResult {
            name: "pty_first_frame".into(),
            render_p50_ms: 0.0,
            render_p95_ms: 0.0,
            render_p99_ms: 0.0,
            frames: 0,
            coalesced: 0,
            highlight_tokens: 0,
            ttff_ms: None,
            ttfi_ms: None,
            notes: "pty first-frame requires unix".into(),
        }
    }
}

#[cfg(unix)]
struct PtyFrameStats {
    first_byte: Option<Instant>,
    total: usize,
}

#[cfg(unix)]
struct PtyFrameWriter {
    inner: std::fs::File,
    stats: std::sync::Arc<std::sync::Mutex<PtyFrameStats>>,
}

#[cfg(unix)]
impl std::io::Write for PtyFrameWriter {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        let n = self.inner.write(buf)?;
        if n > 0 {
            let mut stats = self.stats.lock().expect("pty frame stats");
            if stats.first_byte.is_none() {
                stats.first_byte = Some(Instant::now());
            }
            stats.total += n;
        }
        Ok(n)
    }

    fn flush(&mut self) -> std::io::Result<()> {
        self.inner.flush()
    }
}

#[cfg(unix)]
fn paint_pty_first_frame() -> std::io::Result<BenchScenarioResult> {
    use std::fs::File;
    use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
    use std::sync::{Arc, Mutex};

    use ratatui::Terminal;
    use ratatui::backend::CrosstermBackend;

    let mut master_fd: libc::c_int = 0;
    let mut slave_fd: libc::c_int = 0;
    let mut size = libc::winsize {
        ws_row: 24,
        ws_col: 80,
        ws_xpixel: 0,
        ws_ypixel: 0,
    };
    // SAFETY: pointers are to stack integers/winsize; openpty writes them.
    let rc = unsafe {
        libc::openpty(
            &mut master_fd,
            &mut slave_fd,
            std::ptr::null_mut(),
            std::ptr::null_mut(),
            &mut size,
        )
    };
    if rc != 0 {
        return Err(std::io::Error::last_os_error());
    }
    // SAFETY: openpty succeeded; these fds are exclusive to this function.
    let master = unsafe { OwnedFd::from_raw_fd(master_fd) };
    let slave = unsafe { OwnedFd::from_raw_fd(slave_fd) };
    unsafe {
        libc::ioctl(master.as_raw_fd(), libc::TIOCSWINSZ, &size);
        libc::ioctl(slave.as_raw_fd(), libc::TIOCSWINSZ, &size);
    }
    // Keep the master open so the slave stays a live terminal during draw.
    let _master = File::from(master);
    let stats = Arc::new(Mutex::new(PtyFrameStats {
        first_byte: None,
        total: 0,
    }));
    let writer = PtyFrameWriter {
        inner: File::from(slave),
        stats: Arc::clone(&stats),
    };
    let backend = CrosstermBackend::new(writer);
    let theme = Theme::carina(false);
    let start = Instant::now();
    let mut terminal = Terminal::new(backend)?;
    terminal.draw(|frame| {
        render_startup_conversation(frame, Locale::En, theme);
    })?;
    std::io::Write::flush(terminal.backend_mut())?;
    let ttfi = start.elapsed();
    drop(terminal);
    let stats = stats.lock().expect("pty frame stats");
    let Some(first_byte) = stats.first_byte else {
        return Err(std::io::Error::other("pty first frame produced no bytes"));
    };
    if stats.total == 0 {
        return Err(std::io::Error::other("pty first frame produced no bytes"));
    }
    let ttff_ms = first_byte.saturating_duration_since(start).as_secs_f64() * 1000.0;
    let ttfi_ms = ttfi.as_secs_f64() * 1000.0;
    Ok(BenchScenarioResult {
        name: "pty_first_frame".into(),
        render_p50_ms: ttff_ms,
        render_p95_ms: ttfi_ms,
        render_p99_ms: ttfi_ms,
        frames: 1,
        coalesced: 0,
        highlight_tokens: 0,
        ttff_ms: Some(ttff_ms),
        ttfi_ms: Some(ttfi_ms.max(ttff_ms)),
        notes: "pty; first conversation chrome before inventory".into(),
    })
}

pub fn format_result_jsonl(result: &BenchScenarioResult) -> String {
    let ttff = result
        .ttff_ms
        .map(|value| format!(r#","ttff_ms":{value:.3}"#))
        .unwrap_or_default();
    let ttfi = result
        .ttfi_ms
        .map(|value| format!(r#","ttfi_ms":{value:.3}"#))
        .unwrap_or_default();
    format!(
        r#"{{"name":"{}","render_p50_ms":{:.3},"render_p95_ms":{:.3},"render_p99_ms":{:.3},"frames":{},"coalesced":{},"highlight_tokens":{}{ttff}{ttfi},"notes":"{}"}}"#,
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

    #[test]
    fn pty_first_frame_reports_ttff() {
        let result = run_pty_first_frame();
        assert_eq!(result.name, "pty_first_frame");
        let line = format_result_jsonl(&result);
        #[cfg(not(unix))]
        {
            assert!(line.contains("requires unix"), "{line}");
            return;
        }
        let ttff = result
            .ttff_ms
            .unwrap_or_else(|| panic!("expected PTY ttff, got {line}"));
        let ttfi = result.ttfi_ms.expect("ttfi");
        assert_eq!(result.frames, 1, "{line}");
        assert!(ttfi >= ttff, "{line}");
        assert!(
            ttfi < 1_000.0,
            "PTY first frame should be a paint, not a hang: {line}"
        );
        assert!(line.contains("ttff_ms"));
        assert!(line.contains("pty_first_frame"));
    }
}
