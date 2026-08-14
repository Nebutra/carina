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
        let mut visible = providers
            .iter()
            .enumerate()
            .filter_map(|(index, provider)| {
                let provider_match = matches_query(&provider.id, &query)
                    || matches_query(&provider.name, &query)
                    || matches_query(&provider.source_label, &query);
                let model_matches = provider
                    .models
                    .iter()
                    .filter(|model| {
                        matches_query(&model.id, &query)
                            || matches_query(&model.display_id, &query)
                            || matches_query(&model.name, &query)
                    })
                    .count();
                (provider_match || model_matches > 0).then_some((
                    index,
                    provider_match,
                    model_matches,
                    provider.models.len(),
                ))
            })
            .collect::<Vec<_>>();
        visible.sort_by(
            |(left_index, left_provider, left_matches, left_total),
             (right_index, right_provider, right_matches, right_total)| {
                match right_provider.cmp(left_provider) {
                    std::cmp::Ordering::Equal if *left_provider => left_index.cmp(right_index),
                    std::cmp::Ordering::Equal => ((*right_matches as u128) * (*left_total as u128))
                        .cmp(&((*left_matches as u128) * (*right_total as u128)))
                        .then_with(|| right_matches.cmp(left_matches))
                        .then_with(|| left_index.cmp(right_index)),
                    ordering => ordering,
                }
            },
        );
        visible.into_iter().map(|(index, _, _, _)| index).collect()
    }

    pub fn normalize_selection(&self, providers: &[ModelProvider], selected: usize) -> usize {
        self.visible_indices(providers)
            .first()
            .copied()
            .unwrap_or(selected)
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

fn matches_query(value: &str, query: &str) -> bool {
    value.to_lowercase().contains(query)
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
            source_credential_owner: String::new(),
            source_action: String::new(),
            source_current: false,
            source_importable: false,
            source_reason: String::new(),
            models: Vec::new(),
        }
    }

    fn model(id: &str, display_id: &str, name: &str) -> crate::rpc::Model {
        crate::rpc::Model {
            id: id.into(),
            display_id: display_id.into(),
            name: name.into(),
            available: true,
            status: String::new(),
            status_reason: String::new(),
            reasoning: false,
            reasoning_efforts: Vec::new(),
            default_reasoning_effort: String::new(),
            image_input: false,
            tool_call: false,
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
        providers[1].models = vec![model("open-1", "open-1", "Open Model")];
        providers[2].models = vec![
            model("open-1", "open-1", "Open Model 1"),
            model("open-2", "open-2", "Open Model 2"),
        ];
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
    fn provider_search_matches_models_and_prioritizes_their_primary_catalog() {
        let mut providers = vec![
            provider("openrouter", "OpenRouter"),
            provider("xai", "xAI"),
            provider("unrelated", "Unrelated"),
        ];
        providers[0].models = vec![
            model("openrouter/x-ai/grok-4.6", "x-ai/grok-4.6", "Grok 4.6"),
            model("openrouter/openai/gpt-5", "openai/gpt-5", "GPT-5"),
        ];
        providers[1].models = vec![
            model("xai/grok-4.5", "grok-4.5", "Grok 4.5"),
            model("xai/grok-4.6", "grok-4.6", "Grok 4.6"),
        ];

        for query in ["grok", "x-ai/grok-4.6", "Grok 4.5"] {
            let mut search = ProviderPickerState::default();
            search.begin_search();
            for character in query.chars() {
                search.push(character);
            }
            let expected = if query == "Grok 4.5" {
                vec![1]
            } else if query == "x-ai/grok-4.6" {
                vec![0]
            } else {
                vec![1, 0]
            };
            assert_eq!(search.visible_indices(&providers), expected, "{query}");
            if query == "grok" {
                assert_eq!(search.normalize_selection(&providers, 0), 1);
            }
        }

        let mut direct = ProviderPickerState::default();
        direct.begin_search();
        for character in "openrouter".chars() {
            direct.push(character);
        }
        assert_eq!(direct.visible_indices(&providers), vec![0]);
    }

    #[test]
    fn grok_search_keeps_subscription_and_api_billing_routes_distinct() {
        let mut providers = vec![
            provider("xai", "xAI"),
            provider("grok-build", "Grok Build"),
            provider("openrouter", "OpenRouter"),
        ];
        providers[0].models = vec![model("xai/grok-4.6", "grok-4.6", "Grok 4.6")];
        providers[1].registered = true;
        providers[1].available = true;
        providers[1].source_kind = "grok-build".into();
        providers[1].source_label = "Grok Build".into();
        providers[1].models = vec![model("grok-build/grok-4.6", "grok-4.6", "grok-4.6")];

        let mut search = ProviderPickerState::default();
        search.begin_search();
        for character in "grok".chars() {
            search.push(character);
        }

        assert_eq!(search.visible_indices(&providers), vec![1, 0]);
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
