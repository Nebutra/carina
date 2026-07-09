//! Integration tests for the governed code-intelligence layer
//! (docs/plans/code-intelligence.md): kernel.index.* driven over stdio exactly
//! as the Go control plane does. The load-bearing invariant under test is that
//! the index is a derived artifact of files the session may read — a
//! FileRead-denied path can never leak content into search results.

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
        Service { child, stdin, stdout, id: 0 }
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
    static COUNTER: AtomicU64 = AtomicU64::new(0);
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let seq = COUNTER.fetch_add(1, Ordering::Relaxed);
    format!("{nanos:x}-{seq}")
}

fn setup_with(init_extra: Value) -> (tempWorkspace, Service) {
    let base = std::env::temp_dir().join(format!("carina-index-it-{}-{}", std::process::id(), rand_suffix()));
    let ws = base.join("ws");
    let state = base.join("state");
    std::fs::create_dir_all(&ws).unwrap();
    std::fs::create_dir_all(&state).unwrap();
    let mut svc = Service::start(&state);
    let mut params = json!({"session_id": "sess_ix", "workspace_root": ws.to_str().unwrap(), "profile": "safe-edit"});
    if let Some(extra) = init_extra.as_object() {
        for (k, v) in extra {
            params[k] = v.clone();
        }
    }
    svc.call("kernel.session.init", params);
    (tempWorkspace { base, ws }, svc)
}

fn setup() -> (tempWorkspace, Service) {
    setup_with(json!({}))
}

#[test]
fn build_then_search_finds_a_known_symbol() {
    let (tmp, mut svc) = setup();
    std::fs::write(
        tmp.ws.join("lib.rs"),
        "pub fn zz_indexed_marker() {}\n\npub fn caller() {\n    zz_indexed_marker();\n}\n",
    )
    .unwrap();

    let built = svc.call(
        "kernel.index.build",
        json!({"session_id": "sess_ix", "paths": ["lib.rs"]}),
    );
    assert_eq!(built["indexed"].as_u64(), Some(1), "got {built}");
    assert!(built["symbols"].as_u64().unwrap() >= 2);
    assert!(built["db_path"].as_str().unwrap().ends_with(".sqlite"));

    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_indexed_marker"}),
    );
    let hits = found["hits"].as_array().expect("hits array");
    assert!(!hits.is_empty(), "expected a hit, got {found}");
    assert_eq!(hits[0]["path"], "lib.rs");
    assert!(hits[0]["snippet"].as_str().unwrap().contains("zz_indexed_marker"));

    // Symbol lookup sees the definition with tree-sitter confidence.
    let syms = svc.call(
        "kernel.index.symbols",
        json!({"session_id": "sess_ix", "name": "zz_indexed_marker"}),
    );
    assert_eq!(syms["confidence"], "tree-sitter");
    assert_eq!(syms["definitions"].as_array().unwrap().len(), 1);
    assert!(!syms["references"].as_array().unwrap().is_empty());

    // Repo map renders within a budget and mentions the file.
    let map = svc.call(
        "kernel.index.map",
        json!({"session_id": "sess_ix", "token_budget": 512}),
    );
    assert!(map["map"].as_str().unwrap().contains("lib.rs"));
    assert!(map["token_estimate"].as_u64().unwrap() <= 512);
}

#[test]
fn denied_paths_are_skipped_and_never_enter_the_index() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("ok.rs"), "pub fn zz_visible_fn() {}\n").unwrap();
    // .env is sensitive: FileRead denies it even inside the workspace.
    std::fs::write(tmp.ws.join(".env"), "SECRET_ZZ_TOKEN=zz_forbidden_value\n").unwrap();
    // A workspace escape is denied by the boundary check.
    let outside = tmp.base.join("outside.rs");
    std::fs::write(&outside, "pub fn zz_outside_fn() {}\n").unwrap();

    let built = svc.call(
        "kernel.index.build",
        json!({"session_id": "sess_ix", "paths": ["ok.rs", ".env", "../outside.rs"]}),
    );
    assert_eq!(built["indexed"].as_u64(), Some(1), "got {built}");
    let skipped = built["skipped"].as_array().unwrap();
    assert_eq!(skipped.len(), 2, "got {built}");
    let skipped_paths: Vec<&str> = skipped.iter().filter_map(|s| s["path"].as_str()).collect();
    assert!(skipped_paths.contains(&".env"), "got {skipped_paths:?}");

    // Governance: denied content is unreachable through every query surface.
    for query in ["SECRET_ZZ_TOKEN", "zz_forbidden_value", "zz_outside_fn"] {
        let found = svc.call(
            "kernel.index.search",
            json!({"session_id": "sess_ix", "query": query}),
        );
        assert!(
            found["hits"].as_array().unwrap().is_empty(),
            "denied content leaked for {query}: {found}"
        );
    }

    // The denials themselves are audited as PolicyViolation events.
    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_ix"}));
    let events = events.as_array().unwrap();
    assert!(
        events.iter().any(|e| e["type"] == "PolicyViolation"
            && e["payload"]["resource"].as_str().unwrap_or_default().ends_with(".env")),
        "expected a PolicyViolation for .env"
    );
}

#[test]
fn audit_chain_records_index_decisions_and_status_events() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("a.rs"), "pub fn zz_audited_fn() {}\n").unwrap();

    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["a.rs"]}));
    svc.call("kernel.index.search", json!({"session_id": "sess_ix", "query": "zz_audited_fn"}));
    svc.call("kernel.index.symbols", json!({"session_id": "sess_ix", "name": "zz_audited_fn"}));
    svc.call("kernel.index.map", json!({"session_id": "sess_ix", "token_budget": 256}));

    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_ix"}));
    let events = events.as_array().unwrap();
    let resources: Vec<String> = events
        .iter()
        .filter(|e| e["type"] == "ToolApproved")
        .filter_map(|e| e["payload"]["resource"].as_str().map(String::from))
        .collect();
    assert!(resources.iter().any(|r| r == "index build files=1"), "got {resources:?}");
    assert!(resources.iter().any(|r| r == "index search zz_audited_fn"), "got {resources:?}");
    assert!(resources.iter().any(|r| r == "index symbols zz_audited_fn"), "got {resources:?}");
    assert!(resources.iter().any(|r| r == "index map budget=256"), "got {resources:?}");

    // Every index decision carries a permission_decision_id in the chain.
    assert!(events
        .iter()
        .filter(|e| e["payload"]["resource"].as_str().unwrap_or_default().starts_with("index "))
        .all(|e| e["permission_decision_id"].is_string()));

    // Build completion recorded a decision-linked status event (no new EventType).
    let status = events
        .iter()
        .find(|e| e["payload"]["status"] == "index_build_completed")
        .expect("index_build_completed status event");
    assert_eq!(status["type"], "ToolApproved");
    assert!(status["permission_decision_id"].is_string());
    assert!(status["payload"]["duration_ms"].is_number());
    assert_eq!(status["payload"]["indexed"].as_u64(), Some(1));

    // The chain still verifies end-to-end.
    let verify = svc.call("kernel.audit.verify", json!({"session_id": "sess_ix"}));
    assert_eq!(verify["ok"], true, "got {verify}");
}

