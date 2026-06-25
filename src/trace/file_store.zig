const std = @import("std");
const runtime = @import("../runtime/types.zig");
const trace = @import("types.zig");

const FileTraceError = error{
    SymlinkRejected,
};

pub const MemoryTraceStore = struct {
    events_appended: usize = 0,
    artifacts_written: usize = 0,

    pub fn writer(self: *MemoryTraceStore) trace.TraceWriter {
        return .{
            .context = self,
            .append_event = appendEvent,
            .append_jsonl = appendJsonl,
            .write_artifact = writeArtifact,
            .close = close,
        };
    }
};

fn appendEvent(writer: *trace.TraceWriter, _: runtime.RuntimeEventRecord) anyerror!void {
    const self: *MemoryTraceStore = @ptrCast(@alignCast(writer.context));
    self.events_appended += 1;
}

fn appendJsonl(writer: *trace.TraceWriter, _: trace.ArtifactKind, _: []const u8) anyerror!void {
    const self: *MemoryTraceStore = @ptrCast(@alignCast(writer.context));
    self.artifacts_written += 1;
}

fn writeArtifact(
    writer: *trace.TraceWriter,
    kind: trace.ArtifactKind,
    _: []const u8,
) anyerror!trace.ArtifactRef {
    const self: *MemoryTraceStore = @ptrCast(@alignCast(writer.context));
    self.artifacts_written += 1;
    return .{ .kind = kind, .path = trace.defaultArtifactPath(kind) };
}

fn close(_: *trace.TraceWriter) anyerror!void {}

pub const CreateOptions = struct {
    created_at_ms: runtime.TimestampMs = 0,
    provider: []const u8 = "unknown",
    task: []const u8 = "",
};

pub const FileTrace = struct {
    allocator: std.mem.Allocator,
    run_id: runtime.RunId,
    run_dir: std.fs.Dir,
    sequence: runtime.Sequence = 0,
    closed: bool = false,

    pub fn create(
        allocator: std.mem.Allocator,
        base_dir: std.fs.Dir,
        run_id: runtime.RunId,
        options: CreateOptions,
    ) !FileTrace {
        try runtime.RunId.validate(run_id.value);

        try makeRegularDir(base_dir, ".runs");
        var runs_dir = try base_dir.openDir(".runs", .{ .no_follow = true });
        defer runs_dir.close();

        try makeRegularDir(runs_dir, run_id.value);
        const run_dir = try runs_dir.openDir(run_id.value, .{ .no_follow = true });

        var self: FileTrace = .{
            .allocator = allocator,
            .run_id = run_id,
            .run_dir = run_dir,
        };
        errdefer self.deinit();

        try self.initializeArtifacts(options);
        return self;
    }

    pub fn deinit(self: *FileTrace) void {
        if (!self.closed) {
            self.run_dir.close();
            self.closed = true;
        }
    }

    pub fn fileWriter(self: *FileTrace) trace.TraceWriter {
        return .{
            .context = self,
            .append_event = appendEventFromWriter,
            .append_jsonl = appendJsonlFromWriter,
            .write_artifact = writeArtifactFromWriter,
            .close = closeFromWriter,
        };
    }

    pub fn appendEvent(
        self: *FileTrace,
        timestamp_ms: runtime.TimestampMs,
        event: runtime.RuntimeEvent,
    ) !runtime.RuntimeEventRecord {
        const record: runtime.RuntimeEventRecord = .{
            .run_id = self.run_id,
            .sequence = self.sequence,
            .timestamp_ms = timestamp_ms,
            .event = event,
        };
        try self.appendEventRecord(record);
        self.sequence += 1;
        return record;
    }

    pub fn appendEventRecord(self: *FileTrace, record: runtime.RuntimeEventRecord) !void {
        const line = try renderEventRecord(self.allocator, record);
        defer self.allocator.free(line);

        try self.appendLine(trace.defaultArtifactPath(.events), line);
        switch (record.event) {
            .process_spawned, .process_exited => try self.appendLine(
                trace.defaultArtifactPath(.commands),
                line,
            ),
            .sandbox_prepared, .sandbox_event => try self.appendLine(
                trace.defaultArtifactPath(.sandbox_events),
                line,
            ),
            .resource_sample => try self.appendLine(trace.defaultArtifactPath(.resources), line),
            else => {},
        }
    }

    pub fn appendJsonl(self: *FileTrace, kind: trace.ArtifactKind, line: []const u8) !void {
        try self.appendLine(trace.defaultArtifactPath(kind), line);
    }

    pub fn appendBytes(self: *FileTrace, kind: trace.ArtifactKind, bytes: []const u8) !void {
        try appendFileNoSymlink(self.run_dir, trace.defaultArtifactPath(kind), bytes);
    }

    pub fn writeArtifact(
        self: *FileTrace,
        kind: trace.ArtifactKind,
        contents: []const u8,
    ) !trace.ArtifactRef {
        const path = trace.defaultArtifactPath(kind);
        try writeFileNoSymlink(self.run_dir, path, contents);
        return .{ .kind = kind, .path = path };
    }

    fn initializeArtifacts(self: *FileTrace, options: CreateOptions) !void {
        const manifest = try std.fmt.allocPrint(
            self.allocator,
            "schema_version: 1\nrun_id: {s}\ncreated_at_ms: {d}\nprovider: {s}\ntask: {s}\n",
            .{ self.run_id.value, options.created_at_ms, options.provider, options.task },
        );
        defer self.allocator.free(manifest);
        try writeFileNoSymlink(self.run_dir, trace.defaultArtifactPath(.run_manifest), manifest);

        const empty_files = [_][]const u8{
            trace.defaultArtifactPath(.events),
            trace.defaultArtifactPath(.terminal_raw),
            trace.defaultArtifactPath(.terminal_redacted),
            trace.defaultArtifactPath(.terminal_snapshots),
            trace.defaultArtifactPath(.sandbox_events),
            trace.defaultArtifactPath(.resources),
            trace.defaultArtifactPath(.commands),
            trace.defaultArtifactPath(.approvals),
            trace.defaultArtifactPath(.test_results),
        };
        for (empty_files) |path| {
            try writeFileNoSymlink(self.run_dir, path, "");
        }
        try makeRegularDir(self.run_dir, trace.defaultArtifactPath(.screenshot));
        try makeRegularDir(self.run_dir, trace.defaultArtifactPath(.artifact));
    }

    fn appendLine(self: *FileTrace, path: []const u8, line: []const u8) !void {
        try appendFileNoSymlink(self.run_dir, path, line);
        if (line.len == 0 or line[line.len - 1] != '\n') {
            try appendFileNoSymlink(self.run_dir, path, "\n");
        }
    }
};

