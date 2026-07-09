//! carina-pty — real interactive pseudo-terminal (PRD §8.5, §8.7).
//!
//! Opens a POSIX pty, forks a child on the slave side, sets the window size
//! (resize), streams the child's output, forwards stdin (write), enforces a
//! timeout, and reaps the child so no zombie survives (kill/close).
//!
//! Usage:
//!   carina-pty [--cols N] [--rows N] [--timeout-ms N] [--json] -- <program> [args...]
//!
//! Default is raw mode (transparent terminal). With --json, output is
//! emitted as {"stream":"pty","chunk":"..."} lines plus a final
//! {"exit_code":N,"timed_out":bool}.

const std = @import("std");
const jsonl = @import("jsonl");

const c = @cImport({
    // glibc hides posix_openpt/grantpt/unlockpt/ptsname behind X/Open feature
    // macros; harmless on macOS/BSD where they are always visible.
    @cDefine("_XOPEN_SOURCE", "600");
    @cDefine("_DEFAULT_SOURCE", "1");
    @cInclude("stdlib.h");
    @cInclude("unistd.h");
    @cInclude("fcntl.h");
    @cInclude("sys/ioctl.h");
    @cInclude("termios.h");
    @cInclude("sys/wait.h");
    @cInclude("poll.h");
    @cInclude("signal.h");
});

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);

    var cols: u16 = 80;
    var rows: u16 = 24;
    var timeout_ms: i64 = -1;
    var json_mode = false;
    var argv_start: usize = 0;

    var i: usize = 1;
    while (i < args.len) : (i += 1) {
        const a = args[i];
        if (std.mem.eql(u8, a, "--cols") and i + 1 < args.len) {
            i += 1;
            cols = std.fmt.parseInt(u16, args[i], 10) catch 80;
        } else if (std.mem.eql(u8, a, "--rows") and i + 1 < args.len) {
            i += 1;
            rows = std.fmt.parseInt(u16, args[i], 10) catch 24;
        } else if (std.mem.eql(u8, a, "--timeout-ms") and i + 1 < args.len) {
            i += 1;
            timeout_ms = std.fmt.parseInt(i64, args[i], 10) catch -1;
        } else if (std.mem.eql(u8, a, "--json")) {
            json_mode = true;
        } else if (std.mem.eql(u8, a, "--")) {
            argv_start = i + 1;
            break;
        } else {
            argv_start = i;
            break;
        }
    }

    if (argv_start == 0 or argv_start >= args.len) {
        try jsonl.writeLine("{\"error\":\"usage: carina-pty [--cols N] [--rows N] [--timeout-ms N] [--json] -- <program> [args...]\"}");
        std.process.exit(2);
    }

    // 1. Open the pty master.
    const master = c.posix_openpt(c.O_RDWR | c.O_NOCTTY);
    if (master < 0) {
        try jsonl.writeLine("{\"error\":\"posix_openpt failed\"}");
        std.process.exit(1);
    }
    if (c.grantpt(master) != 0 or c.unlockpt(master) != 0) {
        try jsonl.writeLine("{\"error\":\"grantpt/unlockpt failed\"}");
        std.process.exit(1);
    }
    const sname = c.ptsname(master);
    if (sname == null) {
        try jsonl.writeLine("{\"error\":\"ptsname failed\"}");
        std.process.exit(1);
    }

    // 2. Fork; child runs on the slave side as a session leader.
    const pid = c.fork();
    if (pid < 0) {
        try jsonl.writeLine("{\"error\":\"fork failed\"}");
        std.process.exit(1);
    }
    if (pid == 0) {
        _ = c.setsid();
        const slave = c.open(sname, c.O_RDWR);
        if (slave < 0) c._exit(127);

        // Window size (resize capability).
        var ws: c.winsize = .{ .ws_row = rows, .ws_col = cols, .ws_xpixel = 0, .ws_ypixel = 0 };
        _ = c.ioctl(slave, c.TIOCSWINSZ, &ws);
        _ = c.ioctl(slave, c.TIOCSCTTY, @as(c_int, 0));

        _ = c.dup2(slave, 0);
        _ = c.dup2(slave, 1);
        _ = c.dup2(slave, 2);
        if (slave > 2) _ = c.close(slave);
        _ = c.close(master);

        // Build a null-terminated argv for execvp (C char *const argv[]).
        const cargv = allocator.alloc([*c]u8, args.len - argv_start + 1) catch c._exit(127);
        for (args[argv_start..], 0..) |arg, idx| {
            cargv[idx] = @constCast(arg.ptr);
        }
        cargv[cargv.len - 1] = null;
        _ = c.execvp(args[argv_start].ptr, cargv.ptr);
        c._exit(127); // execvp only returns on failure
    }

    // 3. Parent: relay master <-> stdin/stdout with a timeout.
    const started = std.time.milliTimestamp();
    var timed_out = false;
    var buf: [8192]u8 = undefined;

    var watch_stdin = true;
    while (true) {
        var fds = [_]c.pollfd{
            .{ .fd = master, .events = c.POLLIN, .revents = 0 },
            .{ .fd = 0, .events = if (watch_stdin) c.POLLIN else 0, .revents = 0 },
        };

        var poll_timeout: c_int = -1;
        if (timeout_ms >= 0) {
            const elapsed = std.time.milliTimestamp() - started;
            const remaining = timeout_ms - elapsed;
            if (remaining <= 0) {
                timed_out = true;
                break;
            }
            poll_timeout = @intCast(remaining);
        }

        const n = c.poll(&fds, 2, poll_timeout);
        if (n == 0) {
            timed_out = true;
            break;
        }
        if (n < 0) break;

        // Child output -> stdout.
        if (fds[0].revents & c.POLLIN != 0) {
            const got = c.read(master, &buf, buf.len);
            if (got <= 0) break; // slave closed -> child gone
            try emit(allocator, buf[0..@intCast(got)], json_mode);
        }
        if (fds[0].revents & (c.POLLHUP | c.POLLERR) != 0) break;

        // stdin -> child.
        if (fds[1].revents & c.POLLIN != 0) {
            const got = c.read(0, &buf, buf.len);
            if (got <= 0) {
                watch_stdin = false; // our stdin closed; keep draining child
            } else {
                var written: usize = 0;
                const total: usize = @intCast(got);
                while (written < total) {
                    const w = c.write(master, buf[written..].ptr, total - written);
                    if (w <= 0) break;
                    written += @intCast(w);
                }
            }
        }
    }

    // 4. Timeout -> kill the child's process group; then reap (no zombie).
    if (timed_out) {
        _ = c.kill(pid, c.SIGKILL);
    }
    _ = c.close(master);

    var status: c_int = 0;
    _ = c.waitpid(pid, &status, 0);
    const exit_code: i64 = if (c.WIFEXITED(status)) @intCast(c.WEXITSTATUS(status)) else -1;

    try jsonl.printLine(
        allocator,
        "{{\"exit_code\":{d},\"timed_out\":{},\"cols\":{d},\"rows\":{d}}}",
        .{ exit_code, timed_out, cols, rows },
    );
}

fn emit(allocator: std.mem.Allocator, data: []const u8, json_mode: bool) !void {
    if (!json_mode) {
        var written: usize = 0;
        while (written < data.len) {
            written += try std.posix.write(std.posix.STDOUT_FILENO, data[written..]);
        }
        return;
    }
    const escaped = try jsonl.escape(allocator, data);
    try jsonl.printLine(allocator, "{{\"stream\":\"pty\",\"chunk\":\"{s}\"}}", .{escaped});
}
