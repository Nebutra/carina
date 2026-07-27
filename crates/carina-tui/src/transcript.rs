use std::collections::BTreeMap;

use serde_json::Value;

use crate::rpc::{SessionItemEvent, WireEvent};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BlockKind {
    User,
    Assistant,
    Thinking,
    Tool,
    Governance,
    Status,
    Diagnostic,
}

#[derive(Debug, Clone)]
pub struct TranscriptBlock {
    pub id: String,
    pub task_id: String,
    pub kind: BlockKind,
    pub title: String,
    pub body: String,
    pub status: String,
    pub expanded: bool,
    pub selected: bool,
    pub source_prompt: String,
    pub branchable: bool,
}

impl TranscriptBlock {
    pub fn local_user(id: String, prompt: String) -> Self {
        Self {
            id,
            task_id: String::new(),
            kind: BlockKind::User,
            title: "You".into(),
            body: prompt.clone(),
            status: "submitted".into(),
            expanded: true,
            selected: false,
            source_prompt: prompt.clone(),
            branchable: false,
        }
    }

    pub fn local_steer(id: String, task_id: String, prompt: String) -> Self {
        Self {
            id,
            task_id,
            kind: BlockKind::User,
            title: "You steered".into(),
            body: prompt,
            status: "queued".into(),
            expanded: true,
            selected: false,
            source_prompt: String::new(),
            branchable: false,
        }
    }

    pub fn from_item(event: SessionItemEvent) -> Self {
        let item_kind = event
            .item
            .as_ref()
            .map(|item| item.kind.as_str())
            .unwrap_or(event.kind.as_str());
        let status = event
            .item
            .as_ref()
            .map(|item| item.status.clone())
            .filter(|value| !value.is_empty())
            .unwrap_or_else(|| "recorded".into());
        let details = event
            .item
            .as_ref()
            .map(|item| &item.details)
            .filter(|details| !details.is_empty())
            .unwrap_or(&event.details);
        let kind = classify(item_kind);
        let task_id = event
            .item
            .as_ref()
            .map(|item| item.task_id.clone())
            .filter(|value| !value.is_empty())
            .unwrap_or(event.task_id);
        let branchable =
            kind == BlockKind::User && !details.contains_key("steer_id") && !task_id.is_empty();
        Self {
            id: first_non_empty([
                event.item_id,
                event.source_event_id,
                event.turn_id,
                task_id.clone(),
            ]),
            task_id,
            kind,
            title: title(kind, item_kind),
            body: summarize_details(details),
            status,
            expanded: !matches!(kind, BlockKind::Thinking | BlockKind::Status),
            selected: false,
            source_prompt: if kind == BlockKind::User {
                first_detail(details, &["user_prompt", "prompt", "text", "content"])
                    .unwrap_or_default()
            } else {
                String::new()
            },
            branchable,
        }
    }

    pub fn from_event(event: WireEvent) -> Self {
        let kind = classify(&event.kind);
        let status = event.projected_status().to_owned();
        let body = if kind == BlockKind::Governance {
            first_non_empty([
                event.prompt.clone(),
                event.reason.clone(),
                event.label.clone(),
                event.diff.clone(),
                summarize_details(&event.payload),
            ])
        } else if event.kind == "task.completed" {
            first_non_empty([
                event.summary.clone(),
                event.status.clone(),
                summarize_details(&event.payload),
            ])
        } else {
            summarize_details(&event.payload)
        };
        Self {
            id: event.stable_id(),
            task_id: event.task_id,
            kind,
            title: title(kind, &event.kind),
            body,
            status: if status.is_empty() {
                "live".into()
            } else {
                status
            },
            expanded: !matches!(kind, BlockKind::Thinking | BlockKind::Status),
            selected: false,
            source_prompt: event.prompt,
            branchable: false,
        }
    }
}

fn first_detail(details: &BTreeMap<String, Value>, keys: &[&str]) -> Option<String> {
    keys.iter()
        .find_map(|key| details.get(*key).and_then(safe_value))
}

