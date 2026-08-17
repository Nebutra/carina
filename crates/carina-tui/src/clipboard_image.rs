use std::io::Cursor;
use std::path::Path;
use std::path::PathBuf;

use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};
use image::{DynamicImage, ImageFormat, RgbaImage};
use ratatui::text::{Line, Span};
use xai_ratatui_textarea::{ElementId, ElementKind, TextArea};

use crate::media::MAX_IMAGE_BYTES;

pub const PASTE_ELEMENT_KIND: ElementKind = ElementKind(4);
pub const PASTE_CHIP_MIN_CHARS: usize = 400;
pub const PASTE_CHIP_MIN_LINES: usize = 8;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PasteMode {
    Rich,
    Text,
}

#[derive(Debug)]
pub enum ClipboardContent {
    Image(TemporaryImage),
    Text(String),
}

#[derive(Debug)]
pub struct TemporaryImage {
    path: Option<PathBuf>,
}

impl TemporaryImage {
    fn new(path: PathBuf) -> Self {
        Self { path: Some(path) }
    }

    pub fn path(&self) -> &std::path::Path {
        self.path.as_deref().expect("temporary image owns a path")
    }

    pub fn into_path(mut self) -> PathBuf {
        self.path.take().expect("temporary image owns a path")
    }
}

impl Drop for TemporaryImage {
    fn drop(&mut self) {
        if let Some(path) = self.path.take() {
            let _ = std::fs::remove_file(path);
        }
    }
}

#[derive(Debug)]
pub struct PendingPaste {
    pub generation: u64,
    pub element_id: ElementId,
    pub session_id: Option<String>,
}

impl PendingPaste {
    pub fn insert(
        textarea: &mut TextArea,
        generation: u64,
        session_id: Option<String>,
        paste_label: &str,
        reading_label: &str,
    ) -> Self {
        let theme = crate::theme::Theme::detected(None);
        let element_id = textarea.insert_element(
            &format!("[paste:{generation}]"),
            PASTE_ELEMENT_KIND,
            Some(Line::from(vec![
                Span::styled(format!(" {paste_label} "), theme.action()),
                Span::styled(format!(" {reading_label} "), theme.dim()),
            ])),
        );
        Self {
            generation,
            element_id,
            session_id,
        }
    }

    pub fn range(&self, textarea: &TextArea) -> Option<std::ops::Range<usize>> {
        textarea
            .elements()
            .iter()
            .find(|element| element.id == self.element_id && element.kind == PASTE_ELEMENT_KIND)
            .map(|element| element.range.clone())
    }

    pub fn resolve_text(&self, textarea: &mut TextArea, text: &str) -> bool {
        let Some(range) = self.range(textarea) else {
            return false;
        };
        textarea.replace_range(range, text);
        true
    }

    pub fn resolve_chip(
        &self,
        textarea: &mut TextArea,
        text: &str,
        display: Line<'static>,
    ) -> bool {
        let Some(range) = self.range(textarea) else {
            return false;
        };
        textarea.replace_range_with_element(range, text, PASTE_ELEMENT_KIND, Some(display));
        true
    }

    pub fn remove(&self, textarea: &mut TextArea) -> Option<(usize, usize)> {
        let range = self.range(textarea)?;
        let start = range.start;
        textarea.replace_range(range, "");
        Some((start, textarea.cursor()))
    }
}

pub fn should_chip_paste(text: &str) -> bool {
    paste_line_count(text) >= PASTE_CHIP_MIN_LINES || text.chars().count() >= PASTE_CHIP_MIN_CHARS
}

pub fn paste_line_count(text: &str) -> usize {
    if text.is_empty() {
        return 0;
    }
    text.chars().filter(|character| *character == '\n').count() + 1
}

pub fn paste_chip_line(label: &str, size: &str) -> Line<'static> {
    let theme = crate::theme::Theme::detected(None);
    Line::from(vec![
        Span::styled(format!(" {label} "), theme.action()),
        Span::styled(format!(" {size} "), theme.dim()),
    ])
}

