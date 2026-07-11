//! carina-kernel-service — the Capability Kernel as a process.
//!
//! The Go control plane spawns this binary and speaks JSON-RPC 2.0 over
//! stdio (PRD §15.1). Every side effect in the runtime funnels through
//! here: capability decisions, approvals, the append-only event log, and
//! transactional patch apply/rollback. The kernel is the single writer of
//! the audit log so the control plane cannot bypass it.

use carina_audit::{Actor, Event, EventType};
use carina_index::{ChunkEmbedding, CodeIndex, FileChange as IndexChange, IngestFile};
use carina_kernel::{ApprovalPolicy, Kernel, KernelError};
use carina_patch::{content_hash, PatchTransaction};
use carina_policy::{ApprovalMode, Capability, CapabilityRequest, Decision, PolicyBundle, Principal, Profile, Verdict};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io::{BufRead, Write};
use std::path::{Component, Path, PathBuf};
use std::process::{Command, Stdio};

fn main() {
    let state_dir = std::env::args()
        .nth(1)
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(".carina-state"));
    let mut service = Service::new(state_dir);

    let stdin = std::io::stdin();
    let mut stdout = std::io::stdout();
    for line in stdin.lock().lines() {
        let line = match line {
            Ok(l) => l,
            Err(_) => break,
        };
        if line.trim().is_empty() {
            continue;
        }
        let response = service.handle_line(&line);
        let _ = writeln!(stdout, "{response}");
        let _ = stdout.flush();
    }
}

struct SessionCtx {
    kernel: Kernel,
    workspace_root: PathBuf,
    patches: HashMap<String, PatchRecord>,
    /// decision_id -> pending decision awaiting human approval
    pending: HashMap<String, Decision>,
    /// decision_id -> resolved (approved/denied) decision
    resolved: HashMap<String, Decision>,
    /// Governed code index for this workspace, opened lazily at
    /// <state_dir>/index/<sha256(workspace_root)>.sqlite (kernel.index.*).
    index: Option<CodeIndex>,
}

struct PatchRecord {
    tx: PatchTransaction,
    files: Vec<FileChange>,
    snapshot_dir: PathBuf,
}

#[derive(Clone)]
struct FileChange {
    path: String,
    existed: bool,
    old_content: Vec<u8>,
    new_content: String,
}

struct Service {
    state_dir: PathBuf,
    sessions: HashMap<String, SessionCtx>,
}

impl Service {
    fn new(state_dir: PathBuf) -> Self {
        Self { state_dir, sessions: HashMap::new() }
    }

    fn handle_line(&mut self, line: &str) -> String {
        let req: Value = match serde_json::from_str(line) {
            Ok(v) => v,
            Err(e) => return error_response(Value::Null, -32700, &e.to_string()),
        };
        let id = req.get("id").cloned().unwrap_or(Value::Null);
        let method = req.get("method").and_then(Value::as_str).unwrap_or("");
        let params = req.get("params").cloned().unwrap_or_else(|| json!({}));

        match self.dispatch(method, &params) {
            Ok(result) => json!({"jsonrpc": "2.0", "id": id, "result": result}).to_string(),
            Err(msg) => error_response(id, -32603, &msg),
        }
    }

    fn dispatch(&mut self, method: &str, p: &Value) -> Result<Value, String> {
        match method {
            "ping" => Ok(json!({"ok": true, "event_schema_version": "0.2.0"})),
            "kernel.session.init" => self.session_init(p),
            "kernel.session.add_dir" => self.session_add_dir(p),
            "kernel.request" => self.capability_request(p),
            "kernel.approve" => self.approve(p),
            "kernel.deny" => self.deny(p),
            "kernel.event.record" => self.event_record(p),
            "kernel.audit.read" => self.audit_read(p),
            "kernel.audit.report" => self.audit_report(p),
            "kernel.audit.export" => self.audit_export(p),
            "kernel.audit.verify" => self.audit_verify(p),
            "kernel.patch.propose" => self.patch_propose(p),
            "kernel.patch.apply" => self.patch_apply(p),
            "kernel.patch.rollback" => self.patch_rollback(p),
            "kernel.patch.list" => self.patch_list(p),
            "kernel.patch.show" => self.patch_show(p),
            "kernel.index.build" => self.index_build(p),
            "kernel.index.update" => self.index_update(p),
            "kernel.index.search" => self.index_search(p),
            "kernel.index.pending_chunks" => self.index_pending_chunks(p),
            "kernel.index.embed_store" => self.index_embed_store(p),
            "kernel.index.edges_store" => self.index_edges_store(p),
            "kernel.index.symbols" => self.index_symbols(p),
            "kernel.index.impact" => self.index_impact(p),
            "kernel.index.map" => self.index_map(p),
            "kernel.classify" => {
                let cmd = str_param(p, "command")?;
                Ok(json!({"command": cmd, "risk_level": carina_policy::classify_command(&cmd)}))
            }
            "kernel.profile.describe" => self.profile_describe(p),
            "kernel.secret.grant" => self.secret_grant(p),
            "kernel.secret.request" => self.secret_request(p),
            "kernel.redact" => self.redact(p),
            "kernel.plugin.inspect" => self.plugin_inspect(p),
            "kernel.plugin.run" => self.plugin_run(p),
            _ => Err(format!("unknown method {method}")),
        }
    }

    fn session_init(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let workspace_root = PathBuf::from(str_param(p, "workspace_root")?);
        let events_dir = self.state_dir.join("events");

        // A session may use a builtin profile by name, or supply a custom
        // profile as inline TOML (PRD §8.3).
        let (mut kernel, profile_name) = if let Some(toml) = p.get("profile_toml").and_then(Value::as_str) {
            let profile = Profile::from_toml(toml).map_err(err_str)?;
            let name = profile.name.clone();
            (
                Kernel::with_profile(&session_id, &workspace_root, profile, &events_dir).map_err(err_str)?,
                name,
            )
        } else {
            let name = p.get("profile").and_then(Value::as_str).unwrap_or("safe-edit").to_string();
            (
                Kernel::new(&session_id, &workspace_root, &name, &events_dir).map_err(err_str)?,
                name,
            )
        };

        // Enterprise: attach an org policy bundle (mandatory denies).
        if let Some(bundle_toml) = p.get("bundle_toml").and_then(Value::as_str) {
            kernel.set_bundle(PolicyBundle::from_toml(bundle_toml).map_err(err_str)?);
        }
        // Enterprise: trust publisher keys for signed-plugin enforcement.
        if let Some(keys) = p.get("trusted_plugin_keys").and_then(Value::as_array) {
            for key in keys {
                if let Some(b64) = key.as_str() {
                    let raw = base64_decode(b64).ok_or("invalid base64 plugin key")?;
                    kernel.trust_plugin_key(&raw).map_err(err_str)?;
                }
            }
        }
        // Goal mechanism: the approval-mode axis (untrusted/on_request/never).
        if let Some(mode) = p.get("approval_mode").and_then(Value::as_str) {
            kernel.set_approval_mode(ApprovalMode::from_str(mode));
        }
        // Enterprise: role-based approval thresholds.
        if let Some(rules) = p.get("approval_policy").and_then(Value::as_array) {
            let mut policy = ApprovalPolicy::default();
            for rule in rules {
                if let (Some(risk), Some(role)) = (
                    rule.get("min_risk").and_then(Value::as_u64),
                    rule.get("role").and_then(Value::as_str),
                ) {
                    policy.required_role_at_risk.push((risk as u8, role.to_string()));
                }
            }
            kernel.set_approval_policy(policy);
        }

        self.sessions.insert(
            session_id.clone(),
            SessionCtx {
                kernel,
                workspace_root,
                patches: HashMap::new(),
                pending: HashMap::new(),
                resolved: HashMap::new(),
                index: None,
            },
        );
        Ok(json!({"session_id": session_id, "profile": profile_name}))
    }

