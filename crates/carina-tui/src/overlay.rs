use std::collections::{HashSet, VecDeque};

use crate::component::Action;
use crate::file_viewer::FileViewer;
use crate::glyphs::GlyphPreference;
use crate::i18n::MessageId;
use crate::product_projection::ProductProjection;
use crate::rpc::{
    AgentRecap, ContextSummary, GovernanceId, QuestionOption, SessionItemEvent, WireEvent,
};

#[derive(Debug, Clone)]
pub enum Overlay {
    ProductMenu(ProductMenuOverlay),
    Approval(ApprovalOverlay),
    Question(QuestionOverlay),
    PlanReview(PlanReviewOverlay),
    Settings(SettingsOverlay),
    Status(StatusOverlay),
    Context(ContextSummary),
    Help(HelpOverlay),
    Doctor(crate::doctor::DoctorScreen),
    Agents(Box<AgentDashboardOverlay>),
    Changes(Box<ChangesOverlay>),
    FileViewer(FileViewer),
    ToolOutput(ToolOutputOverlay),
    Queue(QueueOverlay),
}

impl Overlay {
    pub fn from_event(event: &WireEvent) -> Option<Self> {
        match event.kind.as_str() {
            "permission.request" if !event.decision_id.is_empty() => {
                Some(Self::Approval(ApprovalOverlay {
                    decision_id: event.decision_id.clone(),
                    run_id: event.run_id.clone(),
                    capability: event.capability.clone(),
                    resource: event.resource.clone(),
                    reason: event.reason.clone(),
                    label: event.label.clone(),
                    diff: event.diff.clone(),
                    scope: ApprovalScope::Once,
                    resolving: false,
                    error: String::new(),
                    scroll: 0,
                }))
            }
            "user.question" if !event.question_id.is_empty() && !event.prompt.is_empty() => {
                Some(Self::Question(QuestionOverlay {
                    question_id: event.question_id.clone(),
                    run_id: event.run_id.clone(),
                    prompt: event.prompt.clone(),
                    options: event.options.clone(),
                    selected: 0,
                    input: String::new(),
                    resolving: false,
                    error: String::new(),
                }))
            }
            _ => None,
        }
    }

    pub fn is_governance(&self) -> bool {
        matches!(
            self,
            Self::Approval(_) | Self::Question(_) | Self::PlanReview(_)
        )
    }

    fn from_session_item(event: &SessionItemEvent) -> Option<Self> {
        let item = event.item.as_ref()?;
        if item.status != "requested" {
            return None;
        }
        let run_id = if item.run_id.is_empty() {
            event.run_id.clone()
        } else {
            item.run_id.clone()
        };
        match item.kind.as_str() {
            "approval" => {
                let decision_id =
                    item_detail(&item.details, "decision_id").unwrap_or_else(|| item.id.clone());
                (!decision_id.is_empty()).then(|| {
                    Self::Approval(ApprovalOverlay {
                        decision_id,
                        run_id,
                        capability: item_detail(&item.details, "capability").unwrap_or_default(),
                        resource: item_detail(&item.details, "resource").unwrap_or_default(),
                        reason: item_detail(&item.details, "reason").unwrap_or_default(),
                        label: item_detail(&item.details, "label").unwrap_or_default(),
                        diff: item_detail(&item.details, "diff").unwrap_or_default(),
                        scope: ApprovalScope::Once,
                        resolving: false,
                        error: String::new(),
                        scroll: 0,
                    })
                })
            }
            "question" => {
                let question_id =
                    item_detail(&item.details, "question_id").unwrap_or_else(|| item.id.clone());
                let prompt = item_detail(&item.details, "prompt").unwrap_or_default();
                (!question_id.is_empty() && !prompt.is_empty()).then(|| {
                    Self::Question(QuestionOverlay {
                        question_id,
                        run_id,
                        prompt,
                        options: question_options(&item.details),
                        selected: 0,
                        input: String::new(),
                        resolving: false,
                        error: String::new(),
                    })
                })
            }
            _ => None,
        }
    }

