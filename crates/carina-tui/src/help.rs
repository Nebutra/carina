//! Discoverable key hints and a real `/help` surface (#22).

use crate::i18n::{Locale, MessageId, format, text};
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
pub fn build_help_surface(
    bindings: KeyBindings,
    commands: Vec<HelpEntry>,
    locale: Locale,
) -> HelpSurface {
    HelpSurface {
        title: text(locale, MessageId::HelpTitle).into(),
        shortcuts: conversation_key_hints(bindings, locale),
        commands,
        footer: text(locale, MessageId::HelpFooter).into(),
    }
}

/// Compact footer hint line for the conversation surface.
pub fn conversation_key_hints(bindings: KeyBindings, locale: Locale) -> Vec<HelpEntry> {
    vec![
        HelpEntry {
            key: "?".into(),
            description: text(locale, MessageId::HelpShortcutHelp).into(),
        },
        HelpEntry {
            key: "/".into(),
            description: text(locale, MessageId::Commands).into(),
        },
        HelpEntry {
            key: bindings.expand_tools.label().into(),
            description: text(locale, MessageId::HelpShortcutExpandTools).into(),
        },
        HelpEntry {
            key: bindings.interrupt.label().into(),
            description: text(locale, MessageId::HelpShortcutInterrupt).into(),
        },
        HelpEntry {
            key: bindings.hard_cancel.label().into(),
            description: text(locale, MessageId::HelpShortcutHardCancel).into(),
        },
        HelpEntry {
            key: bindings.steer.label().into(),
            description: text(locale, MessageId::HelpShortcutSteer).into(),
        },
        HelpEntry {
            key: bindings.send_now.label().into(),
            description: text(locale, MessageId::HelpShortcutSendNow).into(),
        },
        HelpEntry {
            key: "Esc Esc".into(),
            description: text(locale, MessageId::HelpShortcutCheckpoint).into(),
        },
        HelpEntry {
            key: "Tab".into(),
            description: text(locale, MessageId::HelpShortcutQueueFollowUp).into(),
        },
    ]
}

pub fn footer_hint_line(bindings: KeyBindings, locale: Locale) -> String {
    format(
        locale,
        MessageId::HelpCompactFooter,
        &[
            ("expand", bindings.expand_tools.label()),
            ("interrupt", bindings.interrupt.label()),
        ],
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn help_lists_every_registered_command_including_help_itself() {
        // Issue #22: /help is a real surface, not a composer re-trigger of "/".
        let surface = build_help_surface(
            KeyBindings::default(),
            vec![
                HelpEntry {
                    key: "/help".into(),
                    description: "Show help".into(),
                },
                HelpEntry {
                    key: "/quit".into(),
                    description: "Quit".into(),
                },
            ],
            Locale::En,
        );
        let names: Vec<_> = surface.commands.iter().map(|e| e.key.as_str()).collect();
        assert!(names.contains(&"/help"));
        assert!(names.contains(&"/quit"));
        assert_eq!(names.len(), 2);
        assert!(surface.shortcuts.iter().any(|e| e.key == "?"));
    }

    #[test]
    fn footer_hint_teaches_escape_hatches() {
        let line = footer_hint_line(KeyBindings::default(), Locale::En);
        assert!(line.contains("? help"));
        assert!(line.contains("Ctrl-O"));
        assert!(line.contains("Esc"));
        assert!(!line.contains("Ctrl-C interrupt"));
    }

    #[test]
    fn running_actions_are_discoverable_and_semantically_distinct() {
        let hints = conversation_key_hints(KeyBindings::default(), Locale::En);
        assert!(
            hints
                .iter()
                .any(|entry| entry.key == "Esc" && entry.description.contains("pause"))
        );
        assert!(
            hints
                .iter()
                .any(|entry| entry.key == "Ctrl-C" && entry.description.contains("cancel"))
        );
        assert!(
            hints
                .iter()
                .any(|entry| entry.key == "Enter" && entry.description.contains("steer"))
        );
        assert!(
            hints
                .iter()
                .any(|entry| entry.key == "Tab" && entry.description.contains("queue"))
        );
        assert!(
            hints.iter().any(|entry| {
                entry.key == "Ctrl-Enter" && entry.description.contains("send now")
            })
        );
        assert!(
            hints.iter().any(|entry| {
                entry.key == "Esc Esc" && entry.description.contains("checkpoint")
            })
        );
    }

    #[test]
    fn vscode_keymap_projects_its_send_now_fallback() {
        let hints = conversation_key_hints(
            KeyBindings::for_terminal(crate::keybinding::TerminalFamily::VsCode),
            Locale::En,
        );
        assert!(
            hints.iter().any(|entry| {
                entry.key == "Alt-Enter" && entry.description.contains("send now")
            })
        );
    }
}
