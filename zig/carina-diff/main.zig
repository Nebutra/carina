//! carina-diff — structured diff primitive (PRD §8.5).
//! Phase 0: naive line-by-line comparison emitting JSON ops.
//! Myers diff and rename detection land in Phase 1.
//!
//! Usage: carina-diff <file-a> <file-b>
//! Output: {"op":"-|+","line":N,"text":"..."} per differing line.

const std = @import("std");
const jsonl = @import("jsonl");

const max_file_size = 32 * 1024 * 1024;

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    if (args.len != 3) {
        try jsonl.writeLine("{\"error\":\"usage: carina-diff <file-a> <file-b>\"}");
        std.process.exit(2);
    }

    const a_lines = try readLines(allocator, args[1]);
    const b_lines = try readLines(allocator, args[2]);

    var changes: u64 = 0;
    const common = @min(a_lines.len, b_lines.len);
    var i: usize = 0;
    while (i < common) : (i += 1) {
        if (!std.mem.eql(u8, a_lines[i], b_lines[i])) {
            try emit(allocator, "-", i + 1, a_lines[i]);
            try emit(allocator, "+", i + 1, b_lines[i]);
            changes += 1;
        }
    }
    while (i < a_lines.len) : (i += 1) {
        try emit(allocator, "-", i + 1, a_lines[i]);
        changes += 1;
    }
    i = common;
    while (i < b_lines.len) : (i += 1) {
        try emit(allocator, "+", i + 1, b_lines[i]);
        changes += 1;
    }

    try jsonl.printLine(allocator, "{{\"summary\":{{\"changed_lines\":{d}}}}}", .{changes});
}

fn emit(allocator: std.mem.Allocator, op: []const u8, line: usize, text: []const u8) !void {
    const escaped = try jsonl.escape(allocator, text);
    try jsonl.printLine(allocator, "{{\"op\":\"{s}\",\"line\":{d},\"text\":\"{s}\"}}", .{ op, line, escaped });
}

fn readLines(allocator: std.mem.Allocator, path: []const u8) ![][]const u8 {
    const file = try std.fs.cwd().openFile(path, .{});
    defer file.close();
    const content = try file.readToEndAlloc(allocator, max_file_size);

    var list = std.ArrayList([]const u8){};
    var lines = std.mem.splitScalar(u8, content, '\n');
    while (lines.next()) |line| {
        try list.append(allocator, line);
    }
    return list.toOwnedSlice(allocator);
}
