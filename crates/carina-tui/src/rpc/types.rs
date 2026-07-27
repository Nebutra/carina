use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Debug, Clone, Deserialize)]
pub struct RuntimeInitialize {
    pub runtime_version: String,
    pub protocol_version: String,
    pub projection_version: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct ModelInventory {
    #[serde(default)]
    pub default_model: String,
    #[serde(default)]
    pub providers: Vec<ModelProvider>,
}

impl ModelInventory {
    pub fn available_models(&self) -> Vec<Model> {
        self.providers
            .iter()
            .filter(|provider| provider.registered && provider.available)
            .flat_map(|provider| provider.models.iter())
            .filter(|model| model.available)
            .cloned()
            .collect()
    }

    pub fn has_runnable_provider(&self) -> bool {
        self.providers
            .iter()
            .any(|provider| provider.registered && provider.available)
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct ModelProvider {
    pub id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub registered: bool,
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub auth_source: String,
    #[serde(default)]
    pub models: Vec<Model>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Model {
    pub id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub reasoning: bool,
    #[serde(default)]
    pub reasoning_efforts: Vec<String>,
    #[serde(default)]
    pub image_input: bool,
    #[serde(default)]
    pub tool_call: bool,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Session {
    pub session_id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub workspace_id: String,
    #[serde(default)]
    pub workspace_root: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub next_model: String,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub latest_task_id: String,
    #[serde(default)]
    pub task_status: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub continuity: Option<ContinuityState>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ContinuityState {
    #[serde(default)]
    pub recovery: RecoveryDecision,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct RecoveryDecision {
    #[serde(default)]
    pub disposition: String,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub checkpoint_id: String,
    #[serde(default)]
    pub proofs: BTreeMap<String, bool>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct SessionItemEvent {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub turn_id: String,
    #[serde(default)]
    pub task_id: String,
    #[serde(default)]
    pub item_id: String,
    #[serde(default)]
    pub source_event_id: String,
    #[serde(default)]
    pub timestamp: String,
    #[serde(default)]
    pub details: BTreeMap<String, Value>,
    pub item: Option<SessionItem>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct SessionItem {
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub task_id: String,
    #[serde(default)]
    pub details: BTreeMap<String, Value>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct WireEvent {
    #[serde(default)]
    pub event_id: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub task_id: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub actor: String,
    #[serde(default)]
    pub timestamp: String,
    #[serde(default)]
    pub payload: BTreeMap<String, Value>,
    #[serde(default)]
    pub permission_decision_id: String,
    #[serde(default)]
    pub raw_cursor: usize,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub decision_id: String,
    #[serde(default)]
    pub capability: String,
    #[serde(default)]
    pub resource: String,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub diff: String,
    #[serde(default)]
    pub question_id: String,
    #[serde(default)]
    pub prompt: String,
    #[serde(default)]
    pub options: Vec<QuestionOption>,
}

impl WireEvent {
    pub fn stable_id(&self) -> String {
        for value in [
            self.event_id.as_str(),
            self.decision_id.as_str(),
            self.question_id.as_str(),
            self.task_id.as_str(),
            self.timestamp.as_str(),
        ] {
            if !value.is_empty() {
                return value.to_owned();
            }
        }
        "anonymous".into()
    }

    pub fn is_task_terminal(&self) -> bool {
        self.kind == "task.completed"
            || matches!(
                self.projected_status(),
                "completed" | "failed" | "cancelled" | "degraded"
            )
    }

    pub fn task_activity_status(&self) -> Option<&str> {
        if self.task_id.is_empty() || self.clears_active_task() {
            return None;
        }
        let status = self.projected_status();
        if self.kind == "TaskCreated" {
            return matches!(
                status,
                "queued" | "running" | "waiting_input" | "waiting_approval"
            )
            .then_some(status);
        }
        Some(if status.is_empty() { "running" } else { status })
    }

    pub fn clears_active_task(&self) -> bool {
        self.is_task_terminal() || matches!(self.projected_status(), "paused" | "interrupted")
    }

    pub fn projected_status(&self) -> &str {
        if self.status.is_empty() {
            self.payload
                .get("status")
                .and_then(Value::as_str)
                .unwrap_or_default()
        } else {
            self.status.as_str()
        }
    }

    pub fn governance_resolution(&self) -> Option<GovernanceId> {
        match self.projected_status() {
            "approval_resolved" => self
                .nonempty_decision_id()
                .or_else(|| self.payload.get("decision_id").and_then(Value::as_str))
                .map(|id| GovernanceId::Approval(id.to_owned())),
            "user_question_resolved" => self
                .nonempty_question_id()
                .or_else(|| self.payload.get("question_id").and_then(Value::as_str))
                .map(|id| GovernanceId::Question(id.to_owned())),
            _ => None,
        }
    }

    fn nonempty_decision_id(&self) -> Option<&str> {
        (!self.decision_id.is_empty()).then_some(self.decision_id.as_str())
    }

    fn nonempty_question_id(&self) -> Option<&str> {
        (!self.question_id.is_empty()).then_some(self.question_id.as_str())
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub enum GovernanceId {
    Approval(String),
    Question(String),
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct QuestionOption {
    pub label: String,
    pub value: String,
    #[serde(default)]
    pub description: String,
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn governance_resolution_projects_nested_audit_payloads() {
        let approval: WireEvent = serde_json::from_value(json!({
            "type": "TaskCreated",
            "payload": {"status": "approval_resolved", "decision_id": "perm_1"}
        }))
        .unwrap();
        assert_eq!(
            approval.governance_resolution(),
            Some(GovernanceId::Approval("perm_1".into()))
        );

        let question: WireEvent = serde_json::from_value(json!({
            "type": "TaskCreated",
            "payload": {"status": "user_question_resolved", "question_id": "q_1"}
        }))
        .unwrap();
        assert_eq!(
            question.governance_resolution(),
            Some(GovernanceId::Question("q_1".into()))
        );
    }

    #[test]
    fn terminal_status_projects_from_canonical_audit_payload() {
        let event: WireEvent = serde_json::from_value(json!({
            "type": "TaskCreated",
            "task_id": "task_1",
            "payload": {"status": "completed", "summary": "done"}
        }))
        .unwrap();
        assert_eq!(event.projected_status(), "completed");
        assert!(event.is_task_terminal());
    }

    #[test]
    fn checkpoint_audit_status_does_not_claim_task_activity() {
        let restored: WireEvent = serde_json::from_value(json!({
            "type": "TaskCreated",
            "task_id": "task_1",
            "payload": {"status": "checkpoint_restored", "checkpoint_id": "task_1:2"}
        }))
        .unwrap();
        assert_eq!(restored.task_activity_status(), None);
        assert!(!restored.clears_active_task());

        let paused: WireEvent = serde_json::from_value(json!({
            "type": "TaskCreated",
            "task_id": "task_1",
            "payload": {"status": "paused"}
        }))
        .unwrap();
        assert!(paused.clears_active_task());
    }
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct Task {
    pub task_id: String,
    pub session_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub user_prompt: String,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct Checkpoint {
    pub checkpoint_id: String,
    #[serde(default)]
    pub parent_checkpoint_id: String,
    pub created_at: String,
    pub sequence: String,
    pub task_id: String,
    pub session_id: String,
    pub turn: usize,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub applied_patches: Vec<String>,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct CheckpointPreview {
    pub checkpoint: Checkpoint,
    pub conversation_turns: usize,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub rollback_patches: Vec<String>,
    pub will_resume: String,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct CheckpointRestoreResult {
    pub restored: bool,
    pub checkpoint_id: String,
    pub task_id: String,
    pub turn: usize,
    #[serde(default)]
    pub rolled_back: Vec<String>,
    pub status: String,
    #[serde(default)]
    pub idempotent: bool,
    #[serde(default)]
    pub reconciliation_required: bool,
    #[serde(default)]
    pub journal_cleanup_pending: bool,
}