    fn governance_id(&self) -> Option<GovernanceId> {
        match self {
            Self::Approval(approval) => Some(GovernanceId::Approval(approval.decision_id.clone())),
            Self::Question(question) => Some(GovernanceId::Question(question.question_id.clone())),
            Self::ProductMenu(_)
            | Self::PlanReview(_)
            | Self::Settings(_)
            | Self::Status(_)
            | Self::Context(_)
            | Self::Help(_)
            | Self::Doctor(_)
            | Self::Agents(_)
            | Self::Changes(_)
            | Self::FileViewer(_)
            | Self::ToolOutput(_)
            | Self::Queue(_) => None,
        }
    }
}

#[derive(Debug, Clone)]
pub struct ProductMenuItem {
    pub id: &'static str,
    pub label: MessageId,
    pub action: Action,
}

pub const PRODUCT_MENU_ITEMS: [ProductMenuItem; 5] = [
    ProductMenuItem {
        id: "new",
        label: MessageId::NewConversation,
        action: Action::CreateSession,
    },
    ProductMenuItem {
        id: "sessions",
        label: MessageId::Conversations,
        action: Action::OpenSessions,
    },
    ProductMenuItem {
        id: "status",
        label: MessageId::Status,
        action: Action::OpenStatus,
    },
    ProductMenuItem {
        id: "settings",
        label: MessageId::Settings,
        action: Action::OpenSettings,
    },
    ProductMenuItem {
        id: "help",
        label: MessageId::Help,
        action: Action::OpenHelp,
    },
];

#[derive(Debug, Clone, Default)]
pub struct ProductMenuOverlay {
    pub selected: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ApprovalScope {
    Once,
    Session,
    Project,
}

impl ApprovalScope {
    pub const ALL: [Self; 3] = [Self::Once, Self::Session, Self::Project];

    pub fn as_str(self) -> &'static str {
        match self {
            Self::Once => "once",
            Self::Session => "session",
            Self::Project => "project",
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::Once => "Once",
            Self::Session => "Session",
            Self::Project => "Project",
        }
    }
}

#[derive(Debug, Clone)]
pub struct ApprovalOverlay {
    pub decision_id: String,
    pub run_id: String,
    pub capability: String,
    pub resource: String,
    pub reason: String,
    pub label: String,
    pub diff: String,
    pub scope: ApprovalScope,
    pub resolving: bool,
    pub error: String,
    pub scroll: usize,
}

#[derive(Debug, Clone)]
pub struct QuestionOverlay {
    pub question_id: String,
    pub run_id: String,
    pub prompt: String,
    pub options: Vec<QuestionOption>,
    pub selected: usize,
    pub input: String,
    pub resolving: bool,
    pub error: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PlanLineComment {
    /// One-based inclusive line anchor.
    pub start_line: usize,
    /// One-based inclusive line anchor.
    pub end_line: usize,
    pub text: String,
}

#[derive(Debug, Clone)]
pub struct PlanReviewOverlay {
    pub run_id: String,
    pub summary: String,
    pub resolving: bool,
    pub error: String,
    lines: Vec<String>,
    pub cursor: usize,
    pub scroll: usize,
    pub mark_start: Option<usize>,
    pub commenting: bool,
    pub comment_draft: String,
    comment_range: Option<(usize, usize)>,
    comments: Vec<PlanLineComment>,
}

impl PlanReviewOverlay {
    pub fn new(run_id: impl Into<String>, summary: impl Into<String>) -> Self {
        let summary = summary.into();
        let lines = summary.lines().map(str::to_owned).collect();
        Self {
            run_id: run_id.into(),
            summary,
            resolving: false,
            error: String::new(),
            lines,
            cursor: 0,
            scroll: 0,
            mark_start: None,
            commenting: false,
            comment_draft: String::new(),
            comment_range: None,
            comments: Vec::new(),
        }
    }

    pub fn lines(&self) -> &[String] {
        &self.lines
    }

    pub fn line_count(&self) -> usize {
        self.lines.len()
    }

    pub fn current_line(&self) -> Option<&str> {
        self.lines.get(self.cursor).map(String::as_str)
    }

    /// Returns the selected zero-based inclusive line range.
    pub fn selected_range(&self) -> Option<(usize, usize)> {
        let last = self.line_count().checked_sub(1)?;
        let cursor = self.cursor.min(last);
        let mark = self.mark_start.unwrap_or(cursor).min(last);
        Some((mark.min(cursor), mark.max(cursor)))
    }

