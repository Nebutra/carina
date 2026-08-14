use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::sync::mpsc::{self, Receiver};
use std::time::Instant;

use serde_json::{Value, json};

use super::{
    AssistantMessageSnapshot, CatchUpDelivery, EventStreamFrame, ReceivedEvent, ReplayBoundaryV1,
    RpcError, WireEvent, decode_event_frame,
};

#[derive(Debug, Clone)]
pub struct ReplayTailAttachRequest {
    pub session_id: String,
    pub since: usize,
    pub runtime_id: String,
    pub runtime_epoch: String,
    pub runtime_process_epoch: i64,
}

pub struct EventStreamAttached {
    pub boundary: ReplayBoundaryV1,
    pub catch_up: Vec<ReceivedEvent>,
    pub live: Receiver<Result<ReceivedEvent, RpcError>>,
}

impl std::fmt::Debug for EventStreamAttached {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("EventStreamAttached")
            .field("boundary", &self.boundary)
            .field("catch_up", &self.catch_up)
            .field("live", &"<receiver>")
            .finish()
    }
}

impl EventStreamAttached {
    pub fn delivery_at(&self, index: usize) -> Option<CatchUpDelivery> {
        self.boundary.classify(index)
    }
}

pub fn attach_replay_tail_v1(
    socket: &Path,
    request: &ReplayTailAttachRequest,
) -> Result<EventStreamAttached, RpcError> {
    let stream = UnixStream::connect(socket)?;
    let mut writer = stream.try_clone()?;
    let encoded = subscribe_request(request)?;
    writer.write_all(&encoded)?;
    writer.flush()?;
    // The v1 reader owns this socket. Drop the write half so no later RPC can
    // be multiplexed onto the exclusive replay-tail connection.
    drop(writer);

    let mut reader = BufReader::new(stream);
    let (catch_up, boundary) = read_v1_handshake(&mut reader, request)?;
    let (sender, live) = mpsc::channel();
    std::thread::spawn(move || drain_live_events(reader, sender));
    Ok(EventStreamAttached {
        boundary,
        catch_up,
        live,
    })
}

fn subscribe_request(request: &ReplayTailAttachRequest) -> Result<Vec<u8>, RpcError> {
    let mut encoded = serde_json::to_vec(&json!({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "session.events.stream",
        "params": {
            "session_id": request.session_id,
            "since": request.since,
            "event_mode": "canonical",
            "replay_tail_version": 1,
        },
    }))
    .map_err(|error| RpcError::Protocol(error.to_string()))?;
    encoded.push(b'\n');
    Ok(encoded)
}

fn read_v1_handshake(
    reader: &mut BufReader<UnixStream>,
    request: &ReplayTailAttachRequest,
) -> Result<(Vec<ReceivedEvent>, ReplayBoundaryV1), RpcError> {
    let mut catch_up = Vec::new();
    loop {
        let mut line = String::new();
        let read = reader.read_line(&mut line).map_err(RpcError::from)?;
        if read == 0 {
            return Err(RpcError::Protocol(
                "event stream closed before replay_boundary".into(),
            ));
        }
        match decode_event_frame(line.trim_end()) {
            Ok(EventStreamFrame::Event(event)) => {
                catch_up.push(*event);
            }
            Ok(EventStreamFrame::Subscribed { replayed, boundary }) => {
                let Some(boundary) = boundary else {
                    return Err(RpcError::Protocol(
                        "replay_tail_version=1 missing replay_boundary".into(),
                    ));
                };
                let classified = classify_v1_catch_up(request, &boundary, catch_up, replayed)?;
                return Ok((classified, boundary));
            }
            Ok(EventStreamFrame::Ignored) => {}
            Err(error) => {
                return Err(RpcError::Protocol(format!(
                    "rejecting replay-tail attachment: {error}"
                )));
            }
        }
    }
}

