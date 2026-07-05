//! carina-scan — workspace file-tree scanner (PRD §8.5, §8.3).
//!
//! Walks the tree, honoring a built-in ignore set plus `.gitignore` and
//! `.piignore` at the root; detects binary files (NUL probe); skips files
//! over a size limit; and tags a language by extension. Emits one JSON
//! object per file plus a summary.
//!
//! Usage: carina-scan [root] [--max-size BYTES]
//! Output: {"path","size","binary","language","large"} per file.

const std = @import("std");
const jsonl = @import("jsonl");

const builtin_ignores = [_][]const u8{
    ".git", "node_modules", "target", "zig-out", ".zig-cache", "zig-cache",
    "dist", "build", ".venv", "__pycache__", ".next", ".turbo",
};

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    var root: []const u8 = ".";
    var max_size: u64 = 5 * 1024 * 1024; // 5 MiB default
    var i: usize = 1;
    while (i < args.len) : (i += 1) {
        if (std.mem.eql(u8, args[i], "--max-size") and i + 1 < args.len) {
            i += 1;
            max_size = std.fmt.parseInt(u64, args[i], 10) catch max_size;
        } else {
            root = args[i];
        }
    }

    // Load ignore patterns from .gitignore + .piignore at the root.
    var patterns = std.ArrayList([]const u8){};
    try loadIgnoreFile(allocator, &patterns, root, ".gitignore");
    try loadIgnoreFile(allocator, &patterns, root, ".piignore");

    var dir = std.fs.cwd().openDir(root, .{ .iterate = true }) catch |err| {
        try jsonl.printLine(allocator, "{{\"error\":\"cannot open root\",\"detail\":\"{s}\"}}", .{@errorName(err)});
        std.process.exit(1);
    };
    defer dir.close();

    var walker = try dir.walk(allocator);
    defer walker.deinit();

    var files: u64 = 0;
    var skipped: u64 = 0;
    var ignored: u64 = 0;

    outer: while (try walker.next()) |entry| {
        // Built-in ignore dirs (fast path).
        for (builtin_ignores) |ig| {
            if (pathHasSegment(entry.path, ig)) {
                ignored += 1;
                continue :outer;
            }
        }
        // User ignore patterns.
        for (patterns.items) |pat| {
            if (matchIgnore(entry.path, pat)) {
                ignored += 1;
                continue :outer;
            }
        }
        if (entry.kind != .file) continue;

        const stat = dir.statFile(entry.path) catch {
            skipped += 1;
            continue;
        };
        const large = stat.size > max_size;
        const binary = if (large) false else detectBinary(dir, entry.path);
        const language = languageOf(entry.path);

        const escaped = try jsonl.escape(allocator, entry.path);
        try jsonl.printLine(
            allocator,
            "{{\"path\":\"{s}\",\"size\":{d},\"binary\":{},\"large\":{},\"language\":\"{s}\"}}",
            .{ escaped, stat.size, binary, large, language },
        );
        files += 1;
    }

    try jsonl.printLine(
        allocator,
        "{{\"summary\":{{\"files\":{d},\"skipped\":{d},\"ignored\":{d}}}}}",
        .{ files, skipped, ignored },
    );
}

fn loadIgnoreFile(allocator: std.mem.Allocator, patterns: *std.ArrayList([]const u8), root: []const u8, name: []const u8) !void {
    const path = try std.fs.path.join(allocator, &.{ root, name });
    const file = std.fs.cwd().openFile(path, .{}) catch return;
    defer file.close();
    const content = file.readToEndAlloc(allocator, 1 << 20) catch return;
    var lines = std.mem.splitScalar(u8, content, '\n');
    while (lines.next()) |raw| {
        const line = std.mem.trim(u8, raw, " \t\r");
        if (line.len == 0 or line[0] == '#') continue;
        // Strip a trailing slash (directory marker) and leading slash.
        var p = line;
        if (p.len > 0 and p[p.len - 1] == '/') p = p[0 .. p.len - 1];
        if (p.len > 0 and p[0] == '/') p = p[1..];
        if (p.len > 0) try patterns.append(allocator, try allocator.dupe(u8, p));
    }
}

/// True if any '/'-separated segment of `path` equals `seg`.
fn pathHasSegment(path: []const u8, seg: []const u8) bool {
    var it = std.mem.splitScalar(u8, path, '/');
    while (it.next()) |s| {
        if (std.mem.eql(u8, s, seg)) return true;
    }
    return false;
}

/// Simplified .gitignore matching: supports `*.ext`, `prefix*`, exact
/// segment, and substring path match.
fn matchIgnore(path: []const u8, pat: []const u8) bool {
    if (std.mem.indexOfScalar(u8, pat, '*')) |star| {
        const prefix = pat[0..star];
        const suffix = pat[star + 1 ..];
        const base = std.fs.path.basename(path);
        if (prefix.len == 0) return std.mem.endsWith(u8, base, suffix); // *.ext
        if (suffix.len == 0) return std.mem.startsWith(u8, base, prefix); // prefix*
        return std.mem.startsWith(u8, base, prefix) and std.mem.endsWith(u8, base, suffix);
    }
    // No wildcard: match a whole path segment, or a leading path component.
    if (pathHasSegment(path, pat)) return true;
    return std.mem.startsWith(u8, path, pat);
}

/// Reads the first 4 KiB and flags the file binary if it contains a NUL.
fn detectBinary(dir: std.fs.Dir, path: []const u8) bool {
    const file = dir.openFile(path, .{}) catch return false;
    defer file.close();
    var buf: [4096]u8 = undefined;
    const n = file.read(&buf) catch return false;
    return std.mem.indexOfScalar(u8, buf[0..n], 0) != null;
}

fn languageOf(path: []const u8) []const u8 {
    const ext = std.fs.path.extension(path);
    const table = [_]struct { e: []const u8, l: []const u8 }{
        .{ .e = ".go", .l = "go" },        .{ .e = ".rs", .l = "rust" },
        .{ .e = ".zig", .l = "zig" },      .{ .e = ".ts", .l = "typescript" },
        .{ .e = ".tsx", .l = "typescript" }, .{ .e = ".js", .l = "javascript" },
        .{ .e = ".jsx", .l = "javascript" }, .{ .e = ".py", .l = "python" },
        .{ .e = ".c", .l = "c" },          .{ .e = ".h", .l = "c" },
        .{ .e = ".cpp", .l = "cpp" },      .{ .e = ".cc", .l = "cpp" },
        .{ .e = ".java", .l = "java" },    .{ .e = ".rb", .l = "ruby" },
        .{ .e = ".md", .l = "markdown" },  .{ .e = ".json", .l = "json" },
        .{ .e = ".toml", .l = "toml" },    .{ .e = ".yaml", .l = "yaml" },
        .{ .e = ".yml", .l = "yaml" },     .{ .e = ".sh", .l = "shell" },
        .{ .e = ".sql", .l = "sql" },      .{ .e = ".html", .l = "html" },
        .{ .e = ".css", .l = "css" },
    };
    for (table) |row| {
        if (std.mem.eql(u8, ext, row.e)) return row.l;
    }
    return "unknown";
}
