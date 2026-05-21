const std = @import("std");

const required_files = [_]struct {
    path: []const u8,
    label: []const u8,
}{
    .{ .path = "build.zig", .label = "build script" },
    .{ .path = "build.zig.zon", .label = "package manifest" },
    .{ .path = "src/lib_vt.zig", .label = "Zig libghostty-vt API" },
    .{ .path = "src/build/GhosttyZig.zig", .label = "Ghostty Zig module builder" },
    .{ .path = "src/build/GhosttyLibVt.zig", .label = "libghostty-vt artifact builder" },
    .{ .path = "include/ghostty/vt.h", .label = "C umbrella header" },
    .{ .path = "include/ghostty/vt/terminal.h", .label = "C terminal API" },
    .{ .path = "include/ghostty/vt/screen.h", .label = "C screen API" },
};

pub fn main() !void {
    var arena_state = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena_state.deinit();
    const allocator = arena_state.allocator();

    const args = try std.process.argsAlloc(allocator);
    if (args.len != 3) {
        std.debug.print("usage: {s} <ghostty-source> <expected-commit>\n", .{args[0]});
        return error.InvalidArguments;
    }

    const source_path = args[1];
    const expected_commit = args[2];

    for (required_files) |file| {
        try requireFile(allocator, source_path, file.path, file.label);
    }

    try checkGitHead(allocator, source_path, expected_commit);
    std.debug.print("ghostty source ok: {s}\n", .{source_path});
}

fn requireFile(
    allocator: std.mem.Allocator,
    source_path: []const u8,
    rel_path: []const u8,
    label: []const u8,
) !void {
    const path = try std.fs.path.join(allocator, &.{ source_path, rel_path });
    const file = std.fs.cwd().openFile(path, .{}) catch |err| {
        std.debug.print("missing Ghostty {s}: {s} ({s})\n", .{ label, path, @errorName(err) });
        return error.MissingGhosttyFile;
    };
    file.close();
}

fn checkGitHead(
    allocator: std.mem.Allocator,
    source_path: []const u8,
    expected_commit: []const u8,
) !void {
    if (expected_commit.len == 0) return;

    const git_dir = try resolveGitDir(allocator, source_path) orelse {
        std.debug.print("ghostty source has no .git metadata; commit check skipped\n", .{});
        return;
    };

    const head_path = try std.fs.path.join(allocator, &.{ git_dir, "HEAD" });
    const head_text = try readSmallFile(allocator, head_path);
    const head = std.mem.trim(u8, head_text, " \t\r\n");

    const actual = if (std.mem.startsWith(u8, head, "ref: "))
        try readRefCommit(allocator, git_dir, std.mem.trim(u8, head["ref: ".len..], " \t\r\n"))
    else
        head;

    if (!std.mem.eql(u8, actual, expected_commit)) {
        std.debug.print(
            "Ghostty commit mismatch: expected {s}, found {s}\n",
            .{ expected_commit, actual },
        );
        return error.GhosttyCommitMismatch;
    }
}

fn resolveGitDir(allocator: std.mem.Allocator, source_path: []const u8) !?[]const u8 {
    const dot_git = try std.fs.path.join(allocator, &.{ source_path, ".git" });

    const head_in_dir = try std.fs.path.join(allocator, &.{ dot_git, "HEAD" });
    if (std.fs.cwd().access(head_in_dir, .{})) |_| {
        return dot_git;
    } else |err| switch (err) {
        error.FileNotFound => {},
        else => return err,
    }

    const dot_git_text = readSmallFile(allocator, dot_git) catch |err| switch (err) {
        error.FileNotFound, error.IsDir => return null,
        else => return err,
    };
    const trimmed = std.mem.trim(u8, dot_git_text, " \t\r\n");
    const prefix = "gitdir: ";
    if (!std.mem.startsWith(u8, trimmed, prefix)) return null;

    const git_dir = std.mem.trim(u8, trimmed[prefix.len..], " \t\r\n");
    if (std.fs.path.isAbsolute(git_dir)) return git_dir;
    return try std.fs.path.join(allocator, &.{ source_path, git_dir });
}

fn readRefCommit(
    allocator: std.mem.Allocator,
    git_dir: []const u8,
    ref_path: []const u8,
) ![]const u8 {
    const loose_ref_path = try std.fs.path.join(allocator, &.{ git_dir, ref_path });
    const loose_ref = readSmallFile(allocator, loose_ref_path) catch |err| switch (err) {
        error.FileNotFound => return try readPackedRef(allocator, git_dir, ref_path),
        else => return err,
    };
    return std.mem.trim(u8, loose_ref, " \t\r\n");
}

fn readPackedRef(
    allocator: std.mem.Allocator,
    git_dir: []const u8,
    ref_path: []const u8,
) ![]const u8 {
    const packed_refs_path = try std.fs.path.join(allocator, &.{ git_dir, "packed-refs" });
    const packed_refs = try readSmallFile(allocator, packed_refs_path);
    var lines = std.mem.splitScalar(u8, packed_refs, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r\n");
        if (line.len == 0 or line[0] == '#' or line[0] == '^') continue;

        var parts = std.mem.splitScalar(u8, line, ' ');
        const commit = parts.next() orelse continue;
        const name = parts.next() orelse continue;
        if (std.mem.eql(u8, name, ref_path)) return commit;
    }

    std.debug.print("unable to resolve Ghostty git ref: {s}\n", .{ref_path});
    return error.GhosttyRefNotFound;
}

fn readSmallFile(allocator: std.mem.Allocator, path: []const u8) ![]const u8 {
    return try std.fs.cwd().readFileAlloc(allocator, path, 1024 * 1024);
}
