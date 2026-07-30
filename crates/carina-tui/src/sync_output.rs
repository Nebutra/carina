use std::env;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SyncOutputSupport {
    Enabled,
    Disabled,
}

impl SyncOutputSupport {
    pub fn detect() -> Self {
        Self::from_lookup(|name| env::var(name).ok())
    }

    fn from_lookup(mut value: impl FnMut(&str) -> Option<String>) -> Self {
        // An explicit opt-out wins even when both overrides are present.
        if value("CARINA_NO_SYNC_OUTPUT").is_some() {
            return Self::Disabled;
        }
        if value("CARINA_FORCE_SYNC_OUTPUT").is_some() {
            return Self::Enabled;
        }

        if value("TERM_FEATURES").is_some_and(|features| {
            features
                .split([',', ':', ';'])
                .any(|feature| feature.trim() == "Sy")
        }) {
            return Self::Enabled;
        }

        if value("TMUX").is_some()
            || value("STY").is_some()
            || value("ZELLIJ").is_some()
            || value("ZELLIJ_SESSION_NAME").is_some()
        {
            return Self::Disabled;
        }

        let term_program = value("TERM_PROGRAM")
            .unwrap_or_default()
            .to_ascii_lowercase();
        let term = value("TERM").unwrap_or_default().to_ascii_lowercase();
        if value("WT_SESSION").is_some()
            || value("KITTY_WINDOW_ID").is_some()
            || value("GHOSTTY_RESOURCES_DIR").is_some()
            || matches!(
                term_program.as_str(),
                "apple_terminal" | "ghostty" | "iterm.app" | "wezterm"
            )
            || term.contains("kitty")
            || term.contains("wezterm")
        {
            Self::Enabled
        } else {
            Self::Disabled
        }
    }

    pub fn enabled(self) -> bool {
        self == Self::Enabled
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use super::*;

    fn detect(values: &[(&str, &str)]) -> SyncOutputSupport {
        let values = values.iter().copied().collect::<HashMap<_, _>>();
        SyncOutputSupport::from_lookup(|name| values.get(name).map(ToString::to_string))
    }

    #[test]
    fn explicit_disable_wins_over_force_and_terminal_detection() {
        assert_eq!(
            detect(&[
                ("CARINA_NO_SYNC_OUTPUT", "1"),
                ("CARINA_FORCE_SYNC_OUTPUT", "1"),
                ("TERM_PROGRAM", "iTerm.app"),
            ]),
            SyncOutputSupport::Disabled
        );
    }

    #[test]
    fn explicit_force_enables_unknown_and_multiplexed_terminals() {
        assert_eq!(
            detect(&[("CARINA_FORCE_SYNC_OUTPUT", "1"), ("TMUX", "/tmp/tmux")]),
            SyncOutputSupport::Enabled
        );
    }

    #[test]
    fn term_features_and_known_direct_terminals_enable_sync_output() {
        assert_eq!(
            detect(&[("TERM_FEATURES", "RGB:Sy:foo")]),
            SyncOutputSupport::Enabled
        );
        assert_eq!(
            detect(&[("TMUX", "/tmp/tmux"), ("TERM_FEATURES", "RGB:Sy")]),
            SyncOutputSupport::Enabled
        );
        assert_eq!(
            detect(&[("TERM_PROGRAM", "ghostty")]),
            SyncOutputSupport::Enabled
        );
        assert_eq!(
            detect(&[("WT_SESSION", "session")]),
            SyncOutputSupport::Enabled
        );
    }

    #[test]
    fn multiplexers_and_unknown_terminals_default_to_disabled() {
        assert_eq!(
            detect(&[("TMUX", "/tmp/tmux"), ("TERM_PROGRAM", "iTerm.app")]),
            SyncOutputSupport::Disabled
        );
        assert_eq!(
            detect(&[("TERM", "xterm-256color")]),
            SyncOutputSupport::Disabled
        );
        assert_eq!(detect(&[]), SyncOutputSupport::Disabled);
    }
}
