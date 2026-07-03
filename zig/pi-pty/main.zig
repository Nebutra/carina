//! pi-pty — interactive terminal sessions (PRD §8.5).
//! Phase 0 stub: reports capability surface. Streaming output, resize, and
//! kill land in Phase 1.

const std = @import("std");
const jsonl = @import("jsonl");

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();
    try jsonl.printLine(
        allocator,
        "{{\"tool\":\"pi-pty\",\"status\":\"phase0-stub\",\"planned\":[\"spawn\",\"stream\",\"resize\",\"kill\"]}}",
        .{},
    );
}
