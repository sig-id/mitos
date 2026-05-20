const std = @import("std");
const runtime = @import("../runtime/types.zig");

pub const artifact_paths = struct {
    pub const run_manifest = "run.yaml";
    pub const events = "events.jsonl";
    pub const terminal_raw = "terminal.raw";
    pub const terminal_redacted = "terminal.redacted.txt";
    pub const terminal_snapshots = "terminal.snapshots.jsonl";
    pub const sandbox_events = "sandbox.events.jsonl";
    pub const resources = "resources.jsonl";
    pub const final_output = "final.md";
    pub const diff_patch = "diff.patch";
    pub const files_changed = "files_changed.json";
    pub const commands = "commands.jsonl";
    pub const approvals = "approvals.jsonl";
    pub const test_results = "test_results.jsonl";
    pub const screenshots_dir = "screenshots";
    pub const artifacts_dir = "artifacts";
};

pub const ArtifactKind = enum {
    run_manifest,
    events,
    terminal_raw,
    terminal_redacted,
    terminal_snapshots,
    sandbox_events,
    resources,
    final_output,
    diff_patch,
    files_changed,
    commands,
    approvals,
    test_results,
    screenshot,
    artifact,
};

pub fn defaultArtifactPath(kind: ArtifactKind) []const u8 {
    return switch (kind) {
        .run_manifest => artifact_paths.run_manifest,
        .events => artifact_paths.events,
        .terminal_raw => artifact_paths.terminal_raw,
        .terminal_redacted => artifact_paths.terminal_redacted,
        .terminal_snapshots => artifact_paths.terminal_snapshots,
        .sandbox_events => artifact_paths.sandbox_events,
        .resources => artifact_paths.resources,
        .final_output => artifact_paths.final_output,
        .diff_patch => artifact_paths.diff_patch,
        .files_changed => artifact_paths.files_changed,
        .commands => artifact_paths.commands,
        .approvals => artifact_paths.approvals,
        .test_results => artifact_paths.test_results,
        .screenshot => artifact_paths.screenshots_dir,
        .artifact => artifact_paths.artifacts_dir,
    };
}

pub const JsonlEventFormat = struct {
    schema_version: u16 = 1,
    line_delimiter: u8 = '\n',
    encoding: []const u8 = "utf-8",
};

pub const ArtifactRef = struct {
    kind: ArtifactKind,
    path: []const u8,
};

pub const TraceWriter = struct {
    context: *anyopaque,
    append_event: *const fn (*TraceWriter, runtime.RuntimeEventRecord) anyerror!void,
    append_jsonl: *const fn (*TraceWriter, ArtifactKind, []const u8) anyerror!void,
    write_artifact: *const fn (*TraceWriter, ArtifactKind, []const u8) anyerror!ArtifactRef,
    close: *const fn (*TraceWriter) anyerror!void,
};

test "artifact paths match product artifact names" {
    try std.testing.expectEqualStrings("events.jsonl", defaultArtifactPath(.events));
    try std.testing.expectEqualStrings("terminal.raw", defaultArtifactPath(.terminal_raw));
    try std.testing.expectEqualStrings("sandbox.events.jsonl", defaultArtifactPath(.sandbox_events));
    try std.testing.expectEqualStrings("final.md", defaultArtifactPath(.final_output));
}

test "jsonl format defaults to newline-delimited utf-8" {
    const format: JsonlEventFormat = .{};

    try std.testing.expectEqual(@as(u16, 1), format.schema_version);
    try std.testing.expectEqual(@as(u8, '\n'), format.line_delimiter);
    try std.testing.expectEqualStrings("utf-8", format.encoding);
}
