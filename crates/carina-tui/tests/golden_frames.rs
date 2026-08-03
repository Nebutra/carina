use carina_tui::{
    conversation::ConversationStatus,
    i18n::Locale,
    markdown,
    rpc::ModelContextTokens,
    theme::{ColorLevel, Polarity, Theme},
};
use ratatui::{
    Terminal,
    backend::TestBackend,
    buffer::Buffer,
    layout::{Constraint, Direction, Layout, Rect},
    style::{Modifier, Style},
    text::{Line, Span},
    widgets::{Block, Borders, Paragraph, Wrap},
};
use unicode_width::UnicodeWidthStr;

fn deterministic_theme(mode: carina_tui::glyphs::GlyphMode) -> Theme {
    let mut theme = Theme::carina(false);
    theme.glyphs = carina_tui::glyphs::Glyphs::new(mode);
    theme
}

fn serialize_frame(buffer: &Buffer) -> String {
    let area = buffer.area;
    let mut out = format!("size={}x{}\n", area.width, area.height);
    for y in area.y..area.bottom() {
        let mut x = area.x;
        while x < area.right() {
            out.push_str(buffer[(x, y)].symbol());
            x = x.saturating_add(UnicodeWidthStr::width(buffer[(x, y)].symbol()).max(1) as u16);
        }
        while out.ends_with(' ') {
            out.pop();
        }
        out.push('\n');
    }
    out.push_str("styles:\n");
    let mut run_start = None;
    let mut run_style = None;
    for y in area.y..area.bottom() {
        for x in area.x..area.right() {
            let cell = &buffer[(x, y)];
            let style = (cell.fg, cell.bg, cell.modifier);
            let position = (x, y);
            if style
                == (
                    ratatui::style::Color::Reset,
                    ratatui::style::Color::Reset,
                    Modifier::empty(),
                )
            {
                if let (Some(start), Some(previous)) = (run_start.take(), run_style.take()) {
                    out.push_str(&format!("{start:?}..{position:?} {previous:?}\n"));
                }
                continue;
            }
            if run_style.as_ref() != Some(&style)
                && let (Some(start), Some(previous)) =
                    (run_start.replace(position), run_style.replace(style))
            {
                out.push_str(&format!("{start:?}..{position:?} {previous:?}\n"));
            }
        }
        if let (Some(start), Some(previous)) = (run_start.take(), run_style.take()) {
            out.push_str(&format!(
                "{start:?}..({}, {}) {previous:?}\n",
                area.right(),
                y
            ));
        }
    }
    out
}

fn render_case(
    width: u16,
    height: u16,
    title: &str,
    body: Vec<Line<'static>>,
    overlay: bool,
) -> String {
    let theme = deterministic_theme(carina_tui::glyphs::GlyphMode::Unicode);
    let backend = TestBackend::new(width, height);
    let mut terminal = Terminal::new(backend).unwrap();
    terminal
        .draw(|frame| {
            let area = frame.area();
            let rows = Layout::default()
                .direction(Direction::Vertical)
                .constraints([
                    Constraint::Length(3),
                    Constraint::Min(1),
                    Constraint::Length(3),
                ])
                .split(area);
            frame.render_widget(
                Paragraph::new(Line::from(vec![
                    Span::styled(
                        " CARINA ",
                        Style::default()
                            .fg(theme.brand)
                            .add_modifier(Modifier::BOLD),
                    ),
                    Span::styled(title.to_owned(), Style::default().fg(theme.text)),
                ]))
                .block(
                    Block::default()
                        .borders(Borders::BOTTOM)
                        .border_style(Style::default().fg(theme.border)),
                ),
                rows[0],
            );
            let transcript = carina_tui::layout_contract::transcript_content(rows[1]);
            frame.render_widget(
                Paragraph::new(body)
                    .wrap(Wrap { trim: false })
                    .style(Style::default().fg(theme.text)),
                transcript,
            );
            frame.render_widget(
                Paragraph::new(Line::from(vec![
                    Span::styled(
                        carina_tui::glyphs::Glyphs::new(carina_tui::glyphs::GlyphMode::Unicode)
                            .prompt(),
                        Style::default().fg(theme.accent),
                    ),
                    Span::styled("Ask Carina", Style::default().fg(theme.muted)),
                ]))
                .block(
                    Block::default()
                        .borders(Borders::TOP)
                        .border_style(Style::default().fg(theme.border)),
                ),
                rows[2],
            );
            if overlay {
                let popup = Rect::new(
                    area.width / 4,
                    area.height / 4,
                    area.width / 2,
                    area.height / 2,
                );
                frame.render_widget(ratatui::widgets::Clear, popup);
                frame.render_widget(
                    Paragraph::new("Allow shell command?\n\n  [Allow once]  [Deny]")
                        .style(Style::default().fg(theme.text))
                        .block(
                            Block::default()
                                .title(" Approval ")
                                .borders(Borders::ALL)
                                .border_style(Style::default().fg(theme.accent)),
                        ),
                    popup,
                );
            }
        })
        .unwrap();
    serialize_frame(terminal.backend().buffer())
}