fn makeRegularDir(dir: std.fs.Dir, path: []const u8) !void {
    dir.makeDir(path) catch |err| switch (err) {
        error.PathAlreadyExists => {},
        else => |e| return e,
    };

    var opened = try dir.openDir(path, .{ .no_follow = true });
    opened.close();
}

fn appendFileNoSymlink(dir: std.fs.Dir, path: []const u8, bytes: []const u8) !void {
    try rejectSymlink(dir, path);
    var file = try dir.createFile(path, .{ .truncate = false });
    defer file.close();
    try file.seekFromEnd(0);
    try file.writeAll(bytes);
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
        return FileTraceError.SymlinkRejected;
    } else |err| switch (err) {
        error.NotLink, error.FileNotFound => return,
        else => |e| return e,
    }
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
        error.SymLinkLoop => return FileTraceError.SymlinkRejected,
        else => |e| return e,
    };

    const file: std.fs.File = .{ .handle = fd };
    defer file.close();

    return file.readToEndAlloc(allocator, max_bytes);
}

pub const TraceSummary = struct {
    run_id: ?[]u8 = null,
    final_state: ?[]u8 = null,
    event_count: usize = 0,
    runtime_errors: usize = 0,
    policy_decisions: usize = 0,
    commands: usize = 0,
    approvals_requested: usize = 0,
    approvals_decided: usize = 0,
    sandbox_events: usize = 0,
    sandbox_errors: usize = 0,
    resource_samples: usize = 0,
    peak_memory_bytes: ?u64 = null,
    cpu_millis: ?u64 = null,
    final_output_bytes: usize = 0,

    pub fn deinit(self: *TraceSummary, allocator: std.mem.Allocator) void {
        if (self.run_id) |value| allocator.free(value);
        if (self.final_state) |value| allocator.free(value);
        self.* = .{};
    }
};

pub fn summarizeEvents(
    allocator: std.mem.Allocator,
    run_dir: std.fs.Dir,
) !TraceSummary {
    const bytes = readFileAllocNoSymlink(
        run_dir,
        allocator,
        trace.defaultArtifactPath(.events),
        16 * 1024 * 1024,
    ) catch |err| switch (err) {
        error.FileNotFound => return .{},
        else => return err,
    };
    defer allocator.free(bytes);

    var summary: TraceSummary = .{};
    errdefer summary.deinit(allocator);

    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;
        summary.event_count += 1;

        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
        defer parsed.deinit();

        switch (parsed.value) {
            .object => |*object| try updateSummaryFromEvent(allocator, object, &summary),
            else => {},
        }
    }

    return summary;
}

