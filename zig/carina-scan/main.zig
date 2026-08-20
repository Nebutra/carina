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
    ".cache", ".npm", ".Trash", "Library",
};

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    var root: []const u8 = ".";
    var max_size: u64 = 5 * 1024 * 1024; // 5 MiB default
    var max_files: u64 = 0;
    var max_depth: u64 = 0;
    var i: usize = 1;
    while (i < args.len) : (i += 1) {
        if (std.mem.eql(u8, args[i], "--max-size") and i + 1 < args.len) {
            i += 1;
            max_size = std.fmt.parseInt(u64, args[i], 10) catch max_size;
        } else if (std.mem.eql(u8, args[i], "--max-files") and i + 1 < args.len) {
            i += 1;
            max_files = std.fmt.parseInt(u64, args[i], 10) catch max_files;
        } else if (std.mem.eql(u8, args[i], "--max-depth") and i + 1 < args.len) {
            i += 1;
            max_depth = std.fmt.parseInt(u64, args[i], 10) catch max_depth;
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

    var ctx = ScanCtx{
        .allocator = allocator,
        .max_size = max_size,
        .max_files = max_files,
        .max_depth = max_depth,
        .patterns = patterns.items,
    };
    try scanDir(&ctx, dir, "");

    try jsonl.printLine(
        allocator,
        "{{\"summary\":{{\"files\":{d},\"skipped\":{d},\"ignored\":{d},\"truncated\":{}}}}}",
        .{ ctx.files, ctx.skipped, ctx.ignored, ctx.truncated },
    );
}

const ScanCtx = struct {
    allocator: std.mem.Allocator,
    max_size: u64,
    max_files: u64,
    max_depth: u64,
    patterns: []const []const u8,
    files: u64 = 0,
    skipped: u64 = 0,
    ignored: u64 = 0,
    truncated: bool = false,
};

fn relDepth(rel: []const u8) u64 {
    if (rel.len == 0) return 0;
    var depth: u64 = 1;
    for (rel) |c| {
        if (c == '/') depth += 1;
    }
    return depth;
}

fn scanDir(ctx: *ScanCtx, dir: std.fs.Dir, rel: []const u8) !void {
    var it = dir.iterate();
    while (it.next() catch null) |entry| {
        if (ctx.max_files > 0 and ctx.files >= ctx.max_files) {
            ctx.truncated = true;
            return;
        }
        const child_rel = if (rel.len == 0)
            entry.name
        else
            try std.fs.path.join(ctx.allocator, &.{ rel, entry.name });
        if (shouldSkipDir(entry.name, child_rel, ctx.patterns)) {
            ctx.ignored += 1;
            continue;
        }
        if (entry.kind == .sym_link) {
            ctx.skipped += 1;
            continue;
        }
        if (entry.kind == .directory) {
            if (ctx.max_depth > 0 and relDepth(child_rel) >= ctx.max_depth) {
                ctx.ignored += 1;
                continue;
            }
            var child = dir.openDir(entry.name, .{ .iterate = true }) catch {
                ctx.skipped += 1;
                continue;
            };
            defer child.close();
            try scanDir(ctx, child, child_rel);
            continue;
        }
        if (entry.kind != .file) continue;

        const stat = dir.statFile(entry.name) catch {
            ctx.skipped += 1;
            continue;
        };
        const large = stat.size > ctx.max_size;
        const binary = if (large) false else detectBinary(dir, entry.name);
        const language = languageOf(child_rel);
        const escaped = try jsonl.escape(ctx.allocator, child_rel);
        try jsonl.printLine(
            ctx.allocator,
            "{{\"path\":\"{s}\",\"size\":{d},\"binary\":{},\"large\":{},\"language\":\"{s}\"}}",
            .{ escaped, stat.size, binary, large, language },
        );
        ctx.files += 1;
    }
}

fn shouldSkipDir(name: []const u8, rel: []const u8, patterns: []const []const u8) bool {
    for (builtin_ignores) |ig| {
        if (std.mem.eql(u8, name, ig)) return true;
    }
    for (patterns) |pat| {
        if (matchIgnore(rel, pat) or matchIgnore(name, pat)) return true;
    }
    return false;
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
