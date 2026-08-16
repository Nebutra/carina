use std::collections::{BTreeMap, HashMap, HashSet, VecDeque};

use serde_json::Value;

use crate::i18n::MessageId;
use crate::rpc::{
    AssistantMessagePhase, AssistantMessageUpdate, AssistantTailState, ExecutionLifecycle,
    SessionItemEvent, WireEvent,
};
use crate::tool_projection::{TodoItem, ToolFacts, ToolKind, present as present_tool};
use crate::{i18n, i18n::Locale};

fn failure_kind_message_id(kind: FailureKind) -> MessageId {
    match kind {
        FailureKind::Failed => MessageId::FailureTitleFailed,
        FailureKind::ApprovalDenied => MessageId::FailureTitleApprovalDenied,
        FailureKind::Interrupted => MessageId::FailureTitleInterrupted,
        FailureKind::Cancelled => MessageId::FailureTitleCancelled,
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BlockKind {
    User,
    Assistant,
    Thinking,
    Tool,
    Governance,
    Status,
    Diagnostic,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BlockBodyKind {
    Plain,
    Diff,
    CodeIntelligence,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum UserBlockKind {
    #[default]
    Prompt,
    Steer,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FailureKind {
    Failed,
    ApprovalDenied,
    Interrupted,
    Cancelled,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FailureAction {
    Retry,
    RunAgain,
    Recovering,
    Disabled,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FailureRecoveryAction {
    RetryCurrent,
    ReplayOriginal,
    Details,
    CopyId,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FailurePresentation {
    pub kind: FailureKind,
    pub action: FailureAction,
    pub owner: String,
    pub reason: String,
    pub source_event_id: String,
    pub run_id: String,
    pub model: String,
    pub current_model: String,
    pub retry_root_run_id: String,
    pub attempt_count: usize,
    pub focused_action: Option<FailureRecoveryAction>,
}

impl FailurePresentation {
    pub fn available_actions(&self, current_model: &str) -> Vec<FailureRecoveryAction> {
        let mut actions = Vec::with_capacity(4);
        if matches!(self.action, FailureAction::Retry | FailureAction::RunAgain) {
            actions.push(FailureRecoveryAction::RetryCurrent);
            let failed_model = self.model.trim();
            let current_model = current_model.trim();
            if !failed_model.is_empty()
                && !current_model.is_empty()
                && failed_model != current_model
            {
                actions.push(FailureRecoveryAction::ReplayOriginal);
            }
        }
        actions.push(FailureRecoveryAction::Details);
        actions.push(FailureRecoveryAction::CopyId);
        actions
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ToolGroupMember {
    pub id: String,
    pub tool_name: String,
    pub title: String,
    pub body: String,
    pub body_kind: BlockBodyKind,
    pub additions: usize,
    pub deletions: usize,
    pub status: String,
    pub lifecycle: String,
}

impl ToolGroupMember {
    pub fn is_running(&self) -> bool {
        !is_terminal_tool_status(&self.lifecycle)
    }

    pub fn is_failure(&self) -> bool {
        is_failure_status(&self.lifecycle)
    }
}

#[derive(Debug, Clone)]
pub struct TranscriptBlock {
    pub id: String,
    pub run_id: String,
    pub kind: BlockKind,
    pub assistant_phase: Option<AssistantMessagePhase>,
    pub user_kind: UserBlockKind,
    pub tool_kind: Option<ToolKind>,
    pub tool_members: Vec<ToolGroupMember>,
    pub todo_items: Vec<TodoItem>,
    pub failure: Option<FailurePresentation>,
    pub title: String,
    pub body: String,
    pub body_kind: BlockBodyKind,
    pub additions: usize,
    pub deletions: usize,
    pub status: String,
    pub collapsible: bool,
    pub expanded: bool,
    pub selected: bool,
    pub source_prompt: String,
    pub branchable: bool,
    pub layout_revision: u64,
}

impl TranscriptBlock {
    pub fn localized_title(&self, locale: Locale) -> String {
        match self.tool_group_state() {
            Some((kind, count, running, _)) => {
                let paths = crate::tool_projection::tool_group_path_titles(
                    self.tool_members.iter().map(|member| member.title.as_str()),
                );
                i18n::tool_group_title(locale, kind, count, running, &paths)
            }
            None => {
                if let Some(failure) = self.failure.as_ref() {
                    return i18n::text(locale, failure_kind_message_id(failure.kind)).to_owned();
                }
                self.title.clone()
            }
        }
    }

    pub fn localized_status(&self, locale: Locale) -> String {
        match self.tool_group_state() {
            Some((_, _, _, failures)) => i18n::tool_group_failure(locale, failures),
            None => i18n::localize_tool_status(locale, &self.status),
        }
    }

    /// Operator-facing failure reason for the active locale.
    pub fn localized_failure_reason(&self, locale: Locale) -> Option<String> {
        self.failure
            .as_ref()
            .map(|failure| i18n::localize_operator_failure_reason(locale, &failure.reason))
    }

    fn tool_group_state(&self) -> Option<(ToolKind, usize, bool, usize)> {
        let kind = self.tool_kind?;
        (self.tool_members.len() > 1).then(|| {
            let running = self.tool_members.iter().any(ToolGroupMember::is_running);
            let failures = self
                .tool_members
                .iter()
                .filter(|member| member.is_failure())
                .count();
            (kind, self.tool_members.len(), running, failures)
        })
    }

    pub fn local_user(id: String, prompt: String) -> Self {
        Self {
            id,
            run_id: String::new(),
            kind: BlockKind::User,
            assistant_phase: None,
            user_kind: UserBlockKind::Prompt,
            tool_kind: None,
            tool_members: Vec::new(),
            todo_items: Vec::new(),
            failure: None,
            title: String::new(),
            body: prompt.clone(),
            body_kind: BlockBodyKind::Plain,
            additions: 0,
            deletions: 0,
            status: String::new(),
            collapsible: false,
            expanded: true,
            selected: false,
            source_prompt: prompt,
            branchable: false,
            layout_revision: 1,
        }
    }

    pub fn local_steer(id: String, run_id: String, prompt: String) -> Self {
        Self {
            id,
            run_id,
            kind: BlockKind::User,
            assistant_phase: None,
            user_kind: UserBlockKind::Steer,
            tool_kind: None,
            tool_members: Vec::new(),
            todo_items: Vec::new(),
            failure: None,
            title: String::new(),
            body: prompt.clone(),
            body_kind: BlockBodyKind::Plain,
            additions: 0,
            deletions: 0,
            status: "queued".into(),
            collapsible: false,
            expanded: true,
            selected: false,
            source_prompt: prompt,
            branchable: false,
            layout_revision: 1,
        }
    }

    pub fn is_collapsible(&self) -> bool {
        self.collapsible
    }
}

#[derive(Debug, Clone, Default)]
struct ToolRecord {
    id: String,
    run_id: String,
    tool: String,
    status: String,
    details: BTreeMap<String, Value>,
}

#[derive(Debug, Clone, Default)]
struct AssistantStream {
    generation: u64,
    sequence: u64,
    phase: Option<AssistantMessagePhase>,
    body: String,
    structured_output: bool,
    awaiting_canonical: bool,
}

#[derive(Debug, Clone, Default)]
struct RunMetadata {
    requested_model: String,
    effective_model: String,
    model: String,
    retry_of_run_id: String,
}

impl RunMetadata {
    fn actual_model(&self) -> String {
        [
            self.effective_model.as_str(),
            self.model.as_str(),
            self.requested_model.as_str(),
        ]
        .into_iter()
        .find(|model| !model.is_empty())
        .unwrap_or_default()
        .to_owned()
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
struct FailureContext {
    model: String,
    retry_root_run_id: String,
    attempt_count: usize,
}

#[derive(Debug, Default)]
pub struct TranscriptReducer {
    tools: HashMap<String, ToolRecord>,
    active_tool_by_run: HashMap<String, String>,
    patch_tool_by_patch_id: HashMap<String, String>,
    commands: HashMap<String, ToolRecord>,
    open_command_by_run: HashMap<String, String>,
    assistant_streams: HashMap<String, AssistantStream>,
    run_metadata: HashMap<String, RunMetadata>,
    seen_event_ids: HashSet<String>,
    event_id_order: VecDeque<String>,
}

impl TranscriptReducer {
    const SEEN_EVENT_LIMIT: usize = 4_096;

    pub fn apply_tool_output(
        &mut self,
        blocks: &mut Vec<TranscriptBlock>,
        call_id: &str,
        output: String,
        truncated: bool,
    ) -> bool {
        let id = format!("tool:{call_id}");
        let Some(record) = self.tools.get_mut(&id) else {
            return false;
        };
        record
            .details
            .insert("artifact_output".into(), Value::String(output));
        record
            .details
            .insert("artifact_output_truncated".into(), Value::Bool(truncated));
        upsert_block(blocks, tool_block(record))
    }

    pub fn hydrate(&mut self, items: Vec<SessionItemEvent>) -> Vec<TranscriptBlock> {
        *self = Self::default();
        let mut blocks = Vec::new();
        for item in items {
            self.reduce_item(&mut blocks, item);
        }
        blocks
    }

    fn record_run_metadata(&mut self, run_id: &str, details: &BTreeMap<String, Value>) {
        let run_id = run_id.trim();
        if run_id.is_empty() {
            return;
        }
        let metadata = self.run_metadata.entry(run_id.to_owned()).or_default();
        if let Some(value) = detail(details, "requested_model") {
            metadata.requested_model = value;
        }
        if let Some(value) = detail(details, "effective_model") {
            metadata.effective_model = value;
        }
        if let Some(value) = detail(details, "model") {
            metadata.model = value;
        }
        if let Some(value) = detail(details, "retry_of_run_id") {
            metadata.retry_of_run_id = value;
        }
    }

    fn failure_context(&self, run_id: &str) -> FailureContext {
        let run_id = run_id.trim();
        if run_id.is_empty() {
            return FailureContext::default();
        }

        let model = self
            .run_metadata
            .get(run_id)
            .map(RunMetadata::actual_model)
            .unwrap_or_default();
        let (retry_root_run_id, attempt_count) = self.retry_lineage(run_id);
        FailureContext {
            model,
            retry_root_run_id,
            attempt_count,
        }
    }

    fn retry_lineage(&self, run_id: &str) -> (String, usize) {
        let run_id = run_id.trim();
        if run_id.is_empty() {
            return (String::new(), 0);
        }
        let mut retry_root_run_id = run_id.to_owned();
        let mut attempt_count = 1;
        let mut cursor = run_id;
        let mut seen = HashSet::from([run_id.to_owned()]);
        while let Some(parent) = self
            .run_metadata
            .get(cursor)
            .map(|metadata| metadata.retry_of_run_id.trim())
            .filter(|parent| !parent.is_empty())
        {
            if !seen.insert(parent.to_owned()) {
                break;
            }
            retry_root_run_id = parent.to_owned();
            attempt_count += 1;
            cursor = parent;
        }
        (retry_root_run_id, attempt_count)
    }

    fn user_block_identity(&self, fallback_id: String, run_id: &str) -> String {
        let (retry_root_run_id, _) = self.retry_lineage(run_id);
        if retry_root_run_id.is_empty() {
            fallback_id
        } else {
            retry_root_run_id
        }
    }

    fn upsert_user_block(
        &self,
        blocks: &mut Vec<TranscriptBlock>,
        fallback_id: String,
        run_id: String,
        prompt: String,
    ) -> bool {
        let id = self.user_block_identity(fallback_id, &run_id);
        let canonical_id = format!("user:{id}");
        let retry_id = format!("user:{run_id}");
        if retry_id != canonical_id {
            blocks.retain(|block| block.id != retry_id || block.kind != BlockKind::User);
        }
        upsert_block(blocks, user_block(id, run_id, prompt))
    }

    fn mark_failure_recovering(&self, blocks: &mut [TranscriptBlock], run_id: &str) -> bool {
        let context = self.failure_context(run_id);
        if context.retry_root_run_id.is_empty() || context.retry_root_run_id == run_id {
            return false;
        }
        let id = format!("failure:{}", context.retry_root_run_id);
        let Some(block) = blocks.iter_mut().find(|block| block.id == id) else {
            return false;
        };
        let previous = block.clone();
        block.run_id = run_id.to_owned();
        if let Some(failure) = block.failure.as_mut() {
            failure.action = FailureAction::Recovering;
            failure.run_id = run_id.to_owned();
            failure.retry_root_run_id = context.retry_root_run_id;
            failure.attempt_count = context.attempt_count;
            failure.focused_action = None;
        }
        finalize_projected_update(block, &previous)
    }

    fn mark_failure_recovery_settled(&self, blocks: &mut [TranscriptBlock], run_id: &str) -> bool {
        let context = self.failure_context(run_id);
        if context.retry_root_run_id.is_empty() || context.retry_root_run_id == run_id {
            return false;
        }
        let id = format!("failure:{}", context.retry_root_run_id);
        let Some(block) = blocks.iter_mut().find(|block| block.id == id) else {
            return false;
        };
        let previous = block.clone();
        block.run_id = run_id.to_owned();
        if let Some(failure) = block.failure.as_mut() {
            failure.action = FailureAction::Disabled;
            failure.run_id = run_id.to_owned();
            failure.retry_root_run_id = context.retry_root_run_id;
            failure.attempt_count = context.attempt_count;
            failure.focused_action = None;
        }
        finalize_projected_update(block, &previous)
    }

    pub fn reduce_event(&mut self, blocks: &mut Vec<TranscriptBlock>, event: WireEvent) -> bool {
        if !self.remember_event(&event) {
            return false;
        }
        match event.kind.as_str() {
            "ExecutionQueued" => {
                self.record_run_metadata(&event.run_id, &event.payload);
                self.mark_failure_recovering(blocks, &event.run_id)
            }
            "RuntimeStageChanged"
            | "FileRead"
            | "FileWriteProposed"
            | "NetworkRequested"
            | "ToolRequested"
            | "ToolApproved"
            | "ToolDenied"
            // OverlayStack is the single live/replay owner for pending
            // governance input. Transcript receipts would duplicate the
            // actionable surface and diverge from hydrated history.
            | "permission.request"
            | "user.question" => false,
            "ToolCallRequested"
            | "ToolCallStarted"
            | "ToolCallApprovalRequired"
            | "ToolCallCompleted"
            | "ToolCallFailed"
            | "ToolCallDenied"
            | "ToolCallCancelled" => self.reduce_tool_event(blocks, event),
            "CommandStarted" | "CommandOutput" | "CommandExited" => {
                self.reduce_command_event(blocks, event)
            }
            "PatchProposed" | "PatchApplied" | "PatchFailed" | "RollbackCompleted" => {
                self.reduce_patch_event(blocks, event)
            }
            "PolicyViolation" => self.reduce_policy_event(blocks, event),
            "ExecutionFailed" | "ExecutionInterrupted" | "ExecutionCancelled" => {
                // Terminal events repeat route and retry ancestry so they can
                // reconstruct one truthful failure chain after reconnect even
                // when an earlier queued event was not observed.
                self.record_run_metadata(&event.run_id, &event.payload);
                let context = self.failure_context(&event.run_id);
                failure_block_from_event(event, context)
                    .map(|block| upsert_block(blocks, block))
                    .unwrap_or(false)
            }
            "ExecutionCompleted" => {
                let settled = self.mark_failure_recovery_settled(blocks, &event.run_id);
                let projected = simple_block_from_event(event)
                    .map(|block| upsert_block(blocks, block))
                    .unwrap_or(false);
                settled || projected
            }
            "assistant.message.reset"
            | "assistant.message.delta"
            | "assistant.message.completed"
            | "assistant.message.snapshot" => self.reduce_assistant_stream(blocks, event),
            "ModelResponded" => self.reduce_model_response(blocks, event),
            _ => simple_block_from_event(event)
                .map(|block| upsert_block(blocks, block))
                .unwrap_or(false),
        }
    }

    fn reduce_tool_event(&mut self, blocks: &mut Vec<TranscriptBlock>, event: WireEvent) -> bool {
        if detail(&event.payload, "kind").as_deref() == Some("interaction")
            || detail(&event.payload, "tool").as_deref() == Some("ask_user")
        {
            return false;
        }
        let Some(call_id) = detail(&event.payload, "call_id") else {
            return false;
        };
        let id = format!("tool:{call_id}");
        let next_status = lifecycle_status(&event);
        let record = self.tools.entry(id.clone()).or_insert_with(|| ToolRecord {
            id: id.clone(),
            run_id: event.run_id.clone(),
            ..ToolRecord::default()
        });
        if !can_transition_tool_status(&record.status, &next_status) {
            return false;
        }
        if record.run_id.is_empty() {
            record.run_id = event.run_id.clone();
        }
        if let Some(tool) = detail(&event.payload, "tool") {
            record.tool = tool;
        }
        merge_details(&mut record.details, &event.payload);
        record.status = next_status;

        if matches!(event.kind.as_str(), "ToolCallRequested" | "ToolCallStarted")
            && !event.run_id.is_empty()
        {
            self.active_tool_by_run
                .insert(event.run_id.clone(), id.clone());
        }
        let terminal = is_terminal_tool_status(&record.status);
        let changed = upsert_block(blocks, tool_block(record));
        if terminal && self.active_tool_by_run.get(&event.run_id) == Some(&id) {
            self.active_tool_by_run.remove(&event.run_id);
        }
        changed
    }

    fn remember_event(&mut self, event: &WireEvent) -> bool {
        let event_id = event.event_id.trim();
        if event_id.is_empty() {
            return true;
        }
        if !self.seen_event_ids.insert(event_id.to_owned()) {
            return false;
        }
        self.event_id_order.push_back(event_id.to_owned());
        while self.event_id_order.len() > Self::SEEN_EVENT_LIMIT {
            if let Some(expired) = self.event_id_order.pop_front() {
                self.seen_event_ids.remove(&expired);
            }
        }
        true
    }

    fn reduce_command_event(
        &mut self,
        blocks: &mut Vec<TranscriptBlock>,
        event: WireEvent,
    ) -> bool {
        if let Some(tool_id) = self.active_tool_by_run.get(&event.run_id).cloned()
            && self
                .tools
                .get(&tool_id)
                .is_some_and(|record| record.tool == "run")
        {
            let record = self.tools.get_mut(&tool_id).expect("active tool exists");
            merge_command_details(record, &event);
            return upsert_block(blocks, tool_block(record));
        }

        let command_id = detail(&event.payload, "command_id")
            .map(|id| format!("command:{id}"))
            .or_else(|| self.open_command_by_run.get(&event.run_id).cloned())
            .unwrap_or_else(|| format!("command:{}", event.stable_id()));
        let record = self
            .commands
            .entry(command_id.clone())
            .or_insert_with(|| ToolRecord {
                id: command_id.clone(),
                run_id: event.run_id.clone(),
                tool: "run".into(),
                status: "running".into(),
                ..ToolRecord::default()
            });
        merge_command_details(record, &event);
        if event.kind == "CommandStarted" && !event.run_id.is_empty() {
            self.open_command_by_run
                .insert(event.run_id.clone(), command_id.clone());
        }
        if event.kind == "CommandExited" {
            self.open_command_by_run.remove(&event.run_id);
        }
        upsert_block(blocks, tool_block(record))
    }

    fn reduce_patch_event(&mut self, blocks: &mut Vec<TranscriptBlock>, event: WireEvent) -> bool {
        let patch_id = detail(&event.payload, "patch_id");
        let lifecycle_tool_id = patch_id
            .as_ref()
            .and_then(|patch_id| self.patch_tool_by_patch_id.get(patch_id).cloned())
            .or_else(|| self.active_tool_by_run.get(&event.run_id).cloned());
        if let Some(tool_id) = lifecycle_tool_id
            && self
                .tools
                .get(&tool_id)
                .is_some_and(|record| record.tool == "patch")
        {
            let record = self.tools.get_mut(&tool_id).expect("active tool exists");
            merge_details(&mut record.details, &event.payload);
            if let Some(patch_id) = patch_id {
                self.patch_tool_by_patch_id.insert(patch_id, tool_id);
            }
            record.status = patch_event_status(&event.kind).into();
            return upsert_block(blocks, tool_block(record));
        }

        let id = detail(&event.payload, "patch_id")
            .map(|id| format!("patch:{id}"))
            .unwrap_or_else(|| format!("patch:{}", event.stable_id()));
        let status = patch_event_status(&event.kind);
        let record = self.tools.entry(id.clone()).or_insert_with(|| ToolRecord {
            id,
            run_id: event.run_id,
            tool: "patch".into(),
            ..ToolRecord::default()
        });
        record.status = status.into();
        merge_details(&mut record.details, &event.payload);
        upsert_block(blocks, tool_block(record))
    }

    fn reduce_policy_event(&mut self, blocks: &mut Vec<TranscriptBlock>, event: WireEvent) -> bool {
        let tool_id = detail(&event.payload, "call_id")
            .map(|call_id| format!("tool:{call_id}"))
            .or_else(|| self.active_tool_by_run.get(&event.run_id).cloned());
        if let Some(tool_id) = tool_id
            && let Some(record) = self.tools.get_mut(&tool_id)
        {
            merge_details(&mut record.details, &event.payload);
            record.status = "denied".into();
            self.active_tool_by_run.remove(&event.run_id);
            return upsert_block(blocks, tool_block(record));
        }
        simple_block_from_event(event)
            .map(|block| upsert_block(blocks, block))
            .unwrap_or(false)
    }

    fn reduce_model_response(
        &mut self,
        blocks: &mut Vec<TranscriptBlock>,
        event: WireEvent,
    ) -> bool {
        let raw = first_detail(
            &event.payload,
            &["presentation_text", "text", "content", "message", "result"],
        )
        .unwrap_or_default();
        let text = visible_assistant_text_with_mode(
            &raw,
            !event
                .payload
                .get("structured_output")
                .and_then(Value::as_bool)
                .unwrap_or(false),
        )
        .unwrap_or_default();
        if text.is_empty() {
            return false;
        }
        if !event.run_id.is_empty() {
            self.assistant_streams.remove(&event.run_id);
        }
        let status = display_status(event.projected_status());
        let id = if event.run_id.is_empty() {
            event.stable_id()
        } else {
            event.run_id.clone()
        };
        let mut block = message_block(
            format!("assistant:{id}"),
            event.run_id,
            BlockKind::Assistant,
            "Carina",
            text,
            status,
        );
        block.assistant_phase = Some(AssistantMessagePhase::FinalAnswer);
        upsert_block(blocks, block)
    }

    fn reduce_assistant_stream(
        &mut self,
        blocks: &mut Vec<TranscriptBlock>,
        event: WireEvent,
    ) -> bool {
        if event.run_id.is_empty() {
            return false;
        }
        let Some(update) = event.assistant_message_update() else {
            return false;
        };
        if let AssistantMessageUpdate::Snapshot { .. } = &update {
            return self.reduce_assistant_snapshot(blocks, event.run_id, update);
        }
        let (generation, sequence, phase) = match &update {
            AssistantMessageUpdate::Reset {
                generation,
                sequence,
                phase,
            }
            | AssistantMessageUpdate::Delta {
                generation,
                sequence,
                phase,
                ..
            }
            | AssistantMessageUpdate::Completed {
                generation,
                sequence,
                phase,
                ..
            } => (*generation, *sequence, *phase),
            AssistantMessageUpdate::Snapshot { .. } => unreachable!("snapshot handled above"),
        };
        let stream = self
            .assistant_streams
            .entry(event.run_id.clone())
            .or_default();
        if generation < stream.generation {
            return false;
        }
        if generation > stream.generation {
            if stream.generation != 0
                && !matches!(update, AssistantMessageUpdate::Reset { sequence: 1, .. })
            {
                return false;
            }
            if !matches!(update, AssistantMessageUpdate::Reset { sequence: 1, .. }) && sequence != 1
            {
                return false;
            }
            stream.generation = generation;
            stream.sequence = 0;
            stream.phase = Some(phase);
            stream.body.clear();
            stream.awaiting_canonical = false;
        }
        if stream.awaiting_canonical {
            return false;
        }
        if stream.phase.is_some_and(|current| current != phase) {
            return false;
        }
        stream.phase = Some(phase);
        if sequence <= stream.sequence && stream.sequence != 0 {
            return false;
        }
        if sequence != stream.sequence.saturating_add(1) {
            return false;
        }
        match update {
            AssistantMessageUpdate::Reset { .. } => {
                stream.body.clear();
                stream.sequence = sequence;
                stream.awaiting_canonical = false;
                let id = format!("assistant:{}", event.run_id);
                blocks.retain(|block| block.id != id);
                return true;
            }
            AssistantMessageUpdate::Delta { delta, .. } => stream.body.push_str(&delta),
            AssistantMessageUpdate::Completed { content, .. } => {
                stream.body.clear();
                stream.body.push_str(&content);
                stream.awaiting_canonical = true;
            }
            AssistantMessageUpdate::Snapshot { .. } => unreachable!("snapshot handled above"),
        }
        stream.sequence = sequence;
        if stream.body.is_empty() {
            return false;
        }
        // Same content-type channel as ModelResponded / agent_message hydration.
        let display = visible_assistant_text_with_mode(
            &stream.body,
            !event
                .payload
                .get("structured_output")
                .and_then(Value::as_bool)
                .unwrap_or(false),
        )
        .unwrap_or_default();
        if display.is_empty() {
            // Suppressed action JSON mid-stream: drop the block if present.
            let id = format!("assistant:{}", event.run_id);
            let before = blocks.len();
            blocks.retain(|block| block.id != id);
            return before != blocks.len();
        }
        let mut block = message_block(
            format!("assistant:{}", event.run_id),
            event.run_id,
            BlockKind::Assistant,
            "Carina",
            display,
            String::new(),
        );
        block.assistant_phase = Some(phase);
        block.layout_revision = generation;
        upsert_block(blocks, block)
    }

    fn reduce_assistant_snapshot(
        &mut self,
        blocks: &mut Vec<TranscriptBlock>,
        run_id: String,
        update: AssistantMessageUpdate,
    ) -> bool {
        let AssistantMessageUpdate::Snapshot {
            generation,
            sequence,
            phase,
            content,
            structured_output,
            state,
            ..
        } = update
        else {
            return false;
        };
        let stream = self.assistant_streams.entry(run_id.clone()).or_default();
        let empty = stream.generation == 0 && stream.sequence == 0 && stream.body.is_empty();
        if !empty {
            // Seed only empty stream state. An identical snapshot is
            // idempotent; a conflicting one is rejected without mutation.
            return false;
        }
        stream.generation = generation;
        stream.sequence = sequence;
        stream.phase = Some(phase);
        stream.body = content;
        stream.structured_output = structured_output;
        stream.awaiting_canonical = state == AssistantTailState::AwaitingCanonical;
        if stream.body.is_empty() {
            let id = format!("assistant:{run_id}");
            let before = blocks.len();
            blocks.retain(|block| block.id != id);
            return before != blocks.len();
        }
        let display = visible_assistant_text_with_mode(&stream.body, !stream.structured_output)
            .unwrap_or_default();
        if display.is_empty() {
            let id = format!("assistant:{run_id}");
            let before = blocks.len();
            blocks.retain(|block| block.id != id);
            return before != blocks.len();
        }
        let mut block = message_block(
            format!("assistant:{run_id}"),
            run_id,
            BlockKind::Assistant,
            "Carina",
            display,
            String::new(),
        );
        block.assistant_phase = Some(phase);
        block.layout_revision = generation;
        upsert_block(blocks, block)
    }

    fn reduce_item(&mut self, blocks: &mut Vec<TranscriptBlock>, event: SessionItemEvent) {
        let Some(item) = event.item else {
            self.reduce_turn_item(blocks, event);
            return;
        };
        let item_id = if item.id.is_empty() {
            first_non_empty([
                event.item_id,
                event.source_event_id,
                event.turn_id,
                event.run_id,
            ])
        } else {
            item.id.clone()
        };
        self.record_run_metadata(&item.run_id, &item.details);
        match item.kind.as_str() {
            // Governance is retained by OverlayStack. Rendering either the
            // pending or resolved item here duplicates the input owner as an
            // ACTION receipt in the conversation.
            "approval" | "question" => {}
            "error"
                if detail(&item.details, "source_type").as_deref() == Some("PolicyViolation") =>
            {
                let tool_id = detail(&item.details, "call_id")
                    .map(|call_id| format!("tool:{call_id}"))
                    .or_else(|| self.active_tool_by_run.get(&item.run_id).cloned());
                if let Some(tool_id) = tool_id
                    && let Some(record) = self.tools.get_mut(&tool_id)
                {
                    merge_details(&mut record.details, &item.details);
                    record.status = "denied".into();
                    self.active_tool_by_run.remove(&item.run_id);
                    upsert_block(blocks, tool_block(record));
                } else if let Some(block) = simple_block_from_item(
                    item_id,
                    item.kind,
                    item.status,
                    item.run_id,
                    item.details,
                ) {
                    upsert_block(blocks, block);
                }
            }
            "tool_call" => {
                if detail(&item.details, "kind").as_deref() == Some("interaction")
                    || detail(&item.details, "tool").as_deref() == Some("ask_user")
                {
                    return;
                }
                let id = format!("tool:{item_id}");
                let record = self.tools.entry(id.clone()).or_insert_with(|| ToolRecord {
                    id: id.clone(),
                    run_id: item.run_id.clone(),
                    ..ToolRecord::default()
                });
                if let Some(tool) = detail(&item.details, "tool") {
                    record.tool = tool;
                }
                if record.run_id.is_empty() {
                    record.run_id = item.run_id;
                }
                merge_details(&mut record.details, &item.details);
                record.status = item.status;
                if !is_terminal_tool_status(&record.status) && !record.run_id.is_empty() {
                    self.active_tool_by_run
                        .insert(record.run_id.clone(), id.clone());
                } else if self.active_tool_by_run.get(&record.run_id) == Some(&id) {
                    self.active_tool_by_run.remove(&record.run_id);
                }
                upsert_block(blocks, tool_block(record));
            }
            "command_execution" => {
                let id = format!("command:{item_id}");
                let record = self
                    .commands
                    .entry(id.clone())
                    .or_insert_with(|| ToolRecord {
                        id,
                        run_id: item.run_id,
                        tool: "run".into(),
                        ..ToolRecord::default()
                    });
                record.status = item.status;
                merge_details(&mut record.details, &item.details);
                upsert_block(blocks, tool_block(record));
            }
            "file_change" => {
                let patch_id = detail(&item.details, "patch_id");
                let lifecycle_tool_id = patch_id
                    .as_ref()
                    .and_then(|patch_id| self.patch_tool_by_patch_id.get(patch_id).cloned())
                    .or_else(|| self.active_tool_by_run.get(&item.run_id).cloned());
                if let Some(tool_id) = lifecycle_tool_id
                    && self
                        .tools
                        .get(&tool_id)
                        .is_some_and(|record| record.tool == "patch")
                {
                    let record = self.tools.get_mut(&tool_id).expect("patch tool exists");
                    record.status = patch_item_status(&item.status).into();
                    merge_details(&mut record.details, &item.details);
                    if let Some(patch_id) = patch_id {
                        self.patch_tool_by_patch_id.insert(patch_id, tool_id);
                    }
                    upsert_block(blocks, tool_block(record));
                    return;
                }
                let id = patch_id
                    .as_ref()
                    .map(|patch_id| format!("patch:{patch_id}"))
                    .unwrap_or(item_id);
                let record = self.tools.entry(id.clone()).or_insert_with(|| ToolRecord {
                    id: id.clone(),
                    run_id: item.run_id,
                    tool: "patch".into(),
                    ..ToolRecord::default()
                });
                record.status = patch_item_status(&item.status).into();
                merge_details(&mut record.details, &item.details);
                if let Some(patch_id) = patch_id {
                    self.patch_tool_by_patch_id.insert(patch_id, id);
                }
                upsert_block(blocks, tool_block(record));
            }
            "turn_net_diff" => {
                if net_diff_is_represented(&item.details, &self.patch_tool_by_patch_id) {
                    return;
                }
                let tool = if item.kind == "turn_net_diff" {
                    "diff"
                } else {
                    "patch"
                };
                let record = self
                    .tools
                    .entry(item_id.clone())
                    .or_insert_with(|| ToolRecord {
                        id: item_id,
                        run_id: item.run_id,
                        tool: tool.into(),
                        ..ToolRecord::default()
                    });
                record.status = item.status;
                merge_details(&mut record.details, &item.details);
                upsert_block(blocks, tool_block(record));
            }
            "agent_message" => {
                let raw = first_detail(
                    &item.details,
                    &[
                        "presentation_text",
                        "text",
                        "content",
                        "message",
                        "result",
                        "summary",
                    ],
                )
                .unwrap_or_default();
                if let Some(text) = visible_assistant_text_with_mode(
                    &raw,
                    !item
                        .details
                        .get("structured_output")
                        .and_then(Value::as_bool)
                        .unwrap_or(false),
                ) {
                    let message_id = if item.run_id.is_empty() {
                        item_id
                    } else {
                        item.run_id.clone()
                    };
                    let mut block = message_block(
                        format!("assistant:{message_id}"),
                        item.run_id,
                        BlockKind::Assistant,
                        imported_source_title(&item.details)
                            .as_deref()
                            .unwrap_or("Carina"),
                        text,
                        display_status(&item.status),
                    );
                    block.assistant_phase = Some(AssistantMessagePhase::FinalAnswer);
                    upsert_block(blocks, block);
                }
            }
            "user" => {
                let prompt =
                    first_detail(&item.details, &["user_prompt", "prompt", "text", "content"])
                        .unwrap_or_default();
                if !prompt.is_empty() {
                    let run_id = item.run_id;
                    if let Some(title) = imported_source_title(&item.details) {
                        let mut block = user_block(item_id, run_id, prompt);
                        block.title = title;
                        block.branchable = false;
                        upsert_block(blocks, block);
                    } else {
                        self.upsert_user_block(blocks, item_id, run_id, prompt);
                    }
                }
            }
            _ => {
                if let Some(block) = simple_block_from_item(
                    item_id,
                    item.kind,
                    item.status,
                    item.run_id,
                    item.details,
                ) {
                    upsert_block(blocks, block);
                }
            }
        }
    }

    fn reduce_turn_item(&mut self, blocks: &mut Vec<TranscriptBlock>, event: SessionItemEvent) {
        match event.kind.as_str() {
            "thread.started" | "session.started" | "runtime.stage_changed" => {}
            "turn.started" => {
                let run_id = first_non_empty([event.run_id.clone(), event.turn_id.clone()]);
                self.record_run_metadata(&run_id, &event.details);
                self.mark_failure_recovering(blocks, &run_id);
                let prompt = first_detail(
                    &event.details,
                    &["user_prompt", "prompt", "text", "content"],
                )
                .unwrap_or_default();
                if !prompt.is_empty() {
                    let fallback_id = first_non_empty([event.turn_id, run_id.clone()]);
                    self.upsert_user_block(blocks, fallback_id, run_id, prompt);
                }
            }
            "turn.completed" => {
                let run_id = first_non_empty([event.run_id.clone(), event.turn_id.clone()]);
                self.mark_failure_recovery_settled(blocks, &run_id);
                let summary = first_detail(&event.details, &["summary", "message", "text"])
                    .unwrap_or_default();
                let summary = if event
                    .details
                    .get("structured_output")
                    .and_then(Value::as_bool)
                    .unwrap_or(false)
                {
                    summary
                } else {
                    project_structured_summary_as_markdown(&summary).unwrap_or(summary)
                };
                if !summary.is_empty() {
                    let id = first_non_empty([event.turn_id, event.run_id.clone()]);
                    let mut answer = message_block(
                        format!("assistant:{id}"),
                        event.run_id,
                        BlockKind::Assistant,
                        "Carina",
                        summary,
                        String::new(),
                    );
                    answer.assistant_phase = Some(AssistantMessagePhase::FinalAnswer);
                    upsert_block(blocks, answer);
                }
            }
            "turn.failed" => {
                let source_event_id = event.source_event_id;
                let run_id = first_non_empty([event.run_id, event.turn_id]);
                self.record_run_metadata(&run_id, &event.details);
                let context = self.failure_context(&run_id);
                let source_type = detail(&event.details, "source_type").unwrap_or_default();
                let reason_code = detail(&event.details, "reason_code").unwrap_or_default();
                let kind = match source_type.as_str() {
                    "ExecutionInterrupted" => FailureKind::Interrupted,
                    "ExecutionCancelled" => FailureKind::Cancelled,
                    "ExecutionFailed"
                        if reason_code.contains("approval") || reason_code.contains("denied") =>
                    {
                        FailureKind::ApprovalDenied
                    }
                    _ => FailureKind::Failed,
                };
                let owner = detail(&event.details, "owner").unwrap_or_else(|| match kind {
                    FailureKind::Cancelled => "operator".into(),
                    _ => "runtime".into(),
                });
                let reason = humanize_failure_reason(&first_display([
                    detail(&event.details, "reason").unwrap_or_default(),
                    detail(&event.details, "summary").unwrap_or_default(),
                    detail(&event.details, "error").unwrap_or_default(),
                    detail(&event.details, "message").unwrap_or_default(),
                    reason_code,
                ]));
                let retryable = event
                    .details
                    .get("retryable")
                    .and_then(Value::as_bool)
                    .unwrap_or(source_type.is_empty());
                let recovering = detail(&event.details, "recovery_disposition")
                    .is_some_and(|value| value == "resume_checkpoint");
                upsert_block(
                    blocks,
                    failure_block(
                        run_id,
                        source_event_id,
                        kind,
                        owner,
                        if reason.is_empty() {
                            "The response did not complete".into()
                        } else {
                            reason
                        },
                        (retryable || kind == FailureKind::Interrupted) && !recovering,
                        context,
                    ),
                );
            }
            _ => {}
        }
    }
}

fn lifecycle_status(event: &WireEvent) -> String {
    let payload = event.projected_status();
    if !payload.is_empty() {
        return payload.into();
    }
    match event.kind.as_str() {
        "ToolCallRequested" => "requested",
        "ToolCallStarted" => "running",
        "ToolCallApprovalRequired" => "awaiting_approval",
        "ToolCallCompleted" => "completed",
        "ToolCallDenied" => "denied",
        "ToolCallCancelled" => "cancelled",
        _ => "failed",
    }
    .into()
}

fn patch_event_status(kind: &str) -> &'static str {
    match kind {
        "PatchFailed" => "failed",
        "PatchApplied" => "completed",
        "RollbackCompleted" => "rolled_back",
        _ => "running",
    }
}

fn patch_item_status(status: &str) -> &str {
    match status {
        "proposed" => "running",
        "applied" => "completed",
        status => status,
    }
}

fn merge_command_details(record: &mut ToolRecord, event: &WireEvent) {
    merge_details(&mut record.details, &event.payload);
    match event.kind.as_str() {
        "CommandStarted" => record.status = "running".into(),
        "CommandOutput" => {
            let stream = raw_string(&event.payload, "stream")
                .filter(|value| !value.is_empty())
                .unwrap_or("stdout")
                .to_owned();
            if let Some(chunk) = raw_string(&event.payload, "chunk") {
                let previous = raw_string(&record.details, &stream).unwrap_or_default();
                record
                    .details
                    .insert(stream, Value::String(format!("{previous}{chunk}")));
                let aggregated =
                    raw_string(&record.details, "aggregated_output").unwrap_or_default();
                record.details.insert(
                    "aggregated_output".into(),
                    Value::String(format!("{aggregated}{chunk}")),
                );
            }
        }
        "CommandExited" => {
            record.status = if detail(&event.payload, "error").is_some()
                || event
                    .payload
                    .get("exit_code")
                    .and_then(Value::as_i64)
                    .is_some_and(|code| code != 0)
            {
                "failed"
            } else {
                "completed"
            }
            .into();
        }
        _ => {}
    }
}

fn tool_block(record: &ToolRecord) -> TranscriptBlock {
    let tool = if record.tool.is_empty() {
        detail(&record.details, "tool").unwrap_or_else(|| "tool".into())
    } else {
        record.tool.clone()
    };
    let kind = ToolKind::from_name(&tool);
    let presentation = present_tool(
        &tool_facts(tool.clone(), &record.status, &record.details),
        is_failure_status(&record.status),
    );
    let running = !is_terminal_tool_status(&record.status);
    let (additions, deletions) = if matches!(kind, ToolKind::Patch | ToolKind::Diff) {
        unified_diff_stats(&presentation.body)
    } else {
        (0, 0)
    };
    // Failures and plans stay open so risk and next steps remain visible.
    // Successful code-intel is evidence, not dialogue, and settles collapsed.
    let expanded = if is_failure_status(&record.status) || matches!(kind, ToolKind::Todo) {
        !presentation.body.is_empty() || !presentation.todo_items.is_empty()
    } else if matches!(kind, ToolKind::Patch | ToolKind::Diff) {
        false
    } else {
        presentation.collapsible && running
    };
    let id = if kind == ToolKind::Todo && !record.run_id.is_empty() {
        format!("plan:{}", record.run_id)
    } else {
        record.id.clone()
    };
    let member_title = if kind == ToolKind::Extension && presentation.target.is_empty() {
        tool.clone()
    } else if presentation.target.is_empty() {
        presentation.title.clone()
    } else {
        presentation.target.clone()
    };
    let member = ToolGroupMember {
        id: id.clone(),
        tool_name: tool,
        title: member_title,
        body: presentation.body.clone(),
        body_kind: if matches!(kind, ToolKind::Patch | ToolKind::Diff) {
            BlockBodyKind::Diff
        } else if kind.is_code_intelligence() {
            BlockBodyKind::CodeIntelligence
        } else {
            BlockBodyKind::Plain
        },
        additions,
        deletions,
        status: tool_display_status(kind, &record.status),
        lifecycle: record.status.clone(),
    };
    TranscriptBlock {
        id,
        run_id: record.run_id.clone(),
        kind: BlockKind::Tool,
        assistant_phase: None,
        user_kind: UserBlockKind::Prompt,
        tool_kind: Some(kind),
        tool_members: vec![member],
        todo_items: presentation.todo_items,
        failure: None,
        title: presentation.title,
        body: presentation.body,
        body_kind: if matches!(kind, ToolKind::Patch | ToolKind::Diff) {
            BlockBodyKind::Diff
        } else if kind.is_code_intelligence() {
            BlockBodyKind::CodeIntelligence
        } else {
            BlockBodyKind::Plain
        },
        additions,
        deletions,
        status: tool_display_status(kind, &record.status),
        collapsible: presentation.collapsible,
        // A reviewable edit is the work product, not transient command noise.
        // Keep its diff visible through completion; later lifecycle updates and
        // explicit user folds retain their existing display state in upsert_block.
        expanded,
        selected: false,
        source_prompt: String::new(),
        branchable: false,
        layout_revision: 1,
    }
}

fn tool_facts(tool: String, status: &str, details: &BTreeMap<String, Value>) -> ToolFacts {
    let args = details.get("arguments").and_then(Value::as_object);
    let nested = |key: &str| args.and_then(|value| value.get(key)).and_then(safe_value);
    let command = detail(details, "command")
        .or_else(|| {
            let executable = nested("executable")?;
            let argc = nested("argc");
            Some(match argc {
                Some(argc) => format!("{executable} ({argc} args)"),
                None => executable,
            })
        })
        .unwrap_or_default();
    let diff = first_detail(details, &["diff"]).unwrap_or_default();
    let output = first_detail(
        details,
        &[
            "aggregated_output",
            "artifact_output",
            "stdout",
            "stderr",
            "summary",
            "result",
            "output",
        ],
    )
    .or_else(|| nested_output(details, "output"))
    .unwrap_or_default();
    ToolFacts {
        name: tool,
        intent: detail(details, "intent").unwrap_or_default(),
        path: nested("path")
            .or_else(|| detail(details, "path"))
            .unwrap_or_default(),
        affected_files: detail(details, "active_files")
            .or_else(|| detail(details, "affected_files"))
            .unwrap_or_default(),
        pattern: nested("pattern")
            .or_else(|| detail(details, "pattern"))
            .unwrap_or_default(),
        query: nested("query")
            .or_else(|| detail(details, "query"))
            .unwrap_or_default(),
        symbol: nested("name")
            .or_else(|| detail(details, "name"))
            .unwrap_or_default(),
        command,
        workflow: nested("workflow").unwrap_or_default(),
        mcp_tool: nested("mcp_tool").unwrap_or_default(),
        agent: nested("agent").unwrap_or_default(),
        target: nested("target")
            .or_else(|| nested("host"))
            .or_else(|| detail(details, "host"))
            .unwrap_or_default(),
        diff,
        output,
        error: first_detail(details, &["error", "reason", "message"])
            .or_else(|| nested_message(details, "error"))
            .unwrap_or_default(),
        status: status.to_owned(),
        todo_items: todo_items(details),
    }
}

fn todo_items(details: &BTreeMap<String, Value>) -> Vec<TodoItem> {
    let arguments = details.get("arguments").and_then(Value::as_object);
    let values = ["todos", "items", "plan"]
        .into_iter()
        .find_map(|key| {
            arguments
                .and_then(|arguments| arguments.get(key))
                .or_else(|| details.get(key))
                .and_then(Value::as_array)
        })
        .cloned()
        .unwrap_or_default();
    values
        .iter()
        .filter_map(|value| {
            let item = value.as_object()?;
            let content = ["content", "text", "task", "description", "step", "title"]
                .into_iter()
                .find_map(|key| item.get(key).and_then(Value::as_str))
                .unwrap_or_default()
                .trim()
                .to_owned();
            if content.is_empty() {
                return None;
            }
            let status = item
                .get("status")
                .and_then(Value::as_str)
                .unwrap_or("pending")
                .trim()
                .to_ascii_lowercase();
            Some(TodoItem { content, status })
        })
        .collect()
}

fn unified_diff_stats(diff: &str) -> (usize, usize) {
    diff.lines().fold((0, 0), |(additions, deletions), line| {
        if line.starts_with('+') && !line.starts_with("+++") {
            (additions + 1, deletions)
        } else if line.starts_with('-') && !line.starts_with("---") {
            (additions, deletions + 1)
        } else {
            (additions, deletions)
        }
    })
}

fn tool_display_status(kind: ToolKind, status: &str) -> String {
    if kind != ToolKind::Patch {
        return display_status(status);
    }
    match status {
        "requested" | "pending" => "preparing".into(),
        "started" | "running" => "applying".into(),
        "completed" => "applied".into(),
        other => display_status(other),
    }
}

fn simple_block_from_event(event: WireEvent) -> Option<TranscriptBlock> {
    let classified = classify(&event.kind);
    let terminal_summary = first_display([
        event.summary.clone(),
        first_detail(&event.payload, &["summary", "message"]).unwrap_or_default(),
    ]);
    let terminal_summary = if event
        .payload
        .get("structured_output")
        .and_then(Value::as_bool)
        .unwrap_or(false)
    {
        terminal_summary
    } else {
        project_structured_summary_as_markdown(&terminal_summary).unwrap_or(terminal_summary)
    };
    let kind = if classified == BlockKind::Status
        && event.clears_active_execution()
        && !terminal_summary.is_empty()
    {
        BlockKind::Assistant
    } else {
        classified
    };
    if kind == BlockKind::Status {
        return None;
    }
    let body = if kind == BlockKind::Governance {
        first_display([
            event.prompt.clone(),
            event.reason.clone(),
            event.label.clone(),
            event.diff.clone(),
            summarize_details(&event.payload),
        ])
    } else if event.clears_active_execution() {
        first_display([
            terminal_summary,
            event.status.clone(),
            summarize_details(&event.payload),
        ])
    } else {
        summarize_details(&event.payload)
    };
    if body.is_empty() || body == "No additional details" {
        return None;
    }
    let status = display_status(
        event
            .execution_lifecycle()
            .map(ExecutionLifecycle::status)
            .unwrap_or_else(|| event.projected_status()),
    );
    Some(TranscriptBlock {
        id: if kind == BlockKind::Assistant && !event.run_id.is_empty() {
            format!("assistant:{}", event.run_id)
        } else {
            event.stable_id()
        },
        run_id: event.run_id,
        kind,
        assistant_phase: (kind == BlockKind::Assistant)
            .then_some(AssistantMessagePhase::FinalAnswer),
        user_kind: UserBlockKind::Prompt,
        tool_kind: None,
        tool_members: Vec::new(),
        todo_items: Vec::new(),
        failure: None,
        title: title(kind, &event.kind),
        body,
        body_kind: BlockBodyKind::Plain,
        additions: 0,
        deletions: 0,
        status,
        collapsible: matches!(kind, BlockKind::Thinking | BlockKind::Diagnostic),
        expanded: !matches!(kind, BlockKind::Thinking),
        selected: false,
        source_prompt: event.prompt,
        branchable: false,
        layout_revision: 1,
    })
}

fn failure_block_from_event(event: WireEvent, context: FailureContext) -> Option<TranscriptBlock> {
    let run_id = event.run_id.trim().to_owned();
    if run_id.is_empty() {
        return None;
    }
    let reason_code = detail(&event.payload, "reason_code").unwrap_or_default();
    let kind = match event.kind.as_str() {
        "ExecutionInterrupted" => FailureKind::Interrupted,
        "ExecutionCancelled" => FailureKind::Cancelled,
        "ExecutionFailed" if reason_code.contains("approval") || reason_code.contains("denied") => {
            FailureKind::ApprovalDenied
        }
        "ExecutionFailed" => FailureKind::Failed,
        _ => return None,
    };
    let owner = detail(&event.payload, "owner").unwrap_or_else(|| match kind {
        FailureKind::Cancelled => "operator".into(),
        FailureKind::Interrupted => "runtime".into(),
        _ if !event.agent.trim().is_empty() => event.agent.trim().to_owned(),
        _ => "runtime".into(),
    });
    let reason = first_display([
        detail(&event.payload, "reason").unwrap_or_default(),
        event.reason,
        event.summary,
        reason_code,
    ]);
    let reason = humanize_failure_reason(&reason);
    let retryable = event
        .payload
        .get("retryable")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    let recovering = detail(&event.payload, "recovery_disposition")
        .is_some_and(|value| value == "resume_checkpoint");
    Some(failure_block(
        run_id,
        event.event_id,
        kind,
        owner,
        if reason.is_empty() {
            "The execution did not complete".into()
        } else {
            reason
        },
        (retryable || kind == FailureKind::Interrupted) && !recovering,
        context,
    ))
}

/// Defense-in-depth: strip internal stacks to stable English operator VOICE.
/// Display localization happens at render via [`i18n::localize_operator_failure_reason`].
fn humanize_failure_reason(reason: &str) -> String {
    let trimmed = reason.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    let lower = trimmed.to_ascii_lowercase();
    if lower.contains("while reading body")
        || lower.contains("stream budget")
        || lower.contains("provider_stream_budget")
        || (lower.contains("client.timeout") && lower.contains("body"))
    {
        return "The model stream stopped before finishing. Not auto-retried. Check the proxy and network, then retry explicitly if needed.".into();
    }
    if lower.contains("modelrouter")
        || lower.starts_with("reasoner error:")
        || lower.starts_with("reasoner failed:")
    {
        if lower.contains("credential") || lower.contains("authentication") {
            return "The model provider rejected the credential. Check the provider credential."
                .into();
        }
        if lower.contains("rate") || lower.contains("quota") {
            return "The model provider rate-limited or quota-blocked the request. Wait or choose another provider.".into();
        }
        if lower.contains("circuit") {
            return "The model provider circuit is open. Wait for the probe or choose another provider.".into();
        }
        if lower.contains("reasoning effort") {
            return "Reasoning effort is not supported or not valid for this model route. Clear effort or choose another model.".into();
        }
        return "The model could not complete this turn. Check the provider and network, then retry explicitly if needed.".into();
    }
    if lower.contains("reasoning effort is not supported")
        || (lower.contains("reasoning effort") && lower.contains("not supported by this adapter"))
    {
        return "Reasoning effort is not supported or not valid for this model route. Clear effort or choose another model.".into();
    }
    trimmed.to_owned()
}

fn failure_block(
    run_id: String,
    source_event_id: String,
    kind: FailureKind,
    owner: String,
    reason: String,
    retryable: bool,
    context: FailureContext,
) -> TranscriptBlock {
    let action = if kind == FailureKind::Interrupted && !retryable {
        FailureAction::Recovering
    } else if !retryable || run_id.is_empty() {
        FailureAction::Disabled
    } else if kind == FailureKind::Cancelled {
        FailureAction::RunAgain
    } else {
        FailureAction::Retry
    };
    // English storage title; localized_title remaps FailureKind for display locales.
    let title = match kind {
        FailureKind::Failed => "Execution failed",
        FailureKind::ApprovalDenied => "Approval denied",
        FailureKind::Interrupted => "Execution interrupted",
        FailureKind::Cancelled => "Execution cancelled",
    };
    let failure = FailurePresentation {
        kind,
        action,
        owner,
        reason: reason.clone(),
        source_event_id,
        run_id: run_id.clone(),
        model: context.model,
        current_model: String::new(),
        retry_root_run_id: context.retry_root_run_id.clone(),
        attempt_count: context.attempt_count,
        focused_action: None,
    };
    TranscriptBlock {
        id: format!("failure:{}", context.retry_root_run_id),
        run_id,
        kind: BlockKind::Diagnostic,
        assistant_phase: None,
        user_kind: UserBlockKind::Prompt,
        tool_kind: None,
        tool_members: Vec::new(),
        todo_items: Vec::new(),
        failure: Some(failure),
        title: title.into(),
        body: reason,
        body_kind: BlockBodyKind::Plain,
        additions: 0,
        deletions: 0,
        status: String::new(),
        collapsible: true,
        expanded: false,
        selected: false,
        source_prompt: String::new(),
        branchable: false,
        layout_revision: 1,
    }
}

fn simple_block_from_item(
    id: String,
    item_kind: String,
    status: String,
    run_id: String,
    details: BTreeMap<String, Value>,
) -> Option<TranscriptBlock> {
    let kind = classify(&item_kind);
    if kind == BlockKind::Status {
        return None;
    }
    let body = summarize_details(&details);
    if body == "No additional details" {
        return None;
    }
    Some(TranscriptBlock {
        id,
        run_id,
        kind,
        assistant_phase: (kind == BlockKind::Assistant)
            .then_some(AssistantMessagePhase::Commentary),
        user_kind: UserBlockKind::Prompt,
        tool_kind: None,
        tool_members: Vec::new(),
        todo_items: Vec::new(),
        failure: None,
        title: title(kind, &item_kind),
        body,
        body_kind: BlockBodyKind::Plain,
        additions: 0,
        deletions: 0,
        status: display_status(&status),
        collapsible: matches!(kind, BlockKind::Thinking | BlockKind::Diagnostic),
        expanded: !matches!(kind, BlockKind::Thinking),
        selected: false,
        source_prompt: String::new(),
        branchable: false,
        layout_revision: 1,
    })
}

fn user_block(id: String, run_id: String, prompt: String) -> TranscriptBlock {
    TranscriptBlock {
        id: format!("user:{id}"),
        run_id,
        kind: BlockKind::User,
        assistant_phase: None,
        user_kind: UserBlockKind::Prompt,
        tool_kind: None,
        tool_members: Vec::new(),
        todo_items: Vec::new(),
        failure: None,
        title: String::new(),
        body: prompt.clone(),
        body_kind: BlockBodyKind::Plain,
        additions: 0,
        deletions: 0,
        status: String::new(),
        collapsible: false,
        expanded: true,
        selected: false,
        source_prompt: prompt,
        branchable: true,
        layout_revision: 1,
    }
}

fn message_block(
    id: String,
    run_id: String,
    kind: BlockKind,
    title: &str,
    body: String,
    status: String,
) -> TranscriptBlock {
    TranscriptBlock {
        id,
        run_id,
        kind,
        assistant_phase: (kind == BlockKind::Assistant)
            .then_some(AssistantMessagePhase::Commentary),
        user_kind: UserBlockKind::Prompt,
        tool_kind: None,
        tool_members: Vec::new(),
        todo_items: Vec::new(),
        failure: None,
        title: title.into(),
        body,
        body_kind: BlockBodyKind::Plain,
        additions: 0,
        deletions: 0,
        status,
        collapsible: matches!(kind, BlockKind::Thinking | BlockKind::Diagnostic),
        expanded: !matches!(kind, BlockKind::Thinking),
        selected: false,
        source_prompt: String::new(),
        branchable: false,
        layout_revision: 1,
    }
}

fn upsert_block(blocks: &mut Vec<TranscriptBlock>, block: TranscriptBlock) -> bool {
    if block.tool_kind.is_some() && block.tool_members.len() == 1 {
        return upsert_tool_block(blocks, block);
    }
    upsert_projected_block(blocks, block)
}

fn upsert_tool_block(blocks: &mut Vec<TranscriptBlock>, block: TranscriptBlock) -> bool {
    let incoming = block.tool_members[0].clone();
    if let Some(index) = blocks.iter().position(|existing| {
        existing.tool_members.len() > 1
            && existing
                .tool_members
                .iter()
                .any(|member| member.id == incoming.id)
    }) {
        let previous = blocks[index].clone();
        let group = &mut blocks[index];
        let member = group
            .tool_members
            .iter_mut()
            .find(|member| member.id == incoming.id)
            .expect("group member exists");
        *member = incoming;
        refresh_tool_group(group);
        preserve_group_state(group, &previous);
        return finalize_projected_update(group, &previous);
    }

    if blocks
        .iter()
        .any(|existing| existing.id == block.id && existing.tool_members.len() == 1)
    {
        return upsert_projected_block(blocks, block);
    }

    if blocks
        .last()
        .is_some_and(|existing| tool_blocks_are_groupable(existing, &block))
    {
        let group = blocks.last_mut().expect("last block exists");
        let previous = group.clone();
        group.tool_members.push(incoming);
        refresh_tool_group(group);
        preserve_group_state(group, &previous);
        return finalize_projected_update(group, &previous);
    }

    upsert_projected_block(blocks, block)
}

fn tool_blocks_are_groupable(existing: &TranscriptBlock, incoming: &TranscriptBlock) -> bool {
    let (Some(existing_kind), Some(incoming_kind)) = (existing.tool_kind, incoming.tool_kind)
    else {
        return false;
    };
    if existing.run_id.is_empty() || existing.run_id != incoming.run_id {
        return false;
    }
    // Fail/cancel never join a success group (and a failed group never absorbs
    // later successes). Diff-bearing kinds still group with peers but always
    // expand via preserve_group_state; they must not hide under a success seal.
    let existing_has_failure = existing
        .tool_members
        .iter()
        .any(ToolGroupMember::is_failure);
    let incoming_has_failure = incoming
        .tool_members
        .iter()
        .any(ToolGroupMember::is_failure);
    if existing_has_failure || incoming_has_failure {
        return false;
    }
    if existing_kind.is_exploration() && incoming_kind.is_exploration() {
        return true;
    }
    if existing_kind != incoming_kind || existing_kind == ToolKind::Todo {
        return false;
    }
    if existing_kind != ToolKind::Extension {
        return true;
    }
    existing
        .tool_members
        .last()
        .zip(incoming.tool_members.first())
        .is_some_and(|(left, right)| left.tool_name.eq_ignore_ascii_case(&right.tool_name))
}

fn refresh_tool_group(block: &mut TranscriptBlock) {
    if block.tool_members.len() < 2 {
        return;
    }
    let mut member_kinds = block
        .tool_members
        .iter()
        .map(|member| ToolKind::from_name(&member.tool_name));
    let Some(first_kind) = member_kinds.next() else {
        return;
    };
    let kind = if member_kinds.clone().all(|kind| kind == first_kind) {
        first_kind
    } else if std::iter::once(first_kind)
        .chain(member_kinds)
        .all(ToolKind::is_exploration)
    {
        ToolKind::Explore
    } else {
        return;
    };
    block.tool_kind = Some(kind);
    block.title.clear();
    block.body.clear();
    block.body_kind = if matches!(kind, ToolKind::Patch | ToolKind::Diff) {
        BlockBodyKind::Diff
    } else if kind.is_code_intelligence() {
        BlockBodyKind::CodeIntelligence
    } else {
        BlockBodyKind::Plain
    };
    block.additions = block
        .tool_members
        .iter()
        .map(|member| member.additions)
        .sum();
    block.deletions = block
        .tool_members
        .iter()
        .map(|member| member.deletions)
        .sum();
    block.status.clear();
    block.collapsible = true;
}

fn preserve_group_state(group: &mut TranscriptBlock, previous: &TranscriptBlock) {
    let was_terminal = previous
        .tool_members
        .iter()
        .all(|member| !member.is_running());
    let is_terminal = group.tool_members.iter().all(|member| !member.is_running());
    // Diff-bearing groups expand only on failure (or when operator expanded).
    // Successful edits stay collapsed so the transcript stays dialogue-first.
    let requires_expansion = group.tool_members.iter().any(|member| {
        member.is_failure()
            || member.is_running()
                && !member.body.is_empty()
                && ToolKind::from_name(&member.tool_name).is_code_intelligence()
    });
    group.expanded = requires_expansion
        || if !is_terminal || was_terminal {
            previous.expanded
        } else {
            false
        };
    group.selected = previous.selected;
}

fn finalize_projected_update(block: &mut TranscriptBlock, previous: &TranscriptBlock) -> bool {
    block.layout_revision = if block.layout_matches(previous) {
        previous.layout_revision
    } else {
        previous.layout_revision.saturating_add(1)
    };
    !block.presentation_matches(previous)
}

fn upsert_projected_block(blocks: &mut Vec<TranscriptBlock>, mut block: TranscriptBlock) -> bool {
    if block.kind == BlockKind::Assistant
        && block.assistant_phase == Some(AssistantMessagePhase::FinalAnswer)
        && let Some(previous) = blocks.last()
        && previous.kind == BlockKind::Assistant
        && previous.assistant_phase == Some(AssistantMessagePhase::Commentary)
        && previous.body == block.body
    {
        let previous = blocks.pop().expect("last assistant block exists");
        block.selected = previous.selected;
        block.layout_revision = previous.layout_revision.saturating_add(1);
    }
    if let Some(existing) = blocks.iter_mut().find(|item| item.id == block.id) {
        let was_terminal = existing.status.is_empty() || is_failure_status(&existing.status);
        let is_terminal = block.status.is_empty() || is_failure_status(&block.status);
        let gained_reviewable_diff =
            !existing.collapsible && block.collapsible && block.body_kind == BlockBodyKind::Diff;
        if block.collapsible && !gained_reviewable_diff && (!is_terminal || was_terminal) {
            block.expanded = existing.expanded;
        }
        block.selected = existing.selected;
        block.layout_revision = if block.layout_matches(existing) {
            existing.layout_revision
        } else {
            existing.layout_revision.saturating_add(1)
        };
        if block.presentation_matches(existing) {
            return false;
        }
        *existing = block;
    } else {
        block.layout_revision = block.layout_revision.max(1);
        blocks.push(block);
    }
    true
}

pub fn toggle_block_expansion(blocks: &mut [TranscriptBlock], id: &str) -> bool {
    let Some(block) = blocks
        .iter_mut()
        .find(|block| block.id == id && block.is_collapsible())
    else {
        return false;
    };
    block.expanded = !block.expanded;
    block.layout_revision = block.layout_revision.saturating_add(1);
    true
}

pub fn set_tool_blocks_expanded(blocks: &mut [TranscriptBlock], expanded: bool) -> usize {
    let mut changed = 0;
    for block in blocks
        .iter_mut()
        .filter(|block| block.kind == BlockKind::Tool && block.is_collapsible())
    {
        if block.expanded == expanded {
            continue;
        }
        block.expanded = expanded;
        block.layout_revision = block.layout_revision.saturating_add(1);
        changed += 1;
    }
    changed
}

impl TranscriptBlock {
    fn presentation_matches(&self, other: &Self) -> bool {
        self.run_id == other.run_id
            && self.kind == other.kind
            && self.user_kind == other.user_kind
            && self.tool_kind == other.tool_kind
            && self.tool_members == other.tool_members
            && self.todo_items == other.todo_items
            && self.failure == other.failure
            && self.title == other.title
            && self.body == other.body
            && self.body_kind == other.body_kind
            && self.additions == other.additions
            && self.deletions == other.deletions
            && self.status == other.status
            && self.collapsible == other.collapsible
            && self.expanded == other.expanded
            && self.selected == other.selected
            && self.source_prompt == other.source_prompt
            && self.branchable == other.branchable
    }

    fn layout_matches(&self, other: &Self) -> bool {
        self.kind == other.kind
            && self.user_kind == other.user_kind
            && self.tool_kind == other.tool_kind
            && self.tool_members == other.tool_members
            && self.todo_items == other.todo_items
            && self.failure == other.failure
            && self.title == other.title
            && self.body == other.body
            && self.body_kind == other.body_kind
            && self.additions == other.additions
            && self.deletions == other.deletions
            && self.status == other.status
            && self.collapsible == other.collapsible
            && self.expanded == other.expanded
    }
}

fn merge_details(target: &mut BTreeMap<String, Value>, source: &BTreeMap<String, Value>) {
    for (key, value) in source {
        target.insert(key.clone(), value.clone());
    }
}

fn display_status(status: &str) -> String {
    match status {
        "requested" | "pending" => "pending".into(),
        "started" | "running" => "running".into(),
        "awaiting_approval" | "permission_requested" => "waiting approval".into(),
        "failed" | "denied" | "cancelled" | "timed_out" | "rolled_back" => status.replace('_', " "),
        _ => String::new(),
    }
}

fn is_terminal_tool_status(status: &str) -> bool {
    matches!(
        status,
        "completed" | "failed" | "denied" | "cancelled" | "timed_out" | "rolled_back"
    )
}

fn can_transition_tool_status(current: &str, next: &str) -> bool {
    if current.is_empty() {
        return tool_status_stage(next).is_some();
    }
    if is_terminal_tool_status(current) {
        return current == next;
    }
    match (tool_status_stage(current), tool_status_stage(next)) {
        (Some(current), Some(next)) => next >= current,
        _ => false,
    }
}

fn tool_status_stage(status: &str) -> Option<u8> {
    match status {
        "requested" | "pending" => Some(1),
        "awaiting_approval" | "permission_requested" => Some(2),
        "started" | "running" => Some(3),
        "completed" | "failed" | "denied" | "cancelled" | "timed_out" | "rolled_back" => Some(4),
        _ => None,
    }
}

fn is_failure_status(status: &str) -> bool {
    matches!(
        status,
        "failed" | "denied" | "cancelled" | "timed_out" | "timed out" | "rolled_back"
    )
}

fn strip_json_fence(text: &str) -> &str {
    let trimmed = text.trim();
    trimmed
        .strip_prefix("```json")
        .or_else(|| trimmed.strip_prefix("```JSON"))
        .or_else(|| trimmed.strip_prefix("```"))
        .and_then(|value| value.strip_suffix("```"))
        .map(str::trim)
        .unwrap_or(trimmed)
}

fn is_action_json(text: &str) -> bool {
    let candidate = strip_json_fence(text);
    serde_json::from_str::<Value>(candidate)
        .ok()
        .and_then(|value| value.as_object().cloned())
        .is_some_and(|object| {
            object.contains_key("tool")
                || object.contains_key("actions")
                || object.contains_key("action")
        })
        || (candidate.starts_with('{')
            && ["\"tool\"", "\"actions\"", "\"action\""]
                .iter()
                .any(|field| candidate.contains(field)))
}

/// Human-visible assistant body (content-type channel, not field allowlists).
///
/// Pipeline:
/// 1. Internal action JSON → suppressed (`tool=done` keeps `summary` only).
/// 2. Whole-body JSON object/array → pretty ` ```json ` fence for markdown.
/// 3. Everything else → prose/markdown as authored (primary chat path).
///
/// Business keys like `result`/`path` are never special-cased; structured
/// facts belong on tool projectors, not free-form completion payloads.
#[cfg(test)]
fn visible_assistant_text(text: &str) -> Option<String> {
    visible_assistant_text_with_mode(text, true)
}

fn visible_assistant_text_with_mode(text: &str, project_report: bool) -> Option<String> {
    let trimmed = text.trim();
    if trimmed.is_empty() {
        return None;
    }
    if is_action_json(trimmed) {
        let candidate = strip_json_fence(trimmed);
        let object = serde_json::from_str::<Value>(candidate).ok()?;
        let object = object.as_object()?;
        let tool = object
            .get("tool")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if !tool.eq_ignore_ascii_case("done") {
            return None;
        }
        let summary = object
            .get("summary")
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|value| !value.is_empty())?;
        return Some(if project_report {
            project_structured_summary_as_markdown(summary).unwrap_or_else(|| summary.to_owned())
        } else {
            project_json_document_as_markdown(summary).unwrap_or_else(|| summary.to_owned())
        });
    }
    if let Some(fenced) = project_json_document_as_markdown(trimmed) {
        return Some(fenced);
    }
    Some(trimmed.to_owned())
}

fn project_structured_summary_as_markdown(text: &str) -> Option<String> {
    let candidate = strip_json_fence(text);
    let Value::Object(report) = serde_json::from_str::<Value>(candidate).ok()? else {
        return None;
    };
    let lead = report.get("summary")?.as_str()?.trim();
    if lead.is_empty() {
        return None;
    }

    let mut keys = report
        .keys()
        .filter(|key| key.as_str() != "summary")
        .cloned()
        .collect::<Vec<_>>();
    keys.sort_by(|left, right| {
        report_section_rank(left)
            .cmp(&report_section_rank(right))
            .then_with(|| left.cmp(right))
    });
    let mut markdown = lead.to_owned();
    for key in keys {
        let Some(section) = report
            .get(&key)
            .and_then(|value| render_report_value(value, 0))
            .filter(|value| !value.is_empty())
        else {
            continue;
        };
        markdown.push_str("\n\n**");
        markdown.push_str(&report_section_label(&key));
        markdown.push_str("**\n");
        markdown.push_str(&section);
    }
    Some(markdown)
}

fn report_section_rank(key: &str) -> usize {
    [
        "result",
        "architecture",
        "engineering",
        "changes",
        "risks",
        "commands",
        "tests",
        "verification",
        "next_steps",
    ]
    .iter()
    .position(|candidate| *candidate == key)
    .unwrap_or(100)
}

fn report_section_label(key: &str) -> String {
    key.replace(['_', '-'], " ")
        .split_whitespace()
        .map(|word| {
            let mut chars = word.chars();
            chars
                .next()
                .map(|first| first.to_uppercase().collect::<String>() + chars.as_str())
                .unwrap_or_default()
        })
        .collect::<Vec<_>>()
        .join(" ")
}

fn render_report_value(value: &Value, depth: usize) -> Option<String> {
    if depth > 3 {
        return None;
    }
    match value {
        Value::String(value) => Some(value.trim().to_owned()).filter(|value| !value.is_empty()),
        Value::Array(values) => {
            let lines = values
                .iter()
                .filter_map(|value| render_report_value(value, depth + 1))
                .map(|value| format!("- {}", value.replace('\n', "\n  ")))
                .collect::<Vec<_>>();
            (!lines.is_empty()).then(|| lines.join("\n"))
        }
        Value::Object(values) => {
            let mut keys = values.keys().collect::<Vec<_>>();
            keys.sort();
            let lines = keys
                .into_iter()
                .filter_map(|key| {
                    render_report_value(values.get(key)?, depth + 1).map(|value| {
                        format!(
                            "- **{}:** {}",
                            report_section_label(key),
                            value.replace('\n', "\n  ")
                        )
                    })
                })
                .collect::<Vec<_>>();
            (!lines.is_empty()).then(|| lines.join("\n"))
        }
        Value::Null => None,
        other => Some(other.to_string()),
    }
}

/// When the entire assistant body is one JSON object or array, render it as a
/// markdown code fence so the shared markdown pipeline pretty-prints it.
///
/// Does not invent prose from field names. Prose-with-embedded-JSON is left
/// alone (`from_str` fails on the full body). Scalars (`"hi"`, `1`, `true`)
/// stay raw so quoted strings are not force-fenced.
fn project_json_document_as_markdown(text: &str) -> Option<String> {
    let candidate = strip_json_fence(text);
    let first = candidate.chars().next()?;
    if first != '{' && first != '[' {
        return None;
    }
    let value: Value = serde_json::from_str(candidate).ok()?;
    match &value {
        Value::Object(_) | Value::Array(_) => {}
        _ => return None,
    }
    let pretty = serde_json::to_string_pretty(&value).ok()?;
    Some(format!("```json\n{pretty}\n```"))
}

fn nested_message(details: &BTreeMap<String, Value>, key: &str) -> Option<String> {
    details
        .get(key)
        .and_then(Value::as_object)
        .and_then(|value| value.get("message"))
        .and_then(safe_value)
}

fn nested_output(details: &BTreeMap<String, Value>, key: &str) -> Option<String> {
    let output = details.get(key)?.as_object()?;
    [
        "text", "content", "message", "summary", "stdout", "stderr", "result",
    ]
    .iter()
    .find_map(|field| output.get(*field).and_then(safe_value))
}

fn net_diff_is_represented(
    details: &BTreeMap<String, Value>,
    patch_tools: &HashMap<String, String>,
) -> bool {
    let Some(patches) = details.get("patches").and_then(Value::as_array) else {
        return false;
    };
    !patches.is_empty()
        && patches.iter().all(|patch| {
            patch
                .get("patch_id")
                .and_then(Value::as_str)
                .is_some_and(|patch_id| patch_tools.contains_key(patch_id))
        })
}

fn detail(details: &BTreeMap<String, Value>, key: &str) -> Option<String> {
    details.get(key).and_then(safe_value)
}

fn raw_string<'a>(details: &'a BTreeMap<String, Value>, key: &str) -> Option<&'a str> {
    details.get(key).and_then(Value::as_str)
}

fn first_detail(details: &BTreeMap<String, Value>, keys: &[&str]) -> Option<String> {
    keys.iter().find_map(|key| detail(details, key))
}

fn imported_source_title(details: &BTreeMap<String, Value>) -> Option<String> {
    details
        .get("imported")
        .and_then(Value::as_bool)
        .unwrap_or(false)
        .then(|| match detail(details, "source").as_deref() {
            Some("claude-code") => "Claude Code import".to_owned(),
            Some("codex") => "Codex import".to_owned(),
            Some(source) if !source.is_empty() => format!("{source} import"),
            _ => "Imported conversation".to_owned(),
        })
}

fn classify(value: &str) -> BlockKind {
    let value = value.to_ascii_lowercase();
    if value.contains("user") || value.contains("prompt") || value.contains("turn.input") {
        BlockKind::User
    } else if value.contains("assistant") || value.contains("message") || value.contains("result") {
        BlockKind::Assistant
    } else if value.contains("thinking") || value.contains("reasoning") {
        BlockKind::Thinking
    } else if value.contains("approval")
        || value.contains("permission")
        || value.contains("question")
        || value.contains("plan")
    {
        BlockKind::Governance
    } else if value.contains("tool")
        || value.contains("command")
        || value.contains("patch")
        || value.contains("file")
        || value.contains("diff")
    {
        BlockKind::Tool
    } else if value.contains("error") || value.contains("diagnostic") {
        BlockKind::Diagnostic
    } else {
        BlockKind::Status
    }
}

fn title(kind: BlockKind, wire_kind: &str) -> String {
    match kind {
        BlockKind::User => "You".into(),
        BlockKind::Assistant => "Carina".into(),
        BlockKind::Thinking => "Thinking".into(),
        BlockKind::Tool => humanize(wire_kind),
        BlockKind::Governance => "Needs input".into(),
        BlockKind::Status => humanize(wire_kind),
        BlockKind::Diagnostic => "Diagnostic".into(),
    }
}

fn summarize_details(details: &BTreeMap<String, Value>) -> String {
    const PRIORITY: &[&str] = &[
        "text", "content", "prompt", "message", "summary", "command", "stdout", "stderr", "output",
        "diff", "question", "reason", "error",
    ];
    for key in PRIORITY {
        if let Some(value) = detail(details, key) {
            return value;
        }
    }
    let mut fields = Vec::new();
    for (key, value) in details {
        if sensitive_key(key) {
            continue;
        }
        if let Some(value) = safe_value(value) {
            fields.push(format!("{}: {}", humanize(key), value));
        }
        if fields.len() == 4 {
            break;
        }
    }
    if fields.is_empty() {
        "No additional details".into()
    } else {
        fields.join("\n")
    }
}

fn safe_value(value: &Value) -> Option<String> {
    match value {
        Value::String(value) if !value.trim().is_empty() => Some(value.trim().to_owned()),
        Value::Number(value) => Some(value.to_string()),
        Value::Bool(value) => Some(value.to_string()),
        Value::Array(values) if !values.is_empty() => {
            let values = values.iter().filter_map(safe_value).collect::<Vec<_>>();
            (!values.is_empty()).then(|| values.join(", "))
        }
        _ => None,
    }
}

fn sensitive_key(key: &str) -> bool {
    let key = key.to_ascii_lowercase();
    key.contains("secret")
        || key.contains("credential")
        || key.contains("api_key")
        || key.contains("token")
        || key.contains("password")
}

fn humanize(value: &str) -> String {
    let value = value
        .rsplit(['.', '/'])
        .next()
        .unwrap_or(value)
        .replace(['_', '-'], " ");
    let mut chars = value.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().collect::<String>() + chars.as_str(),
        None => "Activity".into(),
    }
}

fn first_non_empty<const N: usize>(values: [String; N]) -> String {
    values
        .into_iter()
        .find(|value| !value.is_empty())
        .unwrap_or_else(|| "anonymous".into())
}

fn first_display<const N: usize>(values: [String; N]) -> String {
    values
        .into_iter()
        .find(|value| !value.trim().is_empty() && value != "anonymous")
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rpc::SessionItem;
    use crate::tool_projection::format_tool_title;
    use serde_json::json;
    use std::sync::atomic::{AtomicUsize, Ordering};

    fn wire(kind: &str, mut payload: Value) -> WireEvent {
        static NEXT_EVENT: AtomicUsize = AtomicUsize::new(1);
        if kind.starts_with("assistant.message.")
            && let Some(payload) = payload.as_object_mut()
        {
            payload
                .entry("phase")
                .or_insert_with(|| Value::String("final_answer".into()));
        }
        serde_json::from_value(json!({
            "type": kind,
            "event_id": format!("event-{kind}-{}", NEXT_EVENT.fetch_add(1, Ordering::Relaxed)),
            "session_id": "session-1",
            "run_id": "run-1",
            "payload": payload
        }))
        .unwrap()
    }

    fn wire_for_run(kind: &str, run_id: &str, payload: Value) -> WireEvent {
        let mut event = wire(kind, payload);
        event.run_id = run_id.into();
        event
    }

    fn turn_item_for_run(
        kind: &str,
        run_id: &str,
        source_event_id: &str,
        details: Value,
    ) -> SessionItemEvent {
        SessionItemEvent {
            kind: kind.into(),
            session_id: "session-1".into(),
            turn_id: run_id.into(),
            run_id: run_id.into(),
            item_id: String::new(),
            source_event_id: source_event_id.into(),
            timestamp: String::new(),
            details: serde_json::from_value(details).unwrap(),
            item: None,
        }
    }

    fn item(kind: &str, status: &str, details: Value) -> SessionItemEvent {
        SessionItemEvent {
            kind: "item.updated".into(),
            session_id: "session-1".into(),
            turn_id: "run-1".into(),
            run_id: "run-1".into(),
            item_id: "call-1".into(),
            source_event_id: String::new(),
            timestamp: String::new(),
            details: BTreeMap::new(),
            item: Some(SessionItem {
                id: "call-1".into(),
                kind: kind.into(),
                status: status.into(),
                run_id: "run-1".into(),
                details: serde_json::from_value(details).unwrap(),
            }),
        }
    }

    fn item_with_id(id: &str, kind: &str, status: &str, details: Value) -> SessionItemEvent {
        let mut event = item(kind, status, details);
        event.item_id = id.into();
        event.item.as_mut().expect("item exists").id = id.into();
        event
    }

    fn apply_read(
        reducer: &mut TranscriptReducer,
        blocks: &mut Vec<TranscriptBlock>,
        call_id: &str,
        path: &str,
        terminal: Option<(&str, Value)>,
    ) {
        reducer.reduce_event(
            blocks,
            wire(
                "ToolCallRequested",
                json!({"call_id":call_id,"tool":"read","arguments":{"path":path}}),
            ),
        );
        if let Some((kind, payload)) = terminal {
            reducer.reduce_event(blocks, wire(kind, payload));
        }
    }

    fn group_snapshot(block: &TranscriptBlock) -> String {
        let mut lines = vec![format!(
            "id={} title={} status={} expanded={}",
            block.id,
            block.localized_title(Locale::En),
            block.localized_status(Locale::En),
            block.expanded
        )];
        lines.extend(block.tool_members.iter().map(|member| {
            format!(
                "{} | title=[{}] | status=[{}] | body=[{}]",
                member.id,
                member.title,
                member.status,
                member.body.replace('\n', " / ")
            )
        }));
        lines.join("\n")
    }

    #[test]
    fn tool_lifecycle_reduces_to_one_compact_read_receipt() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"read","status":"pending","arguments":{"path":"src/lib.rs"}}),
            ),
            wire(
                "RuntimeStageChanged",
                json!({"call_id":"call-1","tool":"read","status":"running"}),
            ),
            wire(
                "ToolCallStarted",
                json!({"call_id":"call-1","tool":"read","status":"running"}),
            ),
            wire(
                "ToolRequested",
                json!({"tool":"read","arguments":{"path":"src/lib.rs"}}),
            ),
            wire(
                "ToolApproved",
                json!({"tool":"read","decision_id":"decision-1"}),
            ),
            wire("FileRead", json!({"path":"/repo/src/lib.rs","bytes":42})),
            wire(
                "ToolCallCompleted",
                json!({"call_id":"call-1","tool":"read","status":"completed","output":{"bytes":42,"redacted":true}}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].title, format_tool_title("Read", "src/lib.rs"));
        assert_eq!(blocks[0].status, "");
        assert_eq!(blocks[0].body, "");
        assert!(!blocks[0].is_collapsible());
    }

    #[test]
    fn duplicate_and_out_of_order_tool_events_cannot_regress_terminal_state() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        let requested = wire(
            "ToolCallRequested",
            json!({"call_id":"call-1","tool":"read","status":"pending","arguments":{"path":"src/lib.rs"}}),
        );
        assert!(reducer.reduce_event(&mut blocks, requested.clone()));
        assert!(!reducer.reduce_event(&mut blocks, requested));
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallCompleted",
                json!({"call_id":"call-1","tool":"read","status":"completed"}),
            ),
        ));
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallStarted",
                json!({"call_id":"call-1","tool":"read","status":"running"}),
            ),
        ));
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].tool_members[0].lifecycle, "completed");
    }

    #[test]
    fn five_consecutive_reads_project_as_one_retained_group() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for index in 1..=5 {
            let call_id = format!("read-{index}");
            let path = format!("src/file-{index}.rs");
            apply_read(
                &mut reducer,
                &mut blocks,
                &call_id,
                &path,
                Some((
                    "ToolCallCompleted",
                    json!({"call_id":call_id,"tool":"read","status":"completed"}),
                )),
            );
        }

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "tool:read-1");
        assert_eq!(blocks[0].tool_members.len(), 5);
        assert_eq!(
            blocks[0].localized_title(Locale::En),
            "Read ×5 · src/file-1.rs, src/file-2.rs, …"
        );
        assert!(!blocks[0].expanded);
        insta::assert_snapshot!(group_snapshot(&blocks[0]), @r"
id=tool:read-1 title=Read ×5 · src/file-1.rs, src/file-2.rs, … status= expanded=false
tool:read-1 | title=[src/file-1.rs] | status=[] | body=[]
tool:read-2 | title=[src/file-2.rs] | status=[] | body=[]
tool:read-3 | title=[src/file-3.rs] | status=[] | body=[]
tool:read-4 | title=[src/file-4.rs] | status=[] | body=[]
tool:read-5 | title=[src/file-5.rs] | status=[] | body=[]
");
    }

    #[test]
    fn intent_is_stable_across_live_lifecycle_and_hydrated_replay() {
        let requested = json!({
            "call_id":"read-1",
            "tool":"read",
            "intent":"Understand the transcript reducer",
            "arguments":{"path":"src/transcript.rs"}
        });
        let completed = json!({"call_id":"read-1","tool":"read","status":"completed"});

        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        reducer.reduce_event(&mut blocks, wire("ToolCallRequested", requested.clone()));
        reducer.reduce_event(&mut blocks, wire("ToolCallCompleted", completed));
        assert_eq!(blocks.len(), 1);
        assert_eq!(
            blocks[0].title,
            format_tool_title("Read", "Understand the transcript reducer")
        );
        assert_eq!(blocks[0].tool_members[0].title, "src/transcript.rs");

        let hydrated =
            TranscriptReducer::default().hydrate(vec![item("tool_call", "completed", requested)]);
        assert_eq!(hydrated.len(), 1);
        assert_eq!(hydrated[0].title, blocks[0].title);
        assert_eq!(hydrated[0].tool_members[0].title, "src/transcript.rs");
    }

    #[test]
    fn running_and_failed_group_members_keep_their_lifecycle_detail() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        apply_read(
            &mut reducer,
            &mut blocks,
            "read-1",
            "src/ready.rs",
            Some((
                "ToolCallCompleted",
                json!({"call_id":"read-1","tool":"read","status":"completed"}),
            )),
        );
        apply_read(&mut reducer, &mut blocks, "read-2", "src/running.rs", None);

        assert!(blocks[0].title.is_empty());
        assert_eq!(
            blocks[0].localized_title(Locale::En),
            "Reading ×2 · src/ready.rs, src/running.rs"
        );
        assert!(blocks[0].tool_members[1].is_running());
        insta::assert_snapshot!(group_snapshot(&blocks[0]), @r"
id=tool:read-1 title=Reading ×2 · src/ready.rs, src/running.rs status= expanded=false
tool:read-1 | title=[src/ready.rs] | status=[] | body=[]
tool:read-2 | title=[src/running.rs] | status=[pending] | body=[]
");

        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallFailed",
                json!({
                    "call_id":"read-2",
                    "tool":"read",
                    "status":"failed",
                    "error":{"message":"permission denied"}
                }),
            ),
        );
        assert!(blocks[0].title.is_empty());
        assert!(blocks[0].status.is_empty());
        assert_eq!(
            blocks[0].localized_title(Locale::En),
            "Read ×2 · src/ready.rs, src/running.rs"
        );
        assert_eq!(blocks[0].localized_status(Locale::En), "1 failed");
        assert!(
            blocks[0].expanded,
            "failure must break the collapsed success seal"
        );
        assert_eq!(blocks[0].tool_members[1].body, "permission denied");
        insta::assert_snapshot!(group_snapshot(&blocks[0]), @r"
id=tool:read-1 title=Read ×2 · src/ready.rs, src/running.rs status=1 failed expanded=true
tool:read-1 | title=[src/ready.rs] | status=[] | body=[]
tool:read-2 | title=[src/running.rs] | status=[failed] | body=[permission denied]
");

        // Later successes must not join a group that already carries a failure.
        apply_read(
            &mut reducer,
            &mut blocks,
            "read-3",
            "src/after.rs",
            Some((
                "ToolCallCompleted",
                json!({"call_id":"read-3","tool":"read","status":"completed"}),
            )),
        );
        assert_eq!(blocks.len(), 2);
        assert_eq!(blocks[1].id, "tool:read-3");
        assert_eq!(blocks[1].tool_members.len(), 1);
    }

    #[test]
    fn stable_group_id_toggles_the_group_after_indices_shift() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for index in 1..=2 {
            let call_id = format!("read-{index}");
            apply_read(
                &mut reducer,
                &mut blocks,
                &call_id,
                &format!("src/{index}.rs"),
                Some((
                    "ToolCallCompleted",
                    json!({"call_id":call_id,"tool":"read","status":"completed"}),
                )),
            );
        }
        let group_id = blocks[0].id.clone();
        blocks.insert(
            0,
            TranscriptBlock::local_user("earlier".into(), "Earlier prompt".into()),
        );

        assert!(toggle_block_expansion(&mut blocks, &group_id));
        assert!(blocks[1].expanded);
        assert!(!toggle_block_expansion(&mut blocks, "tool:missing"));
    }

    #[test]
    fn grouping_requires_true_semantic_and_timeline_adjacency() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for (call_id, tool) in [("one", "extension.alpha"), ("two", "extension.beta")] {
            reducer.reduce_event(
                &mut blocks,
                wire("ToolCallRequested", json!({"call_id":call_id,"tool":tool})),
            );
        }
        assert_eq!(blocks.len(), 2, "distinct extension tools are not one kind");

        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        apply_read(
            &mut reducer,
            &mut blocks,
            "read-1",
            "src/one.rs",
            Some((
                "ToolCallCompleted",
                json!({"call_id":"read-1","tool":"read","status":"completed"}),
            )),
        );
        blocks.push(message_block(
            "assistant:middle".into(),
            "run-1".into(),
            BlockKind::Assistant,
            "Carina",
            "I found the first file.".into(),
            String::new(),
        ));
        apply_read(&mut reducer, &mut blocks, "read-2", "src/two.rs", None);
        assert_eq!(
            blocks.len(),
            3,
            "visible conversation content closes a group"
        );
    }

    #[test]
    fn adjacent_read_only_discovery_tools_form_one_exploration_group() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for (call_id, tool, arguments) in [
            ("read-1", "read", json!({"path":"README.md"})),
            ("list-1", "list", json!({"path":"workspace"})),
            ("search-1", "search", json!({"pattern":"architecture"})),
            ("map-1", "code.map", json!({"query":"core modules"})),
        ] {
            reducer.reduce_event(
                &mut blocks,
                wire_for_run(
                    "ToolCallRequested",
                    "run-explore",
                    json!({"call_id":call_id,"tool":tool,"arguments":arguments}),
                ),
            );
            reducer.reduce_event(
                &mut blocks,
                wire_for_run(
                    "ToolCallCompleted",
                    "run-explore",
                    json!({"call_id":call_id,"tool":tool,"status":"completed"}),
                ),
            );
        }

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].tool_kind, Some(ToolKind::Explore));
        assert_eq!(blocks[0].tool_members.len(), 4);
        assert_eq!(
            blocks[0].localized_title(Locale::En),
            "Explored ×4 · README.md, workspace, …"
        );
        assert_eq!(
            blocks[0].localized_title(Locale::ZhHans),
            "已探索 ×4 · README.md, workspace, …"
        );
        assert!(!blocks[0].expanded);
    }

    #[test]
    fn running_code_intelligence_opens_a_previously_collapsed_exploration_group() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        apply_read(
            &mut reducer,
            &mut blocks,
            "read-first",
            "README.md",
            Some((
                "ToolCallCompleted",
                json!({"call_id":"read-first","tool":"read","status":"completed"}),
            )),
        );
        assert!(!blocks[0].expanded);

        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallStarted",
                json!({
                    "call_id":"map-running",
                    "tool":"code.map",
                    "status":"running",
                    "output":"entry -> runtime"
                }),
            ),
        );
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].tool_kind, Some(ToolKind::Explore));
        assert!(blocks[0].expanded);
        assert!(blocks[0].tool_members[1].body.contains("entry -> runtime"));

        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallCompleted",
                json!({
                    "call_id":"map-running",
                    "tool":"code.map",
                    "status":"completed",
                    "output":"entry -> runtime"
                }),
            ),
        );
        assert!(!blocks[0].expanded);
    }

    #[test]
    fn exploration_group_stops_at_run_write_and_failure_boundaries() {
        let exploration_block = |id: &str, run_id: &str, tool: &str, status: &str| {
            tool_block(&ToolRecord {
                id: format!("tool:{id}"),
                run_id: run_id.into(),
                tool: tool.into(),
                status: status.into(),
                details: BTreeMap::from([(
                    "arguments".into(),
                    json!({"path":format!("{id}.rs"),"pattern":id}),
                )]),
            })
        };

        let read = exploration_block("read", "run-1", "read", "completed");
        assert!(!tool_blocks_are_groupable(
            &read,
            &exploration_block("other-run", "run-2", "search", "completed")
        ));
        assert!(!tool_blocks_are_groupable(
            &read,
            &exploration_block("write", "run-1", "patch", "completed")
        ));
        assert!(!tool_blocks_are_groupable(
            &read,
            &exploration_block("failed", "run-1", "search", "failed")
        ));
    }

    #[test]
    fn settled_code_intelligence_collapses_but_running_and_failed_evidence_opens() {
        for (status, expected_expanded) in
            [("running", true), ("completed", false), ("failed", true)]
        {
            let block = tool_block(&ToolRecord {
                id: format!("tool:map-{status}"),
                run_id: "run-1".into(),
                tool: "code.map".into(),
                status: status.into(),
                details: BTreeMap::from([("output".into(), json!("symbol graph"))]),
            });
            assert_eq!(
                block.expanded, expected_expanded,
                "unexpected disclosure for {status}"
            );
        }
    }

    #[test]
    fn live_code_intelligence_settles_from_open_progress_to_collapsed_evidence() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallStarted",
                json!({
                    "call_id":"map-live",
                    "tool":"code.map",
                    "status":"running",
                    "output":"symbol graph"
                }),
            ),
        ));
        assert!(blocks[0].expanded);

        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallCompleted",
                json!({
                    "call_id":"map-live",
                    "tool":"code.map",
                    "status":"completed",
                    "output":"symbol graph\nentry -> runtime"
                }),
            ),
        ));
        assert!(!blocks[0].expanded);
        assert!(blocks[0].body.contains("entry -> runtime"));
    }

    #[test]
    fn hydration_and_live_events_share_the_group_projection() {
        let items = (1..=3)
            .map(|index| {
                item_with_id(
                    &format!("read-{index}"),
                    "tool_call",
                    "completed",
                    json!({
                        "tool":"read",
                        "arguments":{"path":format!("src/{index}.rs")}
                    }),
                )
            })
            .collect();
        let mut reducer = TranscriptReducer::default();
        let hydrated = reducer.hydrate(items);

        let mut live_reducer = TranscriptReducer::default();
        let mut live = Vec::new();
        for index in 1..=3 {
            let call_id = format!("read-{index}");
            apply_read(
                &mut live_reducer,
                &mut live,
                &call_id,
                &format!("src/{index}.rs"),
                Some((
                    "ToolCallCompleted",
                    json!({"call_id":call_id,"tool":"read","status":"completed"}),
                )),
            );
        }

        assert_eq!(hydrated.len(), 1);
        assert_eq!(group_snapshot(&hydrated[0]), group_snapshot(&live[0]));
    }

    #[test]
    fn mixed_exploration_has_live_hydrate_parity() {
        let tools = [
            ("read-1", "read", json!({"path":"README.md"})),
            ("list-1", "list", json!({"path":"workspace"})),
            ("search-1", "search", json!({"pattern":"runtime"})),
            ("map-1", "code.map", json!({"query":"entry points"})),
        ];
        let hydrated = TranscriptReducer::default().hydrate(
            tools
                .iter()
                .map(|(id, tool, arguments)| {
                    let mut item = item_with_id(
                        id,
                        "tool_call",
                        "completed",
                        json!({"tool":tool,"arguments":arguments}),
                    );
                    item.run_id = "run-explore".into();
                    item.turn_id = "run-explore".into();
                    item.item.as_mut().expect("item exists").run_id = "run-explore".into();
                    item
                })
                .collect(),
        );

        let mut reducer = TranscriptReducer::default();
        let mut live = Vec::new();
        for (id, tool, arguments) in tools {
            reducer.reduce_event(
                &mut live,
                wire_for_run(
                    "ToolCallRequested",
                    "run-explore",
                    json!({"call_id":id,"tool":tool,"arguments":arguments}),
                ),
            );
            reducer.reduce_event(
                &mut live,
                wire_for_run(
                    "ToolCallCompleted",
                    "run-explore",
                    json!({"call_id":id,"tool":tool,"status":"completed"}),
                ),
            );
        }

        assert_eq!(hydrated.len(), 1);
        assert_eq!(group_snapshot(&hydrated[0]), group_snapshot(&live[0]));
    }

    #[test]
    fn web_fetch_has_live_hydrate_parity_and_remains_a_visible_network_effect() {
        let details = json!({
            "tool":"web.fetch",
            "kind":"network",
            "intent":"查询北京实时天气",
            "arguments":{"host":"wttr.in"}
        });
        let mut hydrated_item = item_with_id("fetch-1", "tool_call", "completed", details.clone());
        hydrated_item.run_id = "run-fetch".into();
        hydrated_item.turn_id = "run-fetch".into();
        hydrated_item.item.as_mut().expect("item exists").run_id = "run-fetch".into();
        let hydrated = TranscriptReducer::default().hydrate(vec![hydrated_item]);

        let mut reducer = TranscriptReducer::default();
        let mut live = Vec::new();
        reducer.reduce_event(
            &mut live,
            wire_for_run(
                "ToolCallRequested",
                "run-fetch",
                json!({
                    "call_id":"fetch-1",
                    "tool":"web.fetch",
                    "kind":"network",
                    "intent":"查询北京实时天气",
                    "arguments":{"host":"wttr.in"}
                }),
            ),
        );
        reducer.reduce_event(
            &mut live,
            wire_for_run(
                "ToolCallCompleted",
                "run-fetch",
                json!({"call_id":"fetch-1","tool":"web.fetch","kind":"network","status":"completed"}),
            ),
        );

        assert_eq!(hydrated.len(), 1);
        assert_eq!(live.len(), 1);
        for block in [&hydrated[0], &live[0]] {
            assert_eq!(block.tool_kind, Some(ToolKind::WebFetch));
            assert!(block.title.contains("查询北京实时天气"));
            assert_eq!(block.tool_members[0].title, "wttr.in");
        }
    }

    #[test]
    fn user_message_is_a_hard_exploration_group_boundary() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        apply_read(&mut reducer, &mut blocks, "before-user", "README.md", None);
        blocks.push(user_block(
            "run-follow-up".into(),
            "run-follow-up".into(),
            "Check the runtime too".into(),
        ));
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallRequested",
                json!({"call_id":"after-user","tool":"search","arguments":{"pattern":"runtime"}}),
            ),
        );
        assert_eq!(blocks.len(), 3);
        assert_eq!(blocks[0].tool_kind, Some(ToolKind::Read));
        assert_eq!(blocks[2].tool_kind, Some(ToolKind::Search));
    }

    #[test]
    fn grouped_edits_retain_each_diff_and_aggregate_stats() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for (index, old, new) in [(1, "old", "new"), (2, "before", "after")] {
            let call_id = format!("edit-{index}");
            let path = format!("src/{index}.rs");
            let diff = format!("--- a/{path}\n+++ b/{path}\n-{old}\n+{new}");
            reducer.reduce_event(
                &mut blocks,
                wire(
                    "ToolCallRequested",
                    json!({
                        "call_id":call_id.clone(),
                        "tool":"patch",
                        "arguments":{"path":path},
                        "diff":diff
                    }),
                ),
            );
            reducer.reduce_event(
                &mut blocks,
                wire(
                    "ToolCallCompleted",
                    json!({"call_id":call_id,"tool":"patch","status":"completed"}),
                ),
            );
        }

        assert_eq!(blocks.len(), 1);
        assert_eq!(
            blocks[0].localized_title(Locale::En),
            "Edited ×2 · src/1.rs, src/2.rs"
        );
        assert!(
            !blocks[0].expanded,
            "successful edit groups stay collapsed by default"
        );
        assert_eq!((blocks[0].additions, blocks[0].deletions), (2, 2));
        assert!(blocks[0].tool_members[0].body.contains("-old\n+new"));
        assert!(blocks[0].tool_members[1].body.contains("-before\n+after"));
    }

    #[test]
    fn todo_calls_in_one_execution_replace_one_plan_component() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({
                    "call_id":"todo-1",
                    "tool":"TodoWrite",
                    "arguments":{"todos":[
                        {"content":"Inspect renderer","status":"in_progress"},
                        {"content":"Run tests","status":"pending"}
                    ]}
                }),
            ),
            wire(
                "ToolCallCompleted",
                json!({"call_id":"todo-1","tool":"TodoWrite","status":"completed"}),
            ),
            wire(
                "ToolCallRequested",
                json!({
                    "call_id":"todo-2",
                    "tool":"update_plan",
                    "arguments":{"plan":[
                        {"step":"Inspect renderer","status":"completed"},
                        {"step":"Run tests","status":"in_progress"}
                    ]}
                }),
            ),
            wire(
                "ToolCallCompleted",
                json!({"call_id":"todo-2","tool":"update_plan","status":"completed"}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }

        assert_eq!(blocks.len(), 1);
        let plan = &blocks[0];
        assert_eq!(plan.id, "plan:run-1");
        assert_eq!(plan.title, "Plan");
        assert!(plan.body.is_empty());
        assert_eq!(plan.todo_items.len(), 2);
        assert_eq!(plan.todo_items[0].content, "Inspect renderer");
        assert_eq!(plan.todo_items[0].status, "completed");
        assert_eq!(plan.todo_items[1].content, "Run tests");
        assert_eq!(plan.todo_items[1].status, "in_progress");
        assert!(!plan.collapsible);
        assert!(plan.expanded);
        assert!(plan.status.is_empty());
    }

    #[test]
    fn governance_items_are_state_not_conversation_content() {
        let mut reducer = TranscriptReducer::default();
        let blocks = reducer.hydrate(vec![
            item(
                "approval",
                "requested",
                json!({"decision_id":"approval-1","label":"Run tests"}),
            ),
            item(
                "question",
                "requested",
                json!({"question_id":"question-1","prompt":"Continue?"}),
            ),
            item(
                "question",
                "resolved",
                json!({
                    "status":"user_question_resolved",
                    "question_id":"question-1",
                    "value":"北京",
                    "timed_out":false
                }),
            ),
        ]);

        assert!(blocks.is_empty());
    }

    #[test]
    fn ask_user_lifecycle_is_owned_only_by_the_governance_overlay() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({
                    "call_id":"question-call",
                    "tool":"ask_user",
                    "kind":"interaction",
                    "intent":"Need a city before querying weather"
                }),
            ),
            wire(
                "ToolCallStarted",
                json!({"call_id":"question-call","tool":"ask_user","kind":"interaction"}),
            ),
            wire(
                "ToolCallCompleted",
                json!({"call_id":"question-call","tool":"ask_user","kind":"interaction"}),
            ),
        ] {
            assert!(!reducer.reduce_event(&mut blocks, event));
        }
        assert!(blocks.is_empty());

        let hydrated = TranscriptReducer::default().hydrate(vec![item_with_id(
            "question-call",
            "tool_call",
            "completed",
            json!({"tool":"ask_user","kind":"interaction"}),
        )]);
        assert!(hydrated.is_empty());
    }

    #[test]
    fn live_governance_events_are_state_not_conversation_content() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "permission.request",
                json!({"decision_id":"approval-1","label":"Run tests"}),
            ),
            wire(
                "user.question",
                json!({"question_id":"question-1","prompt":"Continue?"}),
            ),
        ] {
            assert!(!reducer.reduce_event(&mut blocks, event));
        }
        assert!(blocks.is_empty());
    }

    #[test]
    fn done_action_summary_becomes_assistant_bubble() {
        let mut reducer = TranscriptReducer::default();
        let blocks = reducer.hydrate(vec![item(
            "agent_message",
            "completed",
            json!({"text": r#"{"tool":"done","summary":"你好！"}"#}),
        )]);
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].kind, BlockKind::Assistant);
        assert_eq!(blocks[0].body, "你好！");

        let mut live = TranscriptReducer::default();
        let mut live_blocks = Vec::new();
        live.reduce_event(
            &mut live_blocks,
            wire(
                "ModelResponded",
                json!({"text": r#"{"tool":"done","summary":"我是 Codex"}"#}),
            ),
        );
        assert_eq!(live_blocks.len(), 1);
        assert_eq!(live_blocks[0].body, "我是 Codex");
        assert_eq!(live_blocks[0].id, "assistant:run-1");
        assert_eq!(
            live_blocks[0].assistant_phase,
            Some(AssistantMessagePhase::FinalAnswer)
        );

        let mut hidden = TranscriptReducer::default();
        let hidden_blocks = hidden.hydrate(vec![item(
            "agent_message",
            "completed",
            json!({"text": r#"{"tool":"read","path":"main.go"}"#}),
        )]);
        assert!(hidden_blocks.is_empty());
    }

    #[test]
    fn structured_done_report_becomes_readable_markdown_live_and_replayed() {
        let report = r#"{"summary":"Pebble 是本地优先 IDE。","architecture":["桌面端：Tauri","运行时：Go"],"commands":{"development":"pnpm dev"},"verification":"没有修改文件。"}"#;
        let action = json!({"tool":"done","summary":report}).to_string();

        let mut live = TranscriptReducer::default();
        let mut live_blocks = Vec::new();
        assert!(live.reduce_event(
            &mut live_blocks,
            wire("ModelResponded", json!({"text": action})),
        ));
        let live_body = &live_blocks[0].body;
        assert!(live_body.starts_with("Pebble 是本地优先 IDE。"));
        assert!(live_body.contains("**Architecture**\n- 桌面端：Tauri"));
        assert!(live_body.contains("**Commands**\n- **Development:** pnpm dev"));
        assert!(live_body.contains("**Verification**\n没有修改文件。"));
        assert!(!live_body.starts_with('{'));
        assert!(!live_body.starts_with("```json"));

        let replayed = TranscriptReducer::default().hydrate(vec![item(
            "agent_message",
            "completed",
            json!({"text": report,"presentation_text":live_body}),
        )]);
        assert_eq!(replayed.len(), 1);
        assert_eq!(replayed[0].body, *live_body);
    }

    #[test]
    fn explicit_structured_output_remains_json() {
        let report = r#"{"summary":"machine result","architecture":["runtime"]}"#;
        let action = json!({"tool":"done","summary":report}).to_string();
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "ModelResponded",
                json!({"text":action,"structured_output":true}),
            ),
        ));
        assert!(blocks[0].body.starts_with("```json\n"));
        assert!(blocks[0].body.contains(r#""summary": "machine result""#));
        assert!(!blocks[0].body.contains("**Architecture**"));

        let generic = visible_assistant_text_with_mode(
            r#"{"summary":"user-requested JSON","architecture":["runtime"]}"#,
            true,
        )
        .unwrap();
        assert!(generic.starts_with("```json\n"));
        assert!(!generic.contains("**Architecture**"));
    }

    #[test]
    fn durable_model_response_seals_the_existing_live_component() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.completed",
                json!({"generation":1,"sequence":1,"content":"done"}),
            ),
        ));
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "ModelResponded",
                json!({"text": r#"{"tool":"done","summary":"done"}"#}),
            ),
        ));
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "assistant:run-1");
        assert_eq!(blocks[0].body, "done");
        assert_eq!(
            blocks[0].assistant_phase,
            Some(AssistantMessagePhase::FinalAnswer)
        );
    }

    #[test]
    fn identical_adjacent_final_answer_replaces_commentary_from_an_alias_id() {
        let mut blocks = vec![{
            let mut block = message_block(
                "assistant:commentary-run".into(),
                "commentary-run".into(),
                BlockKind::Assistant,
                "Carina",
                "你好，我是 Carina。有什么需要我处理的？".into(),
                String::new(),
            );
            block.assistant_phase = Some(AssistantMessagePhase::Commentary);
            block
        }];
        let mut final_answer = message_block(
            "assistant:final-run".into(),
            "final-run".into(),
            BlockKind::Assistant,
            "Carina",
            "你好，我是 Carina。有什么需要我处理的？".into(),
            String::new(),
        );
        final_answer.assistant_phase = Some(AssistantMessagePhase::FinalAnswer);

        assert!(upsert_projected_block(&mut blocks, final_answer));
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "assistant:final-run");
        assert_eq!(
            blocks[0].assistant_phase,
            Some(AssistantMessagePhase::FinalAnswer)
        );
    }

    #[test]
    fn distinct_commentary_is_retained_before_the_final_answer() {
        let mut blocks = vec![{
            let mut block = message_block(
                "assistant:commentary-run".into(),
                "commentary-run".into(),
                BlockKind::Assistant,
                "Carina",
                "正在检查工作区".into(),
                String::new(),
            );
            block.assistant_phase = Some(AssistantMessagePhase::Commentary);
            block
        }];
        let mut final_answer = message_block(
            "assistant:final-run".into(),
            "final-run".into(),
            BlockKind::Assistant,
            "Carina",
            "检查完成".into(),
            String::new(),
        );
        final_answer.assistant_phase = Some(AssistantMessagePhase::FinalAnswer);

        assert!(upsert_projected_block(&mut blocks, final_answer));
        assert_eq!(blocks.len(), 2);
    }

    #[test]
    fn assistant_deltas_replace_one_generation_fenced_block() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":1,"delta":"正在"}),
            ),
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":2,"delta":"处理 **结果**"}),
            ),
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":2,"delta":" duplicate"}),
            ),
            wire(
                "assistant.message.completed",
                json!({"generation":1,"sequence":3,"content":"正在处理 **结果**。"}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "assistant:run-1");
        assert_eq!(blocks[0].body, "正在处理 **结果**。");
        assert!(!blocks[0].body.contains("duplicate"));
        assert_eq!(blocks[0].layout_revision, 3);
    }

    #[test]
    fn assistant_stream_rejects_gaps_and_stale_generations() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "assistant.message.delta",
                json!({"generation":2,"sequence":1,"delta":"new"}),
            ),
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":2,"delta":" stale"}),
            ),
            wire(
                "assistant.message.delta",
                json!({"generation":2,"sequence":3,"delta":" gap"}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].body, "new");
    }

    #[test]
    fn assistant_reset_removes_failed_generation_content() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":1,"delta":"discard me"}),
            ),
        );
        reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.reset",
                json!({"generation":2,"sequence":1}),
            ),
        );
        reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":2,"sequence":2,"delta":"replacement"}),
            ),
        );

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].body, "replacement");
        assert_eq!(blocks[0].layout_revision, 2);
    }

    #[test]
    fn tool_cancellation_seals_the_existing_cell_without_an_orphan() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"run","status":"pending","arguments":{"executable":"cargo","argc":2}}),
            ),
            wire(
                "ToolCallStarted",
                json!({"call_id":"call-1","tool":"run","status":"running"}),
            ),
            wire(
                "ToolCallCancelled",
                json!({"call_id":"call-1","tool":"run","status":"cancelled"}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "tool:call-1");
        assert_eq!(blocks[0].status, "cancelled");
    }

    #[test]
    fn terminal_completion_seals_the_existing_stream_block() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":1,"delta":"partial"}),
            ),
        );
        reducer.reduce_event(
            &mut blocks,
            wire("ExecutionCompleted", json!({"summary":"complete answer"})),
        );

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "assistant:run-1");
        assert_eq!(blocks[0].body, "complete answer");
    }

    #[test]
    fn live_policy_violation_is_owned_by_the_run_component() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"run","arguments":{"command":["cargo","test"]}}),
            ),
        );
        reducer.reduce_event(
            &mut blocks,
            wire(
                "PolicyViolation",
                json!({"call_id":"call-1","reason":"risk level 4 requires an explicit profile"}),
            ),
        );

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].kind, BlockKind::Tool);
        assert_eq!(blocks[0].status, "denied");
        assert!(blocks[0].body.contains("explicit profile"));
    }

    #[test]
    fn hydrated_policy_violation_does_not_duplicate_the_run_component() {
        let mut reducer = TranscriptReducer::default();
        let blocks = reducer.hydrate(vec![
            item(
                "tool_call",
                "requested",
                json!({"tool":"run","arguments":{"command":["cargo","test"]}}),
            ),
            item(
                "error",
                "failed",
                json!({
                    "source_type":"PolicyViolation",
                    "call_id":"call-1",
                    "reason":"risk level 4 requires an explicit profile"
                }),
            ),
        ]);

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].kind, BlockKind::Tool);
        assert_eq!(blocks[0].status, "denied");
        assert!(blocks[0].body.contains("explicit profile"));
    }

    #[test]
    fn completed_code_intelligence_stays_available_as_collapsed_semantic_evidence() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"code.def","arguments":{"name":"Kernel"}}),
            ),
        );
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallCompleted",
                json!({
                    "call_id":"call-1",
                    "tool":"code.def",
                    "status":"completed",
                    "output":"definitions (1, precision:lsp):\n  crates/kernel.rs:42:7"
                }),
            ),
        );

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].title, format_tool_title("Def", "Kernel"));
        assert_eq!(blocks[0].body_kind, BlockBodyKind::CodeIntelligence);
        assert!(blocks[0].collapsible);
        assert!(!blocks[0].expanded);
        assert!(blocks[0].body.contains("crates/kernel.rs:42:7"));
        assert!(toggle_block_expansion(&mut blocks, "tool:call-1"));
        assert!(blocks[0].expanded);
    }

    #[test]
    fn audit_only_tool_events_never_create_live_receipts() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire("ToolRequested", json!({"tool":"read"})),
            wire("ToolApproved", json!({"tool":"read"})),
            wire("ToolDenied", json!({"tool":"read","reason":"denied"})),
            wire("FileWriteProposed", json!({"path":"src/lib.rs"})),
        ] {
            assert!(!reducer.reduce_event(&mut blocks, event));
        }
        assert!(blocks.is_empty());
    }

    #[test]
    fn command_audit_events_enrich_one_lifecycle_component() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"run","status":"pending","arguments":{"executable":"cargo","argc":2}}),
            ),
            wire(
                "ToolCallStarted",
                json!({"call_id":"call-1","tool":"run","status":"running"}),
            ),
            wire(
                "CommandStarted",
                json!({"command_id":"cmd-1","command":"cargo test","cwd":"/repo"}),
            ),
            wire(
                "CommandOutput",
                json!({"command_id":"cmd-1","stream":"stdout","chunk":"ok\n"}),
            ),
            wire("CommandExited", json!({"command_id":"cmd-1","exit_code":0})),
            wire(
                "ToolCallCompleted",
                json!({"call_id":"call-1","tool":"run","status":"completed"}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].title, format_tool_title("Run", "cargo test"));
        assert_eq!(blocks[0].body, "ok");
        assert!(blocks[0].is_collapsible());
        assert_eq!(blocks[0].status, "");
    }

    #[test]
    fn command_chunks_preserve_boundary_whitespace_and_duplicate_terminal_is_stable() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"run","arguments":{"executable":"printf"}}),
            ),
            wire("ToolCallStarted", json!({"call_id":"call-1","tool":"run"})),
            wire("CommandOutput", json!({"stream":"stdout","chunk":"foo "})),
            wire("CommandOutput", json!({"stream":"stdout","chunk":"bar\n"})),
            wire("CommandOutput", json!({"stream":"stdout","chunk":"baz\n"})),
            wire(
                "ToolCallCompleted",
                json!({"call_id":"call-1","tool":"run","status":"completed"}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].body, "foo bar\nbaz");
        blocks[0].expanded = true;
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallCompleted",
                json!({"call_id":"call-1","tool":"run","status":"completed"}),
            ),
        ));
        assert!(blocks[0].expanded);
    }

    #[test]
    fn command_without_output_is_a_compact_receipt() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"run","status":"pending","arguments":{"executable":"true","argc":1}}),
            ),
            wire(
                "ToolCallCompleted",
                json!({"call_id":"call-1","tool":"run","status":"completed"}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].title, format_tool_title("Run", "true (1 args)"));
        assert!(blocks[0].body.is_empty());
        assert!(!blocks[0].is_collapsible());
    }

    #[test]
    fn mcp_and_extension_results_keep_typed_inspectable_output() {
        for (tool, target) in [("mcp", "docs.search"), ("custom.remote", "")] {
            let mut reducer = TranscriptReducer::default();
            let mut blocks = Vec::new();
            reducer.reduce_event(
                &mut blocks,
                wire(
                    "ToolCallRequested",
                    json!({
                        "call_id":"call-1",
                        "tool":tool,
                        "arguments":{"mcp_tool":target}
                    }),
                ),
            );
            reducer.reduce_event(
                &mut blocks,
                wire(
                    "ToolCallCompleted",
                    json!({
                        "call_id":"call-1",
                        "tool":tool,
                        "status":"completed",
                        "output":{"text":"Found three references"}
                    }),
                ),
            );
            assert_eq!(blocks.len(), 1);
            assert_eq!(blocks[0].body, "Found three references");
            assert!(blocks[0].is_collapsible());
            assert!(!blocks[0].expanded);
        }
    }

    #[test]
    fn artifact_output_updates_only_its_existing_tool_component() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for call_id in ["call-1", "call-2"] {
            reducer.reduce_event(
                &mut blocks,
                wire(
                    "ToolCallRequested",
                    json!({"call_id":call_id,"tool":"mcp.lookup","arguments":{"query":"docs"}}),
                ),
            );
            reducer.reduce_event(
                &mut blocks,
                wire(
                    "ToolCallCompleted",
                    json!({"call_id":call_id,"tool":"mcp.lookup","status":"completed"}),
                ),
            );
        }

        assert!(reducer.apply_tool_output(&mut blocks, "call-1", "artifact result".into(), false,));
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].tool_members.len(), 2);
        assert!(
            blocks[0]
                .tool_members
                .iter()
                .find(|member| member.id == "tool:call-1")
                .unwrap()
                .body
                .contains("artifact result")
        );
        assert!(
            !blocks[0]
                .tool_members
                .iter()
                .find(|member| member.id == "tool:call-2")
                .unwrap()
                .body
                .contains("artifact result")
        );
        assert!(!reducer.apply_tool_output(&mut blocks, "missing", "ignored".into(), false));
    }

    #[test]
    fn patch_artifact_receipt_cannot_replace_the_reviewable_diff() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({
                    "call_id":"call-1",
                    "tool":"patch",
                    "status":"pending",
                    "arguments":{"path":"src/lib.rs"}
                }),
            ),
            wire(
                "PatchProposed",
                json!({
                    "patch_id":"patch-1",
                    "affected_files":["src/lib.rs"],
                    "diff":"--- a/src/lib.rs\n+++ b/src/lib.rs\n@@ -1 +1 @@\n-old\n+new"
                }),
            ),
            wire(
                "ToolCallStarted",
                json!({"call_id":"call-1","tool":"patch","status":"running"}),
            ),
            wire("PatchApplied", json!({"patch_id":"patch-1"})),
            wire(
                "ToolCallCompleted",
                json!({
                    "call_id":"call-1",
                    "tool":"patch",
                    "status":"completed",
                    "artifact_ids":["artifact-1"]
                }),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }

        assert!(!reducer.apply_tool_output(
            &mut blocks,
            "call-1",
            "patch patch-1 applied (rollbackable)".into(),
            false,
        ));
        assert_eq!(blocks.len(), 1);
        assert!(blocks[0].body.contains("-old\n+new"));
        assert!(!blocks[0].body.contains("patch patch-1 applied"));
        assert_eq!((blocks[0].additions, blocks[0].deletions), (1, 1));
        assert!(!blocks[0].expanded, "successful edits start collapsed");
        assert_eq!(blocks[0].status, "applied");
    }

    #[test]
    fn truncated_artifact_output_preserves_content_without_inventing_recovery() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"extension.run"}),
            ),
        );

        assert!(reducer.apply_tool_output(&mut blocks, "call-1", "partial output".into(), true,));
        assert_eq!(blocks[0].body, "partial output");
        assert!(!blocks[0].body.contains("Ctrl-O"));
        assert!(!blocks[0].body.contains("artifact"));
        assert_eq!(
            reducer.tools["tool:call-1"]
                .details
                .get("artifact_output_truncated"),
            Some(&Value::Bool(true))
        );
    }

    #[test]
    fn failed_read_keeps_semantic_status_and_requested_path() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"read","arguments":{"path":"missing.txt"}}),
            ),
        );
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallDenied",
                json!({"call_id":"call-1","tool":"read","status":"denied","error":{"message":"outside workspace"}}),
            ),
        );
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].title, format_tool_title("Read", "missing.txt"));
        assert_eq!(blocks[0].status, "denied");
        assert_eq!(blocks[0].body, "outside workspace");
        assert!(blocks[0].is_collapsible());
        assert!(blocks[0].expanded);
    }

    #[test]
    fn hydration_reduces_item_updates_and_hides_action_json() {
        let mut reducer = TranscriptReducer::default();
        let items = vec![
            item(
                "tool_call",
                "requested",
                json!({"tool":"search","arguments":{"pattern":"TranscriptReducer"}}),
            ),
            item(
                "tool_call",
                "completed",
                json!({"tool":"search","status":"completed"}),
            ),
            item(
                "agent_message",
                "completed",
                json!({"text":"{\"tool\":\"read\",\"path\":\"src/lib.rs\"}"}),
            ),
        ];
        let blocks = reducer.hydrate(items);
        assert_eq!(blocks.len(), 1);
        assert_eq!(
            blocks[0].title,
            format_tool_title("Search", "TranscriptReducer")
        );
        assert_eq!(blocks[0].status, "");
        assert!(!blocks[0].is_collapsible());
    }

    #[test]
    fn whole_body_json_projects_to_markdown_fence_not_field_prose() {
        let payload = json!({
            "result": "已在桌面创建可直接运行的 ASCII Art 马里奥第一关游戏。",
            "path": "/workspace/ascii-mario-world-1-1/index.html",
            "features": ["横版卷轴 ASCII 场景", "移动与跳跃操作"],
            "verification": "已通过 JavaScript 语法与核心游戏结构检查。",
            "launch": "直接在浏览器中打开 index.html 即可游玩。"
        });
        let raw = payload.to_string();

        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(&mut blocks, wire("ModelResponded", json!({"text": raw})),));
        assert_eq!(blocks.len(), 1);
        let body = &blocks[0].body;
        assert!(
            body.starts_with("```json\n"),
            "must enter markdown json fence: {body}"
        );
        assert!(body.trim_end().ends_with("```"), "fence must close: {body}");
        assert!(
            body.contains("\"result\""),
            "content-type path keeps JSON structure (no field whitelist): {body}"
        );
        assert!(
            body.contains('\n'),
            "pretty-print should introduce newlines: {body}"
        );
        // Compact single-line dump must not remain the body form.
        assert_ne!(body.trim(), raw.trim());

        // Any object/array — not only "result"/"path" shapes — gets the same treatment.
        let generic = visible_assistant_text(r#"{"foo":1,"bar":[2,3]}"#).unwrap();
        assert!(generic.starts_with("```json\n"));
        assert!(generic.contains("\"foo\""));

        // Prose with an embedded object is not rewritten (content-type = markdown).
        let prose = "Done. Details: {\"path\":\"/tmp/x\"}";
        assert_eq!(visible_assistant_text(prose).as_deref(), Some(prose));

        // Action channel still suppressed / tool=done → summary.
        assert_eq!(
            visible_assistant_text(r#"{"tool":"read","path":"x"}"#),
            None
        );
        assert_eq!(
            visible_assistant_text(r#"{"tool":"done","summary":"All set."}"#).as_deref(),
            Some("All set.")
        );
    }

    #[test]
    fn hydration_merges_patch_and_net_diff_into_one_edit_component() {
        let mut reducer = TranscriptReducer::default();
        let blocks = reducer.hydrate(vec![
            item(
                "tool_call",
                "requested",
                json!({"tool":"patch","arguments":{"path":"src/lib.rs"}}),
            ),
            SessionItemEvent {
                kind: "item.updated".into(),
                session_id: "session-1".into(),
                turn_id: "run-1".into(),
                run_id: "run-1".into(),
                item_id: "patch-1".into(),
                source_event_id: String::new(),
                timestamp: String::new(),
                details: BTreeMap::new(),
                item: Some(SessionItem {
                    id: "patch-1".into(),
                    kind: "file_change".into(),
                    status: "proposed".into(),
                    run_id: "run-1".into(),
                    details: serde_json::from_value(json!({
                        "patch_id":"patch-1",
                        "affected_files":["src/lib.rs"],
                        "diff":"--- a/src/lib.rs\n+++ b/src/lib.rs\n+pub fn ready() {}"
                    }))
                    .unwrap(),
                }),
            },
            item(
                "tool_call",
                "completed",
                json!({"tool":"patch","status":"completed"}),
            ),
            SessionItemEvent {
                kind: "item.completed".into(),
                session_id: "session-1".into(),
                turn_id: "run-1".into(),
                run_id: "run-1".into(),
                item_id: "diff-run-1".into(),
                source_event_id: String::new(),
                timestamp: String::new(),
                details: BTreeMap::new(),
                item: Some(SessionItem {
                    id: "diff-run-1".into(),
                    kind: "turn_net_diff".into(),
                    status: "completed".into(),
                    run_id: "run-1".into(),
                    details: serde_json::from_value(json!({
                        "patches":[{"patch_id":"patch-1","status":"applied"}],
                        "active_files":["src/lib.rs"]
                    }))
                    .unwrap(),
                }),
            },
        ]);

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].title, format_tool_title("Edit", "src/lib.rs"));
        assert!(blocks[0].body.contains("pub fn ready"));
        assert!(blocks[0].is_collapsible());
    }

    #[test]
    fn hydration_retains_grouped_edits_before_later_tool_events() {
        let mut reducer = TranscriptReducer::default();
        let hydrated = reducer.hydrate(vec![
            item_with_id(
                "call-edit",
                "tool_call",
                "requested",
                json!({"tool":"patch","arguments":{"path":"src/snake.cpp"}}),
            ),
            item_with_id(
                "patch-edit",
                "file_change",
                "proposed",
                json!({
                    "patch_id":"patch-edit",
                    "affected_files":["src/snake.cpp"],
                    "diff":"--- a/src/snake.cpp\n+++ b/src/snake.cpp\n@@ -1 +1 @@\n-old\n+EDIT-DIFF-UNIQUE"
                }),
            ),
            item_with_id(
                "call-edit",
                "tool_call",
                "completed",
                json!({"tool":"patch"}),
            ),
            item_with_id(
                "call-edit-failed",
                "tool_call",
                "requested",
                json!({"tool":"patch","arguments":{"path":"src/broken.cpp"}}),
            ),
            item_with_id(
                "call-edit-failed",
                "tool_call",
                "failed",
                json!({"tool":"patch","error":{"message":"EDIT-FAILURE-UNIQUE"}}),
            ),
        ]);

        assert_eq!(hydrated.len(), 1);
        assert_eq!(
            hydrated[0].localized_title(Locale::En),
            "Edited ×2 · src/snake.cpp, src/broken.cpp"
        );
        assert_eq!(hydrated[0].localized_status(Locale::En), "1 failed");
        assert!(
            hydrated[0]
                .tool_members
                .iter()
                .any(|member| member.body.contains("EDIT-DIFF-UNIQUE"))
        );

        let mut blocks = hydrated;
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-todo","tool":"TodoWrite","arguments":{"todos":[]}}),
            ),
        );
        assert_eq!(
            blocks[0].localized_title(Locale::En),
            "Edited ×2 · src/snake.cpp, src/broken.cpp"
        );
    }

    #[test]
    fn patch_audit_terminal_event_completes_the_bound_edit_component() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"patch","status":"pending"}),
            ),
            wire(
                "PatchProposed",
                json!({
                    "patch_id":"patch-1",
                    "affected_files":["src/lib.rs"],
                    "diff":"--- a/src/lib.rs\n+++ b/src/lib.rs\n+pub fn ready() {}"
                }),
            ),
            wire(
                "PatchApplied",
                json!({"patch_id":"patch-1","affected_files":["src/lib.rs"]}),
            ),
        ] {
            reducer.reduce_event(&mut blocks, event);
        }

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].title, format_tool_title("Edit", "src/lib.rs"));
        assert_eq!(blocks[0].status, "applied");
        assert!(blocks[0].body.contains("pub fn ready"));
        assert!(blocks[0].is_collapsible());
        assert!(!blocks[0].expanded, "successful edits start collapsed");
    }

    #[test]
    fn explicit_edit_fold_survives_apply_completion() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        reducer.reduce_event(
            &mut blocks,
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"patch","status":"pending"}),
            ),
        );
        reducer.reduce_event(
            &mut blocks,
            wire(
                "PatchProposed",
                json!({
                    "patch_id":"patch-1",
                    "affected_files":["src/lib.rs"],
                    "diff":"--- a/src/lib.rs\n+++ b/src/lib.rs\n-old\n+new"
                }),
            ),
        );
        assert!(!blocks[0].expanded, "edits start collapsed");
        // Operator expands for review, then apply must not re-open a closed fold.
        blocks[0].expanded = true;
        blocks[0].expanded = false;

        reducer.reduce_event(
            &mut blocks,
            wire(
                "PatchApplied",
                json!({"patch_id":"patch-1","affected_files":["src/lib.rs"]}),
            ),
        );

        assert_eq!(blocks[0].status, "applied");
        assert!(!blocks[0].expanded);
    }

    #[test]
    fn hydration_keys_standalone_patch_lifecycle_by_patch_id() {
        let mut reducer = TranscriptReducer::default();
        let mut proposed = item(
            "file_change",
            "proposed",
            json!({
                "patch_id":"patch-1",
                "affected_files":["src/lib.rs"],
                "diff":"--- a/src/lib.rs\n+++ b/src/lib.rs\n+pub fn ready() {}"
            }),
        );
        proposed.item_id = "file-event-1".into();
        proposed.item.as_mut().expect("file change").id = "file-event-1".into();
        let mut applied = item(
            "file_change",
            "applied",
            json!({"patch_id":"patch-1","affected_files":["src/lib.rs"]}),
        );
        applied.item_id = "file-event-2".into();
        applied.item.as_mut().expect("file change").id = "file-event-2".into();

        let blocks = reducer.hydrate(vec![proposed, applied]);

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "patch:patch-1");
        assert_eq!(blocks[0].title, format_tool_title("Edit", "src/lib.rs"));
        assert_eq!(blocks[0].status, "applied");
        assert!(blocks[0].body.contains("pub fn ready"));
        assert!(
            !blocks[0].expanded,
            "hydrated successful edits stay collapsed"
        );
    }

    #[test]
    fn live_and_hydrated_read_have_the_same_presentation() {
        let mut live_reducer = TranscriptReducer::default();
        let mut live = Vec::new();
        live_reducer.reduce_event(
            &mut live,
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"read","arguments":{"path":"src/main.rs"}}),
            ),
        );
        live_reducer.reduce_event(
            &mut live,
            wire(
                "ToolCallCompleted",
                json!({"call_id":"call-1","tool":"read","status":"completed"}),
            ),
        );

        let mut hydrated_reducer = TranscriptReducer::default();
        let hydrated = hydrated_reducer.hydrate(vec![
            item(
                "tool_call",
                "requested",
                json!({"tool":"read","arguments":{"path":"src/main.rs"}}),
            ),
            item(
                "tool_call",
                "completed",
                json!({"tool":"read","status":"completed"}),
            ),
        ]);

        assert_eq!(live.len(), 1);
        assert_eq!(hydrated.len(), 1);
        assert_eq!(live[0].title, hydrated[0].title);
        assert_eq!(live[0].body, hydrated[0].body);
        assert_eq!(live[0].status, hydrated[0].status);
        assert_eq!(live[0].collapsible, hydrated[0].collapsible);
    }

    #[test]
    fn live_and_hydrated_sequences_have_the_same_component_identity_tree() {
        use crate::semantic_cell::SemanticCellKind;

        let mut live_reducer = TranscriptReducer::default();
        let mut live = Vec::new();
        for event in [
            wire(
                "ToolCallRequested",
                json!({"call_id":"call-1","tool":"run","status":"pending","arguments":{"executable":"cargo","argc":2}}),
            ),
            wire(
                "ToolCallStarted",
                json!({"call_id":"call-1","tool":"run","status":"running"}),
            ),
            wire(
                "ToolCallCompleted",
                json!({"call_id":"call-1","tool":"run","status":"completed","output":"ok"}),
            ),
            wire(
                "assistant.message.completed",
                json!({"generation":1,"sequence":1,"content":"done"}),
            ),
        ] {
            live_reducer.reduce_event(&mut live, event);
        }

        let mut hydrated_reducer = TranscriptReducer::default();
        let assistant = item_with_id(
            "legacy-message-id",
            "agent_message",
            "",
            json!({"content":"done"}),
        );
        let hydrated = hydrated_reducer.hydrate(vec![
            item(
                "tool_call",
                "completed",
                json!({"tool":"run","status":"completed","arguments":{"executable":"cargo","argc":2},"output":"ok"}),
            ),
            assistant,
        ]);

        let identity_tree = |blocks: &[TranscriptBlock]| {
            blocks
                .iter()
                .map(|block| {
                    (
                        block.id.clone(),
                        block.run_id.clone(),
                        block.kind,
                        block.assistant_phase,
                        block.tool_kind,
                        SemanticCellKind::from_block(block),
                    )
                })
                .collect::<Vec<_>>()
        };
        assert_eq!(identity_tree(&live), identity_tree(&hydrated));
    }

    #[test]
    fn hydration_projects_turn_prompt_and_terminal_summary() {
        let mut reducer = TranscriptReducer::default();
        let blocks = reducer.hydrate(vec![
            SessionItemEvent {
                kind: "turn.started".into(),
                session_id: "session-1".into(),
                turn_id: "run-1".into(),
                run_id: "run-1".into(),
                item_id: String::new(),
                source_event_id: "event-1".into(),
                timestamp: String::new(),
                details: BTreeMap::from([("prompt".into(), json!("Build Snake"))]),
                item: None,
            },
            SessionItemEvent {
                kind: "turn.completed".into(),
                session_id: "session-1".into(),
                turn_id: "run-1".into(),
                run_id: "run-1".into(),
                item_id: String::new(),
                source_event_id: "event-2".into(),
                timestamp: String::new(),
                details: BTreeMap::from([("summary".into(), json!("Snake is ready"))]),
                item: None,
            },
        ]);
        assert_eq!(blocks.len(), 2);
        assert_eq!(blocks[0].kind, BlockKind::User);
        assert_eq!(blocks[0].body, "Build Snake");
        assert!(blocks[0].branchable);
        assert_eq!(blocks[1].kind, BlockKind::Assistant);
        assert_eq!(
            blocks[1].assistant_phase,
            Some(AssistantMessagePhase::FinalAnswer)
        );
        assert_eq!(blocks[1].body, "Snake is ready");
    }

    #[test]
    fn retry_chain_projects_one_stable_user_intent() {
        let prompt = "Analyze this repo";
        let blocks = TranscriptReducer::default().hydrate(vec![
            turn_item_for_run(
                "turn.started",
                "run-root",
                "queued-root",
                json!({"prompt":prompt}),
            ),
            turn_item_for_run(
                "turn.started",
                "run-retry-1",
                "queued-retry-1",
                json!({"prompt":prompt,"retry_of_run_id":"run-root"}),
            ),
            turn_item_for_run(
                "turn.started",
                "run-retry-2",
                "queued-retry-2",
                json!({"prompt":prompt,"retry_of_run_id":"run-retry-1"}),
            ),
        ]);

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "user:run-root");
        assert_eq!(blocks[0].run_id, "run-retry-2");
        assert_eq!(blocks[0].body, prompt);
    }

    #[test]
    fn independent_identical_prompts_keep_distinct_user_blocks() {
        let prompt = "Analyze this repo";
        let blocks = TranscriptReducer::default().hydrate(
            ["run-1", "run-2", "run-3"]
                .into_iter()
                .map(|run_id| {
                    turn_item_for_run(
                        "turn.started",
                        run_id,
                        &format!("queued-{run_id}"),
                        json!({"prompt":prompt}),
                    )
                })
                .collect(),
        );

        assert_eq!(blocks.len(), 3);
        assert_eq!(
            blocks
                .iter()
                .map(|block| block.id.as_str())
                .collect::<Vec<_>>(),
            ["user:run-1", "user:run-2", "user:run-3"]
        );
        assert!(blocks.iter().all(|block| block.body == prompt));
    }

    #[test]
    fn turn_and_durable_user_item_share_run_identity() {
        let prompt = "Analyze this repo";
        let mut durable = item_with_id(
            "legacy-prompt-item",
            "user",
            "completed",
            json!({"prompt":prompt}),
        );
        durable.run_id = "run-1".into();
        durable.turn_id = "run-1".into();
        durable.item.as_mut().expect("item exists").run_id = "run-1".into();
        let blocks = TranscriptReducer::default().hydrate(vec![
            turn_item_for_run(
                "turn.started",
                "run-1",
                "queued-run-1",
                json!({"prompt":prompt}),
            ),
            durable,
        ]);

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "user:run-1");
        assert_eq!(blocks[0].body, prompt);
    }

    #[test]
    fn retry_turn_reconciles_a_durable_user_item_seen_first() {
        let prompt = "Analyze this repo";
        let mut durable = item_with_id(
            "legacy-retry-prompt",
            "user",
            "completed",
            json!({"prompt":prompt}),
        );
        durable.run_id = "run-retry".into();
        durable.turn_id = "run-retry".into();
        durable.item.as_mut().expect("item exists").run_id = "run-retry".into();
        let blocks = TranscriptReducer::default().hydrate(vec![
            turn_item_for_run(
                "turn.started",
                "run-root",
                "queued-root",
                json!({"prompt":prompt}),
            ),
            durable,
            turn_item_for_run(
                "turn.started",
                "run-retry",
                "queued-retry",
                json!({"prompt":prompt,"retry_of_run_id":"run-root"}),
            ),
        ]);

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "user:run-root");
        assert_eq!(blocks[0].run_id, "run-retry");
    }

    #[test]
    fn terminal_only_replay_projects_legacy_report_as_markdown() {
        let blocks = TranscriptReducer::default().hydrate(vec![turn_item_for_run(
            "turn.completed",
            "run-legacy",
            "event-completed",
            json!({
                "summary":"{\"summary\":\"Readable result.\",\"verification\":\"No files changed.\"}"
            }),
        )]);
        assert_eq!(blocks.len(), 1);
        assert_eq!(
            blocks[0].body,
            "Readable result.\n\n**Verification**\nNo files changed."
        );
    }

    #[test]
    fn assistant_stream_rejects_phase_drift_within_a_generation() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":1,"phase":"commentary","delta":"working"}),
            ),
        ));
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":2,"phase":"final_answer","delta":"done"}),
            ),
        ));
        assert_eq!(blocks[0].body, "working");
        assert_eq!(
            blocks[0].assistant_phase,
            Some(AssistantMessagePhase::Commentary)
        );
    }

    #[test]
    fn assistant_snapshot_seeds_empty_state_and_rejects_conflicts() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.snapshot",
                json!({
                    "generation":2,
                    "sequence":19,
                    "content":"hello",
                    "tail_revision":31,
                    "state":"open"
                }),
            ),
        ));
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].body, "hello");
        assert_eq!(blocks[0].layout_revision, 2);
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.snapshot",
                json!({
                    "generation":2,
                    "sequence":19,
                    "content":"hello",
                    "tail_revision":31,
                    "state":"open"
                }),
            ),
        ));
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.snapshot",
                json!({
                    "generation":2,
                    "sequence":19,
                    "content":"other",
                    "tail_revision":31,
                    "state":"open"
                }),
            ),
        ));
        assert_eq!(blocks[0].body, "hello");
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":2,"sequence":20,"delta":" world"}),
            ),
        ));
        assert_eq!(blocks[0].body, "hello world");
    }

    #[test]
    fn assistant_snapshot_awaiting_canonical_yields_to_model_response() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.snapshot",
                json!({
                    "generation":1,
                    "sequence":4,
                    "content":"draft",
                    "tail_revision":8,
                    "state":"awaiting_canonical"
                }),
            ),
        ));
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":5,"delta":" late"}),
            ),
        ));
        assert_eq!(blocks[0].body, "draft");
        assert!(reducer.reduce_event(
            &mut blocks,
            wire("ModelResponded", json!({"text":"canonical final"})),
        ));
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].body, "canonical final");
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":5,"delta":"resurrect"}),
            ),
        ));
        assert_eq!(blocks[0].body, "canonical final");
    }

    #[test]
    fn assistant_higher_generation_requires_reset_sequence_one() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":1,"sequence":1,"delta":"first"}),
            ),
        ));
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":2,"sequence":1,"delta":"skip reset"}),
            ),
        ));
        assert_eq!(blocks[0].body, "first");
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.reset",
                json!({"generation":2,"sequence":1}),
            ),
        ));
        assert!(blocks.iter().all(|block| block.id != "assistant:run-1"));
        assert!(reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.delta",
                json!({"generation":2,"sequence":2,"delta":"second"}),
            ),
        ));
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].body, "second");
    }

    #[test]
    fn assistant_snapshot_hides_structured_output() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(!reducer.reduce_event(
            &mut blocks,
            wire(
                "assistant.message.snapshot",
                json!({
                    "generation":1,
                    "sequence":2,
                    "content":"{\"tool\":\"read\",\"path\":\"x.rs\"}",
                    "structured_output":false,
                    "tail_revision":3,
                    "state":"open"
                }),
            ),
        ));
        assert!(blocks.is_empty());
    }

    #[test]
    fn projection_never_renders_secret_fields() {
        let details = BTreeMap::from([
            ("api_key".into(), Value::String("sk-secret".into())),
            ("message".into(), Value::String("validation failed".into())),
        ]);
        assert_eq!(summarize_details(&details), "validation failed");
    }

    #[test]
    fn execution_failures_project_typed_actions_and_stable_identity() {
        let cases = [
            (
                "ExecutionFailed",
                json!({"owner":"build","reason":"provider failed","reason_code":"provider_error","retryable":true}),
                FailureKind::Failed,
                FailureAction::Retry,
            ),
            (
                "ExecutionFailed",
                json!({"owner":"policy","reason":"approval denied","reason_code":"approval_denied","retryable":true}),
                FailureKind::ApprovalDenied,
                FailureAction::Retry,
            ),
            (
                "ExecutionCancelled",
                json!({"owner":"operator","reason":"cancelled","retryable":true}),
                FailureKind::Cancelled,
                FailureAction::RunAgain,
            ),
            (
                "ExecutionInterrupted",
                json!({"owner":"runtime","reason":"restarting","retryable":true,"recovery_disposition":"resume_checkpoint"}),
                FailureKind::Interrupted,
                FailureAction::Recovering,
            ),
        ];
        for (event_kind, payload, failure_kind, action) in cases {
            let mut reducer = TranscriptReducer::default();
            let mut blocks = Vec::new();
            assert!(reducer.reduce_event(&mut blocks, wire(event_kind, payload)));
            assert_eq!(blocks[0].id, "failure:run-1");
            let failure = blocks[0].failure.as_ref().unwrap();
            assert_eq!(failure.kind, failure_kind);
            assert_eq!(failure.action, action);
        }
    }

    #[test]
    fn retry_chain_failures_group_and_match_hydrated_projection() {
        let queued = [
            (
                "run-1",
                json!({
                    "requested_model":"provider/original",
                    "effective_model":"provider/model-1",
                    "model":"provider/model-1"
                }),
            ),
            (
                "run-2",
                json!({
                    "requested_model":"provider/current",
                    "effective_model":"provider/model-2",
                    "model":"provider/model-2",
                    "retry_of_run_id":"run-1",
                    "retry_routing":"current"
                }),
            ),
            (
                "run-3",
                json!({
                    "requested_model":"provider/current",
                    "effective_model":"provider/model-3",
                    "model":"provider/model-3",
                    "retry_of_run_id":"run-2",
                    "retry_routing":"original"
                }),
            ),
            ("run-other", json!({"effective_model":"provider/other"})),
        ];

        let mut live_reducer = TranscriptReducer::default();
        let mut live = Vec::new();
        for (index, (run_id, details)) in queued.iter().enumerate() {
            let queued_changed = live_reducer.reduce_event(
                &mut live,
                wire_for_run("ExecutionQueued", run_id, details.clone()),
            );
            assert_eq!(
                queued_changed,
                matches!(index, 1 | 2),
                "only linked retry queues update an existing failure chain"
            );
            let mut failed = wire_for_run(
                "ExecutionFailed",
                run_id,
                json!({"reason":format!("failure-{index}"),"retryable":true}),
            );
            failed.event_id = format!("failed-{index}");
            assert!(live_reducer.reduce_event(&mut live, failed));
        }

        let mut hydrated_items = Vec::new();
        for (index, (run_id, details)) in queued.iter().enumerate() {
            hydrated_items.push(turn_item_for_run(
                "turn.started",
                run_id,
                &format!("queued-{index}"),
                details.clone(),
            ));
            hydrated_items.push(turn_item_for_run(
                "turn.failed",
                run_id,
                &format!("failed-{index}"),
                json!({"error":format!("failure-{index}")}),
            ));
        }
        let hydrated = TranscriptReducer::default().hydrate(hydrated_items);

        let failure_snapshot = |blocks: &[TranscriptBlock]| {
            blocks
                .iter()
                .filter_map(|block| {
                    let failure = block.failure.clone()?;
                    Some((
                        block.id.clone(),
                        block.run_id.clone(),
                        failure,
                        block.collapsible,
                        block.expanded,
                    ))
                })
                .collect::<Vec<_>>()
        };
        assert_eq!(failure_snapshot(&live), failure_snapshot(&hydrated));
        assert_eq!(live.len(), 2, "retry chain and unrelated run stay separate");

        let chain = live
            .iter()
            .find(|block| block.id == "failure:run-1")
            .expect("retry chain failure");
        let failure = chain.failure.as_ref().unwrap();
        assert_eq!(chain.run_id, "run-3");
        assert_eq!(failure.run_id, "run-3");
        assert_eq!(failure.reason, "failure-2");
        assert_eq!(failure.model, "provider/model-3");
        assert_eq!(failure.retry_root_run_id, "run-1");
        assert_eq!(failure.attempt_count, 3);
        assert_eq!(failure.focused_action, None);
        assert!(chain.collapsible);
        assert!(!chain.expanded);

        assert_eq!(
            failure.available_actions("provider/model-3"),
            vec![
                FailureRecoveryAction::RetryCurrent,
                FailureRecoveryAction::Details,
                FailureRecoveryAction::CopyId,
            ]
        );
        assert_eq!(
            failure.available_actions("provider/current"),
            vec![
                FailureRecoveryAction::RetryCurrent,
                FailureRecoveryAction::ReplayOriginal,
                FailureRecoveryAction::Details,
                FailureRecoveryAction::CopyId,
            ]
        );
    }

    #[test]
    fn self_contained_retry_failure_replaces_root_without_queued_event() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire_for_run(
                "ExecutionFailed",
                "run-root",
                json!({
                    "reason":"first failure",
                    "retryable":true,
                    "model":"provider/original"
                }),
            ),
        ));
        let mut retry_failure = wire_for_run(
            "ExecutionFailed",
            "run-retry",
            json!({
                "reason":"credential rejected",
                "retryable":true,
                "model":"provider/current",
                "requested_model":"provider/current",
                "retry_of_run_id":"run-root"
            }),
        );
        retry_failure.event_id = "retry-terminal".into();
        assert!(reducer.reduce_event(&mut blocks, retry_failure));

        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].id, "failure:run-root");
        let failure = blocks[0].failure.as_ref().unwrap();
        assert_eq!(failure.run_id, "run-retry");
        assert_eq!(failure.model, "provider/current");
        assert_eq!(failure.attempt_count, 2);
        assert_eq!(failure.reason, "credential rejected");

        let hydrated = TranscriptReducer::default().hydrate(vec![
            turn_item_for_run(
                "turn.failed",
                "run-root",
                "root-terminal",
                json!({
                    "error":"first failure",
                    "model":"provider/original"
                }),
            ),
            turn_item_for_run(
                "turn.failed",
                "run-retry",
                "retry-terminal",
                json!({
                    "error":"credential rejected",
                    "model":"provider/current",
                    "requested_model":"provider/current",
                    "retry_of_run_id":"run-root"
                }),
            ),
        ]);
        assert_eq!(hydrated.len(), 1);
        assert_eq!(hydrated[0].id, "failure:run-root");
        assert_eq!(hydrated[0].failure.as_ref().unwrap().attempt_count, 2);
        assert_eq!(
            hydrated[0].failure.as_ref().unwrap().model,
            "provider/current"
        );
    }

    #[test]
    fn non_retryable_failure_actions_only_offer_details_and_copy_id() {
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(reducer.reduce_event(
            &mut blocks,
            wire("ExecutionFailed", json!({"reason":"failed"})),
        ));
        assert_eq!(
            blocks[0]
                .failure
                .as_ref()
                .unwrap()
                .available_actions("provider/current"),
            vec![
                FailureRecoveryAction::Details,
                FailureRecoveryAction::CopyId,
            ]
        );
    }

    #[test]
    fn typed_terminal_failures_match_live_and_hydrated_projection() {
        for (event_kind, payload) in [
            (
                "ExecutionFailed",
                json!({"owner":"build","reason":"provider failed","reason_code":"provider_error","retryable":true}),
            ),
            (
                "ExecutionCancelled",
                json!({"owner":"operator","reason":"operator_cancelled","retryable":true}),
            ),
            (
                "ExecutionInterrupted",
                json!({"owner":"runtime","reason":"restarting","retryable":true,"recovery_disposition":"resume_checkpoint"}),
            ),
        ] {
            let queued = json!({"effective_model":"provider/actual"});
            let mut live_reducer = TranscriptReducer::default();
            let mut live = Vec::new();
            assert!(!live_reducer.reduce_event(
                &mut live,
                wire_for_run("ExecutionQueued", "run-1", queued.clone()),
            ));
            let mut terminal = wire_for_run(event_kind, "run-1", payload.clone());
            terminal.event_id = "event-terminal".into();
            assert!(live_reducer.reduce_event(&mut live, terminal));

            let mut hydrated_details = payload;
            hydrated_details
                .as_object_mut()
                .unwrap()
                .insert("source_type".into(), Value::String(event_kind.to_owned()));
            let hydrated = TranscriptReducer::default().hydrate(vec![
                turn_item_for_run("turn.started", "run-1", "event-queued", queued),
                turn_item_for_run("turn.failed", "run-1", "event-terminal", hydrated_details),
            ]);

            assert_eq!(live.len(), 1, "live {event_kind}");
            assert_eq!(hydrated.len(), 1, "hydrated {event_kind}");
            assert_eq!(live[0].id, hydrated[0].id, "identity for {event_kind}");
            assert_eq!(live[0].title, hydrated[0].title, "title for {event_kind}");
            assert_eq!(live[0].body, hydrated[0].body, "reason for {event_kind}");
            assert_eq!(
                live[0].failure, hydrated[0].failure,
                "presentation for {event_kind}"
            );
        }
    }

    #[test]
    fn linked_retry_queue_disables_duplicate_recovery_live_and_after_hydration() {
        let first = json!({"effective_model":"provider/original"});
        let retry = json!({
            "effective_model":"provider/current",
            "retry_of_run_id":"run-1",
            "retry_routing":"current"
        });

        let mut live_reducer = TranscriptReducer::default();
        let mut live = Vec::new();
        assert!(!live_reducer.reduce_event(
            &mut live,
            wire_for_run("ExecutionQueued", "run-1", first.clone()),
        ));
        assert!(live_reducer.reduce_event(
            &mut live,
            wire_for_run(
                "ExecutionFailed",
                "run-1",
                json!({"reason":"first failure","retryable":true}),
            ),
        ));
        assert!(live_reducer.reduce_event(
            &mut live,
            wire_for_run("ExecutionQueued", "run-2", retry.clone()),
        ));

        let mut hydrated_reducer = TranscriptReducer::default();
        let hydrated = hydrated_reducer.hydrate(vec![
            turn_item_for_run("turn.started", "run-1", "queued-1", first),
            turn_item_for_run(
                "turn.failed",
                "run-1",
                "failed-1",
                json!({"error":"first failure"}),
            ),
            turn_item_for_run("turn.started", "run-2", "queued-2", retry),
        ]);

        for blocks in [&live, &hydrated] {
            let failure = blocks[0].failure.as_ref().unwrap();
            assert_eq!(blocks[0].id, "failure:run-1");
            assert_eq!(failure.action, FailureAction::Recovering);
            assert_eq!(failure.run_id, "run-2");
            assert_eq!(failure.model, "provider/original");
            assert_eq!(failure.attempt_count, 2);
            assert_eq!(
                failure.available_actions("provider/current"),
                vec![
                    FailureRecoveryAction::Details,
                    FailureRecoveryAction::CopyId,
                ]
            );
        }

        assert!(live_reducer.reduce_event(
            &mut live,
            wire_for_run(
                "ExecutionCompleted",
                "run-2",
                json!({"summary":"recovered"}),
            ),
        ));
        assert_eq!(
            live.iter()
                .find_map(|block| block.failure.as_ref())
                .unwrap()
                .action,
            FailureAction::Disabled
        );
    }

    #[test]
    fn malformed_failure_never_enables_retry() {
        let mut event = wire(
            "ExecutionFailed",
            json!({"reason":"unknown failure","retryable":true}),
        );
        event.run_id.clear();
        let mut reducer = TranscriptReducer::default();
        let mut blocks = Vec::new();
        assert!(!reducer.reduce_event(&mut blocks, event));
        assert!(blocks.is_empty());

        assert!(reducer.reduce_event(
            &mut blocks,
            wire("ExecutionFailed", json!({"reason":"unknown failure"})),
        ));
        assert_eq!(
            blocks[0].failure.as_ref().unwrap().action,
            FailureAction::Disabled
        );
    }

    #[test]
    fn legacy_turn_failure_uses_the_canonical_failure_identity() {
        let mut reducer = TranscriptReducer::default();
        let blocks = reducer.hydrate(vec![SessionItemEvent {
            kind: "turn.failed".into(),
            session_id: "session-1".into(),
            turn_id: "run-1".into(),
            run_id: "run-1".into(),
            item_id: String::new(),
            source_event_id: "event-legacy".into(),
            timestamp: String::new(),
            details: BTreeMap::from([("error".into(), json!("legacy failure"))]),
            item: None,
        }]);
        assert_eq!(blocks[0].id, "failure:run-1");
        let failure = blocks[0].failure.as_ref().unwrap();
        assert_eq!(failure.owner, "runtime");
        assert_eq!(failure.model, "");
        assert_eq!(failure.retry_root_run_id, "run-1");
        assert_eq!(failure.attempt_count, 1);
        assert_eq!(blocks[0].body, "legacy failure");
    }

    #[test]
    fn imported_messages_keep_external_source_labels_and_are_not_branchable() {
        let details = |role: &str, content: &str| {
            json!({
                "imported": true,
                "source": "claude-code",
                "role": role,
                "content": content,
            })
        };
        let mut reducer = TranscriptReducer::default();
        let blocks = reducer.hydrate(vec![
            item_with_id("import-user", "user", "completed", details("user", "hello")),
            item_with_id(
                "import-assistant",
                "agent_message",
                "completed",
                details("assistant", "historical answer"),
            ),
        ]);
        assert_eq!(blocks.len(), 2);
        assert_eq!(blocks[0].title, "Claude Code import");
        assert_eq!(blocks[0].kind, BlockKind::User);
        assert!(!blocks[0].branchable);
        assert_eq!(blocks[1].title, "Claude Code import");
        assert_eq!(blocks[1].kind, BlockKind::Assistant);
        assert_eq!(blocks[1].body, "historical answer");
    }
}
