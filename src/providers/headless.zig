const std = @import("std");
const adapters = @import("../adapters/types.zig");

pub const DEFAULT_MAX_OUTPUT_BYTES = 8 * 1024 * 1024;

pub const HeadlessCommand = struct {
    argv: []const []const u8,
    cwd: []const u8,
    environment: []const adapters.EnvVar = &.{},

    pub fn deinit(self: HeadlessCommand, allocator: std.mem.Allocator) void {
        if (self.argv.len > 0) allocator.free(self.argv);
    }
};

pub const ExecuteOptions = struct {
    max_output_bytes: usize = DEFAULT_MAX_OUTPUT_BYTES,
};

pub const HeadlessResult = struct {
    stdout: []u8,
    stderr: []u8,
    exit_code: ?u8 = null,
    signal: ?u8 = null,

    pub fn deinit(self: HeadlessResult, allocator: std.mem.Allocator) void {
        if (self.stdout.len > 0) allocator.free(self.stdout);
        if (self.stderr.len > 0) allocator.free(self.stderr);
    }

    pub fn status(self: HeadlessResult) adapters.AdapterRunStatus {
        if (self.exit_code) |code| {
            return if (code == 0) .completed else .failed;
        }
        return .failed;
    }
};

pub fn emitLaunchPlan(
    sink: ?adapters.AdapterEventSink,
    provider: []const u8,
    adapter: []const u8,
) !void {
    if (sink) |event_sink| {
        try event_sink.emit(event_sink.context, .{ .structured = providerLaunchJson(provider, adapter) });
    }
}

pub fn execute(
    allocator: std.mem.Allocator,
    command: HeadlessCommand,
    options: ExecuteOptions,
) !HeadlessResult {
    var env_map: ?std.process.EnvMap = null;
    defer if (env_map) |*map| map.deinit();

    const env_map_ptr = if (command.environment.len > 0) env: {
        env_map = try std.process.getEnvMap(allocator);
        for (command.environment) |entry| {
            try env_map.?.put(entry.name, entry.value);
        }
        break :env &env_map.?;
    } else null;

    const owned_arg0: ?[]u8 = if (command.argv.len > 0) try resolveArg0FromEnvironment(
        allocator,
        command.argv[0],
        command.cwd,
        command.environment,
    ) else null;
    defer if (owned_arg0) |arg0| allocator.free(arg0);

    var owned_argv: ?[][]const u8 = null;
    defer if (owned_argv) |argv| allocator.free(argv);

    const argv = if (owned_arg0) |arg0| argv: {
        const copy = try allocator.alloc([]const u8, command.argv.len);
        @memcpy(copy, command.argv);
        copy[0] = arg0;
        owned_argv = copy;
        break :argv copy;
    } else command.argv;

    const result = try std.process.Child.run(.{
        .allocator = allocator,
        .argv = argv,
        .cwd = command.cwd,
        .env_map = env_map_ptr,
        .max_output_bytes = options.max_output_bytes,
    });

    return .{
        .stdout = result.stdout,
        .stderr = result.stderr,
        .exit_code = switch (result.term) {
            .Exited => |code| code,
            else => null,
        },
        .signal = switch (result.term) {
            .Signal => |signal| @as(u8, @truncate(signal)),
            else => null,
        },
    };
}

pub fn executeAndEmit(
    allocator: std.mem.Allocator,
    command: HeadlessCommand,
    sink: ?adapters.AdapterEventSink,
    options: ExecuteOptions,
) !HeadlessResult {
    if (sink) |event_sink| {
        try event_sink.emit(event_sink.context, .{ .process_spawned = .{
            .argv = command.argv,
            .cwd = command.cwd,
            .pid = null,
        } });
    }

    const result = try execute(allocator, command, options);

    if (sink) |event_sink| {
        if (result.stdout.len > 0) {
            try event_sink.emit(event_sink.context, .{ .stdout = result.stdout });
            try emitStructuredJsonl(allocator, event_sink, result.stdout);
        }
        if (result.stderr.len > 0) {
            try event_sink.emit(event_sink.context, .{ .stderr = result.stderr });
        }

        try event_sink.emit(event_sink.context, .{ .process_exited = .{
            .pid = null,
            .exit_code = result.exit_code,
            .signal = result.signal,
        } });

        if (result.status() == .completed) {
            const final_output = try extractFinalOutputAlloc(allocator, result.stdout);
            defer allocator.free(final_output);
            if (final_output.len > 0) {
                try event_sink.emit(event_sink.context, .{ .final_output = final_output });
            }
        } else {
            try event_sink.emit(event_sink.context, .{ .error_message = if (result.stderr.len > 0)
                result.stderr
            else
                "headless provider command failed" });
        }
    }

    return result;
}

