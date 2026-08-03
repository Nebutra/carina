use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct KeyBinding {
    event: KeyEvent,
    label: &'static str,
}

impl KeyBinding {
    pub const fn new(event: KeyEvent, label: &'static str) -> Self {
        Self { event, label }
    }

    pub const fn label(self) -> &'static str {
        self.label
    }

    pub fn matches(self, event: KeyEvent) -> bool {
        event.code == self.event.code && event.modifiers == self.event.modifiers
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct KeyBindings {
    pub interrupt: KeyBinding,
    pub hard_cancel: KeyBinding,
    pub expand_tools: KeyBinding,
    pub steer: KeyBinding,
    pub send_now: KeyBinding,
}

impl Default for KeyBindings {
    fn default() -> Self {
        Self::for_terminal(TerminalFamily::detect())
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TerminalFamily {
    Ghostty,
    VsCode,
    Other,
}

impl TerminalFamily {
    pub fn detect() -> Self {
        Self::from_environment(
            std::env::var("TERM_PROGRAM").ok().as_deref(),
            std::env::var_os("VSCODE_PID").is_some(),
            std::env::var_os("GHOSTTY_RESOURCES_DIR").is_some(),
        )
    }

    pub fn from_environment(
        term_program: Option<&str>,
        vscode_pid: bool,
        ghostty_resources: bool,
    ) -> Self {
        let program = term_program.unwrap_or_default().trim();
        if program.eq_ignore_ascii_case("vscode") || vscode_pid {
            Self::VsCode
        } else if program.eq_ignore_ascii_case("ghostty") || ghostty_resources {
            Self::Ghostty
        } else {
            Self::Other
        }
    }
}

impl KeyBindings {
    pub fn for_terminal(terminal: TerminalFamily) -> Self {
        let send_now = match terminal {
            // VS Code's integrated terminal does not reliably preserve Ctrl-Enter.
            TerminalFamily::VsCode => KeyBinding::new(
                KeyEvent::new(KeyCode::Enter, KeyModifiers::ALT),
                "Alt-Enter",
            ),
            TerminalFamily::Ghostty | TerminalFamily::Other => KeyBinding::new(
                KeyEvent::new(KeyCode::Enter, KeyModifiers::CONTROL),
                "Ctrl-Enter",
            ),
        };
        Self {
            interrupt: KeyBinding::new(KeyEvent::new(KeyCode::Esc, KeyModifiers::NONE), "Esc"),
            hard_cancel: KeyBinding::new(
                KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL),
                "Ctrl-C",
            ),
            expand_tools: KeyBinding::new(
                KeyEvent::new(KeyCode::Char('o'), KeyModifiers::CONTROL),
                "Ctrl-O",
            ),
            steer: KeyBinding::new(KeyEvent::new(KeyCode::Enter, KeyModifiers::NONE), "Enter"),
            send_now,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn interrupt_label_and_dispatch_share_one_remappable_binding() {
        let binding = KeyBindings::default().interrupt;
        assert_eq!(binding.label(), "Esc");
        assert!(binding.matches(KeyEvent::new(KeyCode::Esc, KeyModifiers::NONE)));

        let remapped = KeyBinding::new(KeyEvent::new(KeyCode::F(12), KeyModifiers::NONE), "F12");
        assert_eq!(remapped.label(), "F12");
        assert!(remapped.matches(KeyEvent::new(KeyCode::F(12), KeyModifiers::NONE)));
        assert!(!remapped.matches(binding.event));
    }

    #[test]
    fn hard_cancel_remains_distinct_from_soft_interrupt() {
        let bindings = KeyBindings::default();
        assert_eq!(bindings.hard_cancel.label(), "Ctrl-C");
        assert!(
            bindings
                .hard_cancel
                .matches(KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL))
        );
        assert!(
            !bindings
                .hard_cancel
                .matches(KeyEvent::new(KeyCode::Esc, KeyModifiers::NONE))
        );
    }

    #[test]
    fn tool_expansion_label_and_dispatch_share_one_binding() {
        let binding = KeyBindings::default().expand_tools;
        assert_eq!(binding.label(), "Ctrl-O");
        assert!(binding.matches(KeyEvent::new(KeyCode::Char('o'), KeyModifiers::CONTROL)));
    }

    #[test]
    fn ghostty_profile_preserves_distinct_steer_and_send_now_events() {
        let bindings = KeyBindings::for_terminal(TerminalFamily::Ghostty);
        assert!(
            bindings
                .steer
                .matches(KeyEvent::new(KeyCode::Enter, KeyModifiers::NONE))
        );
        assert!(
            bindings
                .send_now
                .matches(KeyEvent::new(KeyCode::Enter, KeyModifiers::CONTROL))
        );
        assert_eq!(bindings.send_now.label(), "Ctrl-Enter");
    }

    #[test]
    fn vscode_profile_uses_integrated_terminal_safe_send_now_event() {
        let bindings = KeyBindings::for_terminal(TerminalFamily::VsCode);
        assert!(
            bindings
                .send_now
                .matches(KeyEvent::new(KeyCode::Enter, KeyModifiers::ALT))
        );
        assert_eq!(bindings.send_now.label(), "Alt-Enter");
    }

    #[test]
    fn terminal_family_detection_covers_ghostty_and_vscode_markers() {
        assert_eq!(
            TerminalFamily::from_environment(Some("ghostty"), false, false),
            TerminalFamily::Ghostty
        );
        assert_eq!(
            TerminalFamily::from_environment(None, true, false),
            TerminalFamily::VsCode
        );
        assert_eq!(
            TerminalFamily::from_environment(Some("xterm"), false, true),
            TerminalFamily::Ghostty
        );
    }
}