    /// Grants the session an additional allowed root (`/add-dir`). Paths within
    /// it are thereafter evaluated as in-workspace.
    fn session_add_dir(&mut self, p: &Value) -> Result<Value, String> {
        let path = str_param(p, "path")?;
        let ctx = self.ctx(p)?;
        ctx.kernel.add_dir(PathBuf::from(&path));
        Ok(json!({"path": path, "additional_roots": ctx.kernel.additional_roots().len()}))
    }

    fn profile_describe(&mut self, p: &Value) -> Result<Value, String> {
        let ctx = self.ctx(p)?;
        serde_json::to_value(ctx.kernel.profile().describe()).map_err(err_str)
    }

    fn secret_grant(&mut self, p: &Value) -> Result<Value, String> {
        let name = str_param(p, "name")?;
        let value = str_param(p, "value")?;
        let ctx = self.ctx(p)?;
        ctx.kernel.secrets_mut().grant(&name, &value);
        // Never echo the value; confirm the handle only.
        Ok(json!({"name": name, "handle": format!("secret://{name}")}))
    }

    fn secret_request(&mut self, p: &Value) -> Result<Value, String> {
        let name = str_param(p, "name")?;
        let ctx = self.ctx(p)?;
        let (decision, handle) = ctx.kernel.request_secret(&name).map_err(err_str)?;
        Ok(json!({"decision": decision, "handle": handle}))
    }

    fn redact(&mut self, p: &Value) -> Result<Value, String> {
        let text = str_param(p, "text")?;
        let ctx = self.ctx(p)?;
        Ok(json!({"text": ctx.kernel.secrets().redact(&text)}))
    }

    /// Parses a plugin manifest and returns its declared permissions for
    /// install-time review (PRD §8.7: permissions shown at install).
    fn plugin_inspect(&mut self, p: &Value) -> Result<Value, String> {
        let manifest_toml = str_param(p, "manifest_toml")?;
        let manifest = carina_plugin_runtime::Manifest::from_toml(&manifest_toml).map_err(err_str)?;
        Ok(json!({
            "name": manifest.name,
            "version": manifest.version,
            "permissions": manifest.permission_summary(),
        }))
    }

    /// Loads and runs a WASM plugin under the session policy. `wasm_base64`
    /// carries the module bytes; each capability decision is audited.
    fn plugin_run(&mut self, p: &Value) -> Result<Value, String> {
        let manifest_toml = str_param(p, "manifest_toml")?;
        let wasm_b64 = str_param(p, "wasm_base64")?;
        let wasm = base64_decode(&wasm_b64).ok_or("invalid base64 wasm")?;
        let signature = match p.get("signature_base64").and_then(Value::as_str) {
            Some(sig_b64) => Some(base64_decode(sig_b64).ok_or("invalid base64 signature")?),
            None => None,
        };
        let manifest = carina_plugin_runtime::Manifest::from_toml(&manifest_toml).map_err(err_str)?;
        let ctx = self.ctx(p)?;
        let outcome = ctx
            .kernel
            .run_plugin_signed(&manifest, &wasm, signature.as_deref())
            .map_err(err_str)?;
        let decisions: Vec<Value> = outcome
            .decisions
            .iter()
            .map(|d| json!({"capability": d.capability, "resource": d.resource, "allowed": d.allowed, "reason": d.reason}))
            .collect();
        Ok(json!({
            "result_code": outcome.result_code,
            "logs": outcome.logs,
            "decisions": decisions,
        }))
    }

    fn ctx(&mut self, p: &Value) -> Result<&mut SessionCtx, String> {
        let session_id = str_param(p, "session_id")?;
        self.sessions
            .get_mut(&session_id)
            .ok_or_else(|| format!("unknown session {session_id}"))
    }

    fn capability_request(&mut self, p: &Value) -> Result<Value, String> {
        let capability: Capability =
            serde_json::from_value(p.get("capability").cloned().unwrap_or(Value::Null))
                .map_err(|e| format!("invalid capability: {e}"))?;
        let resource = str_param(p, "resource")?;
        let session_id = str_param(p, "session_id")?;
        let task_id = p.get("task_id").and_then(Value::as_str).map(String::from);
        let ctx = self.ctx(p)?;

        let request = CapabilityRequest {
            capability,
            requested_by: Principal::Agent,
            resource,
            session_id,
            task_id,
        };
        let decision = ctx.kernel.request(request).map_err(err_str)?;
        if decision.decision == Verdict::RequiresApproval {
            ctx.pending.insert(decision.decision_id.clone(), decision.clone());
        }
        serde_json::to_value(&decision).map_err(err_str)
    }

    fn approve(&mut self, p: &Value) -> Result<Value, String> {
        let decision_id = str_param(p, "decision_id")?;
        let approver = p.get("approver").and_then(Value::as_str).unwrap_or("user").to_string();
        let role = p.get("role").and_then(Value::as_str).map(String::from);
        let for_session = p.get("for_session").and_then(Value::as_bool).unwrap_or(false);
        let justification = p.get("justification").and_then(Value::as_str).unwrap_or("approved for session");
        let ctx = self.ctx(p)?;
        let pending = ctx
            .pending
            .remove(&decision_id)
            .ok_or_else(|| format!("no pending decision {decision_id}"))?;
        let approved = if for_session && role.is_none() {
            ctx.kernel.approve_for_session_with_justification(&pending, &approver, justification).map_err(err_str)?
        } else {
            ctx.kernel.approve_as(&pending, &approver, role.as_deref()).map_err(err_str)?
        };
        // A role-rejected approval stays pending so it can be retried.
        if approved.decision != Verdict::Allowed {
            ctx.pending.insert(decision_id.clone(), pending);
        }
        ctx.resolved.insert(decision_id, approved.clone());
        serde_json::to_value(&approved).map_err(err_str)
    }

    fn deny(&mut self, p: &Value) -> Result<Value, String> {
        let decision_id = str_param(p, "decision_id")?;
        let approver = p.get("approver").and_then(Value::as_str).unwrap_or("user").to_string();
        let reason = p.get("reason").and_then(Value::as_str).unwrap_or("denied").to_string();
        let ctx = self.ctx(p)?;
        let pending = ctx
            .pending
            .remove(&decision_id)
            .ok_or_else(|| format!("no pending decision {decision_id}"))?;
        let denied = ctx.kernel.deny(&pending, &approver, &reason).map_err(err_str)?;
        ctx.resolved.insert(decision_id, denied.clone());
        serde_json::to_value(&denied).map_err(err_str)
    }

    fn event_record(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let event_type: EventType =
            serde_json::from_value(p.get("type").cloned().unwrap_or(Value::Null))
                .map_err(|e| format!("invalid event type: {e}"))?;
        let payload = p.get("payload").cloned().unwrap_or_else(|| json!({}));
        let ctx = self.ctx(p)?;

        let actor = p
            .get("actor")
            .and_then(Value::as_str)
            .map(Actor::from_str)
            .unwrap_or(Actor::Go); // events recorded via RPC come from the Go control plane
        let mut event = Event::new(&session_id, event_type, payload).with_actor(actor);
        if let Some(task_id) = p.get("task_id").and_then(Value::as_str) {
            event = event.with_task(task_id);
        }
        if let Some(decision_id) = p.get("permission_decision_id").and_then(Value::as_str) {
            event = event.with_decision(decision_id);
        }
        ctx.kernel.record_event(&event).map_err(err_str)?;
        Ok(json!({"event_id": event.event_id}))
    }

    fn audit_read(&mut self, p: &Value) -> Result<Value, String> {
        let ctx = self.ctx(p)?;
        let events = ctx.kernel.audit().read_all().map_err(err_str)?;
        serde_json::to_value(&events).map_err(err_str)
    }

    fn audit_export(&mut self, p: &Value) -> Result<Value, String> {
        let ctx = self.ctx(p)?;
        ctx.kernel.export_audit().map_err(err_str)
    }

    fn audit_verify(&mut self, p: &Value) -> Result<Value, String> {
        let ctx = self.ctx(p)?;
        let report = ctx.kernel.audit().verify().map_err(err_str)?;
        serde_json::to_value(&report).map_err(err_str)
    }