    pub fn clamp(&mut self, viewport_height: usize) {
        let Some(last) = self.line_count().checked_sub(1) else {
            self.cursor = 0;
            self.scroll = 0;
            self.mark_start = None;
            return;
        };
        let viewport_height = viewport_height.max(1);
        self.cursor = self.cursor.min(last);
        self.mark_start = self.mark_start.map(|mark| mark.min(last));
        self.scroll = self
            .scroll
            .min(self.line_count().saturating_sub(viewport_height));
        if self.cursor < self.scroll {
            self.scroll = self.cursor;
        } else if self.cursor >= self.scroll.saturating_add(viewport_height) {
            self.scroll = self
                .cursor
                .saturating_add(1)
                .saturating_sub(viewport_height);
        }
    }

    pub fn move_up(&mut self, viewport_height: usize) {
        self.cursor = self.cursor.saturating_sub(1);
        self.clamp(viewport_height);
    }

    pub fn move_down(&mut self, viewport_height: usize) {
        self.cursor = self.cursor.saturating_add(1);
        self.clamp(viewport_height);
    }

    pub fn page_up(&mut self, viewport_height: usize) {
        self.cursor = self.cursor.saturating_sub(viewport_height.max(1));
        self.clamp(viewport_height);
    }

    pub fn page_down(&mut self, viewport_height: usize) {
        self.cursor = self.cursor.saturating_add(viewport_height.max(1));
        self.clamp(viewport_height);
    }

    pub fn scroll_up(&mut self, lines: usize) {
        self.scroll = self.scroll.saturating_sub(lines.max(1));
    }

    pub fn scroll_down(&mut self, lines: usize, viewport_height: usize) {
        let max_scroll = self.line_count().saturating_sub(viewport_height.max(1));
        self.scroll = self.scroll.saturating_add(lines.max(1)).min(max_scroll);
    }

    pub fn scroll_home(&mut self) {
        self.scroll = 0;
    }

    pub fn scroll_end(&mut self, viewport_height: usize) {
        self.scroll = self.line_count().saturating_sub(viewport_height.max(1));
    }

    pub fn home(&mut self, viewport_height: usize) {
        self.cursor = 0;
        self.clamp(viewport_height);
    }

    pub fn end(&mut self, viewport_height: usize) {
        self.cursor = self.line_count().saturating_sub(1);
        self.clamp(viewport_height);
    }

    pub fn toggle_mark(&mut self) -> bool {
        if self.lines.is_empty() {
            self.mark_start = None;
            return false;
        }
        if self.mark_start.is_some() {
            self.mark_start = None;
        } else {
            self.mark_start = Some(self.cursor.min(self.line_count() - 1));
        }
        self.mark_start.is_some()
    }

    pub fn line_is_marked(&self, line_index: usize) -> bool {
        self.mark_start.is_some()
            && self
                .selected_range()
                .is_some_and(|(start, end)| (start..=end).contains(&line_index))
    }

    pub fn begin_comment(&mut self) -> bool {
        let Some(range) = self.selected_range() else {
            return false;
        };
        self.commenting = true;
        self.comment_draft.clear();
        self.comment_range = Some(range);
        true
    }

    pub fn comment_range(&self) -> Option<(usize, usize)> {
        if self.commenting {
            self.comment_range
        } else {
            self.selected_range()
        }
    }

    pub fn append_comment(&mut self, text: &str) -> bool {
        if !self.commenting {
            return false;
        }
        self.comment_draft.push_str(text);
        true
    }

    pub fn push_comment_char(&mut self, character: char) -> bool {
        if !self.commenting {
            return false;
        }
        self.comment_draft.push(character);
        true
    }

    pub fn backspace_comment(&mut self) -> bool {
        self.commenting && self.comment_draft.pop().is_some()
    }

    pub fn cancel_comment(&mut self) {
        self.commenting = false;
        self.comment_draft.clear();
        self.comment_range = None;
    }

    pub fn commit_comment(&mut self) -> bool {
        if !self.commenting {
            return false;
        }
        let text = self.comment_draft.trim().to_owned();
        let Some((start, end)) = self.comment_range else {
            return false;
        };
        if text.is_empty() {
            return false;
        }
        self.commenting = false;
        self.comment_draft.clear();
        self.comment_range = None;
        self.comments.push(PlanLineComment {
            start_line: start + 1,
            end_line: end + 1,
            text,
        });
        self.mark_start = None;
        true
    }

