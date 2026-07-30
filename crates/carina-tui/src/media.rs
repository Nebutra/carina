use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};

use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};
use xai_ratatui_textarea::{ElementId, ElementKind, TextArea};

use crate::rpc::MediaRef;

pub const IMAGE_ELEMENT_KIND: ElementKind = ElementKind(3);
pub const MAX_IMAGE_BYTES: u64 = 4 << 20;
pub const MAX_IMAGE_COUNT: usize = 4;

#[derive(Debug, Clone, Copy)]
pub struct MediaChipLabels<'a> {
    pub uploading: &'a str,
    pub image: &'a str,
    pub failed: &'a str,
}

#[derive(Debug, Clone)]
pub enum MediaSourceLabel {
    User(Option<String>),
    Temporary(Option<String>),
}

impl MediaSourceLabel {
    fn into_parts(self) -> (Option<String>, bool) {
        match self {
            Self::User(label) => (label, false),
            Self::Temporary(label) => (label, true),
        }
    }
}

impl Default for MediaChipLabels<'static> {
    fn default() -> Self {
        Self {
            uploading: "uploading",
            image: "image",
            failed: "failed",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum MediaState {
    Pending,
    Ready(MediaRef),
    Failed(String),
}

#[derive(Debug, Clone)]
pub struct MediaAttachment {
    pub element_id: ElementId,
    pub generation: u64,
    pub path: PathBuf,
    pub label: String,
    pub media_type: String,
    pub bytes: u64,
    pub temporary: bool,
    pub state: MediaState,
}

#[derive(Debug, Clone)]
pub struct MediaUploadWork {
    pub element_id: ElementId,
    pub generation: u64,
    pub path: PathBuf,
    pub media_type: String,
    pub temporary: bool,
}

#[derive(Debug, Default)]
pub struct MediaComposer {
    attachments: HashMap<ElementId, MediaAttachment>,
    next_generation: u64,
    post_insert_preview: Option<(ElementId, usize)>,
    hovered_preview: Option<ElementId>,
}

impl MediaComposer {
    pub fn insert_pending(
        &mut self,
        textarea: &mut TextArea,
        path: PathBuf,
        media_type: String,
        bytes: u64,
        source: MediaSourceLabel,
        labels: MediaChipLabels<'_>,
    ) -> Result<(ElementId, u64), String> {
        self.reconcile(textarea);
        if self.attachments.len() >= MAX_IMAGE_COUNT {
            return Err(format!(
                "A prompt can contain at most {MAX_IMAGE_COUNT} images"
            ));
        }
        let (label, temporary) = source.into_parts();
        self.next_generation = self.next_generation.saturating_add(1);
        let generation = self.next_generation;
        let label = label.unwrap_or_else(|| {
            path.file_name()
                .and_then(|name| name.to_str())
                .filter(|name| !name.is_empty())
                .unwrap_or("image")
                .to_owned()
        });
        textarea.begin_undo_group();
        let element_id = textarea.insert_element(
            &format!("[image:{generation}]"),
            IMAGE_ELEMENT_KIND,
            Some(chip_line(&label, &MediaState::Pending, labels)),
        );
        textarea.insert_str(" ");
        textarea.end_undo_group();
        self.attachments.insert(
            element_id,
            MediaAttachment {
                element_id,
                generation,
                path,
                label,
                media_type,
                bytes,
                temporary,
                state: MediaState::Pending,
            },
        );
        self.post_insert_preview = Some((element_id, textarea.cursor()));
        Ok((element_id, generation))
    }

    pub fn apply_upload(
        &mut self,
        textarea: &mut TextArea,
        element_id: ElementId,
        generation: u64,
        result: Result<MediaRef, String>,
        labels: MediaChipLabels<'_>,
    ) -> bool {
        self.reconcile(textarea);
        let Some(attachment) = self.attachments.get_mut(&element_id) else {
            return false;
        };
        if attachment.generation != generation {
            return false;
        }
        attachment.state = match result {
            Ok(reference) => MediaState::Ready(reference),
            Err(error) => MediaState::Failed(error),
        };
        textarea.set_element_display(
            element_id,
            Some(chip_line(&attachment.label, &attachment.state, labels)),
        );
        true
    }

    pub fn begin_retry(
        &mut self,
        textarea: &mut TextArea,
        element_id: ElementId,
        labels: MediaChipLabels<'_>,
    ) -> Option<(u64, PathBuf, String, bool)> {
        self.reconcile(textarea);
        let attachment = self.attachments.get_mut(&element_id)?;
        if !matches!(attachment.state, MediaState::Failed(_)) {
            return None;
        }
        self.next_generation = self.next_generation.saturating_add(1);
        attachment.generation = self.next_generation;
        attachment.state = MediaState::Pending;
        textarea.set_element_display(
            element_id,
            Some(chip_line(&attachment.label, &attachment.state, labels)),
        );
        Some((
            attachment.generation,
            attachment.path.clone(),
            attachment.media_type.clone(),
            attachment.temporary,
        ))
    }

    pub fn rebind_session(
        &mut self,
        textarea: &mut TextArea,
        labels: MediaChipLabels<'_>,
    ) -> Vec<MediaUploadWork> {
        self.reconcile(textarea);
        let mut work = Vec::with_capacity(self.attachments.len());
        for attachment in self.attachments.values_mut() {
            self.next_generation = self.next_generation.saturating_add(1);
            attachment.generation = self.next_generation;
            attachment.state = MediaState::Pending;
            textarea.set_element_display(
                attachment.element_id,
                Some(chip_line(&attachment.label, &attachment.state, labels)),
            );
            work.push(MediaUploadWork {
                element_id: attachment.element_id,
                generation: attachment.generation,
                path: attachment.path.clone(),
                media_type: attachment.media_type.clone(),
                temporary: attachment.temporary,
            });
        }
        work
    }

    pub fn relabel(&self, textarea: &mut TextArea, labels: MediaChipLabels<'_>) {
        for attachment in self.attachments.values() {
            textarea.set_element_display(
                attachment.element_id,
                Some(chip_line(&attachment.label, &attachment.state, labels)),
            );
        }
    }

    pub fn reconcile(&mut self, textarea: &TextArea) {
        let live: HashSet<ElementId> = textarea
            .elements()
            .iter()
            .filter(|element| element.kind == IMAGE_ELEMENT_KIND)
            .map(|element| element.id)
            .collect();
        self.attachments.retain(|id, attachment| {
            let keep = live.contains(id);
            if !keep && attachment.temporary {
                let _ = std::fs::remove_file(&attachment.path);
            }
            keep
        });
        if self
            .post_insert_preview
            .is_some_and(|(id, _)| !live.contains(&id))
        {
            self.post_insert_preview = None;
        }
        if self.hovered_preview.is_some_and(|id| !live.contains(&id)) {
            self.hovered_preview = None;
        }
    }

    pub fn has_pending(&self) -> bool {
        self.attachments
            .values()
            .any(|attachment| matches!(attachment.state, MediaState::Pending))
    }

    pub fn is_empty(&self) -> bool {
        self.attachments.is_empty()
    }

    pub fn failed_message(&self) -> Option<&str> {
        self.attachments
            .values()
            .find_map(|attachment| match &attachment.state {
                MediaState::Failed(message) => Some(message.as_str()),
                _ => None,
            })
    }

    pub fn ready_refs_in_text_order(&self, textarea: &TextArea) -> Vec<MediaRef> {
        textarea
            .elements()
            .iter()
            .filter_map(|element| self.attachments.get(&element.id))
            .filter_map(|attachment| match &attachment.state {
                MediaState::Ready(reference) => Some(reference.clone()),
                _ => None,
            })
            .collect()
    }

    pub fn prompt_text(&self, textarea: &TextArea) -> String {
        let mut text = textarea.text().to_owned();
        let mut ranges: Vec<_> = textarea
            .elements()
            .iter()
            .filter(|element| element.kind == IMAGE_ELEMENT_KIND)
            .map(|element| element.range.clone())
            .collect();
        ranges.sort_by_key(|range| std::cmp::Reverse(range.start));
        for range in ranges {
            text.replace_range(range, "");
        }
        text.trim().to_owned()
    }

    pub fn clear_plain_text(&self, textarea: &mut TextArea) {
        let text_len = textarea.text().len();
        let mut elements = textarea
            .elements()
            .iter()
            .filter(|element| element.kind == IMAGE_ELEMENT_KIND)
            .map(|element| element.range.clone())
            .collect::<Vec<_>>();
        elements.sort_by_key(|range| range.start);

        let mut cursor = 0;
        let mut plain_ranges = Vec::new();
        for element in elements {
            if cursor < element.start {
                plain_ranges.push(cursor..element.start);
            }
            cursor = cursor.max(element.end);
        }
        if cursor < text_len {
            plain_ranges.push(cursor..text_len);
        }
        for range in plain_ranges.into_iter().rev() {
            textarea.replace_range(range, "");
        }
    }

    pub fn clear(&mut self) {
        self.remove_temporary_files();
        self.attachments.clear();
        self.post_insert_preview = None;
        self.hovered_preview = None;
    }

    fn remove_temporary_files(&self) {
        for attachment in self.attachments.values().filter(|item| item.temporary) {
            let _ = std::fs::remove_file(&attachment.path);
        }
    }

    pub fn set_hovered(&mut self, element_id: Option<ElementId>) -> bool {
        let next = element_id.filter(|id| self.attachments.contains_key(id));
        if self.hovered_preview == next {
            return false;
        }
        self.hovered_preview = next;
        true
    }

    pub fn attachment_for_preview<'a>(
        &'a self,
        textarea: &TextArea,
    ) -> Option<&'a MediaAttachment> {
        let cursor = textarea.cursor();
        let element = textarea.elements().iter().find(|element| {
            element.kind == IMAGE_ELEMENT_KIND
                && (element.range.contains(&cursor)
                    || cursor == element.range.start
                    || cursor == element.range.end)
        });
        if let Some(element) = element {
            return self.attachments.get(&element.id);
        }
        if let Some(element_id) = self.hovered_preview {
            return self.attachments.get(&element_id);
        }
        let (element_id, inserted_cursor) = self.post_insert_preview?;
        (cursor == inserted_cursor)
            .then(|| self.attachments.get(&element_id))
            .flatten()
    }
}