    fn audit_report(&mut self, p: &Value) -> Result<Value, String> {
        let ctx = self.ctx(p)?;
        let events = ctx.kernel.audit().read_all().map_err(err_str)?;

        let mut by_type: HashMap<String, u64> = HashMap::new();
        let mut violations = Vec::new();
        let mut files_read = Vec::new();
        let mut commands = Vec::new();
        for ev in &events {
            let type_name = serde_json::to_value(ev.event_type)
                .ok()
                .and_then(|v| v.as_str().map(String::from))
                .unwrap_or_default();
            *by_type.entry(type_name).or_insert(0) += 1;
            match ev.event_type {
                EventType::PolicyViolation => violations.push(ev.payload.clone()),
                EventType::FileRead => {
                    if let Some(path) = ev.payload.get("resource").or(ev.payload.get("path")) {
                        files_read.push(path.clone());
                    }
                }
                EventType::CommandStarted => {
                    if let Some(cmd) = ev.payload.get("command") {
                        commands.push(cmd.clone());
                    }
                }
                _ => {}
            }
        }
        Ok(json!({
            "session_id": str_param(p, "session_id")?,
            "total_events": events.len(),
            "events_by_type": by_type,
            "policy_violations": violations,
            "files_read": files_read,
            "commands": commands,
        }))
    }

    // ---- transactional patches ------------------------------------------

    fn patch_propose(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let reason = p.get("reason").and_then(Value::as_str).unwrap_or("").to_string();
        let task_id = p.get("task_id").and_then(Value::as_str).map(String::from);
        let files_param = p
            .get("files")
            .and_then(Value::as_array)
            .ok_or("files array required")?
            .clone();
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;

        let mut changes = Vec::new();
        for f in &files_param {
            let rel = f.get("path").and_then(Value::as_str).ok_or("file.path required")?;
            let new_content = f
                .get("new_content")
                .and_then(Value::as_str)
                .ok_or("file.new_content required")?
                .to_string();
            let abs = ctx.workspace_root.join(rel);
            if !carina_policy::path_within_workspace(&ctx.workspace_root, &abs) {
                return Err(format!("patch path escapes workspace: {rel}"));
            }
            let (existed, old_content) = match std::fs::read(&abs) {
                Ok(bytes) => (true, bytes),
                Err(_) => (false, Vec::new()),
            };
            changes.push(FileChange { path: rel.to_string(), existed, old_content, new_content });
        }

        let base = combined_hash(&changes, Pre::Old);
        let diff = render_diff(&changes);
        let paths: Vec<String> = changes.iter().map(|c| c.path.clone()).collect();
        let agent_step_id = p.get("agent_step_id").and_then(Value::as_str).map(String::from);
        let model_id = p.get("model_id").and_then(Value::as_str).map(String::from);
        let tx = PatchTransaction::propose(&session_id, paths.clone(), base.as_bytes(), &diff, &reason)
            .map_err(err_str)?
            .with_provenance(task_id.clone(), agent_step_id, model_id);

        let snapshot_dir = state_dir.join("snapshots").join(&tx.patch_id);
        std::fs::create_dir_all(&snapshot_dir).map_err(err_str)?;
        for (i, c) in changes.iter().enumerate() {
            if c.existed {
                std::fs::write(snapshot_dir.join(format!("{i}.pre")), &c.old_content).map_err(err_str)?;
            }
        }

        let mut event = Event::new(
            &session_id,
            EventType::PatchProposed,
            json!({"patch_id": tx.patch_id, "affected_files": paths, "reason": reason}),
        );
        if let Some(t) = &task_id {
            event = event.with_task(t);
        }
        ctx.kernel.record_event(&event).map_err(err_str)?;

        let result = serde_json::to_value(&tx).map_err(err_str)?;
        ctx.patches.insert(
            tx.patch_id.clone(),
            PatchRecord { tx, files: changes, snapshot_dir },
        );
        Ok(result)
    }

    fn patch_apply(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let patch_id = str_param(p, "patch_id")?;
        let approver = p.get("approver").and_then(Value::as_str).unwrap_or("user").to_string();
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;

        let record = ctx.patches.remove(&patch_id).ok_or_else(|| format!("unknown patch {patch_id}"))?;

        // Capability gate: PatchApply always goes through policy.
        let request = CapabilityRequest {
            capability: Capability::PatchApply,
            requested_by: Principal::Agent,
            resource: patch_id.clone(),
            session_id: session_id.clone(),
            task_id: None,
        };
        let decision = ctx.kernel.request(request).map_err(err_str)?;
        let decision = match decision.decision {
            Verdict::Denied => {
                ctx.patches.insert(patch_id.clone(), record);
                return Err(format!("patch apply denied: {}", decision.reason));
            }
            Verdict::RequiresApproval => ctx.kernel.approve(&decision, &approver).map_err(err_str)?,
            Verdict::Allowed => decision,
        };

        // Conflict detection against current on-disk state.
        let mut current = record.files.clone();
        for c in current.iter_mut() {
            let abs = ctx.workspace_root.join(&c.path);
            c.old_content = std::fs::read(&abs).unwrap_or_default();
        }
        let current_hash = combined_hash(&current, Pre::Old);

        let tx = record
            .tx
            .validate(current_hash.as_bytes())
            .map_err(|e| format!("patch validation failed: {e}"))?
            .approve(false)
            .map_err(err_str)?;

        // Delegate the actual disk write to the Zig carina-patch-native tool
        // (PRD §4.4: file mutation runs in the native toolchain, not Rust).
        // The tool applies atomically (all-or-nothing) and restores on
        // failure; if the tool is unavailable we fail rather than writing
        // directly, so Zig is the only patch-apply path (PRD §16.5).
        let plan = build_patch_plan(&ctx.workspace_root, &record.files, &record.snapshot_dir);
        match run_patch_native("apply", &plan) {
            Ok(status) if status == "applied" => {}
            outcome => {
                let reason = match outcome {
                    Ok(s) => format!("carina-patch-native reported '{s}', expected 'applied'"),
                    Err(e) => e,
                };
                let failed = tx.fail().map_err(err_str)?;
                ctx.kernel
                    .record_event(&Event::new(
                        &session_id,
                        EventType::PatchFailed,
                        json!({"patch_id": patch_id, "error": reason}),
                    ).with_actor(Actor::Zig))
                    .map_err(err_str)?;
                ctx.patches.insert(
                    patch_id.clone(),
                    PatchRecord { tx: failed, files: record.files.clone(), snapshot_dir: record.snapshot_dir.clone() },
                );
                return Err(format!("patch apply failed: {reason}"));
            }
        }

        let new_hash = combined_hash(&record.files, Pre::New);
        let tx = tx
            .mark_applied(new_hash.as_bytes(), record.snapshot_dir.to_string_lossy())
            .map_err(err_str)?;

        let event = Event::new(
            &session_id,
            EventType::PatchApplied,
            json!({"patch_id": patch_id, "new_hash": tx.new_hash, "rollback_pointer": tx.rollback_pointer}),
        )
        .with_decision(&decision.decision_id);
        ctx.kernel.record_event(&event).map_err(err_str)?;

        // Keep the code index in step with the write (no-op without an index).
        invalidate_index_after_patch(&state_dir, ctx, &session_id, &record.files);

        let result = serde_json::to_value(&tx).map_err(err_str)?;
        ctx.patches.insert(patch_id, PatchRecord { tx, files: record.files, snapshot_dir: record.snapshot_dir });
        Ok(result)
    }

