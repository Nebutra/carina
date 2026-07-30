use std::fs;
use std::io;
use std::path::PathBuf;
use std::process::Command;
use std::time::Duration;

use anyhow::Result;
use crossterm::event::{self, Event, KeyCode, KeyEventKind};
use crossterm::execute;
use crossterm::terminal::{
    EnterAlternateScreen, LeaveAlternateScreen, disable_raw_mode, enable_raw_mode,
};
use ratatui::backend::CrosstermBackend;
use ratatui::layout::{Constraint, Direction, Layout, Margin, Rect};
use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, Borders, Clear, Paragraph, Wrap};
use xai_ratatui_inline::{Terminal, with_synchronized_output};

use crate::i18n::{Locale, MessageId, text};
use crate::sync_output::SyncOutputSupport;
use crate::theme::Theme;

#[derive(Debug, Clone)]
pub struct RuntimeDiagnosticOptions {
    pub workspace: PathBuf,
    pub runtime_id: String,
    pub log_path: PathBuf,
    pub missing_methods: Vec<String>,
    pub obligations: Vec<String>,
    pub locale: Locale,
    pub carina_bin: PathBuf,
    pub no_alt_screen: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RuntimeDiagnosticOutcome {
    Exit,
    Restart,
}

pub fn run_runtime_diagnostic(
    options: RuntimeDiagnosticOptions,
) -> Result<RuntimeDiagnosticOutcome> {
    let background = crate::terminal_probe::background(Duration::from_millis(80));
    let mut terminal = DiagnosticTerminal::enter(options.no_alt_screen)?;
    let theme = Theme::detected(background);
    let mut show_logs = false;
    let mut retry_failed = false;
    loop {
        terminal.draw(|frame| {
            render(frame, &options, theme, show_logs, retry_failed);
        })?;
        let Event::Key(key) = event::read()? else {
            continue;
        };
        if key.kind == KeyEventKind::Release {
            continue;
        }
        match key.code {
            KeyCode::Esc | KeyCode::Char('q') => return Ok(RuntimeDiagnosticOutcome::Exit),
            KeyCode::Char('d') => show_logs = !show_logs,
            KeyCode::Enter | KeyCode::Char('r') => {
                let status = Command::new(&options.carina_bin)
                    .args(["runtime", "stop", "--workspace"])
                    .arg(&options.workspace)
                    .output();
                match status {
                    Ok(output) if output.status.success() => {
                        return Ok(RuntimeDiagnosticOutcome::Restart);
                    }
                    _ => retry_failed = true,
                }
            }
            _ => {}
        }
    }
}

fn render(
    frame: &mut ratatui::Frame<'_>,
    options: &RuntimeDiagnosticOptions,
    theme: Theme,
    show_logs: bool,
    retry_failed: bool,
) {
    let area = frame.area();
    frame.render_widget(Clear, area);
    let width = area.width.saturating_sub(4).clamp(1, 100);
    let height = area.height.saturating_sub(2).clamp(1, 30);
    let shell = Rect::new(
        area.x + area.width.saturating_sub(width) / 2,
        area.y + area.height.saturating_sub(height) / 2,
        width,
        height,
    );
    let rows = Layout::default()
        .direction(Direction::Vertical)
        .constraints([
            Constraint::Length(3),
            Constraint::Min(8),
            Constraint::Length(2),
        ])
        .split(shell);
    frame.render_widget(
        Paragraph::new(vec![
            Line::from(vec![
                Span::styled(
                    "Carina",
                    Style::default().fg(theme.text).add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    format!("  {}  ", theme.glyphs.separator()),
                    Style::default().fg(theme.border),
                ),
                Span::styled(
                    text(options.locale, MessageId::PhaseDiagnostics),
                    Style::default().fg(theme.danger),
                ),
            ]),
            Line::from(text(options.locale, MessageId::RuntimeUnavailable)),
        ]),
        rows[0],
    );
    let body = Block::default()
        .borders(Borders::ALL)
        .border_type(theme.glyphs.outer_border_type())
        .border_style(Style::default().fg(theme.border))
        .title(format!(" {} ", text(options.locale, MessageId::Runtime)));
    let inner = body.inner(rows[1]).inner(Margin::new(1, 1));
    frame.render_widget(body, rows[1]);
    let mut lines = vec![
        labelled(
            text(options.locale, MessageId::Workspace),
            &options.workspace.display().to_string(),
            theme,
        ),
        labelled(
            text(options.locale, MessageId::Runtime),
            &options.runtime_id,
            theme,
        ),
        labelled(
            text(options.locale, MessageId::Status),
            &options.missing_methods.join(", "),
            theme,
        ),
    ];
    if !options.obligations.is_empty() {
        lines.push(labelled(
            text(options.locale, MessageId::ExecutionStatus),
            &options.obligations.join(", "),
            theme,
        ));
    }
    if retry_failed {
        lines.push(Line::from(""));
        lines.push(Line::from(Span::styled(
            text(options.locale, MessageId::RuntimeUnavailable),
            Style::default().fg(theme.warning),
        )));
    }
    if show_logs {
        lines.push(Line::from(""));
        lines.extend(runtime_log_lines(&options.log_path, inner.width as usize));
    }
    frame.render_widget(
        Paragraph::new(lines)
            .wrap(Wrap { trim: false })
            .style(Style::default().fg(theme.text)),
        inner,
    );
    frame.render_widget(
        Paragraph::new(Line::from(vec![
            Span::styled(" Enter/r ", theme.selected()),
            Span::raw(format!("{}   ", text(options.locale, MessageId::Retry))),
            Span::styled(" d ", theme.selected()),
            Span::raw(format!(
                "{}   ",
                text(options.locale, MessageId::ViewDetails)
            )),
            Span::styled(" Esc/q ", theme.selected()),
            Span::raw(text(options.locale, MessageId::Close)),
        ])),
        rows[2],
    );
}

fn labelled(label: &str, value: &str, theme: Theme) -> Line<'static> {
    Line::from(vec![
        Span::styled(format!("{label:<18}"), Style::default().fg(theme.muted)),
        Span::styled(value.to_owned(), Style::default().fg(theme.text)),
    ])
}

