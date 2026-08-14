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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct LocalizedHint {
    key: &'static str,
    description: MessageId,
    show_description_in_footer: bool,
}

const PLAN_REVIEW_HINTS: &[LocalizedHint] = &[
    LocalizedHint {
        key: "A",
        description: MessageId::Approve,
        show_description_in_footer: true,
    },
    LocalizedHint {
        key: "S",
        description: MessageId::Revise,
        show_description_in_footer: true,
    },
    LocalizedHint {
        key: "C",
        description: MessageId::Comment,
        show_description_in_footer: true,
    },
    LocalizedHint {
        key: "M",
        description: MessageId::PlanMarkRange,
        show_description_in_footer: true,
    },
    LocalizedHint {
        key: "Q",
        description: MessageId::Close,
        show_description_in_footer: true,
    },
    LocalizedHint {
        key: "Up/Dn J/K Page Home/End",
        description: MessageId::PlanNavigate,
        show_description_in_footer: false,
    },
];

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

/// Shared shortcut registry consumed by `/help` and `/keymap`.
pub fn conversation_key_hints(bindings: KeyBindings, locale: Locale) -> Vec<HelpEntry> {
    let mut hints = vec![
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
            key: bindings.inspect_tool_output.label().into(),
            description: text(locale, MessageId::HelpShortcutInspectToolOutput).into(),
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
    ];
    hints.extend(plan_review_key_hints(locale));
    hints
}

/// Canonical Plan Review action matrix shared by `/help`, `/keymap`, and the
/// review surface.
pub fn plan_review_key_hints(locale: Locale) -> Vec<HelpEntry> {
    PLAN_REVIEW_HINTS
        .iter()
        .map(|hint| HelpEntry {
            key: hint.key.into(),
            description: text(locale, hint.description).into(),
        })
        .collect()
}

pub fn plan_review_hint_line(locale: Locale) -> String {
    PLAN_REVIEW_HINTS
        .iter()
        .map(|hint| {
            if hint.show_description_in_footer {
                format!("{} {}", hint.key, text(locale, hint.description))
            } else {
                hint.key.to_owned()
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
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

    #[test]
    fn plan_review_matrix_is_canonical_and_complete() {
        let plan = plan_review_key_hints(Locale::En);
        let keys = plan
            .iter()
            .map(|entry| entry.key.as_str())
            .collect::<Vec<_>>();
        assert_eq!(keys, ["A", "S", "C", "M", "Q", "Up/Dn J/K Page Home/End"]);
        assert_eq!(
            conversation_key_hints(KeyBindings::default(), Locale::En)
                .into_iter()
                .rev()
                .take(plan.len())
                .rev()
                .collect::<Vec<_>>(),
            plan
        );
        let footer = plan_review_hint_line(Locale::En);
        let footer_lower = footer.to_ascii_lowercase();
        for key in [
            "A approve",
            "S revise",
            "C comment",
            "M range",
            "Q close",
            "Home/End",
        ] {
            assert!(
                footer_lower.contains(&key.to_ascii_lowercase()),
                "missing {key:?} in {footer:?}"
            );
        }
        for locale in Locale::ALL {
            let footer = plan_review_hint_line(locale);
            assert!(
                unicode_width::UnicodeWidthStr::width(footer.as_str()) <= 75,
                "Plan Review hints overflow the 80-column footer in {locale:?}: {footer:?}"
            );
        }
        let renderer = include_str!("app/render.rs");
        assert!(renderer.contains("help::plan_review_hint_line"));
        assert!(!renderer.contains("MessageId::PlanHint"));
    }
}
