//! Desktop OS notify for unfocused terminal runs.
//!
//! This is a projection of daemon execution lifecycle, not a second state
//! machine. Replay/catch-up must not fire. Focused terminals stay quiet.

use std::process::{Command, Stdio};

use crate::i18n::{text, Locale, MessageId};
use crate::rpc::ExecutionLifecycle;

pub const DESKTOP_NOTIFY_ENV: &str = "CARINA_DESKTOP_NOTIFY";
const MAX_BODY_CHARS: usize = 80;

pub fn desktop_notify_enabled(getenv: impl Fn(&str) -> Option<String>) -> bool {
    match getenv(DESKTOP_NOTIFY_ENV)
        .unwrap_or_default()
        .trim()
        .to_ascii_lowercase()
        .as_str()
    {
        "" | "1" | "true" | "on" | "yes" => true,
        "0" | "false" | "off" | "no" => false,
        _ => true,
    }
}

pub fn should_emit_desktop_notify(
    terminal_focused: bool,
    replayed: bool,
    enabled: bool,
    lifecycle: ExecutionLifecycle,
) -> bool {
    enabled
        && !terminal_focused
        && !replayed
        && matches!(
            lifecycle,
            ExecutionLifecycle::Completed
                | ExecutionLifecycle::Failed
                | ExecutionLifecycle::Degraded
                | ExecutionLifecycle::WaitingInput
                | ExecutionLifecycle::WaitingApproval
        )
}

pub fn desktop_notify_copy(
    locale: Locale,
    lifecycle: ExecutionLifecycle,
    summary: Option<&str>,
) -> Option<(String, String)> {
    if !matches!(
        lifecycle,
        ExecutionLifecycle::Completed
            | ExecutionLifecycle::Failed
            | ExecutionLifecycle::Degraded
            | ExecutionLifecycle::WaitingInput
            | ExecutionLifecycle::WaitingApproval
    ) {
        return None;
    }
    let status = notify_status_label(locale, lifecycle);
    let summary = sanitize_notify_text(summary.unwrap_or_default());
    let body = if summary.is_empty() {
        status
    } else if summary.chars().count() > MAX_BODY_CHARS {
        format!(
            "{}…",
            summary
                .chars()
                .take(MAX_BODY_CHARS.saturating_sub(1))
                .collect::<String>()
        )
    } else {
        format!("{status}: {summary}")
    };
    Some(("Carina".into(), body))
}

fn notify_status_label(locale: Locale, lifecycle: ExecutionLifecycle) -> String {
    match lifecycle {
        ExecutionLifecycle::Failed => text(locale, MessageId::StatusFailed).to_owned(),
        ExecutionLifecycle::Degraded => text(locale, MessageId::StatusDegraded).to_owned(),
        ExecutionLifecycle::WaitingInput => text(locale, MessageId::ChromeWaitingInput).to_owned(),
        ExecutionLifecycle::WaitingApproval => {
            text(locale, MessageId::StatusWaitingApproval).to_owned()
        }
        ExecutionLifecycle::Completed => match locale {
            Locale::ZhHans | Locale::ZhHant => "完成".into(),
            _ => "done".into(),
        },
        _ => String::new(),
    }
}

fn sanitize_notify_text(raw: &str) -> String {
    raw.chars()
        .map(|ch| if ch.is_control() { ' ' } else { ch })
        .collect::<String>()
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
}

pub fn consider_desktop_notify(
    terminal_focused: bool,
    replayed: bool,
    locale: Locale,
    lifecycle: ExecutionLifecycle,
    summary: Option<&str>,
) {
    if !should_emit_desktop_notify(
        terminal_focused,
        replayed,
        desktop_notify_enabled(|key| std::env::var(key).ok()),
        lifecycle,
    ) {
        return;
    }
    let Some((title, body)) = desktop_notify_copy(locale, lifecycle, summary) else {
        return;
    };
    dispatch_desktop_notify(&title, &body);
}

pub fn dispatch_desktop_notify(title: &str, body: &str) {
    let Some(mut command) = desktop_notify_command(title, body) else {
        return;
    };
    let _ = command
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn();
}

pub fn desktop_notify_command(title: &str, body: &str) -> Option<Command> {
    if title.is_empty() && body.is_empty() {
        return None;
    }
    if cfg!(target_os = "macos") {
        let mut command = Command::new("osascript");
        command.arg("-e").arg(format!(
            r#"display notification "{}" with title "{}""#,
            escape_osascript(body),
            escape_osascript(title)
        ));
        Some(command)
    } else if cfg!(target_os = "linux") {
        let mut command = Command::new("notify-send");
        command.arg("--app-name=Carina").arg(title).arg(body);
        Some(command)
    } else {
        None
    }
}

fn escape_osascript(value: &str) -> String {
    value.replace('\\', "\\\\").replace('"', "\\\"")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn kill_switch_and_default_on() {
        assert!(desktop_notify_enabled(|_| None));
        assert!(!desktop_notify_enabled(|_| Some("off".into())));
        assert!(!desktop_notify_enabled(|_| Some("0".into())));
        assert!(desktop_notify_enabled(|_| Some("yes".into())));
    }

    #[test]
    fn focused_or_replayed_or_cancelled_do_not_notify() {
        assert!(!should_emit_desktop_notify(
            true,
            false,
            true,
            ExecutionLifecycle::Completed
        ));
        assert!(!should_emit_desktop_notify(
            false,
            true,
            true,
            ExecutionLifecycle::Failed
        ));
        assert!(!should_emit_desktop_notify(
            false,
            false,
            false,
            ExecutionLifecycle::Completed
        ));
        assert!(!should_emit_desktop_notify(
            false,
            false,
            true,
            ExecutionLifecycle::Cancelled
        ));
        assert!(should_emit_desktop_notify(
            false,
            false,
            true,
            ExecutionLifecycle::WaitingInput
        ));
    }

    #[test]
    fn copy_uses_product_title_and_strips_controls() {
        let (title, body) = desktop_notify_copy(
            Locale::En,
            ExecutionLifecycle::Failed,
            Some("line\none\x07secret"),
        )
        .expect("copy");
        assert_eq!(title, "Carina");
        assert!(body.contains("failed"));
        assert!(!body.contains('\n'));
        assert!(!body.contains('\u{0007}'));
    }

    #[test]
    fn osascript_escapes_quotes() {
        if !cfg!(target_os = "macos") {
            return;
        }
        let command = desktop_notify_command(r#"ti"tle"#, r#"bo\dy""#).expect("cmd");
        let args: Vec<String> = command
            .get_args()
            .map(|arg| arg.to_string_lossy().into_owned())
            .collect();
        assert!(args.iter().any(|arg| arg.contains(r#"bo\\dy\""#)));
        assert!(args.iter().any(|arg| arg.contains(r#"ti\"tle"#)));
    }
}
