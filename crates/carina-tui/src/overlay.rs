use std::collections::{HashSet, VecDeque};

use crate::rpc::{
    Checkpoint, CheckpointPreview, CheckpointRestoreResult, GovernanceId, QuestionOption, WireEvent,
};

#[derive(Debug, Clone)]
pub enum Overlay {
    Approval(ApprovalOverlay),
    Question(QuestionOverlay),
    Checkpoint(CheckpointOverlay),
    Settings(SettingsOverlay),
}

impl Overlay {
    pub fn from_event(event: &WireEvent) -> Option<Self> {
        match event.kind.as_str() {
            "permission.request" if !event.decision_id.is_empty() => {
                Some(Self::Approval(ApprovalOverlay {
                    decision_id: event.decision_id.clone(),
                    task_id: event.task_id.clone(),
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
                    task_id: event.task_id.clone(),
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
        matches!(self, Self::Approval(_) | Self::Question(_))
    }

    fn governance_id(&self) -> Option<GovernanceId> {
        match self {
            Self::Approval(approval) => Some(GovernanceId::Approval(approval.decision_id.clone())),
            Self::Question(question) => Some(GovernanceId::Question(question.question_id.clone())),
            Self::Checkpoint(_) | Self::Settings(_) => None,
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
    pub task_id: String,
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
    pub task_id: String,
    pub prompt: String,
    pub options: Vec<QuestionOption>,
    pub selected: usize,
    pub input: String,
    pub resolving: bool,
    pub error: String,
}

#[derive(Debug, Clone)]
pub struct SettingsOverlay {
    pub selected: usize,
}

#[derive(Debug, Clone)]
pub struct CheckpointOverlay {
    pub session_id: String,
    pub checkpoints: Vec<Checkpoint>,
    pub selected: usize,
    pub step: CheckpointStep,
    pub error: String,
}

impl CheckpointOverlay {
    pub fn loading(session_id: String) -> Self {
        Self {
            session_id,
            checkpoints: Vec::new(),
            selected: 0,
            step: CheckpointStep::LoadingList,
            error: String::new(),
        }
    }

    pub fn loaded(session_id: String, checkpoints: Vec<Checkpoint>) -> Self {
        let selected = checkpoints.len().saturating_sub(1);
        Self {
            session_id,
            checkpoints,
            selected,
            step: CheckpointStep::List,
            error: String::new(),
        }
    }

    pub fn selected_checkpoint(&self) -> Option<&Checkpoint> {
        self.checkpoints.get(self.selected)
    }

    pub fn select(&mut self, index: usize) {
        if index < self.checkpoints.len() {
            self.selected = index;
            self.error.clear();
        }
    }

    pub fn back(&mut self) -> bool {
        self.error.clear();
        match &self.step {
            CheckpointStep::LoadingList | CheckpointStep::List => false,
            CheckpointStep::Previewing { .. } => {
                self.step = CheckpointStep::List;
                true
            }
            CheckpointStep::Preview(_) => {
                self.step = CheckpointStep::List;
                true
            }
            CheckpointStep::Confirm(preview) => {
                self.step = CheckpointStep::Preview(preview.clone());
                true
            }
            CheckpointStep::Restoring(_) | CheckpointStep::Resuming(_) => true,
            CheckpointStep::Restored(_) => false,
        }
    }

    pub fn blocks_close(&self) -> bool {
        matches!(
            self.step,
            CheckpointStep::Restoring(_) | CheckpointStep::Resuming(_)
        )
    }
}

#[derive(Debug, Clone)]
pub enum CheckpointStep {
    LoadingList,
    List,
    Previewing { checkpoint_id: String },
    Preview(CheckpointPreview),
    Confirm(CheckpointPreview),
    Restoring(CheckpointPreview),
    Restored(CheckpointRestoreResult),
    Resuming(CheckpointRestoreResult),
}

#[derive(Debug, Default)]
pub struct OverlayStack {
    active: Option<Overlay>,
    pending: VecDeque<Overlay>,
    resolved: HashSet<GovernanceId>,
}

impl OverlayStack {
    pub fn active(&self) -> Option<&Overlay> {
        self.active.as_ref()
    }

    pub fn active_mut(&mut self) -> Option<&mut Overlay> {
        self.active.as_mut()
    }

    pub fn push(&mut self, overlay: Overlay) {
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

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use super::*;

    fn event(kind: &str) -> WireEvent {
        WireEvent {
            event_id: String::new(),
            session_id: "s1".into(),
            task_id: "t1".into(),
            kind: kind.into(),
            actor: String::new(),
            timestamp: String::new(),
            payload: BTreeMap::new(),
            permission_decision_id: String::new(),
            raw_cursor: 0,
            status: String::new(),
            summary: String::new(),
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

        let Some(Overlay::Approval(active)) = stack.active() else {
            panic!("approval must remain active");
        };
        assert_eq!(active.decision_id, "perm_1");
        stack.resolve_active();
        let Some(Overlay::Approval(active)) = stack.active() else {
            panic!("second approval must advance after resolution");
        };
        assert_eq!(active.decision_id, "perm_2");
    }

    #[test]
    fn request_then_resolution_leaves_no_overlay() {
        let mut stack = OverlayStack::default();
        stack.reconcile_event(&event("permission.request"));
        stack.reconcile_event(&resolution("approval_resolved", "perm_1"));
        assert!(stack.active().is_none());
    }

    #[test]
    fn resolving_active_request_advances_the_queue() {
        let mut stack = OverlayStack::default();
        stack.reconcile_event(&event("permission.request"));
        let mut second = event("permission.request");
        second.decision_id = "perm_2".into();
        stack.reconcile_event(&second);
        stack.reconcile_event(&resolution("approval_resolved", "perm_1"));
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
        stack.reconcile_event(&resolution("approval_resolved", "perm_2"));
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
        stack.reconcile_event(&resolution("approval_resolved", "perm_1"));
        stack.reconcile_event(&event("permission.request"));
        assert!(stack.active().is_none());
    }

    #[test]
    fn checkpoint_overlay_selects_newest_and_preserves_explicit_confirmation() {
        let mut overlay = CheckpointOverlay::loaded(
            "session_1".into(),
            vec![checkpoint("task_1:1", 1), checkpoint("task_1:2", 2)],
        );
        assert_eq!(
            overlay
                .selected_checkpoint()
                .map(|item| item.checkpoint_id.as_str()),
            Some("task_1:2")
        );
        let preview = CheckpointPreview {
            checkpoint: checkpoint("task_1:2", 2),
            conversation_turns: 2,
            summary: "before change".into(),
            rollback_patches: vec!["patch_2".into()],
            will_resume: "paused".into(),
        };
        overlay.step = CheckpointStep::Preview(preview.clone());
        assert!(overlay.back());
        assert!(matches!(overlay.step, CheckpointStep::List));
        overlay.step = CheckpointStep::Confirm(preview);
        assert!(overlay.back());
        assert!(matches!(overlay.step, CheckpointStep::Preview(_)));
        overlay.step = CheckpointStep::Restoring(match &overlay.step {
            CheckpointStep::Preview(preview) => preview.clone(),
            _ => unreachable!(),
        });
        assert!(overlay.blocks_close());
        assert!(overlay.back());
        assert!(matches!(overlay.step, CheckpointStep::Restoring(_)));
    }

    fn checkpoint(id: &str, turn: usize) -> Checkpoint {
        Checkpoint {
            checkpoint_id: id.into(),
            parent_checkpoint_id: String::new(),
            created_at: "2026-07-27T12:00:00Z".into(),
            sequence: format!("{turn:020}"),
            task_id: "task_1".into(),
            session_id: "sess_1".into(),
            turn,
            summary: format!("turn {turn}"),
            applied_patches: Vec::new(),
        }
    }

    fn resolution(status: &str, id: &str) -> WireEvent {
        let id_key = if status == "approval_resolved" {
            "decision_id"
        } else {
            "question_id"
        };
        WireEvent {
            kind: "TaskCreated".into(),
            payload: BTreeMap::from([("status".into(), status.into()), (id_key.into(), id.into())]),
            ..WireEvent::default()
        }
    }
}
