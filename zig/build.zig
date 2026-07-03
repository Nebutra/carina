const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});

    // Shared JSON-line output helpers used by every tool.
    const jsonl_mod = b.createModule(.{
        .root_source_file = b.path("common/jsonl.zig"),
        .target = target,
        .optimize = optimize,
    });

    const tools = [_][]const u8{
        "pi-scan",
        "pi-grep",
        "pi-diff",
        "pi-patch-native",
        "pi-run",
        "pi-pty",
    };

    for (tools) |tool| {
        const mod = b.createModule(.{
            .root_source_file = b.path(b.fmt("{s}/main.zig", .{tool})),
            .target = target,
            .optimize = optimize,
        });
        mod.addImport("jsonl", jsonl_mod);

        const exe = b.addExecutable(.{
            .name = tool,
            .root_module = mod,
        });
        b.installArtifact(exe);
    }
}