    pub fn comment_count(&self) -> usize {
        self.comments.len()
    }

    pub fn comments_on_line(&self, line_index: usize) -> usize {
        let line_number = line_index.saturating_add(1);
        self.comments
            .iter()
            .filter(|comment| (comment.start_line..=comment.end_line).contains(&line_number))
            .count()
    }

    pub fn line_has_comments(&self, line_index: usize) -> bool {
        self.comments_on_line(line_index) > 0
    }

    pub fn revision_notes(&self) -> &[PlanLineComment] {
        &self.comments
    }
}

#[derive(Debug, Clone)]
pub struct SettingsOverlay {
    pub selected: usize,
    pub page: SettingsPage,
    pub symbol_selected: usize,
    pub original_preference: GlyphPreference,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum SettingsPage {
    #[default]
    Root,
    Symbols,
}

impl SettingsOverlay {
    pub fn root(preference: GlyphPreference) -> Self {
        Self {
            selected: 0,
            page: SettingsPage::Root,
            symbol_selected: GlyphPreference::ALL
                .iter()
                .position(|candidate| *candidate == preference)
                .unwrap_or_default(),
            original_preference: preference,
        }
    }

    pub fn symbols(preference: GlyphPreference) -> Self {
        Self {
            page: SettingsPage::Symbols,
            ..Self::root(preference)
        }
    }

    pub fn symbol_preference(&self) -> GlyphPreference {
        GlyphPreference::ALL[self
            .symbol_selected
            .min(GlyphPreference::ALL.len().saturating_sub(1))]
    }
}

#[derive(Debug, Clone)]
pub struct StatusOverlay {
    pub projection: ProductProjection,
}

/// Real `/help` surface (#22); not a slash-palette re-trigger.
#[derive(Debug, Clone)]
pub struct HelpOverlay {
    pub scroll: usize,
}

#[derive(Debug, Clone)]
pub struct AgentDashboardOverlay {
    pub projection: ProductProjection,
    pub selected: usize,
    pub recap: Option<AgentRecap>,
    pub load: RetainedLoad,
    pub recap_load: RetainedLoad,
    pub confirm_stop: bool,
}

#[derive(Debug, Clone)]
pub struct ChangesOverlay {
    pub projection: ProductProjection,
    pub(crate) patch_reviews: Vec<crate::patch_review::PatchReview>,
    pub selected: usize,
    pub selected_file: usize,
    pub focus: ChangesFocus,
    pub scroll: usize,
    pub load: RetainedLoad,
    pub confirm_rollback: bool,
    pub rollback_preview: Option<crate::rpc::PatchRollbackPreview>,
    pub rollback_error: String,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum ChangesFocus {
    #[default]
    Transactions,
    Files,
}

#[derive(Debug, Clone)]
pub struct QueueOverlay {
    pub run_id: String,
    pub items: Vec<crate::rpc::QueueItem>,
    pub selected: usize,
    pub soft_interrupt_pending: bool,
    pub load: RetainedLoad,
    pub error: String,
}

#[derive(Debug, Clone)]
pub struct ToolOutputOverlay {
    pub block_id: String,
    pub scroll: usize,
}

#[derive(Debug, Clone, Default)]
pub struct RetainedLoad {
    pub generation: u64,
    pub session_id: String,
    pub loading: bool,
    pub error: String,
}

impl RetainedLoad {
    pub fn begin(generation: u64, session_id: String) -> Self {
        Self {
            generation,
            session_id,
            loading: true,
            error: String::new(),
        }
    }

    pub fn refresh(&mut self, generation: u64, session_id: String) {
        *self = Self::begin(generation, session_id);
    }

    pub fn accepts(&self, generation: u64, session_id: &str) -> bool {
        self.loading && self.generation == generation && self.session_id == session_id
    }

