use ratatui::layout::{Position, Rect};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct ComponentId(pub u64);

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Action {
    SelectProvider(usize),
    SelectModel(usize),
    SelectSession(usize),
    ToggleBlock(usize),
    SelectHistory(usize),
    FocusComposer,
    OpenSessions,
    OpenModels,
    OpenCheckpoints,
    OpenSettings,
    OpenLocale,
    OpenProvider,
    ApprovalAllow,
    ApprovalDeny,
    QuestionOption(usize),
    SelectCheckpoint(usize),
    PreviewCheckpoint,
    BeginCheckpointRestore,
    ConfirmCheckpointRestore,
    ResumeRestoredTask,
    ResumePausedTask,
    CheckpointBack,
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
            action: Action::ToggleBlock(3),
        });
        assert_eq!(
            map.action_at(Position::new(2, 1)),
            Some(Action::ToggleBlock(3))
        );
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