fn appendEventFromWriter(writer: *trace.TraceWriter, record: runtime.RuntimeEventRecord) anyerror!void {
    const self: *FileTrace = @ptrCast(@alignCast(writer.context));
    try self.appendEventRecord(record);
    if (record.sequence >= self.sequence) self.sequence = record.sequence + 1;
}

fn appendJsonlFromWriter(
    writer: *trace.TraceWriter,
    kind: trace.ArtifactKind,
    line: []const u8,
) anyerror!void {
    const self: *FileTrace = @ptrCast(@alignCast(writer.context));
    try self.appendJsonl(kind, line);
}

fn writeArtifactFromWriter(
    writer: *trace.TraceWriter,
    kind: trace.ArtifactKind,
    contents: []const u8,
) anyerror!trace.ArtifactRef {
    const self: *FileTrace = @ptrCast(@alignCast(writer.context));
    return self.writeArtifact(kind, contents);
}

fn closeFromWriter(writer: *trace.TraceWriter) anyerror!void {
    const self: *FileTrace = @ptrCast(@alignCast(writer.context));
    self.deinit();
}

fn renderEventRecord(
    allocator: std.mem.Allocator,
    record: runtime.RuntimeEventRecord,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();

    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try field(&json, "schema_version", @as(u16, 1));
    try field(&json, "run_id", record.run_id.value);
    try field(&json, "sequence", record.sequence);
    try field(&json, "timestamp_ms", record.timestamp_ms);
    try field(&json, "type", @tagName(std.meta.activeTag(record.event)));

    switch (record.event) {
        .run_created => |event| {
            try field(&json, "task", event.task);
            try field(&json, "provider", event.provider);
        },
        .run_state_changed => |event| {
            try field(&json, "from", @tagName(event.from));
            try field(&json, "to", @tagName(event.to));
            try field(&json, "reason", event.reason);
        },
        .adapter_started => |event| {
            try field(&json, "provider", event.provider);
            try field(&json, "adapter", event.adapter);
            try field(&json, "mode", event.mode);
        },
        .adapter_output => |event| {
            try field(&json, "stream", @tagName(event.stream));
            try field(&json, "bytes", event.bytes);
        },
        .process_spawned => |event| {
            try field(&json, "argv", event.argv);
            try field(&json, "cwd", event.cwd);
            try field(&json, "pid", event.pid);
        },
        .process_exited => |event| {
            try field(&json, "pid", event.pid);
            try field(&json, "exit_code", event.exit_code);
            try field(&json, "signal", event.signal);
        },
        .terminal_effect => |event| {
            try field(&json, "effect", event.effect);
            try field(&json, "artifact_path", event.artifact_path);
        },
        .screen_snapshot => |event| {
            try field(&json, "artifact_kind", event.kind);
            try field(&json, "path", event.path);
        },
        .prompt_detected => |event| {
            try field(&json, "prompt_kind", event.prompt_kind);
            try field(&json, "text", event.text);
            try field(&json, "risk", event.risk);
        },
        .policy_decision => |event| {
            try field(&json, "action_kind", event.action_kind);
            try field(&json, "decision", event.decision);
            try field(&json, "rule_id", event.rule_id);
            try field(&json, "reason", event.reason);
        },
        .approval_requested => |event| {
            try field(&json, "approval_id", event.approval_id);
            try field(&json, "action_kind", event.action_kind);
            try field(&json, "risk", event.risk);
        },
        .approval_decision => |event| {
            try field(&json, "approval_id", event.approval_id);
            try field(&json, "decision", event.decision);
            try field(&json, "decided_by", event.decided_by);
        },
        .sandbox_prepared => |event| {
            try field(&json, "backend", event.backend);
            try field(&json, "mode", event.mode);
            try field(&json, "workspace_mount", event.workspace_mount);
        },
        .sandbox_event => |event| {
            try field(&json, "event_type", event.event_type);
            try field(&json, "detail", event.detail);
            try field(&json, "is_error", event.is_error);
        },
        .resource_sample => |event| {
            try field(&json, "memory_bytes", event.memory_bytes);
            try field(&json, "cpu_millis", event.cpu_millis);
            try field(&json, "pids", event.pids);
        },
        .trace_artifact => |event| {
            try field(&json, "artifact_kind", event.kind);
            try field(&json, "path", event.path);
        },
        .final_output => |event| {
            try field(&json, "text", event.text);
        },
        .error_reported => |event| {
            try field(&json, "message", event.message);
            try field(&json, "component", event.component);
        },
    }

    try json.endObject();
    try out.writer.writeByte('\n');
    return out.toOwnedSlice();
}

