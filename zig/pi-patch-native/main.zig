//! pi-patch-native — atomic patch apply/verify/rollback primitive (PRD §8.5).
//! Phase 0 stub: reports capability surface. The transactional semantics
//! (write-to-temp + rename, dry-run, rollback snapshots) land in Phase 1,
//! driven by crates/pi-patch which owns the lifecycle state machine.

const std = @import("std");
const jsonl = @import("jsonl");

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();
    try jsonl.printLine(
        allocator,
        "{{\"tool\":\"pi-patch-native\",\"status\":\"phase0-stub\",\"planned\":[\"apply\",\"verify\",\"rollback\",\"dry-run\"]}}",
        .{},
    );
}