impl Drop for MediaComposer {
    fn drop(&mut self) {
        self.remove_temporary_files();
    }
}

pub fn pasted_image_path(value: &str) -> Option<PathBuf> {
    let value = value.trim().trim_matches(['\'', '"']);
    if value.is_empty() || value.contains(['\n', '\r']) {
        return None;
    }
    let path = PathBuf::from(value);
    path.is_file().then_some(path)
}

pub fn inspect_image(path: &Path) -> Result<(String, u64), String> {
    let metadata =
        std::fs::metadata(path).map_err(|error| format!("Cannot read image: {error}"))?;
    if metadata.len() == 0 || metadata.len() > MAX_IMAGE_BYTES {
        return Err(format!(
            "Image must be between 1 byte and {} MiB",
            MAX_IMAGE_BYTES >> 20
        ));
    }
    let mut prefix = [0_u8; 12];
    use std::io::Read as _;
    let count = std::fs::File::open(path)
        .and_then(|mut file| file.read(&mut prefix))
        .map_err(|error| format!("Cannot read image: {error}"))?;
    let bytes = &prefix[..count];
    let media_type = if bytes.starts_with(b"\x89PNG\r\n\x1a\n") {
        "image/png"
    } else if bytes.starts_with(&[0xff, 0xd8, 0xff]) {
        "image/jpeg"
    } else if bytes.starts_with(b"GIF87a") || bytes.starts_with(b"GIF89a") {
        "image/gif"
    } else if bytes.len() >= 12 && &bytes[..4] == b"RIFF" && &bytes[8..12] == b"WEBP" {
        "image/webp"
    } else {
        return Err("Only PNG, JPEG, GIF, and WebP images can be attached".into());
    };
    Ok((media_type.into(), metadata.len()))
}

