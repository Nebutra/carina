//! SPIKE — ratatui TUI spike for docs/plans/tui-stack-decision.md §4.3.
//! Disposable. Four scenarios, one per gate:
//!   --scenario live      G1: real daemon, session.events.stream -> scrolling transcript
//!   --scenario approval  G2: patch diff approval prompt + decision_id roundtrip + resume
//!   --scenario cjk       G3: mixed zh/en transcript + input box fed CJK bytes via PTY
//!   --scenario burst     G4: synthetic 100 ev/s x 10s, p95 frame render + idle CPU
//! Run under tmux/carina-pty; gate scripts live in spikes/tui-ratatui/.

mod diff;
mod rpc;

use ratatui::crossterm::event::{self, Event, KeyCode, KeyEventKind, KeyModifiers};
use ratatui::layout::{Constraint, Layout, Position, Rect};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, Clear, Paragraph};
use serde_json::{json, Value};
use std::io::Write as _;
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use unicode_width::UnicodeWidthStr;

// ---------------------------------------------------------------- messages

enum Msg {
    DaemonEvent(Value),
    Synthetic(String),
    Approval(Approval),
    Info(String),
    Error(String),
    BurstDone { sent: usize, wall_ms: u128 },
}

#[derive(Clone)]
struct Approval {
    title: String,
    diff_body: Option<String>,  // unified diff (patch approvals)
    plain_body: Option<String>, // canonicalized command (exec approvals)
    decision_id: Option<String>,
    patch_id: Option<String>,
    shown_at: Option<Instant>,
}

enum LineKind {
    Event,
    Info,
    Error,
    Resume,
}

struct Entry {
    kind: LineKind,
    text: String,
}

// ---------------------------------------------------------------- app state

struct App {
    scenario: String,
    sock: String,
    session_id: String,
    client: Option<Arc<Mutex<rpc::Client>>>,
    transcript: Vec<Entry>,
    input: String,
    input_cursor: usize, // char index
    approval: Option<Approval>,
    events_seen: usize,
    draw_ms: Vec<f64>,
    burst_draw_ms: Vec<f64>,
    burst_active: bool,
    burst_result: Option<(usize, u128)>,
    perf_out: Option<String>,
    evidence_out: Option<String>,
    auto_ms: u64,
    started: Instant,
    exit_after_secs: u64,
    quit: bool,
}

impl App {
    fn push(&mut self, kind: LineKind, text: String) {
        self.transcript.push(Entry { kind, text });
    }

    fn evidence(&self, v: Value) {
        if let Some(path) = &self.evidence_out {
            if let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(path) {
                let _ = writeln!(f, "{}", v);
            }
        }
    }

    fn write_perf(&self, label: &str) {
        let Some(path) = &self.perf_out else { return };
        let stats = |v: &Vec<f64>| -> Value {
            if v.is_empty() {
                return json!(null);
            }
            let mut s = v.clone();
            s.sort_by(|a, b| a.partial_cmp(b).unwrap());
            let pct = |p: f64| s[((s.len() as f64 * p).ceil() as usize).min(s.len()) - 1];
            json!({
                "frames": s.len(),
                "p50_ms": pct(0.50),
                "p95_ms": pct(0.95),
                "p99_ms": pct(0.99),
                "max_ms": s[s.len() - 1],
                "mean_ms": s.iter().sum::<f64>() / s.len() as f64,
            })
        };
        let out = json!({
            "label": label,
            "scenario": self.scenario,
            "pid": std::process::id(),
            "events_seen": self.events_seen,
            "draw_all": stats(&self.draw_ms),
            "draw_during_burst": stats(&self.burst_draw_ms),
            "burst": self.burst_result.map(|(sent, wall)| json!({
                "events_sent": sent, "wall_ms": wall,
                "rate_per_sec": sent as f64 / (wall as f64 / 1000.0),
            })),
        });
        let _ = std::fs::write(path, serde_json::to_string_pretty(&out).unwrap());
    }
}

// ---------------------------------------------------------------- event -> line

