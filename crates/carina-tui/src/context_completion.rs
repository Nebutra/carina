use std::ops::Range;

use ratatui::text::{Line, Span};
use xai_ratatui_textarea::{ElementKind, TextArea};

use crate::rpc::WorkspaceFile;

pub const FILE_ELEMENT_KIND: ElementKind = ElementKind(2);
const MAX_RESULTS: usize = 100;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AtContext {
    pub range: Range<usize>,
    pub query: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FileCandidate {
    pub path: String,
    pub language: String,
    pub size: u64,
    pub binary: bool,
    pub large: bool,
    pub score: i64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum InventoryState {
    Unloaded,
    Loading { generation: u64, session_id: String },
    Ready(Vec<WorkspaceFile>),
    Failed(String),
}

#[derive(Debug, Clone)]
pub struct ContextCompletion {
    context: Option<AtContext>,
    inventory: InventoryState,
    results: Vec<FileCandidate>,
    selected: usize,
    generation: u64,
    dismissed_token: Option<String>,
}

impl Default for ContextCompletion {
    fn default() -> Self {
        Self {
            context: None,
            inventory: InventoryState::Unloaded,
            results: Vec::new(),
            selected: 0,
            generation: 0,
            dismissed_token: None,
        }
    }
}

impl ContextCompletion {
    pub fn reset_session(&mut self) {
        self.generation = self.generation.saturating_add(1);
        self.context = None;
        self.inventory = InventoryState::Unloaded;
        self.results.clear();
        self.selected = 0;
        self.dismissed_token = None;
    }

    pub fn update_context(&mut self, textarea: &TextArea) -> bool {
        let cursor = textarea.cursor();
        let on_file_element = textarea.elements().iter().any(|element| {
            element.kind == FILE_ELEMENT_KIND
                && cursor >= element.range.start
                && cursor <= element.range.end
        });
        let next = if on_file_element {
            None
        } else {
            detect_at_context(textarea.text(), cursor)
        };
        let token = next
            .as_ref()
            .and_then(|context| textarea.text().get(context.range.clone()))
            .map(str::to_owned);
        if self.dismissed_token.as_ref() != token.as_ref() {
            self.dismissed_token = None;
        }
        self.context = if self.dismissed_token.as_ref() == token.as_ref() {
            None
        } else {
            next
        };
        self.recompute();
        self.context.is_some()
            && matches!(
                self.inventory,
                InventoryState::Unloaded | InventoryState::Failed(_)
            )
    }

    pub fn begin_load(&mut self, session_id: String) -> u64 {
        self.generation = self.generation.saturating_add(1);
        let generation = self.generation;
        self.inventory = InventoryState::Loading {
            generation,
            session_id,
        };
        generation
    }

    pub fn apply_load(
        &mut self,
        generation: u64,
        session_id: &str,
        result: Result<Vec<WorkspaceFile>, String>,
    ) -> bool {
        if !matches!(
            &self.inventory,
            InventoryState::Loading {
                generation: active_generation,
                session_id: active_session,
            } if *active_generation == generation && active_session == session_id
        ) {
            return false;
        }
        self.inventory = match result {
            Ok(files) => InventoryState::Ready(files),
            Err(error) => InventoryState::Failed(error),
        };
        self.recompute();
        true
    }

    pub fn is_open(&self) -> bool {
        self.context.is_some()
    }

    pub fn is_loading(&self) -> bool {
        self.is_open() && matches!(self.inventory, InventoryState::Loading { .. })
    }

    pub fn error(&self) -> Option<&str> {
        self.is_open().then_some(())?;
        match &self.inventory {
            InventoryState::Failed(error) => Some(error),
            _ => None,
        }
    }

    pub fn query(&self) -> &str {
        self.context
            .as_ref()
            .map(|context| context.query.as_str())
            .unwrap_or_default()
    }

    pub fn results(&self) -> &[FileCandidate] {
        &self.results
    }

    pub fn selected(&self) -> usize {
        self.selected
    }

    pub fn viewer_target(&self) -> Option<(AtContext, FileCandidate)> {
        Some((
            self.context.clone()?,
            self.results.get(self.selected)?.clone(),
        ))
    }

    pub fn move_selection(&mut self, delta: isize) {
        let max = self.results.len().saturating_sub(1);
        self.selected = self.selected.saturating_add_signed(delta).min(max);
    }

    pub fn page(&mut self, delta: isize, visible_rows: usize) {
        self.move_selection(delta * (visible_rows / 2).max(1) as isize);
    }

    pub fn select(&mut self, index: usize) -> bool {
        if index >= self.results.len() {
            return false;
        }
        let accept = self.selected == index;
        self.selected = index;
        accept
    }

    pub fn dismiss(&mut self, textarea: &TextArea) {
        self.dismissed_token = self
            .context
            .as_ref()
            .and_then(|context| textarea.text().get(context.range.clone()))
            .map(str::to_owned);
        self.context = None;
        self.results.clear();
        self.selected = 0;
    }

    pub fn accept(&mut self, textarea: &mut TextArea) -> bool {
        let Some(context) = self.context.clone() else {
            return false;
        };
        let Some(candidate) = self.results.get(self.selected).cloned() else {
            return false;
        };
        let backing = format!("@{}", candidate.path);
        let display = file_chip(&candidate.path, None);
        textarea.begin_undo_group();
        textarea.replace_range_with_element(
            context.range,
            &backing,
            FILE_ELEMENT_KIND,
            Some(display),
        );
        textarea.insert_str(" ");
        textarea.end_undo_group();
        self.context = None;
        self.results.clear();
        self.selected = 0;
        self.dismissed_token = None;
        true
    }

    fn recompute(&mut self) {
        let Some(context) = &self.context else {
            self.results.clear();
            self.selected = 0;
            return;
        };
        let InventoryState::Ready(files) = &self.inventory else {
            self.results.clear();
            self.selected = 0;
            return;
        };
        let query = context.query.to_lowercase();
        let mut results: Vec<_> = files
            .iter()
            .filter(|file| !file.path.trim().is_empty())
            .filter_map(|file| {
                fuzzy_path_score(&file.path, &query).map(|score| FileCandidate {
                    path: file.path.trim_start_matches("./").to_owned(),
                    language: file.language.clone(),
                    size: file.size,
                    binary: file.binary,
                    large: file.large,
                    score,
                })
            })
            .collect();
        results.sort_by(|left, right| {
            right
                .score
                .cmp(&left.score)
                .then_with(|| left.path.len().cmp(&right.path.len()))
                .then_with(|| left.path.cmp(&right.path))
        });
        results.truncate(MAX_RESULTS);
        self.results = results;
        self.selected = self.selected.min(self.results.len().saturating_sub(1));
    }
}

pub fn detect_at_context(text: &str, cursor: usize) -> Option<AtContext> {
    if cursor == 0 || cursor > text.len() || !text.is_char_boundary(cursor) {
        return None;
    }
    let at = text[..cursor].rfind('@')?;
    if text[..at]
        .chars()
        .next_back()
        .is_some_and(|character| character.is_alphanumeric() || character == '_')
    {
        return None;
    }
    let end = text[at + 1..]
        .char_indices()
        .find_map(|(offset, character)| {
            (character.is_whitespace() || matches!(character, ',' | ';')).then_some(at + 1 + offset)
        })
        .unwrap_or(text.len());
    if cursor > end {
        return None;
    }
    Some(AtContext {
        range: at..end,
        query: text[at + 1..cursor].to_owned(),
    })
}

fn fuzzy_path_score(path: &str, query: &str) -> Option<i64> {
    if query.is_empty() {
        return Some(0);
    }
    let path = path.to_lowercase();
    if let Some(offset) = path.find(query) {
        let basename_bonus =
            path.rsplit_once('/')
                .is_some_and(|(_, basename)| basename.starts_with(query)) as i64
                * 2_000;
        return Some(20_000 + basename_bonus - offset as i64 * 8 - path.len() as i64);
    }
    let mut wanted = query.chars();
    let mut next = wanted.next()?;
    let mut score = 0_i64;
    let mut previous = None;
    for (index, character) in path.chars().enumerate() {
        if character != next {
            continue;
        }
        score += 100;
        if previous == Some(index.saturating_sub(1)) {
            score += 50;
        }
        if index == 0
            || path
                .chars()
                .nth(index.saturating_sub(1))
                .is_some_and(|before| matches!(before, '/' | '_' | '-' | '.'))
        {
            score += 30;
        }
        previous = Some(index);
        match wanted.next() {
            Some(character) => next = character,
            None => return Some(score - path.len() as i64),
        }
    }
    None
}

pub fn file_chip(path: &str, lines: Option<Range<usize>>) -> Line<'static> {
    let theme = crate::theme::Theme::detected(None);
    let suffix = lines.map_or_else(String::new, |range| {
        if range.end == range.start + 1 {
            format!(":{}", range.start)
        } else {
            format!(":{}-{}", range.start, range.end - 1)
        }
    });
    Line::from(vec![
        Span::styled(" @ ", theme.action()),
        Span::styled(format!(" {path}{suffix} "), theme.focus()),
    ])
}

#[cfg(test)]
mod tests {
    use super::*;

    fn files() -> Vec<WorkspaceFile> {
        vec![
            WorkspaceFile {
                path: "src/app/render.rs".into(),
                language: "rust".into(),
                ..Default::default()
            },
            WorkspaceFile {
                path: "src/runtime.rs".into(),
                language: "rust".into(),
                ..Default::default()
            },
        ]
    }

    #[test]
    fn context_rejects_email_and_stops_at_delimiters() {
        assert!(detect_at_context("user@example.com", 16).is_none());
        let context = detect_at_context("read @src/app, then", 13).unwrap();
        assert_eq!(context.range, 5..13);
        assert_eq!(context.query, "src/app");
    }

    #[test]
    fn stale_inventory_result_is_ignored() {
        let mut completion = ContextCompletion::default();
        let mut textarea = TextArea::new();
        textarea.insert_str("@src");
        assert!(completion.update_context(&textarea));
        let generation = completion.begin_load("one".into());
        assert!(!completion.apply_load(generation, "two", Ok(files())));
        assert!(completion.is_loading());
    }

    #[test]
    fn acceptance_is_one_atomic_file_element_and_undo_restores_query() {
        let mut completion = ContextCompletion::default();
        let mut textarea = TextArea::new();
        textarea.insert_str("inspect @render");
        assert!(completion.update_context(&textarea));
        let generation = completion.begin_load("session".into());
        assert!(completion.apply_load(generation, "session", Ok(files())));
        assert_eq!(completion.results()[0].path, "src/app/render.rs");
        assert!(completion.accept(&mut textarea));
        assert_eq!(textarea.text(), "inspect @src/app/render.rs ");
        assert_eq!(textarea.elements().len(), 1);
        assert_eq!(textarea.elements()[0].kind, FILE_ELEMENT_KIND);
        textarea.undo();
        assert_eq!(textarea.text(), "inspect @render");
        assert!(textarea.elements().is_empty());
    }

    #[test]
    fn accepted_file_chip_deletes_atomically() {
        let mut completion = ContextCompletion::default();
        let mut textarea = TextArea::new();
        textarea.insert_str("inspect @render");
        assert!(completion.update_context(&textarea));
        let generation = completion.begin_load("session".into());
        assert!(completion.apply_load(generation, "session", Ok(files())));
        assert!(completion.accept(&mut textarea));
        textarea.delete_backward(2);
        assert_eq!(textarea.text(), "inspect ");
        assert!(textarea.elements().is_empty());
    }

    #[test]
    fn editing_after_inventory_failure_requests_a_retry() {
        let mut completion = ContextCompletion::default();
        let mut textarea = TextArea::new();
        textarea.insert_str("@src");
        assert!(completion.update_context(&textarea));
        let generation = completion.begin_load("session".into());
        assert!(completion.apply_load(generation, "session", Err("FileRead denied".into())));
        assert_eq!(completion.error(), Some("FileRead denied"));
        textarea.insert_str("/");
        assert!(completion.update_context(&textarea));
    }
}