    fn patch_rollback(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let patch_id = str_param(p, "patch_id")?;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;
        let record = ctx.patches.remove(&patch_id).ok_or_else(|| format!("unknown patch {patch_id}"))?;

        ctx.kernel
            .record_event(&Event::new(
                &session_id,
                EventType::RollbackStarted,
                json!({"patch_id": patch_id, "rollback_pointer": record.tx.rollback_pointer}),
            ))
            .map_err(err_str)?;

        // Restore via the Zig tool (§4.4): copy snapshots back / delete
        // files the patch created.
        let plan = build_patch_plan(&ctx.workspace_root, &record.files, &record.snapshot_dir);
        match run_patch_native("rollback", &plan) {
            Ok(status) if status == "rolled_back" => {}
            outcome => {
                let reason = match outcome {
                    Ok(s) => format!("carina-patch-native reported '{s}'"),
                    Err(e) => e,
                };
                ctx.patches.insert(
                    patch_id.clone(),
                    PatchRecord { tx: record.tx, files: record.files, snapshot_dir: record.snapshot_dir },
                );
                return Err(format!("rollback failed: {reason}"));
            }
        }

        let tx = record.tx.rollback().map_err(err_str)?;
        ctx.kernel
            .record_event(&Event::new(&session_id, EventType::RollbackCompleted, json!({"patch_id": patch_id})).with_actor(Actor::Zig))
            .map_err(err_str)?;

        // Keep the code index in step with the restore (no-op without an index).
        invalidate_index_after_patch(&state_dir, ctx, &session_id, &record.files);

        let result = serde_json::to_value(&tx).map_err(err_str)?;
        ctx.patches.insert(patch_id, PatchRecord { tx, files: record.files, snapshot_dir: record.snapshot_dir });
        Ok(result)
    }

    fn patch_list(&mut self, p: &Value) -> Result<Value, String> {
        let ctx = self.ctx(p)?;
        let list: Vec<Value> = ctx
            .patches
            .values()
            .map(|r| serde_json::to_value(&r.tx).unwrap_or(Value::Null))
            .collect();
        Ok(Value::Array(list))
    }

    fn patch_show(&mut self, p: &Value) -> Result<Value, String> {
        let patch_id = str_param(p, "patch_id")?;
        let ctx = self.ctx(p)?;
        let record = ctx.patches.get(&patch_id).ok_or_else(|| format!("unknown patch {patch_id}"))?;
        serde_json::to_value(&record.tx).map_err(err_str)
    }

    // ---- governed code intelligence (docs/plans/code-intelligence.md) ------

    /// Builds the index from an explicit path allowlist. Every candidate path
    /// is re-gated through FileRead policy: denied paths (workspace escapes,
    /// sensitive files) are skipped — their content can never enter the index.
    /// Files are read and ingested in bounded batches (per-file size cap plus
    /// a batch-bytes flush threshold) so a large workspace cannot balloon the
    /// kernel process — the governance chokepoint — out of memory.
    ///
    /// A build is a full sync to the allowlist: previously indexed paths that
    /// are absent from it (deleted by a run command, vanished from the scan)
    /// or that are no longer FileRead-allowed are pruned, upholding the same
    /// invariant as update — stale content of a file the session can no
    /// longer read must never stay queryable (or reach pending_chunks).
    fn index_build(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let paths = str_array_param(p, "paths")?;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;
        let started = std::time::Instant::now();

        let decision = index_gate(ctx, &session_id, format!("index build files={}", paths.len()))?;
        ensure_index(&state_dir, ctx)?;
        let mut totals = IngestTotals::default();
        let mut skipped: Vec<Value> = Vec::new();
        let mut batch: Vec<IngestFile> = Vec::new();
        let mut batch_bytes = 0usize;
        // Paths whose previously indexed rows may survive this build; every
        // other indexed path is reconciled away below.
        let mut keep: std::collections::HashSet<String> = std::collections::HashSet::new();
        for raw in &paths {
            let (rel, abs) = match rel_and_abs(&ctx.workspace_root, raw) {
                Ok(pair) => pair,
                Err(reason) => {
                    skipped.push(json!({"path": raw, "reason": reason}));
                    continue;
                }
            };
            match ctx.kernel.request_file_read(&abs.to_string_lossy(), None) {
                Ok(d) if d.decision == Verdict::Allowed => {}
                Ok(d) => {
                    skipped.push(json!({"path": rel, "reason": d.reason}));
                    continue;
                }
                Err(e) => {
                    skipped.push(json!({"path": rel, "reason": e.to_string()}));
                    continue;
                }
            }
            if let Some(reason) = exceeds_size_cap(&abs) {
                skipped.push(json!({"path": rel, "reason": reason}));
                continue;
            }
            match std::fs::read_to_string(&abs) {
                Ok(content) => {
                    batch_bytes += content.len();
                    keep.insert(rel.clone());
                    batch.push(IngestFile { path: rel, content });
                }
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                    // Gone from disk: fall through to the reconciliation drop.
                    skipped.push(json!({"path": rel, "reason": format!("read error: {e}")}));
                }
                Err(e) => {
                    // Still present and still FileRead-allowed, but unreadable
                    // (permission denied, non-UTF-8, ...): a transient read
                    // failure is not a deletion — keep the indexed rows.
                    keep.insert(rel.clone());
                    skipped.push(json!({"path": rel, "reason": format!("read error: {e}")}));
                }
            }
            if batch_bytes >= MAX_INGEST_BATCH_BYTES {
                flush_ingest(ctx, &mut batch, &mut totals, &mut skipped)?;
                batch_bytes = 0;
            }
        }
        flush_ingest(ctx, &mut batch, &mut totals, &mut skipped)?;

        // Reconcile: drop every indexed path this build did not keep (sorted
        // for determinism; embeddings cascade with their chunks).
        let mut drops = 0usize;
        {
            let index = ctx.index.as_mut().ok_or("index not open")?;
            let stale: Vec<String> = index
                .indexed_paths()
                .map_err(index_err)?
                .into_iter()
                .filter(|path| !keep.contains(path))
                .collect();
            let mut deletes: Vec<IndexChange> = Vec::new();
            for path in stale {
                skipped.push(json!({"path": path, "reason": "dropped from index: not in build set"}));
                deletes.push(IndexChange::Delete { path });
                drops += 1;
            }
            flush_update(ctx, &mut deletes, &mut totals, &mut skipped)?;
        }

