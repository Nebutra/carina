mod types;

pub use types::*;

use std::collections::BTreeMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::Sender;
use std::time::Duration;

use base64::Engine;
use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use thiserror::Error;

const MEDIA_UPLOAD_CHUNK_BYTES: usize = 512 << 10;
const MAX_MEDIA_UPLOAD_BYTES: usize = 4 << 20;
static NEXT_MEDIA_UPLOAD_ID: AtomicU64 = AtomicU64::new(1);

#[derive(Serialize)]
struct MediaUploadParams<'a> {
    session_id: &'a str,
    upload_id: &'a str,
    chunk_index: usize,
    content_base64: String,
    #[serde(rename = "final")]
    final_chunk: bool,
    sha256: &'a str,
    total_bytes: usize,
    media_type: &'a str,
    origin: &'a str,
}

#[derive(Debug, Error)]
pub enum RpcError {
    #[error("daemon I/O: {0}")]
    Io(#[from] std::io::Error),
    #[error("daemon rejected request ({code}): {message}")]
    Remote {
        code: i64,
        message: String,
        data: Option<Value>,
    },
    #[error("invalid daemon frame: {0}")]
    Protocol(String),
    #[error("ignored malformed event frame: {0}")]
    EventFrame(String),
}

impl RpcError {
    pub fn is_ambiguous_delivery(&self) -> bool {
        matches!(self, Self::Io(_) | Self::Protocol(_))
    }
}

pub struct Client {
    socket: PathBuf,
    stream: UnixStream,
    reader: BufReader<UnixStream>,
    next_id: u64,
}

fn normalize_command_registry(mut registry: CommandRegistry) -> CommandRegistry {
    registry.commands.retain_mut(|command| {
        command.name = command.name.trim().trim_start_matches('/').to_owned();
        command.source = command.source.trim().to_owned();
        if command.name.is_empty()
            || command.name.chars().any(char::is_whitespace)
            || (!command.kind.is_empty() && command.kind != "prompt_template")
        {
            return false;
        }
        command.id = command.id.trim().to_owned();
        if command.id.is_empty() {
            command.id = format!("prompt:{}:{}", command.source, command.name);
        }
        command.kind = "prompt_template".to_owned();
        true
    });
    let mut id_counts = BTreeMap::new();
    for command in &registry.commands {
        *id_counts.entry(command.id.clone()).or_insert(0usize) += 1;
    }
    registry
        .commands
        .retain(|command| id_counts.get(command.id.as_str()) == Some(&1));
    registry.commands.sort_by(|left, right| {
        left.name
            .cmp(&right.name)
            .then_with(|| left.source.cmp(&right.source))
            .then_with(|| left.id.cmp(&right.id))
    });
    registry
}

impl Client {
    pub fn connect(path: impl AsRef<Path>) -> Result<Self, RpcError> {
        let socket = path.as_ref().to_path_buf();
        let stream = UnixStream::connect(&socket)?;
        stream.set_read_timeout(Some(Duration::from_secs(120)))?;
        let reader = BufReader::new(stream.try_clone()?);
        Ok(Self {
            socket,
            stream,
            reader,
            next_id: 0,
        })
    }

    pub fn socket(&self) -> &Path {
        &self.socket
    }

    pub fn call<P, R>(&mut self, method: &str, params: &P) -> Result<R, RpcError>
    where
        P: Serialize + ?Sized,
        R: DeserializeOwned,
    {
        self.next_id += 1;
        let id = self.next_id;
        let request = json!({
            "jsonrpc": "2.0",
            "id": id,
            "method": method,
            "params": params,
        });
        let mut encoded =
            serde_json::to_vec(&request).map_err(|error| RpcError::Protocol(error.to_string()))?;
        encoded.push(b'\n');
        self.stream.write_all(&encoded)?;
        self.stream.flush()?;

        loop {
            let mut line = String::new();
            if self.reader.read_line(&mut line)? == 0 {
                return Err(RpcError::Protocol("connection closed".into()));
            }
            let frame: Value = serde_json::from_str(&line)
                .map_err(|error| RpcError::Protocol(error.to_string()))?;
            if frame.get("id").and_then(Value::as_u64) != Some(id) {
                continue;
            }
            if let Some(error) = frame.get("error").filter(|value| !value.is_null()) {
                return Err(RpcError::Remote {
                    code: error.get("code").and_then(Value::as_i64).unwrap_or(0),
                    message: error
                        .get("message")
                        .and_then(Value::as_str)
                        .unwrap_or("unknown daemon error")
                        .to_owned(),
                    data: error.get("data").cloned(),
                });
            }
            return serde_json::from_value(frame.get("result").cloned().unwrap_or(Value::Null))
                .map_err(|error| RpcError::Protocol(error.to_string()));
        }
    }