pub fn paste_mode(key: KeyEvent) -> Option<PasteMode> {
    if key.code != KeyCode::Char('v') {
        return None;
    }
    let modifiers = key.modifiers;
    if modifiers == KeyModifiers::CONTROL | KeyModifiers::SHIFT
        || modifiers == KeyModifiers::SUPER | KeyModifiers::SHIFT
    {
        return Some(PasteMode::Text);
    }
    if modifiers == KeyModifiers::CONTROL || modifiers == KeyModifiers::SUPER {
        return Some(PasteMode::Rich);
    }
    #[cfg(target_os = "windows")]
    if modifiers == KeyModifiers::ALT {
        return Some(PasteMode::Rich);
    }
    None
}

pub fn capture(mode: PasteMode) -> Result<ClipboardContent, String> {
    let mut clipboard =
        arboard::Clipboard::new().map_err(|error| format!("Clipboard is unavailable: {error}"))?;

    if mode == PasteMode::Text {
        return clipboard_text(&mut clipboard).map(ClipboardContent::Text);
    }

    if let Ok(files) = clipboard.get().file_list() {
        for path in files.into_iter().filter(|path| path.is_file()) {
            if let Ok(image) = decode_bounded(&path) {
                return encode_temp_png(image).map(ClipboardContent::Image);
            }
        }
    }

    let Ok(image) = clipboard.get_image() else {
        return clipboard_text(&mut clipboard).map(ClipboardContent::Text);
    };
    let width = u32::try_from(image.width).map_err(|_| "Clipboard image is too wide")?;
    let height = u32::try_from(image.height).map_err(|_| "Clipboard image is too tall")?;
    if width == 0 || height == 0 || u64::from(width) * u64::from(height) > 48 * 1024 * 1024 {
        return Err("Clipboard image dimensions are unsupported".into());
    }
    let rgba = RgbaImage::from_raw(width, height, image.bytes.into_owned())
        .ok_or_else(|| "Clipboard returned an invalid RGBA image".to_owned())?;
    encode_temp_png(DynamicImage::ImageRgba8(rgba)).map(ClipboardContent::Image)
}

fn clipboard_text(clipboard: &mut arboard::Clipboard) -> Result<String, String> {
    let text = clipboard
        .get_text()
        .map_err(|error| format!("The clipboard does not contain text or an image: {error}"))?;
    if text.is_empty() {
        return Err("The clipboard is empty".into());
    }
    Ok(text)
}

fn decode_bounded(path: &std::path::Path) -> Result<DynamicImage, String> {
    let mut reader = image::ImageReader::open(path)
        .map_err(|error| format!("Could not open clipboard image: {error}"))?
        .with_guessed_format()
        .map_err(|error| format!("Could not identify clipboard image: {error}"))?;
    let mut limits = image::Limits::default();
    limits.max_image_width = Some(16_384);
    limits.max_image_height = Some(16_384);
    limits.max_alloc = Some(48 * 1024 * 1024 * 4);
    reader.limits(limits);
    reader
        .decode()
        .map_err(|error| format!("Could not decode clipboard image: {error}"))
}

fn encode_temp_png(image: DynamicImage) -> Result<TemporaryImage, String> {
    let mut png = Vec::new();
    image
        .write_to(&mut Cursor::new(&mut png), ImageFormat::Png)
        .map_err(|error| format!("Could not encode clipboard image: {error}"))?;
    if png.is_empty() || png.len() as u64 > MAX_IMAGE_BYTES {
        return Err(format!(
            "Clipboard image exceeds the {} MiB attachment limit",
            MAX_IMAGE_BYTES >> 20
        ));
    }
    let file = tempfile::Builder::new()
        .prefix(&format!("carina-clipboard-{}-", std::process::id()))
        .suffix(".png")
        .tempfile()
        .map_err(|error| format!("Could not stage clipboard image: {error}"))?;
    std::fs::write(file.path(), png)
        .map_err(|error| format!("Could not stage clipboard image: {error}"))?;
    let (_handle, path) = file
        .keep()
        .map_err(|error| format!("Could not retain clipboard image: {}", error.error))?;
    Ok(TemporaryImage::new(path))
}

