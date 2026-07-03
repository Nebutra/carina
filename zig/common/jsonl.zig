//! Shared helpers for the Pi-OS native tools: every tool emits
//! machine-readable JSON lines on stdout (PRD §8.5 acceptance criteria).

const std = @import("std");

/// Writes one line to stdout, looping until all bytes are flushed.
/// A closed pipe (downstream `head`, killed daemon) is a clean exit,
/// not a crash.
pub fn writeLine(bytes: []const u8) !void {
    var written: usize = 0;
    while (written < bytes.len) {
        written += write(bytes[written..]);
    }
    _ = write("\n");
}

fn write(bytes: []const u8) usize {
    return std.posix.write(std.posix.STDOUT_FILENO, bytes) catch |err| switch (err) {
        error.BrokenPipe => std.process.exit(0),
        else => std.process.exit(1),
    };
}

/// Formats and writes one JSON line.
pub fn printLine(allocator: std.mem.Allocator, comptime fmt: []const u8, args: anytype) !void {
    const line = try std.fmt.allocPrint(allocator, fmt, args);
    defer allocator.free(line);
    try writeLine(line);
}

/// Minimal JSON string escaping (quotes, backslashes, control chars).
pub fn escape(allocator: std.mem.Allocator, s: []const u8) ![]u8 {
    var out = std.ArrayList(u8){};
    defer out.deinit(allocator);
    for (s) |c| {
        switch (c) {
            '"' => try out.appendSlice(allocator, "\\\""),
            '\\' => try out.appendSlice(allocator, "\\\\"),
            '\n' => try out.appendSlice(allocator, "\\n"),
            '\r' => try out.appendSlice(allocator, "\\r"),
            '\t' => try out.appendSlice(allocator, "\\t"),
            else => {
                if (c < 0x20) {
                    const hex = try std.fmt.allocPrint(allocator, "\\u{x:0>4}", .{c});
                    defer allocator.free(hex);
                    try out.appendSlice(allocator, hex);
                } else {
                    try out.append(allocator, c);
                }
            },
        }
    }
    return out.toOwnedSlice(allocator);
}
