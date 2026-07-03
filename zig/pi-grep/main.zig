//! pi-grep — structured text search (PRD §8.5).
//! Phase 0: literal substring match over one or more files, JSON-line output.
//! Regex, globs, and context lines land in Phase 1.
//!
//! Usage: pi-grep <pattern> <file> [file...]
//! Output: {"file":"...","line":12,"text":"..."} per match, then a summary.

const std = @import("std");
const jsonl = @import("jsonl");

const max_file_size = 32 * 1024 * 1024;

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    if (args.len < 3) {
        try jsonl.writeLine("{\"error\":\"usage: pi-grep <pattern> <file> [file...]\"}");
        std.process.exit(2);
    }
    const pattern = args[1];

    var matches: u64 = 0;
    for (args[2..]) |path| {
        const file = std.fs.cwd().openFile(path, .{}) catch {
            const escaped = try jsonl.escape(allocator, path);
            try jsonl.printLine(allocator, "{{\"error\":\"cannot open\",\"file\":\"{s}\"}}", .{escaped});
            continue;
        };
        defer file.close();
        const content = file.readToEndAlloc(allocator, max_file_size) catch continue;

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
                matches += 1;
            }
        }
    }

    try jsonl.printLine(allocator, "{{\"summary\":{{\"matches\":{d}}}}}", .{matches});
}
