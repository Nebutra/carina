use ratatui::layout::{Constraint, Direction, Layout, Rect};

use crate::layout_contract as layout;
use crate::product_header::product_header_height;
use crate::rpc::ModelProvider;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct PrerequisiteLayout {
    pub header: Rect,
    pub content: Rect,
    pub footer: Rect,
}

impl PrerequisiteLayout {
    pub fn compute(area: Rect) -> Self {
        let canvas = layout::canvas(area);
        let header_height = product_header_height(area);
        let chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(header_height),
                Constraint::Length(layout::SCENE_HEADER_GAP),
                Constraint::Min(layout::SCENE_CONTENT_MIN_HEIGHT),
                Constraint::Length(layout::scene_footer_height(area.height)),
            ])
            .split(canvas);
        Self {
            header: chunks[0],
            content: chunks[2],
            footer: chunks[3],
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct BrowserLayout {
    pub title: Rect,
    pub list: Rect,
    pub detail: Option<Rect>,
}

impl BrowserLayout {
    pub fn compute(area: Rect) -> Self {
        let vertical = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(layout::BROWSER_TITLE_HEIGHT),
                Constraint::Min(layout::BROWSER_MIN_BODY_HEIGHT),
            ])
            .split(area);
        if !layout::browser_has_detail(vertical[1].width) {
            return Self {
                title: vertical[0],
                list: vertical[1],
                detail: None,
            };
        }
        let columns = Layout::default()
            .direction(Direction::Horizontal)
            .constraints([
                Constraint::Percentage(64),
                Constraint::Length(layout::COLUMN_GAP),
                Constraint::Min(layout::BROWSER_DETAIL_MIN_WIDTH),
            ])
            .split(vertical[1]);
        Self {
            title: vertical[0],
            list: columns[0],
            detail: Some(columns[2]),
        }
    }

    pub fn with_compact_detail(self) -> (Rect, Option<Rect>) {
        if self.detail.is_some() || self.list.height < layout::COMPACT_DETAIL_MIN_HEIGHT {
            return (self.list, None);
        }
        let [list, detail] = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Min(layout::COMPACT_DETAIL_LIST_MIN_HEIGHT),
                Constraint::Length(layout::COMPACT_DETAIL_HEIGHT),
            ])
            .areas(self.list);
        (list, Some(detail))
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct PickerWindow {
    pub start: usize,
    pub len: usize,
}

impl PickerWindow {
    pub fn around(total: usize, selected: usize, capacity: usize) -> Self {
        let len = total.min(capacity);
        let start = selected
            .saturating_sub(capacity / 2)
            .min(total.saturating_sub(len));
        Self { start, len }
    }
}

#[derive(Debug, Default)]
pub struct ProviderPickerState {
    query: String,
    search_active: bool,
}

impl ProviderPickerState {
    pub fn query(&self) -> &str {
        &self.query
    }

    pub fn search_active(&self) -> bool {
        self.search_active
    }

    pub fn begin_search(&mut self) {
        self.search_active = true;
    }

    pub fn push(&mut self, character: char) {
        self.query.push(character);
    }

    pub fn backspace(&mut self) {
        self.query.pop();
    }

    pub fn cancel_search(&mut self) -> bool {
        if self.search_active || !self.query.is_empty() {
            self.search_active = false;
            self.query.clear();
            true
        } else {
            false
        }
    }

    pub fn visible_indices(&self, providers: &[ModelProvider]) -> Vec<usize> {
        let query = self.query.trim().to_lowercase();
        if query.is_empty() {
            let mut visible = providers
                .iter()
                .enumerate()
                .filter(|(index, provider)| {
                    *index < 4
                        || provider.registered
                        || provider.available
                        || provider.source_current
                        || provider.source_importable
                })
                .map(|(index, _)| index)
                .collect::<Vec<_>>();
            visible.sort_by_key(|index| {
                let provider = &providers[*index];
                (
                    std::cmp::Reverse(provider.source_current),
                    std::cmp::Reverse(provider.source_importable),
                    std::cmp::Reverse(provider.available),
                    std::cmp::Reverse(provider.registered),
                    *index,
                )
            });
            return visible;
        }
        providers
            .iter()
            .enumerate()
            .filter(|(_, provider)| {
                query.is_empty()
                    || provider.id.to_lowercase().contains(&query)
                    || provider.name.to_lowercase().contains(&query)
                    || provider.source_label.to_lowercase().contains(&query)
            })
            .map(|(index, _)| index)
            .collect()
    }

