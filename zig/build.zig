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

    // carina-pty needs libc for the POSIX pseudo-terminal calls (posix_openpt,
    // grantpt, ptsname, ioctl, forkpty semantics).
    const needs_libc = std.StaticStringMap(void).initComptime(.{
        .{"carina-pty"},
    });

    const tools = [_][]const u8{
        "carina-scan",
        "carina-grep",
        "carina-diff",
        "carina-patch-native",
        "carina-run",
        "carina-pty",
    };

    for (tools) |tool| {
        const mod = b.createModule(.{
            .root_source_file = b.path(b.fmt("{s}/main.zig", .{tool})),
            .target = target,
            .optimize = optimize,
            .link_libc = needs_libc.has(tool),
        });
        mod.addImport("jsonl", jsonl_mod);

        const exe = b.addExecutable(.{
            .name = tool,
            .root_module = mod,
        });
        b.installArtifact(exe);
    }
}
