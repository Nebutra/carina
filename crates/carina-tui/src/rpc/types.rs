use std::collections::{BTreeMap, BTreeSet, VecDeque};

use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct CommandRegistry {
    #[serde(default)]
    pub revision: String,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub commands: Vec<PromptCommand>,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct PromptCommand {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub source: String,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub hints: Vec<String>,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub arguments: Vec<PromptCommandArgument>,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct PromptCommandArgument {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub required: bool,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct DaemonStatus {
    #[serde(default)]
    pub queued_executions: usize,
    #[serde(default)]
    pub active_workers: usize,
    #[serde(default)]
    pub uptime_seconds: u64,
    #[serde(default)]
    pub context_engine: ContextEngineStatus,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ContextEngineStatus {
    #[serde(default)]
    pub effective_engine: String,
    #[serde(default)]
    pub phase: String,
    #[serde(default)]
    pub reason: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct UsageCostReport {
    #[serde(default)]
    pub totals: UsageTotals,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct UsageTotals {
    #[serde(default)]
    pub requests: u64,
    #[serde(default)]
    pub input_tokens: u64,
    #[serde(default)]
    pub output_tokens: u64,
    #[serde(default)]
    pub cache_read_tokens: u64,
    #[serde(default)]
    pub cache_write_tokens: u64,
    #[serde(default, alias = "cost", alias = "total_cost_usd")]
    pub cost_usd: f64,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
pub struct ContextSummary {
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub model_context_tokens: ModelContextTokens,
    #[serde(default)]
    pub task: ContextExecutionSummary,
    #[serde(default)]
    pub compaction_policy: ContextCompactionPolicy,
    #[serde(default)]
    pub checkpoint: ContextCheckpointSummary,
    #[serde(default)]
    pub compact: ContextCompactAvailability,
    #[serde(default)]
    pub recent_receipt: Option<ContextCompactionReceipt>,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ContextCompactionPolicy {
    #[serde(default)]
    pub policy_version: String,
    #[serde(default)]
    pub window_tokens: u64,
    #[serde(default)]
    pub reserve_tokens: u64,
    #[serde(default)]
    pub trigger_tokens: u64,
    #[serde(default)]
    pub metadata_source: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ContextCheckpointSummary {
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub checkpoint_id: String,
    #[serde(default)]
    pub transcript_bytes: u64,
    #[serde(default)]
    pub turn_count: usize,
    #[serde(default)]
    pub compaction_count: usize,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ReplayBoundaryV1 {
    #[serde(default)]
    pub version: i64,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub runtime_id: String,
    #[serde(default)]
    pub runtime_epoch: String,
    #[serde(default)]
    pub runtime_process_epoch: i64,
    #[serde(default)]
    pub requested_since: i64,
    #[serde(default)]
    pub durable_cursor: i64,
    #[serde(default)]
    pub durable_replayed: i64,
    #[serde(default)]
    pub transient_tail_revision: i64,
    #[serde(default)]
    pub transient_snapshots: i64,
    #[serde(default)]
    pub buffered_live: i64,
}

impl ReplayBoundaryV1 {
    pub fn validate(&self) -> Result<(), String> {
        if self.version != 1 {
            return Err("replay_boundary.version must be 1".into());
        }
        if self.session_id.trim().is_empty()
            || self.runtime_id.trim().is_empty()
            || self.runtime_epoch.trim().is_empty()
        {
            return Err("replay_boundary identity is incomplete".into());
        }
        if self.runtime_process_epoch < 0
            || self.requested_since < 0
            || self.durable_cursor < 0
            || self.durable_replayed < 0
            || self.transient_tail_revision < 0
            || self.transient_snapshots < 0
            || self.buffered_live < 0
        {
            return Err("replay_boundary counts must be >= 0".into());
        }
        if self.durable_cursor < self.requested_since {
            return Err("replay_boundary.durable_cursor is below requested_since".into());
        }
        Ok(())
    }

    pub fn validate_attachment(
        &self,
        snapshots: usize,
        durable: usize,
        live: usize,
    ) -> Result<(), String> {
        self.validate()?;
        if durable != self.as_usize(self.durable_replayed)
            || snapshots != self.as_usize(self.transient_snapshots)
            || live != self.as_usize(self.buffered_live)
        {
            return Err("replay attachment counts do not match replay_boundary".into());
        }
        if snapshots > 0 && self.transient_tail_revision <= 0 {
            return Err("transient snapshots require a positive transient_tail_revision".into());
        }
        Ok(())
    }

    pub fn catch_up_len(&self) -> Option<usize> {
        self.durable_replayed
            .checked_add(self.transient_snapshots)?
            .checked_add(self.buffered_live)
            .and_then(|total| usize::try_from(total).ok())
    }

    pub fn classify(&self, index: usize) -> Option<CatchUpDelivery> {
        let durable = self.as_usize(self.durable_replayed);
        let snapshots = self.as_usize(self.transient_snapshots);
        let live = self.as_usize(self.buffered_live);
        if index < durable {
            Some(CatchUpDelivery::DurableReplay)
        } else if index < durable.saturating_add(snapshots) {
            Some(CatchUpDelivery::TransientSnapshot)
        } else if index < durable.saturating_add(snapshots).saturating_add(live) {
            Some(CatchUpDelivery::BufferedLive)
        } else {
            None
        }
    }

    fn as_usize(&self, value: i64) -> usize {
        usize::try_from(value).unwrap_or(0)
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CatchUpDelivery {
    DurableReplay,
    TransientSnapshot,
    BufferedLive,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct AssistantMessageSnapshot {
    #[serde(default)]
    pub r#type: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub run_id: String,
    #[serde(default)]
    pub actor: String,
    #[serde(default)]
    pub payload: AssistantMessageSnapshotPayload,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct AssistantMessageSnapshotPayload {
    #[serde(default)]
    pub generation: i64,
    #[serde(default)]
    pub sequence: i64,
    #[serde(default)]
    pub phase: String,
    #[serde(default)]
    pub content: String,
    #[serde(default)]
    pub structured_output: bool,
    #[serde(default)]
    pub tail_revision: i64,
    #[serde(default)]
    pub state: String,
}

impl AssistantMessageSnapshot {
    pub fn validate(&self, boundary: &ReplayBoundaryV1) -> Result<(), String> {
        if self.r#type != "assistant.message.snapshot" {
            return Err("snapshot type must be assistant.message.snapshot".into());
        }
        if self.session_id != boundary.session_id {
            return Err("snapshot session_id does not match replay_boundary".into());
        }
        if self.run_id.trim().is_empty() || self.payload.phase.trim().is_empty() {
            return Err("snapshot run_id and phase are required".into());
        }
        if self.payload.generation <= 0
            || self.payload.sequence <= 0
            || self.payload.tail_revision <= 0
        {
            return Err("snapshot generation, sequence, and tail_revision must be positive".into());
        }
        if self.payload.state != "open" && self.payload.state != "awaiting_canonical" {
            return Err("snapshot state must be open or awaiting_canonical".into());
        }
        if self.payload.tail_revision > boundary.transient_tail_revision {
            return Err("snapshot tail_revision is newer than the advertised cut".into());
        }
        Ok(())
    }
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
pub struct SessionGoal {
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub objective: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub token_budget: u64,
    #[serde(default)]
    pub tokens_used: u64,
    #[serde(default)]
    pub continuations_used: u64,
    #[serde(default)]
    pub max_continuations: u64,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
pub struct GoalGetResult {
    #[serde(default)]
    pub goal: Option<SessionGoal>,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
pub struct CheckpointCompactResult {
    #[serde(default)]
    pub compacted: bool,
    #[serde(default)]
    pub task_id: String,
    #[serde(default)]
    pub checkpoint_id: String,
    #[serde(default)]
    pub reason: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ContextCompactAvailability {
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub reason: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
pub struct ContextCompactionReceipt {
    #[serde(default)]
    pub policy_version: String,
    #[serde(default)]
    pub metadata_source: String,
    #[serde(default)]
    pub pressure_before: f64,
    #[serde(default)]
    pub removed_turns: usize,
    #[serde(default)]
    pub kept_turn_indices: Vec<usize>,
    #[serde(default)]
    pub key_files: Vec<String>,
    #[serde(default)]
    pub preimage_sha256: String,
    #[serde(default)]
    pub summary_sha256: String,
    #[serde(default)]
    pub kept_sha256: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ContextExecutionSummary {
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub mode: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ModelContextTokens {
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub estimated: bool,
    #[serde(default)]
    pub estimated_limit: bool,
    #[serde(default)]
    pub metadata_source: String,
    #[serde(default)]
    pub tokens: u64,
    #[serde(default)]
    pub limit_tokens: u64,
    #[serde(default)]
    pub remaining_tokens: u64,
    #[serde(default)]
    pub used_percent: u8,
    #[serde(default)]
    pub threshold: String,
    #[serde(default)]
    pub measurement: String,
    #[serde(default)]
    pub breakdown: ModelTokenBreakdown,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ModelTokenBreakdown {
    #[serde(default)]
    pub input_tokens: u64,
    #[serde(default)]
    pub output_tokens: u64,
    #[serde(default)]
    pub cache_read_tokens: u64,
    #[serde(default)]
    pub cache_write_tokens: u64,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct AgentView {
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub needs_input: Vec<AgentViewEntry>,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub working: Vec<AgentViewEntry>,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub completed: Vec<AgentViewEntry>,
}

fn deserialize_null_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Option::<T>::deserialize(deserializer).map(Option::unwrap_or_default)
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct AgentViewEntry {
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub task_id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub category: String,
    #[serde(default)]
    pub prompt: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub agent: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub workspace: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct AgentRecap {
    #[serde(default)]
    pub agent: AgentViewEntry,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub recent_events: Vec<WireEvent>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct SessionReview {
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub changes: Vec<ReviewEntry>,
    #[serde(default)]
    pub checks: Vec<ReviewEntry>,
    #[serde(default)]
    pub diagnostics: Vec<ReviewEntry>,
    #[serde(default)]
    pub artifact_ids: Vec<String>,
    #[serde(default)]
    pub rollback: RollbackSummary,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct WorkspaceDiff {
    #[serde(default)]
    pub files: Vec<WorkspaceDiffFile>,
    #[serde(default)]
    pub truncated: bool,
    #[serde(default)]
    pub total_bytes: usize,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct WorkspaceDiffFile {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub untracked: bool,
    #[serde(default)]
    pub binary: bool,
    #[serde(default)]
    pub truncated: bool,
    #[serde(default)]
    pub bytes: usize,
    #[serde(default)]
    pub diff: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct WorkspacePatch {
    #[serde(default)]
    pub patch_id: String,
    #[serde(default)]
    pub transaction_id: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub actor: String,
    #[serde(default)]
    pub task_id: String,
    #[serde(default)]
    pub agent_step_id: String,
    #[serde(default)]
    pub model_id: String,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub status: String,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub affected_files: Vec<String>,
    #[serde(default)]
    pub base_hash: String,
    #[serde(default)]
    pub new_hash: String,
    #[serde(default)]
    pub diff: String,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub risk_level: u8,
    #[serde(default)]
    pub approval_status: String,
    #[serde(default)]
    pub test_status: String,
    #[serde(default)]
    pub approval_id: String,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub audit_event_ids: Vec<String>,
    #[serde(default)]
    pub verify_result: Option<PatchVerifyResult>,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub hunk_attributions: Vec<PatchHunkAttribution>,
    #[serde(default)]
    pub rollback_pointer: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct PatchVerifyResult {
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub expected_hash: String,
    #[serde(default)]
    pub actual_hash: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct PatchHunkAttribution {
    #[serde(default)]
    pub file: String,
    #[serde(default)]
    pub hunk_index: usize,
    #[serde(default)]
    pub actor: String,
    #[serde(default)]
    pub transaction_id: String,
    #[serde(default)]
    pub approval_id: String,
    #[serde(default)]
    pub audit_event_id: String,
    #[serde(default)]
    pub verify_result: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct PatchRollbackPreview {
    #[serde(default)]
    pub patch_id: String,
    #[serde(default)]
    pub transaction_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub can_rollback: bool,
    #[serde(default)]
    pub workspace_unchanged: bool,
    #[serde(default)]
    pub expected_hash: String,
    #[serde(default)]
    pub actual_hash: String,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub files: Vec<PatchRollbackFile>,
    #[serde(default)]
    pub file_count: usize,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub hunk_attributions: Vec<PatchHunkAttribution>,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct PatchRollbackFile {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub action: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ReviewEntry {
    #[serde(default)]
    pub id: String,
    #[serde(default, rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    #[serde(alias = "task_id")]
    pub run_id: String,
    #[serde(default)]
    pub started_at: String,
    #[serde(default)]
    pub completed_at: String,
    #[serde(default)]
    pub details: ReviewDetails,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ReviewDetails {
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub affected_files: Vec<String>,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub active_files: Vec<String>,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub failed_files: Vec<String>,
    #[serde(default)]
    pub patch_id: String,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub error: String,
    #[serde(default)]
    pub patch_count: usize,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct RollbackSummary {
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub patch_ids: Vec<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct RuntimeInitialize {
    pub runtime_version: String,
    pub protocol_version: String,
    pub projection_version: String,
    #[serde(default)]
    pub capabilities: RuntimeCapabilities,
    #[serde(default)]
    pub runtime: RuntimeIdentity,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct RuntimeIdentity {
    #[serde(default)]
    pub runtime_id: String,
    #[serde(default)]
    pub workspace_id: String,
    #[serde(default)]
    pub epoch: String,
    #[serde(default)]
    pub process_epoch: i64,
    #[serde(default)]
    pub pid: i64,
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RuntimeExpectation {
    pub workspace_id: String,
    pub runtime_id: String,
    pub epoch: String,
}

impl RuntimeExpectation {
    pub fn is_complete(&self) -> bool {
        !self.workspace_id.trim().is_empty()
            && !self.runtime_id.trim().is_empty()
            && !self.epoch.trim().is_empty()
    }

    pub fn matches(&self, actual: &RuntimeIdentity) -> bool {
        actual.workspace_id == self.workspace_id
            && actual.runtime_id == self.runtime_id
            && actual.epoch == self.epoch
    }
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct RuntimeCapabilities {
    #[serde(default)]
    pub rpc_methods: Vec<String>,
    #[serde(default)]
    pub session_items_watermark: VersionedCapability,
    #[serde(default)]
    pub event_replay_tail: Option<EventReplayTailCapability>,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct EventReplayTailCapability {
    #[serde(default)]
    pub version: u64,
    #[serde(default)]
    pub snapshot_event: String,
    #[serde(default)]
    pub durable_cursor: String,
}

impl RuntimeCapabilities {
    pub fn event_replay_tail_v1(&self) -> bool {
        self.event_replay_tail
            .as_ref()
            .is_some_and(|capability| capability.version == 1)
    }
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct VersionedCapability {
    #[serde(default)]
    pub version: u64,
}

impl RuntimeInitialize {
    pub fn require_methods(&self, required: &[&str]) -> Result<(), super::RpcError> {
        let missing = required
            .iter()
            .copied()
            .filter(|method| {
                !self
                    .capabilities
                    .rpc_methods
                    .iter()
                    .any(|available| available == method)
            })
            .collect::<Vec<_>>();
        if missing.is_empty() {
            return Ok(());
        }
        Err(super::RpcError::Protocol(format!(
            "incompatible Carina runtime {}: missing required RPC methods: {}; restart the workspace runtime with the installed Carina version",
            self.runtime_version,
            missing.join(", ")
        )))
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct ModelInventory {
    #[serde(default)]
    pub default_model: String,
    #[serde(default)]
    pub providers: Vec<ModelProvider>,
    #[serde(default)]
    pub reasoner: ModelReasoner,
    #[serde(default)]
    pub readiness: ExecutionReadiness,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ExecutionReadiness {
    #[serde(default)]
    pub step: String,
    #[serde(default)]
    pub blockers: Vec<String>,
    #[serde(default)]
    pub route_kind: String,
    #[serde(default)]
    pub model_id: String,
    #[serde(default)]
    pub locale: String,
    #[serde(default)]
    pub can_submit: bool,
    #[serde(default)]
    pub epoch: String,
    #[serde(default)]
    pub generation: u64,
}

impl ModelInventory {
    pub fn available_models(&self) -> Vec<Model> {
        self.providers
            .iter()
            .filter(|provider| self.is_provider_runnable(provider))
            .flat_map(|provider| provider.models.iter())
            .filter(|model| model.available)
            .cloned()
            .collect()
    }

    pub fn has_runnable_provider(&self) -> bool {
        self.providers
            .iter()
            .any(|provider| self.is_provider_runnable(provider))
    }

    pub fn is_provider_runnable(&self, provider: &ModelProvider) -> bool {
        self.reasoner.available && provider.registered && provider.available
    }
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ModelReasoner {
    #[serde(default)]
    pub backend: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub explicit: bool,
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
    pub source_kind: String,
    #[serde(default)]
    pub source_label: String,
    #[serde(default)]
    pub source_app: String,
    #[serde(default)]
    pub source_route: String,
    #[serde(default)]
    pub source_auth_mode: String,
    #[serde(default)]
    pub source_credential_owner: String,
    #[serde(default)]
    pub source_action: String,
    #[serde(default)]
    pub source_current: bool,
    #[serde(default)]
    pub source_importable: bool,
    #[serde(default)]
    pub source_reason: String,
    #[serde(default)]
    pub models: Vec<Model>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Model {
    pub id: String,
    #[serde(default)]
    pub display_id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub available: bool,
    /// ready|circuit_open|rate_limited|probing|auth_error|unavailable
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub status_reason: String,
    #[serde(default)]
    pub reasoning: bool,
    #[serde(default)]
    pub reasoning_efforts: Vec<String>,
    #[serde(default)]
    pub default_reasoning_effort: String,
    #[serde(default)]
    pub image_input: bool,
    #[serde(default)]
    pub tool_call: bool,
}

impl Model {
    /// Operator-facing route health code (ready when unset and available).
    pub fn health_status(&self) -> &str {
        if !self.available {
            return "unavailable";
        }
        match self.status.as_str() {
            "" => "ready",
            other => other,
        }
    }

    /// Operator-facing health: whether the model is currently safe to select.
    pub fn is_usable(&self) -> bool {
        match self.health_status() {
            "ready" | "probing" => true, // half-open probe may succeed
            "circuit_open" | "rate_limited" | "auth_error" | "unavailable" => false,
            _ => self.available,
        }
    }
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
    pub next_reasoning_effort: String,
    #[serde(default)]
    pub model_preference_revision: u64,
    #[serde(default)]
    pub plan_mode: bool,
    #[serde(default)]
    pub permission_profile: String,
    /// Session/kernel approval axis; distinct from product HITL mode.
    #[serde(default)]
    pub approval_mode: String,
    #[serde(default)]
    pub created_at: String,
    /// Recency for cold-start resume / session browser (task activity or created_at).
    #[serde(default)]
    pub updated_at: String,
    #[serde(default, alias = "latest_task_id")]
    pub latest_run_id: String,
    #[serde(default, alias = "latest_task_agent")]
    pub latest_run_agent: String,
    #[serde(default)]
    pub latest_run_result_kind: String,
    #[serde(default, alias = "task_status")]
    pub execution_status: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub continuity: Option<ContinuityState>,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ConversationImportCandidate {
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub workspace_root: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub updated_at: String,
    #[serde(default)]
    pub message_count: usize,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub warnings: Vec<String>,
    #[serde(default)]
    pub imported_session_id: String,
    #[serde(default)]
    pub imported_messages: usize,
    #[serde(default)]
    pub new_messages: usize,
    #[serde(default)]
    pub target_workspace: String,
    #[serde(default)]
    pub importable: bool,
    #[serde(default)]
    pub import_error: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ConversationImportDiscovery {
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub conversations: Vec<ConversationImportCandidate>,
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub warnings: Vec<String>,
    #[serde(default)]
    pub copy_semantics: String,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct ConversationImportSelection {
    pub source: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub source_root: String,
    pub path: String,
    pub conversation_id: String,
    pub target_workspace: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ConversationImportReceipt {
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub conversation_id: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub workspace_root: String,
    #[serde(default)]
    pub imported_messages: usize,
    #[serde(default)]
    pub skipped_messages: usize,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub error: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ConversationImportApplyResult {
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub results: Vec<ConversationImportReceipt>,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct ConfigInventory {
    #[serde(default)]
    pub effective: EffectiveConfig,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct EffectiveConfig {
    #[serde(default)]
    pub approval_mode: String,
    #[serde(default)]
    pub disable_always_approve: bool,
    #[serde(default)]
    pub sandbox_commands: bool,
    #[serde(default)]
    pub permission_profile: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct PlanModeState {
    pub session_id: String,
    pub plan_mode: bool,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct SoftInterruptResult {
    pub requested: bool,
    #[serde(default)]
    pub already_requested: bool,
    pub run_id: String,
    pub mode: String,
    pub safe_point: String,
    #[serde(default)]
    pub active_tool: bool,
    #[serde(default)]
    pub queue_depth: usize,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct QueueItem {
    pub steer_id: String,
    #[serde(default)]
    pub priority: String,
    #[serde(default)]
    pub preview: String,
    #[serde(default)]
    pub index: usize,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct QueueListResult {
    pub run_id: String,
    #[serde(default)]
    pub queue_depth: usize,
    #[serde(default)]
    pub items: Vec<QueueItem>,
    #[serde(default)]
    pub soft_interrupt_pending: bool,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct QueueDropResult {
    pub run_id: String,
    pub steer_id: String,
    pub dropped: bool,
    #[serde(default)]
    pub queue_depth: usize,
}

#[derive(Debug, Clone, Deserialize)]
pub struct PlanApprovalResult {
    pub session_id: String,
    pub plan_mode: bool,
    pub approved: bool,
    #[serde(default, alias = "execution")]
    pub task: Option<ExecutionRun>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct SessionModelSelection {
    pub session_id: String,
    pub next_model: String,
    #[serde(default)]
    pub next_reasoning_effort: String,
    #[serde(default)]
    pub model_preference_revision: u64,
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
    #[serde(alias = "task_id")]
    pub run_id: String,
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
pub struct SessionItemsSnapshot {
    pub session_id: String,
    #[serde(default)]
    pub runtime_id: String,
    #[serde(default)]
    pub runtime_epoch: String,
    #[serde(default)]
    pub runtime_process_epoch: i64,
    pub durable_cursor: usize,
    #[serde(default)]
    pub items: Vec<SessionItemEvent>,
}

impl SessionItemsSnapshot {
    pub fn validate_identity(
        &self,
        session_id: &str,
        runtime: &RuntimeIdentity,
    ) -> Result<(), super::RpcError> {
        let matches = self.session_id == session_id
            && self.runtime_id == runtime.runtime_id
            && self.runtime_epoch == runtime.epoch
            && self.runtime_process_epoch == runtime.process_epoch;
        if matches {
            return Ok(());
        }
        Err(super::RpcError::Protocol(
            "session.items watermark returned mismatched session/runtime identity".into(),
        ))
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct SessionItem {
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    #[serde(alias = "task_id")]
    pub run_id: String,
    #[serde(default)]
    pub details: BTreeMap<String, Value>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct WireEvent {
    #[serde(default)]
    pub event_id: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default, alias = "task_id")]
    pub run_id: String,
    #[serde(default)]
    pub agent: String,
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
    pub result_kind: String,
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
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub options: Vec<QuestionOption>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ToolArtifactRef {
    pub session_id: String,
    pub run_id: String,
    pub call_id: String,
    pub artifact_id: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ExecutionLifecycle {
    Queued,
    Started,
    WaitingInput,
    WaitingApproval,
    Paused,
    Interrupted,
    Completed,
    Failed,
    Degraded,
    Cancelled,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ExecutionLifecycleReduction {
    NotLifecycle,
    Accepted(ExecutionLifecycle),
    Ignored,
}

#[derive(Debug, Default)]
pub struct ExecutionLifecycleReducer {
    by_run: BTreeMap<String, ExecutionLifecycle>,
    seen_event_ids: BTreeSet<String>,
    event_id_order: VecDeque<String>,
}

impl ExecutionLifecycleReducer {
    const SEEN_EVENT_LIMIT: usize = 4_096;

    pub fn clear(&mut self) {
        self.by_run.clear();
        self.seen_event_ids.clear();
        self.event_id_order.clear();
    }

    pub fn seed(&mut self, run_id: &str, lifecycle: ExecutionLifecycle) -> bool {
        if run_id.is_empty() {
            return false;
        }
        let previous = self.by_run.get(run_id).copied();
        if previous == Some(lifecycle)
            || previous.is_some_and(|state| !state.can_transition_to(lifecycle))
        {
            return false;
        }
        self.by_run.insert(run_id.to_owned(), lifecycle);
        true
    }

    pub fn reduce(&mut self, event: &WireEvent) -> ExecutionLifecycleReduction {
        if self.remembered(event) {
            return ExecutionLifecycleReduction::Ignored;
        }
        let Some(next) = event.execution_lifecycle() else {
            return ExecutionLifecycleReduction::NotLifecycle;
        };
        let previous = self.by_run.get(&event.run_id).copied();
        if previous.is_none()
            && !matches!(
                next,
                ExecutionLifecycle::Queued | ExecutionLifecycle::Started
            )
        {
            return ExecutionLifecycleReduction::Ignored;
        }
        if previous == Some(next) || previous.is_some_and(|state| !state.can_transition_to(next)) {
            return ExecutionLifecycleReduction::Ignored;
        }
        self.by_run.insert(event.run_id.clone(), next);
        ExecutionLifecycleReduction::Accepted(next)
    }

    fn remembered(&mut self, event: &WireEvent) -> bool {
        let event_id = event.event_id.trim();
        if event_id.is_empty() {
            return false;
        }
        if !self.seen_event_ids.insert(event_id.to_owned()) {
            return true;
        }
        self.event_id_order.push_back(event_id.to_owned());
        while self.event_id_order.len() > Self::SEEN_EVENT_LIMIT {
            if let Some(expired) = self.event_id_order.pop_front() {
                self.seen_event_ids.remove(&expired);
            }
        }
        false
    }
}

impl ExecutionLifecycle {
    pub fn from_status(status: &str) -> Option<Self> {
        match status {
            "queued" => Some(Self::Queued),
            "running" | "started" => Some(Self::Started),
            "waiting_input" | "needs_input" => Some(Self::WaitingInput),
            "waiting_approval" => Some(Self::WaitingApproval),
            "paused" => Some(Self::Paused),
            "interrupted" => Some(Self::Interrupted),
            "completed" | "succeeded" => Some(Self::Completed),
            "failed" => Some(Self::Failed),
            "degraded" => Some(Self::Degraded),
            "cancelled" => Some(Self::Cancelled),
            _ => None,
        }
    }

    pub fn status(self) -> &'static str {
        match self {
            Self::Queued => "queued",
            Self::Started => "running",
            Self::WaitingInput => "waiting_input",
            Self::WaitingApproval => "waiting_approval",
            Self::Paused => "paused",
            Self::Interrupted => "interrupted",
            Self::Completed => "completed",
            Self::Failed => "failed",
            Self::Degraded => "degraded",
            Self::Cancelled => "cancelled",
        }
    }

    pub fn is_terminal(self) -> bool {
        matches!(
            self,
            Self::Completed | Self::Failed | Self::Degraded | Self::Cancelled
        )
    }

    pub fn is_active(self) -> bool {
        matches!(
            self,
            Self::Queued | Self::Started | Self::WaitingInput | Self::WaitingApproval
        )
    }

    pub fn clears_active(self) -> bool {
        self.is_terminal() || matches!(self, Self::Paused | Self::Interrupted)
    }

    fn can_transition_to(self, next: Self) -> bool {
        if self.is_terminal() {
            return false;
        }
        match self {
            Self::Queued => matches!(next, Self::Started | Self::Failed | Self::Cancelled),
            Self::Started => matches!(
                next,
                Self::WaitingInput
                    | Self::WaitingApproval
                    | Self::Paused
                    | Self::Interrupted
                    | Self::Completed
                    | Self::Failed
                    | Self::Degraded
                    | Self::Cancelled
            ),
            Self::WaitingInput | Self::WaitingApproval => matches!(
                next,
                Self::Started
                    | Self::WaitingInput
                    | Self::WaitingApproval
                    | Self::Paused
                    | Self::Interrupted
                    | Self::Failed
                    | Self::Degraded
                    | Self::Cancelled
            ),
            Self::Paused | Self::Interrupted => matches!(
                next,
                Self::Started | Self::Failed | Self::Degraded | Self::Cancelled
            ),
            Self::Completed | Self::Failed | Self::Degraded | Self::Cancelled => false,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AssistantMessageUpdate {
    Reset {
        generation: u64,
        sequence: u64,
        phase: AssistantMessagePhase,
    },
    Delta {
        generation: u64,
        sequence: u64,
        phase: AssistantMessagePhase,
        delta: String,
    },
    Completed {
        generation: u64,
        sequence: u64,
        phase: AssistantMessagePhase,
        content: String,
    },
    Snapshot {
        generation: u64,
        sequence: u64,
        phase: AssistantMessagePhase,
        content: String,
        structured_output: bool,
        tail_revision: u64,
        state: AssistantTailState,
    },
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AssistantTailState {
    Open,
    AwaitingCanonical,
}

impl AssistantTailState {
    fn from_wire(value: &str) -> Option<Self> {
        match value {
            "open" => Some(Self::Open),
            "awaiting_canonical" => Some(Self::AwaitingCanonical),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AssistantMessagePhase {
    Commentary,
    FinalAnswer,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum FeedbackPhase {
    FirstToken,
    ToolStart,
    ApprovalWait,
    ToolResult,
    Completion,
}

impl FeedbackPhase {
    pub const ALL: [Self; 5] = [
        Self::FirstToken,
        Self::ToolStart,
        Self::ApprovalWait,
        Self::ToolResult,
        Self::Completion,
    ];

    pub const fn as_str(self) -> &'static str {
        match self {
            Self::FirstToken => "first_token",
            Self::ToolStart => "tool_start",
            Self::ApprovalWait => "approval_wait",
            Self::ToolResult => "tool_result",
            Self::Completion => "completion",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FeedbackMilestone {
    pub phase: FeedbackPhase,
    pub key: String,
}

impl AssistantMessagePhase {
    fn from_wire(value: &str) -> Option<Self> {
        match value {
            "commentary" => Some(Self::Commentary),
            "final_answer" => Some(Self::FinalAnswer),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, Deserialize, Serialize, PartialEq, Eq)]
pub struct MediaRef {
    pub artifact_id: String,
    pub media_type: String,
    pub bytes: usize,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub origin: String,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct MediaUploadReceipt {
    pub upload_id: String,
    pub next_chunk_index: usize,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
#[serde(untagged)]
pub enum MediaUploadResult {
    Complete(MediaRef),
    Pending(MediaUploadReceipt),
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ArtifactReadResponse {
    #[serde(default)]
    pub metadata: ArtifactMetadata,
    #[serde(default)]
    pub offset: usize,
    #[serde(default)]
    pub next_offset: usize,
    #[serde(default)]
    pub eof: bool,
    #[serde(default)]
    pub content_base64: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ArtifactMetadata {
    #[serde(default, alias = "media_type")]
    pub media_type: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ArtifactText {
    pub content: String,
    pub truncated: bool,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct PromptHistory {
    #[serde(default)]
    pub entries: Vec<String>,
    #[serde(default)]
    pub count: usize,
    #[serde(default)]
    pub scope: String,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct WorkspaceFile {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub size: u64,
    #[serde(default)]
    pub binary: bool,
    #[serde(default)]
    pub large: bool,
    #[serde(default)]
    pub language: String,
    #[serde(default)]
    pub mtime: i64,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub struct WorkspaceFileContent {
    #[serde(default)]
    pub content: String,
    #[serde(default)]
    pub hash: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LiveActivityUpdate {
    Started { key: String, label: String },
    Finished { key: String },
}

impl WireEvent {
    pub fn live_activity_update(&self) -> Option<LiveActivityUpdate> {
        let payload_text = |key: &str| {
            self.payload
                .get(key)
                .and_then(Value::as_str)
                .map(str::trim)
                .filter(|value| !value.is_empty())
        };
        match self.kind.as_str() {
            "ToolCallStarted" => Some(LiveActivityUpdate::Started {
                key: format!("tool:{}", payload_text("call_id")?),
                label: payload_text("tool")?.to_owned(),
            }),
            "ToolCallCompleted" | "ToolCallFailed" | "ToolCallDenied" | "ToolCallCancelled" => {
                Some(LiveActivityUpdate::Finished {
                    key: format!("tool:{}", payload_text("call_id")?),
                })
            }
            "CommandStarted" => Some(LiveActivityUpdate::Started {
                key: format!("command:{}", payload_text("command_id")?),
                label: payload_text("command")?.to_owned(),
            }),
            "CommandExited" => Some(LiveActivityUpdate::Finished {
                key: format!("command:{}", payload_text("command_id")?),
            }),
            _ => None,
        }
    }

    pub fn assistant_message_update(&self) -> Option<AssistantMessageUpdate> {
        let generation = self.payload.get("generation")?.as_u64()?;
        let sequence = self.payload.get("sequence")?.as_u64()?;
        let phase = AssistantMessagePhase::from_wire(self.payload.get("phase")?.as_str()?)?;
        if generation == 0 || sequence == 0 || self.run_id.is_empty() {
            return None;
        }
        match self.kind.as_str() {
            "assistant.message.reset" => Some(AssistantMessageUpdate::Reset {
                generation,
                sequence,
                phase,
            }),
            "assistant.message.delta" => Some(AssistantMessageUpdate::Delta {
                generation,
                sequence,
                phase,
                delta: self.payload.get("delta")?.as_str()?.to_owned(),
            }),
            "assistant.message.completed" => Some(AssistantMessageUpdate::Completed {
                generation,
                sequence,
                phase,
                content: self.payload.get("content")?.as_str()?.to_owned(),
            }),
            "assistant.message.snapshot" => Some(AssistantMessageUpdate::Snapshot {
                generation,
                sequence,
                phase,
                content: self.payload.get("content")?.as_str()?.to_owned(),
                structured_output: self
                    .payload
                    .get("structured_output")
                    .and_then(Value::as_bool)
                    .unwrap_or(false),
                tail_revision: self
                    .payload
                    .get("tail_revision")?
                    .as_u64()
                    .filter(|value| *value > 0)?,
                state: AssistantTailState::from_wire(self.payload.get("state")?.as_str()?)?,
            }),
            _ => None,
        }
    }

    pub fn feedback_milestone(&self) -> Option<FeedbackMilestone> {
        let payload_id = |key: &str| {
            self.payload
                .get(key)
                .and_then(Value::as_str)
                .map(str::trim)
                .filter(|value| !value.is_empty())
        };
        let milestone = |phase, key: &str| FeedbackMilestone {
            phase,
            key: key.to_owned(),
        };
        match self.kind.as_str() {
            "assistant.message.delta" | "assistant.message.completed"
                if self.assistant_message_update().is_some() =>
            {
                Some(milestone(FeedbackPhase::FirstToken, self.run_id.trim()))
            }
            "ToolCallRequested" | "ToolCallStarted" => {
                payload_id("call_id").map(|key| milestone(FeedbackPhase::ToolStart, key))
            }
            "ToolCallApprovalRequired" => self
                .nonempty_decision_id()
                .map(str::trim)
                .filter(|value| !value.is_empty())
                .or_else(|| payload_id("decision_id"))
                .or_else(|| payload_id("call_id"))
                .map(|key| milestone(FeedbackPhase::ApprovalWait, key)),
            "ToolCallCompleted" | "ToolCallFailed" | "ToolCallDenied" | "ToolCallCancelled" => {
                payload_id("call_id").map(|key| milestone(FeedbackPhase::ToolResult, key))
            }
            _ if self.is_execution_terminal() && !self.run_id.trim().is_empty() => {
                Some(milestone(FeedbackPhase::Completion, self.run_id.trim()))
            }
            _ => None,
        }
        .filter(|candidate| !candidate.key.is_empty())
    }

    pub fn tool_artifact_ref(&self) -> Option<ToolArtifactRef> {
        if self.kind != "ToolCallCompleted" {
            return None;
        }
        let call_id = self.payload.get("call_id")?.as_str()?.trim();
        let artifact_id = self
            .payload
            .get("artifact_ids")?
            .as_array()?
            .iter()
            .find_map(Value::as_str)?
            .trim();
        if call_id.is_empty() || artifact_id.is_empty() || self.session_id.is_empty() {
            return None;
        }
        Some(ToolArtifactRef {
            session_id: self.session_id.clone(),
            run_id: self.run_id.clone(),
            call_id: call_id.to_owned(),
            artifact_id: artifact_id.to_owned(),
        })
    }

    pub fn stable_id(&self) -> String {
        let tool_call_id = self
            .kind
            .to_ascii_lowercase()
            .contains("tool")
            .then(|| self.payload.get("call_id").and_then(Value::as_str))
            .flatten()
            .filter(|call_id| !call_id.is_empty());
        if let Some(call_id) = tool_call_id {
            return format!("tool:{call_id}");
        }
        for value in [
            self.event_id.as_str(),
            self.decision_id.as_str(),
            self.question_id.as_str(),
            self.run_id.as_str(),
            self.timestamp.as_str(),
        ] {
            if !value.is_empty() {
                return value.to_owned();
            }
        }
        "anonymous".into()
    }

    pub fn execution_lifecycle(&self) -> Option<ExecutionLifecycle> {
        if self.run_id.is_empty() {
            return None;
        }
        match self.kind.as_str() {
            "ExecutionQueued" => Some(ExecutionLifecycle::Queued),
            "ExecutionStarted" => Some(ExecutionLifecycle::Started),
            "ExecutionProgressed" => match self.projected_status() {
                "queued" => Some(ExecutionLifecycle::Queued),
                "running" => Some(ExecutionLifecycle::Started),
                "waiting_input" => Some(ExecutionLifecycle::WaitingInput),
                "waiting_approval" => Some(ExecutionLifecycle::WaitingApproval),
                "paused" => Some(ExecutionLifecycle::Paused),
                "interrupted" => Some(ExecutionLifecycle::Interrupted),
                _ => None,
            },
            "ExecutionInterrupted" => Some(ExecutionLifecycle::Interrupted),
            "ExecutionCompleted" => Some(ExecutionLifecycle::Completed),
            "ExecutionFailed" => {
                if self.payload.get("outcome").and_then(Value::as_str) == Some("degraded") {
                    Some(ExecutionLifecycle::Degraded)
                } else {
                    Some(ExecutionLifecycle::Failed)
                }
            }
            "ExecutionCancelled" => Some(ExecutionLifecycle::Cancelled),
            "execution.completed" => match self.projected_status() {
                "completed" => Some(ExecutionLifecycle::Completed),
                "failed" => Some(ExecutionLifecycle::Failed),
                "degraded" => Some(ExecutionLifecycle::Degraded),
                "cancelled" => Some(ExecutionLifecycle::Cancelled),
                _ => None,
            },
            _ => None,
        }
    }

    pub fn execution_result_kind(&self) -> Option<&str> {
        let kind = if self.result_kind.is_empty() {
            self.payload
                .get("result_kind")
                .and_then(Value::as_str)
                .unwrap_or_default()
        } else {
            self.result_kind.as_str()
        };
        matches!(kind, "answer" | "plan").then_some(kind)
    }

    pub fn is_execution_terminal(&self) -> bool {
        self.execution_lifecycle()
            .is_some_and(ExecutionLifecycle::is_terminal)
    }

    pub fn execution_activity_status(&self) -> Option<&str> {
        self.execution_lifecycle()
            .filter(|lifecycle| lifecycle.is_active())
            .map(ExecutionLifecycle::status)
    }

    pub fn clears_active_execution(&self) -> bool {
        self.execution_lifecycle()
            .is_some_and(ExecutionLifecycle::clears_active)
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
        match self.kind.as_str() {
            "ToolApproved" | "ToolDenied" => self
                .nonempty_decision_id()
                .or_else(|| self.payload.get("decision_id").and_then(Value::as_str))
                .map(|id| GovernanceId::Approval(id.to_owned())),
            "ExecutionProgressed" if self.projected_status() == "approval_resolved" => self
                .nonempty_decision_id()
                .or_else(|| self.payload.get("decision_id").and_then(Value::as_str))
                .map(|id| GovernanceId::Approval(id.to_owned())),
            "ExecutionProgressed" if self.projected_status() == "user_question_resolved" => self
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

#[derive(Debug, Clone, PartialEq, Eq, Hash, serde::Serialize, serde::Deserialize)]
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
    fn runtime_expectation_requires_and_matches_the_complete_attachment() {
        let expected = RuntimeExpectation {
            workspace_id: "ws1_verified".into(),
            runtime_id: "runtime_verified".into(),
            epoch: "runtime_process".into(),
        };
        assert!(expected.is_complete());
        assert!(expected.matches(&RuntimeIdentity {
            workspace_id: expected.workspace_id.clone(),
            runtime_id: expected.runtime_id.clone(),
            epoch: expected.epoch.clone(),
            ..RuntimeIdentity::default()
        }));
        assert!(
            !RuntimeExpectation {
                epoch: " ".into(),
                ..expected.clone()
            }
            .is_complete()
        );
        assert!(!expected.matches(&RuntimeIdentity {
            workspace_id: expected.workspace_id.clone(),
            runtime_id: expected.runtime_id.clone(),
            epoch: "runtime_other".into(),
            ..RuntimeIdentity::default()
        }));
    }

    #[test]
    fn plan_approval_decodes_canonical_task_and_legacy_execution_fields() {
        for (field, run_id) in [("task", "run-task"), ("execution", "run-legacy")] {
            let mut value = json!({
                "session_id": "sess-1",
                "plan_mode": false,
                "approved": true
            });
            value[field] = json!({
                "run_id": run_id,
                "session_id": "sess-1",
                "status": "queued"
            });
            let result: PlanApprovalResult = serde_json::from_value(value).unwrap();

            assert_eq!(
                result.task.as_ref().map(|task| task.run_id.as_str()),
                Some(run_id)
            );
        }
    }

    #[test]
    fn product_status_payloads_decode_into_typed_projections() {
        let runtime: DaemonStatus = serde_json::from_value(json!({
            "queued_executions": 2,
            "active_workers": 3,
            "uptime_seconds": 42,
            "context_engine": {"effective_engine": "native", "phase": "ready"}
        }))
        .unwrap();
        assert_eq!(runtime.queued_executions, 2);
        assert_eq!(runtime.context_engine.phase, "ready");

        let agents: AgentView = serde_json::from_value(json!({
            "needs_input": null,
            "working": [{
                "session_id": "sess_1", "run_id": "run_2", "status": "running",
                "category": "working", "prompt": "Implement it", "summary": "Editing",
                "agent": "build", "model": "proxy/model", "workspace": "/workspace"
            }],
            "completed": []
        }))
        .unwrap();
        assert_eq!(agents.needs_input.len() + agents.working.len(), 1);
        assert_eq!(agents.working[0].summary, "Editing");

        let usage: UsageCostReport = serde_json::from_value(json!({
            "totals": {"requests": 4, "input_tokens": 120, "output_tokens": 30, "cost_usd": 0.25}
        }))
        .unwrap();
        assert_eq!(usage.totals.input_tokens, 120);
        assert_eq!(usage.totals.cost_usd, 0.25);

        let review: SessionReview = serde_json::from_value(json!({
            "state": "completed",
            "changes": [{
                "id": "file_evt_1", "type": "file_change", "status": "proposed",
                "details": {"affected_files": ["src/main.rs"], "active_files": null,
                    "failed_files": null, "patch_id": "patch_1", "reason": "agent edit"}
            }],
            "checks": [{}],
            "rollback": {"available": true, "patch_ids": ["patch_1"]}
        }))
        .unwrap();
        assert_eq!(review.changes.len(), 1);
        assert_eq!(review.changes[0].details.affected_files, ["src/main.rs"]);
        assert!(review.changes[0].details.active_files.is_empty());
        assert!(review.rollback.available);

        let diff: WorkspaceDiff = serde_json::from_value(json!({
            "files": [{"path": "src/main.rs", "status": " M", "bytes": 42,
                "diff": "@@ -1 +1 @@\n-old\n+new\n"}],
            "truncated": false,
            "total_bytes": 42
        }))
        .unwrap();
        assert_eq!(diff.files[0].path, "src/main.rs");
        assert!(diff.files[0].diff.contains("+new"));

        let patch: WorkspacePatch = serde_json::from_value(json!({
            "patch_id": "patch_1",
            "transaction_id": "txn_1",
            "session_id": "sess_1",
            "actor": "model",
            "status": "applied",
            "affected_files": ["src/main.rs"],
            "approval_status": "approved",
            "approval_id": "decision_1",
            "test_status": "passed",
            "audit_event_ids": ["evt_1"],
            "verify_result": {"status": "passed", "expected_hash": "a", "actual_hash": "a"},
            "hunk_attributions": [{
                "file": "src/main.rs",
                "hunk_index": 0,
                "actor": "model",
                "transaction_id": "txn_1",
                "approval_id": "decision_1",
                "audit_event_id": "evt_1",
                "verify_result": "passed"
            }],
            "rollback_pointer": "snapshot_1"
        }))
        .unwrap();
        assert_eq!(patch.hunk_attributions[0].transaction_id, "txn_1");
        assert_eq!(patch.verify_result.unwrap().status, "passed");

        let preview: PatchRollbackPreview = serde_json::from_value(json!({
            "patch_id": "patch_1",
            "transaction_id": "txn_1",
            "status": "verified",
            "can_rollback": true,
            "workspace_unchanged": true,
            "expected_hash": "a",
            "actual_hash": "a",
            "files": [{"path": "src/main.rs", "action": "restore"}],
            "file_count": 1,
            "hunk_attributions": []
        }))
        .unwrap();
        assert!(preview.can_rollback);
        assert_eq!(preview.files[0].action, "restore");

        let recap: AgentRecap = serde_json::from_value(json!({
            "agent": {"task_id": "task_2", "category": "working"},
            "recent_events": null
        }))
        .unwrap();
        assert!(recap.recent_events.is_empty());
    }

    #[test]
    fn question_event_accepts_null_options_without_dropping_the_stream() {
        let event: WireEvent = serde_json::from_value(json!({
            "type": "user.question",
            "question_id": "question_1",
            "prompt": "Which city?",
            "options": null
        }))
        .unwrap();
        assert!(event.options.is_empty());
        assert_eq!(event.question_id, "question_1");
    }

    #[test]
    fn assistant_message_updates_decode_only_the_typed_public_contract() {
        let delta: WireEvent = serde_json::from_value(json!({
            "type": "assistant.message.delta",
            "run_id": "run_1",
            "payload": {"generation": 2, "sequence": 3, "phase": "commentary", "delta": "hello"}
        }))
        .unwrap();
        assert_eq!(
            delta.assistant_message_update(),
            Some(AssistantMessageUpdate::Delta {
                generation: 2,
                sequence: 3,
                phase: AssistantMessagePhase::Commentary,
                delta: "hello".into(),
            })
        );

        let reset: WireEvent = serde_json::from_value(json!({
            "type": "assistant.message.reset",
            "run_id": "run_1",
            "payload": {"generation": 3, "sequence": 1, "phase": "final_answer"}
        }))
        .unwrap();
        assert_eq!(
            reset.assistant_message_update(),
            Some(AssistantMessageUpdate::Reset {
                generation: 3,
                sequence: 1,
                phase: AssistantMessagePhase::FinalAnswer,
            })
        );

        let completed: WireEvent = serde_json::from_value(json!({
            "type": "assistant.message.completed",
            "run_id": "run_1",
            "payload": {"generation": 2, "sequence": 4, "phase": "final_answer", "content": "hello world"}
        }))
        .unwrap();
        assert_eq!(
            completed.assistant_message_update(),
            Some(AssistantMessageUpdate::Completed {
                generation: 2,
                sequence: 4,
                phase: AssistantMessagePhase::FinalAnswer,
                content: "hello world".into(),
            })
        );

        let snapshot: WireEvent = serde_json::from_value(json!({
            "type": "assistant.message.snapshot",
            "session_id": "sess",
            "run_id": "run_1",
            "payload": {
                "generation": 2,
                "sequence": 19,
                "phase": "final_answer",
                "content": "hello",
                "tail_revision": 31,
                "state": "awaiting_canonical"
            }
        }))
        .unwrap();
        assert_eq!(
            snapshot.assistant_message_update(),
            Some(AssistantMessageUpdate::Snapshot {
                generation: 2,
                sequence: 19,
                phase: AssistantMessagePhase::FinalAnswer,
                content: "hello".into(),
                structured_output: false,
                tail_revision: 31,
                state: AssistantTailState::AwaitingCanonical,
            })
        );
    }

    #[test]
    fn assistant_message_updates_reject_malformed_or_private_payloads() {
        for value in [
            json!({
                "type": "assistant.message.delta",
                "run_id": "",
                "payload": {"generation": 1, "sequence": 1, "phase": "final_answer", "delta": "hidden"}
            }),
            json!({
                "type": "assistant.message.delta",
                "run_id": "run_1",
                "payload": {"generation": 0, "sequence": 1, "phase": "final_answer", "delta": "hidden"}
            }),
            json!({
                "type": "assistant.message.delta",
                "run_id": "run_1",
                "payload": {"generation": 1, "sequence": 0, "phase": "final_answer", "delta": "hidden"}
            }),
            json!({
                "type": "assistant.message.delta",
                "run_id": "run_1",
                "payload": {"generation": 1, "sequence": 1, "content": "wrong key"}
            }),
            json!({
                "type": "assistant.message.delta",
                "run_id": "run_1",
                "payload": {"generation": 1, "sequence": 1, "phase": "private_reasoning", "delta": "hidden"}
            }),
            json!({
                "type": "assistant.message.completed",
                "run_id": "run_1",
                "payload": {"generation": 1, "sequence": 1, "phase": "final_answer", "content": 42}
            }),
            json!({
                "type": "reasoner.raw.delta",
                "run_id": "run_1",
                "payload": {"generation": 1, "sequence": 1, "delta": "private"}
            }),
        ] {
            let event: WireEvent = serde_json::from_value(value).unwrap();
            assert_eq!(event.assistant_message_update(), None);
        }
    }

    #[test]
    fn feedback_milestones_cover_live_fallbacks_and_reject_malformed_events() {
        let cases = [
            (
                json!({
                    "type":"assistant.message.delta", "run_id":"run_1",
                    "payload":{"generation":1,"sequence":1,"phase":"final_answer","delta":"hi"}
                }),
                FeedbackPhase::FirstToken,
                "run_1",
            ),
            (
                json!({"type":"ToolCallRequested","run_id":"run_1","payload":{"call_id":"call_1"}}),
                FeedbackPhase::ToolStart,
                "call_1",
            ),
            (
                json!({"type":"ToolCallStarted","run_id":"run_1","payload":{"call_id":"call_legacy"}}),
                FeedbackPhase::ToolStart,
                "call_legacy",
            ),
            (
                json!({
                    "type":"ToolCallApprovalRequired", "run_id":"run_1",
                    "payload":{"call_id":"call_1","decision_id":"decision_1"}
                }),
                FeedbackPhase::ApprovalWait,
                "decision_1",
            ),
            (
                json!({
                    "type":"ToolCallApprovalRequired", "run_id":"run_1",
                    "payload":{"call_id":"call_fallback"}
                }),
                FeedbackPhase::ApprovalWait,
                "call_fallback",
            ),
            (
                json!({"type":"ToolCallCancelled","run_id":"run_1","payload":{"call_id":"call_1"}}),
                FeedbackPhase::ToolResult,
                "call_1",
            ),
            (
                json!({"type":"ExecutionCompleted","run_id":"run_1","payload":{"summary":"done"}}),
                FeedbackPhase::Completion,
                "run_1",
            ),
            (
                json!({"type":"ExecutionFailed","run_id":"run_failed","payload":{"error":"no"}}),
                FeedbackPhase::Completion,
                "run_failed",
            ),
        ];
        for (value, phase, key) in cases {
            let event: WireEvent = serde_json::from_value(value).unwrap();
            assert_eq!(
                event.feedback_milestone(),
                Some(FeedbackMilestone {
                    phase,
                    key: key.into(),
                })
            );
        }

        let malformed: WireEvent = serde_json::from_value(json!({
            "type":"ToolCallStarted", "run_id":"run_1", "payload":{}
        }))
        .unwrap();
        assert_eq!(malformed.feedback_milestone(), None);

        let reset: WireEvent = serde_json::from_value(json!({
            "type":"assistant.message.reset", "run_id":"run_1",
            "payload":{"generation":2,"sequence":1,"phase":"final_answer"}
        }))
        .unwrap();
        assert_eq!(reset.feedback_milestone(), None);
    }

    fn runnable_inventory(reasoner_available: bool) -> ModelInventory {
        ModelInventory {
            default_model: "provider/model".into(),
            reasoner: ModelReasoner {
                backend: "model-router".into(),
                available: reasoner_available,
                ..Default::default()
            },
            providers: vec![ModelProvider {
                id: "provider".into(),
                name: "Provider".into(),
                registered: true,
                available: true,
                auth_source: "auth:provider".into(),
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
                models: vec![Model {
                    id: "provider/model".into(),
                    display_id: String::new(),
                    name: "Model".into(),
                    available: true,
                    status: String::new(),
                    status_reason: String::new(),
                    reasoning: false,
                    reasoning_efforts: Vec::new(),
                    default_reasoning_effort: String::new(),
                    image_input: false,
                    tool_call: true,
                }],
            }],
            readiness: ExecutionReadiness::default(),
        }
    }

    #[test]
    fn provider_selection_requires_execution_ready_reasoner() {
        let unavailable = runnable_inventory(false);
        assert!(!unavailable.has_runnable_provider());
        assert!(unavailable.available_models().is_empty());

        let available = runnable_inventory(true);
        assert!(available.has_runnable_provider());
        assert_eq!(available.available_models().len(), 1);
    }

    #[test]
    fn model_is_usable_reflects_route_health() {
        let mut model = Model {
            id: "ccswitch-claude/claude-opus-5".into(),
            display_id: "claude-opus-5".into(),
            name: "Claude Opus 5".into(),
            available: true,
            status: String::new(),
            status_reason: String::new(),
            reasoning: true,
            reasoning_efforts: Vec::new(),
            default_reasoning_effort: String::new(),
            image_input: false,
            tool_call: true,
        };
        assert!(model.is_usable());
        assert_eq!(model.health_status(), "ready");

        model.status = "rate_limited".into();
        model.status_reason = "Recently rate-limited".into();
        assert!(!model.is_usable());
        assert_eq!(model.health_status(), "rate_limited");

        model.status = "circuit_open".into();
        assert!(!model.is_usable());

        model.status = "probing".into();
        assert!(model.is_usable());

        model.available = false;
        model.status = "ready".into();
        assert!(!model.is_usable());
        assert_eq!(model.health_status(), "unavailable");
    }

    #[test]
    fn model_inventory_decodes_authoritative_readiness_and_legacy_absence() {
        let current: ModelInventory = serde_json::from_value(json!({
            "default_model": "openai/gpt-5",
            "providers": [],
            "reasoner": {"available": true},
            "readiness": {
                "step": "conversation",
                "blockers": [],
                "route_kind": "credential_source",
                "model_id": "openai/gpt-5",
                "locale": "zh",
                "can_submit": true,
                "epoch": "runtime-1",
                "generation": 7
            }
        }))
        .unwrap();
        assert!(current.readiness.can_submit);
        assert_eq!(current.readiness.generation, 7);
        assert_eq!(current.readiness.locale, "zh");

        let legacy: ModelInventory = serde_json::from_value(json!({
            "default_model": "openai/gpt-5",
            "providers": [],
            "reasoner": {"available": true}
        }))
        .unwrap();
        assert_eq!(legacy.readiness.generation, 0);
        assert!(!legacy.readiness.can_submit);
    }

    #[test]
    fn governance_resolution_projects_typed_tool_decisions() {
        let approval: WireEvent = serde_json::from_value(json!({
            "type": "ToolApproved",
            "payload": {"decision_id": "perm_1"}
        }))
        .unwrap();
        assert_eq!(
            approval.governance_resolution(),
            Some(GovernanceId::Approval("perm_1".into()))
        );

        let denial: WireEvent = serde_json::from_value(json!({
            "type": "ToolDenied",
            "decision_id": "perm_2"
        }))
        .unwrap();
        assert_eq!(
            denial.governance_resolution(),
            Some(GovernanceId::Approval("perm_2".into()))
        );

        let question: WireEvent = serde_json::from_value(json!({
            "type": "ExecutionProgressed",
            "run_id": "run_1",
            "payload": {"status": "user_question_resolved", "question_id": "q_1"}
        }))
        .unwrap();
        assert_eq!(
            question.governance_resolution(),
            Some(GovernanceId::Question("q_1".into()))
        );
    }

    #[test]
    fn typed_execution_lifecycle_owns_active_state() {
        for (kind, status, lifecycle) in [
            ("ExecutionQueued", None, ExecutionLifecycle::Queued),
            ("ExecutionStarted", None, ExecutionLifecycle::Started),
            (
                "ExecutionProgressed",
                Some("waiting_input"),
                ExecutionLifecycle::WaitingInput,
            ),
            (
                "ExecutionInterrupted",
                None,
                ExecutionLifecycle::Interrupted,
            ),
            ("ExecutionCompleted", None, ExecutionLifecycle::Completed),
            ("ExecutionFailed", None, ExecutionLifecycle::Failed),
            ("ExecutionCancelled", None, ExecutionLifecycle::Cancelled),
            (
                "execution.completed",
                Some("degraded"),
                ExecutionLifecycle::Degraded,
            ),
        ] {
            let mut value = json!({
                "type": kind,
                "run_id": "run_1",
            });
            if let Some(status) = status {
                value["status"] = status.into();
            }
            let event: WireEvent = serde_json::from_value(value).unwrap();
            assert_eq!(event.execution_lifecycle(), Some(lifecycle));
            assert_eq!(event.is_execution_terminal(), lifecycle.is_terminal());
            assert_eq!(
                event.execution_activity_status(),
                lifecycle.is_active().then_some(lifecycle.status())
            );
            assert_eq!(event.clears_active_execution(), lifecycle.clears_active());
        }

        let degraded: WireEvent = serde_json::from_value(json!({
            "type": "ExecutionFailed",
            "run_id": "run_1",
            "payload": {"outcome": "degraded", "reason": "provider unavailable"}
        }))
        .unwrap();
        assert_eq!(
            degraded.execution_lifecycle(),
            Some(ExecutionLifecycle::Degraded)
        );

        let unrelated_progress: WireEvent = serde_json::from_value(json!({
            "type": "ExecutionProgressed",
            "run_id": "run_1",
            "payload": {"status": "checkpoint_restored", "checkpoint_id": "run_1:2"}
        }))
        .unwrap();
        assert_eq!(unrelated_progress.execution_lifecycle(), None);
        assert_eq!(unrelated_progress.execution_activity_status(), None);
        assert!(!unrelated_progress.clears_active_execution());
    }

    #[test]
    fn lifecycle_reducer_rejects_duplicates_and_illegal_regressions() {
        let event = |event_id: &str, kind: &str, status: Option<&str>| {
            let mut value = json!({
                "event_id": event_id,
                "type": kind,
                "run_id": "run_1",
                "payload": {}
            });
            if let Some(status) = status {
                value["payload"]["status"] = status.into();
            }
            serde_json::from_value::<WireEvent>(value).unwrap()
        };
        let mut reducer = ExecutionLifecycleReducer::default();
        let unknown = event("evt_unknown", "FutureLifecycleEvent", None);
        assert_eq!(
            reducer.reduce(&unknown),
            ExecutionLifecycleReduction::NotLifecycle
        );
        assert_eq!(
            reducer.reduce(&unknown),
            ExecutionLifecycleReduction::Ignored
        );
        assert_eq!(
            reducer.reduce(&event("evt_unknown_done", "ExecutionCompleted", None)),
            ExecutionLifecycleReduction::Ignored
        );
        let queued = event("evt_queued", "ExecutionQueued", None);
        assert_eq!(
            reducer.reduce(&queued),
            ExecutionLifecycleReduction::Accepted(ExecutionLifecycle::Queued)
        );
        assert_eq!(
            reducer.reduce(&queued),
            ExecutionLifecycleReduction::Ignored
        );
        assert_eq!(
            reducer.reduce(&event("evt_early_done", "ExecutionCompleted", None)),
            ExecutionLifecycleReduction::Ignored
        );
        assert_eq!(
            reducer.reduce(&event("evt_started", "ExecutionStarted", None)),
            ExecutionLifecycleReduction::Accepted(ExecutionLifecycle::Started)
        );
        assert_eq!(
            reducer.reduce(&event(
                "evt_waiting",
                "ExecutionProgressed",
                Some("waiting_approval")
            )),
            ExecutionLifecycleReduction::Accepted(ExecutionLifecycle::WaitingApproval)
        );
        assert_eq!(
            reducer.reduce(&event("evt_resumed", "ExecutionStarted", None)),
            ExecutionLifecycleReduction::Accepted(ExecutionLifecycle::Started)
        );
        assert_eq!(
            reducer.reduce(&event("evt_done", "ExecutionCompleted", None)),
            ExecutionLifecycleReduction::Accepted(ExecutionLifecycle::Completed)
        );
        assert_eq!(
            reducer.reduce(&event("evt_regression", "ExecutionStarted", None)),
            ExecutionLifecycleReduction::Ignored
        );

        let mut legacy = ExecutionLifecycleReducer::default();
        assert_eq!(
            legacy.reduce(&event("evt_legacy_started", "ExecutionStarted", None)),
            ExecutionLifecycleReduction::Accepted(ExecutionLifecycle::Started)
        );

        let mut snapshot = ExecutionLifecycleReducer::default();
        assert!(snapshot.seed("run_snapshot", ExecutionLifecycle::WaitingApproval));
        assert!(!snapshot.seed("run_snapshot", ExecutionLifecycle::Queued));
        assert_eq!(
            ExecutionLifecycle::from_status("needs_input"),
            Some(ExecutionLifecycle::WaitingInput)
        );
    }

    #[test]
    fn legacy_task_id_aliases_to_run_identity_at_decode_boundaries() {
        let wire: WireEvent = serde_json::from_value(json!({
            "event_id": "evt_1",
            "type": "ExecutionStarted",
            "task_id": "run_legacy"
        }))
        .unwrap();
        assert_eq!(wire.run_id, "run_legacy");

        let item: SessionItemEvent = serde_json::from_value(json!({
            "type": "item.started",
            "session_id": "sess_1",
            "task_id": "run_legacy",
            "item": {
                "id": "call_1",
                "type": "tool_call",
                "task_id": "run_legacy"
            }
        }))
        .unwrap();
        assert_eq!(item.run_id, "run_legacy");
        assert_eq!(item.item.unwrap().run_id, "run_legacy");
    }

    #[test]
    fn completed_tool_receipt_never_terminates_the_parent_execution() {
        let event: WireEvent = serde_json::from_value(json!({
            "type": "ToolCallCompleted",
            "run_id": "run_1",
            "payload": {"call_id": "call_1", "tool": "run", "status": "completed"}
        }))
        .unwrap();
        assert!(!event.is_execution_terminal());
        assert!(!event.clears_active_execution());
        assert_eq!(event.execution_activity_status(), None);
        assert_eq!(event.stable_id(), "tool:call_1");
    }

    #[test]
    fn completed_tool_event_projects_the_first_scoped_artifact() {
        let event: WireEvent = serde_json::from_value(json!({
            "type": "ToolCallCompleted",
            "session_id": "sess-1",
            "run_id": "run-1",
            "payload": {
                "call_id": "call-1",
                "artifact_ids": ["artifact-1", "artifact-2"]
            }
        }))
        .unwrap();

        assert_eq!(
            event.tool_artifact_ref(),
            Some(ToolArtifactRef {
                session_id: "sess-1".into(),
                run_id: "run-1".into(),
                call_id: "call-1".into(),
                artifact_id: "artifact-1".into(),
            })
        );
    }

    #[test]
    fn live_activity_uses_typed_identity_and_terminal_events() {
        let tool: WireEvent = serde_json::from_value(json!({
            "type": "ToolCallStarted",
            "run_id": "run_1",
            "payload": {"call_id": "call_1", "tool": "test", "private_trace": "must not render"}
        }))
        .unwrap();
        assert_eq!(
            tool.live_activity_update(),
            Some(LiveActivityUpdate::Started {
                key: "tool:call_1".into(),
                label: "test".into(),
            })
        );

        let completed: WireEvent = serde_json::from_value(json!({
            "type": "ToolCallCompleted",
            "run_id": "run_1",
            "payload": {"call_id": "call_1", "tool": "test"}
        }))
        .unwrap();
        assert_eq!(
            completed.live_activity_update(),
            Some(LiveActivityUpdate::Finished {
                key: "tool:call_1".into(),
            })
        );

        let unknown: WireEvent = serde_json::from_value(json!({
            "type": "InternalDiagnostic",
            "run_id": "run_1",
            "summary": "private state"
        }))
        .unwrap();
        assert_eq!(unknown.live_activity_update(), None);
    }

    #[test]
    fn artifact_projection_rejects_incomplete_or_nonterminal_events() {
        for value in [
            json!({
                "type": "ToolCallStarted",
                "session_id": "sess-1",
                "payload": {"call_id": "call-1", "artifact_ids": ["artifact-1"]}
            }),
            json!({
                "type": "ToolCallCompleted",
                "session_id": "sess-1",
                "payload": {"call_id": "", "artifact_ids": ["artifact-1"]}
            }),
            json!({
                "type": "ToolCallCompleted",
                "session_id": "sess-1",
                "payload": {"call_id": "call-1", "artifact_ids": [""]}
            }),
            json!({
                "type": "ToolCallCompleted",
                "session_id": "",
                "payload": {"call_id": "call-1", "artifact_ids": ["artifact-1"]}
            }),
        ] {
            let event: WireEvent = serde_json::from_value(value).unwrap();
            assert_eq!(event.tool_artifact_ref(), None);
        }
    }

    #[test]
    fn media_upload_results_decode_both_wire_shapes() {
        let pending: MediaUploadResult = serde_json::from_value(json!({
            "upload_id": "upload-1",
            "next_chunk_index": 1
        }))
        .unwrap();
        assert_eq!(
            pending,
            MediaUploadResult::Pending(MediaUploadReceipt {
                upload_id: "upload-1".into(),
                next_chunk_index: 1,
            })
        );

        let complete: MediaUploadResult = serde_json::from_value(json!({
            "artifact_id": "a".repeat(64),
            "media_type": "image/png",
            "bytes": 42,
            "origin": "clipboard"
        }))
        .unwrap();
        assert_eq!(
            complete,
            MediaUploadResult::Complete(MediaRef {
                artifact_id: "a".repeat(64),
                media_type: "image/png".into(),
                bytes: 42,
                origin: "clipboard".into(),
            })
        );
    }

    #[test]
    fn legacy_task_created_never_claims_execution_or_governance_state() {
        let legacy: WireEvent = serde_json::from_value(json!({
            "type": "TaskCreated",
            "run_id": "run_1",
            "payload": {"status": "completed", "decision_id": "perm_1"}
        }))
        .unwrap();
        assert_eq!(legacy.execution_lifecycle(), None);
        assert_eq!(legacy.execution_activity_status(), None);
        assert!(!legacy.clears_active_execution());
        assert_eq!(legacy.governance_resolution(), None);
    }

    #[test]
    fn execution_run_rejects_legacy_task_id_shape() {
        let legacy = serde_json::from_value::<ExecutionRun>(json!({
            "task_id": "task_1",
            "session_id": "sess_1",
            "status": "running",
            "agent": "build",
            "user_prompt": "ship"
        }));
        assert!(legacy.is_err());
    }

    #[test]
    fn runtime_initialize_rejects_a_daemon_without_required_methods() {
        let runtime: RuntimeInitialize = serde_json::from_value(json!({
            "runtime_version": "0.6.4",
            "protocol_version": "1.3.0",
            "projection_version": "1.0.0",
            "capabilities": {"rpc_methods": ["session.list"]}
        }))
        .unwrap();

        let error = runtime
            .require_methods(&["session.list", "execution.start"])
            .unwrap_err()
            .to_string();
        assert!(error.contains("incompatible Carina runtime 0.6.4"));
        assert!(error.contains("execution.start"));
        assert!(error.contains("restart the workspace runtime"));
    }

    #[test]
    fn legacy_runtime_without_method_inventory_is_not_assumed_compatible() {
        let runtime: RuntimeInitialize = serde_json::from_value(json!({
            "runtime_version": "0.6.4",
            "protocol_version": "1.3.0",
            "projection_version": "1.0.0"
        }))
        .unwrap();

        assert!(runtime.require_methods(&["execution.start"]).is_err());
    }

    /// Daemon-shaped JSON fixtures for every product-decoded contract type
    /// used by the Rust TUI journey (Wave 1 closure).
    #[test]
    fn daemon_owned_json_fixtures_decode_product_contracts() {
        let cases: Vec<(&str, serde_json::Value)> = vec![
            (
                "RuntimeInitialize",
                json!({
                    "runtime_version": "0.7.0",
                    "protocol_version": "1.3.0",
                    "projection_version": "1.0.0",
                    "capabilities": {
                        "rpc_methods": [
                            "execution.start",
                            "execution.cancel",
                            "session.list",
                            "model.list",
                            "events.subscribe"
                        ]
                    }
                }),
            ),
            (
                "ExecutionRun",
                json!({
                    "run_id": "run_fixture_1",
                    "session_id": "sess_1",
                    "status": "running",
                    "agent": "build",
                    "user_prompt": "ship it",
                    "summary": ""
                }),
            ),
            (
                "Session",
                json!({
                    "session_id": "sess_1",
                    "workspace_root": "/tmp/ws",
                    "created_at": "2026-07-30T00:00:00Z",
                    "title": "fixture",
                    "status": "active"
                }),
            ),
            (
                "ModelInventory",
                json!({
                    "providers": [{
                        "id": "xai",
                        "name": "xAI",
                        "registered": true,
                        "available": true,
                        "models": [{
                            "id": "grok",
                            "name": "Grok",
                            "provider_id": "xai"
                        }]
                    }]
                }),
            ),
            (
                "WireEvent",
                json!({
                    "type": "assistant.delta",
                    "run_id": "run_fixture_1",
                    "payload": {"text": "hello"}
                }),
            ),
            (
                "MediaRef",
                json!({
                    "artifact_id": "a".repeat(64),
                    "media_type": "image/png",
                    "bytes": 42,
                    "origin": "clipboard"
                }),
            ),
            (
                "Checkpoint",
                json!({
                    "checkpoint_id": "cp_1",
                    "created_at": "2026-07-30T00:00:00Z",
                    "sequence": "1",
                    "run_id": "run_fixture_1",
                    "session_id": "sess_1",
                    "turn": 1,
                    "summary": "before edit"
                }),
            ),
            (
                "AgentView",
                json!({
                    "needs_input": [],
                    "working": [{"task_id": "task_bg_1", "category": "working"}],
                    "completed": []
                }),
            ),
            (
                "WorkspaceDiff",
                json!({
                    "files": [{
                        "path": "src/lib.rs",
                        "status": "modified",
                        "additions": 1,
                        "deletions": 0
                    }]
                }),
            ),
            (
                "SessionReview",
                json!({
                    "session_id": "sess_1",
                    "changes": [],
                    "rollback": {"available": false, "patch_ids": []}
                }),
            ),
            (
                "ContextSummary",
                json!({
                    "session_id": "sess_1",
                    "model_context_tokens": {
                        "tokens": 100,
                        "limit_tokens": 32000,
                        "used_percent": 1,
                        "estimated_limit": true,
                        "metadata_source": "fallback-estimate"
                    },
                    "task": {"mode": "foreground", "status": "running"},
                    "compaction_policy": {
                        "policy_version": "model-window-v1",
                        "window_tokens": 32000,
                        "reserve_tokens": 4000,
                        "trigger_tokens": 22400,
                        "metadata_source": "fallback-estimate"
                    },
                    "recent_receipt": {
                        "pressure_before": 1.02,
                        "summary_sha256": "abcdef123456",
                        "key_files": ["src/lib.rs"]
                    }
                }),
            ),
            (
                "PlanModeState",
                json!({"session_id": "sess_1", "plan_mode": false}),
            ),
            (
                "PromptHistory",
                json!({"entries": ["one", "two"], "count": 2, "scope": "workspace"}),
            ),
        ];

        for (name, value) in cases {
            match name {
                "RuntimeInitialize" => {
                    let decoded: RuntimeInitialize = serde_json::from_value(value).unwrap();
                    decoded
                        .require_methods(&["execution.start", "session.list"])
                        .unwrap();
                }
                "ExecutionRun" => {
                    let decoded: ExecutionRun = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.run_id, "run_fixture_1");
                    assert!(!decoded.run_id.starts_with("task_"));
                }
                "Session" => {
                    let decoded: Session = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.session_id, "sess_1");
                }
                "ModelInventory" => {
                    let decoded: ModelInventory = serde_json::from_value(value).unwrap();
                    assert!(!decoded.providers.is_empty());
                }
                "WireEvent" => {
                    let decoded: WireEvent = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.kind, "assistant.delta");
                }
                "MediaRef" => {
                    let decoded: MediaRef = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.bytes, 42);
                }
                "Checkpoint" => {
                    let decoded: Checkpoint = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.run_id, "run_fixture_1");
                }
                "AgentView" => {
                    let decoded: AgentView = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.working[0].task_id, "task_bg_1");
                }
                "WorkspaceDiff" => {
                    let decoded: WorkspaceDiff = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.files[0].path, "src/lib.rs");
                }
                "SessionReview" => {
                    let _: SessionReview = serde_json::from_value(value).unwrap();
                }
                "ContextSummary" => {
                    let decoded: ContextSummary = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.session_id, "sess_1");
                    assert!(decoded.model_context_tokens.estimated_limit);
                    assert_eq!(decoded.compaction_policy.reserve_tokens, 4000);
                    assert_eq!(
                        decoded.recent_receipt.unwrap().summary_sha256,
                        "abcdef123456"
                    );
                }
                "PlanModeState" => {
                    let decoded: PlanModeState = serde_json::from_value(value).unwrap();
                    assert!(!decoded.plan_mode);
                }
                "PromptHistory" => {
                    let decoded: PromptHistory = serde_json::from_value(value).unwrap();
                    assert_eq!(decoded.entries.len(), 2);
                }
                other => panic!("unhandled fixture {other}"),
            }
        }
    }

    #[test]
    fn daemon_owned_fixtures_reject_legacy_task_submit_shape() {
        // Old task.submit style payloads must not decode as ExecutionRun.
        assert!(
            serde_json::from_value::<ExecutionRun>(json!({
                "task_id": "task_legacy",
                "session_id": "sess_1",
                "status": "running"
            }))
            .is_err()
        );
        // Wire events without a typed kind stay non-governance.
        let bare: WireEvent = serde_json::from_value(json!({
            "type": "task.created",
            "payload": {}
        }))
        .unwrap();
        assert_eq!(bare.execution_lifecycle(), None);
    }

    #[test]
    fn typed_plan_result_kind_decodes_additively() {
        let event: WireEvent = serde_json::from_value(json!({
            "type": "execution.completed",
            "run_id": "run_1",
            "status": "completed",
            "result_kind": "answer"
        }))
        .unwrap();
        assert_eq!(event.execution_result_kind(), Some("answer"));

        let nested: WireEvent = serde_json::from_value(json!({
            "type": "ExecutionCompleted",
            "run_id": "run_2",
            "payload": {"result_kind": "plan"}
        }))
        .unwrap();
        assert_eq!(nested.execution_result_kind(), Some("plan"));

        let legacy: WireEvent = serde_json::from_value(json!({
            "type": "ExecutionCompleted",
            "run_id": "run_old",
            "payload": {}
        }))
        .unwrap();
        assert_eq!(legacy.execution_result_kind(), None);

        let session: Session = serde_json::from_value(json!({
            "session_id": "sess_1",
            "latest_task_id": "run_1",
            "latest_task_agent": "plan",
            "task_status": "completed",
            "latest_run_result_kind": "answer"
        }))
        .unwrap();
        assert_eq!(session.latest_run_id, "run_1");
        assert_eq!(session.latest_run_agent, "plan");
        assert_eq!(session.execution_status, "completed");
        assert_eq!(session.latest_run_result_kind, "answer");

        let run: ExecutionRun = serde_json::from_value(json!({
            "run_id": "run_1",
            "session_id": "sess_1",
            "result_kind": "plan"
        }))
        .unwrap();
        assert_eq!(run.result_kind, "plan");
        assert!(run.requested_model.is_empty());

        let routed: ExecutionRun = serde_json::from_value(json!({
            "run_id": "run_2",
            "session_id": "sess_1",
            "model": "provider/requested",
            "requested_model": "provider/requested",
            "effective_model": "provider/effective",
            "requested_reasoning_effort": "high",
            "effective_reasoning_effort": "medium"
        }))
        .unwrap();
        assert_eq!(routed.requested_model, "provider/requested");
        assert_eq!(routed.effective_model, "provider/effective");
        assert_eq!(routed.requested_reasoning_effort, "high");
        assert_eq!(routed.effective_reasoning_effort, "medium");
    }
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ExecutionRun {
    pub run_id: String,
    #[serde(default)]
    pub retry_of_run_id: String,
    pub session_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub agent: String,
    #[serde(default)]
    pub user_prompt: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub result_kind: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub requested_model: String,
    #[serde(default)]
    pub effective_model: String,
    #[serde(default)]
    pub requested_reasoning_effort: String,
    #[serde(default)]
    pub effective_reasoning_effort: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RetryRouting {
    Current,
    Original,
}

impl RetryRouting {
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Current => "current",
            Self::Original => "original",
        }
    }
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
pub struct Checkpoint {
    pub checkpoint_id: String,
    #[serde(default)]
    pub parent_checkpoint_id: String,
    pub created_at: String,
    pub sequence: String,
    pub run_id: String,
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
    pub run_id: String,
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