pub fn emitStructuredJsonl(
    allocator: std.mem.Allocator,
    sink: adapters.AdapterEventSink,
    bytes: []const u8,
) !void {
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;
        if (!try isJsonObjectLine(allocator, line)) continue;

        try sink.emit(sink.context, .{ .structured = line });
    }
}

pub fn extractFinalOutputAlloc(
    allocator: std.mem.Allocator,
    stdout: []const u8,
) ![]u8 {
    var last_candidate: ?[]u8 = null;
    errdefer if (last_candidate) |candidate| allocator.free(candidate);

    var lines = std.mem.splitScalar(u8, stdout, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;

        var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch {
            try replaceCandidate(allocator, &last_candidate, line);
            continue;
        };
        defer parsed.deinit();

        switch (parsed.value) {
            .object => |*object| {
                if (stringField(object, "final_output")) |text| {
                    try replaceCandidate(allocator, &last_candidate, text);
                }
                if (stringField(object, "last_message")) |text| {
                    try replaceCandidate(allocator, &last_candidate, text);
                }
                if (stringField(object, "message")) |text| {
                    try replaceCandidate(allocator, &last_candidate, text);
                }
                if (stringField(object, "content")) |text| {
                    try replaceCandidate(allocator, &last_candidate, text);
                }
                if (stringField(object, "result")) |text| {
                    try replaceCandidate(allocator, &last_candidate, text);
                }
                if (stringField(object, "response")) |text| {
                    try replaceCandidate(allocator, &last_candidate, text);
                }
                if (stringField(object, "text")) |text| {
                    try replaceCandidate(allocator, &last_candidate, text);
                }
            },
            else => {},
        }
    }

    if (last_candidate) |candidate| {
        last_candidate = null;
        return candidate;
    }

    return try allocator.dupe(u8, std.mem.trim(u8, stdout, " \t\r\n"));
}

fn providerLaunchJson(provider: []const u8, adapter: []const u8) []const u8 {
    if (std.mem.eql(u8, provider, "codex") and std.mem.eql(u8, adapter, "codex.exec")) {
        return "{\"type\":\"headless_command_built\",\"provider\":\"codex\",\"adapter\":\"codex.exec\"}";
    }
    return "{\"type\":\"headless_command_built\"}";
}

fn resolveArg0FromEnvironment(
    allocator: std.mem.Allocator,
    arg0: []const u8,
    cwd: []const u8,
    environment: []const adapters.EnvVar,
) !?[]u8 {
    if (arg0.len == 0 or std.mem.indexOfScalar(u8, arg0, '/') != null) return null;

    const path_value = explicitPathValue(environment) orelse return null;
    var paths = std.mem.splitScalar(u8, path_value, ':');
    while (paths.next()) |raw_path| {
        const search_path = if (raw_path.len == 0) "." else raw_path;
        const candidate = if (std.fs.path.isAbsolute(search_path))
            try std.fs.path.join(allocator, &.{ search_path, arg0 })
        else
            try std.fs.path.join(allocator, &.{ cwd, search_path, arg0 });
        errdefer allocator.free(candidate);

        const exists = if (std.fs.path.isAbsolute(candidate)) exists: {
            std.fs.accessAbsolute(candidate, .{}) catch break :exists false;
            break :exists true;
        } else exists: {
            std.fs.cwd().access(candidate, .{}) catch break :exists false;
            break :exists true;
        };
        if (exists) return candidate;

        allocator.free(candidate);
    }

    return null;
}

fn explicitPathValue(environment: []const adapters.EnvVar) ?[]const u8 {
    var path_value: ?[]const u8 = null;
    for (environment) |entry| {
        if (std.mem.eql(u8, entry.name, "PATH")) {
            path_value = entry.value;
        }
    }
    return path_value;
}

fn isJsonObjectLine(allocator: std.mem.Allocator, line: []const u8) !bool {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch return false;
    defer parsed.deinit();

    return parsed.value == .object;
}

fn stringField(object: *const std.json.ObjectMap, name: []const u8) ?[]const u8 {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .string => |text| text,
        else => null,
    };
}

fn replaceCandidate(
    allocator: std.mem.Allocator,
    candidate: *?[]u8,
    text: []const u8,
) !void {
    if (candidate.*) |owned| allocator.free(owned);
    candidate.* = try allocator.dupe(u8, text);
}

test "headless command owns argv array only" {
    const argv = try std.testing.allocator.alloc([]const u8, 2);
    argv[0] = "agent";
    argv[1] = "task";

    const command: HeadlessCommand = .{ .argv = argv, .cwd = "/workspace" };
    command.deinit(std.testing.allocator);
}

