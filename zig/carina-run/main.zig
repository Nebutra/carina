//! carina-run — mediated command runner (PRD §8.5, §8.8).
//!
//! Captures stdout/stderr, enforces a wall-clock timeout, supports cwd and
//! an env allowlist. Only ever invoked after a kernel CommandExec decision.
//!
//! Usage: carina-run [--cwd DIR] [--timeout-ms N] [--env NAME]... -- <program> [args...]
//! Output: {"stream":"stdout|stderr","chunk":"..."} per line, then
//!         {"exit_code":N,"duration_ms":N,"timed_out":bool}

const std = @import("std");
const builtin = @import("builtin");
const jsonl = @import("jsonl");

const max_output = 64 * 1024 * 1024;

var child_pid: std.atomic.Value(i32) = std.atomic.Value(i32).init(0);
var finished: std.atomic.Value(bool) = std.atomic.Value(bool).init(false);
var timed_out: std.atomic.Value(bool) = std.atomic.Value(bool).init(false);

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);

    var cwd: ?[]const u8 = null;
    var timeout_ms: u64 = 120_000;
    var env_names = std.ArrayList([]const u8){};
    var sandbox = false;
    var argv_start: usize = 0;

    var i: usize = 1;
    while (i < args.len) : (i += 1) {
        const arg = args[i];
        if (std.mem.eql(u8, arg, "--cwd") and i + 1 < args.len) {
            i += 1;
            cwd = args[i];
        } else if (std.mem.eql(u8, arg, "--timeout-ms") and i + 1 < args.len) {
            i += 1;
            timeout_ms = std.fmt.parseInt(u64, args[i], 10) catch 120_000;
        } else if (std.mem.eql(u8, arg, "--env") and i + 1 < args.len) {
            i += 1;
            try env_names.append(allocator, args[i]);
        } else if (std.mem.eql(u8, arg, "--sandbox")) {
            sandbox = true;
        } else if (std.mem.eql(u8, arg, "--")) {
            argv_start = i + 1;
            break;
        } else {
            argv_start = i;
            break;
        }
    }

    if (argv_start == 0 or argv_start >= args.len) {
        try jsonl.writeLine("{\"error\":\"usage: carina-run [--cwd DIR] [--timeout-ms N] [--env NAME]... -- <program> [args...]\"}");
        std.process.exit(2);
    }

    const started = std.time.milliTimestamp();

    // OS-level sandbox: on macOS, wrap the child in sandbox-exec with a profile
    // that confines file writes to the workspace (cwd) + tmp — a syscall-level
    // safety net orthogonal to the capability kernel. Non-macOS falls back to
    // running unwrapped (kernel policy still applies).
    var argv_list = std.ArrayList([]const u8){};
    if (sandbox and builtin.os.tag == .macos) {
        const cwd_raw = cwd orelse ".";
        // Canonicalize so the profile subpath matches the kernel-resolved path
        // (macOS /var -> /private/var). Confine writes to cwd + /tmp only.
        const cwd_abs = std.fs.realpathAlloc(allocator, cwd_raw) catch cwd_raw;
        const profile = try std.fmt.allocPrint(
            allocator,
            "(version 1)(allow default)(deny file-write* (subpath \"/\"))(allow file-write* (subpath \"{s}\") (subpath \"/tmp\") (subpath \"/private/tmp\"))(allow file-write-data (literal \"/dev/null\") (literal \"/dev/stdout\") (literal \"/dev/stderr\") (literal \"/dev/tty\") (literal \"/dev/urandom\") (literal \"/dev/dtracehelper\"))",
            .{cwd_abs},
        );
        try argv_list.append(allocator, "/usr/bin/sandbox-exec");
        try argv_list.append(allocator, "-p");
        try argv_list.append(allocator, profile);
    }
    for (args[argv_start..]) |a| try argv_list.append(allocator, a);
    const child_argv = try argv_list.toOwnedSlice(allocator);

    var child = std.process.Child.init(child_argv, allocator);
    child.stdout_behavior = .Pipe;
    child.stderr_behavior = .Pipe;
    if (cwd) |dir| child.cwd = dir;

    // Env allowlist: PATH + HOME always pass; everything else must be named.
    var env_map = std.process.EnvMap.init(allocator);
    var parent_env = try std.process.getEnvMap(allocator);
    // PATH/HOME/TMPDIR always pass; proxy vars pass so the egress proxy applies;
    // everything else must be explicitly named via --env.
    const always = [_][]const u8{ "PATH", "HOME", "TMPDIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy" };
    for (always) |name| {
        if (parent_env.get(name)) |value| try env_map.put(name, value);
    }
    for (env_names.items) |name| {
        if (parent_env.get(name)) |value| try env_map.put(name, value);
    }
    child.env_map = &env_map;

    child.spawn() catch |err| {
        try jsonl.printLine(allocator, "{{\"error\":\"spawn failed\",\"detail\":\"{s}\"}}", .{@errorName(err)});
        std.process.exit(1);
    };
    child_pid.store(@intCast(child.id), .seq_cst);

    const watchdog = try std.Thread.spawn(.{}, watchdogMain, .{timeout_ms});

    var stdout_buf = std.ArrayList(u8){};
    var stderr_buf = std.ArrayList(u8){};
    child.collectOutput(allocator, &stdout_buf, &stderr_buf, max_output) catch {};
    const term = child.wait() catch std.process.Child.Term{ .Exited = 255 };
    finished.store(true, .seq_cst);
    watchdog.join();

    try emitChunks(allocator, "stdout", stdout_buf.items);
    try emitChunks(allocator, "stderr", stderr_buf.items);

    const duration: i64 = std.time.milliTimestamp() - started;
    const exit_code: i64 = switch (term) {
        .Exited => |code| @intCast(code),
        else => -1,
    };
    try jsonl.printLine(
        allocator,
        "{{\"exit_code\":{d},\"duration_ms\":{d},\"timed_out\":{}}}",
        .{ exit_code, duration, timed_out.load(.seq_cst) },
    );
}

fn watchdogMain(timeout_ms: u64) void {
    const step_ms: u64 = 50;
    var waited: u64 = 0;
    while (waited < timeout_ms) : (waited += step_ms) {
        if (finished.load(.seq_cst)) return;
        std.Thread.sleep(step_ms * std.time.ns_per_ms);
    }
    if (!finished.load(.seq_cst)) {
        timed_out.store(true, .seq_cst);
        const pid = child_pid.load(.seq_cst);
        if (pid > 0) {
            std.posix.kill(pid, std.posix.SIG.KILL) catch {};
        }
    }
}

fn emitChunks(allocator: std.mem.Allocator, stream: []const u8, data: []const u8) !void {
    var lines = std.mem.splitScalar(u8, data, '\n');
    while (lines.next()) |line| {
        if (line.len == 0) continue;
        const escaped = try jsonl.escape(allocator, line);
        try jsonl.printLine(allocator, "{{\"stream\":\"{s}\",\"chunk\":\"{s}\"}}", .{ stream, escaped });
    }
}