fn classify_v1_catch_up(
    request: &ReplayTailAttachRequest,
    boundary: &ReplayBoundaryV1,
    events: Vec<WireEvent>,
    replayed: usize,
) -> Result<Vec<ReceivedEvent>, RpcError> {
    boundary.validate().map_err(RpcError::Protocol)?;
    if boundary.session_id != request.session_id
        || boundary.runtime_id != request.runtime_id
        || boundary.runtime_epoch != request.runtime_epoch
        || boundary.runtime_process_epoch != request.runtime_process_epoch
        || boundary.requested_since != i64::try_from(request.since).unwrap_or(-1)
    {
        return Err(RpcError::Protocol(
            "replay_boundary identity does not match the attach request".into(),
        ));
    }
    let expected = boundary
        .catch_up_len()
        .ok_or_else(|| RpcError::Protocol("replay_boundary counts overflow".into()))?;
    if events.len() != expected {
        return Err(RpcError::Protocol(
            "replay attachment counts do not match replay_boundary".into(),
        ));
    }
    if replayed != boundary.as_replayed() {
        return Err(RpcError::Protocol(
            "legacy replayed count does not match durable_replayed".into(),
        ));
    }
    boundary
        .validate_attachment(
            boundary.as_count(boundary.transient_snapshots),
            boundary.as_count(boundary.durable_replayed),
            boundary.as_count(boundary.buffered_live),
        )
        .map_err(RpcError::Protocol)?;

    let received_at = Instant::now();
    let mut classified = Vec::with_capacity(events.len());
    for (index, event) in events.into_iter().enumerate() {
        let delivery = boundary.classify(index).ok_or_else(|| {
            RpcError::Protocol("catch-up index is outside replay_boundary".into())
        })?;
        match delivery {
            CatchUpDelivery::TransientSnapshot => {
                let snapshot = snapshot_from_event(&event)?;
                snapshot.validate(boundary).map_err(RpcError::Protocol)?;
            }
            CatchUpDelivery::DurableReplay | CatchUpDelivery::BufferedLive => {
                if event.kind == "assistant.message.snapshot" {
                    return Err(RpcError::Protocol(
                        "assistant.message.snapshot is outside the negotiated snapshot region"
                            .into(),
                    ));
                }
            }
        }
        classified.push(ReceivedEvent {
            event,
            received_at,
            replayed: matches!(
                delivery,
                CatchUpDelivery::DurableReplay | CatchUpDelivery::TransientSnapshot
            ),
            delivery: Some(delivery),
        });
    }
    Ok(classified)
}

fn snapshot_from_event(event: &WireEvent) -> Result<AssistantMessageSnapshot, RpcError> {
    if event.kind != "assistant.message.snapshot" {
        return Err(RpcError::Protocol(
            "snapshot region contains a non-snapshot event".into(),
        ));
    }
    Ok(AssistantMessageSnapshot {
        r#type: event.kind.clone(),
        session_id: event.session_id.clone(),
        run_id: event.run_id.clone(),
        actor: event.actor.clone(),
        payload: super::AssistantMessageSnapshotPayload {
            generation: event.payload_i64("generation"),
            sequence: event.payload_i64("sequence"),
            phase: event.payload_string("phase"),
            content: event.payload_string("content"),
            structured_output: event
                .payload
                .get("structured_output")
                .and_then(Value::as_bool)
                .unwrap_or(false),
            tail_revision: event.payload_i64("tail_revision"),
            state: event.payload_string("state"),
        },
    })
}

fn drain_live_events(
    reader: BufReader<UnixStream>,
    sender: mpsc::Sender<Result<ReceivedEvent, RpcError>>,
) {
    for line in reader.lines() {
        let line = match line {
            Ok(line) => line,
            Err(error) => {
                let _ = sender.send(Err(error.into()));
                return;
            }
        };
        match decode_event_frame(&line) {
            Ok(EventStreamFrame::Event(event)) => {
                if event.kind == "assistant.message.snapshot" {
                    let _ = sender.send(Err(RpcError::Protocol(
                        "live assistant.message.snapshot is outside the negotiated snapshot region"
                            .into(),
                    )));
                    return;
                }
                if sender
                    .send(Ok(ReceivedEvent {
                        event: *event,
                        received_at: Instant::now(),
                        replayed: false,
                        delivery: None,
                    }))
                    .is_err()
                {
                    return;
                }
            }
            Ok(EventStreamFrame::Subscribed { .. }) => {
                let _ = sender.send(Err(RpcError::Protocol(
                    "second subscription response on an exclusive replay-tail socket".into(),
                )));
                return;
            }
            Ok(EventStreamFrame::Ignored) => {}
            Err(error) => {
                if sender.send(Err(error)).is_err() {
                    return;
                }
            }
        }
    }
    let _ = sender.send(Err(RpcError::Protocol("event stream closed".into())));
}