fn render_status(
    execution_status: &str,
    activity: Option<&str>,
    queued_follow_ups: usize,
    context: Option<&ModelContextTokens>,
) -> String {
    render_status_with_mode(
        execution_status,
        activity,
        queued_follow_ups,
        context,
        carina_tui::glyphs::GlyphMode::Unicode,
    )
}

fn render_status_with_mode(
    execution_status: &str,
    activity: Option<&str>,
    queued_follow_ups: usize,
    context: Option<&ModelContextTokens>,
    mode: carina_tui::glyphs::GlyphMode,
) -> String {
    let mut terminal = Terminal::new(TestBackend::new(120, 1)).unwrap();
    let theme = deterministic_theme(mode);
    terminal
        .draw(|frame| {
            ConversationStatus {
                execution_status,
                notice: "",
                elapsed: Some(std::time::Duration::from_secs(62)),
                activity,
                queued_follow_ups,
                background_work: false,
                interrupt_key: "Esc",
                priority_notice: false,
                no_color: false,
                context,
                locale: Locale::En,
            }
            .render(frame, frame.area(), theme);
        })
        .unwrap();
    serialize_frame(terminal.backend().buffer())
}

fn render_palette(theme: Theme) -> String {
    let mut terminal = Terminal::new(TestBackend::new(64, 8)).unwrap();
    terminal
        .draw(|frame| {
            frame.render_widget(
                Paragraph::new(vec![
                    Line::from(vec![
                        Span::styled("CARINA", Style::default().fg(theme.brand)),
                        Span::raw("  "),
                        Span::styled("primary", Style::default().fg(theme.text)),
                        Span::raw("  "),
                        Span::styled("dim", theme.dim()),
                        Span::raw("  "),
                        Span::styled("muted", theme.muted()),
                        Span::raw("  "),
                        Span::styled("bright", theme.bright_muted()),
                    ]),
                    Line::from(vec![
                        Span::styled("focus", theme.focus()),
                        Span::raw("  "),
                        Span::styled("selected", theme.selected()),
                        Span::raw("  "),
                        Span::styled(
                            "link",
                            Style::default()
                                .fg(theme.link)
                                .add_modifier(Modifier::UNDERLINED),
                        ),
                    ]),
                    Line::from(vec![
                        Span::styled("+ added", theme.diff_add()),
                        Span::raw("  "),
                        Span::styled("- removed", theme.diff_remove()),
                    ]),
                    Line::styled(
                        "fn semantic_theme() {}",
                        Style::default().fg(theme.code_fg).bg(theme.code_bg),
                    ),
                    Line::from(vec![
                        Span::styled("pending", Style::default().fg(theme.tool_pending)),
                        Span::raw("  "),
                        Span::styled("success", Style::default().fg(theme.tool_success)),
                        Span::raw("  "),
                        Span::styled("error", Style::default().fg(theme.tool_error)),
                    ]),
                    Line::from(
                        (1..=6)
                            .map(|level| Span::styled(format!("H{level} "), theme.heading(level)))
                            .collect::<Vec<_>>(),
                    ),
                ]),
                frame.area(),
            );
        })
        .unwrap();
    serialize_frame(terminal.backend().buffer())
}

