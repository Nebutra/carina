//! Best-effort terminal default-color probing performed before event handling.

use std::io::{IsTerminal, Read, Write};
use std::time::{Duration, Instant};

pub fn background(timeout: Duration) -> Option<[u8; 3]> {
    if !std::io::stdin().is_terminal() || !std::io::stdout().is_terminal() {
        return None;
    }
    probe(timeout).ok().flatten()
}

#[cfg(unix)]
fn probe(timeout: Duration) -> std::io::Result<Option<[u8; 3]>> {
    use std::os::fd::AsRawFd;
    crossterm::terminal::enable_raw_mode()?;
    struct RawGuard;
    impl Drop for RawGuard {
        fn drop(&mut self) {
            let _ = crossterm::terminal::disable_raw_mode();
        }
    }
    let _guard = RawGuard;
    let mut stdout = std::io::stdout().lock();
    stdout.write_all(b"\x1b]11;?\x07")?;
    stdout.flush()?;
    let mut stdin = std::io::stdin().lock();
    let fd = stdin.as_raw_fd();
    let deadline = Instant::now() + timeout;
    let mut response = Vec::with_capacity(64);
    while Instant::now() < deadline && response.len() < 128 {
        let remaining = deadline.saturating_duration_since(Instant::now());
        let mut descriptor = libc::pollfd {
            fd,
            events: libc::POLLIN,
            revents: 0,
        };
        // SAFETY: `descriptor` points to one initialized pollfd for this call.
        let ready = unsafe {
            libc::poll(
                &mut descriptor,
                1,
                remaining.as_millis().min(i32::MAX as u128) as i32,
            )
        };
        if ready <= 0 {
            break;
        }
        let mut byte = [0_u8; 1];
        if stdin.read(&mut byte)? == 0 {
            break;
        }
        response.push(byte[0]);
        if byte[0] == 0x07 || response.ends_with(b"\x1b\\") {
            break;
        }
    }
    Ok(parse_osc11(&response))
}

#[cfg(not(unix))]
fn probe(_timeout: Duration) -> std::io::Result<Option<[u8; 3]>> {
    Ok(None)
}

pub fn parse_osc11(bytes: &[u8]) -> Option<[u8; 3]> {
    let value = std::str::from_utf8(bytes).ok()?;
    let payload = value
        .split("rgb:")
        .nth(1)?
        .trim_end_matches(['\x07', '\x1b', '\\']);
    let mut channels = payload.split('/');
    let parse = |value: &str| {
        let raw = u16::from_str_radix(value.trim(), 16).ok()?;
        Some(if value.trim().len() <= 2 {
            raw as u8
        } else {
            (raw >> 8) as u8
        })
    };
    Some([
        parse(channels.next()?)?,
        parse(channels.next()?)?,
        parse(channels.next()?)?,
    ])
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn parses_four_digit_bel_response() {
        assert_eq!(
            parse_osc11(b"\x1b]11;rgb:ffff/8080/0000\x07"),
            Some([255, 128, 0])
        );
    }
    #[test]
    fn parses_two_digit_st_response() {
        assert_eq!(
            parse_osc11(b"\x1b]11;rgb:12/34/56\x1b\\"),
            Some([0x12, 0x34, 0x56])
        );
    }
    #[test]
    fn rejects_unrelated_input() {
        assert_eq!(parse_osc11(b"hello"), None);
    }
}