    pub fn initialize(&mut self) -> Result<RuntimeInitialize, RpcError> {
        let initialized: RuntimeInitialize = self.call(
            "runtime.initialize",
            &json!({
                "protocol_version": "1.3.0",
                "schema_version": "1.2.0",
                "projection_version": "1.0.0",
                "client_name": "carina-tui-rs",
                "client_version": env!("CARGO_PKG_VERSION"),
            }),
        )?;
        initialized.require_methods(&[
            "execution.start",
            "model.list",
            "session.create",
            "session.events.stream",
            "session.list",
        ])?;
        Ok(initialized)
    }

    pub fn model_inventory(&mut self) -> Result<ModelInventory, RpcError> {
        self.call("model.list", &json!({}))
    }

    pub fn daemon_status(&mut self) -> Result<DaemonStatus, RpcError> {
        self.call("daemon.status", &json!({}))
    }

    pub fn usage_cost(&mut self, session_id: &str) -> Result<UsageCostReport, RpcError> {
        self.call("usage.cost", &json!({"session_id": session_id}))
    }

    pub fn context_summary(&mut self, session_id: &str) -> Result<ContextSummary, RpcError> {
        self.call("context.summary", &json!({"session_id": session_id}))
    }

    pub fn command_registry(&mut self, session_id: &str) -> Result<CommandRegistry, RpcError> {
        let registry: CommandRegistry =
            self.call("command.list", &json!({"session_id": session_id}))?;
        Ok(normalize_command_registry(registry))
    }

    pub fn agent_view(&mut self) -> Result<AgentView, RpcError> {
        self.call("agent.view", &json!({}))
    }

    pub fn doctor(&mut self) -> Result<serde_json::Value, RpcError> {
        self.call("daemon.doctor", &json!({}))
    }

    pub fn agent_recap(&mut self, task_id: &str) -> Result<AgentRecap, RpcError> {
        self.call("agent.recap", &json!({"task_id": task_id}))
    }

    pub fn agent_stop(&mut self, task_id: &str) -> Result<AgentViewEntry, RpcError> {
        self.call("agent.stop", &json!({"task_id": task_id}))
    }

    pub fn session_review(&mut self, session_id: &str) -> Result<SessionReview, RpcError> {
        self.call("session.review", &json!({"session_id": session_id}))
    }

    pub fn workspace_diff(&mut self, session_id: &str) -> Result<WorkspaceDiff, RpcError> {
        self.call("workspace.diff", &json!({"session_id": session_id}))
    }

    pub fn sessions(&mut self) -> Result<Vec<Session>, RpcError> {
        self.call("session.list", &json!({}))
    }

    pub fn create_session(&mut self, workspace_root: &str) -> Result<Session, RpcError> {
        self.call(
            "session.create",
            &json!({
                "workspace_root": workspace_root,
                "profile": "safe-edit",
                "approval_mode": "on_request",
            }),
        )
    }

    pub fn resume_session(&mut self, session_id: &str) -> Result<Session, RpcError> {
        self.call("session.resume", &json!({"session_id": session_id}))
    }

    pub fn rename_session(&mut self, session_id: &str, name: &str) -> Result<Session, RpcError> {
        let name = name.trim();
        if name.is_empty() || name.chars().count() > 80 {
            return Err(RpcError::Protocol(
                "conversation name must contain 1 to 80 characters".into(),
            ));
        }
        let session: Session = self.call(
            "session.rename",
            &json!({"session_id": session_id, "name": name}),
        )?;
        if session.session_id != session_id || session.name.trim() != name {
            return Err(RpcError::Protocol(
                "session.rename returned a mismatched session".into(),
            ));
        }
        Ok(session)
    }

    pub fn archive_session(&mut self, session_id: &str) -> Result<Session, RpcError> {
        let session: Session = self.call("session.archive", &json!({"session_id": session_id}))?;
        if session.session_id != session_id || session.status != "closed" {
            return Err(RpcError::Protocol(
                "session.archive returned a mismatched or active session".into(),
            ));
        }
        Ok(session)
    }

    pub fn unarchive_session(&mut self, session_id: &str) -> Result<Session, RpcError> {
        let session: Session =
            self.call("session.unarchive", &json!({"session_id": session_id}))?;
        if session.session_id != session_id || session.status == "closed" {
            return Err(RpcError::Protocol(
                "session.unarchive returned a mismatched or archived session".into(),
            ));
        }
        Ok(session)
    }