#[test]
fn semantic_palette_dark_truecolor() {
    insta::assert_snapshot!(render_palette(Theme::new(
        Polarity::Dark,
        ColorLevel::TrueColor
    )));
}

#[test]
fn semantic_palette_light_truecolor() {
    insta::assert_snapshot!(render_palette(Theme::new(
        Polarity::Light,
        ColorLevel::TrueColor
    )));
}

#[test]
fn semantic_palette_color_depths() {
    for (name, level) in [
        ("none", ColorLevel::None),
        ("basic", ColorLevel::Basic),
        ("ansi256", ColorLevel::Ansi256),
        ("truecolor", ColorLevel::TrueColor),
    ] {
        insta::assert_snapshot!(
            format!("semantic_palette_depth_{name}"),
            render_palette(Theme::new(Polarity::Dark, level))
        );
    }
}

#[test]
fn golden_empty_conversation() {
    insta::assert_snapshot!(render_case(
        80,
        24,
        "workspace / model",
        vec![Line::from("Start a conversation.")],
        false
    ));
}

#[test]
fn golden_single_turn() {
    let theme = deterministic_theme(carina_tui::glyphs::GlyphMode::Unicode);
    let glyphs = carina_tui::glyphs::Glyphs::new(carina_tui::glyphs::GlyphMode::Unicode);
    insta::assert_snapshot!(render_case(
        120,
        40,
        "carina / high",
        vec![
            Line::from(vec![
                Span::styled(
                    format!(
                        "{}You{}{}",
                        glyphs.role_prefix(),
                        " ".repeat(carina_tui::layout_contract::TRANSCRIPT_ROLE_LABEL_WIDTH - 3),
                        " ".repeat(carina_tui::layout_contract::TRANSCRIPT_ROLE_GAP_WIDTH)
                    ),
                    theme.transcript_user(),
                ),
                Span::raw("Explain the renderer."),
            ]),
            Line::from(""),
            Line::from(vec![
                Span::styled(
                    format!(
                        "{}Carina{}",
                        glyphs.role_prefix(),
                        " ".repeat(carina_tui::layout_contract::TRANSCRIPT_ROLE_GAP_WIDTH)
                    ),
                    theme.transcript_assistant(),
                ),
                Span::raw("The transcript is retained and width-aware."),
            ]),
        ],
        false
    ));
}

#[test]
fn golden_tool_call() {
    insta::assert_snapshot!(render_case(
        80,
        24,
        "tool lifecycle",
        vec![
            Line::from("◐ Reading src/lib.rs"),
            Line::from("✓ Read src/lib.rs · 42 lines")
        ],
        false
    ));
}

#[test]
fn golden_diff() {
    insta::assert_snapshot!(render_case(
        80,
        24,
        "changes",
        vec![
            Line::styled(
                "- old value",
                Style::default()
                    .fg(deterministic_theme(carina_tui::glyphs::GlyphMode::Unicode).danger)
            ),
            Line::styled(
                "+ new value",
                Style::default()
                    .fg(deterministic_theme(carina_tui::glyphs::GlyphMode::Unicode).success)
            )
        ],
        false
    ));
}

