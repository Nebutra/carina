//! Unified-diff presentation with line numbers and word-level highlights (#19).

use unicode_segmentation::UnicodeSegmentation;

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

/// Parse a unified diff into numbered, word-emphasized lines.
pub fn render_unified_diff(diff: &str) -> Vec<DiffLine> {
    if diff.is_empty() {
        return Vec::new();
    }
    let raw: Vec<&str> = diff.split('\n').collect();
    let mut out = Vec::with_capacity(raw.len());
    let mut old_no: Option<u32> = None;
    let mut new_no: Option<u32> = None;
    let mut i = 0;
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
                    text: line.to_owned(),
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
                    text: line.to_owned(),
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
                let (remove_spans, add_spans) = word_level_pair(remove_body, add_body);
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
                    text: remove_body.to_owned(),
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
                    text: line[1..].to_owned(),
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
                text: body.to_owned(),
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
            old = rest
                .split(',')
                .next()
                .and_then(|s| s.parse::<u32>().ok());
        } else if let Some(rest) = part.strip_prefix('+') {
            new = rest
                .split(',')
                .next()
                .and_then(|s| s.parse::<u32>().ok());
        }
    }
    (old, new)
}

fn word_level_pair(old: &str, new: &str) -> (Vec<DiffSpan>, Vec<DiffSpan>) {
    let old_words: Vec<&str> = old.split_word_bounds().collect();
    let new_words: Vec<&str> = new.split_word_bounds().collect();
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
    (
        coalesce_spans(remove_stack),
        coalesce_spans(add_stack),
    )
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
}