fn field(json: *std.json.Stringify, name: []const u8, value: anytype) !void {
    try json.objectField(name);
    try json.write(value);
}

fn updateSummaryFromEvent(
    allocator: std.mem.Allocator,
    object: *const std.json.ObjectMap,
    summary: *TraceSummary,
) !void {
    if (summary.run_id == null) {
        if (stringField(object, "run_id")) |run_id| {
            summary.run_id = try allocator.dupe(u8, run_id);
        }
    }

    const event_type = stringField(object, "type") orelse return;
    if (std.mem.eql(u8, event_type, "run_state_changed")) {
        if (stringField(object, "to")) |state| {
            if (summary.final_state) |previous| allocator.free(previous);
            summary.final_state = try allocator.dupe(u8, state);
        }
    } else if (std.mem.eql(u8, event_type, "error_reported")) {
        summary.runtime_errors += 1;
    } else if (std.mem.eql(u8, event_type, "policy_decision")) {
        summary.policy_decisions += 1;
    } else if (std.mem.eql(u8, event_type, "process_spawned")) {
        summary.commands += 1;
    } else if (std.mem.eql(u8, event_type, "approval_requested")) {
        summary.approvals_requested += 1;
    } else if (std.mem.eql(u8, event_type, "approval_decision")) {
        summary.approvals_decided += 1;
    } else if (std.mem.eql(u8, event_type, "sandbox_prepared") or
        std.mem.eql(u8, event_type, "sandbox_event"))
    {
        summary.sandbox_events += 1;
        if (boolField(object, "is_error") orelse false) summary.sandbox_errors += 1;
    } else if (std.mem.eql(u8, event_type, "resource_sample")) {
        summary.resource_samples += 1;
        if (intField(u64, object, "memory_bytes")) |memory| {
            summary.peak_memory_bytes = @max(summary.peak_memory_bytes orelse 0, memory);
        }
        if (intField(u64, object, "cpu_millis")) |cpu| {
            summary.cpu_millis = cpu;
        }
    } else if (std.mem.eql(u8, event_type, "final_output")) {
        if (stringField(object, "text")) |text| summary.final_output_bytes += text.len;
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

fn intField(comptime T: type, object: *const std.json.ObjectMap, name: []const u8) ?T {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .integer => |integer| if (integer >= 0) @intCast(integer) else null,
        else => null,
    };
}

test "memory trace store exposes trace writer contract" {
    var store: MemoryTraceStore = .{};
    var writer = store.writer();
    const run_id = try runtime.RunId.init("run-trace");

    try writer.append_event(&writer, .{
        .run_id = run_id,
        .sequence = 1,
        .timestamp_ms = 1,
        .event = .{ .final_output = .{ .text = "done" } },
    });
    const artifact = try writer.write_artifact(&writer, .final_output, "done");

    try std.testing.expectEqual(@as(usize, 1), store.events_appended);
    try std.testing.expectEqualStrings("final.md", artifact.path);
}

test "file trace writes events and sidecar resources" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const run_id = try runtime.RunId.init("01HXYZ");
    var store = try FileTrace.create(allocator, tmp.dir, run_id, .{
        .created_at_ms = 1,
        .provider = "fake",
        .task = "test",
    });
    defer store.deinit();

    _ = try store.appendEvent(2, .{ .run_created = .{
        .task = "test",
        .provider = "fake",
    } });
    _ = try store.appendEvent(3, .{ .resource_sample = .{
        .memory_bytes = 1024,
        .cpu_millis = 55,
        .pids = 3,
    } });
    _ = try store.appendEvent(4, .{ .process_spawned = .{
        .argv = &.{ "zig", "build", "test" },
        .cwd = "/workspace",
        .pid = 42,
    } });

    const events = try store.run_dir.readFileAlloc(
        allocator,
        trace.defaultArtifactPath(.events),
        4096,
    );
    defer allocator.free(events);
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"run_created\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"sequence\":1"));

    const resources = try store.run_dir.readFileAlloc(
        allocator,
        trace.defaultArtifactPath(.resources),
        4096,
    );
    defer allocator.free(resources);
    try std.testing.expect(std.mem.containsAtLeast(u8, resources, 1, "\"memory_bytes\":1024"));

    var summary = try summarizeEvents(allocator, store.run_dir);
    defer summary.deinit(allocator);
    try std.testing.expectEqual(@as(usize, 3), summary.event_count);
    try std.testing.expectEqual(@as(usize, 1), summary.commands);
    try std.testing.expectEqual(@as(usize, 1), summary.resource_samples);
    try std.testing.expectEqual(@as(u64, 1024), summary.peak_memory_bytes.?);

    const commands = try store.run_dir.readFileAlloc(
        allocator,
        trace.defaultArtifactPath(.commands),
        4096,
    );
    defer allocator.free(commands);
    try std.testing.expect(std.mem.containsAtLeast(u8, commands, 1, "\"type\":\"process_spawned\""));
}