#[test]
fn patch_apply_then_index_update_reflects_the_edit() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("m.rs"), "pub fn zz_before_edit() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["m.rs"]}));

    let proposed = svc.call(
        "kernel.patch.propose",
        json!({"session_id": "sess_ix", "reason": "t", "files": [{"path": "m.rs", "new_content": "pub fn zz_after_edit() {}\n"}]}),
    );
    let patch_id = proposed["patch_id"].as_str().unwrap().to_string();
    let applied = svc.call("kernel.patch.apply", json!({"session_id": "sess_ix", "patch_id": patch_id}));
    assert_eq!(applied["status"], "applied");

    let updated = svc.call(
        "kernel.index.update",
        json!({"session_id": "sess_ix", "changed_paths": ["m.rs"]}),
    );
    assert!(updated.get("__error").is_none(), "got {updated}");

    let stale = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_before_edit"}),
    );
    assert!(stale["hits"].as_array().unwrap().is_empty(), "stale content must be gone: {stale}");
    let fresh = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_after_edit"}),
    );
    assert!(!fresh["hits"].as_array().unwrap().is_empty(), "got {fresh}");

    // Rollback + kernel-side invalidation hook: the pre-image is queryable again.
    let rolled = svc.call("kernel.patch.rollback", json!({"session_id": "sess_ix", "patch_id": proposed["patch_id"].as_str().unwrap()}));
    assert_eq!(rolled["status"], "rolled_back");
    let restored = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_before_edit"}),
    );
    assert!(
        !restored["hits"].as_array().unwrap().is_empty(),
        "rollback hook should restore the pre-image in the index: {restored}"
    );
}

#[test]
fn update_treats_missing_changed_paths_as_deletes() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("gone.rs"), "pub fn zz_soon_gone() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["gone.rs"]}));
    std::fs::remove_file(tmp.ws.join("gone.rs")).unwrap();

    let updated = svc.call(
        "kernel.index.update",
        json!({"session_id": "sess_ix", "changed_paths": ["gone.rs"]}),
    );
    assert!(updated.get("__error").is_none(), "got {updated}");
    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_soon_gone"}),
    );
    assert!(found["hits"].as_array().unwrap().is_empty(), "got {found}");
}

#[test]
fn org_bundle_can_deny_the_code_index_capability() {
    let (tmp, mut svc) = setup_with(json!({
        "bundle_toml": "name = \"locked\"\ndeny_capabilities = [\"CodeIndex\"]\n"
    }));
    std::fs::write(tmp.ws.join("x.rs"), "pub fn zz_locked_fn() {}\n").unwrap();

    let built = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["x.rs"]}));
    let err = built.get("__error").expect("build must be denied");
    assert!(err["message"].as_str().unwrap().contains("code index denied"), "got {err}");

    let found = svc.call("kernel.index.search", json!({"session_id": "sess_ix", "query": "zz_locked_fn"}));
    let err = found.get("__error").expect("search must be denied");
    assert!(err["message"].as_str().unwrap().contains("code index denied"), "got {err}");

    // Both denials are in the audit chain.
    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_ix"}));
    let denials = events
        .as_array()
        .unwrap()
        .iter()
        .filter(|e| e["type"] == "PolicyViolation"
            && e["payload"]["capability"] == "CodeIndex")
        .count();
    assert!(denials >= 2, "expected audited CodeIndex denials");
}

#[test]
fn bundle_file_read_deny_blocks_queries_over_a_previously_built_index() {
    // The index DB is keyed by workspace root and outlives sessions: a later,
    // stricter session must not read content through it. A bundle that denies
    // FileRead (but never mentions CodeIndex) has to deny index queries too.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("x.rs"), "pub fn zz_prebuilt_fn() {}\n").unwrap();
    let built = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["x.rs"]}));
    assert_eq!(built["indexed"].as_u64(), Some(1), "got {built}");

    svc.call(
        "kernel.session.init",
        json!({
            "session_id": "sess_strict",
            "workspace_root": tmp.ws.to_str().unwrap(),
            "profile": "safe-edit",
            "bundle_toml": "name = \"locked\"\ndeny_capabilities = [\"FileRead\"]\n",
        }),
    );
    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_strict", "query": "zz_prebuilt_fn"}),
    );
    let err = found.get("__error").expect("stricter session must be denied");
    assert!(
        err["message"].as_str().unwrap().contains("code index denied"),
        "got {err}"
    );
}

#[test]
fn update_drops_now_denied_paths_instead_of_keeping_stale_content() {
    // kernel.index.update must treat "this path is no longer FileRead-allowed"
    // as a drop (like the kernel-internal patch hook), never as a keep.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("cfg.rs"), "pub fn zz_stale_secret() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["cfg.rs"]}));

    // The path becomes FileRead-denied: it now resolves outside the workspace.
    std::fs::remove_file(tmp.ws.join("cfg.rs")).unwrap();
    #[cfg(unix)]
    std::os::unix::fs::symlink("/etc/hosts", tmp.ws.join("cfg.rs")).unwrap();

    let updated = svc.call(
        "kernel.index.update",
        json!({"session_id": "sess_ix", "changed_paths": ["cfg.rs"]}),
    );
    assert!(updated.get("__error").is_none(), "got {updated}");

    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_stale_secret"}),
    );
    assert!(
        found["hits"].as_array().unwrap().is_empty(),
        "stale content of a now-denied path must be dropped: {found}"
    );
}

#[test]
fn build_skips_oversized_files() {
    // The kernel service is the governance chokepoint: one huge file must not
    // be slurped whole into its memory (or stored twice in chunks + FTS).
    let (tmp, mut svc) = setup();
    let mut big = String::from("pub fn zz_huge_fn() {}\n");
    big.push_str(&"// filler line to inflate the file well past the cap\n".repeat(120_000));
    assert!(big.len() > 5 * 1024 * 1024);
    std::fs::write(tmp.ws.join("huge.rs"), &big).unwrap();
    std::fs::write(tmp.ws.join("ok.rs"), "pub fn zz_small_fn() {}\n").unwrap();

    let built = svc.call(
        "kernel.index.build",
        json!({"session_id": "sess_ix", "paths": ["huge.rs", "ok.rs"]}),
    );
    assert_eq!(built["indexed"].as_u64(), Some(1), "got {built}");
    let skipped = built["skipped"].as_array().unwrap();
    assert!(
        skipped.iter().any(|s| s["path"] == "huge.rs"
            && s["reason"].as_str().unwrap_or_default().contains("size cap")),
        "oversized file must be skipped with a reason: {built}"
    );
    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_huge_fn"}),
    );
    assert!(found["hits"].as_array().unwrap().is_empty(), "got {found}");
}