    pub fn finish(&mut self, error: Option<String>) {
        self.loading = false;
        self.error = error.unwrap_or_default();
    }
}

#[derive(Debug, Default)]
pub struct OverlayStack {
    active: Option<Overlay>,
    pending: VecDeque<Overlay>,
    resolved: HashSet<GovernanceId>,
}

impl OverlayStack {
    pub fn hydrate_governance(items: &[SessionItemEvent]) -> Self {
        let mut stack = Self::default();
        for event in items {
            let Some(item) = event.item.as_ref() else {
                continue;
            };
            let id = match item.kind.as_str() {
                "approval" => item_detail(&item.details, "decision_id")
                    .or_else(|| (!item.id.is_empty()).then(|| item.id.clone()))
                    .map(GovernanceId::Approval),
                "question" => item_detail(&item.details, "question_id")
                    .or_else(|| (!item.id.is_empty()).then(|| item.id.clone()))
                    .map(GovernanceId::Question),
                _ => None,
            };
            if item.status == "resolved" {
                if let Some(id) = id {
                    stack.resolve_by_id(id);
                }
            } else if let Some(overlay) = Overlay::from_session_item(event) {
                stack.push(overlay);
            }
        }
        stack
    }

    pub fn active(&self) -> Option<&Overlay> {
        self.active.as_ref()
    }

    pub fn active_mut(&mut self) -> Option<&mut Overlay> {
        self.active.as_mut()
    }

    pub fn governance_queue_len(&self) -> usize {
        self.active
            .iter()
            .chain(self.pending.iter())
            .filter(|overlay| overlay.is_governance())
            .count()
    }

    pub fn governance_ids(&self) -> Vec<GovernanceId> {
        self.active
            .iter()
            .chain(self.pending.iter())
            .filter_map(Overlay::governance_id)
            .collect()
    }

    pub fn push(&mut self, overlay: Overlay) {
        if let Overlay::PlanReview(review) = &overlay
            && self
                .active
                .iter()
                .chain(self.pending.iter())
                .any(|candidate| {
                    matches!(candidate, Overlay::PlanReview(active) if active.run_id == review.run_id)
                })
        {
            return;
        }
        if let Some(id) = overlay.governance_id()
            && (self.resolved.contains(&id)
                || self
                    .active
                    .as_ref()
                    .and_then(Overlay::governance_id)
                    .as_ref()
                    == Some(&id)
                || self
                    .pending
                    .iter()
                    .filter_map(Overlay::governance_id)
                    .any(|pending| pending == id))
        {
            return;
        }
        if overlay.is_governance() && matches!(self.active, Some(Overlay::ProductMenu(_))) {
            self.active = Some(overlay);
            return;
        }
        if self.active.is_some() {
            self.pending.push_back(overlay);
        } else {
            self.active = Some(overlay);
        }
    }

    pub fn replace(&mut self, overlay: Overlay) {
        self.active = Some(overlay);
    }

    pub fn resolve_active(&mut self) {
        self.active = self.pending.pop_front();
    }

    pub fn resolve_by_id(&mut self, id: GovernanceId) -> bool {
        self.resolved.insert(id.clone());
        if self
            .active
            .as_ref()
            .and_then(Overlay::governance_id)
            .as_ref()
            == Some(&id)
        {
            self.resolve_active();
            return true;
        }
        let previous_len = self.pending.len();
        self.pending
            .retain(|overlay| overlay.governance_id().as_ref() != Some(&id));
        previous_len != self.pending.len()
    }

    pub fn reconcile_event(&mut self, event: &WireEvent) -> bool {
        if let Some(id) = event.governance_resolution() {
            return self.resolve_by_id(id);
        }
        if let Some(overlay) = Overlay::from_event(event) {
            let before = self.pending.len() + usize::from(self.active.is_some());
            let preempts_product_menu =
                overlay.is_governance() && matches!(self.active, Some(Overlay::ProductMenu(_)));
            self.push(overlay);
            return preempts_product_menu
                || before != self.pending.len() + usize::from(self.active.is_some());
        }
        false
    }