test "headless command executes process and emits output events" {
    const argv = [_][]const u8{ "sh", "-c", "printf '{\"final_output\":\"done\"}\\n'; printf warn >&2" };
    var recorder: EventRecorder = .{};
    const result = try executeAndEmit(std.testing.allocator, .{
        .argv = &argv,
        .cwd = ".",
    }, .{
        .context = &recorder,
        .emit = recordEvent,
    }, .{});
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, result.status());
    try std.testing.expectEqual(@as(usize, 6), recorder.count);
    try std.testing.expect(recorder.saw_stdout);
    try std.testing.expect(recorder.saw_stderr);
    try std.testing.expect(recorder.saw_structured);
    try std.testing.expect(recorder.saw_final);
    try std.testing.expectEqual(@as(?u8, 0), recorder.exit_code);
    try std.testing.expect(recorder.saw_done_final);
}

test "headless structured jsonl emission ignores plain text lines" {
    var recorder: EventRecorder = .{};
    try emitStructuredJsonl(std.testing.allocator, .{
        .context = &recorder,
        .emit = recordEvent,
    }, "progress\n{\"type\":\"event\"}\n");

    try std.testing.expectEqual(@as(usize, 1), recorder.count);
    try std.testing.expect(recorder.saw_structured);
}

test "headless command passes explicit environment to real process" {
    const argv = [_][]const u8{ "sh", "-c", "printf \"$MITOS_HEADLESS_TEST_VALUE\"" };
    const env = [_]adapters.EnvVar{.{ .name = "MITOS_HEADLESS_TEST_VALUE", .value = "env-ok" }};
    const result = try execute(std.testing.allocator, .{
        .argv = &argv,
        .cwd = ".",
        .environment = &env,
    }, .{});
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, result.status());
    try std.testing.expectEqualStrings("env-ok", result.stdout);
}

test "headless command resolves executable from explicit PATH" {
    var fake_bin = try @import("test_support.zig").FakeBin.init(std.testing.allocator);
    defer fake_bin.deinit();
    try fake_bin.writeExecutable("mitos-headless-fake",
        \\#!/bin/sh
        \\printf fake-path-ok
        \\
    );

    const argv = [_][]const u8{"mitos-headless-fake"};
    const env = [_]adapters.EnvVar{fake_bin.pathEnv()};
    const result = try execute(std.testing.allocator, .{
        .argv = &argv,
        .cwd = ".",
        .environment = &env,
    }, .{});
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, result.status());
    try std.testing.expectEqualStrings("fake-path-ok", result.stdout);
}

test "headless command failure emits process exit and error message" {
    const argv = [_][]const u8{ "sh", "-c", "printf provider-failed >&2; exit 7" };
    var recorder: EventRecorder = .{};
    const result = try executeAndEmit(std.testing.allocator, .{
        .argv = &argv,
        .cwd = ".",
    }, .{
        .context = &recorder,
        .emit = recordEvent,
    }, .{});
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.failed, result.status());
    try std.testing.expectEqual(@as(?u8, 7), recorder.exit_code);
    try std.testing.expect(recorder.saw_error);
    try std.testing.expect(!recorder.saw_final);
}

test "headless final output falls back to last non-json line" {
    const final = try extractFinalOutputAlloc(std.testing.allocator, "progress\nfinal text\n");
    defer std.testing.allocator.free(final);

    try std.testing.expectEqualStrings("final text", final);
}

test "headless final output handles provider stream json fields" {
    const claude = try extractFinalOutputAlloc(std.testing.allocator, "{\"type\":\"result\",\"result\":\"claude done\"}\n");
    defer std.testing.allocator.free(claude);
    const gemini = try extractFinalOutputAlloc(std.testing.allocator, "{\"type\":\"response\",\"text\":\"gemini done\"}\n");
    defer std.testing.allocator.free(gemini);

    try std.testing.expectEqualStrings("claude done", claude);
    try std.testing.expectEqualStrings("gemini done", gemini);
}

const EventRecorder = struct {
    count: usize = 0,
    exit_code: ?u8 = null,
    saw_done_final: bool = false,
    saw_stdout: bool = false,
    saw_stderr: bool = false,
    saw_structured: bool = false,
    saw_final: bool = false,
    saw_error: bool = false,
};

fn recordEvent(context: *anyopaque, event: adapters.AdapterEvent) anyerror!void {
    const recorder: *EventRecorder = @ptrCast(@alignCast(context));
    recorder.count += 1;
    switch (event) {
        .process_exited => |process| recorder.exit_code = process.exit_code,
        .stdout => recorder.saw_stdout = true,
        .stderr => recorder.saw_stderr = true,
        .structured => recorder.saw_structured = true,
        .final_output => |text| {
            recorder.saw_final = true;
            if (std.mem.eql(u8, text, "done")) recorder.saw_done_final = true;
        },
        .error_message => recorder.saw_error = true,
        else => {},
    }
}