#[test]
fn code_index_approval_is_actionable_and_retryable() {
    // A requires_approval verdict must carry its decision_id so the control
    // plane can kernel.approve it and retry — and retries must not grow the
    // pending set unboundedly (the same decision_id is reused).
    let (tmp, mut svc) = setup_with(json!({
        "bundle_toml": "name = \"strict\"\nrequire_approval = [\"CodeIndex\"]\n"
    }));
    std::fs::write(tmp.ws.join("y.rs"), "pub fn zz_approved_fn() {}\n").unwrap();

    let extract_decision_id = |v: &Value| -> String {
        let msg = v["__error"]["message"].as_str().expect("error message").to_string();
        assert!(msg.contains("requires approval"), "got {msg}");
        let start = msg.find("decision_id=").expect("decision_id in message") + "decision_id=".len();
        msg[start..].split(')').next().unwrap().to_string()
    };

    let first = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["y.rs"]}));
    let decision_id = extract_decision_id(&first);
    // A retry before approval reuses the parked decision instead of leaking a new one.
    let second = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["y.rs"]}));
    assert_eq!(extract_decision_id(&second), decision_id);

    let approved = svc.call(
        "kernel.approve",
        json!({"session_id": "sess_ix", "decision_id": decision_id, "for_session": true}),
    );
    assert_eq!(approved["decision"], "allowed", "got {approved}");

    // The session overlay now satisfies every kernel.index.* call.
    let built = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["y.rs"]}));
    assert_eq!(built["indexed"].as_u64(), Some(1), "got {built}");
    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_approved_fn"}),
    );
    assert!(!found["hits"].as_array().unwrap().is_empty(), "got {found}");
}

#[test]
fn search_hits_carry_provenance() {
    // Callers must be able to detect stale results: every hit reports the
    // content_hash / indexed_at of the file version it was indexed from.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("p.rs"), "pub fn zz_prov_marker() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["p.rs"]}));
    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_prov_marker"}),
    );
    let hit = &found["hits"].as_array().unwrap()[0];
    assert_eq!(hit["content_hash"].as_str().unwrap().len(), 64, "got {hit}");
    assert!(!hit["indexed_at"].as_str().unwrap().is_empty(), "got {hit}");
}

// ---- V2: embeddings (vector channel) + deferred-minor regressions ---------

/// Standard base64 (with padding), dependency-free — mirror of the service's
/// decoder, used to ship f32-LE vectors over the RPC surface.
fn base64_encode(input: &[u8]) -> String {
    const TABLE: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for chunk in input.chunks(3) {
        let b = [chunk[0], *chunk.get(1).unwrap_or(&0), *chunk.get(2).unwrap_or(&0)];
        let n = ((b[0] as u32) << 16) | ((b[1] as u32) << 8) | b[2] as u32;
        out.push(TABLE[(n >> 18) as usize & 63] as char);
        out.push(TABLE[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 { TABLE[(n >> 6) as usize & 63] as char } else { '=' });
        out.push(if chunk.len() > 2 { TABLE[n as usize & 63] as char } else { '=' });
    }
    out
}

fn vector_base64(values: &[f32]) -> String {
    let bytes: Vec<u8> = values.iter().flat_map(|v| v.to_le_bytes()).collect();
    base64_encode(&bytes)
}

const EMBED_MODEL: &str = "test/unit-embed";

/// Fetches every pending chunk and stores `vector` for each; returns the
/// embed_store result (callers assert on stored/stale/total_pending).
fn embed_all_pending(svc: &mut Service, session_id: &str, vector: &[f32]) -> Value {
    let pending = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": session_id, "model_id": EMBED_MODEL, "limit": 256}),
    );
    assert!(pending.get("__error").is_none(), "pending_chunks failed: {pending}");
    let chunks = pending["chunks"].as_array().expect("chunks array").clone();
    assert!(!chunks.is_empty(), "expected pending chunks, got {pending}");
    let embeddings: Vec<Value> = chunks
        .iter()
        .map(|c| {
            json!({
                "chunk_id": c["chunk_id"],
                "content_hash": c["content_hash"],
                "vector_base64": vector_base64(vector),
            })
        })
        .collect();
    svc.call(
        "kernel.index.embed_store",
        json!({
            "session_id": session_id,
            "model_id": EMBED_MODEL,
            "dims": vector.len(),
            "embeddings": embeddings,
        }),
    )
}

#[test]
fn pending_chunks_then_embed_store_then_vector_search_round_trip() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("lib.rs"), "pub fn zz_vec_round_trip() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["lib.rs"]}));

    // pending_chunks returns full chunk text + provenance for the caller to embed.
    let pending = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": "sess_ix", "model_id": EMBED_MODEL}),
    );
    assert!(pending.get("__error").is_none(), "got {pending}");
    let chunks = pending["chunks"].as_array().expect("chunks array");
    assert!(!chunks.is_empty(), "got {pending}");
    assert!(pending["total_pending"].as_u64().unwrap() >= chunks.len() as u64);
    let first = &chunks[0];
    assert!(first["chunk_id"].is_number(), "got {first}");
    assert_eq!(first["path"], "lib.rs");
    assert!(first["content"].as_str().unwrap().contains("zz_vec_round_trip"));
    assert_eq!(first["content_hash"].as_str().unwrap().len(), 64);

    let stored = embed_all_pending(&mut svc, "sess_ix", &[1.0, 0.0]);
    assert!(stored.get("__error").is_none(), "got {stored}");
    assert!(stored["stored"].as_u64().unwrap() >= 1, "got {stored}");
    assert_eq!(stored["stale"].as_array().unwrap().len(), 0, "got {stored}");
    assert_eq!(stored["total_pending"].as_u64(), Some(0), "got {stored}");

    // Vector-augmented search: the cosine channel contributes a "vector" source
    // and hits keep V1 provenance.
    let found = svc.call(
        "kernel.index.search",
        json!({
            "session_id": "sess_ix",
            "query": "zz_vec_round_trip",
            "query_vector_base64": vector_base64(&[1.0, 0.0]),
            "model_id": EMBED_MODEL,
        }),
    );
    assert!(found.get("__error").is_none(), "got {found}");
    let hits = found["hits"].as_array().expect("hits array");
    assert!(!hits.is_empty(), "got {found}");
    let hit = &hits[0];
    assert!(
        hit["sources"].as_array().unwrap().iter().any(|s| s == "vector"),
        "vector channel must contribute a source: {hit}"
    );
    assert_eq!(hit["content_hash"].as_str().unwrap().len(), 64, "got {hit}");
    assert!(!hit["indexed_at"].as_str().unwrap().is_empty(), "got {hit}");

    // The embed completion is a decision-linked status event (V1 idiom).
    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_ix"}));
    let status = events
        .as_array()
        .unwrap()
        .iter()
        .find(|e| e["payload"]["status"] == "index_embed_completed")
        .expect("index_embed_completed status event");
    assert_eq!(status["type"], "ToolApproved");
    assert!(status["permission_decision_id"].is_string());
    assert!(status["payload"]["duration_ms"].is_number());
}

