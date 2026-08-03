#![cfg(unix)]

use std::fs::File;
use std::io::Write;
use std::os::fd::FromRawFd;
use std::process::{Command, Stdio};

use carina_tui::app::{AltScreenPolicy, ScreenMode, detected_screen_mode};
use crossterm::event::{KeyCode, KeyModifiers, read};

const PROBE_ENV: &str = "CARINA_TERMINAL_KEY_PROBE";
const SCREEN_PROBE_ENV: &str = "CARINA_SCREEN_MODE_PROBE";

#[test]
fn terminal_key_probe() {
    let Ok(expected) = std::env::var(PROBE_ENV) else {
        return;
    };
    crossterm::terminal::enable_raw_mode().expect("probe PTY supports raw mode");
    let event = read().expect("read terminal key event");
    crossterm::terminal::disable_raw_mode().expect("restore probe PTY mode");
    let crossterm::event::Event::Key(key) = event else {
        panic!("expected key event, got {event:?}");
    };
    assert_eq!(key.code, KeyCode::Enter);
    match expected.as_str() {
        "ghostty" => assert!(key.modifiers.contains(KeyModifiers::CONTROL)),
        "vscode" => assert!(key.modifiers.contains(KeyModifiers::ALT)),
        other => panic!("unknown probe profile {other}"),
    }
}

#[test]
fn terminal_screen_mode_probe() {
    let Ok(expected) = std::env::var(SCREEN_PROBE_ENV) else {
        return;
    };
    assert!(std::io::IsTerminal::is_terminal(&std::io::stdin()));
    let expected = match expected.as_str() {
        "minimal" => ScreenMode::Minimal,
        "inline" => ScreenMode::Inline,
        other => panic!("unknown screen mode {other}"),
    };
    assert_eq!(detected_screen_mode(false, AltScreenPolicy::Auto), expected);
}

fn run_pty_probe(profile: &str, bytes: &[u8]) {
    let mut master_fd = -1;
    let mut slave_fd = -1;
    let status = unsafe {
        libc::openpty(
            &mut master_fd,
            &mut slave_fd,
            std::ptr::null_mut(),
            std::ptr::null_mut(),
            std::ptr::null_mut(),
        )
    };
    assert_eq!(status, 0, "open PTY pair");
    let mut master = unsafe { File::from_raw_fd(master_fd) };
    let slave = unsafe { File::from_raw_fd(slave_fd) };
    let child = Command::new(std::env::current_exe().expect("test executable"))
        .args(["--exact", "terminal_key_probe", "--nocapture"])
        .env(PROBE_ENV, profile)
        .stdin(Stdio::from(slave))
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn PTY key probe");
    master.write_all(bytes).expect("write terminal key bytes");
    master.flush().expect("flush terminal key bytes");
    let output = child.wait_with_output().expect("wait for PTY key probe");
    assert!(
        output.status.success(),
        "PTY probe failed: stdout={} stderr={}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn run_screen_mode_probe(expected: &str, environment: &[(&str, &str)]) {
    let mut master_fd = -1;
    let mut slave_fd = -1;
    let status = unsafe {
        libc::openpty(
            &mut master_fd,
            &mut slave_fd,
            std::ptr::null_mut(),
            std::ptr::null_mut(),
            std::ptr::null_mut(),
        )
    };
    assert_eq!(status, 0, "open PTY pair");
    let _master = unsafe { File::from_raw_fd(master_fd) };
    let slave = unsafe { File::from_raw_fd(slave_fd) };
    let mut command = Command::new(std::env::current_exe().expect("test executable"));
    command
        .args(["--exact", "terminal_screen_mode_probe", "--nocapture"])
        .env_clear()
        .env("PATH", std::env::var_os("PATH").unwrap_or_default())
        .env(SCREEN_PROBE_ENV, expected)
        .stdin(Stdio::from(slave))
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    for (key, value) in environment {
        command.env(key, value);
    }
    let output = command
        .spawn()
        .expect("spawn PTY screen-mode probe")
        .wait_with_output()
        .expect("wait for PTY screen-mode probe");
    assert!(
        output.status.success(),
        "PTY screen-mode probe failed: stdout={} stderr={}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

#[test]
fn ghostty_pty_decodes_enhanced_ctrl_enter() {
    run_pty_probe("ghostty", b"\x1b[13;5u");
}

#[test]
fn vscode_pty_decodes_alt_enter_fallback() {
    run_pty_probe("vscode", b"\x1b[13;3u");
}

#[test]
fn screen_mode_pty_matrix_covers_supported_terminal_families() {
    run_screen_mode_probe(
        "minimal",
        &[("TERM", "xterm-ghostty"), ("TERM_PROGRAM", "ghostty")],
    );
    run_screen_mode_probe(
        "minimal",
        &[("TERM", "xterm-256color"), ("TERM_PROGRAM", "iTerm.app")],
    );
    run_screen_mode_probe(
        "minimal",
        &[("TERM", "wezterm"), ("TERM_PROGRAM", "WezTerm")],
    );
    run_screen_mode_probe(
        "inline",
        &[
            ("TERM", "xterm-256color"),
            ("SSH_CONNECTION", "client server"),
        ],
    );
    run_screen_mode_probe(
        "minimal",
        &[("TERM", "screen-256color"), ("TMUX", "/tmp/tmux")],
    );
    run_screen_mode_probe("minimal", &[("TERM", "xterm-256color"), ("NO_COLOR", "1")]);
    run_screen_mode_probe("inline", &[("TERM", "dumb")]);
}
