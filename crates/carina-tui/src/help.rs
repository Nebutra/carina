//! Discoverable key hints and a real `/help` surface (#22).

use crate::command::{COMMANDS, CommandSpec};
use crate::keybinding::KeyBindings;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HelpEntry {
    pub key: String,
    pub description: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HelpSurface {
    pub title: String,
    pub shortcuts: Vec<HelpEntry>,
    pub commands: Vec<HelpEntry>,
    pub footer: String,
}

/// Build the help surface from the command registry and keybindings.
///
/// `/help` must list every registered slash command so new commands cannot be
/// invisible (jcode pattern).
pub fn build_help_surface(bindings: KeyBindings) -> HelpSurface {
    HelpSurface {
        title: "Carina help".into(),
        shortcuts: conversation_key_hints(bindings),
        commands: COMMANDS
            .iter()
            .map(|spec| HelpEntry {
                key: spec.name.to_owned(),
                description: command_description(spec),
            })
            .collect(),
        footer: "Esc close  |  / opens command palette  |  ? toggles this help".into(),
    }
}

/// Compact footer hint line for the conversation surface.
pub fn conversation_key_hints(bindings: KeyBindings) -> Vec<HelpEntry> {
    vec![
        HelpEntry {
            key: "?".into(),
            description: "help / shortcuts".into(),
        },
        HelpEntry {
            key: "/".into(),
            description: "commands".into(),
        },
        HelpEntry {
            key: bindings.expand_tools.label().into(),
            description: "expand tool output".into(),
        },
        HelpEntry {
            key: bindings.interrupt.label().into(),
            description: "interrupt run".into(),
        },
        HelpEntry {
            key: "Esc Esc".into(),
            description: "edit previous prompt".into(),
        },
        HelpEntry {
            key: "Tab".into(),
            description: "queue follow-up while running".into(),
        },
    ]
}

pub fn footer_hint_line(bindings: KeyBindings) -> String {
    format!(
        "? help  |  / commands  |  {} expand tools  |  {} interrupt",
        bindings.expand_tools.label(),
        bindings.interrupt.label()
    )
}

fn command_description(spec: &CommandSpec) -> String {
    // Stable English labels for the help surface; i18n overlay can replace later.
    match spec.name {
        "/settings" => "Open settings".into(),
        "/status" => "Show runtime and session status".into(),
        "/provider" => "Choose or reconfigure a provider".into(),
        "/model" => "Choose a model".into(),
        "/plan" => "Toggle plan mode".into(),
        "/build" => "Switch to build mode".into(),
        "/sessions" => "Browse sessions".into(),
        "/resume" => "Resume a session".into(),
        "/cancel" => "Cancel the active run".into(),
        "/doctor" => "Run health checks and recovery".into(),
        "/help" => "Show this help surface".into(),
        "/quit" => "Quit Carina".into(),
        other => format!("Run {other}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn help_lists_every_registered_command_including_help_itself() {
        // Issue #22: /help is a real surface, not a composer re-trigger of "/".
        let surface = build_help_surface(KeyBindings::default());
        let names: Vec<_> = surface.commands.iter().map(|e| e.key.as_str()).collect();
        assert!(names.contains(&"/help"));
        assert!(names.contains(&"/quit"));
        assert_eq!(names.len(), COMMANDS.len());
        assert!(surface.shortcuts.iter().any(|e| e.key == "?"));
    }

    #[test]
    fn footer_hint_teaches_escape_hatches() {
        let line = footer_hint_line(KeyBindings::default());
        assert!(line.contains("? help"));
        assert!(line.contains("Ctrl-O"));
        assert!(line.contains("Ctrl-C"));
    }
}