#[test]
fn vector_search_reports_channel_liveness_for_observable_degrade() {
    // V3 §B: "semantic:on" must mean the cosine channel actually had live,
    // rankable vectors — the search result carries the counters so the
    // daemon can tell an on channel from a silently dead one (empty store,
    // or a dims change the daemon has no in-process memory of).
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("lib.rs"), "pub fn zz_vec_liveness() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["lib.rs"]}));

    // Keyword-only search: no vector channel, no counters.
    let keyword = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_vec_liveness"}),
    );
    assert!(keyword.get("__error").is_none(), "got {keyword}");
    assert!(
        keyword.get("vector_channel").is_none(),
        "keyword-only search has no vector channel: {keyword}"
    );

    // Vector search over an EMPTY embeddings store: stored == live == 0.
    let empty = svc.call(
        "kernel.index.search",
        json!({
            "session_id": "sess_ix",
            "query": "zz_vec_liveness",
            "query_vector_base64": vector_base64(&[1.0, 0.0]),
            "model_id": EMBED_MODEL,
        }),
    );
    assert!(empty.get("__error").is_none(), "got {empty}");
    assert_eq!(empty["vector_channel"]["stored"].as_u64(), Some(0), "got {empty}");
    assert_eq!(empty["vector_channel"]["live"].as_u64(), Some(0), "got {empty}");

    let stored = embed_all_pending(&mut svc, "sess_ix", &[1.0, 0.0]);
    assert!(stored["stored"].as_u64().unwrap() >= 1, "got {stored}");

    // Matching dims: every stored vector is live.
    let live = svc.call(
        "kernel.index.search",
        json!({
            "session_id": "sess_ix",
            "query": "zz_vec_liveness",
            "query_vector_base64": vector_base64(&[1.0, 0.0]),
            "model_id": EMBED_MODEL,
        }),
    );
    assert!(live.get("__error").is_none(), "got {live}");
    assert!(live["vector_channel"]["stored"].as_u64().unwrap() >= 1, "got {live}");
    assert_eq!(
        live["vector_channel"]["live"], live["vector_channel"]["stored"],
        "matching dims must rank every stored vector: {live}"
    );

    // A 3-dim query against 2-dim vectors: stored > 0, live == 0 — the
    // observable dims-mismatch signal (e.g. after a daemon restart).
    let mismatched = svc.call(
        "kernel.index.search",
        json!({
            "session_id": "sess_ix",
            "query": "zz_vec_liveness",
            "query_vector_base64": vector_base64(&[1.0, 0.0, 0.0]),
            "model_id": EMBED_MODEL,
        }),
    );
    assert!(mismatched.get("__error").is_none(), "got {mismatched}");
    assert!(
        mismatched["vector_channel"]["stored"].as_u64().unwrap() >= 1,
        "got {mismatched}"
    );
    assert_eq!(
        mismatched["vector_channel"]["live"].as_u64(),
        Some(0),
        "dims-mismatched vectors are not live: {mismatched}"
    );
}

#[test]
fn bundle_file_read_deny_blocks_pending_chunks_over_a_previously_built_index() {
    // pending_chunks returns raw chunk text — content-equivalent data. The
    // analog of the V1 checkpoint test: a stricter session whose bundle denies
    // FileRead (never mentioning CodeIndex) must be denied at query time.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("x.rs"), "pub fn zz_pending_locked_fn() {}\n").unwrap();
    let built = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["x.rs"]}));
    assert_eq!(built["indexed"].as_u64(), Some(1), "got {built}");

    svc.call(
        "kernel.session.init",
        json!({
            "session_id": "sess_strict",
            "workspace_root": tmp.ws.to_str().unwrap(),
            "profile": "safe-edit",
            "bundle_toml": "name = \"locked\"\ndeny_capabilities = [\"FileRead\"]\n",
        }),
    );
    let pending = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": "sess_strict", "model_id": EMBED_MODEL}),
    );
    let err = pending.get("__error").expect("stricter session must be denied");
    assert!(
        err["message"].as_str().unwrap().contains("code index denied"),
        "got {err}"
    );
}

#[test]
fn bundle_file_read_deny_blocks_vector_search_and_embed_store() {
    // Vectors are derived from content: matching on them (search) and writing
    // them (embed_store names chunk ids of readable content) are both gated.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("x.rs"), "pub fn zz_vector_locked_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["x.rs"]}));
    let stored = embed_all_pending(&mut svc, "sess_ix", &[1.0, 0.0]);
    assert!(stored.get("__error").is_none(), "embed in permissive session: {stored}");

    svc.call(
        "kernel.session.init",
        json!({
            "session_id": "sess_strict",
            "workspace_root": tmp.ws.to_str().unwrap(),
            "profile": "safe-edit",
            "bundle_toml": "name = \"locked\"\ndeny_capabilities = [\"FileRead\"]\n",
        }),
    );
    let found = svc.call(
        "kernel.index.search",
        json!({
            "session_id": "sess_strict",
            "query": "zz_vector_locked_fn",
            "query_vector_base64": vector_base64(&[1.0, 0.0]),
            "model_id": EMBED_MODEL,
        }),
    );
    let err = found.get("__error").expect("vector search must be denied");
    assert!(
        err["message"].as_str().unwrap().contains("code index denied"),
        "got {err}"
    );
    let store_denied = svc.call(
        "kernel.index.embed_store",
        json!({
            "session_id": "sess_strict",
            "model_id": EMBED_MODEL,
            "dims": 2,
            "embeddings": [{"chunk_id": 1, "content_hash": "00", "vector_base64": vector_base64(&[1.0, 0.0])}],
        }),
    );
    let err = store_denied.get("__error").expect("embed_store must be denied");
    assert!(
        err["message"].as_str().unwrap().contains("code index denied"),
        "got {err}"
    );
}

#[test]
fn vector_search_cannot_surface_pre_patch_content_and_embed_store_reports_stale() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("m.rs"), "pub fn zz_vec_before_edit() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["m.rs"]}));

    // Remember the pre-patch chunk so we can try to embed it after the edit.
    let pending = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": "sess_ix", "model_id": EMBED_MODEL}),
    );
    assert!(pending.get("__error").is_none(), "got {pending}");
    let old_chunk = pending["chunks"].as_array().unwrap()[0].clone();

    let stored = embed_all_pending(&mut svc, "sess_ix", &[1.0, 0.0]);
    assert!(stored.get("__error").is_none(), "got {stored}");

    let proposed = svc.call(
        "kernel.patch.propose",
        json!({"session_id": "sess_ix", "reason": "t", "files": [{"path": "m.rs", "new_content": "pub fn zz_vec_after_edit() {}\n"}]}),
    );
    let patch_id = proposed["patch_id"].as_str().unwrap().to_string();
    let applied = svc.call("kernel.patch.apply", json!({"session_id": "sess_ix", "patch_id": patch_id}));
    assert_eq!(applied["status"], "applied", "got {applied}");

    // The stale-vector governance test: pre-patch content is unreachable
    // through the vector channel even though nobody re-embedded.
    let stale_search = svc.call(
        "kernel.index.search",
        json!({
            "session_id": "sess_ix",
            "query": "zz_vec_before_edit",
            "query_vector_base64": vector_base64(&[1.0, 0.0]),
            "model_id": EMBED_MODEL,
        }),
    );
    assert!(
        stale_search["hits"].as_array().unwrap().is_empty(),
        "a stale vector must never surface pre-patch content: {stale_search}"
    );

    // Storing the old chunk's vector now reports it stale (never an error).
    let restored = svc.call(
        "kernel.index.embed_store",
        json!({
            "session_id": "sess_ix",
            "model_id": EMBED_MODEL,
            "dims": 2,
            "embeddings": [{
                "chunk_id": old_chunk["chunk_id"],
                "content_hash": old_chunk["content_hash"],
                "vector_base64": vector_base64(&[1.0, 0.0]),
            }],
        }),
    );
    assert!(restored.get("__error").is_none(), "got {restored}");
    assert_eq!(restored["stored"].as_u64(), Some(0), "got {restored}");
    assert!(
        restored["stale"].as_array().unwrap().contains(&old_chunk["chunk_id"]),
        "replaced chunk must be reported stale: {restored}"
    );

    // Re-embedding the pending (new) chunks makes the new content reachable.
    let resynced = embed_all_pending(&mut svc, "sess_ix", &[1.0, 0.0]);
    assert!(resynced.get("__error").is_none(), "got {resynced}");
    let fresh = svc.call(
        "kernel.index.search",
        json!({
            "session_id": "sess_ix",
            "query": "zz_vec_after_edit",
            "query_vector_base64": vector_base64(&[1.0, 0.0]),
            "model_id": EMBED_MODEL,
        }),
    );
    assert!(!fresh["hits"].as_array().unwrap().is_empty(), "got {fresh}");
}

