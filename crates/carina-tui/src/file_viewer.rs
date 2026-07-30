use std::ops::Range;

use ratatui::text::Line;
use xai_ratatui_textarea::TextArea;

use crate::context_completion::{FILE_ELEMENT_KIND, file_chip};

pub const MAX_PREVIEW_BYTES: usize = 1024 * 1024;

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
        let backing = format!("@{}:{}", self.path, self.selected_label());
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
    let body = backing.strip_prefix('@')?;
    let (path, range) = match body.rsplit_once(':') {
        Some((path, suffix))
            if suffix
                .chars()
                .all(|character| character.is_ascii_digit() || character == '-') =>
        {
            (path, parse_line_range(suffix))
        }
        _ => (body, None),
    };
    (!path.is_empty()).then(|| (path.to_owned(), range))
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
        assert_eq!(textarea.text(), "inspect @src/main.rs:1-3 ");
        assert_eq!(textarea.elements().len(), 1);
        textarea.undo();
        assert_eq!(textarea.text(), "inspect @main");
    }
}