    pub fn set_session_model(
        &mut self,
        session_id: &str,
        model: &str,
        reasoning_effort: &str,
    ) -> Result<SessionModelSelection, RpcError> {
        let mut params = json!({"session_id": session_id, "model": model});
        if !reasoning_effort.trim().is_empty() {
            params["reasoning_effort"] = json!(reasoning_effort.trim());
        }
        let selection: SessionModelSelection = self.call("session.model.set", &params)?;
        if selection.session_id != session_id || selection.next_model.trim().is_empty() {
            return Err(RpcError::Protocol(
                "session.model.set returned a mismatched or empty selection".into(),
            ));
        }
        Ok(selection)
    }

    pub fn items(&mut self, session_id: &str) -> Result<Vec<SessionItemEvent>, RpcError> {
        self.call("session.items", &json!({"session_id": session_id}))
    }

    pub fn prompt_history(
        &mut self,
        session_id: &str,
        limit: usize,
    ) -> Result<PromptHistory, RpcError> {
        let history: PromptHistory = self.call(
            "history.recent",
            &json!({
                "limit": limit,
                "scope": "workspace",
                "session_id": session_id,
            }),
        )?;
        if history.scope != "workspace" {
            return Err(RpcError::Protocol(format!(
                "history.recent returned unexpected scope {:?}",
                history.scope
            )));
        }
        Ok(history)
    }

    pub fn workspace_files(&mut self, session_id: &str) -> Result<Vec<WorkspaceFile>, RpcError> {
        let files: Vec<WorkspaceFile> =
            self.call("workspace.tree", &json!({"session_id": session_id}))?;
        if !workspace_files_are_relative(&files) {
            return Err(RpcError::Protocol(
                "workspace.tree returned an invalid or non-relative path".into(),
            ));
        }
        Ok(files)
    }

    pub fn workspace_file(
        &mut self,
        session_id: &str,
        path: &str,
    ) -> Result<WorkspaceFileContent, RpcError> {
        if session_id.trim().is_empty() || !valid_relative_workspace_path(path) {
            return Err(RpcError::Protocol(
                "workspace.file.get requires a session and relative workspace path".into(),
            ));
        }
        let file: WorkspaceFileContent = self.call(
            "workspace.file.get",
            &json!({"session_id": session_id, "path": path}),
        )?;
        if file.content.len() > crate::file_viewer::MAX_PREVIEW_BYTES {
            return Err(RpcError::Protocol(format!(
                "file preview exceeds {} bytes",
                crate::file_viewer::MAX_PREVIEW_BYTES
            )));
        }
        Ok(file)
    }

    pub fn artifact_text(
        &mut self,
        reference: &ToolArtifactRef,
        limit: usize,
    ) -> Result<ArtifactText, RpcError> {
        let response: ArtifactReadResponse = self.call(
            "artifact.read",
            &json!({
                "session_id": reference.session_id,
                "run_id": reference.run_id,
                "call_id": reference.call_id,
                "artifact_id": reference.artifact_id,
                "offset": 0,
                "limit": limit,
            }),
        )?;
        decode_artifact_text(response)
    }

    pub fn upload_media(
        &mut self,
        session_id: &str,
        path: &Path,
        media_type: &str,
        origin: &str,
    ) -> Result<MediaRef, RpcError> {
        if session_id.trim().is_empty() {
            return Err(RpcError::Protocol(
                "media upload requires a session id".into(),
            ));
        }
        let mut bytes = Vec::new();
        std::fs::File::open(path)?
            .take((MAX_MEDIA_UPLOAD_BYTES + 1) as u64)
            .read_to_end(&mut bytes)?;
        if bytes.is_empty() || bytes.len() > MAX_MEDIA_UPLOAD_BYTES {
            return Err(RpcError::Protocol(format!(
                "media upload must contain 1..{MAX_MEDIA_UPLOAD_BYTES} bytes"
            )));
        }
        if !matches!(
            media_type,
            "image/png" | "image/jpeg" | "image/gif" | "image/webp"
        ) {
            return Err(RpcError::Protocol(format!(
                "unsupported media type {media_type:?}"
            )));
        }

        let digest = format!("{:x}", Sha256::digest(&bytes));
        let upload_id = format!(
            "tui-media-{}-{}",
            std::process::id(),
            NEXT_MEDIA_UPLOAD_ID.fetch_add(1, Ordering::Relaxed)
        );
        let total_chunks = bytes.len().div_ceil(MEDIA_UPLOAD_CHUNK_BYTES);

        for (chunk_index, chunk) in bytes.chunks(MEDIA_UPLOAD_CHUNK_BYTES).enumerate() {
            let final_chunk = chunk_index + 1 == total_chunks;
            let result: MediaUploadResult = self.call(
                "artifact.upload",
                &MediaUploadParams {
                    session_id,
                    upload_id: &upload_id,
                    chunk_index,
                    content_base64: base64::engine::general_purpose::STANDARD.encode(chunk),
                    final_chunk,
                    sha256: &digest,
                    total_bytes: bytes.len(),
                    media_type,
                    origin,
                },
            )?;

            match (final_chunk, result) {
                (false, MediaUploadResult::Pending(receipt))
                    if receipt.upload_id == upload_id
                        && receipt.next_chunk_index == chunk_index + 1 => {}
                (false, MediaUploadResult::Pending(_)) => {
                    return Err(RpcError::Protocol(
                        "artifact.upload returned a mismatched upload receipt".into(),
                    ));
                }
                (false, MediaUploadResult::Complete(_)) => {
                    return Err(RpcError::Protocol(
                        "artifact.upload completed before the final chunk".into(),
                    ));
                }
                (true, MediaUploadResult::Pending(_)) => {
                    return Err(RpcError::Protocol(
                        "artifact.upload did not complete on the final chunk".into(),
                    ));
                }
                (true, MediaUploadResult::Complete(reference)) => {
                    if reference.artifact_id != digest
                        || reference.media_type != media_type
                        || reference.bytes != bytes.len()
                        || reference.origin != origin
                    {
                        return Err(RpcError::Protocol(
                            "artifact.upload returned mismatched media metadata".into(),
                        ));
                    }
                    return Ok(reference);
                }
            }
        }

        Err(RpcError::Protocol(
            "artifact.upload produced no final media reference".into(),
        ))
    }