#[test]
fn embed_store_validates_dims_and_base64() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("v.rs"), "pub fn zz_validate_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["v.rs"]}));

    // Vector payload shorter than dims * 4 bytes.
    let short = svc.call(
        "kernel.index.embed_store",
        json!({
            "session_id": "sess_ix", "model_id": EMBED_MODEL, "dims": 2,
            "embeddings": [{"chunk_id": 1, "content_hash": "00", "vector_base64": vector_base64(&[1.0])}],
        }),
    );
    let err = short.get("__error").expect("dims mismatch must error");
    assert!(err["message"].as_str().unwrap().contains("dims"), "got {err}");

    // Invalid base64.
    let bad = svc.call(
        "kernel.index.embed_store",
        json!({
            "session_id": "sess_ix", "model_id": EMBED_MODEL, "dims": 2,
            "embeddings": [{"chunk_id": 1, "content_hash": "00", "vector_base64": "!!not-base64!!"}],
        }),
    );
    let err = bad.get("__error").expect("invalid base64 must error");
    assert!(err["message"].as_str().unwrap().contains("base64"), "got {err}");

    // dims out of the accepted 1..=4096 range.
    for dims in [0, 5000] {
        let out_of_range = svc.call(
            "kernel.index.embed_store",
            json!({
                "session_id": "sess_ix", "model_id": EMBED_MODEL, "dims": dims,
                "embeddings": [],
            }),
        );
        let err = out_of_range.get("__error").unwrap_or(&Value::Null);
        assert!(
            err["message"].as_str().unwrap_or_default().contains("dims"),
            "dims={dims} must be rejected, got {out_of_range}"
        );
    }
}

#[test]
fn search_with_query_vector_requires_model_id() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("q.rs"), "pub fn zz_needs_model_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["q.rs"]}));
    let found = svc.call(
        "kernel.index.search",
        json!({
            "session_id": "sess_ix",
            "query": "zz_needs_model_fn",
            "query_vector_base64": vector_base64(&[1.0, 0.0]),
        }),
    );
    let err = found.get("__error").expect("query_vector without model_id must error");
    assert!(err["message"].as_str().unwrap().contains("model_id"), "got {err}");
}

#[test]
fn pending_chunks_before_build_reports_missing_index() {
    let (_tmp, mut svc) = setup();
    let pending = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": "sess_ix", "model_id": EMBED_MODEL}),
    );
    let err = pending.get("__error").expect("expected error");
    assert!(
        err["message"].as_str().unwrap().contains("index not built"),
        "got {err}"
    );
}

// ---- V1 deferred minors (D1/D2), fixed in V2 -------------------------------

#[cfg(unix)]
#[test]
fn update_keeps_rows_and_reports_skipped_for_unreadable_changed_file() {
    // D1: a read failure on a still-present, still-FileRead-allowed path is
    // NOT a deletion. Only NotFound means delete; permission errors keep the
    // previously ingested rows and surface as a skipped entry.
    use std::os::unix::fs::PermissionsExt;
    let (tmp, mut svc) = setup();
    let path = tmp.ws.join("locked.rs");
    std::fs::write(&path, "pub fn zz_unreadable_kept_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["locked.rs"]}));

    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o000)).unwrap();
    let updated = svc.call(
        "kernel.index.update",
        json!({"session_id": "sess_ix", "changed_paths": ["locked.rs"]}),
    );
    // Restore permissions before asserting so the tempdir always cleans up.
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
    assert!(updated.get("__error").is_none(), "got {updated}");
    let skipped = updated["skipped"].as_array().unwrap();
    assert!(
        skipped.iter().any(|s| s["path"] == "locked.rs"
            && s["reason"].as_str().unwrap_or_default().contains("read error")),
        "read failure must be a skipped entry, got {updated}"
    );

    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_unreadable_kept_fn"}),
    );
    assert!(
        !found["hits"].as_array().unwrap().is_empty(),
        "rows of an allowed-but-unreadable path must be kept: {found}"
    );
}

#[test]
fn update_keeps_rows_and_reports_skipped_for_non_utf8_changed_file() {
    // D1: InvalidData (non-UTF-8) is a read failure, not a deletion.
    let (tmp, mut svc) = setup();
    let path = tmp.ws.join("bin.rs");
    std::fs::write(&path, "pub fn zz_non_utf8_kept_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["bin.rs"]}));

    std::fs::write(&path, [0xff, 0xfe, 0x00, 0x9f, 0x92, 0x96]).unwrap();
    let updated = svc.call(
        "kernel.index.update",
        json!({"session_id": "sess_ix", "changed_paths": ["bin.rs"]}),
    );
    assert!(updated.get("__error").is_none(), "got {updated}");
    let skipped = updated["skipped"].as_array().unwrap();
    assert!(
        skipped.iter().any(|s| s["path"] == "bin.rs"
            && s["reason"].as_str().unwrap_or_default().contains("read error")),
        "non-UTF-8 read failure must be a skipped entry, got {updated}"
    );

    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_non_utf8_kept_fn"}),
    );
    assert!(
        !found["hits"].as_array().unwrap().is_empty(),
        "rows must be kept on a non-UTF-8 read failure: {found}"
    );
}

#[test]
fn index_params_reject_non_string_array_elements() {
    // D2: str_array_param must reject [1, "a.rs"] instead of silently
    // operating on a different path set than the caller named.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("a.rs"), "pub fn zz_d2_fn() {}\n").unwrap();
    let built = svc.call(
        "kernel.index.build",
        json!({"session_id": "sess_ix", "paths": [1, "a.rs"]}),
    );
    let err = built.get("__error").expect("mixed-type paths array must error");
    assert!(
        err["message"].as_str().unwrap().contains("must be an array of strings"),
        "got {err}"
    );
}

#[test]
fn search_before_build_reports_missing_index() {
    let (_tmp, mut svc) = setup();
    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "anything"}),
    );
    let err = found.get("__error").expect("expected error");
    assert!(
        err["message"].as_str().unwrap().contains("index not built"),
        "got {err}"
    );
}

