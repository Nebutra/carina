//! Integration tests that drive `carina-kernel-service` over stdio exactly as
//! the Go control plane does. These validate the PRD §8.4 acceptance
//! criteria at the process boundary: atomic apply, one-click rollback,
//! conflict detection, and the workspace boundary for patches.

use serde_json::{json, Value};
use std::io::{BufRead, BufReader, Write};
use std::process::{Child, ChildStdin, ChildStdout, Command, Stdio};

struct Service {
    child: Child,
    stdin: ChildStdin,
    stdout: BufReader<ChildStdout>,
    id: i64,
}

impl Service {
    fn start(state_dir: &std::path::Path) -> Self {
        let bin = env!("CARGO_BIN_EXE_carina-kernel-service");
        // The kernel delegates patch writes to carina-patch-native.
        let tools = format!("{}/../../zig/zig-out/bin", env!("CARGO_MANIFEST_DIR"));
        let mut child = Command::new(bin)
            .arg(state_dir)
            .env("CARINA_TOOLS_DIR", tools)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .expect("spawn carina-kernel-service");
        let stdin = child.stdin.take().unwrap();
        let stdout = BufReader::new(child.stdout.take().unwrap());
        Service {
            child,
            stdin,
            stdout,
            id: 0,
        }
    }

    fn call(&mut self, method: &str, params: Value) -> Value {
        self.id += 1;
        let req = json!({"jsonrpc": "2.0", "id": self.id, "method": method, "params": params});
        writeln!(self.stdin, "{req}").unwrap();
        self.stdin.flush().unwrap();

        let mut line = String::new();
        self.stdout.read_line(&mut line).unwrap();
        let resp: Value = serde_json::from_str(&line).unwrap();
        if let Some(err) = resp.get("error") {
            if !err.is_null() {
                return json!({"__error": err.clone()});
            }
        }
        resp.get("result").cloned().unwrap_or(Value::Null)
    }
}

impl Drop for Service {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn setup() -> (tempWorkspace, Service) {
    let base = std::env::temp_dir().join(format!(
        "carina-kernel-it-{}-{}",
        std::process::id(),
        rand_suffix()
    ));
    let ws = base.join("ws");
    let state = base.join("state");
    std::fs::create_dir_all(&ws).unwrap();
    std::fs::create_dir_all(&state).unwrap();
    let mut svc = Service::start(&state);
    svc.call(
        "kernel.session.init",
        json!({"session_id": "sess_it", "workspace_root": ws.to_str().unwrap(), "profile": "safe-edit"}),
    );
    (tempWorkspace { base, ws }, svc)
}

#[allow(non_camel_case_types)]
struct tempWorkspace {
    base: std::path::PathBuf,
    ws: std::path::PathBuf,
}

impl Drop for tempWorkspace {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.base);
    }
}

fn rand_suffix() -> String {
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::time::{SystemTime, UNIX_EPOCH};
    // Combine a timestamp with a process-wide counter so parallel tests
    // never collide on the same temp directory.
    static COUNTER: AtomicU64 = AtomicU64::new(0);
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let seq = COUNTER.fetch_add(1, Ordering::Relaxed);
    format!("{nanos:x}-{seq}")
}

#[test]
fn patch_apply_then_rollback_restores_preimage() {
    let (tmp, mut svc) = setup();
    let file = tmp.ws.join("a.txt");
    std::fs::write(&file, "original\n").unwrap();

    let proposed = svc.call(
        "kernel.patch.propose",
        json!({"session_id": "sess_it", "reason": "t", "files": [{"path": "a.txt", "new_content": "changed\n"}]}),
    );
    let patch_id = proposed["patch_id"].as_str().unwrap().to_string();
    let proposed_events = proposed["_audit_events"].as_array().unwrap();
    assert_eq!(proposed_events.len(), 1);
    assert_eq!(proposed_events[0]["event"]["type"], "PatchProposed");
    assert!(proposed_events[0]["cursor"].as_u64().unwrap_or_default() > 0);
    assert!(proposed_events[0]["event"]["payload"]["diff"]
        .as_str()
        .unwrap_or_default()
        .contains("-original\n+changed"));

    let applied = svc.call(
        "kernel.patch.apply",
        json!({"session_id": "sess_it", "patch_id": patch_id}),
    );
    assert_eq!(applied["status"], "applied");
    let applied_events = applied["_audit_events"].as_array().unwrap();
    assert_eq!(applied_events.len(), 1);
    assert_eq!(applied_events[0]["event"]["type"], "PatchApplied");
    assert!(
        applied_events[0]["cursor"].as_u64().unwrap()
            > proposed_events[0]["cursor"].as_u64().unwrap()
    );
    assert_eq!(std::fs::read_to_string(&file).unwrap(), "changed\n");

    let rolled = svc.call(
        "kernel.patch.rollback",
        json!({"session_id": "sess_it", "patch_id": patch_id}),
    );
    assert_eq!(rolled["status"], "rolled_back");
    let rollback_events = rolled["_audit_events"].as_array().unwrap();
    assert_eq!(rollback_events.len(), 2);
    assert_eq!(rollback_events[0]["event"]["type"], "RollbackStarted");
    assert_eq!(rollback_events[1]["event"]["type"], "RollbackCompleted");
    assert!(
        rollback_events[1]["cursor"].as_u64().unwrap()
            > rollback_events[0]["cursor"].as_u64().unwrap()
    );
    assert_eq!(std::fs::read_to_string(&file).unwrap(), "original\n");
}

