#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HistoryMatch {
    pub text: String,
    pub score: i64,
    pub recency: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HistoryMode {
    Browse,
    Search,
}

#[derive(Debug, Clone, Default)]
pub struct HistorySearchState {
    mode: Option<HistoryMode>,
    saved_draft: String,
    query: String,
    source: Vec<String>,
    matches: Vec<HistoryMatch>,
    selected: usize,
    persistent_unavailable: bool,
}

impl HistorySearchState {
    pub fn activate(history: Vec<String>, draft: String, persistent_unavailable: bool) -> Self {
        Self::activate_with_mode(HistoryMode::Search, history, draft, persistent_unavailable)
    }

    pub fn activate_browse(
        history: Vec<String>,
        draft: String,
        persistent_unavailable: bool,
    ) -> Self {
        Self::activate_with_mode(HistoryMode::Browse, history, draft, persistent_unavailable)
    }

    fn activate_with_mode(
        mode: HistoryMode,
        history: Vec<String>,
        draft: String,
        persistent_unavailable: bool,
    ) -> Self {
        let mut state = Self {
            mode: Some(mode),
            saved_draft: draft,
            query: String::new(),
            source: history,
            matches: Vec::new(),
            selected: 0,
            persistent_unavailable,
        };
        state.recompute();
        state
    }

    pub fn mode(&self) -> HistoryMode {
        self.mode.unwrap_or(HistoryMode::Search)
    }

    pub fn is_browse(&self) -> bool {
        self.mode() == HistoryMode::Browse
    }

    pub fn saved_draft(&self) -> &str {
        &self.saved_draft
    }

    pub fn query(&self) -> &str {
        &self.query
    }

    pub fn matches(&self) -> &[HistoryMatch] {
        &self.matches
    }

    pub fn selected(&self) -> usize {
        self.selected
    }

    pub fn persistent_unavailable(&self) -> bool {
        self.persistent_unavailable
    }

    pub fn selected_text(&self) -> Option<&str> {
        self.matches
            .get(self.selected)
            .map(|entry| entry.text.as_str())
    }

    pub fn push(&mut self, character: char) {
        self.query.push(character);
        self.recompute();
    }

    pub fn backspace(&mut self) {
        self.query.pop();
        self.recompute();
    }

    pub fn move_older(&mut self) {
        self.selected = self.selected.saturating_sub(1);
    }

    pub fn move_newer(&mut self) {
        self.selected = (self.selected + 1).min(self.matches.len().saturating_sub(1));
    }

    pub fn is_newest_selected(&self) -> bool {
        !self.matches.is_empty() && self.selected + 1 == self.matches.len()
    }

    pub fn page(&mut self, delta: isize, visible_rows: usize) {
        let amount = (visible_rows / 2).max(1);
        if delta < 0 {
            self.selected = self.selected.saturating_sub(amount);
        } else {
            self.selected = (self.selected + amount).min(self.matches.len().saturating_sub(1));
        }
    }

    pub fn select(&mut self, index: usize) -> bool {
        if index >= self.matches.len() {
            return false;
        }
        let already_selected = self.selected == index;
        self.selected = index;
        already_selected
    }

    fn recompute(&mut self) {
        let query = self.query.trim().to_lowercase();
        self.matches = self
            .source
            .iter()
            .enumerate()
            .filter_map(|(recency, text)| {
                let score = fuzzy_score(text, &query)?;
                Some(HistoryMatch {
                    text: text.clone(),
                    score,
                    recency,
                })
            })
            .collect();
        self.matches.sort_by(|left, right| {
            left.score
                .cmp(&right.score)
                .then_with(|| right.recency.cmp(&left.recency))
        });
        self.selected = self.matches.len().saturating_sub(1);
    }
}

fn fuzzy_score(candidate: &str, query: &str) -> Option<i64> {
    if query.is_empty() {
        return Some(0);
    }
    let candidate = candidate.to_lowercase();
    if let Some(offset) = candidate.find(query) {
        return Some(10_000 - offset as i64 * 4 - candidate.len() as i64);
    }

    let mut query_chars = query.chars();
    let mut wanted = query_chars.next()?;
    let mut score = 0_i64;
    let mut previous_match = None;
    for (index, character) in candidate.chars().enumerate() {
        if character != wanted {
            continue;
        }
        score += 100;
        if previous_match == Some(index.saturating_sub(1)) {
            score += 40;
        }
        if index == 0
            || candidate
                .chars()
                .nth(index.saturating_sub(1))
                .is_some_and(|previous| !previous.is_alphanumeric())
        {
            score += 25;
        }
        previous_match = Some(index);
        match query_chars.next() {
            Some(next) => wanted = next,
            None => return Some(score - candidate.len() as i64),
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn empty_search_places_newest_at_bottom_and_selects_it() {
        let state = HistorySearchState::activate(
            vec!["newest".into(), "middle".into(), "oldest".into()],
            "draft".into(),
            false,
        );
        assert_eq!(
            state
                .matches()
                .iter()
                .map(|item| item.text.as_str())
                .collect::<Vec<_>>(),
            vec!["oldest", "middle", "newest"]
        );
        assert_eq!(state.selected_text(), Some("newest"));
        assert_eq!(state.saved_draft(), "draft");
    }

    #[test]
    fn browse_mode_starts_at_newest_and_exposes_newest_boundary() {
        let mut state = HistorySearchState::activate_browse(
            vec!["newest".into(), "oldest".into()],
            "draft".into(),
            false,
        );
        assert!(state.is_browse());
        assert_eq!(state.selected_text(), Some("newest"));
        assert!(state.is_newest_selected());
        state.move_older();
        assert_eq!(state.selected_text(), Some("oldest"));
        assert!(!state.is_newest_selected());
    }

    #[test]
    fn query_prefers_contiguous_matches_and_navigation_does_not_wrap() {
        let mut state = HistorySearchState::activate(
            vec![
                "fix provider journey".into(),
                "find prior job".into(),
                "other".into(),
            ],
            String::new(),
            false,
        );
        for character in "fpj".chars() {
            state.push(character);
        }
        assert_eq!(state.selected_text(), Some("find prior job"));
        state.move_newer();
        assert_eq!(state.selected_text(), Some("find prior job"));
        state.move_older();
        assert_eq!(state.selected_text(), Some("fix provider journey"));
    }

    #[test]
    fn unicode_query_and_backspace_are_safe() {
        let mut state = HistorySearchState::activate(
            vec!["修复历史搜索".into(), "其他任务".into()],
            String::new(),
            false,
        );
        state.push('历');
        assert_eq!(state.selected_text(), Some("修复历史搜索"));
        state.backspace();
        assert_eq!(state.query(), "");
        assert_eq!(state.matches().len(), 2);
    }
}
