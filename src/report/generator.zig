const std = @import("std");
const approvals = @import("../approvals/store.zig");
const redaction = @import("../redaction/redactor.zig");
const trace_file = @import("../trace/file_store.zig");
const trace = @import("../trace/types.zig");

const ReportError = error{
    SymlinkRejected,
};

const Summary = struct {
    run_id: ?[]u8 = null,
    final_state: ?[]u8 = null,
    event_count: usize = 0,
    approvals_requested: usize = 0,
    approvals_decided: usize = 0,
    sandbox_events: usize = 0,
    sandbox_errors: usize = 0,

    fn deinit(self: *Summary, allocator: std.mem.Allocator) void {
        if (self.run_id) |value| allocator.free(value);
        if (self.final_state) |value| allocator.free(value);
    }
};

const DiffSummary = struct {
    files: usize = 0,
    added_lines: usize = 0,
    removed_lines: usize = 0,
};

pub fn generate(
    allocator: std.mem.Allocator,
    run_dir: std.fs.Dir,
    redactor: redaction.Redactor,
) !trace.ArtifactRef {
    const sandbox_events = try readOptional(
        allocator,
        run_dir,
        trace.defaultArtifactPath(.sandbox_events),
    );
    defer allocator.free(sandbox_events);
    const final_output = try readOptional(allocator, run_dir, trace.defaultArtifactPath(.final_output));
    defer allocator.free(final_output);
    const diff_patch = try readOptional(allocator, run_dir, trace.defaultArtifactPath(.diff_patch));
    defer allocator.free(diff_patch);

    var trace_summary = try trace_file.summarizeEvents(allocator, run_dir);
    defer trace_summary.deinit(allocator);
    const approval_summary = try approvals.summarize(allocator, run_dir);
    var sandbox_sidecar: Summary = .{};
    defer sandbox_sidecar.deinit(allocator);
    try summarizeSandbox(allocator, sandbox_events, &sandbox_sidecar);
    const diff_summary = summarizeDiff(diff_patch);
    const sandbox_event_count = @max(trace_summary.sandbox_events, sandbox_sidecar.sandbox_events);
    const sandbox_error_count = @max(trace_summary.sandbox_errors, sandbox_sidecar.sandbox_errors);

    var out: std.io.Writer.Allocating = .init(allocator);
    defer out.deinit();

    try out.writer.print("# Run {s}\n\n", .{trace_summary.run_id orelse "unknown"});
    try out.writer.print("- Final state: {s}\n", .{trace_summary.final_state orelse "unknown"});
    try out.writer.print("- Events: {d}\n", .{trace_summary.event_count});
    try out.writer.print("- Runtime errors: {d}\n", .{trace_summary.runtime_errors});
    try out.writer.print("- Policy decisions: {d}\n", .{trace_summary.policy_decisions});
    try out.writer.print("- Commands: {d}\n", .{trace_summary.commands});
    try out.writer.print("- Approvals requested: {d}\n", .{approval_summary.requested});
    try out.writer.print("- Approvals decided: {d}\n", .{approval_summary.decided});
    try out.writer.print("- Approvals pending: {d}\n", .{approval_summary.pending});
    try out.writer.print("- Sandbox events: {d}\n", .{sandbox_event_count});
    try out.writer.print("- Sandbox errors: {d}\n", .{sandbox_error_count});
    try out.writer.print("- Resource samples: {d}\n", .{trace_summary.resource_samples});
    if (trace_summary.peak_memory_bytes) |memory| {
        try out.writer.print("- Peak memory bytes: {d}\n", .{memory});
    }
    if (trace_summary.cpu_millis) |cpu| {
        try out.writer.print("- CPU millis: {d}\n", .{cpu});
    }
    try out.writer.print("- Diff files: {d}\n", .{diff_summary.files});
    try out.writer.print("- Diff added lines: {d}\n", .{diff_summary.added_lines});
    try out.writer.print("- Diff removed lines: {d}\n\n", .{diff_summary.removed_lines});

    try out.writer.writeAll("## Final Output\n\n");
    if (std.mem.trim(u8, final_output, " \t\r\n").len == 0) {
        try out.writer.writeAll("(none)\n\n");
    } else {
        try out.writer.writeAll(final_output);
        if (final_output[final_output.len - 1] != '\n') try out.writer.writeByte('\n');
        try out.writer.writeByte('\n');
    }

    try out.writer.writeAll("## Diff\n\n");
    if (std.mem.trim(u8, diff_patch, " \t\r\n").len == 0) {
        try out.writer.writeAll("(none)\n");
    } else {
        try out.writer.writeAll("```diff\n");
        try out.writer.writeAll(diff_patch);
        if (diff_patch[diff_patch.len - 1] != '\n') try out.writer.writeByte('\n');
        try out.writer.writeAll("```\n");
    }

    const report = try out.toOwnedSlice();
    defer allocator.free(report);
    const redacted = try redactor.redactAlloc(allocator, .report, report);
    defer allocator.free(redacted);

    try writeFileNoSymlink(run_dir, trace.defaultArtifactPath(.report), redacted);
    return .{ .kind = .report, .path = trace.defaultArtifactPath(.report) };
}