    #[allow(clippy::too_many_arguments)]
    pub fn submit(
        &mut self,
        session_id: &str,
        prompt: &str,
        model: &str,
        agent: &str,
        locale: &str,
        client_submission_id: &str,
        input_media_refs: &[MediaRef],
    ) -> Result<ExecutionRun, RpcError> {
        self.call(
            "execution.start",
            &json!({
                "session_id": session_id,
                "prompt": prompt,
                "model": model,
                "agent": agent,
                "locale": locale,
                "client_submission_id": client_submission_id,
                "input_media_refs": input_media_refs,
            }),
        )
    }

    pub fn set_plan_mode(&mut self, session_id: &str, on: bool) -> Result<PlanModeState, RpcError> {
        self.call(
            "session.plan_mode",
            &json!({"session_id": session_id, "on": on}),
        )
    }

    pub fn approve_plan(&mut self, session_id: &str) -> Result<PlanApprovalResult, RpcError> {
        self.call("session.approve_plan", &json!({"session_id": session_id}))
    }

    pub fn steer(
        &mut self,
        run_id: &str,
        message: &str,
        steer_id: &str,
    ) -> Result<Value, RpcError> {
        self.call(
            "execution.steer",
            &json!({"run_id": run_id, "message": message, "steer_id": steer_id}),
        )
    }

    pub fn cancel_execution(&mut self, run_id: &str) -> Result<ExecutionRun, RpcError> {
        self.call("execution.cancel", &json!({"run_id": run_id}))
    }

    pub fn resolve_approval(
        &mut self,
        decision_id: &str,
        approve: bool,
        scope: &str,
    ) -> Result<Value, RpcError> {
        self.call(
            "governance.approval.resolve",
            &json!({
                "decision_id": decision_id,
                "approve": approve,
                "approver": "operator",
                "scope": scope,
            }),
        )
    }

    pub fn answer_question(&mut self, question_id: &str, value: &str) -> Result<Value, RpcError> {
        self.call(
            "question.answer",
            &json!({"question_id": question_id, "value": value}),
        )
    }

    pub fn fork_session(
        &mut self,
        session_id: &str,
        last_run_id: Option<&str>,
        before_first: bool,
        client_fork_id: &str,
    ) -> Result<Session, RpcError> {
        if before_first {
            self.call(
                "session.fork",
                &json!({
                    "session_id": session_id,
                    "before_first": true,
                    "client_fork_id": client_fork_id,
                }),
            )
        } else {
            let last_run_id = last_run_id.ok_or_else(|| {
                RpcError::Protocol("history fork requires a previous run boundary".into())
            })?;
            self.call(
                "session.fork",
                &json!({
                    "session_id": session_id,
                    "last_run_id": last_run_id,
                    "client_fork_id": client_fork_id,
                }),
            )
        }
    }

    pub fn checkpoints(&mut self, session_id: &str) -> Result<Vec<Checkpoint>, RpcError> {
        self.call(
            "session.checkpoint.list",
            &json!({"session_id": session_id}),
        )
    }

    pub fn checkpoint_preview(
        &mut self,
        session_id: &str,
        checkpoint_id: &str,
    ) -> Result<CheckpointPreview, RpcError> {
        self.call(
            "session.checkpoint.preview",
            &json!({"session_id": session_id, "checkpoint_id": checkpoint_id}),
        )
    }