        let result = totals.to_json(drops, skipped, &index_db_path(&state_dir, &ctx.workspace_root));
        record_index_status(ctx, &session_id, "index_build_completed", &result, started, &decision)?;
        Ok(result)
    }

    /// Applies caller-reported changes (patch apply / rollback outcomes) with
    /// the same per-path FileRead gating as build. Changed paths that no
    /// longer exist on disk are treated as deletes — and so are paths whose
    /// FileRead verdict is no longer Allowed (or that exceed the size cap):
    /// the one thing an update must never do is keep stale content of a file
    /// the session can no longer read queryable.
    fn index_update(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let changed = str_array_param(p, "changed_paths")?;
        let deleted = match p.get("deleted_paths") {
            Some(_) => str_array_param(p, "deleted_paths")?,
            None => Vec::new(),
        };
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;
        let started = std::time::Instant::now();

        let decision = index_gate(
            ctx,
            &session_id,
            format!("index update changed={} deleted={}", changed.len(), deleted.len()),
        )?;
        ensure_index(&state_dir, ctx)?;

        let mut totals = IngestTotals::default();
        let mut skipped: Vec<Value> = Vec::new();
        let mut drops = 0usize;
        let mut batch: Vec<IndexChange> = Vec::new();
        let mut batch_bytes = 0usize;
        for raw in &changed {
            let (rel, abs) = match rel_and_abs(&ctx.workspace_root, raw) {
                Ok(pair) => pair,
                Err(reason) => {
                    skipped.push(json!({"path": raw, "reason": reason}));
                    continue;
                }
            };
            let d = ctx.kernel.request_file_read(&abs.to_string_lossy(), None).map_err(err_str)?;
            if d.decision != Verdict::Allowed {
                // Now denied: drop the stale rows (mirror of the patch hook).
                skipped.push(json!({"path": rel, "reason": format!("dropped from index: {}", d.reason)}));
                batch.push(IndexChange::Delete { path: rel });
                drops += 1;
                continue;
            }
            if let Some(reason) = exceeds_size_cap(&abs) {
                skipped.push(json!({"path": rel, "reason": format!("dropped from index: {reason}")}));
                batch.push(IndexChange::Delete { path: rel });
                drops += 1;
                continue;
            }
            match std::fs::read_to_string(&abs) {
                Ok(content) => {
                    batch_bytes += content.len();
                    batch.push(IndexChange::Upsert { path: rel, content });
                }
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                    // Gone from disk: the change is a delete.
                    batch.push(IndexChange::Delete { path: rel });
                    drops += 1;
                }
                Err(e) => {
                    // Still present and still FileRead-allowed, but unreadable
                    // (permission denied, non-UTF-8 InvalidData, ...): a read
                    // failure is not a deletion. Keeping the previously
                    // ingested rows is not a policy leak; silently deleting on
                    // a transient error would lose index coverage.
                    skipped.push(json!({
                        "path": rel,
                        "reason": format!("read error (kept indexed rows): {e}"),
                    }));
                }
            }
            if batch_bytes >= MAX_INGEST_BATCH_BYTES {
                flush_update(ctx, &mut batch, &mut totals, &mut skipped)?;
                batch_bytes = 0;
            }
        }
        for raw in &deleted {
            match rel_and_abs(&ctx.workspace_root, raw) {
                Ok((rel, _)) => {
                    batch.push(IndexChange::Delete { path: rel });
                    drops += 1;
                }
                Err(reason) => skipped.push(json!({"path": raw, "reason": reason})),
            }
        }
        flush_update(ctx, &mut batch, &mut totals, &mut skipped)?;

        // Deleted files count into "indexed" as drops (RPC contract).
        let result = totals.to_json(drops, skipped, &index_db_path(&state_dir, &ctx.workspace_root));
        record_index_status(ctx, &session_id, "index_update_completed", &result, started, &decision)?;
        Ok(result)
    }

    fn index_search(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let query = str_param(p, "query")?;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;

        index_gate(ctx, &session_id, format!("index search {}", truncate_chars(&query, 200)))?;
        let mut opts = carina_index::SearchOptions::default();
        if let Some(limit) = p.get("limit").and_then(Value::as_u64) {
            opts.limit = limit as usize;
        }
        if let Some(lang) = p.get("lang").and_then(Value::as_str) {
            opts.lang = Some(
                serde_json::from_value(json!(lang)).map_err(|e| format!("invalid lang: {e}"))?,
            );
        }
        if let Some(prefix) = p.get("path_prefix").and_then(Value::as_str) {
            opts.path_prefix = Some(prefix.to_string());
        }
        // Optional third (cosine) RRF channel: without query_vector_base64
        // this method is bit-for-bit V1 two-way.
        if let Some(vec_b64) = p.get("query_vector_base64").and_then(Value::as_str) {
            let model_id = p
                .get("model_id")
                .and_then(Value::as_str)
                .ok_or("model_id is required when query_vector_base64 is set")?;
            let bytes = base64_decode(vec_b64).ok_or("query_vector_base64 is not valid base64")?;
            if bytes.is_empty() || bytes.len() % 4 != 0 {
                return Err(format!(
                    "query_vector_base64 must decode to a non-empty multiple of 4 bytes (f32-LE), got {} bytes",
                    bytes.len()
                ));
            }
            let query_vector = f32_from_le_bytes(&bytes);
            // D1: the crate's finiteness chokepoint is embed_store, which
            // search never crosses — reject NaN/±Inf here, before the search
            // runs (a non-finite component poisons every cosine comparison).
            if !query_vector.iter().all(|v| v.is_finite()) {
                return Err("query_vector_base64 contains a non-finite component".into());
            }
            opts.query_vector = Some(query_vector);
            opts.model_id = Some(model_id.to_string());
        }
        let index = existing_index(&state_dir, ctx)?;
        let hits = index.search(&query, &opts).map_err(index_err)?;
        let mut result = json!({"hits": hits});
        // Vector-channel liveness counters (V3 observable degrade): a caller
        // claiming "semantic:on" must be able to tell whether the cosine
        // channel had live, dims-matching vectors to rank — counts only,
        // never content.
        if let (Some(query_vector), Some(model_id)) = (&opts.query_vector, &opts.model_id) {
            let stats = index
                .embedding_stats(model_id, query_vector.len())
                .map_err(index_err)?;
            result["vector_channel"] = json!(stats);
        }
        Ok(result)
    }

    /// Chunks lacking an embedding for `model_id` (docs/plans/
    /// code-intelligence.md V2). Policy-gated content egress: raw chunk text
    /// leaves the kernel here, exactly like search snippets — hence the same
    /// query-time gate.
    fn index_pending_chunks(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let model_id = str_param(p, "model_id")?;
        let limit = p
            .get("limit")
            .and_then(Value::as_u64)
            .unwrap_or(DEFAULT_PENDING_CHUNKS)
            .min(MAX_EMBED_BATCH) as usize;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;

        index_gate(ctx, &session_id, format!("index pending_chunks model={model_id} limit={limit}"))?;
        let index = existing_index(&state_dir, ctx)?;
        let (chunks, total_pending) = index.pending_chunks(&model_id, limit).map_err(index_err)?;
        Ok(json!({"chunks": chunks, "total_pending": total_pending}))
    }

    /// Stores caller-supplied vectors for chunk ids (the kernel never computes
    /// or fetches embeddings — zero network I/O). Vectors are derived from
    /// content, so the write is gated like every other index surface. Stale
    /// ids (chunk replaced/deleted since pending_chunks) are success, not
    /// error — the caller just re-syncs.
    fn index_embed_store(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let model_id = str_param(p, "model_id")?;
        let dims = p.get("dims").and_then(Value::as_u64).ok_or("dims is required")? as usize;
        let embeddings_param = p
            .get("embeddings")
            .and_then(Value::as_array)
            .cloned()
            .ok_or("embeddings array required")?;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;
        let started = std::time::Instant::now();

        let decision = index_gate(
            ctx,
            &session_id,
            format!("index embed_store model={model_id} chunks={}", embeddings_param.len()),
        )?;
        if dims < 1 || dims > MAX_EMBED_DIMS {
            return Err(format!("dims must be in 1..={MAX_EMBED_DIMS}, got {dims}"));
        }
        if embeddings_param.len() as u64 > MAX_EMBED_BATCH {
            return Err(format!(
                "at most {MAX_EMBED_BATCH} embeddings per call, got {}",
                embeddings_param.len()
            ));
        }
        let mut items: Vec<ChunkEmbedding> = Vec::with_capacity(embeddings_param.len());
        for e in &embeddings_param {
            let chunk_id = e.get("chunk_id").and_then(Value::as_i64).ok_or("embedding.chunk_id required")?;
            let content_hash = e
                .get("content_hash")
                .and_then(Value::as_str)
                .ok_or("embedding.content_hash required")?
                .to_string();
            let vec_b64 = e
                .get("vector_base64")
                .and_then(Value::as_str)
                .ok_or("embedding.vector_base64 required")?;
            let bytes = base64_decode(vec_b64)
                .ok_or_else(|| format!("vector_base64 is not valid base64 (chunk {chunk_id})"))?;
            if bytes.len() != dims * 4 {
                return Err(format!(
                    "vector for chunk {chunk_id} decodes to {} bytes, expected dims * 4 = {} bytes",
                    bytes.len(),
                    dims * 4
                ));
            }
            items.push(ChunkEmbedding { chunk_id, content_hash, vector: f32_from_le_bytes(&bytes) });
        }

        let index = existing_index(&state_dir, ctx)?;
        let report = index.embed_store(&model_id, &items).map_err(index_err)?;
        let (_, total_pending) = index.pending_chunks(&model_id, 0).map_err(index_err)?;

        // Decision-linked completion status event (same idiom as
        // index_build_completed) — no new EventType variants.
        let event = Event::new(
            &session_id,
            EventType::ToolApproved,
            json!({
                "status": "index_embed_completed",
                "model_id": model_id,
                "stored": report.stored,
                "stale": report.stale.len(),
                "duration_ms": started.elapsed().as_millis() as u64,
            }),
        )
        .with_decision(&decision.decision_id);
        ctx.kernel.record_event(&event).map_err(err_str)?;

        Ok(json!({
            "stored": report.stored,
            "stale": report.stale,
            "total_pending": total_pending,
        }))
    }

    /// Persists LSP-sourced precise edges (docs/plans/code-intelligence.md
    /// V4 §B), gated and audited like its siblings: resource
    /// `index edges_store edges=<n>`, at most MAX_EDGES_STORE_BATCH edges,
    /// lines >= 1, both paths normalized via rel_and_abs AND re-gated through
    /// FileRead (denied -> skipped with the denial reason), endpoints must
    /// resolve to indexed symbols, and completion records a decision-linked
    /// `index_edges_stored` status event.
    fn index_edges_store(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let edges_param = p
            .get("edges")
            .and_then(Value::as_array)
            .cloned()
            .ok_or("edges array required")?;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;
        let started = std::time::Instant::now();

        let decision = index_gate(
            ctx,
            &session_id,
            format!("index edges_store edges={}", edges_param.len()),
        )?;
        if edges_param.len() as u64 > MAX_EDGES_STORE_BATCH {
            return Err(format!(
                "at most {MAX_EDGES_STORE_BATCH} edges per call, got {}",
                edges_param.len()
            ));
        }
        // Parse strictly (param errors), then normalize + re-gate per
        // endpoint. A denied or non-workspace path is a *skip*, never a
        // stored relation: an edge must not smuggle a relation about content
        // the session cannot read.
        let mut specs: Vec<carina_index::EdgeSpec> = Vec::with_capacity(edges_param.len());
        let mut skipped: Vec<Value> = Vec::new();
        for e in &edges_param {
            let src_path = e.get("src_path").and_then(Value::as_str).ok_or("edge.src_path required")?;
            let dst_path = e.get("dst_path").and_then(Value::as_str).ok_or("edge.dst_path required")?;
            let src_line = e.get("src_line").and_then(Value::as_u64).ok_or("edge.src_line required")?;
            let dst_line = e.get("dst_line").and_then(Value::as_u64).ok_or("edge.dst_line required")?;
            if src_line < 1 || dst_line < 1 || src_line > u32::MAX as u64 || dst_line > u32::MAX as u64 {
                return Err("edge line numbers must be 1-based (>= 1)".into());
            }
            // Both endpoints re-pass the FileRead gate exactly like build/
            // update; the skip carries the denial reason (audited as usual by
            // the request itself).
            let mut endpoints_ok = true;
            let mut rels: [String; 2] = [String::new(), String::new()];
            for (i, raw) in [src_path, dst_path].iter().enumerate() {
                match rel_and_abs(&ctx.workspace_root, raw) {
                    Ok((rel, abs)) => {
                        let d = ctx.kernel.request_file_read(&abs.to_string_lossy(), None).map_err(err_str)?;
                        if d.decision != Verdict::Allowed {
                            skipped.push(json!({
                                "src_path": src_path,
                                "dst_path": dst_path,
                                "reason": format!("{raw}: {}", d.reason),
                            }));
                            endpoints_ok = false;
                            break;
                        }
                        rels[i] = rel;
                    }
                    Err(reason) => {
                        skipped.push(json!({
                            "src_path": src_path,
                            "dst_path": dst_path,
                            "reason": format!("{raw}: {reason}"),
                        }));
                        endpoints_ok = false;
                        break;
                    }
                }
            }
            if !endpoints_ok {
                continue;
            }
            let [src_rel, dst_rel] = rels;
            specs.push(carina_index::EdgeSpec {
                src_path: src_rel,
                src_line: src_line as u32,
                dst_path: dst_rel,
                dst_line: dst_line as u32,
            });
        }

        // Endpoints must already resolve to indexed symbols — the edges
        // store never ingests (the index stays a derived artifact).
        let index = existing_index(&state_dir, ctx)?;
        let report = index.edges_store(&specs).map_err(index_err)?;
        for s in &report.skipped {
            skipped.push(serde_json::to_value(s).map_err(err_str)?);
        }

        // Decision-linked completion status event (same idiom as
        // index_embed_completed) — no new EventType variants.
        let event = Event::new(
            &session_id,
            EventType::ToolApproved,
            json!({
                "status": "index_edges_stored",
                "stored": report.stored,
                "skipped": skipped.len(),
                "duration_ms": started.elapsed().as_millis() as u64,
            }),
        )
        .with_decision(&decision.decision_id);
        ctx.kernel.record_event(&event).map_err(err_str)?;

        Ok(json!({"stored": report.stored, "skipped": skipped}))
    }

    fn index_symbols(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let name = str_param(p, "name")?;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;

        index_gate(ctx, &session_id, format!("index symbols {name}"))?;
        let mut opts = carina_index::SymbolOptions::default();
        if let Some(kind) = p.get("kind").and_then(Value::as_str) {
            opts.kind = Some(
                serde_json::from_value(json!(kind)).map_err(|e| format!("invalid kind: {e}"))?,
            );
        }
        if let Some(include) = p.get("include_refs").and_then(Value::as_bool) {
            opts.include_references = include;
        }
        if let Some(limit) = p.get("limit").and_then(Value::as_u64) {
            opts.limit = limit as usize;
        }
        let index = existing_index(&state_dir, ctx)?;
        let report = index.symbol_lookup(&name, &opts).map_err(index_err)?;
        serde_json::to_value(&report).map_err(err_str)
    }

    /// Transitive dependents of a symbol name over the edges graph
    /// (docs/plans/code-intelligence.md V3) — index_gate'd and audited like
    /// search/symbols (query idiom: no extra status event). max_depth clamps
    /// to 1..=5 (default 3), limit to 1..=200 (default 50); the clamped
    /// values appear in the audited resource string.
    fn index_impact(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let name = str_param(p, "name")?;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;

        let mut opts = carina_index::ImpactOptions::default();
        if let Some(depth) = p.get("max_depth").and_then(Value::as_u64) {
            opts.max_depth = depth as usize;
        }
        if let Some(limit) = p.get("limit").and_then(Value::as_u64) {
            opts.limit = limit as usize;
        }
        // Clamp before the gate so the audited resource string carries the
        // effective (documented) bounds, making the clamp itself observable.
        opts.max_depth = opts.max_depth.clamp(1, 5);
        opts.limit = opts.limit.clamp(1, 200);

        index_gate(
            ctx,
            &session_id,
            format!(
                "index impact {} depth={} limit={}",
                truncate_chars(&name, 200),
                opts.max_depth,
                opts.limit
            ),
        )?;
        let index = existing_index(&state_dir, ctx)?;
        let report = index.impact(&name, &opts).map_err(index_err)?;
        serde_json::to_value(&report).map_err(err_str)
    }

    fn index_map(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let state_dir = self.state_dir.clone();
        let ctx = self.ctx(p)?;

        let mut opts = carina_index::RepoMapOptions::default();
        if let Some(budget) = p.get("token_budget").and_then(Value::as_u64) {
            opts.token_budget = budget as usize;
        }
        if let Some(focus) = p.get("focus_paths").and_then(Value::as_array) {
            opts.focus_paths = focus.iter().filter_map(Value::as_str).map(String::from).collect();
        }
        index_gate(ctx, &session_id, format!("index map budget={}", opts.token_budget))?;
        let index = existing_index(&state_dir, ctx)?;
        let map = index.repo_map(&opts).map_err(index_err)?;
        Ok(json!({
            "map": map.text,
            "ranked": map.ranked,
            "token_estimate": map.token_estimate,
        }))
    }
}

