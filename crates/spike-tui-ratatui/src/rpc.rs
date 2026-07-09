//! SPIKE: minimal Carina JSON-RPC 2.0 client (NDJSON over unix socket).
//!
//! This is the "first Rust JSON-RPC client" the stack-decision doc prices in
//! (§2.2, gate G9-rs). Mirrors go/rpc/client.go: blocking call with id
//! correlation on one connection, plus a second connection dedicated to the
//! `session.events.stream` subscription (notifications = method "event",
//! no id). Wire types are handled as loose `serde_json::Value` — a real
//! client would hand-write typed structs for Session/Decision/Patch/Event
//! (priced separately in the README).

use serde_json::{json, Value};
use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::UnixStream;
use std::sync::mpsc::Sender;
use std::time::Duration;

pub struct Client {
    stream: UnixStream,
    reader: BufReader<UnixStream>,
    next_id: i64,
}

#[derive(Debug)]
pub enum RpcError {
    Io(std::io::Error),
    Remote { code: i64, message: String },
    Protocol(String),
}

impl std::fmt::Display for RpcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RpcError::Io(e) => write!(f, "io: {e}"),
            RpcError::Remote { code, message } => write!(f, "rpc error {code}: {message}"),
            RpcError::Protocol(m) => write!(f, "protocol: {m}"),
        }
    }
}

impl From<std::io::Error> for RpcError {
    fn from(e: std::io::Error) -> Self {
        RpcError::Io(e)
    }
}

impl Client {
    pub fn dial(socket_path: &str) -> Result<Self, RpcError> {
        let stream = UnixStream::connect(socket_path)?;
        stream.set_read_timeout(Some(Duration::from_secs(120)))?;
        let reader = BufReader::new(stream.try_clone()?);
        Ok(Client { stream, reader, next_id: 0 })
    }

    /// Blocking request/response round trip. Interleaved server notifications
    /// (no id) are skipped — this connection is calls-only; the event stream
    /// lives on its own connection, matching go/rpc's two-connection pattern.
    pub fn call(&mut self, method: &str, params: Value) -> Result<Value, RpcError> {
        self.next_id += 1;
        let id = self.next_id;
        let req = json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params});
        let mut line = serde_json::to_string(&req).map_err(|e| RpcError::Protocol(e.to_string()))?;
        line.push('\n');
        self.stream.write_all(line.as_bytes())?;

        loop {
            let mut buf = String::new();
            let n = self.reader.read_line(&mut buf)?;
            if n == 0 {
                return Err(RpcError::Protocol("connection closed".into()));
            }
            let msg: Value = match serde_json::from_str(&buf) {
                Ok(v) => v,
                Err(e) => return Err(RpcError::Protocol(format!("bad frame: {e}"))),
            };
            match msg.get("id").and_then(Value::as_i64) {
                None => continue, // notification interleaved with the response; drop
                Some(got) if got != id => continue, // stale response
                Some(_) => {
                    if let Some(err) = msg.get("error").filter(|e| !e.is_null()) {
                        return Err(RpcError::Remote {
                            code: err.get("code").and_then(Value::as_i64).unwrap_or(0),
                            message: err
                                .get("message")
                                .and_then(Value::as_str)
                                .unwrap_or("unknown")
                                .to_string(),
                        });
                    }
                    return Ok(msg.get("result").cloned().unwrap_or(Value::Null));
                }
            }
        }
    }
}

/// Second connection: subscribe to a session's event stream and forward every
/// `event` notification onto a channel until the daemon closes the socket.
/// Runs on its own thread; returns the number of events forwarded.
pub fn stream_events(
    socket_path: &str,
    session_id: &str,
    tx: Sender<Value>,
) -> Result<usize, RpcError> {
    let mut stream = UnixStream::connect(socket_path)?;
    let sub = json!({"jsonrpc": "2.0", "id": 1, "method": "session.events.stream",
                     "params": {"session_id": session_id}});
    let mut line = serde_json::to_string(&sub).map_err(|e| RpcError::Protocol(e.to_string()))?;
    line.push('\n');
    stream.write_all(line.as_bytes())?;

    let reader = BufReader::new(stream);
    let mut forwarded = 0usize;
    for raw in reader.lines() {
        let raw = raw?;
        let msg: Value = match serde_json::from_str(&raw) {
            Ok(v) => v,
            Err(_) => continue,
        };
        // The subscribe ack has an id; notifications have method "event".
        if msg.get("id").is_some() && !msg["id"].is_null() {
            continue; // {"result":{"subscribed":true}} ack
        }
        if msg.get("method").and_then(Value::as_str) == Some("event") {
            let params = msg.get("params").cloned().unwrap_or(Value::Null);
            if tx.send(params).is_err() {
                break; // UI went away
            }
            forwarded += 1;
        }
    }
    Ok(forwarded)
}
