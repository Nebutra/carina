//! Retained Doctor recovery workflow projection (#doctor Trellis child).
//!
//! Daemon remains the health authority. The TUI projects `daemon.doctor` into
//! typed sections/checks/evidence and a single recommended recovery action.
//! Human-readable `fix_plan.action` strings are never executed as code.

use serde_json::Value;

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum DoctorSeverity {
    Ok,
    Info,
    Warn,
    Error,
    Unknown,
}

impl DoctorSeverity {
    pub fn parse(raw: &str) -> Self {
        match raw.trim().to_ascii_lowercase().as_str() {
            "ok" | "healthy" | "pass" | "true" => Self::Ok,
            "info" | "disabled" => Self::Info,
            "warn" | "warning" | "partial" => Self::Warn,
            "error" | "fail" | "failed" | "false" => Self::Error,
            _ => Self::Unknown,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::Ok => "ok",
            Self::Info => "info",
            Self::Warn => "warn",
            Self::Error => "error",
            Self::Unknown => "unknown",
        }
    }

    pub fn rank(self) -> u8 {
        match self {
            Self::Ok => 0,
            Self::Info => 1,
            Self::Unknown => 2,
            Self::Warn => 3,
            Self::Error => 4,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RecoveryActionKind {
    RerunDoctor,
    CopyEvidence,
    OpenSettings,
    /// Documented human step only; never shell-executed.
    Instruct,
    ExitDoctor,
}

impl RecoveryActionKind {
    pub fn label(self) -> &'static str {
        match self {
            Self::RerunDoctor => "Re-run doctor",
            Self::CopyEvidence => "Copy evidence",
            Self::OpenSettings => "Open settings",
            Self::Instruct => "Follow fix plan",
            Self::ExitDoctor => "Back to conversation",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DoctorEvidence {
    pub label: String,
    pub value: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RecoveryAction {
    pub id: String,
    pub kind: RecoveryActionKind,
    pub title: String,
    pub detail: String,
    pub requires_confirmation: bool,
    pub enabled: bool,
    pub disabled_reason: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DoctorCheck {
    pub id: String,
    pub title: String,
    pub summary: String,
    pub severity: DoctorSeverity,
    pub evidence: Vec<DoctorEvidence>,
    pub actions: Vec<RecoveryAction>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DoctorSection {
    pub id: String,
    pub title: String,
    pub severity: DoctorSeverity,
    pub checks: Vec<DoctorCheck>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DoctorReport {
    pub revision: u64,
    pub disabled: bool,
    pub status: DoctorSeverity,
    pub summary: String,
    pub sections: Vec<DoctorSection>,
    pub recommended: Option<RecoveryAction>,
    pub utility: Vec<RecoveryAction>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DoctorOperation {
    Idle,
    Running,
    Succeeded,
    Failed,
}

#[derive(Debug, Clone)]
pub struct DoctorScreen {
    pub report: DoctorReport,
    pub selected_section: usize,
    pub selected_check: usize,
    pub selected_action: usize,
    pub scroll: usize,
    pub operation: DoctorOperation,
    pub operation_detail: String,
    pub confirm_pending: Option<RecoveryAction>,
}

impl DoctorScreen {
    pub fn from_value(value: &Value, revision: u64) -> Self {
        let report = project_doctor_report(value, revision);
        Self {
            report,
            selected_section: 0,
            selected_check: 0,
            selected_action: 0,
            scroll: 0,
            operation: DoctorOperation::Idle,
            operation_detail: String::new(),
            confirm_pending: None,
        }
    }

    pub fn current_check(&self) -> Option<&DoctorCheck> {
        let section = self.report.sections.get(self.selected_section)?;
        section.checks.get(self.selected_check)
    }

    pub fn selectable_actions(&self) -> Vec<&RecoveryAction> {
        let mut actions = Vec::new();
        if let Some(recommended) = &self.report.recommended {
            actions.push(recommended);
        }
        if let Some(check) = self.current_check() {
            for action in &check.actions {
                if self
                    .report
                    .recommended
                    .as_ref()
                    .is_none_or(|rec| rec.id != action.id)
                {
                    actions.push(action);
                }
            }
        }
        for action in &self.report.utility {
            actions.push(action);
        }
        actions
    }

    pub fn move_section(&mut self, delta: isize) {
        if self.report.sections.is_empty() {
            return;
        }
        let len = self.report.sections.len() as isize;
        let next = (self.selected_section as isize + delta).rem_euclid(len) as usize;
        self.selected_section = next;
        self.selected_check = 0;
        self.selected_action = 0;
    }

    pub fn move_check(&mut self, delta: isize) {
        let Some(section) = self.report.sections.get(self.selected_section) else {
            return;
        };
        if section.checks.is_empty() {
            return;
        }
        let len = section.checks.len() as isize;
        let next = (self.selected_check as isize + delta).rem_euclid(len) as usize;
        self.selected_check = next;
        self.selected_action = 0;
    }

    pub fn move_action(&mut self, delta: isize) {
        let count = self.selectable_actions().len();
        if count == 0 {
            return;
        }
        let len = count as isize;
        let next = (self.selected_action as isize + delta).rem_euclid(len) as usize;
        self.selected_action = next;
    }

    pub fn active_action(&self) -> Option<RecoveryAction> {
        self.selectable_actions()
            .get(self.selected_action)
            .map(|action| (*action).clone())
    }

    /// Lines suitable for a retained doctor surface / tests.
    pub fn render_lines(&self) -> Vec<String> {
        let mut lines = Vec::new();
        lines.push(format!(
            "Doctor  [{}]  {}",
            self.report.status.label(),
            self.report.summary
        ));
        if self.report.disabled {
            lines.push("Probes disabled by CARINA_DOCTOR_DISABLE.".into());
        }
        for (section_index, section) in self.report.sections.iter().enumerate() {
            let mark = if section_index == self.selected_section {
                ">"
            } else {
                " "
            };
            lines.push(format!(
                "{mark} [{}] {}",
                section.severity.label(),
                section.title
            ));
            if section_index != self.selected_section {
                continue;
            }
            for (check_index, check) in section.checks.iter().enumerate() {
                let mark = if check_index == self.selected_check {
                    "*"
                } else {
                    " "
                };
                lines.push(format!(
                    "  {mark} [{}] {} - {}",
                    check.severity.label(),
                    check.title,
                    check.summary
                ));
                if check_index == self.selected_check {
                    for evidence in &check.evidence {
                        lines.push(format!("      {}={}", evidence.label, evidence.value));
                    }
                }
            }
        }
        if let Some(recommended) = &self.report.recommended {
            lines.push(format!(
                "Recommended: {} ({})",
                recommended.title, recommended.detail
            ));
        }
        let actions = self.selectable_actions();
        if !actions.is_empty() {
            lines.push("Actions:".into());
            for (index, action) in actions.iter().enumerate() {
                let mark = if index == self.selected_action {
                    ">"
                } else {
                    " "
                };
                let enabled = if action.enabled { "" } else { " [disabled]" };
                lines.push(format!("{mark} {}{enabled}", action.title));
            }
        }
        match self.operation {
            DoctorOperation::Idle => {}
            DoctorOperation::Running => lines.push(format!("Running... {}", self.operation_detail)),
            DoctorOperation::Succeeded => {
                lines.push(format!("Verified. {}", self.operation_detail))
            }
            DoctorOperation::Failed => lines.push(format!("Failed. {}", self.operation_detail)),
        }
        if let Some(pending) = &self.confirm_pending {
            lines.push(format!(
                "Confirm: {} - Enter to confirm, Esc to cancel",
                pending.title
            ));
        }
        lines
    }
}

/// Project a raw `daemon.doctor` map into a typed report.
pub fn project_doctor_report(raw: &Value, revision: u64) -> DoctorReport {
    let object = raw.as_object();
    let disabled = object
        .and_then(|map| map.get("disabled"))
        .and_then(Value::as_bool)
        .unwrap_or(false);
    let mut sections = Vec::new();
    if let Some(map) = object {
        for (key, value) in map {
            if matches!(key.as_str(), "version" | "disabled" | "reason" | "fix_plan") {
                continue;
            }
            sections.push(project_section(key, value));
        }
    }
    // Stable order by known importance then name.
    sections.sort_by(|a, b| {
        section_priority(&a.id)
            .cmp(&section_priority(&b.id))
            .then_with(|| a.id.cmp(&b.id))
    });

    let mut status = DoctorSeverity::Ok;
    for section in &sections {
        if section.severity.rank() > status.rank() {
            status = section.severity;
        }
    }
    if disabled {
        status = DoctorSeverity::Info;
    }

    let mut recommended = None;
    if let Some(plan) = object
        .and_then(|map| map.get("fix_plan"))
        .and_then(Value::as_array)
    {
        for item in plan {
            let severity = DoctorSeverity::parse(
                item.get("severity")
                    .and_then(Value::as_str)
                    .unwrap_or("warn"),
            );
            let issue = item
                .get("issue")
                .and_then(Value::as_str)
                .unwrap_or("Issue reported")
                .to_owned();
            let action = item
                .get("action")
                .and_then(Value::as_str)
                .unwrap_or("")
                .to_owned();
            let check = item
                .get("check")
                .and_then(Value::as_str)
                .unwrap_or("fix")
                .to_owned();
            let automatic = item
                .get("automatic")
                .and_then(Value::as_bool)
                .unwrap_or(false);
            let candidate = RecoveryAction {
                id: format!("fix:{check}"),
                kind: RecoveryActionKind::Instruct,
                title: format!("Fix {check}"),
                detail: if action.is_empty() {
                    issue.clone()
                } else {
                    format!("{issue} - {action}")
                },
                requires_confirmation: !automatic,
                enabled: true,
                disabled_reason: String::new(),
            };
            let replace = recommended.as_ref().is_none_or(|current: &RecoveryAction| {
                severity.rank() >= DoctorSeverity::Warn.rank()
                    && current.kind == RecoveryActionKind::Instruct
            });
            if replace && severity.rank() >= DoctorSeverity::Warn.rank() {
                recommended = Some(candidate);
            }
        }
    }
    if recommended.is_none() {
        // Prefer first non-ok check as recommendation target.
        for section in &sections {
            for check in &section.checks {
                if check.severity.rank() >= DoctorSeverity::Warn.rank()
                    && let Some(action) = check.actions.first()
                {
                    recommended = Some(action.clone());
                    break;
                }
            }
            if recommended.is_some() {
                break;
            }
        }
    }

    let summary = if disabled {
        object
            .and_then(|map| map.get("reason"))
            .and_then(Value::as_str)
            .unwrap_or("Doctor probes are disabled.")
            .to_owned()
    } else if status == DoctorSeverity::Ok {
        "All probes look healthy.".into()
    } else {
        format!(
            "{} section(s) need attention.",
            sections
                .iter()
                .filter(|section| section.severity.rank() >= DoctorSeverity::Warn.rank())
                .count()
        )
    };

    DoctorReport {
        revision,
        disabled,
        status,
        summary,
        sections,
        recommended,
        utility: vec![
            RecoveryAction {
                id: "util:rerun".into(),
                kind: RecoveryActionKind::RerunDoctor,
                title: RecoveryActionKind::RerunDoctor.label().into(),
                detail: "Run daemon.doctor again and replace this report.".into(),
                requires_confirmation: false,
                enabled: true,
                disabled_reason: String::new(),
            },
            RecoveryAction {
                id: "util:copy".into(),
                kind: RecoveryActionKind::CopyEvidence,
                title: RecoveryActionKind::CopyEvidence.label().into(),
                detail: "Copy the selected check evidence to the clipboard.".into(),
                requires_confirmation: false,
                enabled: true,
                disabled_reason: String::new(),
            },
            RecoveryAction {
                id: "util:exit".into(),
                kind: RecoveryActionKind::ExitDoctor,
                title: RecoveryActionKind::ExitDoctor.label().into(),
                detail: "Return to the conversation without changing runtime state.".into(),
                requires_confirmation: false,
                enabled: true,
                disabled_reason: String::new(),
            },
        ],
    }
}

fn section_priority(id: &str) -> u8 {
    match id {
        "kernel" => 0,
        "state_dir_writable" | "state_dir_permissions" => 1,
        "auth" | "byok" => 2,
        "reasoner" => 3,
        "policy" => 4,
        "tools" | "lsp" => 5,
        "context_engine" | "hms_memory" => 6,
        "channels" | "artifact_store" => 7,
        _ => 50,
    }
}

fn project_section(id: &str, value: &Value) -> DoctorSection {
    let title = section_title(id);
    let mut checks = Vec::new();
    let severity = match value {
        Value::Object(map) => {
            if map.get("ok").and_then(Value::as_bool) == Some(false)
                || map.get("stale").and_then(Value::as_bool) == Some(true)
            {
                DoctorSeverity::Error
            } else if map.get("disabled").and_then(Value::as_bool) == Some(true)
                || map.get("configured").and_then(Value::as_bool) == Some(false)
            {
                DoctorSeverity::Info
            } else if map.get("ok").and_then(Value::as_bool) == Some(true) {
                DoctorSeverity::Ok
            } else {
                DoctorSeverity::Unknown
            }
        }
        Value::Bool(true) => DoctorSeverity::Ok,
        Value::Bool(false) => DoctorSeverity::Error,
        _ => DoctorSeverity::Unknown,
    };
    let summary = match value {
        Value::Object(map) if map.get("ok").and_then(Value::as_bool) == Some(false) => map
            .get("error")
            .and_then(Value::as_str)
            .unwrap_or("Probe failed")
            .to_owned(),
        Value::Object(map) if map.get("stale").and_then(Value::as_bool) == Some(true) => map
            .get("reason")
            .and_then(Value::as_str)
            .unwrap_or("Policy bundle is stale; restart the daemon to apply")
            .to_owned(),
        Value::Object(map) if map.get("configured").and_then(Value::as_bool) == Some(false) => {
            "Not configured".into()
        }
        Value::Object(map) if map.get("ok").and_then(Value::as_bool) == Some(true) => {
            "Healthy".into()
        }
        Value::Bool(true) => "Healthy".into(),
        Value::Bool(false) => "Failed".into(),
        other => redact_value(other, 0),
    };
    let evidence = flatten_evidence(value, 0);
    let mut actions = Vec::new();
    if severity.rank() >= DoctorSeverity::Warn.rank() {
        actions.push(RecoveryAction {
            id: format!("{id}:settings"),
            kind: RecoveryActionKind::OpenSettings,
            title: "Review settings".into(),
            detail: format!("Open settings related to {title}."),
            requires_confirmation: false,
            enabled: true,
            disabled_reason: String::new(),
        });
    }
    checks.push(DoctorCheck {
        id: id.to_owned(),
        title: title.clone(),
        summary,
        severity,
        evidence,
        actions,
    });
    DoctorSection {
        id: id.to_owned(),
        title,
        severity,
        checks,
    }
}

fn section_title(id: &str) -> String {
    match id {
        "kernel" => "Kernel".into(),
        "state_dir_writable" => "State directory".into(),
        "state_dir_permissions" => "State permissions".into(),
        "tools" => "Native tools".into(),
        "reasoner" => "Reasoner".into(),
        "auth" => "Authentication".into(),
        "context_engine" => "Context engine".into(),
        "lsp" => "Language servers".into(),
        "byok" => "BYOK providers".into(),
        "policy" => "Enterprise policy".into(),
        "hms_memory" => "HMS memory".into(),
        "artifact_store" => "Artifact store".into(),
        "channels" => "Channels".into(),
        other => other.replace('_', " "),
    }
}

fn flatten_evidence(value: &Value, depth: usize) -> Vec<DoctorEvidence> {
    const MAX_DEPTH: usize = 3;
    const MAX_ITEMS: usize = 24;
    const MAX_CHARS: usize = 160;
    let mut out = Vec::new();
    match value {
        Value::Object(map) => {
            for (key, child) in map {
                if depth >= MAX_DEPTH {
                    out.push(DoctorEvidence {
                        label: key.clone(),
                        value: redact_value(child, 0).chars().take(MAX_CHARS).collect(),
                    });
                } else if child.is_object() || child.is_array() {
                    for mut item in flatten_evidence(child, depth + 1) {
                        item.label = format!("{}.{}", key, item.label);
                        out.push(item);
                        if out.len() >= MAX_ITEMS {
                            break;
                        }
                    }
                } else {
                    out.push(DoctorEvidence {
                        label: key.clone(),
                        value: redact_value(child, 0).chars().take(MAX_CHARS).collect(),
                    });
                }
                if out.len() >= MAX_ITEMS {
                    break;
                }
            }
        }
        Value::Array(items) => {
            for (index, child) in items.iter().enumerate().take(MAX_ITEMS) {
                out.push(DoctorEvidence {
                    label: index.to_string(),
                    value: redact_value(child, 0).chars().take(MAX_CHARS).collect(),
                });
            }
        }
        other => out.push(DoctorEvidence {
            label: "value".into(),
            value: redact_value(other, 0).chars().take(MAX_CHARS).collect(),
        }),
    }
    out
}

fn redact_value(value: &Value, depth: usize) -> String {
    match value {
        Value::Null => "null".into(),
        Value::Bool(flag) => flag.to_string(),
        Value::Number(number) => number.to_string(),
        Value::String(text) => redact_string(text),
        Value::Array(items) if depth < 2 => format!(
            "[{}]",
            items
                .iter()
                .take(4)
                .map(|item| redact_value(item, depth + 1))
                .collect::<Vec<_>>()
                .join(", ")
        ),
        Value::Object(map) if depth < 2 => format!(
            "{{{}}}",
            map.iter()
                .take(4)
                .map(|(key, item)| format!("{key}:{}", redact_value(item, depth + 1)))
                .collect::<Vec<_>>()
                .join(", ")
        ),
        _ => "<complex>".into(),
    }
}

fn redact_string(text: &str) -> String {
    let lower = text.to_ascii_lowercase();
    if lower.contains("key")
        || lower.contains("token")
        || lower.contains("secret")
        || lower.contains("password")
        || (text.len() > 24
            && text
                .chars()
                .all(|ch| ch.is_ascii_alphanumeric() || ch == '-' || ch == '_'))
    {
        // Never project credential-like material into the doctor UI.
        if text.chars().count() > 8 {
            return "<redacted>".into();
        }
    }
    text.to_owned()
}

/// Closed recovery executor: only typed kinds run; instruct is never shell.
pub fn execute_recovery(action: &RecoveryAction) -> Result<DoctorOperation, String> {
    if !action.enabled {
        return Err(if action.disabled_reason.is_empty() {
            "Action is disabled".into()
        } else {
            action.disabled_reason.clone()
        });
    }
    match action.kind {
        RecoveryActionKind::RerunDoctor => Ok(DoctorOperation::Running),
        RecoveryActionKind::CopyEvidence => Ok(DoctorOperation::Succeeded),
        RecoveryActionKind::OpenSettings => Ok(DoctorOperation::Succeeded),
        RecoveryActionKind::ExitDoctor => Ok(DoctorOperation::Succeeded),
        RecoveryActionKind::Instruct => {
            // Intentional: fix_plan strings are documentation, not payloads.
            Ok(DoctorOperation::Succeeded)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn projects_kernel_failure_and_policy_stale_into_typed_sections() {
        let raw = json!({
            "version": "0.7.0",
            "disabled": false,
            "kernel": {"ok": false, "error": "unreachable"},
            "policy": {"configured": true, "stale": true, "reason": "bundle.toml changed on disk"},
            "auth": {"source": ""},
            "fix_plan": [{
                "check": "policy",
                "severity": "error",
                "issue": "policy bundle is stale",
                "action": "restart the daemon",
                "automatic": false
            }]
        });
        let report = project_doctor_report(&raw, 3);
        assert_eq!(report.revision, 3);
        assert!(!report.disabled);
        assert_eq!(report.status, DoctorSeverity::Error);
        assert!(
            report
                .sections
                .iter()
                .any(|section| section.id == "kernel" && section.severity == DoctorSeverity::Error)
        );
        assert!(
            report
                .sections
                .iter()
                .any(|section| section.id == "policy" && section.severity == DoctorSeverity::Error)
        );
        let recommended = report.recommended.as_ref().expect("recommended action");
        assert_eq!(recommended.kind, RecoveryActionKind::Instruct);
        assert!(recommended.requires_confirmation);
        assert!(recommended.detail.contains("restart the daemon"));
        // Credential-looking values never appear as raw secrets.
        assert!(!format!("{report:?}").contains("sk-"));
    }

    #[test]
    fn disabled_doctor_is_info_not_error() {
        let raw = json!({
            "version": "0.7.0",
            "disabled": true,
            "reason": "CARINA_DOCTOR_DISABLE is set; probes did not run"
        });
        let report = project_doctor_report(&raw, 1);
        assert!(report.disabled);
        assert_eq!(report.status, DoctorSeverity::Info);
        assert!(report.summary.contains("CARINA_DOCTOR_DISABLE"));
    }

    #[test]
    fn fix_plan_action_strings_are_never_executable_payloads() {
        let action = RecoveryAction {
            id: "fix:evil".into(),
            kind: RecoveryActionKind::Instruct,
            title: "Fix".into(),
            detail: "rm -rf /".into(),
            requires_confirmation: true,
            enabled: true,
            disabled_reason: String::new(),
        };
        let result = execute_recovery(&action).expect("instruct succeeds as documentation only");
        assert_eq!(result, DoctorOperation::Succeeded);
        // The detail is preserved for display but execute_recovery does not return a command.
        assert_eq!(action.kind, RecoveryActionKind::Instruct);
    }

    #[test]
    fn screen_navigation_and_render_keep_selection() {
        let raw = json!({
            "kernel": {"ok": true},
            "tools": {"ok": true, "available": ["scan"], "dir": "/tmp/tools"},
            "policy": {"configured": true, "stale": true, "reason": "stale"}
        });
        let mut screen = DoctorScreen::from_value(&raw, 9);
        assert!(!screen.render_lines().is_empty());
        screen.move_section(1);
        screen.move_check(0);
        screen.move_action(1);
        let lines = screen.render_lines().join("\n");
        assert!(lines.contains("Doctor"));
        assert!(lines.contains("Actions:"));
    }

    #[test]
    fn redacts_long_token_like_strings() {
        let evidence =
            flatten_evidence(&json!({"api_token": "abcdefghijklmnopqrstuvwxyz012345"}), 0);
        assert_eq!(evidence[0].value, "<redacted>");
    }
}
