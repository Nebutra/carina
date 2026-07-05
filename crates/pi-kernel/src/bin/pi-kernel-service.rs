//! pi-kernel-service — the Capability Kernel as a process.
//!
//! The Go control plane spawns this binary and speaks JSON-RPC 2.0 over
//! stdio (PRD §15.1). Every side effect in the runtime funnels through
//! here: capability decisions, approvals, the append-only event log, and
//! transactional patch apply/rollback. The kernel is the single writer of
//! the audit log so the control plane cannot bypass it.

use pi_audit::{Actor, Event, EventType};
use pi_kernel::{ApprovalPolicy, Kernel, KernelError};
use pi_patch::{content_hash, PatchTransaction};
use pi_policy::{ApprovalMode, Capability, CapabilityRequest, Decision, PolicyBundle, Principal, Profile, Verdict};
use serde_json::{json, Value};
use std::collections::HashMap;
use std::io::{BufRead, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

fn main() {
    let state_dir = std::env::args()
        .nth(1)
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(".pi-os-state"));
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
            "ping" => Ok(json!({"ok": true})),
            "kernel.session.init" => self.session_init(p),
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
            "kernel.classify" => {
                let cmd = str_param(p, "command")?;
                Ok(json!({"command": cmd, "risk_level": pi_policy::classify_command(&cmd)}))
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
            },
        );
        Ok(json!({"session_id": session_id, "profile": profile_name}))
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
        let manifest = pi_plugin_runtime::Manifest::from_toml(&manifest_toml).map_err(err_str)?;
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
        let manifest = pi_plugin_runtime::Manifest::from_toml(&manifest_toml).map_err(err_str)?;
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
        let ctx = self.ctx(p)?;
        let pending = ctx
            .pending
            .remove(&decision_id)
            .ok_or_else(|| format!("no pending decision {decision_id}"))?;
        let approved = if for_session && role.is_none() {
            ctx.kernel.approve_for_session(&pending, &approver).map_err(err_str)?
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
            if !pi_policy::path_within_workspace(&ctx.workspace_root, &abs) {
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

        // Delegate the actual disk write to the Zig pi-patch-native tool
        // (PRD §4.4: file mutation runs in the native toolchain, not Rust).
        // The tool applies atomically (all-or-nothing) and restores on
        // failure; if the tool is unavailable we fail rather than writing
        // directly, so Zig is the only patch-apply path (PRD §16.5).
        let plan = build_patch_plan(&ctx.workspace_root, &record.files, &record.snapshot_dir);
        match run_patch_native("apply", &plan) {
            Ok(status) if status == "applied" => {}
            outcome => {
                let reason = match outcome {
                    Ok(s) => format!("pi-patch-native reported '{s}', expected 'applied'"),
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

        let result = serde_json::to_value(&tx).map_err(err_str)?;
        ctx.patches.insert(patch_id, PatchRecord { tx, files: record.files, snapshot_dir: record.snapshot_dir });
        Ok(result)
    }

    fn patch_rollback(&mut self, p: &Value) -> Result<Value, String> {
        let session_id = str_param(p, "session_id")?;
        let patch_id = str_param(p, "patch_id")?;
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
                    Ok(s) => format!("pi-patch-native reported '{s}'"),
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

/// Builds the JSON plan consumed by pi-patch-native.
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

/// Locates the pi-patch-native binary. There is NO Rust write fallback: if
/// the native tool is missing, patch apply fails (PRD §4.4, §16.5).
fn patch_native_bin() -> Result<PathBuf, String> {
    if let Ok(p) = std::env::var("PI_PATCH_NATIVE_BIN") {
        return Ok(PathBuf::from(p));
    }
    if let Ok(dir) = std::env::var("PI_TOOLS_DIR") {
        let candidate = Path::new(&dir).join("pi-patch-native");
        if candidate.exists() {
            return Ok(candidate);
        }
    }
    Err("pi-patch-native not found (set PI_TOOLS_DIR or PI_PATCH_NATIVE_BIN); refusing to write directly".into())
}

/// Runs pi-patch-native with a plan on stdin and returns its reported status.
fn run_patch_native(subcmd: &str, plan: &Value) -> Result<String, String> {
    let bin = patch_native_bin()?;
    let mut child = Command::new(&bin)
        .arg(subcmd)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("spawn pi-patch-native: {e}"))?;
    {
        let stdin = child.stdin.as_mut().ok_or("pi-patch-native: no stdin")?;
        stdin.write_all(plan.to_string().as_bytes()).map_err(err_str)?;
    }
    let out = child.wait_with_output().map_err(err_str)?;
    let stdout = String::from_utf8_lossy(&out.stdout);
    let last = stdout.lines().last().unwrap_or("");
    let v: Value = serde_json::from_str(last).map_err(|_| format!("pi-patch-native bad output: {stdout}"))?;
    v.get("status")
        .and_then(Value::as_str)
        .map(String::from)
        .ok_or_else(|| format!("pi-patch-native error: {stdout}"))
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