fn event_line(ev: &Value) -> String {
    let ts = ev["timestamp"].as_str().unwrap_or("");
    let ts = ts.get(11..19).unwrap_or(ts); // HH:MM:SS
    let typ = ev["type"].as_str().unwrap_or("?");
    let actor = ev["actor"].as_str().unwrap_or("-");
    let p = &ev["payload"];
    let detail = match typ {
        "CommandStarted" => format!("$ {}", p["command"].as_str().unwrap_or("")),
        "CommandOutput" => format!("| {}", p["chunk"].as_str().unwrap_or("").replace('\n', " ⏎ ")),
        "CommandExited" => format!("exit={} {}ms", p["exit_code"], p["duration_ms"]),
        "permission.request" => format!(
            "capability={} decision_id={}",
            ev["capability"].as_str().unwrap_or("?"),
            ev["decision_id"].as_str().unwrap_or("?")
        ),
        _ => {
            let mut s = p.to_string();
            if s == "null" {
                s = String::new();
            }
            if s.len() > 110 {
                s.truncate(110);
                s.push('…');
            }
            s
        }
    };
    format!("{ts} {typ:<16} [{actor}] {detail}")
}

// ---------------------------------------------------------------- scenarios

fn setup_session(client: &Arc<Mutex<rpc::Client>>, workspace: &str) -> Result<String, String> {
    let sess = client
        .lock()
        .unwrap()
        .call(
            "session.create",
            json!({"workspace_root": workspace, "profile": "safe-edit", "approval_mode": "on_request"}),
        )
        .map_err(|e| e.to_string())?;
    sess["session_id"]
        .as_str()
        .map(str::to_string)
        .ok_or_else(|| format!("no session_id in {sess}"))
}

fn spawn_stream(sock: String, session_id: String, tx: Sender<Msg>) {
    std::thread::spawn(move || {
        let etx = tx.clone();
        let fwd = channel();
        let sock2 = sock.clone();
        let sid = session_id.clone();
        std::thread::spawn(move || {
            if let Err(e) = rpc::stream_events(&sock2, &sid, fwd.0) {
                // surfaced on the UI as an error line
                eprintln!("stream error: {e}");
            }
        });
        for ev in fwd.1 {
            if etx.send(Msg::DaemonEvent(ev)).is_err() {
                break;
            }
        }
    });
}

/// G1 driver: issue a series of allowlisted (risk-0) commands so the daemon
/// publishes real CommandStarted/Output/Exited events onto the stream.
fn spawn_live_driver(sock: String, session_id: String, tx: Sender<Msg>) {
    std::thread::spawn(move || {
        let cmds: Vec<Vec<&str>> = vec![
            vec!["ls"],
            vec!["cat", "hello.txt"],
            vec!["echo", "补丁干净落地。无惊无险，本该如此。"],
            vec!["date"],
            vec!["echo", "审计链校验通过：1,204 条记录"],
            vec!["pwd"],
            vec!["git", "status"],
            vec!["true"],
        ];
        std::thread::sleep(Duration::from_millis(600));
        let mut client = match rpc::Client::dial(&sock) {
            Ok(c) => c,
            Err(e) => {
                let _ = tx.send(Msg::Error(format!("driver dial: {e}")));
                return;
            }
        };
        for argv in cmds {
            let _ = tx.send(Msg::Info(format!("driver: command.exec {argv:?}")));
            let res = client.call(
                "command.exec",
                json!({"session_id": session_id, "argv": argv}),
            );
            if let Err(e) = res {
                let _ = tx.send(Msg::Error(format!("command.exec: {e}")));
            }
            std::thread::sleep(Duration::from_millis(400));
        }
        let _ = tx.send(Msg::Info("driver: done (8 commands)".into()));
    });
}

/// G2 driver: propose a real patch, then hand the approval to the UI.
fn spawn_approval_driver(
    client: Arc<Mutex<rpc::Client>>,
    session_id: String,
    tx: Sender<Msg>,
) {
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(800));
        let new_content = "hello from carina\n审批测试：这一行由 ratatui spike 写入。\nsecond line kept intact\n";
        let res = client.lock().unwrap().call(
            "workspace.patch.propose",
            json!({"session_id": session_id, "reason": "spike G2 approval demo",
                   "files": [{"path": "hello.txt", "new_content": new_content}]}),
        );
        match res {
            Ok(patch) => {
                let _ = tx.send(Msg::Info(format!(
                    "patch proposed: {} status={} risk={}",
                    patch["patch_id"].as_str().unwrap_or("?"),
                    patch["approval_status"].as_str().unwrap_or("?"),
                    patch["risk_level"]
                )));
                let _ = tx.send(Msg::Approval(Approval {
                    title: format!(
                        "PatchApply · {} · files {}",
                        patch["patch_id"].as_str().unwrap_or("?"),
                        patch["affected_files"].to_string()
                    ),
                    diff_body: patch["diff"].as_str().map(str::to_string),
                    plain_body: None,
                    decision_id: None,
                    patch_id: patch["patch_id"].as_str().map(str::to_string),
                    shown_at: None,
                }));
            }
            Err(e) => {
                let _ = tx.send(Msg::Error(format!("patch.propose: {e}")));
            }
        }
    });
}

