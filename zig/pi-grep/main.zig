//! pi-grep — structured text search (PRD §8.5).
//! Literal substring match over files or directory trees, JSON-line output.
//! Regex and context lines land later; directory walking skips the same
//! ignore set as pi-scan.
//!
//! Usage: pi-grep <pattern> <path> [path...]
//! Output: {"file":"...","line":12,"text":"..."} per match, then a summary.

const std = @import("std");
const jsonl = @import("jsonl");

const max_file_size = 32 * 1024 * 1024;

const ignored_dirs = [_][]const u8{
    ".git", "node_modules", "target", "zig-out", ".zig-cache", "zig-cache", "dist", ".venv", "__pycache__",
};

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    if (args.len < 3) {
        try jsonl.writeLine("{\"error\":\"usage: pi-grep <pattern> <path> [path...]\"}");
        std.process.exit(2);
    }
    const pattern = args[1];

    var matches: u64 = 0;
    for (args[2..]) |path| {
        const stat = std.fs.cwd().statFile(path) catch {
            const escaped = try jsonl.escape(allocator, path);
            try jsonl.printLine(allocator, "{{\"error\":\"cannot open\",\"file\":\"{s}\"}}", .{escaped});
            continue;
        };
        if (stat.kind == .directory) {
            matches += try grepDir(allocator, pattern, path);
        } else {
            matches += try grepFile(allocator, pattern, path);
        }
    }

    try jsonl.printLine(allocator, "{{\"summary\":{{\"matches\":{d}}}}}", .{matches});
}

fn grepDir(allocator: std.mem.Allocator, pattern: []const u8, root: []const u8) !u64 {
    var dir = std.fs.cwd().openDir(root, .{ .iterate = true }) catch return 0;
    defer dir.close();
    var walker = try dir.walk(allocator);
    defer walker.deinit();

    var total: u64 = 0;
    outer: while (try walker.next()) |entry| {
        if (entry.kind != .file) continue;
        for (ignored_dirs) |ig| {
            if (std.mem.indexOf(u8, entry.path, ig) != null) continue :outer;
        }
        const full = try std.fs.path.join(allocator, &.{ root, entry.path });
        total += try grepFile(allocator, pattern, full);
    }
    return total;
}

fn grepFile(allocator: std.mem.Allocator, pattern: []const u8, path: []const u8) !u64 {
    const file = std.fs.cwd().openFile(path, .{}) catch return 0;
    defer file.close();
    const content = file.readToEndAlloc(allocator, max_file_size) catch return 0;
    // Skip binary files: NUL byte heuristic on the first 4KB.
    const probe = content[0..@min(content.len, 4096)];
    if (std.mem.indexOfScalar(u8, probe, 0) != null) return 0;

    var found: u64 = 0;
    var line_no: u64 = 0;
    var lines = std.mem.splitScalar(u8, content, '\n');
    while (lines.next()) |line| {
        line_no += 1;
        if (std.mem.indexOf(u8, line, pattern) != null) {
            const escaped_path = try jsonl.escape(allocator, path);
            const escaped_line = try jsonl.escape(allocator, line);
            try jsonl.printLine(
                allocator,
                "{{\"file\":\"{s}\",\"line\":{d},\"text\":\"{s}\"}}",
                .{ escaped_path, line_no, escaped_line },
            );
            found += 1;
        }
    }
    return found;
}
