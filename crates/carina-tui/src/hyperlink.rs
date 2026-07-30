use std::collections::HashSet;
use std::io::IsTerminal;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::{Arc, LazyLock};

use pulldown_cmark::{Event, Options, Parser, Tag, TagEnd};
use ratatui::buffer::Buffer;
use regex::Regex;
use unicode_width::UnicodeWidthStr;
use xai_ratatui_inline::LinkSpan;

static URL: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r#"(?i)\bhttps?://[^\s<>()\[\]{}\"']+"#).expect("valid URL regex"));
static FILE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r#"(?:\.{0,2}/|/)?(?:[[:alnum:]_.-]+/)+[[:alnum:]_.-]+(?::[0-9]+)?(?::[0-9]+)?"#)
        .expect("valid file regex")
});

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum HyperlinkMode {
    Off,
    Auto,
    Always,
}

impl HyperlinkMode {
    fn parse(value: Option<&str>) -> Self {
        match value.map(str::trim).map(str::to_ascii_lowercase).as_deref() {
            Some("off") => Self::Off,
            Some("always") => Self::Always,
            _ => Self::Auto,
        }
    }
}

#[derive(Debug, Clone)]
pub struct HyperlinkSupport(bool);

impl HyperlinkSupport {
    pub fn detect() -> Self {
        let mode = HyperlinkMode::parse(std::env::var("CARINA_HYPERLINKS").ok().as_deref());
        let tty = std::io::stdout().is_terminal();
        let no_color = std::env::var_os("NO_COLOR").is_some();
        let term = std::env::var("TERM").unwrap_or_default();
        let program = std::env::var("TERM_PROGRAM").unwrap_or_default();
        let tmux = std::env::var_os("TMUX").is_some();
        Self(support_for(
            mode,
            tty,
            no_color,
            &term,
            &program,
            tmux,
            tmux_version(),
        ))
    }

    pub fn links(
        &self,
        buffer: &Buffer,
        workspace: &Path,
        markdown: &[MarkdownLink],
    ) -> Vec<LinkSpan> {
        if self.0 {
            links_in_buffer(buffer, workspace, markdown)
        } else {
            Vec::new()
        }
    }
}

fn support_for(
    mode: HyperlinkMode,
    tty: bool,
    no_color: bool,
    term: &str,
    program: &str,
    tmux: bool,
    tmux_version: Option<(u16, u16)>,
) -> bool {
    if mode == HyperlinkMode::Off || !tty || no_color {
        return false;
    }
    if mode == HyperlinkMode::Always {
        return true;
    }
    if program.eq_ignore_ascii_case("WarpTerminal") || term.starts_with("screen") {
        return false;
    }
    !tmux || tmux_version.is_some_and(|version| version >= (3, 4))
}