fn summarizeDiff(bytes: []const u8) DiffSummary {
    var summary: DiffSummary = .{};
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |line| {
        if (std.mem.startsWith(u8, line, "diff --git ")) {
            summary.files += 1;
        } else if (std.mem.startsWith(u8, line, "+") and !std.mem.startsWith(u8, line, "+++")) {
            summary.added_lines += 1;
        } else if (std.mem.startsWith(u8, line, "-") and !std.mem.startsWith(u8, line, "---")) {
            summary.removed_lines += 1;
        }
    }
    return summary;
}

fn readOptional(
    allocator: std.mem.Allocator,
    dir: std.fs.Dir,
    path: []const u8,
) ![]u8 {
    return readFileAllocNoSymlink(dir, allocator, path, 16 * 1024 * 1024) catch |err| switch (err) {
        error.FileNotFound => try allocator.dupe(u8, ""),
        else => err,
    };
}

fn readFileAllocNoSymlink(
    dir: std.fs.Dir,
    allocator: std.mem.Allocator,
    path: []const u8,
    max_bytes: usize,
) ![]u8 {
    const fd = std.posix.openat(
        dir.fd,
        path,
        .{ .ACCMODE = .RDONLY, .NOFOLLOW = true, .CLOEXEC = true },
        0,
    ) catch |err| switch (err) {
        error.SymLinkLoop => return ReportError.SymlinkRejected,
        else => |e| return e,
    };

    const file: std.fs.File = .{ .handle = fd };
    defer file.close();

    return file.readToEndAlloc(allocator, max_bytes);
}

fn writeFileNoSymlink(dir: std.fs.Dir, path: []const u8, bytes: []const u8) !void {
    try rejectSymlink(dir, path);
    dir.deleteFile(path) catch |err| switch (err) {
        error.FileNotFound => {},
        else => |e| return e,
    };

    var file = try dir.createFile(path, .{ .exclusive = true });
    defer file.close();
    try file.writeAll(bytes);
}

fn rejectSymlink(dir: std.fs.Dir, path: []const u8) !void {
    var buffer: [std.fs.max_path_bytes]u8 = undefined;
    if (dir.readLink(path, &buffer)) |_| {
        return ReportError.SymlinkRejected;
    } else |err| switch (err) {
        error.NotLink, error.FileNotFound => return,
        else => |e| return e,
    }
}

fn summarizeEvents(allocator: std.mem.Allocator, bytes: []const u8, summary: *Summary) !void {
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;
        summary.event_count += 1;

        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
        defer parsed.deinit();

        switch (parsed.value) {
            .object => |*object| {
                if (summary.run_id == null) {
                    if (stringField(object, "run_id")) |run_id| {
                        summary.run_id = try allocator.dupe(u8, run_id);
                    }
                }

                const event_type = stringField(object, "type") orelse continue;
                if (std.mem.eql(u8, event_type, "run_state_changed")) {
                    if (stringField(object, "to")) |state| {
                        if (summary.final_state) |previous| allocator.free(previous);
                        summary.final_state = try allocator.dupe(u8, state);
                    }
                }
            },
            else => {},
        }
    }
}

