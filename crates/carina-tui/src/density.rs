//! Transcript density is a presentation preference, never lifecycle state.

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash)]
pub enum DensityMode {
    #[default]
    Compact,
    Comfortable,
}

impl DensityMode {
    pub const fn parse(value: &str) -> Option<Self> {
        if value.eq_ignore_ascii_case("compact") {
            Some(Self::Compact)
        } else if value.eq_ignore_ascii_case("comfortable") {
            Some(Self::Comfortable)
        } else {
            None
        }
    }

    pub const fn as_config_value(self) -> &'static str {
        match self {
            Self::Compact => "compact",
            Self::Comfortable => "comfortable",
        }
    }

    pub const fn toggled(self) -> Self {
        match self {
            Self::Compact => Self::Comfortable,
            Self::Comfortable => Self::Compact,
        }
    }

    pub const fn profile(self) -> DensityProfile {
        match self {
            Self::Compact => DensityProfile {
                related_gap: 0,
                block_gap: 1,
                final_gap: 1,
                default_tool_expanded: false,
                collapsed_group_members: 0,
                collapsed_output_lines: 0,
                collapsed_create_preview_lines: 4,
            },
            Self::Comfortable => DensityProfile {
                related_gap: 1,
                block_gap: 2,
                final_gap: 2,
                default_tool_expanded: false,
                collapsed_group_members: 2,
                collapsed_output_lines: 3,
                collapsed_create_preview_lines: 8,
            },
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct DensityProfile {
    pub related_gap: usize,
    pub block_gap: usize,
    pub final_gap: usize,
    pub default_tool_expanded: bool,
    pub collapsed_group_members: usize,
    pub collapsed_output_lines: usize,
    pub collapsed_create_preview_lines: usize,
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::layout_contract;

    #[test]
    fn compact_is_the_stable_default_and_config_round_trips() {
        assert_eq!(DensityMode::default(), DensityMode::Compact);
        for mode in [DensityMode::Compact, DensityMode::Comfortable] {
            assert_eq!(DensityMode::parse(mode.as_config_value()), Some(mode));
        }
        assert_eq!(
            DensityMode::parse("COMFORTABLE"),
            Some(DensityMode::Comfortable)
        );
        assert_eq!(DensityMode::parse("dense"), None);
    }

    #[test]
    fn comfortable_adds_air_without_weakening_budgets() {
        let compact = DensityMode::Compact.profile();
        let comfortable = DensityMode::Comfortable.profile();
        assert!(comfortable.related_gap > compact.related_gap);
        assert!(comfortable.block_gap > compact.block_gap);
        assert!(comfortable.final_gap > compact.final_gap);
        assert!(!compact.default_tool_expanded);
        assert!(!comfortable.default_tool_expanded);
        assert!(comfortable.collapsed_group_members > compact.collapsed_group_members);
        assert!(comfortable.collapsed_output_lines > compact.collapsed_output_lines);
        assert!(
            comfortable.collapsed_create_preview_lines > compact.collapsed_create_preview_lines
        );
    }

    #[test]
    fn compact_preserves_the_established_transcript_spacing_contract() {
        let compact = DensityMode::Compact.profile();
        assert_eq!(compact.related_gap, layout_contract::TRANSCRIPT_RELATED_GAP);
        assert_eq!(compact.block_gap, layout_contract::TRANSCRIPT_BLOCK_GAP);
        assert_eq!(compact.final_gap, layout_contract::TRANSCRIPT_FINAL_GAP);
    }
}