pub fn cleanup_orphaned_temp_images() {
    cleanup_orphaned_temp_images_in(&std::env::temp_dir());
}

fn cleanup_orphaned_temp_images_in(directory: &Path) {
    let Ok(entries) = std::fs::read_dir(directory) else {
        return;
    };
    for entry in entries.flatten() {
        let Ok(file_type) = entry.file_type() else {
            continue;
        };
        if !file_type.is_file() {
            continue;
        }
        let name = entry.file_name();
        let Some(name) = name.to_str() else {
            continue;
        };
        let Some(rest) = name.strip_prefix("carina-clipboard-") else {
            continue;
        };
        let Some((pid, suffix)) = rest.split_once('-') else {
            continue;
        };
        let Ok(pid) = pid.parse::<u32>() else {
            continue;
        };
        if suffix.is_empty() || !suffix.ends_with(".png") || process_is_live(pid) {
            continue;
        }
        let _ = std::fs::remove_file(entry.path());
    }
}

#[cfg(unix)]
fn process_is_live(pid: u32) -> bool {
    let Ok(pid) = libc::pid_t::try_from(pid) else {
        return false;
    };
    if pid <= 0 {
        return false;
    }

    // Signal zero performs existence/permission checking without sending a signal.
    // A permission error still proves that the process exists.
    if unsafe { libc::kill(pid, 0) } == 0 {
        return true;
    }
    std::io::Error::last_os_error().raw_os_error() != Some(libc::ESRCH)
}