/// G4 driver: synthetic burst, 100 lines/sec for 10 s on a fixed schedule.
fn spawn_burst(tx: Sender<Msg>) {
    std::thread::spawn(move || {
        let total = 1000usize;
        let interval = Duration::from_millis(10);
        let start = Instant::now();
        let samples = [
            "补丁干净落地。无惊无险，本该如此。",
            "audit chain verified: 1,204 records intact",
            "审计链校验通过：1,204 条记录",
            "kernel verdict: read-only-allow (collapsed)",
            "模型路由:降级到本地推理,延迟 42ms",
            "scheduler: task tsk_9f2 waiting_approval -> running",
        ];
        for i in 0..total {
            let target = start + interval * (i as u32);
            let now = Instant::now();
            if target > now {
                std::thread::sleep(target - now);
            }
            let line = format!("evt {:04} {}", i, samples[i % samples.len()]);
            if tx.send(Msg::Synthetic(line)).is_err() {
                return;
            }
        }
        let _ = tx.send(Msg::BurstDone { sent: total, wall_ms: start.elapsed().as_millis() });
    });
}

// ---------------------------------------------------------------- approval actions

fn resolve_approval(app: &mut App, allow: bool) {
    let Some(ap) = app.approval.take() else { return };
    let Some(client) = app.client.clone() else { return };
    let sid = app.session_id.clone();

    if let Some(patch_id) = &ap.patch_id {
        // Patch flow: reviewable diff was the prompt body; allow => apply.
        if allow {
            let res = client.lock().unwrap().call(
                "workspace.patch.apply",
                json!({"session_id": sid, "patch_id": patch_id, "approver": "operator"}),
            );
            match res {
                Ok(p) => {
                    app.push(
                        LineKind::Resume,
                        format!(
                            "RESUME: patch {} applied, status={} new_hash={}",
                            patch_id,
                            p["status"].as_str().unwrap_or("?"),
                            p["new_hash"].as_str().unwrap_or("?")
                        ),
                    );
                    app.evidence(json!({"gate":"G2","step":"patch.apply","patch_id":patch_id,"result":p}));
                }
                Err(e) => app.push(LineKind::Error, format!("patch.apply: {e}")),
            }
            // Stage 2: a requires_approval command => real daemon-side pending
            // approval object with a decision_id (pendingCmds in go/daemon).
            let res = client.lock().unwrap().call(
                "command.exec",
                json!({"session_id": sid, "argv": ["touch", "spike-approved.txt"]}),
            );
            match res {
                Ok(r) => {
                    let dec = &r["decision"];
                    if dec["decision"] == "requires_approval" {
                        let decision_id = dec["decision_id"].as_str().unwrap_or("").to_string();
                        app.push(
                            LineKind::Info,
                            format!(
                                "command.exec paused: requires_approval decision_id={decision_id} ({})",
                                dec["reason"].as_str().unwrap_or("")
                            ),
                        );
                        app.evidence(json!({"gate":"G2","step":"command.pending","decision":dec}));
                        app.approval = Some(Approval {
                            title: format!("CommandExec · decision {decision_id}"),
                            diff_body: None,
                            plain_body: Some(format!(
                                "$ touch spike-approved.txt\n\nreason: {}\ncapability: {}",
                                dec["reason"].as_str().unwrap_or(""),
                                dec["capability"].as_str().unwrap_or("CommandExec"),
                            )),
                            decision_id: Some(decision_id),
                            patch_id: None,
                            shown_at: Some(Instant::now()),
                        });
                    } else {
                        app.push(LineKind::Info, format!("command.exec: {}", r["decision"]));
                    }
                }
                Err(e) => app.push(LineKind::Error, format!("command.exec: {e}")),
            }
        } else {
            app.push(LineKind::Resume, format!("patch {patch_id} NOT applied (denied in review)"));
            app.evidence(json!({"gate":"G2","step":"patch.deny","patch_id":patch_id}));
        }
        return;
    }

    if let Some(decision_id) = &ap.decision_id {
        // Command flow: true decision_id roundtrip over RPC.
        let (method, params) = if allow {
            (
                "task.action.approve",
                json!({"session_id": sid, "decision_id": decision_id, "approver": "operator"}),
            )
        } else {
            (
                "task.action.deny",
                json!({"session_id": sid, "decision_id": decision_id, "approver": "operator", "reason": "spike deny"}),
            )
        };
        let res = client.lock().unwrap().call(method, params);
        match res {
            Ok(r) => {
                app.push(
                    LineKind::Resume,
                    format!(
                        "RESUME: {method} decision_id={decision_id} -> {} (exit={})",
                        r["decision"]["decision"].as_str().unwrap_or(r["decision"].as_str().unwrap_or("?")),
                        r["result"]["exit_code"]
                    ),
                );
                app.evidence(json!({"gate":"G2","step":method,"decision_id":decision_id,"result":r}));
            }
            Err(e) => app.push(LineKind::Error, format!("{method}: {e}")),
        }
    }
}

