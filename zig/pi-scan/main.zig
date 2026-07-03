//! pi-scan — fast workspace file-tree scanner (PRD §8.5).
//! Phase 0: recursive walk with a built-in ignore set and size reporting.
//! Ignore-file rules, binary detection, and language detection land in Phase 1.
//!
//! Usage: pi-scan [root]
//! Output: one JSON object per file: {"path":"...","size":123}

const std = @import("std");
const jsonl = @import("jsonl");

const ignored_dirs = [_][]const u8{
    ".git", "node_modules", "target", "zig-out", ".zig-cache", "zig-cache", "dist", ".venv", "__pycache__",
};

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    const root = if (args.len > 1) args[1] else ".";

    var dir = std.fs.cwd().openDir(root, .{ .iterate = true }) catch |err| {
        try jsonl.printLine(allocator, "{{\"error\":\"cannot open root\",\"detail\":\"{s}\"}}", .{@errorName(err)});
        std.process.exit(1);
    };
    defer dir.close();

    var walker = try dir.walk(allocator);
    defer walker.deinit();

    var files: u64 = 0;
    var skipped: u64 = 0;

    outer: while (try walker.next()) |entry| {
        for (ignored_dirs) |ig| {
            if (std.mem.indexOf(u8, entry.path, ig) != null) {
                skipped += 1;
                continue :outer;
            }
        }
        if (entry.kind != .file) continue;
        const stat = dir.statFile(entry.path) catch {
            skipped += 1;
            continue;
        };
        const escaped = try jsonl.escape(allocator, entry.path);
        try jsonl.printLine(allocator, "{{\"path\":\"{s}\",\"size\":{d}}}", .{ escaped, stat.size });
        files += 1;
    }

    try jsonl.printLine(allocator, "{{\"summary\":{{\"files\":{d},\"skipped\":{d}}}}}", .{ files, skipped });
}
