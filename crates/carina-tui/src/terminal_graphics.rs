use std::env;
use std::io::{self, IsTerminal, Write};
use std::path::{Path, PathBuf};

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD;
use ratatui::layout::Rect;
use ratatui::style::{Color, Style};
use ratatui::text::Span;

pub const MARK_WIDTH_CELLS: u16 = 10;
pub const MARK_HEIGHT_CELLS: u16 = 5;

const IMAGE_ID: u32 = 0x8e4053;
const PREVIEW_IMAGE_ID: u32 = 0x8e4054;
const PLACEHOLDER: char = '\u{10eeee}';
const DIACRITICS: [char; 10] = [
    '\u{0305}', '\u{030d}', '\u{030e}', '\u{0310}', '\u{0312}', '\u{033d}', '\u{033e}', '\u{033f}',
    '\u{0346}', '\u{034a}',
];
const MARK_PNG: &[u8] =
    include_bytes!("../../../docs/brand/assets/logo/raster/carina-symbol-terminal-microtype.png");

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TerminalGraphics {
    enabled: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MediaPreviewPlacement {
    pub path: PathBuf,
    pub owner_id: u64,
    pub area: Rect,
}

impl TerminalGraphics {
    pub const fn disabled() -> Self {
        Self { enabled: false }
    }

    pub fn detect() -> Self {
        Self {
            enabled: detect_support(
                env::var("CARINA_TERMINAL_GRAPHICS").ok().as_deref(),
                env::var("TERM_PROGRAM").ok().as_deref(),
                env::var_os("KITTY_WINDOW_ID").is_some(),
                env::var_os("TMUX").is_some(),
                env::var_os("NO_COLOR").is_some(),
                io::stdout().is_terminal(),
            ),
        }
    }

    pub const fn enabled(self) -> bool {
        self.enabled
    }

    pub fn upload(self, writer: &mut impl Write) -> io::Result<()> {
        if !self.enabled {
            return Ok(());
        }
        writer.write_all(upload_sequence().as_bytes())?;
        writer.flush()
    }

    pub fn refresh(self, writer: &mut impl Write) -> io::Result<()> {
        if !self.enabled {
            return Ok(());
        }
        writer.write_all(delete_sequence().as_bytes())?;
        writer.write_all(upload_sequence().as_bytes())?;
        writer.flush()
    }

    pub fn cleanup(self, writer: &mut impl Write) -> io::Result<()> {
        if !self.enabled {
            return Ok(());
        }
        writer.write_all(delete_sequence().as_bytes())?;
        writer.write_all(delete_preview_sequence().as_bytes())?;
        writer.flush()
    }

    pub fn render_preview(
        self,
        writer: &mut impl Write,
        placement: &MediaPreviewPlacement,
        retransmit: bool,
    ) -> io::Result<()> {
        if !self.enabled || placement.area.width == 0 || placement.area.height == 0 {
            return Ok(());
        }
        if retransmit {
            let png = preview_png(&placement.path)?;
            writer.write_all(delete_preview_sequence().as_bytes())?;
            writer.write_all(transmit_sequence(PREVIEW_IMAGE_ID, &png).as_bytes())?;
        }
        writer.write_all(place_preview_sequence(placement.area).as_bytes())?;
        writer.flush()
    }

    pub fn clear_preview(self, writer: &mut impl Write) -> io::Result<()> {
        if self.enabled {
            writer.write_all(delete_preview_sequence().as_bytes())?;
            writer.flush()?;
        }
        Ok(())
    }
}

pub fn placeholder_rows() -> Vec<Span<'static>> {
    #[allow(clippy::disallowed_methods)]
    // Kitty placeholder RGB encodes IMAGE_ID; it is not a visual palette color.
    let color = Color::Rgb(
        ((IMAGE_ID >> 16) & 0xff) as u8,
        ((IMAGE_ID >> 8) & 0xff) as u8,
        (IMAGE_ID & 0xff) as u8,
    );
    let style = Style::default().fg(color).underline_color(color);
    (0..MARK_HEIGHT_CELLS as usize)
        .map(|row| {
            let mut value = String::with_capacity(MARK_WIDTH_CELLS as usize * 9);
            for column_mark in DIACRITICS.iter().take(MARK_WIDTH_CELLS as usize) {
                value.push(PLACEHOLDER);
                value.push(DIACRITICS[row]);
                value.push(*column_mark);
            }
            Span::styled(value, style)
        })
        .collect()
}

fn detect_support(
    override_value: Option<&str>,
    term_program: Option<&str>,
    kitty_window: bool,
    in_tmux: bool,
    no_color: bool,
    stdout_is_terminal: bool,
) -> bool {
    if let Some(value) = override_value.map(str::trim).map(str::to_ascii_lowercase) {
        if matches!(value.as_str(), "0" | "false" | "off" | "none") {
            return false;
        }
        if matches!(value.as_str(), "1" | "true" | "on" | "kitty") {
            return !in_tmux && !no_color && stdout_is_terminal;
        }
    }
    if in_tmux || no_color || !stdout_is_terminal {
        return false;
    }
    kitty_window
        || term_program.is_some_and(|program| {
            let program = program.to_ascii_lowercase();
            program.contains("ghostty") || program.contains("kitty")
        })
}

