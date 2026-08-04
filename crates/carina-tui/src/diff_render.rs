//! Unified-diff presentation with line numbers and word-level highlights (#19).

use unicode_segmentation::UnicodeSegmentation;

pub const DIFF_HARD_BYTE_LIMIT: usize = 2 << 20;
pub const DIFF_HARD_LINE_LIMIT: usize = 50_000;
pub const DIFF_PROGRESSIVE_LINE_LIMIT: usize = 500;
pub const DIFF_PROGRESSIVE_BYTE_LIMIT: usize = 32 << 10;
const DIFF_DISPLAY_LINE_BYTE_LIMIT: usize = 4 << 10;
const DIFF_WORD_PAIR_BYTE_LIMIT: usize = 8 << 10;
const DIFF_WORD_TOKEN_LIMIT: usize = 256;
const DIFF_WORD_PAIR_LIMIT: usize = 16;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DiffLineKind {
    Context,
    Add,
    Remove,
    Header,
    Hunk,
    Meta,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DiffSpan {
    pub text: String,
    pub emphasize: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DiffLine {
    pub kind: DiffLineKind,
    pub old_no: Option<u32>,
    pub new_no: Option<u32>,
    pub marker: char,
    pub spans: Vec<DiffSpan>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DiffRenderWindow {
    pub lines: Vec<DiffLine>,
    pub omitted_lines: usize,
    pub omitted_exact: bool,
    pub hard_capped: bool,
    pub total_bytes: usize,
}

/// Parse a unified diff into numbered, word-emphasized lines.
pub fn render_unified_diff(diff: &str) -> Vec<DiffLine> {
    render_unified_diff_window(diff, DIFF_HARD_LINE_LIMIT).lines
}

/// Parse only a bounded prefix. Budgeting happens before line collection and
/// word-level LCS so untrusted large diffs cannot make one frame unbounded.
pub fn render_unified_diff_window(diff: &str, line_limit: usize) -> DiffRenderWindow {
    render_unified_diff_window_bounded(diff, line_limit, DIFF_HARD_BYTE_LIMIT)
}

pub fn render_unified_diff_progressive(diff: &str) -> DiffRenderWindow {
    render_unified_diff_window_bounded(
        diff,
        DIFF_PROGRESSIVE_LINE_LIMIT,
        DIFF_PROGRESSIVE_BYTE_LIMIT,
    )
}

/// Preview line count for collapsed create-only file cards in the transcript.
pub const COLLAPSED_CREATE_PREVIEW_LINES: usize = 8;

/// True when the unified diff only adds content (new file / pure create).
pub fn is_create_only_diff(diff: &str) -> bool {
    let (adds, deletes) = unified_diff_add_delete_counts(diff);
    deletes == 0 && adds > 0
}

pub fn unified_diff_add_delete_counts(diff: &str) -> (usize, usize) {
    let mut adds = 0usize;
    let mut deletes = 0usize;
    for line in diff.lines() {
        if line.starts_with('+') && !line.starts_with("+++") {
            adds += 1;
        } else if line.starts_with('-') && !line.starts_with("---") {
            deletes += 1;
        }
    }
    (adds, deletes)
}

/// Best-effort path from `+++ b/...` (or `--- a/...` when new path is /dev/null).
pub fn primary_new_path(diff: &str) -> Option<String> {
    for line in diff.lines() {
        if let Some(rest) = line.strip_prefix("+++ ") {
            let path = rest
                .strip_prefix("b/")
                .or_else(|| rest.strip_prefix("b\\"))
                .unwrap_or(rest)
                .trim();
            if !path.is_empty() && path != "/dev/null" {
                return Some(path.to_owned());
            }
        }
    }
    for line in diff.lines() {
        if let Some(rest) = line.strip_prefix("--- ") {
            let path = rest
                .strip_prefix("a/")
                .or_else(|| rest.strip_prefix("a\\"))
                .unwrap_or(rest)
                .trim();
            if !path.is_empty() && path != "/dev/null" {
                return Some(path.to_owned());
            }
        }
    }
    None
}

/// Content lines for a create-only diff, without leading `+` markers.
pub fn create_file_preview_lines(diff: &str, max_lines: usize) -> Vec<String> {
    if max_lines == 0 {
        return Vec::new();
    }
    diff.lines()
        .filter(|line| line.starts_with('+') && !line.starts_with("+++"))
        .take(max_lines)
        .map(|line| line[1..].to_owned())
        .collect()
}

fn render_unified_diff_window_bounded(
    diff: &str,
    line_limit: usize,
    byte_limit: usize,
) -> DiffRenderWindow {
    if diff.is_empty() {
        return DiffRenderWindow {
            lines: Vec::new(),
            omitted_lines: 0,
            omitted_exact: true,
            hard_capped: false,
            total_bytes: 0,
        };
    }
    if line_limit == 0 {
        let line_count = diff.lines().count().max(1);
        return DiffRenderWindow {
            lines: Vec::new(),
            omitted_lines: line_count,
            omitted_exact: true,
            hard_capped: false,
            total_bytes: diff.len(),
        };
    }
    let (bounded, byte_capped) = bounded_source(diff, byte_limit);
    let visible_limit = line_limit.min(DIFF_HARD_LINE_LIMIT);
    let raw: Vec<&str> = bounded.split('\n').take(visible_limit).collect();
    let bounded_lines = bounded.split('\n').count();
    let line_capped = bounded_lines > DIFF_HARD_LINE_LIMIT;
    let visible = raw.len();
    let omitted_within_window = bounded_lines.saturating_sub(visible);
    let omitted_lines = omitted_within_window + usize::from(byte_capped);
    let lines = render_bounded_lines(&raw);
    DiffRenderWindow {
        lines,
        omitted_lines,
        omitted_exact: !byte_capped,
        hard_capped: byte_capped || line_capped,
        total_bytes: diff.len(),
    }
}

fn render_bounded_lines(raw: &[&str]) -> Vec<DiffLine> {
    let mut out = Vec::with_capacity(raw.len());
    let mut old_no: Option<u32> = None;
    let mut new_no: Option<u32> = None;
    let mut i = 0;
    let mut emphasized_pairs = 0;
    while i < raw.len() {
        let line = raw[i];
        if line.starts_with("@@") {
            let (o, n) = parse_hunk_header(line);
            old_no = o;
            new_no = n;
            out.push(DiffLine {
                kind: DiffLineKind::Hunk,
                old_no: None,
                new_no: None,
                marker: ' ',
                spans: vec![DiffSpan {
                    text: bounded_display_text(line),
                    emphasize: false,
                }],
            });
            i += 1;
            continue;
        }
        if line.starts_with("---") || line.starts_with("+++") || line.starts_with("diff ") {
            out.push(DiffLine {
                kind: DiffLineKind::Meta,
                old_no: None,
                new_no: None,
                marker: ' ',
                spans: vec![DiffSpan {
                    text: bounded_display_text(line),
                    emphasize: false,
                }],
            });
            i += 1;
            continue;
        }
        if line.starts_with('-') && !line.starts_with("---") {
            let remove_body = &line[1..];
            if let Some(add_line) = raw.get(i + 1).copied()
                && add_line.starts_with('+')
                && !add_line.starts_with("+++")
            {
                let add_body = &add_line[1..];
                let (remove_spans, add_spans) = if emphasized_pairs < DIFF_WORD_PAIR_LIMIT
                    && remove_body.len() + add_body.len() <= DIFF_WORD_PAIR_BYTE_LIMIT
                {
                    word_level_pair(remove_body, add_body)
                        .map(|(remove, add)| {
                            emphasized_pairs += 1;
                            (bound_spans(remove), bound_spans(add))
                        })
                        .unwrap_or_else(|| (plain_span(remove_body), plain_span(add_body)))
                } else {
                    (plain_span(remove_body), plain_span(add_body))
                };
                let current_old = old_no;
                let current_new = new_no;
                out.push(DiffLine {
                    kind: DiffLineKind::Remove,
                    old_no: current_old,
                    new_no: None,
                    marker: '-',
                    spans: remove_spans,
                });
                out.push(DiffLine {
                    kind: DiffLineKind::Add,
                    old_no: None,
                    new_no: current_new,
                    marker: '+',
                    spans: add_spans,
                });
                if let Some(n) = old_no.as_mut() {
                    *n += 1;
                }
                if let Some(n) = new_no.as_mut() {
                    *n += 1;
                }
                i += 2;
                continue;
            }
            out.push(DiffLine {
                kind: DiffLineKind::Remove,
                old_no,
                new_no: None,
                marker: '-',
                spans: vec![DiffSpan {
                    text: bounded_display_text(remove_body),
                    emphasize: false,
                }],
            });
            if let Some(n) = old_no.as_mut() {
                *n += 1;
            }
            i += 1;
            continue;
        }
        if line.starts_with('+') && !line.starts_with("+++") {
            out.push(DiffLine {
                kind: DiffLineKind::Add,
                old_no: None,
                new_no,
                marker: '+',
                spans: vec![DiffSpan {
                    text: bounded_display_text(&line[1..]),
                    emphasize: false,
                }],
            });
            if let Some(n) = new_no.as_mut() {
                *n += 1;
            }
            i += 1;
            continue;
        }
        // Context or header-ish residual.
        let kind = if line.is_empty() || line.starts_with(' ') {
            DiffLineKind::Context
        } else {
            DiffLineKind::Header
        };
        let body = line.strip_prefix(' ').unwrap_or(line);
        out.push(DiffLine {
            kind,
            old_no,
            new_no,
            marker: ' ',
            spans: vec![DiffSpan {
                text: bounded_display_text(body),
                emphasize: false,
            }],
        });
        if let Some(n) = old_no.as_mut() {
            *n += 1;
        }
        if let Some(n) = new_no.as_mut() {
            *n += 1;
        }
        i += 1;
    }
    out
}

fn plain_span(value: &str) -> Vec<DiffSpan> {
    vec![DiffSpan {
        text: bounded_display_text(value),
        emphasize: false,
    }]
}

pub(crate) fn bounded_display_text(value: &str) -> String {
    if value.len() <= DIFF_DISPLAY_LINE_BYTE_LIMIT {
        return value.to_owned();
    }
    let end = floor_char_boundary(value, DIFF_DISPLAY_LINE_BYTE_LIMIT);
    format!("{} ...", &value[..end])
}

pub(crate) fn bounded_diff_source(diff: &str) -> (&str, bool) {
    bounded_source(diff, DIFF_HARD_BYTE_LIMIT)
}

fn bounded_source(diff: &str, byte_limit: usize) -> (&str, bool) {
    let byte_end = floor_char_boundary(diff, diff.len().min(byte_limit));
    (&diff[..byte_end], byte_end < diff.len())
}

fn bound_spans(spans: Vec<DiffSpan>) -> Vec<DiffSpan> {
    let mut remaining = DIFF_DISPLAY_LINE_BYTE_LIMIT;
    let mut bounded = Vec::new();
    for mut span in spans {
        if span.text.len() <= remaining {
            remaining -= span.text.len();
            bounded.push(span);
            continue;
        }
        let end = floor_char_boundary(&span.text, remaining);
        span.text.truncate(end);
        if !span.text.is_empty() {
            bounded.push(span);
        }
        bounded.push(DiffSpan {
            text: " ...".to_owned(),
            emphasize: false,
        });
        return bounded;
    }
    bounded
}

fn floor_char_boundary(value: &str, mut end: usize) -> usize {
    while end > 0 && !value.is_char_boundary(end) {
        end -= 1;
    }
    end
}

/// Format a single rendered line as plain text: `NNN|MMM ± body`.
pub fn format_line_plain(line: &DiffLine) -> String {
    let old = line
        .old_no
        .map(|n| format!("{n:>4}"))
        .unwrap_or_else(|| "    ".into());
    let new = line
        .new_no
        .map(|n| format!("{n:>4}"))
        .unwrap_or_else(|| "    ".into());
    let body: String = line.spans.iter().map(|s| s.text.as_str()).collect();
    if matches!(line.kind, DiffLineKind::Meta | DiffLineKind::Hunk) {
        return body;
    }
    format!("{old}|{new} {}{body}", line.marker)
}

fn parse_hunk_header(line: &str) -> (Option<u32>, Option<u32>) {
    // @@ -12,3 +14,4 @@
    let mut old = None;
    let mut new = None;
    for part in line.split_whitespace() {
        if let Some(rest) = part.strip_prefix('-') {
            old = rest.split(',').next().and_then(|s| s.parse::<u32>().ok());
        } else if let Some(rest) = part.strip_prefix('+') {
            new = rest.split(',').next().and_then(|s| s.parse::<u32>().ok());
        }
    }
    (old, new)
}

fn word_level_pair(old: &str, new: &str) -> Option<(Vec<DiffSpan>, Vec<DiffSpan>)> {
    let old_words: Vec<&str> = old
        .split_word_bounds()
        .take(DIFF_WORD_TOKEN_LIMIT + 1)
        .collect();
    let new_words: Vec<&str> = new
        .split_word_bounds()
        .take(DIFF_WORD_TOKEN_LIMIT + 1)
        .collect();
    if old_words.len() > DIFF_WORD_TOKEN_LIMIT || new_words.len() > DIFF_WORD_TOKEN_LIMIT {
        return None;
    }
    let lcs = lcs_table(&old_words, &new_words);
    let mut i = old_words.len();
    let mut j = new_words.len();
    let mut remove_stack = Vec::new();
    let mut add_stack = Vec::new();
    while i > 0 || j > 0 {
        if i > 0 && j > 0 && old_words[i - 1] == new_words[j - 1] {
            remove_stack.push(DiffSpan {
                text: old_words[i - 1].to_owned(),
                emphasize: false,
            });
            add_stack.push(DiffSpan {
                text: new_words[j - 1].to_owned(),
                emphasize: false,
            });
            i -= 1;
            j -= 1;
        } else if j > 0 && (i == 0 || lcs[i][j - 1] >= lcs[i - 1][j]) {
            add_stack.push(DiffSpan {
                text: new_words[j - 1].to_owned(),
                emphasize: true,
            });
            j -= 1;
        } else if i > 0 {
            remove_stack.push(DiffSpan {
                text: old_words[i - 1].to_owned(),
                emphasize: true,
            });
            i -= 1;
        }
    }
    remove_stack.reverse();
    add_stack.reverse();
    Some((coalesce_spans(remove_stack), coalesce_spans(add_stack)))
}

fn lcs_table(a: &[&str], b: &[&str]) -> Vec<Vec<usize>> {
    let mut table = vec![vec![0; b.len() + 1]; a.len() + 1];
    for i in 1..=a.len() {
        for j in 1..=b.len() {
            table[i][j] = if a[i - 1] == b[j - 1] {
                table[i - 1][j - 1] + 1
            } else {
                table[i - 1][j].max(table[i][j - 1])
            };
        }
    }
    table
}

fn coalesce_spans(spans: Vec<DiffSpan>) -> Vec<DiffSpan> {
    let mut out: Vec<DiffSpan> = Vec::new();
    for span in spans {
        if let Some(last) = out.last_mut()
            && last.emphasize == span.emphasize
        {
            last.text.push_str(&span.text);
        } else {
            out.push(span);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn line_numbers_follow_hunk_header() {
        // Issue #19: numbered unified diff.
        let diff = "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n context\n-old\n+new\n";
        let lines = render_unified_diff(diff);
        let remove = lines
            .iter()
            .find(|l| l.kind == DiffLineKind::Remove)
            .expect("remove");
        let add = lines
            .iter()
            .find(|l| l.kind == DiffLineKind::Add)
            .expect("add");
        assert_eq!(remove.old_no, Some(2));
        assert_eq!(add.new_no, Some(2));
        assert!(format_line_plain(remove).contains("2|"));
        assert!(format_line_plain(add).contains("|   2"));
    }

    #[test]
    fn word_level_marks_changed_token() {
        let diff = "@@ -1 +1 @@\n-hello world\n+hello carina\n";
        let lines = render_unified_diff(diff);
        let remove = lines
            .iter()
            .find(|l| l.kind == DiffLineKind::Remove)
            .unwrap();
        let add = lines.iter().find(|l| l.kind == DiffLineKind::Add).unwrap();
        assert!(
            remove
                .spans
                .iter()
                .any(|s| s.emphasize && s.text.contains("world"))
        );
        assert!(
            add.spans
                .iter()
                .any(|s| s.emphasize && s.text.contains("carina"))
        );
        assert!(
            remove
                .spans
                .iter()
                .any(|s| !s.emphasize && s.text.contains("hello"))
        );
    }

    #[test]
    fn empty_diff_is_empty() {
        assert!(render_unified_diff("").is_empty());
    }

    #[test]
    fn progressive_window_never_parses_the_complete_diff_first() {
        let diff = (0..10_000)
            .map(|index| format!("+line {index}"))
            .collect::<Vec<_>>()
            .join("\n");
        let window = render_unified_diff_window(&diff, 12);
        assert_eq!(window.lines.len(), 12);
        assert_eq!(window.omitted_lines, 9_988);
        assert!(window.omitted_exact);
        assert!(!window.hard_capped);
    }

    #[test]
    fn hard_line_cap_bounds_allocation_before_rendering() {
        let diff = std::iter::repeat_n("+x", DIFF_HARD_LINE_LIMIT + 100)
            .collect::<Vec<_>>()
            .join("\n");
        let window = render_unified_diff_window(&diff, usize::MAX);
        assert_eq!(window.lines.len(), DIFF_HARD_LINE_LIMIT);
        assert_eq!(window.omitted_lines, 100);
        assert!(window.hard_capped);
    }

    #[test]
    fn byte_and_display_line_caps_preserve_utf8_boundaries() {
        let diff = format!("+{}", "界".repeat(DIFF_HARD_BYTE_LIMIT));
        let window = render_unified_diff_window(&diff, 1);
        assert_eq!(window.lines.len(), 1);
        assert!(window.hard_capped);
        assert!(!window.omitted_exact);
        let rendered = window.lines[0]
            .spans
            .iter()
            .map(|span| span.text.as_str())
            .collect::<String>();
        assert!(rendered.is_char_boundary(rendered.len()));
        assert!(rendered.len() <= DIFF_DISPLAY_LINE_BYTE_LIMIT + " ...".len());
        assert!(!rendered.contains('\u{fffd}'));
    }

    #[test]
    fn pathological_word_pairs_skip_quadratic_lcs_but_keep_polarity() {
        let old = "a ".repeat(DIFF_WORD_TOKEN_LIMIT + 50);
        let new = "b ".repeat(DIFF_WORD_TOKEN_LIMIT + 50);
        let diff = format!("-{old}\n+{new}");
        let window = render_unified_diff_window(&diff, 2);
        assert_eq!(window.lines[0].kind, DiffLineKind::Remove);
        assert_eq!(window.lines[1].kind, DiffLineKind::Add);
        assert!(
            window
                .lines
                .iter()
                .all(|line| line.spans.iter().all(|span| !span.emphasize))
        );
    }

    #[test]
    fn paired_word_highlight_cannot_bypass_display_line_cap() {
        let old = format!("{} tail", "a ".repeat(2_000));
        let new = "b";
        let diff = format!("-{old}\n+{new}");
        let window = render_unified_diff_window(&diff, 2);
        let rendered = window.lines[0]
            .spans
            .iter()
            .map(|span| span.text.as_str())
            .collect::<String>();
        assert!(rendered.len() <= DIFF_DISPLAY_LINE_BYTE_LIMIT + " ...".len());
    }

    #[test]
    fn progressive_window_bounds_total_source_and_lcs_pairs() {
        let pair = format!("-{}\n+{}", "old ".repeat(200), "new ".repeat(200));
        let diff = std::iter::repeat_n(pair, DIFF_PROGRESSIVE_LINE_LIMIT)
            .collect::<Vec<_>>()
            .join("\n");
        let window = render_unified_diff_progressive(&diff);
        let rendered_bytes = window
            .lines
            .iter()
            .flat_map(|line| line.spans.iter())
            .map(|span| span.text.len())
            .sum::<usize>();
        let emphasized_lines = window
            .lines
            .iter()
            .filter(|line| line.spans.iter().any(|span| span.emphasize))
            .count();
        assert!(rendered_bytes <= DIFF_PROGRESSIVE_BYTE_LIMIT + 2 * " ...".len());
        assert!(emphasized_lines <= DIFF_WORD_PAIR_LIMIT * 2);
        assert!(window.hard_capped);
    }
}