    pub fn restore_checkpoint(
        &mut self,
        session_id: &str,
        checkpoint_id: &str,
    ) -> Result<CheckpointRestoreResult, RpcError> {
        self.call(
            "session.checkpoint.restore",
            &json!({
                "session_id": session_id,
                "checkpoint_id": checkpoint_id,
                "confirmed": true,
            }),
        )
    }

    pub fn resume_execution(&mut self, run_id: &str) -> Result<ExecutionRun, RpcError> {
        self.call("execution.resume", &json!({"run_id": run_id}))
    }
}

fn decode_artifact_text(response: ArtifactReadResponse) -> Result<ArtifactText, RpcError> {
    let media_type = response.metadata.media_type.to_ascii_lowercase();
    if !(media_type.starts_with("text/")
        || media_type.starts_with("application/json")
        || media_type.starts_with("application/xml"))
    {
        return Err(RpcError::Protocol(format!(
            "artifact is not textual ({})",
            response.metadata.media_type
        )));
    }
    let bytes = base64::engine::general_purpose::STANDARD
        .decode(response.content_base64)
        .map_err(|error| RpcError::Protocol(format!("invalid artifact base64: {error}")))?;
    let content = String::from_utf8(bytes)
        .map_err(|error| RpcError::Protocol(format!("artifact is not UTF-8: {error}")))?;
    Ok(ArtifactText {
        content,
        truncated: !response.eof,
    })
}

pub fn spawn_event_stream(
    socket: PathBuf,
    session_id: String,
    since: usize,
    sender: Sender<Result<WireEvent, RpcError>>,
) {
    std::thread::spawn(move || {
        let result = stream_events(&socket, &session_id, since, &sender);
        if let Err(error) = result {
            let _ = sender.send(Err(error));
        }
    });
}

fn stream_events(
    socket: &Path,
    session_id: &str,
    since: usize,
    sender: &Sender<Result<WireEvent, RpcError>>,
) -> Result<(), RpcError> {
    let mut stream = UnixStream::connect(socket)?;
    let request = json!({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "session.events.stream",
        "params": {"session_id": session_id, "since": since, "event_mode": "canonical"},
    });
    let mut encoded =
        serde_json::to_vec(&request).map_err(|error| RpcError::Protocol(error.to_string()))?;
    encoded.push(b'\n');
    stream.write_all(&encoded)?;
    stream.flush()?;

    for line in BufReader::new(stream).lines() {
        let line = line?;
        let event = match decode_event_frame(&line) {
            Ok(event) => event,
            Err(error) => {
                let _ = sender.send(Err(error));
                continue;
            }
        };
        let Some(event) = event else {
            continue;
        };
        if sender.send(Ok(event)).is_err() {
            return Ok(());
        }
    }
    Err(RpcError::Protocol("event stream closed".into()))
}

fn decode_event_frame(line: &str) -> Result<Option<WireEvent>, RpcError> {
    let frame: Value =
        serde_json::from_str(line).map_err(|error| RpcError::EventFrame(error.to_string()))?;
    if frame.get("method").and_then(Value::as_str) != Some("event") {
        return Ok(None);
    }
    serde_json::from_value::<WireEvent>(frame.get("params").cloned().unwrap_or(Value::Null))
        .map(Some)
        .map_err(|error| RpcError::EventFrame(error.to_string()))
}

fn workspace_files_are_relative(files: &[WorkspaceFile]) -> bool {
    files
        .iter()
        .all(|file| valid_relative_workspace_path(&file.path))
}

fn valid_relative_workspace_path(path: &str) -> bool {
    let parsed = Path::new(path);
    !path.trim().is_empty()
        && path.trim() == path
        && !parsed.is_absolute()
        && parsed
            .components()
            .all(|component| matches!(component, std::path::Component::Normal(_)))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn command_registry_normalizes_legacy_entries_and_rejects_invalid_kinds() {
        let registry = normalize_command_registry(CommandRegistry {
            revision: String::new(),
            commands: vec![
                PromptCommand {
                    name: " /review ".into(),
                    source: " project ".into(),
                    ..PromptCommand::default()
                },
                PromptCommand {
                    kind: "shell".into(),
                    name: "unsafe".into(),
                    source: "project".into(),
                    ..PromptCommand::default()
                },
                PromptCommand {
                    name: "bad name".into(),
                    source: "project".into(),
                    ..PromptCommand::default()
                },
            ],
        });
        assert_eq!(registry.commands.len(), 1);
        assert_eq!(registry.commands[0].id, "prompt:project:review");
        assert_eq!(registry.commands[0].kind, "prompt_template");
        assert_eq!(registry.commands[0].name, "review");
    }

    #[test]
    fn command_registry_deduplicates_stable_ids_before_display_sorting() {
        let registry = normalize_command_registry(CommandRegistry {
            revision: "sha256:test".into(),
            commands: vec![
                PromptCommand {
                    id: "prompt:project:same".into(),
                    name: "alpha".into(),
                    source: "project".into(),
                    ..PromptCommand::default()
                },
                PromptCommand {
                    id: "prompt:project:other".into(),
                    name: "between".into(),
                    source: "project".into(),
                    ..PromptCommand::default()
                },
                PromptCommand {
                    id: "prompt:project:same".into(),
                    name: "zeta".into(),
                    source: "project".into(),
                    ..PromptCommand::default()
                },
            ],
        });
        assert_eq!(registry.commands.len(), 1);
        assert_eq!(registry.commands[0].name, "between");
    }

    #[test]
    fn only_transport_or_frame_loss_has_ambiguous_delivery() {
        assert!(
            RpcError::Io(std::io::Error::new(
                std::io::ErrorKind::BrokenPipe,
                "closed after write"
            ))
            .is_ambiguous_delivery()
        );
        assert!(RpcError::Protocol("connection closed".into()).is_ambiguous_delivery());
        assert!(
            !RpcError::Remote {
                code: -32602,
                message: "invalid model".into(),
                data: None,
            }
            .is_ambiguous_delivery()
        );
        assert!(!RpcError::EventFrame("malformed event".into()).is_ambiguous_delivery());
    }

    #[test]
    fn initialize_rejects_a_reachable_legacy_runtime_before_product_calls() {
        let nonce = NEXT_MEDIA_UPLOAD_ID.fetch_add(1, Ordering::Relaxed);
        let root = std::env::temp_dir().join(format!(
            "carina-tui-legacy-runtime-rpc-{}-{nonce}",
            std::process::id()
        ));
        std::fs::create_dir_all(&root).unwrap();
        let socket = root.join("daemon.sock");
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut line = String::new();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            let request: Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "runtime.initialize");
            writeln!(
                stream,
                "{}",
                json!({
                    "jsonrpc": "2.0",
                    "id": request["id"],
                    "result": {
                        "runtime_version": "0.6.4",
                        "protocol_version": "1.3.0",
                        "projection_version": "1.0.0",
                        "capabilities": {"rpc_methods": ["session.list"]}
                    }
                })
            )
            .unwrap();
        });

        let error = Client::connect(&socket)
            .unwrap()
            .initialize()
            .unwrap_err()
            .to_string();
        assert!(error.contains("execution.start"));
        assert!(error.contains("restart the workspace runtime"));
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn workspace_files_uses_typed_session_scoped_relative_inventory() {
        let nonce = NEXT_MEDIA_UPLOAD_ID.fetch_add(1, Ordering::Relaxed);
        let root = std::env::temp_dir().join(format!(
            "carina-tui-workspace-rpc-{}-{nonce}",
            std::process::id()
        ));
        std::fs::create_dir_all(&root).unwrap();
        let socket = root.join("daemon.sock");
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut line = String::new();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            let request: Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "workspace.tree");
            assert_eq!(request["params"], json!({"session_id": "sess-1"}));
            writeln!(
                stream,
                "{}",
                json!({
                    "jsonrpc": "2.0",
                    "id": request["id"],
                    "result": [{
                        "path": "src/app.rs",
                        "size": 42,
                        "binary": false,
                        "large": false,
                        "language": "rust",
                        "mtime": 7
                    }]
                })
            )
            .unwrap();
            stream.flush().unwrap();
        });

        let files = Client::connect(&socket)
            .unwrap()
            .workspace_files("sess-1")
            .unwrap();
        assert_eq!(files.len(), 1);
        assert_eq!(files[0].path, "src/app.rs");
        assert_eq!(files[0].language, "rust");
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn workspace_file_uses_typed_relative_session_request() {
        let nonce = NEXT_MEDIA_UPLOAD_ID.fetch_add(1, Ordering::Relaxed);
        let root = std::env::temp_dir().join(format!(
            "carina-tui-file-rpc-{}-{nonce}",
            std::process::id()
        ));
        std::fs::create_dir_all(&root).unwrap();
        let socket = root.join("daemon.sock");
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut line = String::new();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            let request: Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "workspace.file.get");
            assert_eq!(
                request["params"],
                json!({"session_id": "sess-1", "path": "src/app.rs"})
            );
            writeln!(
                stream,
                "{}",
                json!({
                    "jsonrpc": "2.0",
                    "id": request["id"],
                    "result": {"content": "fn main() {}\n", "hash": "abc123"}
                })
            )
            .unwrap();
        });
        let file = Client::connect(&socket)
            .unwrap()
            .workspace_file("sess-1", "src/app.rs")
            .unwrap();
        assert_eq!(file.content, "fn main() {}\n");
        assert_eq!(file.hash, "abc123");
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn workspace_inventory_rejects_absolute_and_parent_paths() {
        let file = |path: &str| WorkspaceFile {
            path: path.into(),
            ..Default::default()
        };
        assert!(workspace_files_are_relative(&[file("src/app.rs")]));
        assert!(!workspace_files_are_relative(&[file("/tmp/secret")]));
        assert!(!workspace_files_are_relative(&[file("src/../secret")]));
        assert!(!workspace_files_are_relative(&[file("")]));
    }

    #[test]
    fn media_upload_chunks_content_and_submit_forwards_refs() {
        let nonce = NEXT_MEDIA_UPLOAD_ID.fetch_add(1, Ordering::Relaxed);
        let root = std::env::temp_dir().join(format!(
            "carina-tui-media-rpc-{}-{nonce}",
            std::process::id()
        ));
        std::fs::create_dir_all(&root).unwrap();
        let socket = root.join("daemon.sock");
        let path = root.join("image.png");
        let mut content = b"\x89PNG\r\n\x1a\n".to_vec();
        content.resize(MEDIA_UPLOAD_CHUNK_BYTES + 17, 0x5a);
        std::fs::write(&path, &content).unwrap();
        let digest = format!("{:x}", Sha256::digest(&content));
        let expected_content = content.clone();
        let expected_digest = digest.clone();
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();

        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut uploaded = Vec::new();
            let mut upload_id = String::new();

            for chunk_index in 0..2 {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let request: Value = serde_json::from_str(&line).unwrap();
                assert_eq!(request["method"], "artifact.upload");
                let params = &request["params"];
                assert_eq!(params["session_id"], "sess-1");
                assert_eq!(params["chunk_index"], chunk_index);
                assert_eq!(params["sha256"], expected_digest);
                assert_eq!(params["total_bytes"], expected_content.len());
                assert_eq!(params["media_type"], "image/png");
                assert_eq!(params["origin"], "clipboard");
                assert_eq!(params["final"], chunk_index == 1);
                assert!(params.get("final_chunk").is_none());
                let current_upload_id = params["upload_id"].as_str().unwrap();
                if chunk_index == 0 {
                    upload_id = current_upload_id.to_owned();
                } else {
                    assert_eq!(current_upload_id, upload_id);
                }
                uploaded.extend(
                    base64::engine::general_purpose::STANDARD
                        .decode(params["content_base64"].as_str().unwrap())
                        .unwrap(),
                );

                let result = if chunk_index == 0 {
                    json!({"upload_id": upload_id, "next_chunk_index": 1})
                } else {
                    json!({
                        "artifact_id": expected_digest,
                        "media_type": "image/png",
                        "bytes": expected_content.len(),
                        "origin": "clipboard"
                    })
                };
                writeln!(
                    stream,
                    "{}",
                    json!({"jsonrpc": "2.0", "id": request["id"], "result": result})
                )
                .unwrap();
                stream.flush().unwrap();
            }
            assert_eq!(uploaded, expected_content);

            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            let request: Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "execution.start");
            assert_eq!(
                request["params"]["input_media_refs"]
                    .as_array()
                    .unwrap()
                    .len(),
                1
            );
            assert_eq!(
                request["params"]["input_media_refs"][0]["artifact_id"],
                expected_digest
            );
            writeln!(
                stream,
                "{}",
                json!({
                    "jsonrpc": "2.0",
                    "id": request["id"],
                    "result": {"run_id": "run-1", "session_id": "sess-1"}
                })
            )
            .unwrap();
            stream.flush().unwrap();
        });

        let mut client = Client::connect(&socket).unwrap();
        let reference = client
            .upload_media("sess-1", &path, "image/png", "clipboard")
            .unwrap();
        assert_eq!(reference.artifact_id, digest);
        assert_eq!(reference.bytes, content.len());
        let execution = client
            .submit(
                "sess-1",
                "inspect this image",
                "provider/model",
                "build",
                "en",
                "submit-1",
                std::slice::from_ref(&reference),
            )
            .unwrap();
        assert_eq!(execution.run_id, "run-1");

        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    fn artifact_response(
        media_type: &str,
        content_base64: &str,
        eof: bool,
    ) -> ArtifactReadResponse {
        ArtifactReadResponse {
            metadata: ArtifactMetadata {
                media_type: media_type.into(),
            },
            eof,
            content_base64: content_base64.into(),
            ..Default::default()
        }
    }

    #[test]
    fn textual_artifact_decodes_and_preserves_truncation() {
        let artifact = decode_artifact_text(artifact_response(
            "application/json; charset=utf-8",
            "eyJvayI6dHJ1ZX0=",
            false,
        ))
        .unwrap();
        assert_eq!(artifact.content, r#"{"ok":true}"#);
        assert!(artifact.truncated);
    }

    #[test]
    fn artifact_decoder_rejects_binary_invalid_base64_and_invalid_utf8() {
        assert!(matches!(
            decode_artifact_text(artifact_response("image/png", "AA==", true)),
            Err(RpcError::Protocol(message)) if message.contains("not textual")
        ));
        assert!(matches!(
            decode_artifact_text(artifact_response("text/plain", "%%%", true)),
            Err(RpcError::Protocol(message)) if message.contains("base64")
        ));
        assert!(matches!(
            decode_artifact_text(artifact_response("text/plain", "/w==", true)),
            Err(RpcError::Protocol(message)) if message.contains("UTF-8")
        ));
    }

    #[test]
    fn malformed_event_isolated_without_losing_the_next_frame() {
        assert!(matches!(
            decode_event_frame("{bad"),
            Err(RpcError::EventFrame(_))
        ));
        let event = decode_event_frame(
            r#"{"jsonrpc":"2.0","method":"event","params":{"type":"user.question","options":null,"future_field":{"nested":true}}}"#,
        )
        .unwrap()
        .unwrap();
        assert_eq!(event.kind, "user.question");
        assert!(event.options.is_empty());
    }

    #[test]
    fn typed_inventory_decodes_without_render_layer_casts() {
        let inventory: ModelInventory = serde_json::from_value(json!({
            "default_model": "openai/gpt-5",
            "reasoner": {"backend": "model-router", "available": true},
            "providers": [{
                "id": "openai",
                "registered": true,
                "available": true,
                "models": [{
                    "id": "openai/gpt-5",
                    "available": true,
                    "reasoning": true,
                    "reasoning_efforts": ["low", "medium", "high"],
                    "default_reasoning_effort": "high",
                    "image_input": true,
                    "tool_call": true
                }]
            }]
        }))
        .unwrap();
        assert_eq!(inventory.available_models()[0].id, "openai/gpt-5");
        assert_eq!(
            inventory.available_models()[0].default_reasoning_effort,
            "high"
        );
    }

    #[test]
    fn session_decodes_the_saved_reasoning_preference() {
        let session: Session = serde_json::from_value(json!({
            "session_id": "sess_1",
            "workspace_root": "/tmp/carina",
            "status": "active",
            "next_model": "openai/gpt-5",
            "next_reasoning_effort": "high"
        }))
        .unwrap();
        assert_eq!(session.next_reasoning_effort, "high");
    }

    #[test]
    fn typed_inventory_decodes_safe_provider_discovery_metadata() {
        let inventory: ModelInventory = serde_json::from_value(json!({
            "providers": [{
                "id": "ccswitch-safe",
                "name": "Relay",
                "registered": true,
                "available": false,
                "source_kind": "cc-switch",
                "source_label": "CC Switch",
                "source_app": "codex",
                "source_route": "managed_proxy",
                "source_auth_mode": "bearer_token",
                "source_action": "use_active_route",
                "source_current": true,
                "source_importable": true,
                "models": []
            }]
        }))
        .unwrap();
        let provider = &inventory.providers[0];
        assert_eq!(provider.source_kind, "cc-switch");
        assert_eq!(provider.source_label, "CC Switch");
        assert_eq!(provider.source_app, "codex");
        assert_eq!(provider.source_route, "managed_proxy");
        assert_eq!(provider.source_auth_mode, "bearer_token");
        assert_eq!(provider.source_action, "use_active_route");
        assert!(provider.source_current);
        assert!(provider.source_importable);
        assert!(!provider.available);
    }

    #[test]
    fn checkpoint_preview_decodes_destructive_restore_evidence() {
        let preview: CheckpointPreview = serde_json::from_value(json!({
            "checkpoint": {
                "checkpoint_id": "run_1:2",
                "parent_checkpoint_id": "run_1:1",
                "created_at": "2026-07-27T12:00:00Z",
                "sequence": "00000000000000000002",
                "run_id": "run_1",
                "session_id": "sess_1",
                "turn": 2,
                "summary": "before refactor",
                "applied_patches": ["patch_1"]
            },
            "conversation_turns": 2,
            "summary": "before refactor",
            "rollback_patches": ["patch_2"],
            "will_resume": "paused"
        }))
        .unwrap();
        assert_eq!(preview.checkpoint.checkpoint_id, "run_1:2");
        assert_eq!(preview.rollback_patches, ["patch_2"]);
        assert_eq!(preview.will_resume, "paused");
    }
}