    pub fn normalize_selection(&self, providers: &[ModelProvider], selected: usize) -> usize {
        let visible = self.visible_indices(providers);
        if visible.contains(&selected) {
            selected
        } else {
            visible.first().copied().unwrap_or(0)
        }
    }

    pub fn move_selection(
        &self,
        providers: &[ModelProvider],
        selected: usize,
        down: bool,
    ) -> usize {
        let visible = self.visible_indices(providers);
        let current = visible
            .iter()
            .position(|index| *index == selected)
            .unwrap_or(0);
        let next = if down {
            (current + 1).min(visible.len().saturating_sub(1))
        } else {
            current.saturating_sub(1)
        };
        visible.get(next).copied().unwrap_or(selected)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn provider(id: &str, name: &str) -> ModelProvider {
        ModelProvider {
            id: id.into(),
            name: name.into(),
            registered: false,
            available: false,
            auth_source: String::new(),
            source_kind: String::new(),
            source_label: String::new(),
            source_app: String::new(),
            source_route: String::new(),
            source_auth_mode: String::new(),
            source_action: String::new(),
            source_current: false,
            source_importable: false,
            source_reason: String::new(),
            models: Vec::new(),
        }
    }

    #[test]
    fn wide_layout_uses_the_viewport_instead_of_a_fixed_centered_shell() {
        let layout = PrerequisiteLayout::compute(Rect::new(0, 0, 190, 52));
        assert_eq!(layout.content.width, layout::PRODUCT_MAX_WIDTH);
        assert!(layout.content.height > 40);
        assert!(BrowserLayout::compute(layout.content).detail.is_some());

        let ultrawide = PrerequisiteLayout::compute(Rect::new(0, 0, 244, 71));
        assert_eq!(ultrawide.content.width, layout::PRODUCT_MAX_WIDTH);
        assert_eq!(ultrawide.content.x, 32);
    }

    #[test]
    fn narrow_layout_collapses_to_one_column() {
        let layout = PrerequisiteLayout::compute(Rect::new(0, 0, 70, 22));
        assert!(BrowserLayout::compute(layout.content).detail.is_none());
    }

    #[test]
    fn narrow_provider_layout_retains_a_compact_detail_action() {
        let browser = BrowserLayout::compute(Rect::new(2, 6, 76, 28));
        let (list, detail) = browser.with_compact_detail();
        let detail = detail.expect("narrow provider detail");
        assert_eq!(list.width, 76);
        assert_eq!(detail.width, 76);
        assert_eq!(detail.height, 2);
        assert_eq!(list.bottom(), detail.y);
    }

    #[test]
    fn provider_search_and_navigation_share_one_visible_index_map() {
        let mut providers = vec![
            provider("anthropic", "Anthropic"),
            provider("openai", "OpenAI"),
            provider("openrouter", "OpenRouter"),
        ];
        let mut state = ProviderPickerState::default();
        state.begin_search();
        for character in "open".chars() {
            state.push(character);
        }
        assert_eq!(state.visible_indices(&providers), vec![1, 2]);
        assert_eq!(state.normalize_selection(&providers, 0), 1);
        assert_eq!(state.move_selection(&providers, 1, true), 2);

        providers[0].source_label = "CC Switch".into();
        let mut source_search = ProviderPickerState::default();
        source_search.begin_search();
        for character in "switch".chars() {
            source_search.push(character);
        }
        assert_eq!(source_search.visible_indices(&providers), vec![0]);
    }

    #[test]
    fn empty_provider_query_prioritizes_actionable_and_recommended_choices() {
        let mut providers = (0..12)
            .map(|index| provider(&format!("provider-{index}"), &format!("Provider {index}")))
            .collect::<Vec<_>>();
        providers[9].source_importable = true;
        providers[10].source_current = true;
        providers[11].available = true;

        assert_eq!(
            ProviderPickerState::default().visible_indices(&providers),
            vec![10, 9, 11, 0, 1, 2, 3]
        );

        let mut search = ProviderPickerState::default();
        search.begin_search();
        for character in "provider-8".chars() {
            search.push(character);
        }
        assert_eq!(search.visible_indices(&providers), vec![8]);
    }

    #[test]
    fn picker_window_keeps_selection_visible_without_dumping_the_inventory() {
        assert_eq!(
            PickerWindow::around(100, 50, 16),
            PickerWindow { start: 42, len: 16 }
        );
        assert_eq!(
            PickerWindow::around(5, 4, 16),
            PickerWindow { start: 0, len: 5 }
        );
    }
}
