//! carina-grep — structured text search (PRD §8.5).
//! Literal substring match over files or directory trees, JSON-line output.
//! Regex and context lines land later; directory walking skips the same
//! ignore set as carina-scan.
//!
//! Usage: carina-grep <pattern> <path> [path...] [--max-matches N]
//! Output: {"file":"...","line":12,"text":"..."} per match, then a summary.

const std = @import("std");
const jsonl = @import("jsonl");

const max_file_size = 32 * 1024 * 1024;

const ignored_dirs = [_][]const u8{
    ".git",   "node_modules", "target", "zig-out",     ".zig-cache", "zig-cache",
    "dist",   "build",        ".venv",  "__pycache__", ".next",      ".turbo",
    ".cache", ".npm",         ".Trash", "Library",
};

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    if (args.len < 3) {
        try jsonl.writeLine("{\"error\":\"usage: carina-grep <pattern> <path> [path...] [--max-matches N]\"}");
        std.process.exit(2);
    }
    const pattern = args[1];
    if (pattern.len == 0) {
        try jsonl.writeLine("{\"error\":\"empty pattern\"}");
        std.process.exit(2);
    }

    var max_matches: u64 = 0;
    var i: usize = 2;
    while (i < args.len) : (i += 1) {
        if (std.mem.eql(u8, args[i], "--max-matches") and i + 1 < args.len) {
            i += 1;
            max_matches = std.fmt.parseInt(u64, args[i], 10) catch max_matches;
        }
    }

    var ctx = GrepCtx{
        .allocator = allocator,
        .pattern = pattern,
        .max_matches = max_matches,
    };

    i = 2;
    while (i < args.len) : (i += 1) {
        if (std.mem.eql(u8, args[i], "--max-matches") and i + 1 < args.len) {
            i += 1;
            continue;
        }
        if (ctx.atCap()) {
            ctx.truncated = true;
            break;
        }
        const path = args[i];
        const stat = std.fs.cwd().statFile(path) catch {
            const escaped = try jsonl.escape(allocator, path);
            try jsonl.printLine(allocator, "{{\"error\":\"cannot open\",\"file\":\"{s}\"}}", .{escaped});
            continue;
        };
        if (stat.kind == .directory) {
            try grepDir(&ctx, path);
        } else {
            try grepFile(&ctx, path);
        }
    }

    try jsonl.printLine(
        allocator,
        "{{\"summary\":{{\"matches\":{d},\"truncated\":{}}}}}",
        .{ ctx.matches, ctx.truncated },
    );
}

const GrepCtx = struct {
    allocator: std.mem.Allocator,
    pattern: []const u8,
    max_matches: u64,
    matches: u64 = 0,
    truncated: bool = false,

    fn atCap(self: *const GrepCtx) bool {
        return self.max_matches > 0 and self.matches >= self.max_matches;
    }
};

fn grepDir(ctx: *GrepCtx, root: []const u8) !void {
    var dir = std.fs.cwd().openDir(root, .{ .iterate = true }) catch return;
    defer dir.close();
    try grepWalk(ctx, dir, root);
}

fn grepWalk(ctx: *GrepCtx, dir: std.fs.Dir, prefix: []const u8) !void {
    var it = dir.iterate();
    while (it.next() catch null) |entry| {
        if (ctx.atCap()) {
            ctx.truncated = true;
            return;
        }
        if (skipIgnoredName(entry.name)) continue;
        if (entry.kind == .sym_link) continue;
        const full = try std.fs.path.join(ctx.allocator, &.{ prefix, entry.name });
        if (entry.kind == .directory) {
            var child = dir.openDir(entry.name, .{ .iterate = true }) catch continue;
            defer child.close();
            try grepWalk(ctx, child, full);
            continue;
        }
        if (entry.kind == .file) {
            try grepFile(ctx, full);
        }
    }
}

fn skipIgnoredName(name: []const u8) bool {
    for (ignored_dirs) |ig| {
        if (std.mem.eql(u8, name, ig)) return true;
    }
    return false;
}

fn grepFile(ctx: *GrepCtx, path: []const u8) !void {
    if (ctx.atCap()) {
        ctx.truncated = true;
        return;
    }
    const file = std.fs.cwd().openFile(path, .{}) catch return;
    defer file.close();
    const content = file.readToEndAlloc(ctx.allocator, max_file_size) catch return;
    // Skip binary files: NUL byte heuristic on the first 4KB.
    const probe = content[0..@min(content.len, 4096)];
    if (std.mem.indexOfScalar(u8, probe, 0) != null) return;

    var line_no: u64 = 0;
    var lines = std.mem.splitScalar(u8, content, '\n');
    while (lines.next()) |line| {
        line_no += 1;
        if (std.mem.indexOf(u8, line, ctx.pattern) == null) continue;
        if (ctx.atCap()) {
            ctx.truncated = true;
            return;
        }
        const escaped_path = try jsonl.escape(ctx.allocator, path);
        const escaped_line = try jsonl.escape(ctx.allocator, line);
        try jsonl.printLine(
            ctx.allocator,
            "{{\"file\":\"{s}\",\"line\":{d},\"text\":\"{s}\"}}",
            .{ escaped_path, line_no, escaped_line },
        );
        ctx.matches += 1;
    }
}
