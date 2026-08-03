use std::collections::{HashSet, VecDeque};

use crate::file_viewer::FileViewer;
use crate::product_projection::ProductProjection;
use crate::rpc::{
    AgentRecap, ContextSummary, GovernanceId, QuestionOption, SessionItemEvent, WireEvent,
};

#[derive(Debug, Clone)]
pub enum Overlay {
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
            Self::PlanReview(_)
            | Self::Settings(_)
            | Self::Status(_)
            | Self::Context(_)
            | Self::Help(_)
            | Self::Doctor(_)
            | Self::Agents(_)
            | Self::Changes(_)
            | Self::FileViewer(_)
            | Self::Queue(_) => None,
        }
    }
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

#[derive(Debug, Clone)]
pub struct PlanReviewOverlay {
    pub run_id: String,
    pub summary: String,
    pub resolving: bool,
    pub error: String,
    pub scroll: usize,
}

#[derive(Debug, Clone)]
pub struct SettingsOverlay {
    pub selected: usize,
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
    pub selected: usize,
    pub scroll: usize,
    pub load: RetainedLoad,
    pub confirm_rollback: bool,
    pub rollback_preview: Option<crate::rpc::PatchRollbackPreview>,
    pub rollback_error: String,
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
            self.push(overlay);
            return before != self.pending.len() + usize::from(self.active.is_some());
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
