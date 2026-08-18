use std::ops::Range;

use ratatui::text::Line;
use xai_ratatui_textarea::TextArea;

use crate::context_completion::{file_chip, FILE_ELEMENT_KIND};

pub const MAX_PREVIEW_BYTES: usize = 1024 * 1024;
pub const MAX_FILE_CHIP_EXCERPT_CHARS: usize = 4000;
const MAX_FILE_CHIP_EXCERPT_LINES: usize = 80;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FileRangeError {
    Invalid,
    OutOfRange { lines: usize },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FileViewerOrigin {
    Completion { range: Range<usize> },
    Element { range: Range<usize> },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FileViewerLoad {
    Loading,
    Ready,
    Failed(String),
}

#[derive(Debug, Clone)]
pub struct FileViewer {
    pub session_id: String,
    pub path: String,
    pub origin: FileViewerOrigin,
    pub generation: u64,
    pub load: FileViewerLoad,
    pub lines: Vec<String>,
    pub cursor: usize,
    pub anchor: Option<usize>,
    pub scroll: usize,
    pub search: Option<String>,
    pub hash: String,
}

impl FileViewer {
    pub fn loading(
        session_id: String,
        path: String,
        origin: FileViewerOrigin,
        generation: u64,
        initial_range: Option<Range<usize>>,
    ) -> Self {
        let cursor = initial_range
            .as_ref()
            .map(|range| range.start.saturating_sub(1))
            .unwrap_or(0);
        let anchor = initial_range
            .filter(|range| range.end > range.start + 1)
            .map(|range| range.end.saturating_sub(2));
        Self {
            session_id,
            path,
            origin,
            generation,
            load: FileViewerLoad::Loading,
            lines: Vec::new(),
            cursor,
            anchor,
            scroll: 0,
            search: None,
            hash: String::new(),
        }
    }

    pub fn apply_content(&mut self, content: String, hash: String) {
        self.lines = if content.is_empty() {
            vec![String::new()]
        } else {
            content.lines().map(str::to_owned).collect()
        };
        self.cursor = self.cursor.min(self.lines.len().saturating_sub(1));
        self.anchor = self
            .anchor
            .map(|anchor| anchor.min(self.lines.len().saturating_sub(1)));
        self.hash = hash;
        self.load = FileViewerLoad::Ready;
    }

    pub fn fail(&mut self, error: String) {
        self.load = FileViewerLoad::Failed(error);
    }

    pub fn move_cursor(&mut self, delta: isize, visible_rows: usize) {
        if self.lines.is_empty() {
            return;
        }
        self.cursor = self
            .cursor
            .saturating_add_signed(delta)
            .min(self.lines.len().saturating_sub(1));
        self.ensure_visible(visible_rows);
    }

    pub fn page(&mut self, delta: isize, visible_rows: usize) {
        self.move_cursor(
            delta * visible_rows.saturating_sub(1).max(1) as isize,
            visible_rows,
        );
    }

    pub fn select_line(&mut self, line: usize, visible_rows: usize) {
        if line < self.lines.len() {
            self.cursor = line;
            self.ensure_visible(visible_rows);
        }
    }

    pub fn toggle_range(&mut self) {
        self.anchor = self.anchor.map_or(Some(self.cursor), |_| None);
    }

    pub fn selected_range(&self) -> Range<usize> {
        let anchor = self.anchor.unwrap_or(self.cursor);
        let start = anchor.min(self.cursor) + 1;
        let end = anchor.max(self.cursor) + 2;
        start..end
    }

    pub fn selected_label(&self) -> String {
        let range = self.selected_range();
        if range.end == range.start + 1 {
            range.start.to_string()
        } else {
            format!("{}-{}", range.start, range.end - 1)
        }
    }

    pub fn begin_search(&mut self) {
        self.search = Some(String::new());
    }

    pub fn search_next(&mut self, backwards: bool, visible_rows: usize) -> bool {
        let Some(query) = self.search.as_ref().filter(|query| !query.is_empty()) else {
            return false;
        };
        let query = query.to_lowercase();
        let count = self.lines.len();
        for distance in 1..=count {
            let index = if backwards {
                (self.cursor + count - distance % count) % count
            } else {
                (self.cursor + distance) % count
            };
            if self.lines[index].to_lowercase().contains(&query) {
                self.cursor = index;
                self.ensure_visible(visible_rows);
                return true;
            }
        }
        false
    }

    pub fn confirm(self, textarea: &mut TextArea) {
        let selected = self.selected_range();
        let content = self.lines.join("\n");
        let backing = file_chip_backing(&self.path, selected.clone(), &content)
            .unwrap_or_else(|_| format!("@{}:{}", self.path, self.selected_label()));
        let display = file_chip(&self.path, Some(selected));
        let (target, trailing_space) = match self.origin {
            FileViewerOrigin::Completion { range } => (range, true),
            FileViewerOrigin::Element { range } => (range, false),
        };
        textarea.begin_undo_group();
        textarea.replace_range_with_element(target, &backing, FILE_ELEMENT_KIND, Some(display));
        if trailing_space {
            textarea.insert_str(" ");
        }
        textarea.end_undo_group();
    }

    pub fn selected_line(&self, index: usize) -> bool {
        self.selected_range().contains(&(index + 1))
    }

    fn ensure_visible(&mut self, visible_rows: usize) {
        let visible_rows = visible_rows.max(1);
        if self.cursor < self.scroll {
            self.scroll = self.cursor;
        } else if self.cursor >= self.scroll + visible_rows {
            self.scroll = self.cursor + 1 - visible_rows;
        }
    }
}

pub fn parse_file_reference(backing: &str) -> Option<(String, Option<Range<usize>>)> {
    let head = backing.lines().next().unwrap_or(backing);
    let body = head.strip_prefix('@')?;
    match split_at_query(body) {
        Ok((path, range)) if !path.is_empty() => Some((path, range)),
        _ => None,
    }
}

pub fn split_at_query(query: &str) -> Result<(String, Option<Range<usize>>), FileRangeError> {
    match query.rsplit_once(':') {
        Some((path, suffix)) if is_line_range_suffix(suffix) => {
            let range = parse_line_range(suffix).ok_or(FileRangeError::Invalid)?;
            if path.is_empty() {
                return Err(FileRangeError::Invalid);
            }
            Ok((path.to_owned(), Some(range)))
        }
        _ => Ok((query.to_owned(), None)),
    }
}

fn is_line_range_suffix(suffix: &str) -> bool {
    !suffix.is_empty()
        && suffix
            .chars()
            .all(|character| character.is_ascii_digit() || character == '-')
}

pub fn file_chip_backing(
    path: &str,
    lines: Range<usize>,
    content: &str,
) -> Result<String, FileRangeError> {
    let all: Vec<&str> = if content.is_empty() {
        Vec::new()
    } else {
        content.lines().collect()
    };
    if lines.start == 0 || lines.end <= lines.start || lines.start > all.len() {
        return Err(FileRangeError::OutOfRange { lines: all.len() });
    }
    if lines.end - 1 > all.len() {
        return Err(FileRangeError::OutOfRange { lines: all.len() });
    }
    let selected = &all[lines.start - 1..lines.end - 1];
    let label = if lines.end == lines.start + 1 {
        lines.start.to_string()
    } else {
        format!("{}-{}", lines.start, lines.end - 1)
    };
    let numbered: Vec<String> = selected
        .iter()
        .enumerate()
        .map(|(index, line)| format!("{}| {}", lines.start + index, clip_excerpt_line(line)))
        .collect();
    let mut backing = format!("@{path}:{label}\n");
    if numbered.len() <= MAX_FILE_CHIP_EXCERPT_LINES
        && backing.len() + numbered.iter().map(String::len).sum::<usize>() + numbered.len()
            <= MAX_FILE_CHIP_EXCERPT_CHARS
    {
        backing.push_str(&numbered.join("\n"));
        return Ok(backing);
    }
    let head = numbered.len().min(40).max(1).min(numbered.len());
    let tail = numbered.len().saturating_sub(head).min(20);
    let omitted = numbered.len().saturating_sub(head + tail);
    backing.push_str(&numbered[..head].join("\n"));
    if omitted > 0 {
        backing.push_str(&format!("\n... {omitted} lines omitted ..."));
    }
    if tail > 0 {
        backing.push('\n');
        backing.push_str(&numbered[numbered.len() - tail..].join("\n"));
    }
    Ok(backing)
}

fn clip_excerpt_line(line: &str) -> String {
    let mut chars = line.chars();
    let clipped: String = chars.by_ref().take(200).collect();
    if chars.next().is_some() {
        format!("{clipped}…")
    } else {
        clipped
    }
}

fn parse_line_range(value: &str) -> Option<Range<usize>> {
    let (start, end) = value
        .split_once('-')
        .map_or((value, value), |(start, end)| (start, end));
    let start = start.parse::<usize>().ok().filter(|line| *line > 0)?;
    let end = end.parse::<usize>().ok().filter(|line| *line >= start)?;
    Some(start..end.saturating_add(1))
}

pub fn selection_hint(viewer: &FileViewer, template: &str) -> Line<'static> {
    Line::from(template.replace("{lines}", &viewer.selected_label()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_plain_and_ranged_file_references() {
        assert_eq!(
            parse_file_reference("@src/main.rs"),
            Some(("src/main.rs".into(), None))
        );
        assert_eq!(
            parse_file_reference("@src/main.rs:4"),
            Some(("src/main.rs".into(), Some(4..5)))
        );
        assert_eq!(
            parse_file_reference("@src/main.rs:4-9"),
            Some(("src/main.rs".into(), Some(4..10)))
        );
        assert_eq!(
            parse_file_reference("@文档/主.rs:2-3\n2| α\n3| β"),
            Some(("文档/主.rs".into(), Some(2..4)))
        );
        assert!(split_at_query("src/main.rs:40-12").is_err());
    }

    #[test]
    fn excerpt_is_bounded_and_fails_closed_past_eof() {
        let content = (1..=5)
            .map(|n| format!("line{n}"))
            .collect::<Vec<_>>()
            .join("\n");
        let backing = file_chip_backing("src/a.rs", 2..4, &content).unwrap();
        assert!(backing.starts_with("@src/a.rs:2-3\n"));
        assert!(backing.contains("2| line2"));
        assert!(backing.contains("3| line3"));
        assert!(!backing.contains("line1"));
        assert!(!backing.contains("line5"));
        assert!(matches!(
            file_chip_backing("src/a.rs", 4..8, &content),
            Err(FileRangeError::OutOfRange { lines: 5 })
        ));
        let wide = (1..=200)
            .map(|n| format!("row{n} {}", "x".repeat(80)))
            .collect::<Vec<_>>()
            .join("\n");
        let bounded = file_chip_backing("src/a.rs", 1..201, &wide).unwrap();
        assert!(bounded.contains("lines omitted"));
        assert!(bounded.len() < wide.len());
    }

    #[test]
    fn confirm_replaces_completion_as_one_atomic_ranged_element() {
        let mut textarea = TextArea::new();
        textarea.insert_str("inspect @main");
        let mut viewer = FileViewer::loading(
            "session".into(),
            "src/main.rs".into(),
            FileViewerOrigin::Completion { range: 8..13 },
            1,
            None,
        );
        viewer.apply_content("one\ntwo\nthree".into(), "hash".into());
        viewer.toggle_range();
        viewer.move_cursor(2, 8);
        viewer.confirm(&mut textarea);
        assert!(textarea.text().starts_with("inspect @src/main.rs:1-3\n"));
        assert!(textarea.text().contains("1| one"));
        assert!(textarea.text().contains("3| three"));
        assert_eq!(textarea.elements().len(), 1);
        textarea.undo();
        assert_eq!(textarea.text(), "inspect @main");
    }
}