fn classify(value: &str) -> BlockKind {
    let value = value.to_ascii_lowercase();
    if value.contains("user") || value.contains("prompt") || value.contains("turn.input") {
        BlockKind::User
    } else if value.contains("assistant") || value.contains("message") || value.contains("result") {
        BlockKind::Assistant
    } else if value.contains("thinking") || value.contains("reasoning") {
        BlockKind::Thinking
    } else if value.contains("approval")
        || value.contains("permission")
        || value.contains("question")
        || value.contains("plan")
    {
        BlockKind::Governance
    } else if value.contains("tool")
        || value.contains("command")
        || value.contains("patch")
        || value.contains("file")
    {
        BlockKind::Tool
    } else if value.contains("error") || value.contains("diagnostic") {
        BlockKind::Diagnostic
    } else {
        BlockKind::Status
    }
}

fn title(kind: BlockKind, wire_kind: &str) -> String {
    match kind {
        BlockKind::User => "You".into(),
        BlockKind::Assistant => "Carina".into(),
        BlockKind::Thinking => "Thinking".into(),
        BlockKind::Tool => humanize(wire_kind),
        BlockKind::Governance => "Needs input".into(),
        BlockKind::Status => humanize(wire_kind),
        BlockKind::Diagnostic => "Diagnostic".into(),
    }
}

fn summarize_details(details: &BTreeMap<String, Value>) -> String {
    const PRIORITY: &[&str] = &[
        "text", "content", "prompt", "message", "summary", "command", "stdout", "stderr", "output",
        "diff", "question", "reason",
    ];
    for key in PRIORITY {
        if let Some(value) = details.get(*key).and_then(safe_value) {
            return value;
        }
    }
    let mut fields = Vec::new();
    for (key, value) in details {
        if sensitive_key(key) {
            continue;
        }
        if let Some(value) = safe_value(value) {
            fields.push(format!("{}: {}", humanize(key), value));
        }
        if fields.len() == 4 {
            break;
        }
    }
    if fields.is_empty() {
        "No additional details".into()
    } else {
        fields.join("\n")
    }
}

fn safe_value(value: &Value) -> Option<String> {
    match value {
        Value::String(value) if !value.trim().is_empty() => Some(value.trim().to_owned()),
        Value::Number(value) => Some(value.to_string()),
        Value::Bool(value) => Some(value.to_string()),
        Value::Array(values) if !values.is_empty() => {
            let values = values.iter().filter_map(safe_value).collect::<Vec<_>>();
            (!values.is_empty()).then(|| values.join(", "))
        }
        _ => None,
    }
}

fn sensitive_key(key: &str) -> bool {
    let key = key.to_ascii_lowercase();
    key.contains("secret")
        || key.contains("credential")
        || key.contains("api_key")
        || key.contains("token")
        || key.contains("password")
}

fn humanize(value: &str) -> String {
    let value = value
        .rsplit(['.', '/'])
        .next()
        .unwrap_or(value)
        .replace(['_', '-'], " ");
    let mut chars = value.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().collect::<String>() + chars.as_str(),
        None => "Activity".into(),
    }
}

fn first_non_empty<const N: usize>(values: [String; N]) -> String {
    values
        .into_iter()
        .find(|value| !value.is_empty())
        .unwrap_or_else(|| "anonymous".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn projection_never_renders_secret_fields() {
        let details = BTreeMap::from([
            ("api_key".into(), Value::String("sk-secret".into())),
            ("message".into(), Value::String("validation failed".into())),
        ]);
        assert_eq!(summarize_details(&details), "validation failed");
    }

    #[test]
    fn tool_events_become_tool_blocks() {
        let block = TranscriptBlock::from_event(WireEvent {
            event_id: "e1".into(),
            session_id: "s1".into(),
            task_id: "t1".into(),
            kind: "tool.call.started".into(),
            actor: String::new(),
            timestamp: String::new(),
            payload: BTreeMap::from([("command".into(), Value::String("git status".into()))]),
            permission_decision_id: String::new(),
            ..WireEvent::default()
        });
        assert_eq!(block.kind, BlockKind::Tool);
        assert_eq!(block.body, "git status");
    }
}
