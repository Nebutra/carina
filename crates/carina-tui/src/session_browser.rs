use std::path::{Path, PathBuf};

use crate::rpc::{
    ConversationImportApplyResult, ConversationImportCandidate, ConversationImportDiscovery,
    ConversationImportReceipt, ConversationImportSelection, Session,
};

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum SessionScope {
    #[default]
    Workspace,
    All,
    Archived,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum ConversationImportSource {
    #[default]
    All,
    ClaudeCode,
    Codex,
}

impl ConversationImportSource {
    pub fn rpc_value(self) -> Option<&'static str> {
        match self {
            Self::All => None,
            Self::ClaudeCode => Some("claude-code"),
            Self::Codex => Some("codex"),
        }
    }

    fn next(self) -> Self {
        match self {
            Self::All => Self::ClaudeCode,
            Self::ClaudeCode => Self::Codex,
            Self::Codex => Self::All,
        }
    }
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum ConversationImportStage {
    #[default]
    Closed,
    Discovering,
    Selecting,
    Confirming,
    Applying,
    Results,
}

#[derive(Debug, Default)]
pub struct ConversationImportState {
    stage: ConversationImportStage,
    source: ConversationImportSource,
    all_workspaces: bool,
    candidates: Vec<ConversationImportCandidate>,
    selected: Vec<bool>,
    selected_visible: usize,
    warnings: Vec<String>,
    copy_semantics: String,
    receipts: Vec<ConversationImportReceipt>,
    result_selected: usize,
    error: String,
    generation: u64,
}

impl ConversationImportState {
    pub fn stage(&self) -> ConversationImportStage {
        self.stage
    }

    pub fn is_open(&self) -> bool {
        self.stage != ConversationImportStage::Closed
    }

    pub fn source(&self) -> ConversationImportSource {
        self.source
    }

    pub fn all_workspaces(&self) -> bool {
        self.all_workspaces
    }

    pub fn candidates(&self) -> &[ConversationImportCandidate] {
        &self.candidates
    }

    pub fn warnings(&self) -> &[String] {
        &self.warnings
    }

    pub fn copy_semantics(&self) -> &str {
        &self.copy_semantics
    }

    pub fn receipts(&self) -> &[ConversationImportReceipt] {
        &self.receipts
    }

    pub fn selected_visible(&self) -> usize {
        self.selected_visible
    }

    pub fn result_selected(&self) -> usize {
        self.result_selected
    }

    pub fn error(&self) -> &str {
        &self.error
    }

    pub fn is_selected(&self, index: usize) -> bool {
        self.selected.get(index).copied().unwrap_or(false)
    }

    pub fn selected_count(&self) -> usize {
        self.selected.iter().filter(|selected| **selected).count()
    }

    pub fn selected_message_count(&self) -> usize {
        self.candidates
            .iter()
            .zip(&self.selected)
            .filter_map(|(candidate, selected)| selected.then_some(candidate.new_messages))
            .sum()
    }

    pub fn selected_target_summary(&self) -> Option<(&str, usize)> {
        let mut targets =
            self.candidates
                .iter()
                .zip(&self.selected)
                .filter_map(|(candidate, selected)| {
                    (*selected && !candidate.target_workspace.is_empty())
                        .then_some(candidate.target_workspace.as_str())
                });
        let first = targets.next()?;
        let mut unique = Vec::new();
        for target in targets {
            if target != first && !unique.contains(&target) {
                unique.push(target);
            }
        }
        Some((first, unique.len()))
    }

    pub fn begin(&mut self) -> u64 {
        *self = Self::default();
        self.begin_discovery()
    }

    pub fn begin_discovery(&mut self) -> u64 {
        self.generation = self.generation.saturating_add(1);
        self.stage = ConversationImportStage::Discovering;
        self.candidates.clear();
        self.selected.clear();
        self.selected_visible = 0;
        self.warnings.clear();
        self.copy_semantics.clear();
        self.receipts.clear();
        self.result_selected = 0;
        self.error.clear();
        self.generation
    }

    pub fn accepts(&self, generation: u64, stage: ConversationImportStage) -> bool {
        self.generation == generation && self.stage == stage
    }

    pub fn apply_discovery(
        &mut self,
        generation: u64,
        result: Result<ConversationImportDiscovery, String>,
    ) {
        if !self.accepts(generation, ConversationImportStage::Discovering) {
            return;
        }
        match result {
            Ok(discovery) => {
                self.selected = vec![false; discovery.conversations.len()];
                self.candidates = discovery.conversations;
                self.warnings = discovery.warnings;
                self.copy_semantics = discovery.copy_semantics;
                self.stage = ConversationImportStage::Selecting;
                self.normalize_candidate_selection();
            }
            Err(error) => {
                self.error = error;
                self.stage = ConversationImportStage::Selecting;
            }
        }
    }

    pub fn cycle_source(&mut self) -> u64 {
        self.source = self.source.next();
        self.begin_discovery()
    }

    pub fn toggle_workspace_scope(&mut self) -> u64 {
        self.all_workspaces = !self.all_workspaces;
        self.begin_discovery()
    }

    pub fn move_candidate(&mut self, down: bool) {
        if self.candidates.is_empty() {
            self.selected_visible = 0;
        } else if down {
            self.selected_visible = (self.selected_visible + 1).min(self.candidates.len() - 1);
        } else {
            self.selected_visible = self.selected_visible.saturating_sub(1);
        }
        self.error.clear();
    }

    pub fn toggle_candidate(&mut self, index: Option<usize>) {
        let index = index.unwrap_or(self.selected_visible);
        let Some(candidate) = self.candidates.get(index) else {
            return;
        };
        self.selected_visible = index;
        if candidate.importable && candidate.new_messages > 0 {
            if let Some(selected) = self.selected.get_mut(index) {
                *selected = !*selected;
            }
            self.error.clear();
        } else if !candidate.import_error.is_empty() {
            self.error = candidate.import_error.clone();
        }
    }

    pub fn toggle_all(&mut self) {
        let selectable = self
            .candidates
            .iter()
            .enumerate()
            .filter(|(_, candidate)| candidate.importable && candidate.new_messages > 0)
            .map(|(index, _)| index)
            .collect::<Vec<_>>();
        let select = selectable
            .iter()
            .any(|index| !self.selected.get(*index).copied().unwrap_or(false));
        for index in selectable {
            self.selected[index] = select;
        }
        self.error.clear();
    }

    pub fn begin_confirmation(&mut self) -> bool {
        if self.selected_count() == 0 {
            return false;
        }
        self.stage = ConversationImportStage::Confirming;
        self.error.clear();
        true
    }

    pub fn cancel_confirmation(&mut self) {
        if self.stage == ConversationImportStage::Confirming {
            self.stage = ConversationImportStage::Selecting;
            self.error.clear();
        }
    }

    pub fn begin_apply(&mut self) -> Option<(u64, Vec<ConversationImportSelection>)> {
        if self.stage != ConversationImportStage::Confirming {
            return None;
        }
        let selections = self
            .candidates
            .iter()
            .zip(&self.selected)
            .filter(|(_, selected)| **selected)
            .map(|(candidate, _)| ConversationImportSelection {
                source: candidate.source.clone(),
                source_root: String::new(),
                path: candidate.path.clone(),
                conversation_id: candidate.id.clone(),
                target_workspace: candidate.target_workspace.clone(),
            })
            .collect::<Vec<_>>();
        if selections.is_empty() {
            return None;
        }
        self.generation = self.generation.saturating_add(1);
        self.stage = ConversationImportStage::Applying;
        self.error.clear();
        Some((self.generation, selections))
    }

    pub fn apply_results(
        &mut self,
        generation: u64,
        result: Result<ConversationImportApplyResult, String>,
    ) {
        if !self.accepts(generation, ConversationImportStage::Applying) {
            return;
        }
        match result {
            Ok(result) => {
                self.receipts = result.results;
                self.result_selected = 0;
                self.stage = ConversationImportStage::Results;
                self.error.clear();
            }
            Err(error) => {
                self.stage = ConversationImportStage::Confirming;
                self.error = error;
            }
        }
    }

    pub fn move_result(&mut self, down: bool) {
        if self.receipts.is_empty() {
            self.result_selected = 0;
        } else if down {
            self.result_selected = (self.result_selected + 1).min(self.receipts.len() - 1);
        } else {
            self.result_selected = self.result_selected.saturating_sub(1);
        }
    }

    pub fn select_result(&mut self, index: usize) {
        if index < self.receipts.len() {
            self.result_selected = index;
        }
    }

    pub fn selected_result_session_id(&self) -> Option<&str> {
        self.receipts
            .get(self.result_selected)
            .map(|receipt| receipt.session_id.as_str())
            .filter(|session_id| !session_id.is_empty())
    }

    pub fn close(&mut self) {
        self.generation = self.generation.saturating_add(1);
        self.stage = ConversationImportStage::Closed;
        self.error.clear();
    }

    fn normalize_candidate_selection(&mut self) {
        self.selected_visible = self
            .selected_visible
            .min(self.candidates.len().saturating_sub(1));
    }
}

#[derive(Debug, Default)]
pub struct SessionBrowserState {
    query: String,
    search_active: bool,
    scope: SessionScope,
    selected_visible: usize,
    loading_session_id: Option<String>,
    error: String,
    generation: u64,
    preview_session_id: Option<String>,
    preview_lines: Vec<String>,
    renaming_session_id: Option<String>,
    rename_value: String,
    archive_confirmation_id: Option<String>,
    conversation_import: ConversationImportState,
}

impl SessionBrowserState {
    pub fn conversation_import(&self) -> &ConversationImportState {
        &self.conversation_import
    }

    pub fn conversation_import_mut(&mut self) -> &mut ConversationImportState {
        &mut self.conversation_import
    }
    pub fn query(&self) -> &str {
        &self.query
    }

    pub fn search_active(&self) -> bool {
        self.search_active
    }

    pub fn scope(&self) -> SessionScope {
        self.scope
    }

    pub fn selected_visible(&self) -> usize {
        self.selected_visible
    }

    pub fn loading_session_id(&self) -> Option<&str> {
        self.loading_session_id.as_deref()
    }

    pub fn error(&self) -> &str {
        &self.error
    }

    pub fn show_error(&mut self, message: String) {
        self.error = message;
    }

    pub fn preview_lines(&self) -> &[String] {
        &self.preview_lines
    }

    pub fn renaming_session_id(&self) -> Option<&str> {
        self.renaming_session_id.as_deref()
    }

    pub fn rename_value(&self) -> &str {
        &self.rename_value
    }

    pub fn archive_confirmation_id(&self) -> Option<&str> {
        self.archive_confirmation_id.as_deref()
    }

    pub fn begin_archive(&mut self, session: &Session) {
        self.search_active = false;
        self.renaming_session_id = None;
        self.rename_value.clear();
        self.archive_confirmation_id = Some(session.session_id.clone());
        self.error.clear();
    }

    pub fn cancel_archive(&mut self) {
        self.archive_confirmation_id = None;
        self.error.clear();
    }

    pub fn finish_archive_action(&mut self, sessions: &[Session], workspace: &Path) {
        self.archive_confirmation_id = None;
        self.error.clear();
        self.normalize_selection(sessions, workspace);
    }

    pub fn begin_rename(&mut self, session: &Session) {
        self.search_active = false;
        self.archive_confirmation_id = None;
        self.renaming_session_id = Some(session.session_id.clone());
        self.rename_value = session.name.clone();
        self.error.clear();
    }

    pub fn push_rename(&mut self, character: char) {
        if self.rename_value.chars().count() < 80 && !character.is_control() {
            self.rename_value.push(character);
        }
        self.error.clear();
    }

    pub fn backspace_rename(&mut self) {
        self.rename_value.pop();
        self.error.clear();
    }

    pub fn cancel_rename(&mut self) {
        self.renaming_session_id = None;
        self.rename_value.clear();
        self.archive_confirmation_id = None;
        self.error.clear();
    }

    pub fn finish_rename(&mut self, sessions: &[Session], workspace: &Path) {
        self.renaming_session_id = None;
        self.rename_value.clear();
        self.archive_confirmation_id = None;
        self.error.clear();
        self.normalize_selection(sessions, workspace);
    }

    pub fn open(&mut self, sessions: &[Session], workspace: &Path) {
        self.query.clear();
        self.search_active = false;
        self.loading_session_id = None;
        self.error.clear();
        self.generation = self.generation.saturating_add(1);
        self.preview_session_id = None;
        self.preview_lines.clear();
        self.renaming_session_id = None;
        self.rename_value.clear();
        self.conversation_import.close();
        self.normalize_selection(sessions, workspace);
    }

    pub fn begin_search(&mut self) {
        self.renaming_session_id = None;
        self.rename_value.clear();
        self.search_active = true;
        self.error.clear();
    }

    pub fn cancel_search(&mut self) -> bool {
        if !self.search_active {
            return false;
        }
        self.search_active = false;
        true
    }

    pub fn push(&mut self, character: char, sessions: &[Session], workspace: &Path) {
        self.query.push(character);
        self.selected_visible = 0;
        self.normalize_selection(sessions, workspace);
    }

    pub fn backspace(&mut self, sessions: &[Session], workspace: &Path) {
        self.query.pop();
        self.selected_visible = 0;
        self.normalize_selection(sessions, workspace);
    }

    pub fn cycle_scope(&mut self, sessions: &[Session], workspace: &Path, forward: bool) {
        self.scope = match (self.scope, forward) {
            (SessionScope::Workspace, true) | (SessionScope::Archived, false) => SessionScope::All,
            (SessionScope::All, true) | (SessionScope::Workspace, false) => SessionScope::Archived,
            (SessionScope::Archived, true) | (SessionScope::All, false) => SessionScope::Workspace,
        };
        self.selected_visible = 0;
        self.error.clear();
        self.normalize_selection(sessions, workspace);
    }

    pub fn toggle_scope(&mut self, sessions: &[Session], workspace: &Path) {
        self.cycle_scope(sessions, workspace, true);
    }

    pub fn move_selection(&mut self, sessions: &[Session], workspace: &Path, down: bool) {
        self.renaming_session_id = None;
        self.rename_value.clear();
        self.archive_confirmation_id = None;
        let len = self.visible_indices(sessions, workspace).len();
        if len == 0 {
            self.selected_visible = 0;
        } else if down {
            self.selected_visible = (self.selected_visible + 1).min(len - 1);
        } else {
            self.selected_visible = self.selected_visible.saturating_sub(1);
        }
        self.error.clear();
    }

    pub fn select_visible(&mut self, visible: usize, sessions: &[Session], workspace: &Path) {
        let len = self.visible_indices(sessions, workspace).len();
        if visible < len {
            self.selected_visible = visible;
            self.renaming_session_id = None;
            self.rename_value.clear();
            self.archive_confirmation_id = None;
            self.error.clear();
        }
    }

    pub fn selected_index(&self, sessions: &[Session], workspace: &Path) -> Option<usize> {
        self.visible_indices(sessions, workspace)
            .get(self.selected_visible)
            .copied()
    }

    pub fn visible_indices(&self, sessions: &[Session], workspace: &Path) -> Vec<usize> {
        let query = self.query.trim().to_ascii_lowercase();
        sessions
            .iter()
            .enumerate()
            .filter(|(_, session)| match self.scope {
                SessionScope::Workspace => {
                    session.status != "closed" && same_workspace(&session.workspace_root, workspace)
                }
                SessionScope::All => session.status != "closed",
                SessionScope::Archived => session.status == "closed",
            })
            .filter(|(_, session)| {
                query.is_empty()
                    || [
                        session.name.as_str(),
                        session.summary.as_str(),
                        session.workspace_root.as_str(),
                        session.next_model.as_str(),
                        session.status.as_str(),
                        session.execution_status.as_str(),
                    ]
                    .iter()
                    .any(|value| value.to_ascii_lowercase().contains(&query))
            })
            .map(|(index, _)| index)
            .collect()
    }

    pub fn begin_load(&mut self, session_id: String) -> u64 {
        self.generation = self.generation.saturating_add(1);
        self.loading_session_id = Some(session_id);
        self.error.clear();
        self.generation
    }

    pub fn accepts_load(&self, generation: u64, session_id: &str) -> bool {
        self.generation == generation && self.loading_session_id.as_deref() == Some(session_id)
    }

    pub fn finish_load(&mut self, generation: u64, session_id: &str) {
        if !self.accepts_load(generation, session_id) {
            return;
        }
        self.loading_session_id = None;
    }

    pub fn fail_load(&mut self, generation: u64, session_id: &str, message: String) {
        if !self.accepts_load(generation, session_id) {
            return;
        }
        self.loading_session_id = None;
        self.error = message;
    }

    pub fn begin_preview(&mut self, session_id: String) -> u64 {
        self.generation = self.generation.saturating_add(1);
        self.preview_session_id = Some(session_id);
        self.preview_lines.clear();
        self.generation
    }

    pub fn apply_preview(&mut self, generation: u64, session_id: &str, lines: Vec<String>) {
        if self.generation == generation && self.preview_session_id.as_deref() == Some(session_id) {
            self.preview_lines = lines;
        }
    }

    fn normalize_selection(&mut self, sessions: &[Session], workspace: &Path) {
        self.selected_visible = self.selected_visible.min(
            self.visible_indices(sessions, workspace)
                .len()
                .saturating_sub(1),
        );
    }
}

fn same_workspace(session_root: &str, workspace: &Path) -> bool {
    if session_root.trim().is_empty() {
        return false;
    }
    let session = PathBuf::from(session_root);
    match (session.canonicalize(), workspace.canonicalize()) {
        (Ok(session), Ok(workspace)) => session == workspace,
        _ => session == workspace,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rpc::ConversationImportDiscovery;

    fn session(id: &str, root: &str, name: &str, summary: &str) -> Session {
        Session {
            session_id: id.into(),
            name: name.into(),
            workspace_id: String::new(),
            workspace_root: root.into(),
            status: "active".into(),
            next_model: "openai/gpt-5".into(),
            next_reasoning_effort: "high".into(),
            plan_mode: false,
            permission_profile: "safe-edit".into(),
            approval_mode: "on_request".into(),
            created_at: String::new(),
            updated_at: String::new(),
            latest_run_id: String::new(),
            latest_run_agent: String::new(),
            latest_run_result_kind: String::new(),
            execution_status: String::new(),
            summary: summary.into(),
            continuity: None,
        }
    }

    fn import_candidate(
        id: &str,
        new_messages: usize,
        importable: bool,
    ) -> ConversationImportCandidate {
        ConversationImportCandidate {
            source: "codex".into(),
            id: id.into(),
            path: format!("/tmp/{id}.jsonl"),
            workspace_root: "/tmp/current".into(),
            title: format!("Conversation {id}"),
            message_count: new_messages,
            new_messages,
            target_workspace: if importable { "/tmp/current" } else { "" }.into(),
            importable,
            import_error: if importable {
                ""
            } else {
                "workspace unavailable"
            }
            .into(),
            ..ConversationImportCandidate::default()
        }
    }

    #[test]
    fn filtering_and_selection_share_one_visible_projection() {
        let workspace = Path::new("/tmp/current");
        let sessions = vec![
            session("a", "/tmp/current", "Alpha", "first"),
            session("b", "/tmp/current", "Beta", "second"),
        ];
        let mut state = SessionBrowserState::default();
        state.begin_search();
        for character in "beta".chars() {
            state.push(character, &sessions, workspace);
        }
        assert_eq!(state.visible_indices(&sessions, workspace), vec![1]);
        assert_eq!(state.selected_index(&sessions, workspace), Some(1));
    }

    #[test]
    fn reopening_after_navigation_clears_the_previous_query() {
        let workspace = Path::new("/tmp/current");
        let sessions = vec![
            session("a", "/tmp/current", "Alpha", "first"),
            session("b", "/tmp/current", "Beta", "second"),
        ];
        let mut browser = SessionBrowserState::default();
        browser.open(&sessions, workspace);
        browser.begin_search();
        for character in "Beta".chars() {
            browser.push(character, &sessions, workspace);
        }
        assert_eq!(browser.query(), "Beta");
        browser.open(&sessions, workspace);
        assert_eq!(browser.query(), "");
        assert!(!browser.search_active());
        assert_eq!(browser.visible_indices(&sessions, workspace).len(), 2);
    }

    #[test]
    fn scope_change_normalizes_selection_and_empty_workspace_can_reach_all() {
        let workspace = Path::new("/tmp/current");
        let sessions = vec![session("other", "/tmp/other", "Other", "elsewhere")];
        let mut state = SessionBrowserState::default();
        assert!(state.visible_indices(&sessions, workspace).is_empty());
        state.toggle_scope(&sessions, workspace);
        assert_eq!(state.scope(), SessionScope::All);
        assert_eq!(state.selected_index(&sessions, workspace), Some(0));
        state.toggle_scope(&sessions, workspace);
        assert_eq!(state.scope(), SessionScope::Archived);
        assert_eq!(state.selected_index(&sessions, workspace), None);

        state.cycle_scope(&sessions, workspace, false);
        assert_eq!(state.scope(), SessionScope::All);
        state.cycle_scope(&sessions, workspace, false);
        assert_eq!(state.scope(), SessionScope::Workspace);
    }

    #[test]
    fn load_failure_retains_query_scope_and_selection() {
        let workspace = Path::new("/tmp/current");
        let sessions = vec![session("a", "/tmp/current", "Alpha", "first")];
        let mut state = SessionBrowserState::default();
        state.begin_search();
        state.push('a', &sessions, workspace);
        let generation = state.begin_load("a".into());
        state.fail_load(generation, "a", "moved workspace".into());
        assert_eq!(state.query(), "a");
        assert_eq!(state.selected_index(&sessions, workspace), Some(0));
        assert_eq!(state.error(), "moved workspace");
    }

    #[test]
    fn stale_load_result_cannot_replace_the_current_target() {
        let mut state = SessionBrowserState::default();
        let first = state.begin_load("first".into());
        let second = state.begin_load("second".into());
        state.fail_load(first, "first", "stale".into());
        assert_eq!(state.loading_session_id(), Some("second"));
        assert!(state.error().is_empty());
        state.finish_load(second, "second");
        assert_eq!(state.loading_session_id(), None);
    }

    #[test]
    fn rename_is_bounded_and_cancel_does_not_change_the_session() {
        let source = session("a", "/tmp/current", "Original", "first");
        let mut state = SessionBrowserState::default();
        state.begin_rename(&source);
        assert_eq!(state.renaming_session_id(), Some("a"));
        assert_eq!(state.rename_value(), "Original");
        for _ in 0..100 {
            state.push_rename('x');
        }
        assert_eq!(state.rename_value().chars().count(), 80);
        state.cancel_rename();
        assert_eq!(state.renaming_session_id(), None);
        assert_eq!(source.name, "Original");
    }

    #[test]
    fn archived_scope_is_distinct_and_archive_confirmation_is_target_scoped() {
        let workspace = Path::new("/tmp/current");
        let active = session("active", "/tmp/current", "Active", "first");
        let mut archived = session("archived", "/tmp/current", "Archived", "second");
        archived.status = "closed".into();
        let sessions = vec![active.clone(), archived];
        let mut state = SessionBrowserState::default();
        assert_eq!(state.visible_indices(&sessions, workspace), vec![0]);
        state.toggle_scope(&sessions, workspace);
        assert_eq!(state.visible_indices(&sessions, workspace), vec![0]);
        state.toggle_scope(&sessions, workspace);
        assert_eq!(state.visible_indices(&sessions, workspace), vec![1]);
        state.begin_archive(&active);
        assert_eq!(state.archive_confirmation_id(), Some("active"));
        state.cancel_archive();
        assert_eq!(state.archive_confirmation_id(), None);
    }

    #[test]
    fn conversation_import_requires_explicit_selection_and_confirmation() {
        let mut state = ConversationImportState::default();
        let generation = state.begin();
        state.apply_discovery(
            generation,
            Ok(ConversationImportDiscovery {
                conversations: vec![
                    import_candidate("new", 2, true),
                    import_candidate("missing", 1, false),
                ],
                copy_semantics: "local copy".into(),
                ..ConversationImportDiscovery::default()
            }),
        );
        assert_eq!(state.stage(), ConversationImportStage::Selecting);
        assert!(!state.begin_confirmation());
        state.toggle_candidate(Some(1));
        assert_eq!(state.error(), "workspace unavailable");
        state.toggle_candidate(Some(0));
        assert_eq!(state.selected_count(), 1);
        assert!(state.begin_confirmation());
        let (_, selections) = state.begin_apply().expect("confirmed selection");
        assert_eq!(selections.len(), 1);
        assert_eq!(selections[0].conversation_id, "new");
        assert_eq!(selections[0].target_workspace, "/tmp/current");
    }

    #[test]
    fn stale_conversation_discovery_does_not_replace_newer_scope() {
        let mut state = ConversationImportState::default();
        let first = state.begin();
        let second = state.toggle_workspace_scope();
        state.apply_discovery(
            first,
            Ok(ConversationImportDiscovery {
                conversations: vec![import_candidate("stale", 1, true)],
                ..ConversationImportDiscovery::default()
            }),
        );
        assert_eq!(state.stage(), ConversationImportStage::Discovering);
        assert!(state.candidates().is_empty());
        state.apply_discovery(
            second,
            Ok(ConversationImportDiscovery {
                conversations: vec![import_candidate("current", 1, true)],
                ..ConversationImportDiscovery::default()
            }),
        );
        assert_eq!(state.candidates()[0].id, "current");
    }
}
