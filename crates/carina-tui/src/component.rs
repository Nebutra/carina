use ratatui::layout::{Position, Rect};
use xai_ratatui_textarea::ElementId;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct ComponentId(pub u64);

impl ComponentId {
    pub fn stable(namespace: &str, value: &str) -> Self {
        const OFFSET: u64 = 0xcbf2_9ce4_8422_2325;
        const PRIME: u64 = 0x0000_0100_0000_01b3;
        let hash = namespace
            .bytes()
            .chain([0])
            .chain(value.bytes())
            .fold(OFFSET, |hash, byte| {
                (hash ^ u64::from(byte)).wrapping_mul(PRIME)
            });
        Self(hash | (1 << 63))
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Action {
    SelectLocale(usize),
    SelectProvider(usize),
    ConfirmProviderImport,
    CancelProviderImport,
    FocusProviderSearch,
    SelectModel(usize),
    SelectSession(usize),
    CreateSession,
    BeginRenameSession,
    ConfirmRenameSession,
    CancelRenameSession,
    BeginArchiveSession,
    ConfirmArchiveSession,
    CancelArchiveSession,
    UnarchiveSession,
    FocusSessionSearch,
    ToggleSessionScope,
    ToggleBlock(String),
    SelectHistory(usize),
    FocusComposer,
    PreviewMedia(ElementId),
    RetryMedia(ElementId),
    RetryExecution(String),
    CopyFailureId(String),
    OpenSessions,
    OpenModels,
    OpenSettings,
    ToggleDensity,
    OpenStatus,
    OpenAgents,
    OpenChanges,
    RefreshAgents,
    RefreshChanges,
    BeginPatchRollback,
    ConfirmPatchRollback,
    CancelPatchRollback,
    SelectAgent(usize),
    OpenSelectedAgentSession,
    BeginStopAgent,
    ConfirmStopAgent,
    SelectChange(usize),
    SelectSlashCommand {
        id: String,
        registry_revision: Option<String>,
    },
    SelectPromptHistory(usize),
    SelectFileCandidate(usize),
    SelectFileViewerLine(usize),
    ConfirmFileViewer,
    OpenLocale,
    OpenProvider,
    ApprovalAllow,
    ApprovalDeny,
    QuestionOption(usize),
    TogglePlanMode,
    ApprovePlan,
    RevisePlan,
    CancelPlan,
    ResumePausedExecutionRun,
    CloseOverlay,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HitRegion {
    pub component: ComponentId,
    pub area: Rect,
    pub action: Action,
}

#[derive(Debug, Default)]
pub struct InteractionMap {
    regions: Vec<HitRegion>,
    hovered: Option<ComponentId>,
    overlay_start: Option<usize>,
}

impl InteractionMap {
    pub fn begin_frame(&mut self) {
        self.regions.clear();
        self.overlay_start = None;
    }

    pub fn begin_overlay(&mut self) {
        self.overlay_start = Some(self.regions.len());
    }

    pub fn register(&mut self, region: HitRegion) {
        if region.area.width > 0 && region.area.height > 0 {
            self.regions.push(region);
        }
    }

    pub fn action_at(&self, position: Position) -> Option<Action> {
        let start = self.overlay_start.unwrap_or(0);
        self.regions[start..]
            .iter()
            .rev()
            .find(|region| region.area.contains(position))
            .map(|region| region.action.clone())
    }

    pub fn update_hover(&mut self, position: Position) -> bool {
        let start = self.overlay_start.unwrap_or(0);
        let next = self.regions[start..]
            .iter()
            .rev()
            .find(|region| region.area.contains(position))
            .map(|region| region.component);
        let changed = next != self.hovered;
        self.hovered = next;
        changed
    }

    pub fn hovered(&self, component: ComponentId) -> bool {
        self.hovered == Some(component)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn last_registered_region_owns_overlaps() {
        let mut map = InteractionMap::default();
        let area = Rect::new(1, 1, 4, 2);
        map.register(HitRegion {
            component: ComponentId(1),
            area,
            action: Action::FocusComposer,
        });
        map.register(HitRegion {
            component: ComponentId(2),
            area,
            action: Action::ToggleBlock("tool:first".into()),
        });
        assert_eq!(
            map.action_at(Position::new(2, 1)),
            Some(Action::ToggleBlock("tool:first".into()))
        );
    }

    #[test]
    fn semantic_component_ids_do_not_depend_on_vector_position() {
        let original = ComponentId::stable("transcript", "tool:first");
        assert_eq!(original, ComponentId::stable("transcript", "tool:first"));
        assert_ne!(original, ComponentId::stable("transcript", "tool:second"));
        assert_ne!(original, ComponentId::stable("overlay", "tool:first"));
    }

    #[test]
    fn overlay_regions_hide_background_actions() {
        let mut map = InteractionMap::default();
        map.register(HitRegion {
            component: ComponentId(1),
            area: Rect::new(0, 0, 20, 10),
            action: Action::FocusComposer,
        });
        map.begin_overlay();
        map.register(HitRegion {
            component: ComponentId(2),
            area: Rect::new(2, 7, 8, 1),
            action: Action::CloseOverlay,
        });

        assert_eq!(
            map.action_at(Position::new(3, 7)),
            Some(Action::CloseOverlay)
        );
        assert_eq!(map.action_at(Position::new(1, 1)), None);
    }
}
