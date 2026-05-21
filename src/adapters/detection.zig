const std = @import("std");

pub fn executableAvailable(allocator: std.mem.Allocator, name: []const u8) !bool {
    const path_value = std.process.getEnvVarOwned(allocator, "PATH") catch |err| switch (err) {
        error.EnvironmentVariableNotFound => return false,
        else => return err,
    };
    defer allocator.free(path_value);

    var it = std.mem.splitScalar(u8, path_value, std.fs.path.delimiter);
    while (it.next()) |dir| {
        if (dir.len == 0) continue;

        const candidate = try std.fs.path.join(allocator, &.{ dir, name });
        defer allocator.free(candidate);

        std.fs.cwd().access(candidate, .{}) catch continue;
        return true;
    }

    return false;
}

pub fn commandOutputContains(
    allocator: std.mem.Allocator,
    argv: []const []const u8,
    needle: []const u8,
) !bool {
    const output = try commandOutputAlloc(allocator, argv);
    defer allocator.free(output);

    return std.mem.indexOf(u8, output, needle) != null;
}

pub fn commandOutputAlloc(
    allocator: std.mem.Allocator,
    argv: []const []const u8,
) ![]u8 {
    const result = try std.process.Child.run(.{
        .allocator = allocator,
        .argv = argv,
        .max_output_bytes = 64 * 1024,
    });
    defer allocator.free(result.stdout);
    defer allocator.free(result.stderr);

    return try std.mem.concat(allocator, u8, &.{ result.stdout, result.stderr });
}

pub fn commandOutputTrimmedAlloc(
    allocator: std.mem.Allocator,
    argv: []const []const u8,
) ![]u8 {
    const output = try commandOutputAlloc(allocator, argv);
    defer allocator.free(output);
    return try allocator.dupe(u8, std.mem.trim(u8, output, " \t\r\n"));
}

pub fn authStatusFromEnv(
    allocator: std.mem.Allocator,
    keys: []const []const u8,
) !@import("types.zig").AuthStatus {
    for (keys) |key| {
        if (try std.process.hasEnvVar(allocator, key)) return .authenticated;
    }
    return .unknown;
}

test "missing executable is reported unavailable" {
    try std.testing.expect(!try executableAvailable(
        std.testing.allocator,
        "mitos-definitely-not-present",
    ));
}

test "command output detection scans stdout" {
    try std.testing.expect(try commandOutputContains(
        std.testing.allocator,
        &.{ "sh", "-c", "printf feature-json" },
        "feature-json",
    ));
}

test "command output helper returns trimmed output" {
    const output = try commandOutputTrimmedAlloc(
        std.testing.allocator,
        &.{ "sh", "-c", "printf '  version 1  \\n'" },
    );
    defer std.testing.allocator.free(output);

    try std.testing.expectEqualStrings("version 1", output);
}

test "auth status helper leaves missing env-based auth unknown" {
    const status = try authStatusFromEnv(
        std.testing.allocator,
        &.{"MITOS_DEFINITELY_MISSING_PROVIDER_KEY"},
    );

    try std.testing.expectEqual(@import("types.zig").AuthStatus.unknown, status);
}