#[test]
fn patch_proposed_audit_carries_reviewable_diff_and_path() {
    let (tmp, mut svc) = setup();
    let file = tmp.ws.join("review.txt");
    std::fs::write(&file, "before\n").unwrap();

    let proposed = svc.call(
        "kernel.patch.propose",
        json!({
            "session_id": "sess_it",
            "task_id": "task_review",
            "reason": "review lifecycle",
            "files": [{"path": "review.txt", "new_content": "after\n"}],
        }),
    );
    assert!(proposed.get("__error").is_none(), "{proposed}");

    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_it"}));
    let patch = events
        .as_array()
        .unwrap()
        .iter()
        .find(|event| event["type"] == "PatchProposed")
        .expect("PatchProposed audit event");
    assert_eq!(patch["task_id"], "task_review");
    assert_eq!(patch["payload"]["affected_files"], json!(["review.txt"]));
    let diff = patch["payload"]["diff"].as_str().unwrap_or_default();
    assert!(diff.contains("--- a/review.txt"), "{diff}");
    assert!(diff.contains("+++ b/review.txt"), "{diff}");
    assert!(diff.contains("-before\n+after"), "{diff}");
}

#[test]
fn concurrent_modification_is_detected_as_conflict() {
    let (tmp, mut svc) = setup();
    let file = tmp.ws.join("b.txt");
    std::fs::write(&file, "base\n").unwrap();

    let proposed = svc.call(
        "kernel.patch.propose",
        json!({"session_id": "sess_it", "reason": "t", "files": [{"path": "b.txt", "new_content": "mine\n"}]}),
    );
    let patch_id = proposed["patch_id"].as_str().unwrap().to_string();

    // Someone else edits the file after the proposal.
    std::fs::write(&file, "theirs\n").unwrap();

    let result = svc.call(
        "kernel.patch.apply",
        json!({"session_id": "sess_it", "patch_id": patch_id}),
    );
    assert!(
        result.get("__error").is_some(),
        "expected conflict error, got {result}"
    );
    // The file must be untouched by the failed apply.
    assert_eq!(std::fs::read_to_string(&file).unwrap(), "theirs\n");
}

#[test]
fn new_file_patch_rollback_deletes_the_file() {
    let (tmp, mut svc) = setup();
    let file = tmp.ws.join("created.txt");

    let proposed = svc.call(
        "kernel.patch.propose",
        json!({"session_id": "sess_it", "reason": "t", "files": [{"path": "created.txt", "new_content": "new\n"}]}),
    );
    let patch_id = proposed["patch_id"].as_str().unwrap().to_string();
    svc.call(
        "kernel.patch.apply",
        json!({"session_id": "sess_it", "patch_id": patch_id}),
    );
    assert!(file.exists());

    svc.call(
        "kernel.patch.rollback",
        json!({"session_id": "sess_it", "patch_id": patch_id}),
    );
    assert!(
        !file.exists(),
        "rollback should delete a file the patch created"
    );
}

#[test]
fn patch_outside_workspace_is_rejected() {
    let (_tmp, mut svc) = setup();
    let result = svc.call(
        "kernel.patch.propose",
        json!({"session_id": "sess_it", "reason": "t", "files": [{"path": "../escape.txt", "new_content": "x"}]}),
    );
    assert!(
        result.get("__error").is_some(),
        "expected boundary rejection, got {result}"
    );
}

#[test]
fn audit_report_counts_events_and_violations() {
    let (_tmp, mut svc) = setup();
    // A denied read produces a PolicyViolation.
    svc.call(
        "kernel.request",
        json!({"session_id": "sess_it", "capability": "FileRead", "resource": "/etc/passwd"}),
    );
    let report = svc.call("kernel.audit.report", json!({"session_id": "sess_it"}));
    assert!(report["total_events"].as_u64().unwrap() >= 1);
    assert!(!report["policy_violations"].as_array().unwrap().is_empty());
}