#[test]
fn build_drops_now_denied_paths_instead_of_keeping_stale_content() {
    // The live daemon re-sync path (run-command invalidation -> ensureIndex)
    // goes through kernel.index.build, so build must uphold the same invariant
    // as update: stale content of a path the session can no longer read must
    // not stay queryable — nor exfiltrable via pending_chunks.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("cfg.rs"), "pub fn zz_build_stale_secret() {}\n").unwrap();
    let built = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["cfg.rs"]}));
    assert_eq!(built["indexed"].as_u64(), Some(1), "got {built}");

    // The path becomes FileRead-denied: it now resolves outside the workspace.
    std::fs::remove_file(tmp.ws.join("cfg.rs")).unwrap();
    #[cfg(unix)]
    std::os::unix::fs::symlink("/etc/hosts", tmp.ws.join("cfg.rs")).unwrap();

    let rebuilt = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["cfg.rs"]}));
    assert!(rebuilt.get("__error").is_none(), "got {rebuilt}");

    let found = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_build_stale_secret"}),
    );
    assert!(
        found["hits"].as_array().unwrap().is_empty(),
        "stale content of a now-denied path must be dropped on rebuild: {found}"
    );
    let pending = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": "sess_ix", "model_id": EMBED_MODEL}),
    );
    assert!(pending.get("__error").is_none(), "got {pending}");
    assert!(
        !pending["chunks"]
            .as_array()
            .unwrap()
            .iter()
            .any(|c| c["content"].as_str().unwrap_or_default().contains("zz_build_stale_secret")),
        "stale chunk text must not be handed to the embedding pipeline: {pending}"
    );
    assert_eq!(pending["total_pending"].as_u64(), Some(0), "got {pending}");
}

// ---- V3: impact analysis + D1 embed_store finiteness -----------------------

#[test]
fn impact_walks_transitive_dependents_with_provenance_and_audit() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("core.rs"), "pub fn zz_imp_core() {}\n").unwrap();
    std::fs::write(
        tmp.ws.join("caller.rs"),
        "pub fn zz_imp_caller() {\n    zz_imp_core();\n}\n",
    )
    .unwrap();
    std::fs::write(
        tmp.ws.join("outer.rs"),
        "pub fn zz_imp_outer() {\n    zz_imp_caller();\n}\n",
    )
    .unwrap();
    let built = svc.call(
        "kernel.index.build",
        json!({"session_id": "sess_ix", "paths": ["core.rs", "caller.rs", "outer.rs"]}),
    );
    assert_eq!(built["indexed"].as_u64(), Some(3), "got {built}");

    let report = svc.call(
        "kernel.index.impact",
        json!({"session_id": "sess_ix", "name": "zz_imp_core"}),
    );
    assert!(report.get("__error").is_none(), "got {report}");
    let seeds = report["seeds"].as_array().expect("seeds array");
    assert_eq!(seeds.len(), 1, "got {report}");
    assert_eq!(seeds[0]["name"], "zz_imp_core");
    let deps = report["dependents"].as_array().expect("dependents array");
    assert_eq!(deps.len(), 2, "got {report}");
    assert_eq!(deps[0]["symbol"]["name"], "zz_imp_caller");
    assert_eq!(deps[0]["depth"].as_u64(), Some(1));
    assert!((deps[0]["confidence"].as_f64().unwrap() - 1.0).abs() < 1e-9, "got {report}");
    assert_eq!(deps[0]["source"], "tree-sitter");
    assert_eq!(deps[1]["symbol"]["name"], "zz_imp_outer");
    assert_eq!(deps[1]["depth"].as_u64(), Some(2));
    assert_eq!(report["truncated"], false);
    // Dependents reuse SymbolRecord and carry provenance.
    assert_eq!(deps[0]["symbol"]["content_hash"].as_str().unwrap().len(), 64, "got {report}");
    assert!(!deps[0]["symbol"]["indexed_at"].as_str().unwrap().is_empty(), "got {report}");

    // The query went through the ordinary index gate: a decision-linked
    // ToolApproved with the documented (default-clamped) resource string.
    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_ix"}));
    let gate = events
        .as_array()
        .unwrap()
        .iter()
        .find(|e| e["type"] == "ToolApproved"
            && e["payload"]["resource"] == "index impact zz_imp_core depth=3 limit=50")
        .expect("audited impact gate decision with the documented resource string");
    assert!(gate["permission_decision_id"].is_string());
}

#[test]
fn impact_clamps_depth_and_limit_into_documented_bounds() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("a.rs"), "pub fn zz_clamp_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["a.rs"]}));

    let wild = svc.call(
        "kernel.index.impact",
        json!({"session_id": "sess_ix", "name": "zz_clamp_fn", "max_depth": 99, "limit": 100000}),
    );
    assert!(wild.get("__error").is_none(), "out-of-range params clamp, never error: {wild}");
    let tiny = svc.call(
        "kernel.index.impact",
        json!({"session_id": "sess_ix", "name": "zz_clamp_fn", "max_depth": 0, "limit": 0}),
    );
    assert!(tiny.get("__error").is_none(), "got {tiny}");

    // Clamping is observable: the audited resource carries the clamped values.
    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_ix"}));
    let resources: Vec<String> = events
        .as_array()
        .unwrap()
        .iter()
        .filter_map(|e| e["payload"]["resource"].as_str().map(String::from))
        .collect();
    assert!(
        resources.iter().any(|r| r == "index impact zz_clamp_fn depth=5 limit=200"),
        "got {resources:?}"
    );
    assert!(
        resources.iter().any(|r| r == "index impact zz_clamp_fn depth=1 limit=1"),
        "got {resources:?}"
    );
}

#[test]
fn impact_requires_a_name() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("a.rs"), "pub fn zz_named_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["a.rs"]}));
    let report = svc.call("kernel.index.impact", json!({"session_id": "sess_ix"}));
    let err = report.get("__error").expect("missing name must error");
    assert!(
        err["message"].as_str().unwrap().contains("name is required"),
        "got {err}"
    );
}

#[test]
fn impact_before_build_reports_missing_index() {
    let (_tmp, mut svc) = setup();
    let report = svc.call(
        "kernel.index.impact",
        json!({"session_id": "sess_ix", "name": "zz_any_fn"}),
    );
    let err = report.get("__error").expect("expected error");
    assert!(
        err["message"].as_str().unwrap().contains("index not built"),
        "got {err}"
    );
}

#[test]
fn bundle_file_read_deny_blocks_impact_over_a_previously_built_index() {
    // Impact results are derived from edges, which are derived from file
    // content: the V1/V2 checkpoint-test analogue — a stricter session whose
    // bundle denies FileRead (never mentioning CodeIndex) is denied at query
    // time, even over a previously built index.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("core.rs"), "pub fn zz_imp_locked() {}\n").unwrap();
    std::fs::write(
        tmp.ws.join("caller.rs"),
        "pub fn zz_imp_locked_caller() {\n    zz_imp_locked();\n}\n",
    )
    .unwrap();
    let built = svc.call(
        "kernel.index.build",
        json!({"session_id": "sess_ix", "paths": ["core.rs", "caller.rs"]}),
    );
    assert_eq!(built["indexed"].as_u64(), Some(2), "got {built}");

    svc.call(
        "kernel.session.init",
        json!({
            "session_id": "sess_strict",
            "workspace_root": tmp.ws.to_str().unwrap(),
            "profile": "safe-edit",
            "bundle_toml": "name = \"locked\"\ndeny_capabilities = [\"FileRead\"]\n",
        }),
    );
    let report = svc.call(
        "kernel.index.impact",
        json!({"session_id": "sess_strict", "name": "zz_imp_locked"}),
    );
    let err = report.get("__error").expect("stricter session must be denied");
    assert!(
        err["message"].as_str().unwrap().contains("code index denied"),
        "got {err}"
    );
}

