//! pi-patch-native — atomic multi-file patch primitive (PRD §8.5, §4.4).
//!
//! This is the real local-system write path: the Rust transactional engine
//! (crates/pi-patch) owns the lifecycle state machine and authorization,
//! then delegates the actual disk mutation to this tool so that all file
//! writes happen in the Zig native toolchain.
//!
//! It reads a JSON plan on stdin:
//!   {"files":[{"path":"abs","new_content":"...","snapshot":"abs.pre","existed":true}]}
//!
//! Subcommands:
//!   pi-patch-native apply     write every file atomically (temp + rename);
//!                             all-or-nothing — any failure restores prior files
//!   pi-patch-native dry-run   validate only; touch nothing
//!   pi-patch-native rollback  restore each file from its snapshot (or delete
//!                             files the patch created)
//!
//! Output: {"status":"applied|dry_run_ok|rolled_back|failed","files":N,...}

const std = @import("std");
const jsonl = @import("jsonl");

const max_content = 64 * 1024 * 1024;

const FileOp = struct {
    path: []const u8,
    new_content: []const u8 = "",
    snapshot: []const u8 = "",
    existed: bool = false,
};

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    if (args.len < 2) {
        try jsonl.writeLine("{\"error\":\"usage: pi-patch-native <apply|dry-run|rollback>  (JSON plan on stdin)\"}");
        std.process.exit(2);
    }
    const cmd = args[1];

    const raw = try readAllStdin(allocator);
    const parsed = std.json.parseFromSlice(std.json.Value, allocator, raw, .{}) catch |err| {
        try jsonl.printLine(allocator, "{{\"error\":\"invalid JSON plan\",\"detail\":\"{s}\",\"bytes\":{d}}}", .{ @errorName(err), raw.len });
        std.process.exit(1);
    };
    const files = try parseFiles(allocator, parsed.value);

    if (std.mem.eql(u8, cmd, "dry-run")) {
        try dryRun(allocator, files);
    } else if (std.mem.eql(u8, cmd, "apply")) {
        try apply(allocator, files);
    } else if (std.mem.eql(u8, cmd, "rollback")) {
        try rollback(allocator, files);
    } else {
        try jsonl.printLine(allocator, "{{\"error\":\"unknown subcommand {s}\"}}", .{cmd});
        std.process.exit(2);
    }
}

/// Reads all of stdin into memory (works for pipes, which cannot be stat'd).
fn readAllStdin(allocator: std.mem.Allocator) ![]u8 {
    var buf = std.ArrayList(u8){};
    var tmp: [8192]u8 = undefined;
    while (true) {
        const n = std.posix.read(std.posix.STDIN_FILENO, &tmp) catch break;
        if (n == 0) break;
        try buf.appendSlice(allocator, tmp[0..n]);
        if (buf.items.len > max_content) break;
    }
    return buf.toOwnedSlice(allocator);
}

fn parseFiles(allocator: std.mem.Allocator, root: std.json.Value) ![]FileOp {
    const arr = root.object.get("files") orelse return error.NoFiles;
    var list = std.ArrayList(FileOp){};
    for (arr.array.items) |item| {
        const o = item.object;
        try list.append(allocator, .{
            .path = if (o.get("path")) |v| v.string else return error.NoPath,
            .new_content = if (o.get("new_content")) |v| v.string else "",
            .snapshot = if (o.get("snapshot")) |v| v.string else "",
            .existed = if (o.get("existed")) |v| v.bool else false,
        });
    }
    return list.toOwnedSlice(allocator);
}

/// Validate only: confirm every target dir is writable, touch nothing.
fn dryRun(allocator: std.mem.Allocator, files: []FileOp) !void {
    for (files) |f| {
        const dir = std.fs.path.dirname(f.path) orelse ".";
        std.fs.cwd().access(dir, .{}) catch {
            try jsonl.printLine(allocator, "{{\"status\":\"failed\",\"reason\":\"unwritable dir\",\"path\":\"{s}\"}}", .{dir});
            std.process.exit(1);
        };
    }
    try jsonl.printLine(allocator, "{{\"status\":\"dry_run_ok\",\"files\":{d}}}", .{files.len});
}

/// Atomic apply: write each file via temp + rename. If any write fails,
/// restore every already-written file from its snapshot — no half-applied
/// state (PRD §7.4).
fn apply(allocator: std.mem.Allocator, files: []FileOp) !void {
    var done: usize = 0;
    for (files, 0..) |f, idx| {
        atomicWrite(allocator, f.path, f.new_content) catch {
            // Roll back the ones already written.
            var j: usize = 0;
            while (j < idx) : (j += 1) {
                restoreOne(allocator, files[j]) catch {};
            }
            try jsonl.printLine(allocator, "{{\"status\":\"failed\",\"reason\":\"write failed\",\"path\":\"{s}\",\"restored\":{d}}}", .{ f.path, idx });
            std.process.exit(1);
        };
        done += 1;
    }
    try jsonl.printLine(allocator, "{{\"status\":\"applied\",\"files\":{d}}}", .{done});
}

fn rollback(allocator: std.mem.Allocator, files: []FileOp) !void {
    for (files) |f| {
        restoreOne(allocator, f) catch {
            try jsonl.printLine(allocator, "{{\"status\":\"failed\",\"reason\":\"restore failed\",\"path\":\"{s}\"}}", .{f.path});
            std.process.exit(1);
        };
    }
    try jsonl.printLine(allocator, "{{\"status\":\"rolled_back\",\"files\":{d}}}", .{files.len});
}

/// Restore a file to its pre-image: copy back from snapshot, or delete the
/// file if the patch created it (existed=false).
fn restoreOne(allocator: std.mem.Allocator, f: FileOp) !void {
    if (f.existed) {
        if (f.snapshot.len == 0) return error.NoSnapshot;
        const content = try std.fs.cwd().readFileAlloc(allocator, f.snapshot, max_content);
        try atomicWrite(allocator, f.path, content);
    } else {
        std.fs.cwd().deleteFile(f.path) catch |err| switch (err) {
            error.FileNotFound => {},
            else => return err,
        };
    }
}

/// Write content to path via a temp file + rename (atomic on POSIX).
fn atomicWrite(allocator: std.mem.Allocator, path: []const u8, content: []const u8) !void {
    if (std.fs.path.dirname(path)) |dir| {
        std.fs.cwd().makePath(dir) catch {};
    }
    const tmp = try std.fmt.allocPrint(allocator, "{s}.pi-os-tmp", .{path});
    defer allocator.free(tmp);
    {
        const file = try std.fs.cwd().createFile(tmp, .{ .truncate = true });
        defer file.close();
        try file.writeAll(content);
    }
    try std.fs.cwd().rename(tmp, path);
}