#[cfg(not(unix))]
fn process_is_live(_pid: u32) -> bool {
    true
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rgba_clipboard_payload_is_staged_as_a_bounded_png() {
        let image =
            DynamicImage::ImageRgba8(RgbaImage::from_raw(2, 2, vec![255; 2 * 2 * 4]).unwrap());
        let image = encode_temp_png(image).unwrap();
        let bytes = std::fs::read(image.path()).unwrap();
        assert!(bytes.starts_with(b"\x89PNG\r\n\x1a\n"));
        assert!(bytes.len() as u64 <= MAX_IMAGE_BYTES);
        let path = image.path().to_owned();
        drop(image);
        assert!(!path.exists());
    }

    #[test]
    fn paste_chords_distinguish_rich_and_text_only_routes() {
        assert_eq!(
            paste_mode(KeyEvent::new(KeyCode::Char('v'), KeyModifiers::CONTROL)),
            Some(PasteMode::Rich)
        );
        assert_eq!(
            paste_mode(KeyEvent::new(KeyCode::Char('v'), KeyModifiers::SUPER)),
            Some(PasteMode::Rich)
        );
        assert_eq!(
            paste_mode(KeyEvent::new(
                KeyCode::Char('v'),
                KeyModifiers::CONTROL | KeyModifiers::SHIFT
            )),
            Some(PasteMode::Text)
        );
        assert_eq!(
            paste_mode(KeyEvent::new(KeyCode::Char('v'), KeyModifiers::NONE)),
            None
        );
    }

    #[test]
    fn startup_cleanup_removes_only_orphaned_owned_pngs() {
        let directory = tempfile::tempdir().unwrap();
        let live = directory
            .path()
            .join(format!("carina-clipboard-{}-live.png", std::process::id()));
        let orphan = directory
            .path()
            .join("carina-clipboard-4294967295-orphan.png");
        let legacy = directory.path().join("carina-clipboard-legacy.png");
        let unrelated = directory.path().join("user-image.png");
        for path in [&live, &orphan, &legacy, &unrelated] {
            std::fs::write(path, b"png").unwrap();
        }
        cleanup_orphaned_temp_images_in(directory.path());
        assert!(live.exists());
        assert!(!orphan.exists());
        assert!(legacy.exists());
        assert!(unrelated.exists());
    }

    #[cfg(unix)]
    #[test]
    fn process_probe_rejects_values_outside_positive_pid_t() {
        assert!(process_is_live(std::process::id()));
        assert!(!process_is_live(0));
        assert!(!process_is_live(u32::MAX));
    }

    #[test]
    fn pending_paste_retains_its_logical_position_during_later_typing() {
        let mut textarea = TextArea::new();
        textarea.insert_str("before ");
        let pending = PendingPaste::insert(
            &mut textarea,
            1,
            Some("session".into()),
            "paste",
            "reading clipboard",
        );
        textarea.insert_str("after");

        assert!(pending.resolve_text(&mut textarea, "PASTED "));
        assert_eq!(textarea.text(), "before PASTED after");
        assert_eq!(textarea.cursor(), textarea.text().len());
    }

    #[test]
    fn pending_paste_display_uses_renderer_owned_locale_copy() {
        let mut textarea = TextArea::new();
        PendingPaste::insert(&mut textarea, 1, None, "貼上", "正在讀取剪貼簿");
        let display = textarea.elements()[0].display.as_ref().unwrap();
        let rendered = display
            .spans
            .iter()
            .map(|span| span.content.as_ref())
            .collect::<String>();
        assert!(rendered.contains("貼上"));
        assert!(rendered.contains("正在讀取剪貼簿"));
        assert!(!rendered.contains("reading clipboard"));
    }

    #[test]
    fn pending_paste_tracks_edits_before_its_anchor() {
        let mut textarea = TextArea::new();
        textarea.insert_str("left right");
        textarea.set_cursor("left ".len());
        let pending = PendingPaste::insert(&mut textarea, 1, None, "paste", "reading clipboard");
        textarea.set_cursor(0);
        textarea.insert_str("prefix ");

        assert!(pending.resolve_text(&mut textarea, "middle "));
        assert_eq!(textarea.text(), "prefix left middle right");
        assert_eq!(textarea.cursor(), "prefix ".len());
    }

    #[test]
    fn concurrent_pastes_can_settle_out_of_order_without_reordering_text() {
        let mut textarea = TextArea::new();
        let first = PendingPaste::insert(&mut textarea, 1, None, "paste", "reading clipboard");
        let second = PendingPaste::insert(&mut textarea, 2, None, "paste", "reading clipboard");
        textarea.insert_str("typed");

        assert!(second.resolve_text(&mut textarea, "second "));
        assert!(first.resolve_text(&mut textarea, "first "));
        assert_eq!(textarea.text(), "first second typed");
    }

    #[test]
    fn deleted_pending_paste_rejects_a_late_result() {
        let mut textarea = TextArea::new();
        let pending = PendingPaste::insert(&mut textarea, 1, None, "paste", "reading clipboard");
        textarea.delete_backward(1);

        assert!(!pending.resolve_text(&mut textarea, "late"));
        assert!(textarea.text().is_empty());
    }

    #[test]
    fn paste_chip_threshold_uses_lines_or_character_count() {
        assert!(!should_chip_paste("short"));
        assert!(!should_chip_paste("1\n2\n3\n4\n5\n6\n7"));
        assert!(should_chip_paste("1\n2\n3\n4\n5\n6\n7\n8"));
        assert!(should_chip_paste(&"x".repeat(PASTE_CHIP_MIN_CHARS)));
        assert!(!should_chip_paste(&"x".repeat(PASTE_CHIP_MIN_CHARS - 1)));
    }

    #[test]
    fn pending_paste_can_settle_as_a_chip_instead_of_inline_text() {
        let mut textarea = TextArea::new();
        textarea.insert_str("before ");
        let pending = PendingPaste::insert(&mut textarea, 1, None, "paste", "reading clipboard");
        textarea.insert_str("after");
        let payload = "1\n2\n3\n4\n5\n6\n7\n8";
        assert!(pending.resolve_chip(&mut textarea, payload, paste_chip_line("paste", "8 lines"),));
        assert_eq!(textarea.text(), format!("before {payload}after"));
        assert_eq!(textarea.elements()[0].kind, PASTE_ELEMENT_KIND);
        let rendered = textarea.elements()[0]
            .display
            .as_ref()
            .unwrap()
            .spans
            .iter()
            .map(|span| span.content.as_ref())
            .collect::<String>();
        assert!(rendered.contains("8 lines"));
        assert!(!textarea.text().contains("reading clipboard"));
    }
}
