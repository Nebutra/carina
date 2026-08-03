use crate::rpc::{
    AgentView, AgentViewEntry, Client, DaemonStatus, ReviewEntry, SessionReview, UsageTotals,
    WireEvent, WorkspaceDiff, WorkspaceDiffFile, WorkspacePatch,
};
use std::path::Path;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ProductStatus<'a> {
    Ready,
    Queued,
    Running,
    WaitingApproval,
    WaitingInput,
    Paused,
    Cancelled,
    Failed,
    Unknown(&'a str),
}

impl<'a> ProductStatus<'a> {
    pub fn from_wire(value: &'a str) -> Self {
        match value.trim() {
            "" | "ready" | "completed" | "complete" | "applied" | "closed" => Self::Ready,
            "queued" | "pending" | "proposed" | "requested" | "started" => Self::Queued,
            "running" | "active" | "working" | "in_progress" => Self::Running,
            "waiting_approval" | "blocked_on_approval" => Self::WaitingApproval,
            "waiting_input" | "needs_input" | "blocked_on_input" => Self::WaitingInput,
            "paused" | "degraded" => Self::Paused,
            "cancelled" | "canceled" | "denied" => Self::Cancelled,
            "failed" | "error" | "conflict" => Self::Failed,
            unknown => Self::Unknown(unknown),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AgentCategory<'a> {
    NeedsInput,
    Working,
    Completed,
    Unknown(&'a str),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ActivityKind<'a> {
    Message,
    Tool,
    Change,
    Governance,
    Execution,
    Unknown(&'a str),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ChangeKind<'a> {
    File,
    Turn,
    Unknown(&'a str),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FileChangeKind<'a> {
    Added,
    Modified,
    Deleted,
    Renamed,
    Unmerged,
    Unknown(&'a str),
}

impl AgentViewEntry {
    pub fn product_status(&self) -> ProductStatus<'_> {
        ProductStatus::from_wire(&self.status)
    }

    pub fn product_category(&self) -> AgentCategory<'_> {
        match self.category.trim() {
            "needs_input" => AgentCategory::NeedsInput,
            "working" => AgentCategory::Working,
            "completed" => AgentCategory::Completed,
            "" => match self.product_status() {
                ProductStatus::WaitingApproval | ProductStatus::WaitingInput => {
                    AgentCategory::NeedsInput
                }
                ProductStatus::Ready | ProductStatus::Cancelled | ProductStatus::Failed => {
                    AgentCategory::Completed
                }
                _ => AgentCategory::Working,
            },
            unknown => AgentCategory::Unknown(unknown),
        }
    }
}

impl WireEvent {
    pub fn product_activity_kind(&self) -> ActivityKind<'_> {
        let kind = self.kind.trim();
        if kind.contains("message") || kind.contains("thinking") {
            ActivityKind::Message
        } else if kind.contains("tool") || kind.contains("command") {
            ActivityKind::Tool
        } else if kind.contains("patch") || kind.contains("file") || kind.contains("diff") {
            ActivityKind::Change
        } else if kind.contains("permission")
            || kind.contains("approval")
            || kind.contains("question")
        {
            ActivityKind::Governance
        } else if kind.contains("execution") || kind.contains("turn") || kind.contains("thread") {
            ActivityKind::Execution
        } else {
            ActivityKind::Unknown(kind)
        }
    }

    pub fn product_status(&self) -> ProductStatus<'_> {
        let value = if self.status.is_empty() {
            self.projected_status()
        } else {
            self.status.as_str()
        };
        ProductStatus::from_wire(value)
    }
}

impl ReviewEntry {
    pub fn product_kind(&self) -> ChangeKind<'_> {
        match self.kind.trim() {
            "file_change" => ChangeKind::File,
            "turn_net_diff" => ChangeKind::Turn,
            unknown => ChangeKind::Unknown(unknown),
        }
    }

    pub fn product_status(&self) -> ProductStatus<'_> {
        ProductStatus::from_wire(&self.status)
    }
}

impl WorkspaceDiffFile {
    pub fn product_kind(&self) -> FileChangeKind<'_> {
        if self.untracked {
            return FileChangeKind::Added;
        }
        let status = self.status.trim();
        if status.contains('U') {
            FileChangeKind::Unmerged
        } else if status.contains('R') {
            FileChangeKind::Renamed
        } else if status.contains('D') {
            FileChangeKind::Deleted
        } else if status.contains('A') || status == "??" {
            FileChangeKind::Added
        } else if status.contains('M') {
            FileChangeKind::Modified
        } else {
            FileChangeKind::Unknown(status)
        }
    }
}

#[derive(Debug, Clone, Default)]
pub struct ProductProjection {
    pub runtime: DaemonStatus,
    pub usage: UsageTotals,
    pub agents: AgentView,
    pub review: SessionReview,
    pub workspace_diff: WorkspaceDiff,
    pub workspace_diff_error: Option<String>,
    pub patches: Vec<WorkspacePatch>,
    pub patches_error: Option<String>,
}

impl ProductProjection {
    pub fn load(client: &mut Client, session_id: Option<&str>) -> Result<Self, String> {
        let runtime = client.daemon_status().map_err(|error| error.to_string())?;
        let agents = client.agent_view().map_err(|error| error.to_string())?;
        let (usage, review, workspace_diff, workspace_diff_error, patches, patches_error) =
            if let Some(session_id) = session_id {
                let (workspace_diff, workspace_diff_error) = match client.workspace_diff(session_id)
                {
                    Ok(diff) => (diff, None),
                    Err(error) => (WorkspaceDiff::default(), Some(error.to_string())),
                };
                let (patches, patches_error) = match client.workspace_patches(session_id) {
                    Ok(patches) => (patches, None),
                    Err(error) => (Vec::new(), Some(error.to_string())),
                };
                (
                    client
                        .usage_cost(session_id)
                        .map_err(|error| error.to_string())?
                        .totals,
                    client
                        .session_review(session_id)
                        .map_err(|error| error.to_string())?,
                    workspace_diff,
                    workspace_diff_error,
                    patches,
                    patches_error,
                )
            } else {
                (
                    UsageTotals::default(),
                    SessionReview::default(),
                    WorkspaceDiff::default(),
                    None,
                    Vec::new(),
                    None,
                )
            };
        Ok(Self {
            runtime,
            usage,
            agents,
            review,
            workspace_diff,
            workspace_diff_error,
            patches,
            patches_error,
        })
    }

    pub fn load_from_socket(socket: &Path, session_id: Option<&str>) -> Result<Self, String> {
        let mut client = Client::connect(socket).map_err(|error| error.to_string())?;
        client.initialize().map_err(|error| error.to_string())?;
        Self::load(&mut client, session_id)
    }

    pub fn active_agents(&self) -> usize {
        self.agents.needs_input.len() + self.agents.working.len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn protocol_vocabulary_is_normalized_before_rendering() {
        assert_eq!(ProductStatus::from_wire("proposed"), ProductStatus::Queued);
        assert_eq!(
            ProductStatus::from_wire("blocked_on_approval"),
            ProductStatus::WaitingApproval
        );
        assert_eq!(
            ProductStatus::from_wire("future"),
            ProductStatus::Unknown("future")
        );

        let file = WorkspaceDiffFile {
            status: "RM".into(),
            ..WorkspaceDiffFile::default()
        };
        assert_eq!(file.product_kind(), FileChangeKind::Renamed);
    }
}