fn upload_sequence() -> String {
    let mut output = transmit_sequence(IMAGE_ID, MARK_PNG);
    output.push_str(&format!(
        "\x1b_Ga=p,U=1,q=2,i={IMAGE_ID},p={IMAGE_ID},c={MARK_WIDTH_CELLS},r={MARK_HEIGHT_CELLS}\x1b\\"
    ));
    output
}

fn transmit_sequence(image_id: u32, png: &[u8]) -> String {
    let encoded = STANDARD.encode(png);
    let mut output = String::with_capacity(encoded.len() + encoded.len() / 4096 * 32 + 128);
    for (index, chunk) in encoded.as_bytes().chunks(4096).enumerate() {
        let more = usize::from((index + 1) * 4096 < encoded.len());
        if index == 0 {
            output.push_str(&format!(
                "\x1b_Ga=t,f=100,q=2,i={image_id},m={more};{}\x1b\\",
                String::from_utf8_lossy(chunk)
            ));
        } else {
            output.push_str(&format!(
                "\x1b_Gq=2,m={more};{}\x1b\\",
                String::from_utf8_lossy(chunk)
            ));
        }
    }
    output
}

fn delete_sequence() -> String {
    format!("\x1b_Ga=d,d=I,i={IMAGE_ID},q=2\x1b\\")
}

fn delete_preview_sequence() -> String {
    format!("\x1b_Ga=d,d=I,i={PREVIEW_IMAGE_ID},q=2\x1b\\")
}

fn place_preview_sequence(area: Rect) -> String {
    format!(
        "\x1b7\x1b[{};{}H\x1b_Ga=p,q=2,i={PREVIEW_IMAGE_ID},p={PREVIEW_IMAGE_ID},c={},r={}\x1b\\\x1b8",
        area.y.saturating_add(1),
        area.x.saturating_add(1),
        area.width,
        area.height,
    )
}

fn preview_png(path: &Path) -> io::Result<Vec<u8>> {
    let mut reader = image::ImageReader::open(path)
        .map_err(io::Error::other)?
        .with_guessed_format()
        .map_err(io::Error::other)?;
    let mut limits = image::Limits::default();
    limits.max_image_width = Some(16_384);
    limits.max_image_height = Some(16_384);
    limits.max_alloc = Some(48 * 1024 * 1024 * 4);
    reader.limits(limits);
    let image = reader.decode().map_err(io::Error::other)?;
    let mut png = Vec::new();
    image
        .write_to(&mut std::io::Cursor::new(&mut png), image::ImageFormat::Png)
        .map_err(io::Error::other)?;
    Ok(png)
}

#[cfg(test)]
mod tests {
    use unicode_width::UnicodeWidthStr;

    use super::*;

    #[test]
    fn capability_detection_fails_closed() {
        assert!(!detect_support(
            None,
            Some("ghostty"),
            false,
            true,
            false,
            true
        ));
        assert!(!detect_support(
            None,
            Some("ghostty"),
            false,
            false,
            true,
            true
        ));
        assert!(!detect_support(
            None,
            Some("ghostty"),
            false,
            false,
            false,
            false
        ));
        assert!(!detect_support(
            Some("off"),
            Some("ghostty"),
            false,
            false,
            false,
            true
        ));
        assert!(detect_support(
            Some("kitty"),
            None,
            false,
            false,
            false,
            true
        ));
        assert!(detect_support(
            None,
            Some("Ghostty"),
            false,
            false,
            false,
            true
        ));
    }

    #[test]
    fn placeholders_stay_inside_the_cell_buffer() {
        let rows = placeholder_rows();
        assert_eq!(rows.len(), MARK_HEIGHT_CELLS as usize);
        for row in rows {
            assert_eq!(
                UnicodeWidthStr::width(row.content.as_ref()),
                MARK_WIDTH_CELLS as usize
            );
            assert!(!row.content.contains("\x1b_G"));
        }
    }

    #[test]
    fn transport_upload_and_cleanup_are_explicit() {
        let upload = upload_sequence();
        assert!(upload.contains("\x1b_Ga=t,f=100"));
        assert!(upload.contains("\x1b_Ga=p,U=1"));
        assert!(upload.ends_with("\x1b\\"));
        assert!(delete_sequence().contains("a=d,d=I"));
    }

    #[test]
    fn preview_transport_retransmits_png_then_places_at_absolute_cells() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("preview.png");
        std::fs::write(&path, MARK_PNG).unwrap();
        let png = preview_png(&path).unwrap();
        let transmit = transmit_sequence(PREVIEW_IMAGE_ID, &png);
        let place = place_preview_sequence(Rect::new(7, 9, 30, 8));
        assert!(transmit.contains(&format!("i={PREVIEW_IMAGE_ID}")));
        assert!(place.starts_with("\x1b7\x1b[10;8H"));
        assert!(place.contains("c=30,r=8"));
        assert!(!place.contains("U=1"));
        assert!(place.ends_with("\x1b8"));
        assert!(delete_preview_sequence().contains(&format!("i={PREVIEW_IMAGE_ID}")));
    }
}