// ---------------------------------------------------------------- rendering

fn transcript_lines<'a>(app: &'a App) -> Vec<Line<'a>> {
    app.transcript
        .iter()
        .map(|e| {
            let style = match e.kind {
                LineKind::Event => Style::new().fg(Color::Gray),
                LineKind::Info => Style::new().fg(Color::Blue),
                LineKind::Error => Style::new().fg(Color::Red).add_modifier(Modifier::BOLD),
                LineKind::Resume => Style::new().fg(Color::Green).add_modifier(Modifier::BOLD),
            };
            Line::from(Span::styled(e.text.as_str(), style))
        })
        .collect()
}

fn centered(area: Rect, pct_x: u16, pct_y: u16) -> Rect {
    let v = Layout::vertical([
        Constraint::Percentage((100 - pct_y) / 2),
        Constraint::Percentage(pct_y),
        Constraint::Percentage((100 - pct_y) / 2),
    ])
    .split(area);
    let h = Layout::horizontal([
        Constraint::Percentage((100 - pct_x) / 2),
        Constraint::Percentage(pct_x),
        Constraint::Percentage((100 - pct_x) / 2),
    ])
    .split(v[1]);
    h[1]
}

fn draw(frame: &mut ratatui::Frame, app: &App) {
    let show_input = app.scenario == "cjk";
    let chunks = Layout::vertical([
        Constraint::Min(3),
        Constraint::Length(if show_input { 3 } else { 1 }),
        Constraint::Length(1),
    ])
    .split(frame.area());

    // Transcript with follow-scroll.
    let inner_h = chunks[0].height.saturating_sub(2) as usize;
    let lines = transcript_lines(app);
    let scroll = lines.len().saturating_sub(inner_h) as u16;
    let title = format!(
        " Carina · {} · {} events · {} lines ",
        app.scenario,
        app.events_seen,
        app.transcript.len()
    );
    frame.render_widget(
        Paragraph::new(lines)
            .block(Block::bordered().title(title))
            .scroll((scroll, 0)),
        chunks[0],
    );

    if show_input {
        frame.render_widget(
            Paragraph::new(app.input.as_str()).block(Block::bordered().title(" 输入 input (Esc quits) ")),
            chunks[1],
        );
        // Physical cursor at the logical caret cell (R13 architecture).
        let byte_idx = app
            .input
            .char_indices()
            .nth(app.input_cursor)
            .map(|(i, _)| i)
            .unwrap_or(app.input.len());
        let w = UnicodeWidthStr::width(&app.input[..byte_idx]) as u16;
        frame.set_cursor_position(Position::new(chunks[1].x + 1 + w, chunks[1].y + 1));
    } else {
        let status = format!(
            " session={} sock={} pid={} ",
            if app.session_id.is_empty() { "-" } else { &app.session_id },
            app.sock,
            std::process::id()
        );
        frame.render_widget(
            Paragraph::new(status).style(Style::new().fg(Color::DarkGray)),
            chunks[1],
        );
    }

    let help = if app.approval.is_some() {
        " [a] allow · [d] deny · approval pending "
    } else if show_input {
        " type CJK freely · Esc quits "
    } else {
        " [q] quit "
    };
    frame.render_widget(
        Paragraph::new(help).style(Style::new().fg(Color::Black).bg(Color::Cyan)),
        chunks[2],
    );

    // Approval overlay: colored unified diff as the prompt body.
    if let Some(ap) = &app.approval {
        let area = centered(frame.area(), 84, 76);
        frame.render_widget(Clear, area);
        let mut body: Vec<Line> = vec![
            Line::from(Span::styled(
                ap.title.clone(),
                Style::new().fg(Color::Yellow).add_modifier(Modifier::BOLD),
            )),
            Line::from(""),
        ];
        if let Some(d) = &ap.diff_body {
            body.extend(diff::colorize(d));
        }
        if let Some(p) = &ap.plain_body {
            body.extend(p.lines().map(|l| Line::from(Span::styled(l.to_string(), Style::new().fg(Color::White)))));
        }
        body.push(Line::from(""));
        body.push(Line::from(Span::styled(
            "[a] Allow once    [d] Deny with reason    (Esc keeps it pending)",
            Style::new().fg(Color::Cyan).add_modifier(Modifier::BOLD),
        )));
        frame.render_widget(
            Paragraph::new(body).block(
                Block::bordered()
                    .title(" APPROVAL REQUIRED · 需要审批 ")
                    .border_style(Style::new().fg(Color::Yellow)),
            ),
            area,
        );
    }
}