#[test]
fn golden_code_block() {
    let theme = deterministic_theme(carina_tui::glyphs::GlyphMode::Unicode);
    let lines = markdown::render(
        "```rust\npub fn ready() -> bool { true }\n```",
        72,
        markdown::MarkdownTheme {
            glyphs: theme.glyphs,
            text: Style::default().fg(theme.text),
            accent: Style::default().fg(theme.accent),
            muted: Style::default().fg(theme.muted),
            code: Style::default().fg(theme.warning),
            quote: Style::default().fg(theme.muted),
            link: Style::default()
                .fg(theme.link)
                .add_modifier(Modifier::UNDERLINED),
            headings: std::array::from_fn(|index| theme.heading(index + 1)),
            code_keyword: Style::default().fg(theme.accent),
            code_string: Style::default().fg(theme.warning),
            code_comment: Style::default().fg(theme.muted),
            code_number: Style::default().fg(theme.warning),
            code_type: Style::default().fg(theme.accent),
        },
    );
    insta::assert_snapshot!(render_case(80, 24, "markdown", lines, false));
}

#[test]
fn golden_overlay_open() {
    insta::assert_snapshot!(render_case(
        80,
        24,
        "governance paused",
        vec![Line::from("Waiting for approval")],
        true
    ));
}

#[test]
fn golden_narrow_terminal_degradation() {
    insta::assert_snapshot!(render_case(
        40,
        10,
        "项目 / claude-sonnet",
        vec![
            Line::from("你好，这是一条需要换行的中文消息。"),
            Line::from("A deliberately long fallback line.")
        ],
        false
    ));
}

#[test]
fn golden_ultrawide_terminal_keeps_a_bounded_conversation_column() {
    let rendered = render_case(
        200,
        50,
        "ultrawide workspace",
        vec![Line::from(
            "Conversation content remains a deliberate reading measure instead of an unbounded log line.",
        )],
        false,
    );
    insta::assert_snapshot!(rendered);
}

#[test]
fn vt100_snapshot_captures_terminal_escape_semantics() {
    let mut parser = vt100::Parser::new(6, 32, 10);
    parser.process(b"CARINA\r\n\x1b[32mready\x1b[0m\r\nworking\x1b[1A\r\x1b[2Kdone");
    insta::assert_snapshot!(parser.screen().contents());
}

#[test]
fn osc8_snapshot_is_compared_without_terminal_metadata() {
    let output = "CARINA \x1b]8;id=deadbeef;https://example.com\x07docs\x1b]8;;\x07 ready";
    insta::assert_snapshot!(carina_tui::hyperlink::strip_osc8(output), @"CARINA docs ready");
}

#[test]
fn golden_live_status_states() {
    let warning_context = ModelContextTokens {
        available: true,
        estimated: false,
        tokens: 85_000,
        limit_tokens: 100_000,
        remaining_tokens: 15_000,
        used_percent: 85,
        ..ModelContextTokens::default()
    };
    insta::assert_snapshot!("status_idle", render_status("ready", None, 0, None));
    insta::assert_snapshot!(
        "status_running",
        render_status("running", Some("running tests"), 0, None)
    );
    insta::assert_snapshot!(
        "status_queued_follow_up",
        render_status("running", Some("editing src/lib.rs"), 2, None)
    );
    insta::assert_snapshot!(
        "status_waiting_approval",
        render_status("waiting_approval", None, 0, None)
    );
    insta::assert_snapshot!(
        "status_context_warning",
        render_status(
            "running",
            Some("checking context"),
            0,
            Some(&warning_context)
        )
    );
}

#[test]
fn live_status_glyph_mode_is_fixture_owned() {
    let unicode = render_status_with_mode(
        "running",
        Some("running tests"),
        0,
        None,
        carina_tui::glyphs::GlyphMode::Unicode,
    );
    let ascii = render_status_with_mode(
        "running",
        Some("running tests"),
        0,
        None,
        carina_tui::glyphs::GlyphMode::Ascii,
    );
    assert!(unicode.contains("⠧ running tests"));
    assert!(unicode.contains("· Esc pause safely"));
    assert!(ascii.contains("\\ running tests"));
    assert!(ascii.contains("| Esc pause safely"));
}