// ---- code-index helpers ----------------------------------------------------

/// Per-file ingestion cap (matches the Zig scanner's 5 MiB limit): a single
/// multi-GB source file must not be read whole into the kernel process.
const MAX_INDEX_FILE_BYTES: u64 = 5 * 1024 * 1024;
/// Flush threshold for batched ingestion: at most this many content bytes are
/// buffered before they are handed to the index and released.
const MAX_INGEST_BATCH_BYTES: usize = 16 * 1024 * 1024;
/// pending_chunks default batch size (docs/plans/code-intelligence.md V2).
const DEFAULT_PENDING_CHUNKS: u64 = 64;
/// Cap on pending_chunks `limit` and on embeddings per embed_store call.
const MAX_EMBED_BATCH: u64 = 256;
/// Cap on edges per edges_store call (the MAX_EMBED_BATCH idiom).
const MAX_EDGES_STORE_BATCH: u64 = 256;
/// Accepted embedding dimensionality range (covers every BYOK catalog model).
const MAX_EMBED_DIMS: usize = 4096;

/// Decodes f32 little-endian bytes (dims * 4) into a vector; callers validate
/// the length, so `chunks_exact` never drops meaningful trailing bytes.
fn f32_from_le_bytes(bytes: &[u8]) -> Vec<f32> {
    bytes
        .chunks_exact(4)
        .map(|b| f32::from_le_bytes([b[0], b[1], b[2], b[3]]))
        .collect()
}