#[test]
fn embed_store_rejects_non_finite_vector_components() {
    // D1: the kernel is the validation chokepoint — a NaN/±Inf component is a
    // param error and the vector is never stored (a NaN vector would silently
    // rank as garbage in cosine_rank forever).
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("v.rs"), "pub fn zz_finite_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["v.rs"]}));

    let pending = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": "sess_ix", "model_id": EMBED_MODEL}),
    );
    assert!(pending.get("__error").is_none(), "got {pending}");
    let chunk = pending["chunks"].as_array().unwrap()[0].clone();
    let total_before = pending["total_pending"].as_u64().unwrap();
    assert!(total_before > 0, "got {pending}");

    for bad in [f32::NAN, f32::INFINITY, f32::NEG_INFINITY] {
        let stored = svc.call(
            "kernel.index.embed_store",
            json!({
                "session_id": "sess_ix",
                "model_id": EMBED_MODEL,
                "dims": 2,
                "embeddings": [{
                    "chunk_id": chunk["chunk_id"],
                    "content_hash": chunk["content_hash"],
                    "vector_base64": vector_base64(&[bad, 1.0]),
                }],
            }),
        );
        let err = stored
            .get("__error")
            .unwrap_or_else(|| panic!("non-finite component ({bad}) must be a param error, got {stored}"));
        assert!(
            err["message"].as_str().unwrap().contains("non-finite"),
            "got {err}"
        );
    }

    // Nothing was stored: the backlog is unchanged.
    let after = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": "sess_ix", "model_id": EMBED_MODEL}),
    );
    assert_eq!(
        after["total_pending"].as_u64(),
        Some(total_before),
        "an invalid vector must never reach the store: {after}"
    );
}

// ---- V4: edges_store + query-vector finiteness + invalidation observability

fn edge_json(src_path: &str, src_line: u64, dst_path: &str, dst_line: u64) -> Value {
    json!({
        "src_path": src_path,
        "src_line": src_line,
        "dst_path": dst_path,
        "dst_line": dst_line,
    })
}

#[test]
fn edges_store_round_trip_upgrades_impact_to_lsp_source() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("core.rs"), "pub fn zz_v4rt_core() {}\n").unwrap();
    std::fs::write(
        tmp.ws.join("caller.rs"),
        "pub fn zz_v4rt_caller() {\n    zz_v4rt_core();\n}\n",
    )
    .unwrap();
    let built = svc.call(
        "kernel.index.build",
        json!({"session_id": "sess_ix", "paths": ["core.rs", "caller.rs"]}),
    );
    assert_eq!(built["indexed"].as_u64(), Some(2), "got {built}");

    let stored = svc.call(
        "kernel.index.edges_store",
        json!({"session_id": "sess_ix", "edges": [edge_json("caller.rs", 2, "core.rs", 1)]}),
    );
    assert!(stored.get("__error").is_none(), "got {stored}");
    assert_eq!(stored["stored"].as_u64(), Some(1), "got {stored}");
    assert_eq!(stored["skipped"].as_array().unwrap().len(), 0, "got {stored}");

    // The persisted precise edge upgrades impact: 1.0 confidence, source lsp.
    let report = svc.call(
        "kernel.index.impact",
        json!({"session_id": "sess_ix", "name": "zz_v4rt_core"}),
    );
    let deps = report["dependents"].as_array().expect("dependents array");
    assert_eq!(deps.len(), 1, "got {report}");
    assert_eq!(deps[0]["source"], "lsp", "got {report}");
    assert!((deps[0]["confidence"].as_f64().unwrap() - 1.0).abs() < 1e-9, "got {report}");

    // Gated + audited like siblings: decision-linked gate with the documented
    // resource string, plus a decision-linked index_edges_stored status event.
    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_ix"}));
    let events = events.as_array().unwrap();
    assert!(
        events.iter().any(|e| e["type"] == "ToolApproved"
            && e["payload"]["resource"] == "index edges_store edges=1"),
        "expected the audited edges_store gate decision"
    );
    let status = events
        .iter()
        .find(|e| e["payload"]["status"] == "index_edges_stored")
        .expect("index_edges_stored status event");
    assert_eq!(status["type"], "ToolApproved");
    assert!(status["permission_decision_id"].is_string());
    assert_eq!(status["payload"]["stored"].as_u64(), Some(1), "got {status}");
    assert!(status["payload"]["duration_ms"].is_number(), "got {status}");
}

#[test]
fn edges_store_skips_denied_and_unresolved_endpoints() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("ok.rs"), "pub fn zz_v4skip_fn() {}\n").unwrap();
    // .env exists on disk but is FileRead-denied (sensitive).
    std::fs::write(tmp.ws.join(".env"), "SECRET=1\n").unwrap();
    let outside = tmp.base.join("outside.rs");
    std::fs::write(&outside, "pub fn zz_v4skip_outside() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["ok.rs"]}));

    let stored = svc.call(
        "kernel.index.edges_store",
        json!({"session_id": "sess_ix", "edges": [
            edge_json(".env", 1, "ok.rs", 1),           // FileRead-denied src
            edge_json("../outside.rs", 1, "ok.rs", 1),  // workspace escape
            edge_json("ok.rs", 99, "ok.rs", 1),         // no symbol at src line
            edge_json("ok.rs", 1, "ok.rs", 1),          // self edge
        ]}),
    );
    assert!(stored.get("__error").is_none(), "skips are success: {stored}");
    assert_eq!(stored["stored"].as_u64(), Some(0), "got {stored}");
    let skipped = stored["skipped"].as_array().unwrap();
    assert_eq!(skipped.len(), 4, "got {stored}");
    assert!(
        skipped.iter().all(|s| !s["reason"].as_str().unwrap_or_default().is_empty()),
        "every skip carries a reason: {stored}"
    );
    // Nothing may have landed in the edges graph.
    let report = svc.call(
        "kernel.index.impact",
        json!({"session_id": "sess_ix", "name": "zz_v4skip_fn"}),
    );
    assert_eq!(report["dependents"].as_array().unwrap().len(), 0, "got {report}");
}

#[test]
fn edges_store_rejects_more_than_256_edges() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("a.rs"), "pub fn zz_v4cap_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["a.rs"]}));

    let edges: Vec<Value> = (0..257).map(|_| edge_json("a.rs", 1, "a.rs", 1)).collect();
    let stored = svc.call(
        "kernel.index.edges_store",
        json!({"session_id": "sess_ix", "edges": edges}),
    );
    let err = stored.get("__error").expect("257 edges must be a param error");
    assert!(
        err["message"].as_str().unwrap().contains("at most 256"),
        "got {err}"
    );
}