impl WireEvent {
    fn payload_i64(&self, key: &str) -> i64 {
        self.payload
            .get(key)
            .and_then(Value::as_i64)
            .or_else(|| {
                self.payload
                    .get(key)
                    .and_then(Value::as_u64)
                    .map(|value| value as i64)
            })
            .unwrap_or(0)
    }

    fn payload_string(&self, key: &str) -> String {
        self.payload
            .get(key)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    }
}

impl ReplayBoundaryV1 {
    fn as_replayed(&self) -> usize {
        self.as_count(self.durable_replayed)
    }

    fn as_count(&self, value: i64) -> usize {
        usize::try_from(value).unwrap_or(0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rpc::{CatchUpDelivery, EventStreamFrame, RpcError, decode_event_frame};
    use serde_json::json;
    use std::io::{BufRead, BufReader, Write};
    use std::os::unix::net::UnixListener;
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::time::Duration;

    static NEXT_SOCKET: AtomicU64 = AtomicU64::new(1);

    fn temp_socket() -> (std::path::PathBuf, std::path::PathBuf) {
        let nonce = NEXT_SOCKET.fetch_add(1, Ordering::Relaxed);
        let root = std::env::temp_dir().join(format!(
            "carina-tui-replay-tail-{}-{}",
            std::process::id(),
            nonce
        ));
        std::fs::create_dir_all(&root).unwrap();
        let socket = root.join("daemon.sock");
        (root, socket)
    }

    fn attach_request() -> ReplayTailAttachRequest {
        ReplayTailAttachRequest {
            session_id: "sess".into(),
            since: 4,
            runtime_id: "rt".into(),
            runtime_epoch: "ep".into(),
            runtime_process_epoch: 3,
        }
    }

    fn boundary_json() -> Value {
        json!({
            "version": 1,
            "session_id": "sess",
            "runtime_id": "rt",
            "runtime_epoch": "ep",
            "runtime_process_epoch": 3,
            "requested_since": 4,
            "durable_cursor": 7,
            "durable_replayed": 1,
            "transient_tail_revision": 8,
            "transient_snapshots": 1,
            "buffered_live": 1
        })
    }

    fn write_line(stream: &mut impl Write, value: Value) {
        writeln!(stream, "{value}").unwrap();
        stream.flush().unwrap();
    }

    #[test]
    fn v1_attach_classifies_three_regions_and_owns_a_dedicated_socket() {
        let (root, socket) = temp_socket();
        let listener = UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut line = String::new();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            let request: Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "session.events.stream");
            assert_eq!(request["params"]["replay_tail_version"], 1);
            assert_eq!(request["params"]["event_mode"], "canonical");
            write_line(
                &mut stream,
                json!({"jsonrpc":"2.0","method":"event","params":{"type":"ToolCallStarted","run_id":"run","raw_cursor":5}}),
            );
            write_line(
                &mut stream,
                json!({
                    "jsonrpc":"2.0","method":"event","params":{
                        "type":"assistant.message.snapshot",
                        "session_id":"sess",
                        "run_id":"run",
                        "payload":{
                            "generation":2,"sequence":19,"phase":"final_answer",
                            "content":"hello","tail_revision":8,"state":"open"
                        }
                    }
                }),
            );
            write_line(
                &mut stream,
                json!({"jsonrpc":"2.0","method":"event","params":{"type":"assistant.message.delta","run_id":"run","payload":{"generation":2,"sequence":20,"phase":"final_answer","delta":"!"}}}),
            );
            write_line(
                &mut stream,
                json!({
                    "jsonrpc":"2.0","id":1,"result":{
                        "subscription_id":"sub_1",
                        "cursor":7,
                        "replayed":1,
                        "event_mode":"canonical",
                        "replay_boundary": boundary_json()
                    }
                }),
            );
            write_line(
                &mut stream,
                json!({"jsonrpc":"2.0","method":"event","params":{"type":"ToolCallCompleted","run_id":"run","raw_cursor":8}}),
            );
            stream
                .set_read_timeout(Some(Duration::from_millis(80)))
                .unwrap();
            let mut extra = String::new();
            let second = BufReader::new(stream).read_line(&mut extra);
            assert!(
                second.is_err() || extra.is_empty(),
                "v1 client multiplexed: {extra}"
            );
        });