/// One CodeIndex capability request through the ordinary `Kernel::request`
/// path, so approval modes, overlays, and org bundles apply unchanged and the
/// decision lands in the hash chain. Anything but Allowed is an error. A
/// requires_approval decision is parked in `pending` (one per capability —
/// retries reuse it, so the set stays bounded) and its decision_id is carried
/// in the error so the control plane can `kernel.approve` it and retry.
fn index_gate(ctx: &mut SessionCtx, session_id: &str, resource: String) -> Result<Decision, String> {
    let request = CapabilityRequest {
        capability: Capability::CodeIndex,
        requested_by: Principal::Agent,
        resource,
        session_id: session_id.to_string(),
        task_id: None,
    };
    let decision = ctx.kernel.request(request).map_err(err_str)?;
    match decision.decision {
        Verdict::Allowed => Ok(decision),
        Verdict::RequiresApproval => {
            let reason = decision.reason.clone();
            let decision_id = match ctx
                .pending
                .values()
                .find(|d| d.capability == Capability::CodeIndex)
            {
                Some(existing) => existing.decision_id.clone(),
                None => {
                    let id = decision.decision_id.clone();
                    ctx.pending.insert(id.clone(), decision);
                    id
                }
            };
            Err(format!("code index requires approval (decision_id={decision_id}): {reason}"))
        }
        Verdict::Denied => Err(format!("code index denied: {}", decision.reason)),
    }
}

/// Running totals across batched ingest/update flushes.
#[derive(Default)]
struct IngestTotals {
    indexed: usize,
    unchanged: usize,
    symbols: usize,
    edges: usize,
    chunks: usize,
}

impl IngestTotals {
    fn add(&mut self, report: &carina_index::IngestReport) {
        self.indexed += report.indexed;
        self.unchanged += report.unchanged;
        self.symbols += report.symbols;
        self.edges += report.edges;
        self.chunks += report.chunks;
    }

    fn to_json(&self, drops: usize, skipped: Vec<Value>, db_path: &Path) -> Value {
        json!({
            "indexed": self.indexed + drops,
            "unchanged": self.unchanged,
            "skipped": skipped,
            "symbols": self.symbols,
            "edges": self.edges,
            "chunks": self.chunks,
            "db_path": db_path.to_string_lossy(),
        })
    }
}

/// Ingests and drains one buffered batch (no-op when empty). The session's
/// index must already be open (`ensure_index`).
fn flush_ingest(
    ctx: &mut SessionCtx,
    batch: &mut Vec<IngestFile>,
    totals: &mut IngestTotals,
    skipped: &mut Vec<Value>,
) -> Result<(), String> {
    if batch.is_empty() {
        return Ok(());
    }
    let index = ctx.index.as_mut().ok_or("index not open")?;
    let report = index.ingest(batch).map_err(index_err)?;
    batch.clear();
    totals.add(&report);
    skipped.extend(skipped_json(&report.skipped));
    Ok(())
}

/// Applies and drains one buffered change batch (no-op when empty).
fn flush_update(
    ctx: &mut SessionCtx,
    batch: &mut Vec<IndexChange>,
    totals: &mut IngestTotals,
    skipped: &mut Vec<Value>,
) -> Result<(), String> {
    if batch.is_empty() {
        return Ok(());
    }
    let index = ctx.index.as_mut().ok_or("index not open")?;
    let report = index.update(batch).map_err(index_err)?;
    batch.clear();
    totals.add(&report);
    skipped.extend(skipped_json(&report.skipped));
    Ok(())
}

/// Reason string when `abs` is over the per-file ingestion cap, None when it
/// fits (or cannot be stat'ed — the read itself reports that error).
fn exceeds_size_cap(abs: &Path) -> Option<String> {
    let len = std::fs::metadata(abs).map(|m| m.len()).unwrap_or(0);
    (len > MAX_INDEX_FILE_BYTES).then(|| {
        format!("file exceeds index size cap ({len} > {MAX_INDEX_FILE_BYTES} bytes)")
    })
}

/// The per-workspace index database path:
/// <state_dir>/index/<sha256(workspace_root)>.sqlite.
fn index_db_path(state_dir: &Path, workspace_root: &Path) -> PathBuf {
    let mut hasher = Sha256::new();
    hasher.update(workspace_root.to_string_lossy().as_bytes());
    state_dir.join("index").join(format!("{:x}.sqlite", hasher.finalize()))
}

/// Opens (creating if needed) the session's index database.
fn ensure_index<'a>(state_dir: &Path, ctx: &'a mut SessionCtx) -> Result<&'a mut CodeIndex, String> {
    if ctx.index.is_none() {
        let db = index_db_path(state_dir, &ctx.workspace_root);
        if let Some(parent) = db.parent() {
            std::fs::create_dir_all(parent).map_err(err_str)?;
        }
        ctx.index = Some(CodeIndex::open(&db).map_err(index_err)?);
    }
    Ok(ctx.index.as_mut().expect("index just opened"))
}

/// Opens the session's index database only if it already exists; query
/// methods must not silently return empty results from a never-built index.
fn existing_index<'a>(state_dir: &Path, ctx: &'a mut SessionCtx) -> Result<&'a mut CodeIndex, String> {
    if ctx.index.is_none() && !index_db_path(state_dir, &ctx.workspace_root).exists() {
        return Err("index not built — call kernel.index.build first".into());
    }
    ensure_index(state_dir, ctx)
}

/// Normalizes a candidate path to (workspace-relative key, absolute path).
/// The relative key is what the index stores; the absolute path is what the
/// FileRead policy evaluates. Paths that cannot be expressed relative to the
/// workspace root are rejected (they could never have been read in-workspace).
fn rel_and_abs(root: &Path, raw: &str) -> Result<(String, PathBuf), String> {
    let candidate = Path::new(raw);
    let abs = if candidate.is_absolute() {
        normalize_lexical(candidate)
    } else {
        normalize_lexical(&root.join(candidate))
    };
    let rel = abs
        .strip_prefix(root)
        .map_err(|_| "path is not workspace-relative".to_string())?
        .to_string_lossy()
        .replace('\\', "/");
    if rel.is_empty() {
        return Err("path names the workspace root".into());
    }
    Ok((rel, abs))
}

/// Lexical `.`/`..` normalization (no filesystem access); the policy layer
/// does its own symlink-aware resolution for the containment decision.
fn normalize_lexical(path: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for comp in path.components() {
        match comp {
            Component::ParentDir => {
                out.pop();
            }
            Component::CurDir => {}
            other => out.push(other),
        }
    }
    out
}

fn skipped_json(skipped: &[carina_index::SkippedFile]) -> Vec<Value> {
    skipped
        .iter()
        .map(|s| json!({"path": s.path, "reason": s.reason}))
        .collect()
}

/// Records the decision-linked build/update status event (same idiom as the
/// approval_overlay_created status event) — no new EventType variants.
fn record_index_status(
    ctx: &SessionCtx,
    session_id: &str,
    status: &str,
    result: &Value,
    started: std::time::Instant,
    decision: &Decision,
) -> Result<(), String> {
    let event = Event::new(
        session_id,
        EventType::ToolApproved,
        json!({
            "status": status,
            "indexed": result["indexed"],
            "unchanged": result["unchanged"],
            "skipped": result["skipped"].as_array().map(Vec::len).unwrap_or(0),
            "symbols": result["symbols"],
            "edges": result["edges"],
            "chunks": result["chunks"],
            "duration_ms": started.elapsed().as_millis() as u64,
        }),
    )
    .with_decision(&decision.decision_id);
    ctx.kernel.record_event(&event).map_err(err_str)
}