#[test]
fn edges_store_rejects_zero_line_numbers() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("a.rs"), "pub fn zz_v4line_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["a.rs"]}));

    let stored = svc.call(
        "kernel.index.edges_store",
        json!({"session_id": "sess_ix", "edges": [edge_json("a.rs", 0, "a.rs", 1)]}),
    );
    let err = stored.get("__error").expect("line 0 must be a param error");
    assert!(err["message"].as_str().unwrap().contains("line"), "got {err}");
}

#[test]
fn edges_store_requires_an_edges_array() {
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("a.rs"), "pub fn zz_v4arr_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["a.rs"]}));

    let stored = svc.call("kernel.index.edges_store", json!({"session_id": "sess_ix"}));
    let err = stored.get("__error").expect("missing edges must error");
    assert!(
        err["message"].as_str().unwrap().contains("edges array required"),
        "got {err}"
    );
}

#[test]
fn bundle_file_read_deny_blocks_edges_store() {
    // An edge names two readable positions — relation data derived from
    // content. A stricter session whose bundle denies FileRead (never
    // mentioning CodeIndex) must be denied, like every index surface.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("x.rs"), "pub fn zz_v4locked_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["x.rs"]}));

    svc.call(
        "kernel.session.init",
        json!({
            "session_id": "sess_strict",
            "workspace_root": tmp.ws.to_str().unwrap(),
            "profile": "safe-edit",
            "bundle_toml": "name = \"locked\"\ndeny_capabilities = [\"FileRead\"]\n",
        }),
    );
    let stored = svc.call(
        "kernel.index.edges_store",
        json!({"session_id": "sess_strict", "edges": [edge_json("x.rs", 1, "x.rs", 1)]}),
    );
    let err = stored.get("__error").expect("stricter session must be denied");
    assert!(
        err["message"].as_str().unwrap().contains("code index denied"),
        "got {err}"
    );
}

#[test]
fn search_rejects_non_finite_query_vector_components() {
    // D1: the crate's finiteness chokepoint is embed_store, which search
    // never crosses — the service must reject NaN/±Inf query-vector
    // components as a param error before the search runs.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("q.rs"), "pub fn zz_v4finite_fn() {}\n").unwrap();
    svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["q.rs"]}));

    for bad in [f32::NAN, f32::INFINITY, f32::NEG_INFINITY] {
        let found = svc.call(
            "kernel.index.search",
            json!({
                "session_id": "sess_ix",
                "query": "zz_v4finite_fn",
                "query_vector_base64": vector_base64(&[bad, 1.0]),
                "model_id": EMBED_MODEL,
            }),
        );
        let err = found
            .get("__error")
            .unwrap_or_else(|| panic!("non-finite component ({bad}) must be a param error, got {found}"));
        assert!(
            err["message"].as_str().unwrap().contains("non-finite"),
            "got {err}"
        );
    }
}

#[test]
fn patch_apply_records_invalidation_failure_observably() {
    // D4 (kernel half): a failing index update inside the post-patch hook
    // must no longer be silently swallowed — the patch still succeeds, but an
    // index_invalidation_failed status event lands in the audit chain so a
    // stale index is visible (the next sweep/build heals it).
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("m.rs"), "pub fn zz_v4inv_before() {}\n").unwrap();
    let built = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["m.rs"]}));
    let db_path = std::path::PathBuf::from(built["db_path"].as_str().expect("db_path"));
    assert!(db_path.exists(), "got {built}");

    // A second session over the same workspace has no open index handle; its
    // hook must open the db — which we replace with a directory, so the open
    // fails deterministically.
    svc.call(
        "kernel.session.init",
        json!({
            "session_id": "sess_two",
            "workspace_root": tmp.ws.to_str().unwrap(),
            "profile": "safe-edit",
        }),
    );
    std::fs::remove_file(&db_path).unwrap();
    let _ = std::fs::remove_file(db_path.with_extension("sqlite-wal"));
    let _ = std::fs::remove_file(db_path.with_extension("sqlite-shm"));
    std::fs::create_dir(&db_path).unwrap();

    let proposed = svc.call(
        "kernel.patch.propose",
        json!({"session_id": "sess_two", "reason": "t", "files": [{"path": "m.rs", "new_content": "pub fn zz_v4inv_after() {}\n"}]}),
    );
    let patch_id = proposed["patch_id"].as_str().unwrap().to_string();
    let applied = svc.call("kernel.patch.apply", json!({"session_id": "sess_two", "patch_id": patch_id}));
    assert_eq!(applied["status"], "applied", "the patch itself must never fail: {applied}");

    let events = svc.call("kernel.audit.read", json!({"session_id": "sess_two"}));
    let failure = events
        .as_array()
        .unwrap()
        .iter()
        .find(|e| e["payload"]["status"] == "index_invalidation_failed")
        .expect("a failed post-patch invalidation must record index_invalidation_failed")
        .clone();
    assert!(
        !failure["payload"]["error"].as_str().unwrap_or_default().is_empty(),
        "the event carries the error text (never content): {failure}"
    );
    let _ = std::fs::remove_dir(&db_path);
}

#[test]
fn build_prunes_paths_missing_from_the_scan() {
    // Files deleted by a mutating run command vanish from the Zig scan; the
    // rebuild is a full sync to the allowlist, so their rows (and pending
    // chunk text) must be pruned, not kept searchable indefinitely.
    let (tmp, mut svc) = setup();
    std::fs::write(tmp.ws.join("a.rs"), "pub fn zz_prune_deleted_fn() {}\n").unwrap();
    std::fs::write(tmp.ws.join("b.rs"), "pub fn zz_prune_kept_fn() {}\n").unwrap();
    let built = svc.call(
        "kernel.index.build",
        json!({"session_id": "sess_ix", "paths": ["a.rs", "b.rs"]}),
    );
    assert_eq!(built["indexed"].as_u64(), Some(2), "got {built}");

    std::fs::remove_file(tmp.ws.join("a.rs")).unwrap();
    let rebuilt = svc.call("kernel.index.build", json!({"session_id": "sess_ix", "paths": ["b.rs"]}));
    assert!(rebuilt.get("__error").is_none(), "got {rebuilt}");

    let gone = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_prune_deleted_fn"}),
    );
    assert!(
        gone["hits"].as_array().unwrap().is_empty(),
        "deleted files must be pruned on rebuild: {gone}"
    );
    let kept = svc.call(
        "kernel.index.search",
        json!({"session_id": "sess_ix", "query": "zz_prune_kept_fn"}),
    );
    assert!(!kept["hits"].as_array().unwrap().is_empty(), "got {kept}");
    let pending = svc.call(
        "kernel.index.pending_chunks",
        json!({"session_id": "sess_ix", "model_id": EMBED_MODEL, "limit": 256}),
    );
    assert!(
        !pending["chunks"]
            .as_array()
            .unwrap()
            .iter()
            .any(|c| c["content"].as_str().unwrap_or_default().contains("zz_prune_deleted_fn")),
        "pruned chunk text must not reach the embedding pipeline: {pending}"
    );
}