    pub fn close_non_governance(&mut self) -> bool {
        if self.active.as_ref().is_some_and(Overlay::is_governance) {
            return false;
        }
        let had_active = self.active.is_some();
        self.resolve_active();
        had_active
    }
}

fn item_detail(
    details: &std::collections::BTreeMap<String, serde_json::Value>,
    key: &str,
) -> Option<String> {
    details
        .get(key)
        .and_then(serde_json::Value::as_str)
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
}

fn question_options(
    details: &std::collections::BTreeMap<String, serde_json::Value>,
) -> Vec<QuestionOption> {
    details
        .get("options")
        .and_then(serde_json::Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|option| match option {
            serde_json::Value::String(value) => Some(QuestionOption {
                label: value.clone(),
                value: value.clone(),
                description: String::new(),
            }),
            serde_json::Value::Object(option) => {
                let value = option.get("value").and_then(serde_json::Value::as_str)?;
                let label = option
                    .get("label")
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or(value);
                Some(QuestionOption {
                    label: label.to_owned(),
                    value: value.to_owned(),
                    description: option
                        .get("description")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default()
                        .to_owned(),
                })
            }
            _ => None,
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use super::*;
    use crate::rpc::SessionItem;

    #[test]
    fn governance_preempts_the_product_menu() {
        let mut stack = OverlayStack::default();
        stack.replace(Overlay::ProductMenu(ProductMenuOverlay::default()));
        stack.push(Overlay::PlanReview(PlanReviewOverlay::new(
            "run-1", "review",
        )));
        assert!(matches!(stack.active(), Some(Overlay::PlanReview(_))));
        assert_eq!(stack.pending.len(), 0);
    }

    #[test]
    fn plan_review_records_single_and_range_comments_with_one_based_anchors() {
        let mut review = PlanReviewOverlay::new("run-1", "inspect\nchange\nverify");
        assert_eq!(review.line_count(), 3);
        assert_eq!(review.current_line(), Some("inspect"));

        review.move_down(2);
        assert!(review.begin_comment());
        assert!(review.append_comment("explain the change"));
        assert!(review.commit_comment());

        review.end(2);
        assert!(review.toggle_mark());
        review.home(2);
        assert_eq!(review.selected_range(), Some((0, 2)));
        assert!(review.begin_comment());
        assert!(review.append_comment("cover the full sequence"));
        assert!(review.commit_comment());

        assert_eq!(
            review.revision_notes(),
            [
                PlanLineComment {
                    start_line: 2,
                    end_line: 2,
                    text: "explain the change".into(),
                },
                PlanLineComment {
                    start_line: 1,
                    end_line: 3,
                    text: "cover the full sequence".into(),
                },
            ]
        );
        assert_eq!(review.comment_count(), 2);
        assert_eq!(review.comments_on_line(0), 1);
        assert_eq!(review.comments_on_line(1), 2);
        assert!(review.line_has_comments(2));
        assert!(!review.line_has_comments(3));
    }

    #[test]
    fn plan_review_comment_editing_preserves_unicode_scalars() {
        let mut review = PlanReviewOverlay::new("run-cjk", "检查方案\n运行测试");
        assert!(review.begin_comment());
        assert_eq!(review.comment_range(), Some((0, 0)));
        assert!(review.append_comment("请补充验证"));
        assert!(review.push_comment_char('。'));
        assert!(review.push_comment_char('界'));
        assert!(review.backspace_comment());
        assert_eq!(review.comment_draft, "请补充验证。");
        review.move_down(1);
        assert_eq!(review.comment_range(), Some((0, 0)));
        assert!(review.commit_comment());
        assert_eq!(review.comment_range(), Some((1, 1)));
        assert_eq!(review.revision_notes()[0].text, "请补充验证。");
        assert_eq!(review.revision_notes()[0].start_line, 1);
    }

    #[test]
    fn plan_review_empty_and_cancelled_drafts_do_not_add_comments() {
        let mut review = PlanReviewOverlay::new("run-1", "one line");
        assert!(review.begin_comment());
        assert!(review.append_comment(" \t "));
        assert!(!review.commit_comment());
        assert_eq!(review.comment_count(), 0);
        assert!(review.commenting);
        assert_eq!(review.comment_draft, " \t ");

        review.cancel_comment();
        assert!(review.begin_comment());
        assert!(review.append_comment("discard this"));
        review.cancel_comment();
        assert_eq!(review.comment_count(), 0);
        assert!(!review.commenting);
        assert!(review.comment_draft.is_empty());

        let mut empty = PlanReviewOverlay::new("run-empty", "");
        assert_eq!(empty.line_count(), 0);
        assert_eq!(empty.current_line(), None);
        assert!(!empty.begin_comment());
        assert!(!empty.toggle_mark());
    }

    #[test]
    fn plan_review_navigation_clamps_cursor_scroll_and_mark() {
        let mut review = PlanReviewOverlay::new("run-1", "one\ntwo\nthree\nfour\nfive");
        review.end(2);
        assert_eq!((review.cursor, review.scroll), (4, 3));
        review.move_down(2);
        assert_eq!((review.cursor, review.scroll), (4, 3));
        review.page_up(2);
        assert_eq!((review.cursor, review.scroll), (2, 2));
        review.page_down(2);
        assert_eq!((review.cursor, review.scroll), (4, 3));
        review.home(2);
        assert_eq!((review.cursor, review.scroll), (0, 0));

        review.scroll_down(2, 2);
        assert_eq!((review.cursor, review.scroll), (0, 2));
        review.scroll_up(1);
        assert_eq!(review.scroll, 1);
        review.scroll_end(2);
        assert_eq!(review.scroll, 3);
        review.scroll_home();
        assert_eq!(review.scroll, 0);

        review.cursor = usize::MAX;
        review.scroll = usize::MAX;
        review.mark_start = Some(usize::MAX);
        review.clamp(2);
        assert_eq!((review.cursor, review.scroll), (4, 3));
        assert_eq!(review.mark_start, Some(4));
        assert!(review.line_is_marked(4));
    }

    #[test]
    fn plan_review_stack_deduplicates_by_run_id_without_replacing_state() {
        let mut stack = OverlayStack::default();
        let mut original = PlanReviewOverlay::new("run-1", "original");
        assert!(original.begin_comment());
        assert!(original.append_comment("keep me"));
        assert!(original.commit_comment());
        stack.push(Overlay::PlanReview(original));
        stack.push(Overlay::PlanReview(PlanReviewOverlay::new(
            "run-1",
            "duplicate",
        )));
        stack.push(Overlay::PlanReview(PlanReviewOverlay::new("run-2", "next")));

        assert_eq!(stack.governance_queue_len(), 2);
        let Some(Overlay::PlanReview(active)) = stack.active() else {
            panic!("the first review must remain active");
        };
        assert_eq!(active.summary, "original");
        assert_eq!(active.comment_count(), 1);

        stack.resolve_active();
        let Some(Overlay::PlanReview(active)) = stack.active() else {
            panic!("the distinct review must remain queued");
        };
        assert_eq!(active.run_id, "run-2");
    }

    #[test]
    fn retained_load_fences_generation_and_target_and_keeps_failure_visible() {
        let mut load = RetainedLoad::begin(4, "session-a".into());
        assert!(load.accepts(4, "session-a"));
        assert!(!load.accepts(3, "session-a"));
        assert!(!load.accepts(4, "session-b"));

        load.finish(Some("offline".into()));
        assert!(!load.loading);
        assert_eq!(load.error, "offline");
        assert!(!load.accepts(4, "session-a"));

        load.refresh(5, "session-b".into());
        assert!(load.loading);
        assert!(load.error.is_empty());
        assert!(load.accepts(5, "session-b"));
    }

    fn event(kind: &str) -> WireEvent {
        WireEvent {
            event_id: String::new(),
            session_id: "s1".into(),
            run_id: "t1".into(),
            agent: String::new(),
            kind: kind.into(),
            actor: String::new(),
            timestamp: String::new(),
            payload: BTreeMap::new(),
            permission_decision_id: String::new(),
            raw_cursor: 0,
            status: String::new(),
            summary: String::new(),
            result_kind: String::new(),
            decision_id: "perm_1".into(),
            capability: "CommandExec".into(),
            resource: "git status".into(),
            reason: "policy".into(),
            label: "Run command".into(),
            diff: String::new(),
            question_id: String::new(),
            prompt: String::new(),
            options: Vec::new(),
        }
    }

    #[test]
    fn governance_queue_never_replaces_the_visible_request() {
        let mut stack = OverlayStack::default();
        stack.push(Overlay::from_event(&event("permission.request")).unwrap());
        let mut second = event("permission.request");
        second.decision_id = "perm_2".into();
        stack.push(Overlay::from_event(&second).unwrap());
        assert_eq!(stack.governance_queue_len(), 2);

        let Some(Overlay::Approval(active)) = stack.active() else {
            panic!("approval must remain active");
        };
        assert_eq!(active.decision_id, "perm_1");
        stack.resolve_active();
        let Some(Overlay::Approval(active)) = stack.active() else {
            panic!("second approval must advance after resolution");
        };
        assert_eq!(active.decision_id, "perm_2");
        assert_eq!(stack.governance_queue_len(), 1);
    }

    #[test]
    fn request_then_resolution_leaves_no_overlay() {
        let mut stack = OverlayStack::default();
        stack.reconcile_event(&event("permission.request"));
        stack.reconcile_event(&approval_resolution("perm_1"));
        assert!(stack.active().is_none());
    }

    #[test]
    fn resolving_active_request_advances_the_queue() {
        let mut stack = OverlayStack::default();
        stack.reconcile_event(&event("permission.request"));
        let mut second = event("permission.request");
        second.decision_id = "perm_2".into();
        stack.reconcile_event(&second);
        stack.reconcile_event(&approval_resolution("perm_1"));
        let Some(Overlay::Approval(active)) = stack.active() else {
            panic!("queued approval must advance");
        };
        assert_eq!(active.decision_id, "perm_2");
    }

    #[test]
    fn resolving_queued_request_removes_only_that_request() {
        let mut stack = OverlayStack::default();
        stack.reconcile_event(&event("permission.request"));
        let mut second = event("permission.request");
        second.decision_id = "perm_2".into();
        stack.reconcile_event(&second);
        stack.reconcile_event(&approval_resolution("perm_2"));
        let Some(Overlay::Approval(active)) = stack.active() else {
            panic!("first approval must remain active");
        };
        assert_eq!(active.decision_id, "perm_1");
        stack.resolve_active();
        assert!(stack.active().is_none());
    }

    #[test]
    fn replay_does_not_reopen_resolved_governance() {
        let mut stack = OverlayStack::default();
        stack.reconcile_event(&event("permission.request"));
        stack.reconcile_event(&approval_resolution("perm_1"));
        stack.reconcile_event(&event("permission.request"));
        assert!(stack.active().is_none());
    }

    #[test]
    fn session_items_restore_only_unresolved_governance_in_fifo_order() {
        let items = vec![
            governance_item(
                "approval",
                "perm_1",
                "requested",
                BTreeMap::from([
                    ("decision_id".into(), "perm_1".into()),
                    ("capability".into(), "CommandExec".into()),
                    ("resource".into(), "git status".into()),
                ]),
            ),
            governance_item(
                "question",
                "question_1",
                "requested",
                BTreeMap::from([
                    ("question_id".into(), "question_1".into()),
                    ("prompt".into(), "Continue?".into()),
                    (
                        "options".into(),
                        serde_json::json!(["yes", {"label": "No", "value": "no"}]),
                    ),
                ]),
            ),
            governance_item(
                "approval",
                "perm_1",
                "resolved",
                BTreeMap::from([("decision_id".into(), "perm_1".into())]),
            ),
        ];

        let stack = OverlayStack::hydrate_governance(&items);
        let Some(Overlay::Question(question)) = stack.active() else {
            panic!("the unresolved question must own input after restart");
        };
        assert_eq!(question.question_id, "question_1");
        assert_eq!(question.options.len(), 2);
        assert_eq!(question.options[0].value, "yes");
        assert_eq!(question.options[1].label, "No");
    }

    #[test]
    fn resolved_item_prevents_a_replayed_request_from_reopening() {
        let items = vec![
            governance_item(
                "approval",
                "perm_1",
                "resolved",
                BTreeMap::from([("decision_id".into(), "perm_1".into())]),
            ),
            governance_item(
                "approval",
                "perm_1",
                "requested",
                BTreeMap::from([("decision_id".into(), "perm_1".into())]),
            ),
        ];
        assert!(OverlayStack::hydrate_governance(&items).active().is_none());
    }

    fn approval_resolution(id: &str) -> WireEvent {
        WireEvent {
            kind: "ToolApproved".into(),
            payload: BTreeMap::from([("decision_id".into(), id.into())]),
            ..WireEvent::default()
        }
    }

    fn governance_item(
        kind: &str,
        id: &str,
        status: &str,
        details: BTreeMap<String, serde_json::Value>,
    ) -> SessionItemEvent {
        SessionItemEvent {
            kind: "item.updated".into(),
            session_id: "sess_1".into(),
            turn_id: "turn_1".into(),
            run_id: "run_1".into(),
            item_id: id.into(),
            source_event_id: String::new(),
            timestamp: String::new(),
            details: BTreeMap::new(),
            item: Some(SessionItem {
                id: id.into(),
                kind: kind.into(),
                status: status.into(),
                run_id: "run_1".into(),
                details,
            }),
        }
    }
}
