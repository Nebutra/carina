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
    pub expand_tools: KeyBinding,
}

impl Default for KeyBindings {
    fn default() -> Self {
        Self {
            interrupt: KeyBinding::new(
                KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL),
                "Ctrl-C",
            ),
            expand_tools: KeyBinding::new(
                KeyEvent::new(KeyCode::Char('o'), KeyModifiers::CONTROL),
                "Ctrl-O",
            ),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn interrupt_label_and_dispatch_share_one_remappable_binding() {
        let binding = KeyBindings::default().interrupt;
        assert_eq!(binding.label(), "Ctrl-C");
        assert!(binding.matches(KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL)));

        let remapped = KeyBinding::new(KeyEvent::new(KeyCode::F(12), KeyModifiers::NONE), "F12");
        assert_eq!(remapped.label(), "F12");
        assert!(remapped.matches(KeyEvent::new(KeyCode::F(12), KeyModifiers::NONE)));
        assert!(!remapped.matches(binding.event));
    }

    #[test]
    fn tool_expansion_label_and_dispatch_share_one_binding() {
        let binding = KeyBindings::default().expand_tools;
        assert_eq!(binding.label(), "Ctrl-O");
        assert!(binding.matches(KeyEvent::new(KeyCode::Char('o'), KeyModifiers::CONTROL)));
    }
}