        let attached = attach_replay_tail_v1(&socket, &attach_request()).unwrap();
        assert_eq!(attached.catch_up.len(), 3);
        assert_eq!(
            attached.delivery_at(0),
            Some(CatchUpDelivery::DurableReplay)
        );
        assert_eq!(
            attached.delivery_at(1),
            Some(CatchUpDelivery::TransientSnapshot)
        );
        assert_eq!(attached.delivery_at(2), Some(CatchUpDelivery::BufferedLive));
        assert!(attached.catch_up[0].replayed);
        assert!(attached.catch_up[1].replayed);
        assert!(!attached.catch_up[2].replayed);
        assert_eq!(attached.catch_up[1].feedback_milestone(), None);
        assert_eq!(attached.catch_up[0].durable_raw_cursor(), Some(5));
        assert_eq!(attached.catch_up[1].durable_raw_cursor(), None);
        let live = attached
            .live
            .recv_timeout(Duration::from_secs(1))
            .unwrap()
            .unwrap();
        assert_eq!(live.event.kind, "ToolCallCompleted");
        drop(attached.live);
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn v1_attach_rejects_multiplex_error_without_partial_catch_up() {
        let (root, socket) = temp_socket();
        let listener = UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut line = String::new();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            write_line(
                &mut stream,
                json!({"jsonrpc":"2.0","method":"event","params":{"type":"ToolCallStarted","run_id":"run"}}),
            );
            write_line(
                &mut stream,
                json!({
                    "jsonrpc":"2.0","id":1,"error":{
                        "code":-32600,
                        "message":"replay_tail_version=1 must be the first request on a fresh connection"
                    }
                }),
            );
        });

        let error = attach_replay_tail_v1(&socket, &attach_request()).unwrap_err();
        assert!(
            matches!(error, RpcError::Protocol(ref message) if message.contains("first request")),
            "{error}"
        );
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn v1_attach_rejects_snapshot_outside_its_region() {
        let (root, socket) = temp_socket();
        let listener = UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut line = String::new();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            write_line(
                &mut stream,
                json!({
                    "jsonrpc":"2.0","method":"event","params":{
                        "type":"assistant.message.snapshot",
                        "session_id":"sess",
                        "run_id":"run",
                        "payload":{
                            "generation":1,"sequence":1,"phase":"final_answer",
                            "content":"x","tail_revision":8,"state":"open"
                        }
                    }
                }),
            );
            write_line(
                &mut stream,
                json!({"jsonrpc":"2.0","method":"event","params":{"type":"ToolCallStarted","run_id":"run"}}),
            );
            write_line(
                &mut stream,
                json!({"jsonrpc":"2.0","method":"event","params":{"type":"ToolCallCompleted","run_id":"run"}}),
            );
            write_line(
                &mut stream,
                json!({
                    "jsonrpc":"2.0","id":1,"result":{
                        "replayed":1,
                        "replay_boundary": boundary_json()
                    }
                }),
            );
        });

        let error = attach_replay_tail_v1(&socket, &attach_request()).unwrap_err();
        assert!(
            matches!(error, RpcError::Protocol(ref message) if message.contains("snapshot")),
            "{error}"
        );
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn v1_attach_rejects_malformed_prefix_instead_of_partial_hydrate() {
        let (root, socket) = temp_socket();
        let listener = UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut line = String::new();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            writeln!(stream, "{{bad").unwrap();
            stream.flush().unwrap();
        });

        let error = attach_replay_tail_v1(&socket, &attach_request()).unwrap_err();
        assert!(matches!(error, RpcError::Protocol(_)), "{error}");
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn legacy_ack_fixture_still_decodes_without_a_boundary() {
        let frame = decode_event_frame(
            r#"{"jsonrpc":"2.0","id":1,"result":{"replayed":3,"cursor":9,"event_mode":"canonical"}}"#,
        )
        .unwrap();
        match frame {
            EventStreamFrame::Subscribed { replayed, boundary } => {
                assert_eq!(replayed, 3);
                assert!(boundary.is_none());
            }
            other => panic!("unexpected frame: {other:?}"),
        }
    }
}