/// Best-effort index invalidation after a successful patch apply/rollback.
/// Runs only when an index already exists for the workspace; every re-read is
/// still FileRead-gated (a path that became denied is dropped, never leaked),
/// and an index failure never fails the patch — but it is no longer silent
/// (V4 D4): a failed open/update records a decision-free
/// `index_invalidation_failed` status event (error text, never content) so a
/// stale index stays visible until the next sweep/build heals it.
fn invalidate_index_after_patch(
    state_dir: &Path,
    ctx: &mut SessionCtx,
    session_id: &str,
    files: &[FileChange],
) {
    if ctx.index.is_none() && !index_db_path(state_dir, &ctx.workspace_root).exists() {
        return;
    }
    let mut changes: Vec<IndexChange> = Vec::new();
    for f in files {
        let (rel, abs) = match rel_and_abs(&ctx.workspace_root, &f.path) {
            Ok(pair) => pair,
            Err(_) => continue,
        };
        let allowed = ctx
            .kernel
            .request_file_read(&abs.to_string_lossy(), None)
            .map(|d| d.decision == Verdict::Allowed)
            .unwrap_or(false)
            && exceeds_size_cap(&abs).is_none();
        // Short-circuit so a denied/oversized path is never read into memory.
        match allowed.then(|| std::fs::read_to_string(&abs)) {
            Some(Ok(content)) => changes.push(IndexChange::Upsert { path: rel, content }),
            _ => changes.push(IndexChange::Delete { path: rel }),
        }
    }
    let failure = match ensure_index(state_dir, ctx) {
        Ok(index) => index.update(&changes).map_err(index_err).err(),
        Err(e) => Some(e),
    };
    if let Some(error) = failure {
        // The patch itself already succeeded; surface the stale index in the
        // audit chain. A failing record_event has nowhere further to report.
        let _ = ctx.kernel.record_event(&Event::new(
            session_id,
            EventType::ToolApproved,
            json!({"status": "index_invalidation_failed", "error": error}),
        ));
    }
}

fn index_err(e: carina_index::IndexError) -> String {
    format!("index error: {e}")
}

/// Truncates to at most `max` characters on a char boundary.
fn truncate_chars(s: &str, max: usize) -> String {
    s.chars().take(max).collect()
}

fn str_array_param(p: &Value, key: &str) -> Result<Vec<String>, String> {
    let arr = p
        .get(key)
        .and_then(Value::as_array)
        .ok_or_else(|| format!("{key} array required"))?;
    arr.iter()
        .map(|v| {
            // Element-wise strictness: silently dropping a non-string element
            // would operate on a different path set than the caller named.
            v.as_str()
                .map(String::from)
                .ok_or_else(|| format!("{key} must be an array of strings"))
        })
        .collect()
}

enum Pre {
    Old,
    New,
}

fn combined_hash(files: &[FileChange], which: Pre) -> String {
    let mut buf = Vec::new();
    for f in files {
        buf.extend_from_slice(f.path.as_bytes());
        buf.push(0);
        match which {
            Pre::Old => buf.extend_from_slice(&f.old_content),
            Pre::New => buf.extend_from_slice(f.new_content.as_bytes()),
        }
        buf.push(0);
    }
    content_hash(&buf)
}

/// Minimal human-readable diff (PRD §8.4). Line-based; Myers diff later.
fn render_diff(files: &[FileChange]) -> String {
    let mut out = String::new();
    for f in files {
        out.push_str(&format!("--- a/{}\n+++ b/{}\n", f.path, f.path));
        let old = String::from_utf8_lossy(&f.old_content);
        let old_lines: Vec<&str> = old.lines().collect();
        let new_lines: Vec<&str> = f.new_content.lines().collect();
        let common = old_lines.len().min(new_lines.len());
        for i in 0..common {
            if old_lines[i] != new_lines[i] {
                out.push_str(&format!("-{}\n+{}\n", old_lines[i], new_lines[i]));
            }
        }
        for line in old_lines.iter().skip(common) {
            out.push_str(&format!("-{line}\n"));
        }
        for line in new_lines.iter().skip(common) {
            out.push_str(&format!("+{line}\n"));
        }
    }
    out
}

/// Builds the JSON plan consumed by carina-patch-native.
fn build_patch_plan(root: &Path, files: &[FileChange], snapshot_dir: &Path) -> Value {
    let items: Vec<Value> = files
        .iter()
        .enumerate()
        .map(|(i, c)| {
            json!({
                "path": root.join(&c.path).to_string_lossy(),
                "new_content": c.new_content,
                "snapshot": snapshot_dir.join(format!("{i}.pre")).to_string_lossy(),
                "existed": c.existed,
            })
        })
        .collect();
    json!({ "files": items })
}

/// Locates the carina-patch-native binary. There is NO Rust write fallback: if
/// the native tool is missing, patch apply fails (PRD §4.4, §16.5).
fn patch_native_bin() -> Result<PathBuf, String> {
    if let Ok(p) = std::env::var("CARINA_PATCH_NATIVE_BIN") {
        return Ok(PathBuf::from(p));
    }
    if let Ok(dir) = std::env::var("CARINA_TOOLS_DIR") {
        let candidate = Path::new(&dir).join("carina-patch-native");
        if candidate.exists() {
            return Ok(candidate);
        }
    }
    Err("carina-patch-native not found (set CARINA_TOOLS_DIR or CARINA_PATCH_NATIVE_BIN); refusing to write directly".into())
}

/// Runs carina-patch-native with a plan on stdin and returns its reported status.
fn run_patch_native(subcmd: &str, plan: &Value) -> Result<String, String> {
    let bin = patch_native_bin()?;
    let mut child = Command::new(&bin)
        .arg(subcmd)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("spawn carina-patch-native: {e}"))?;
    {
        let stdin = child.stdin.as_mut().ok_or("carina-patch-native: no stdin")?;
        stdin.write_all(plan.to_string().as_bytes()).map_err(err_str)?;
    }
    let out = child.wait_with_output().map_err(err_str)?;
    let stdout = String::from_utf8_lossy(&out.stdout);
    let last = stdout.lines().last().unwrap_or("");
    let v: Value = serde_json::from_str(last).map_err(|_| format!("carina-patch-native bad output: {stdout}"))?;
    v.get("status")
        .and_then(Value::as_str)
        .map(String::from)
        .ok_or_else(|| format!("carina-patch-native error: {stdout}"))
}

fn str_param(p: &Value, key: &str) -> Result<String, String> {
    p.get(key)
        .and_then(Value::as_str)
        .map(String::from)
        .ok_or_else(|| format!("{key} is required"))
}

fn err_str<E: std::fmt::Display>(e: E) -> String {
    e.to_string()
}

fn error_response(id: Value, code: i64, message: &str) -> String {
    json!({"jsonrpc": "2.0", "id": id, "error": {"code": code, "message": message}}).to_string()
}

/// Standard base64 decode (no padding requirement). Kept dependency-free.
fn base64_decode(input: &str) -> Option<Vec<u8>> {
    const TABLE: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut lookup = [255u8; 256];
    for (i, &c) in TABLE.iter().enumerate() {
        lookup[c as usize] = i as u8;
    }
    let mut out = Vec::new();
    let mut buf = 0u32;
    let mut bits = 0u32;
    for &c in input.as_bytes() {
        if c == b'=' || c == b'\n' || c == b'\r' {
            continue;
        }
        let v = lookup[c as usize];
        if v == 255 {
            return None;
        }
        buf = (buf << 6) | v as u32;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((buf >> bits) as u8);
        }
    }
    Some(out)
}

// Unused import guard: KernelError is part of the public surface we exercise.
#[allow(dead_code)]
fn _assert_error_type(e: KernelError) -> String {
    e.to_string()
}