test "file trace rejects unsafe run id path segments" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const unsafe_run_id: runtime.RunId = .{ .value = "../escape" };

    try std.testing.expectError(
        error.UnsafeRunId,
        FileTrace.create(allocator, tmp.dir, unsafe_run_id, .{}),
    );

    try std.testing.expectError(error.FileNotFound, tmp.dir.openDir("escape", .{}));
}

test "file trace rejects symlinked run directories" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.makePath(".runs");
    try tmp.dir.makePath("outside");
    try tmp.dir.symLink("../outside", ".runs/01HSYMLINK", .{ .is_directory = true });

    const run_id = try runtime.RunId.init("01HSYMLINK");
    try std.testing.expectError(
        error.NotDir,
        FileTrace.create(allocator, tmp.dir, run_id, .{}),
    );

    try std.testing.expectError(error.FileNotFound, tmp.dir.openFile("outside/run.yaml", .{}));
}

test "file trace rejects symlinked artifact files" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const run_id = try runtime.RunId.init("01HARTIFACT");
    var store = try FileTrace.create(allocator, tmp.dir, run_id, .{});
    defer store.deinit();

    try tmp.dir.writeFile(.{ .sub_path = "outside-events.jsonl", .data = "outside\n" });
    try store.run_dir.deleteFile(trace.defaultArtifactPath(.events));
    try store.run_dir.symLink("../../outside-events.jsonl", trace.defaultArtifactPath(.events), .{});

    try std.testing.expectError(
        error.SymlinkRejected,
        store.appendJsonl(.events, "inside"),
    );

    const outside = try tmp.dir.readFileAlloc(allocator, "outside-events.jsonl", 4096);
    defer allocator.free(outside);
    try std.testing.expectEqualStrings("outside\n", outside);
}

test "trace summary rejects symlinked event logs" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const run_id = try runtime.RunId.init("01HSUMMARY");
    var store = try FileTrace.create(allocator, tmp.dir, run_id, .{});
    defer store.deinit();

    try tmp.dir.writeFile(.{ .sub_path = "outside-events.jsonl", .data = "{}\n" });
    try store.run_dir.deleteFile(trace.defaultArtifactPath(.events));
    try store.run_dir.symLink("../../outside-events.jsonl", trace.defaultArtifactPath(.events), .{});

    try std.testing.expectError(
        error.SymlinkRejected,
        summarizeEvents(allocator, store.run_dir),
    );
}

test "trace summary replays terminal state and audit counts" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(.{
        .sub_path = trace.defaultArtifactPath(.events),
        .data =
        \\{"run_id":"01HXYZ","type":"run_state_changed","to":"running"}
        \\{"run_id":"01HXYZ","type":"process_spawned"}
        \\{"run_id":"01HXYZ","type":"policy_decision"}
        \\{"run_id":"01HXYZ","type":"approval_requested"}
        \\{"run_id":"01HXYZ","type":"approval_decision"}
        \\{"run_id":"01HXYZ","type":"sandbox_event","is_error":true}
        \\{"run_id":"01HXYZ","type":"error_reported"}
        \\{"run_id":"01HXYZ","type":"run_state_changed","to":"failed"}
        \\
        ,
    });

    var summary = try summarizeEvents(allocator, tmp.dir);
    defer summary.deinit(allocator);

    try std.testing.expectEqualStrings("01HXYZ", summary.run_id.?);
    try std.testing.expectEqualStrings("failed", summary.final_state.?);
    try std.testing.expectEqual(@as(usize, 1), summary.policy_decisions);
    try std.testing.expectEqual(@as(usize, 1), summary.commands);
    try std.testing.expectEqual(@as(usize, 1), summary.approvals_requested);
    try std.testing.expectEqual(@as(usize, 1), summary.approvals_decided);
    try std.testing.expectEqual(@as(usize, 1), summary.sandbox_errors);
    try std.testing.expectEqual(@as(usize, 1), summary.runtime_errors);
}