// ---------------------------------------------------------------- main loop

fn main() {
    let mut args = std::collections::HashMap::new();
    let argv: Vec<String> = std::env::args().skip(1).collect();
    let mut i = 0;
    while i + 1 < argv.len() + 1 {
        if i + 1 < argv.len() && argv[i].starts_with("--") {
            args.insert(argv[i].trim_start_matches("--").to_string(), argv[i + 1].clone());
            i += 2;
        } else {
            i += 1;
        }
    }
    let scenario = args.get("scenario").cloned().unwrap_or_else(|| "cjk".into());
    let sock = args.get("sock").cloned().unwrap_or_else(|| {
        format!("{}/.carina/daemon.sock", std::env::var("HOME").unwrap_or_default())
    });
    let workspace = args.get("workspace").cloned().unwrap_or_default();
    let exit_after_secs: u64 = args.get("exit-after-secs").and_then(|s| s.parse().ok()).unwrap_or(0);
    let auto_ms: u64 = args.get("auto-ms").and_then(|s| s.parse().ok()).unwrap_or(0);

    let (tx, rx): (Sender<Msg>, Receiver<Msg>) = channel();

    let mut app = App {
        scenario: scenario.clone(),
        sock: sock.clone(),
        session_id: String::new(),
        client: None,
        transcript: Vec::new(),
        input: String::new(),
        input_cursor: 0,
        approval: None,
        events_seen: 0,
        draw_ms: Vec::new(),
        burst_draw_ms: Vec::new(),
        burst_active: false,
        burst_result: None,
        perf_out: args.get("perf-out").cloned(),
        evidence_out: args.get("evidence-out").cloned(),
        auto_ms,
        started: Instant::now(),
        exit_after_secs,
        quit: false,
    };

    // Scenario wiring (before entering the alternate screen so errors print).
    match scenario.as_str() {
        "live" | "approval" => {
            let client = match rpc::Client::dial(&sock) {
                Ok(c) => Arc::new(Mutex::new(c)),
                Err(e) => {
                    eprintln!("cannot dial daemon at {sock}: {e}");
                    std::process::exit(1);
                }
            };
            let sid = match setup_session(&client, &workspace) {
                Ok(s) => s,
                Err(e) => {
                    eprintln!("session.create failed: {e}");
                    std::process::exit(1);
                }
            };
            app.push(LineKind::Info, format!("session created: {sid} (safe-edit/on_request)"));
            app.evidence(json!({"gate":"setup","session_id":sid,"scenario":scenario}));
            spawn_stream(sock.clone(), sid.clone(), tx.clone());
            if scenario == "live" {
                spawn_live_driver(sock.clone(), sid.clone(), tx.clone());
            } else {
                spawn_approval_driver(client.clone(), sid.clone(), tx.clone());
            }
            app.session_id = sid;
            app.client = Some(client);
        }
        "cjk" => {
            for l in [
                "会话已恢复。governed agent runtime — carina.",
                "补丁干净落地。无惊无险，本该如此。",
                "audit chain verified: OK (mixed english line)",
                "审计链校验通过：1,204 条记录",
                "策略引擎:safe-edit 模式,写操作仅限 PatchApply。",
                "half-width/全角 boundary test: abcABC字宽测试xyz。",
            ] {
                app.push(LineKind::Event, l.to_string());
            }
        }
        "burst" => {
            spawn_burst(tx.clone());
        }
        other => {
            eprintln!("unknown scenario {other}");
            std::process::exit(1);
        }
    }

    let mut terminal = ratatui::init();
    let mut dirty = true;

    while !app.quit {
        // Drain pending messages.
        while let Ok(msg) = rx.try_recv() {
            dirty = true;
            match msg {
                Msg::DaemonEvent(ev) => {
                    app.events_seen += 1;
                    app.push(LineKind::Event, event_line(&ev));
                }
                Msg::Synthetic(line) => {
                    app.events_seen += 1;
                    app.burst_active = true;
                    app.push(LineKind::Event, line);
                }
                Msg::Approval(mut ap) => {
                    ap.shown_at = Some(Instant::now());
                    app.approval = Some(ap);
                }
                Msg::Info(s) => app.push(LineKind::Info, s),
                Msg::Error(s) => app.push(LineKind::Error, s),
                Msg::BurstDone { sent, wall_ms } => {
                    app.burst_active = false;
                    app.burst_result = Some((sent, wall_ms));
                    app.push(
                        LineKind::Resume,
                        format!("burst done: {sent} events in {wall_ms}ms — now idle (perf written)"),
                    );
                    app.write_perf("post-burst");
                }
            }
        }

        // Auto-approve (headless fallback; gate scripts prefer tmux send-keys).
        if app.auto_ms > 0 {
            if let Some(ap) = &app.approval {
                if ap.shown_at.map(|t| t.elapsed().as_millis() as u64 > app.auto_ms).unwrap_or(false) {
                    resolve_approval(&mut app, true);
                    dirty = true;
                }
            }
        }

        if app.exit_after_secs > 0 && app.started.elapsed().as_secs() >= app.exit_after_secs {
            app.quit = true;
        }

        if dirty {
            let t0 = Instant::now();
            terminal.draw(|f| draw(f, &app)).ok();
            let ms = t0.elapsed().as_secs_f64() * 1000.0;
            app.draw_ms.push(ms);
            if app.burst_active {
                app.burst_draw_ms.push(ms);
            }
            dirty = false;
        }

        // Input (50 ms poll keeps idle CPU near zero; render gate is measured
        // on draw duration, not poll cadence).
        if event::poll(Duration::from_millis(50)).unwrap_or(false) {
            match event::read() {
                Ok(Event::Key(k)) if k.kind != KeyEventKind::Release => {
                    dirty = true;
                    let in_input = app.scenario == "cjk";
                    match k.code {
                        KeyCode::Char('c') if k.modifiers.contains(KeyModifiers::CONTROL) => {
                            app.quit = true;
                        }
                        KeyCode::Esc => {
                            if in_input {
                                app.quit = true;
                            } // else: approval stays pending (never locks)
                        }
                        KeyCode::Char('a') if app.approval.is_some() && !in_input => {
                            resolve_approval(&mut app, true);
                        }
                        KeyCode::Char('d') if app.approval.is_some() && !in_input => {
                            resolve_approval(&mut app, false);
                        }
                        KeyCode::Char('q') if !in_input => app.quit = true,
                        KeyCode::Char(c) if in_input => {
                            let byte_idx = app
                                .input
                                .char_indices()
                                .nth(app.input_cursor)
                                .map(|(i, _)| i)
                                .unwrap_or(app.input.len());
                            app.input.insert(byte_idx, c);
                            app.input_cursor += 1;
                        }
                        KeyCode::Backspace if in_input && app.input_cursor > 0 => {
                            app.input_cursor -= 1;
                            let byte_idx = app
                                .input
                                .char_indices()
                                .nth(app.input_cursor)
                                .map(|(i, _)| i)
                                .unwrap();
                            app.input.remove(byte_idx);
                        }
                        KeyCode::Left if in_input && app.input_cursor > 0 => app.input_cursor -= 1,
                        KeyCode::Right if in_input && app.input_cursor < app.input.chars().count() => {
                            app.input_cursor += 1
                        }
                        KeyCode::Enter if in_input => {
                            let line = std::mem::take(&mut app.input);
                            app.input_cursor = 0;
                            app.push(LineKind::Info, format!("you typed: {line}"));
                        }
                        _ => {}
                    }
                }
                Ok(Event::Resize(_, _)) => dirty = true,
                _ => {}
            }
        }
    }

    ratatui::restore();
    app.write_perf("exit");
    eprintln!(
        "spike exit: scenario={} events={} frames={}",
        app.scenario,
        app.events_seen,
        app.draw_ms.len()
    );
}