fn summarizeApprovals(allocator: std.mem.Allocator, bytes: []const u8, summary: *Summary) !void {
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;

        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
        defer parsed.deinit();

        switch (parsed.value) {
            .object => |*object| {
                const record_type = stringField(object, "type") orelse continue;
                if (std.mem.eql(u8, record_type, "approval_requested")) {
                    summary.approvals_requested += 1;
                } else if (std.mem.eql(u8, record_type, "approval_decision")) {
                    summary.approvals_decided += 1;
                }
            },
            else => {},
        }
    }
}

fn summarizeSandbox(allocator: std.mem.Allocator, bytes: []const u8, summary: *Summary) !void {
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;

        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
        defer parsed.deinit();

        switch (parsed.value) {
            .object => |*object| {
                summary.sandbox_events += 1;
                if (boolField(object, "is_error") orelse false) summary.sandbox_errors += 1;
            },
            else => {},
        }
    }
}

fn stringField(object: *const std.json.ObjectMap, name: []const u8) ?[]const u8 {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .string => |text| text,
        else => null,
    };
}

fn boolField(object: *const std.json.ObjectMap, name: []const u8) ?bool {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .bool => |flag| flag,
        else => null,
    };
}

test "report is generated from trace artifacts and redacted" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.writeFile(.{
        .sub_path = trace.defaultArtifactPath(.events),
        .data =
        \\{"run_id":"01HXYZ","type":"run_created"}
        \\{"run_id":"01HXYZ","type":"process_spawned"}
        \\{"run_id":"01HXYZ","type":"run_state_changed","to":"completed"}
        \\
        ,
    });
    try tmp.dir.writeFile(.{
        .sub_path = trace.defaultArtifactPath(.approvals),
        .data =
        \\{"type":"approval_requested"}
        \\{"type":"approval_decision"}
        \\
        ,
    });
    try tmp.dir.writeFile(.{
        .sub_path = trace.defaultArtifactPath(.sandbox_events),
        .data =
        \\{"type":"sandbox_event","is_error":true}
        \\
        ,
    });
    try tmp.dir.writeFile(.{
        .sub_path = trace.defaultArtifactPath(.final_output),
        .data = "done with SECRET_TOKEN\n",
    });
    try tmp.dir.writeFile(.{
        .sub_path = trace.defaultArtifactPath(.diff_patch),
        .data = "",
    });

    const rules = [_]redaction.RedactionRule{
        .{ .id = "secret", .pattern = "SECRET_TOKEN" },
    };
    const ref = try generate(allocator, tmp.dir, .{ .rules = &rules });
    try std.testing.expectEqual(trace.ArtifactKind.report, ref.kind);

    const report = try tmp.dir.readFileAlloc(
        allocator,
        trace.defaultArtifactPath(.report),
        4096,
    );
    defer allocator.free(report);

    try std.testing.expect(std.mem.containsAtLeast(u8, report, 1, "Final state: completed"));
    try std.testing.expect(std.mem.containsAtLeast(u8, report, 1, "Commands: 1"));
    try std.testing.expect(std.mem.containsAtLeast(u8, report, 1, "Sandbox errors: 1"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, report, 1, "SECRET_TOKEN"));
}

test "report generation rejects symlinked input artifacts" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.writeFile(.{ .sub_path = "outside-final.md", .data = "outside secret\n" });
    try tmp.dir.symLink("outside-final.md", trace.defaultArtifactPath(.final_output), .{});

    try std.testing.expectError(
        error.SymlinkRejected,
        generate(allocator, tmp.dir, .{}),
    );

    try std.testing.expectError(error.FileNotFound, tmp.dir.openFile(trace.defaultArtifactPath(.report), .{}));
}

test "report generation rejects symlinked output targets" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.writeFile(.{
        .sub_path = trace.defaultArtifactPath(.events),
        .data =
        \\{"run_id":"01HXYZ","type":"run_created"}
        \\
        ,
    });
    try tmp.dir.writeFile(.{ .sub_path = "outside-report.md", .data = "outside\n" });
    try tmp.dir.symLink("outside-report.md", trace.defaultArtifactPath(.report), .{});

    try std.testing.expectError(
        error.SymlinkRejected,
        generate(allocator, tmp.dir, .{}),
    );

    const outside = try tmp.dir.readFileAlloc(allocator, "outside-report.md", 4096);
    defer allocator.free(outside);
    try std.testing.expectEqualStrings("outside\n", outside);
}
