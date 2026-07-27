mod types;

pub use types::*;

use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::mpsc::Sender;
use std::time::Duration;

use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::{Value, json};
use thiserror::Error;

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
}

pub struct Client {
    socket: PathBuf,
    stream: UnixStream,
    reader: BufReader<UnixStream>,
    next_id: u64,
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
        self.call(
            "runtime.initialize",
            &json!({
                "protocol_version": "1.3.0",
                "schema_version": "1.2.0",
                "projection_version": "1.0.0",
                "client_name": "carina-tui-rs",
                "client_version": env!("CARGO_PKG_VERSION"),
            }),
        )
    }

    pub fn model_inventory(&mut self) -> Result<ModelInventory, RpcError> {
        self.call("model.list", &json!({}))
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

    pub fn items(&mut self, session_id: &str) -> Result<Vec<SessionItemEvent>, RpcError> {
        self.call("session.items", &json!({"session_id": session_id}))
    }

    pub fn submit(
        &mut self,
        session_id: &str,
        prompt: &str,
        model: &str,
        client_submission_id: &str,
    ) -> Result<Task, RpcError> {
        self.call(
            "task.submit",
            &json!({
                "session_id": session_id,
                "prompt": prompt,
                "model": model,
                "client_submission_id": client_submission_id,
            }),
        )
    }

    pub fn steer(
        &mut self,
        task_id: &str,
        message: &str,
        steer_id: &str,
    ) -> Result<Value, RpcError> {
        self.call(
            "task.steer",
            &json!({"task_id": task_id, "message": message, "steer_id": steer_id}),
        )
    }

    pub fn cancel_task(&mut self, task_id: &str) -> Result<Task, RpcError> {
        self.call("task.cancel", &json!({"task_id": task_id}))
    }

    pub fn resolve_approval(
        &mut self,
        decision_id: &str,
        approve: bool,
        scope: &str,
    ) -> Result<Value, RpcError> {
        self.call(
            "task.approval.resolve",
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
            "task.user.answer",
            &json!({"question_id": question_id, "value": value}),
        )
    }

    pub fn fork_session(
        &mut self,
        session_id: &str,
        last_task_id: &str,
    ) -> Result<Session, RpcError> {
        self.call(
            "session.fork",
            &json!({"session_id": session_id, "last_task_id": last_task_id}),
        )
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

    pub fn resume_task(&mut self, task_id: &str) -> Result<Task, RpcError> {
        self.call("task.resume", &json!({"task_id": task_id}))
    }
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
        let frame: Value = match serde_json::from_str(&line) {
            Ok(frame) => frame,
            Err(error) => {
                let _ = sender.send(Err(RpcError::Protocol(error.to_string())));
                continue;
            }
        };
        if frame.get("method").and_then(Value::as_str) != Some("event") {
            continue;
        }
        let event = serde_json::from_value::<WireEvent>(
            frame.get("params").cloned().unwrap_or(Value::Null),
        )
        .map_err(|error| RpcError::Protocol(error.to_string()))?;
        if sender.send(Ok(event)).is_err() {
            return Ok(());
        }
    }
    Err(RpcError::Protocol("event stream closed".into()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn typed_inventory_decodes_without_render_layer_casts() {
        let inventory: ModelInventory = serde_json::from_value(json!({
            "default_model": "openai/gpt-5",
            "providers": [{
                "id": "openai",
                "registered": true,
                "available": true,
                "models": [{
                    "id": "openai/gpt-5",
                    "available": true,
                    "reasoning": true,
                    "image_input": true,
                    "tool_call": true
                }]
            }]
        }))
        .unwrap();
        assert_eq!(inventory.available_models()[0].id, "openai/gpt-5");
    }

    #[test]
    fn checkpoint_preview_decodes_destructive_restore_evidence() {
        let preview: CheckpointPreview = serde_json::from_value(json!({
            "checkpoint": {
                "checkpoint_id": "task_1:2",
                "parent_checkpoint_id": "task_1:1",
                "created_at": "2026-07-27T12:00:00Z",
                "sequence": "00000000000000000002",
                "task_id": "task_1",
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
        assert_eq!(preview.checkpoint.checkpoint_id, "task_1:2");
        assert_eq!(preview.rollback_patches, ["patch_2"]);
        assert_eq!(preview.will_resume, "paused");
    }
}