fn runtime_log_lines(path: &PathBuf, width: usize) -> Vec<Line<'static>> {
    let Ok(bytes) = fs::read(path) else {
        return Vec::new();
    };
    let start = bytes.len().saturating_sub(16 * 1024);
    String::from_utf8_lossy(&bytes[start..])
        .lines()
        .rev()
        .take(8)
        .collect::<Vec<_>>()
        .into_iter()
        .rev()
        .map(|line| Line::from(line.chars().take(width).collect::<String>()))
        .collect()
}

struct DiagnosticTerminal {
    terminal: Terminal<CrosstermBackend<io::Stdout>>,
    sync_output: SyncOutputSupport,
    alternate: bool,
}

impl DiagnosticTerminal {
    fn enter(no_alt_screen: bool) -> Result<Self> {
        enable_raw_mode()?;
        let mut stdout = io::stdout();
        if !no_alt_screen {
            execute!(stdout, EnterAlternateScreen)?;
        }
        let mut terminal = Terminal::new(CrosstermBackend::new(stdout))?;
        terminal.hide_cursor()?;
        Ok(Self {
            terminal,
            sync_output: SyncOutputSupport::detect(),
            alternate: !no_alt_screen,
        })
    }

    fn draw(&mut self, render: impl FnOnce(&mut ratatui::Frame<'_>)) -> Result<()> {
        if self.sync_output.enabled() {
            with_synchronized_output(&mut self.terminal, |terminal| {
                terminal.draw(render).map(|_| ())
            })?;
        } else {
            self.terminal.draw(render)?;
        }
        Ok(())
    }
}

impl Drop for DiagnosticTerminal {
    fn drop(&mut self) {
        let _ = disable_raw_mode();
        if self.alternate {
            let _ = execute!(self.terminal.backend_mut(), LeaveAlternateScreen);
        }
        let _ = self.terminal.show_cursor();
    }
}