fn chip_line(label: &str, state: &MediaState, labels: MediaChipLabels<'_>) -> Line<'static> {
    let theme = crate::theme::Theme::detected(None);
    let (marker, color) = match state {
        MediaState::Pending => (labels.uploading, theme.tool_pending),
        MediaState::Ready(_) => (labels.image, theme.tool_success),
        MediaState::Failed(_) => (labels.failed, theme.tool_error),
    };
    Line::from(vec![
        Span::styled(
            format!(" {marker} "),
            Style::default()
                .fg(color)
                .add_modifier(Modifier::BOLD | Modifier::REVERSED),
        ),
        Span::styled(format!(" {label} "), Style::default().fg(color)),
    ])
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn prompt_text_strips_elements_by_identity() {
        let mut textarea = TextArea::new();
        textarea.insert_str("explain ");
        let mut media = MediaComposer::default();
        media
            .insert_pending(
                &mut textarea,
                PathBuf::from("/tmp/screen.png"),
                "image/png".into(),
                42,
                MediaSourceLabel::User(None),
                MediaChipLabels::default(),
            )
            .unwrap();
        textarea.insert_str("please");
        assert_eq!(media.prompt_text(&textarea), "explain  please");
    }

    #[test]
    fn local_command_text_can_clear_without_deleting_image_elements() {
        let mut textarea = TextArea::new();
        let mut media = MediaComposer::default();
        let path = std::path::PathBuf::from("/tmp/image.png");
        let (element_id, _) = media
            .insert_pending(
                &mut textarea,
                path,
                "image/png".into(),
                10,
                MediaSourceLabel::User(Some("image.png".into())),
                MediaChipLabels::default(),
            )
            .unwrap();
        textarea.insert_str(" /model");

        media.clear_plain_text(&mut textarea);

        assert_eq!(media.prompt_text(&textarea), "");
        assert_eq!(textarea.elements().len(), 1);
        assert_eq!(textarea.elements()[0].id, element_id);
        assert!(media.attachments.contains_key(&element_id));
    }

    #[test]
    fn deleting_atomic_element_reconciles_attachment() {
        let mut textarea = TextArea::new();
        let mut media = MediaComposer::default();
        media
            .insert_pending(
                &mut textarea,
                PathBuf::from("/tmp/screen.png"),
                "image/png".into(),
                42,
                MediaSourceLabel::User(None),
                MediaChipLabels::default(),
            )
            .unwrap();
        textarea.delete_backward(2);
        media.reconcile(&textarea);
        assert!(!media.has_pending());
        assert!(media.ready_refs_in_text_order(&textarea).is_empty());
    }

    #[test]
    fn dropping_composer_removes_owned_pending_and_failed_images_only() {
        let directory = tempfile::tempdir().unwrap();
        let pending = directory.path().join("carina-clipboard-pending.png");
        let failed = directory.path().join("carina-clipboard-failed.png");
        let user_file = directory.path().join("user-image.png");
        std::fs::write(&pending, b"pending").unwrap();
        std::fs::write(&failed, b"failed").unwrap();
        std::fs::write(&user_file, b"user").unwrap();

        let mut textarea = TextArea::new();
        {
            let mut media = MediaComposer::default();
            media
                .insert_pending(
                    &mut textarea,
                    pending.clone(),
                    "image/png".into(),
                    7,
                    MediaSourceLabel::Temporary(None),
                    MediaChipLabels::default(),
                )
                .unwrap();
            let (failed_id, failed_generation) = media
                .insert_pending(
                    &mut textarea,
                    failed.clone(),
                    "image/png".into(),
                    6,
                    MediaSourceLabel::Temporary(None),
                    MediaChipLabels::default(),
                )
                .unwrap();
            assert!(media.apply_upload(
                &mut textarea,
                failed_id,
                failed_generation,
                Err("upload rejected".into()),
                MediaChipLabels::default(),
            ));
            media
                .insert_pending(
                    &mut textarea,
                    user_file.clone(),
                    "image/png".into(),
                    4,
                    MediaSourceLabel::User(None),
                    MediaChipLabels::default(),
                )
                .unwrap();
        }

        assert!(!pending.exists());
        assert!(!failed.exists());
        assert!(user_file.exists());
    }

    #[test]
    fn clear_is_idempotent_and_never_deletes_user_files() {
        let directory = tempfile::tempdir().unwrap();
        let temporary = directory.path().join("carina-clipboard-clear.png");
        let user_file = directory.path().join("user-image.png");
        std::fs::write(&temporary, b"temporary").unwrap();
        std::fs::write(&user_file, b"user").unwrap();

        let mut textarea = TextArea::new();
        let mut media = MediaComposer::default();
        media
            .insert_pending(
                &mut textarea,
                temporary.clone(),
                "image/png".into(),
                9,
                MediaSourceLabel::Temporary(None),
                MediaChipLabels::default(),
            )
            .unwrap();
        media
            .insert_pending(
                &mut textarea,
                user_file.clone(),
                "image/png".into(),
                4,
                MediaSourceLabel::User(None),
                MediaChipLabels::default(),
            )
            .unwrap();

        media.clear();
        media.clear();

        assert!(!temporary.exists());
        assert!(user_file.exists());
        assert!(media.is_empty());
    }

    #[test]
    fn image_chip_label_cells_are_hit_testable_after_followup_text() {
        let mut textarea = TextArea::new();
        let mut media = MediaComposer::default();
        let (id, _) = media
            .insert_pending(
                &mut textarea,
                PathBuf::from("/tmp/media-sample.png"),
                "image/png".into(),
                42,
                MediaSourceLabel::User(Some("media-sample.png".into())),
                MediaChipLabels::default(),
            )
            .unwrap();
        textarea.insert_str("x");

        let area = ratatui::layout::Rect::new(1, 1, 80, 5);
        let state = xai_ratatui_textarea::TextAreaState::default();
        let hit_columns = (area.x..area.right())
            .filter(|column| {
                textarea
                    .element_at_screen(*column, area.y, area, state)
                    .is_some_and(|element| element.id == id)
            })
            .collect::<Vec<_>>();
        assert!(hit_columns.len() >= "media-sample.png".len());
    }

    #[test]
    fn fresh_insert_previews_until_cursor_moves_past_the_spacer() {
        let mut textarea = TextArea::new();
        let mut media = MediaComposer::default();
        media
            .insert_pending(
                &mut textarea,
                PathBuf::from("/tmp/screen.png"),
                "image/png".into(),
                42,
                MediaSourceLabel::User(None),
                MediaChipLabels::default(),
            )
            .unwrap();
        assert!(media.attachment_for_preview(&textarea).is_some());
        textarea.insert_str("x");
        assert!(media.attachment_for_preview(&textarea).is_none());
        textarea.set_cursor(0);
        assert!(media.attachment_for_preview(&textarea).is_some());
    }

    #[test]
    fn hover_previews_without_moving_the_editor_cursor() {
        let mut textarea = TextArea::new();
        let mut media = MediaComposer::default();
        let (element_id, _) = media
            .insert_pending(
                &mut textarea,
                PathBuf::from("/tmp/screen.png"),
                "image/png".into(),
                42,
                MediaSourceLabel::User(None),
                MediaChipLabels::default(),
            )
            .unwrap();
        textarea.insert_str("after");
        let cursor = textarea.cursor();
        assert!(media.attachment_for_preview(&textarea).is_none());
        assert!(media.set_hovered(Some(element_id)));
        assert_eq!(textarea.cursor(), cursor);
        assert_eq!(
            media
                .attachment_for_preview(&textarea)
                .map(|attachment| attachment.element_id),
            Some(element_id)
        );
    }

    #[test]
    fn failed_upload_retries_in_place_with_a_new_generation() {
        let mut media = MediaComposer::default();
        let mut textarea = TextArea::new();
        let (element_id, generation) = media
            .insert_pending(
                &mut textarea,
                PathBuf::from("/tmp/retry.png"),
                "image/png".into(),
                42,
                MediaSourceLabel::User(None),
                MediaChipLabels::default(),
            )
            .unwrap();
        assert!(media.apply_upload(
            &mut textarea,
            element_id,
            generation,
            Err("offline".into()),
            MediaChipLabels::default(),
        ));
        let (retry_generation, path, media_type, temporary) = media
            .begin_retry(&mut textarea, element_id, MediaChipLabels::default())
            .unwrap();
        assert!(retry_generation > generation);
        assert_eq!(path, PathBuf::from("/tmp/retry.png"));
        assert_eq!(media_type, "image/png");
        assert!(!temporary);
        assert!(media.has_pending());
        assert_eq!(textarea.elements().len(), 1);
        assert!(
            media
                .begin_retry(&mut textarea, element_id, MediaChipLabels::default())
                .is_none()
        );
    }

    #[test]
    fn existing_media_chip_relabels_after_locale_change() {
        let mut media = MediaComposer::default();
        let mut textarea = TextArea::new();
        media
            .insert_pending(
                &mut textarea,
                PathBuf::from("/tmp/localized.png"),
                "image/png".into(),
                42,
                MediaSourceLabel::User(Some("localized.png".into())),
                MediaChipLabels::default(),
            )
            .unwrap();
        media.relabel(
            &mut textarea,
            MediaChipLabels {
                uploading: "上传中",
                image: "图片",
                failed: "失败",
            },
        );
        let display = textarea.elements()[0].display.as_ref().unwrap();
        let rendered = display
            .spans
            .iter()
            .map(|span| span.content.as_ref())
            .collect::<String>();
        assert!(rendered.contains("上传中"));
        assert!(!rendered.contains("uploading"));
    }

    #[test]
    fn session_rebind_invalidates_old_results_without_duplicating_elements() {
        let mut media = MediaComposer::default();
        let mut textarea = TextArea::new();
        let (element_id, generation) = media
            .insert_pending(
                &mut textarea,
                PathBuf::from("/tmp/rebind.png"),
                "image/png".into(),
                42,
                MediaSourceLabel::User(None),
                MediaChipLabels::default(),
            )
            .unwrap();
        let work = media.rebind_session(&mut textarea, MediaChipLabels::default());
        assert_eq!(work.len(), 1);
        assert_eq!(work[0].element_id, element_id);
        assert!(work[0].generation > generation);
        assert_eq!(textarea.elements().len(), 1);
        assert!(!media.apply_upload(
            &mut textarea,
            element_id,
            generation,
            Err("stale source-session result".into()),
            MediaChipLabels::default(),
        ));
        assert!(media.has_pending());
    }
}
