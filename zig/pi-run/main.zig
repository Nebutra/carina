//! pi-run — mediated command runner (PRD §8.5, §8.8).
//! Phase 0: spawn a command, inherit output streams, report exit status as
//! JSON. Timeout enforcement, env allowlist, and stream capture land in
//! Phase 1. pi-run is only ever invoked after a kernel CommandExec decision.
//!
//! Usage: pi-run <program> [args...]
//! Output (last line): {"exit_code":N,"duration_ms":N}

const std = @import("std");
const jsonl = @import("jsonl");

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    if (args.len < 2) {
        try jsonl.writeLine("{\"error\":\"usage: pi-run <program> [args...]\"}");
        std.process.exit(2);
    }

    const started = std.time.milliTimestamp();

    var child = std.process.Child.init(args[1..], allocator);
    child.stdout_behavior = .Inherit;
    child.stderr_behavior = .Inherit;

    const term = child.spawnAndWait() catch |err| {
        try jsonl.printLine(allocator, "{{\"error\":\"spawn failed\",\"detail\":\"{s}\"}}", .{@errorName(err)});
        std.process.exit(1);
    };

    const duration: i64 = std.time.milliTimestamp() - started;
    const exit_code: i64 = switch (term) {
        .Exited => |code| @intCast(code),
        else => -1,
    };
    try jsonl.printLine(allocator, "{{\"exit_code\":{d},\"duration_ms\":{d}}}", .{ exit_code, duration });
}