fn tmux_version() -> Option<(u16, u16)> {
    let value = std::env::var("TMUX_VERSION").ok().or_else(|| {
        Command::new("tmux")
            .arg("-V")
            .output()
            .ok()
            .filter(|output| output.status.success())
            .and_then(|output| String::from_utf8(output.stdout).ok())
    })?;
    let version = value.split_whitespace().last()?;
    let mut parts = version.split('.');
    let major = parts.next()?.parse().ok()?;
    let minor = parts
        .next()?
        .trim_end_matches(|character: char| !character.is_ascii_digit())
        .parse()
        .ok()?;
    Some((major, minor))
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MarkdownLink {
    pub label: String,
    pub target: String,
}

pub fn markdown_links(input: &str) -> Vec<MarkdownLink> {
    let mut result = Vec::new();
    let mut active: Option<(String, String)> = None;
    for event in Parser::new_ext(
        input,
        Options::ENABLE_TABLES | Options::ENABLE_STRIKETHROUGH,
    ) {
        match event {
            Event::Start(Tag::Link { dest_url, .. }) => {
                active = Some((dest_url.into_string(), String::new()));
            }
            Event::Text(text) | Event::Code(text) if active.is_some() => {
                active
                    .as_mut()
                    .expect("active markdown link")
                    .1
                    .push_str(&text);
            }
            Event::End(TagEnd::Link) => {
                if let Some((target, label)) = active.take()
                    && !label.is_empty()
                {
                    result.push(MarkdownLink { label, target });
                }
            }
            _ => {}
        }
    }
    result
}

fn links_in_buffer(buffer: &Buffer, workspace: &Path, markdown: &[MarkdownLink]) -> Vec<LinkSpan> {
    let mut links = Vec::new();
    let mut seen = HashSet::new();
    for y in buffer.area.y..buffer.area.bottom() {
        let (text, columns) = row_text(buffer, y);
        for found in URL.find_iter(&text) {
            let raw = trim_url(found.as_str());
            add_match(
                &mut links,
                &mut seen,
                y,
                &columns,
                found.start(),
                found.start() + raw.len(),
                raw.to_owned(),
            );
        }
        for found in FILE.find_iter(&text) {
            if let Some(target) = file_uri(workspace, found.as_str()) {
                add_match(
                    &mut links,
                    &mut seen,
                    y,
                    &columns,
                    found.start(),
                    found.end(),
                    target,
                );
            }
        }
        for link in markdown {
            if !safe_target(&link.target) {
                continue;
            }
            for (start, _) in text.match_indices(&link.label) {
                if !match_has_modifier(&columns, buffer, y, start, start + link.label.len()) {
                    continue;
                }
                let target = if looks_like_file(&link.target) {
                    file_uri(workspace, &link.target).unwrap_or_else(|| link.target.clone())
                } else {
                    link.target.clone()
                };
                add_match(
                    &mut links,
                    &mut seen,
                    y,
                    &columns,
                    start,
                    start + link.label.len(),
                    target,
                );
            }
        }
    }
    links
}

fn match_has_modifier(
    columns: &[(usize, u16, u16)],
    buffer: &Buffer,
    row: u16,
    start: usize,
    end: usize,
) -> bool {
    columns
        .iter()
        .filter(|(byte, _, _)| *byte >= start && *byte < end)
        .all(|(_, column, _)| {
            buffer[(*column, row)]
                .modifier
                .contains(ratatui::style::Modifier::UNDERLINED)
        })
}

fn row_text(buffer: &Buffer, y: u16) -> (String, Vec<(usize, u16, u16)>) {
    let mut text = String::new();
    let mut columns = Vec::new();
    let mut x = buffer.area.x;
    while x < buffer.area.right() {
        let symbol = buffer[(x, y)].symbol();
        let start = text.len();
        text.push_str(symbol);
        let width = UnicodeWidthStr::width(symbol).max(1) as u16;
        columns.push((start, x, x.saturating_add(width)));
        x = x.saturating_add(width);
    }
    (text, columns)
}

fn add_match(
    links: &mut Vec<LinkSpan>,
    seen: &mut HashSet<(u16, u16, u16)>,
    row: u16,
    columns: &[(usize, u16, u16)],
    start: usize,
    end: usize,
    target: String,
) {
    let Some((_, col_start, _)) = columns.iter().find(|(byte, _, _)| *byte == start) else {
        return;
    };
    let Some((_, _, col_end)) = columns.iter().rev().find(|(byte, _, _)| *byte < end) else {
        return;
    };
    if seen.insert((row, *col_start, *col_end)) {
        links.push(LinkSpan {
            row,
            col_start: *col_start,
            col_end: *col_end,
            url: Arc::from(target.as_str()),
            id: Some(stable_id(target.as_bytes())),
        });
    }
}

fn stable_id(value: &[u8]) -> u32 {
    value.iter().fold(0x811c_9dc5_u32, |hash, byte| {
        (hash ^ u32::from(*byte)).wrapping_mul(0x0100_0193)
    })
}

fn trim_url(value: &str) -> &str {
    value.trim_end_matches(['.', ',', ';', ':', '!', '?'])
}

fn looks_like_file(value: &str) -> bool {
    value.starts_with('/')
        || value.starts_with("./")
        || value.starts_with("../")
        || value.contains('/') && !value.contains("://")
}

fn safe_target(value: &str) -> bool {
    value.starts_with("http://")
        || value.starts_with("https://")
        || value.starts_with("file://")
        || looks_like_file(value)
}

fn file_uri(workspace: &Path, reference: &str) -> Option<String> {
    let whole = FILE.find(reference)?.as_str();
    let mut path_end = whole.len();
    let mut numbers = Vec::new();
    while let Some(index) = whole[..path_end].rfind(':') {
        let suffix = &whole[index + 1..path_end];
        if suffix.is_empty()
            || !suffix.bytes().all(|byte| byte.is_ascii_digit())
            || numbers.len() == 2
        {
            break;
        }
        numbers.push(suffix);
        path_end = index;
    }
    numbers.reverse();
    let path = Path::new(&whole[..path_end]);
    let absolute: PathBuf = if path.is_absolute() {
        path.into()
    } else {
        workspace.join(path)
    };
    let encoded = percent_encode(absolute.to_string_lossy().as_bytes());
    let mut uri = format!("file://{encoded}");
    if let Some(line) = numbers.first() {
        uri.push_str(&format!("?line={line}"));
    }
    if let Some(column) = numbers.get(1) {
        uri.push_str(&format!("&col={column}"));
    }
    Some(uri)
}

fn percent_encode(value: &[u8]) -> String {
    let mut out = String::new();
    for byte in value {
        if byte.is_ascii_alphanumeric() || matches!(*byte, b'/' | b'-' | b'_' | b'.' | b'~') {
            out.push(char::from(*byte));
        } else {
            out.push_str(&format!("%{byte:02X}"));
        }
    }
    out
}

pub fn strip_osc8(input: &str) -> String {
    let bytes = input.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index..].starts_with(b"\x1b]8;") {
            index += 4;
            while index < bytes.len()
                && bytes[index] != 0x07
                && !bytes[index..].starts_with(b"\x1b\\")
            {
                index += 1;
            }
            if bytes.get(index) == Some(&0x07) {
                index += 1;
            } else if bytes.get(index..index.saturating_add(2)) == Some(b"\x1b\\") {
                index += 2;
            }
        } else {
            out.push(bytes[index]);
            index += 1;
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

pub fn display_width(input: &str) -> usize {
    UnicodeWidthStr::width(strip_osc8(input).as_str())
}

#[cfg(test)]
mod tests {
    use super::*;
    use ratatui::buffer::Buffer;
    use ratatui::layout::Rect;

    #[test]
    fn terminal_gates_are_specific_and_overridable() {
        assert!(!support_for(
            HyperlinkMode::Auto,
            true,
            false,
            "xterm",
            "WarpTerminal",
            false,
            None
        ));
        assert!(!support_for(
            HyperlinkMode::Auto,
            true,
            false,
            "screen-256color",
            "",
            false,
            None
        ));
        assert!(!support_for(
            HyperlinkMode::Auto,
            true,
            false,
            "xterm",
            "",
            true,
            Some((3, 3))
        ));
        assert!(support_for(
            HyperlinkMode::Auto,
            true,
            false,
            "xterm",
            "",
            true,
            Some((3, 4))
        ));
        assert!(support_for(
            HyperlinkMode::Always,
            true,
            false,
            "screen",
            "WarpTerminal",
            true,
            None
        ));
        assert!(!support_for(
            HyperlinkMode::Always,
            false,
            false,
            "xterm",
            "",
            false,
            None
        ));
        assert!(!support_for(
            HyperlinkMode::Always,
            true,
            true,
            "xterm",
            "",
            false,
            None
        ));
    }

    #[test]
    fn file_target_carries_line_and_column() {
        assert_eq!(
            file_uri(
                Path::new("/work"),
                "crates/carina-index/src/search.rs:142:7"
            )
            .as_deref(),
            Some("file:///work/crates/carina-index/src/search.rs?line=142&col=7")
        );
    }

    #[test]
    fn markdown_keeps_the_real_destination() {
        assert_eq!(
            markdown_links("read [the docs](https://example.com/x)"),
            vec![MarkdownLink {
                label: "the docs".into(),
                target: "https://example.com/x".into()
            }]
        );
    }

    #[test]
    fn osc8_does_not_change_cjk_display_width() {
        let linked = "中文 \x1b]8;id=1234;https://example.com\x07链接\x1b]8;;\x07 ok";
        assert_eq!(strip_osc8(linked), "中文 链接 ok");
        assert_eq!(
            display_width(linked),
            UnicodeWidthStr::width("中文 链接 ok")
        );
    }

    #[test]
    fn rendered_transcript_detects_urls_files_and_markdown_labels() {
        let mut buffer = Buffer::empty(Rect::new(0, 0, 100, 3));
        buffer.set_string(
            0,
            0,
            "See https://example.com/docs",
            ratatui::style::Style::default(),
        );
        buffer.set_string(
            0,
            1,
            "Open crates/carina-index/src/search.rs:142:9",
            ratatui::style::Style::default(),
        );
        buffer.set_string(
            0,
            2,
            "Read docs",
            ratatui::style::Style::default().add_modifier(ratatui::style::Modifier::UNDERLINED),
        );
        let links = links_in_buffer(
            &buffer,
            Path::new("/work"),
            &[MarkdownLink {
                label: "docs".into(),
                target: "https://carina.example/guide".into(),
            }],
        );
        assert!(
            links
                .iter()
                .any(|link| link.url.as_ref() == "https://example.com/docs")
        );
        assert!(links.iter().any(|link| {
            link.url.as_ref() == "file:///work/crates/carina-index/src/search.rs?line=142&col=9"
        }));
        assert!(
            links
                .iter()
                .any(|link| link.url.as_ref() == "https://carina.example/guide")
        );
    }
}
