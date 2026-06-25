const std = @import("std");
const adapter_detection = @import("../adapters/detection.zig");
const adapters = @import("../adapters/types.zig");
const headless = @import("headless.zig");
const transport = @import("../transport/root.zig");

pub const provider_id = adapters.ProviderId{ .value = "opencode" };
pub const run_adapter_id = adapters.AdapterId{ .value = "opencode.run" };
pub const pty_adapter_id = adapters.AdapterId{ .value = "opencode.pty" };

const pty_terminal_chrome_markers = [_][]const u8{
    "OpenCode",
    "opencode",
    "Working",
    "Thinking",
    "follow-up",
    "loading",
    "connected",
    "transport:",
};
pub const pty_terminal_observation = adapters.terminalObservationProviderTui(&pty_terminal_chrome_markers);

const supported_transports = [_]transport.TransportName{.stdio_json};
const unavailable_warnings = [_][]const u8{"opencode binary was not found on PATH or common toolchain directories"};
const unsupported_warnings = [_][]const u8{"opencode does not advertise run --format json support"};

pub fn initRunAdapter() adapters.AgentAdapter {
    return .{
        .id = run_adapter_id,
        .provider = provider_id,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
            .supports_streaming_events = true,
            .supports_model_selection = true,
        },
        .detect = detectRun,
        .start = startRun,
    };
}

pub fn buildRunCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !headless.HeadlessCommand {
    const argv = try allocator.alloc([]const u8, 8);
    argv[0] = "opencode";
    argv[1] = "--pure";
    argv[2] = "run";
    argv[3] = "--format";
    argv[4] = "json";
    argv[5] = "--dir";
    argv[6] = cwd;
    argv[7] = task.text;

    return .{
        .argv = argv,
        .cwd = cwd,
    };
}

pub fn buildPtyCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !headless.HeadlessCommand {
    const argv = try allocator.alloc([]const u8, 5);
    argv[0] = "opencode";
    argv[1] = "--pure";
    argv[2] = "--prompt";
    argv[3] = task.text;
    argv[4] = cwd;

    return .{
        .argv = argv,
        .cwd = cwd,
    };
}

fn detectRun(
    _: *adapters.AgentAdapter,
    allocator: std.mem.Allocator,
) anyerror!adapters.AdapterDetection {
    if (!try adapter_detection.executableAvailable(allocator, "opencode")) {
        return .{
            .available = false,
            .supported_transports = &supported_transports,
            .warnings = &unavailable_warnings,
        };
    }

    return .{
        .available = true,
        .binary = "opencode",
        .version = null,
        .owns_version = false,
        .auth_status = try adapter_detection.authStatusFromEnv(
            allocator,
            &.{ "OPENCODE_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY" },
        ),
        .supported_transports = &supported_transports,
    };
}

fn startRun(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    task: adapters.Task,
) anyerror!adapters.AdapterRun {
    const cwd = context.workspace_container_path orelse context.workspace_host_path;
    var command = try buildRunCommand(context.allocator, task, cwd);
    command.environment = context.env;
    defer command.deinit(context.allocator);

    try headless.emitLaunchPlan(context.event_sink, self.provider.value, self.id.value);
    const result = try headless.executeAndEmitForProvider(context.allocator, command, context.event_sink, .{}, self.provider);
    defer result.deinit(context.allocator);
    try headless.emitDetectedPrompts(
        context.allocator,
        context.event_sink,
        self.provider,
        self.id,
        result.stdout,
        context.started_at_ms,
    );
    try headless.emitDetectedPrompts(
        context.allocator,
        context.event_sink,
        self.provider,
        self.id,
        result.stderr,
        context.started_at_ms,
    );

    return .{
        .run_id = context.run_id,
        .adapter_id = self.id,
        .transport_name = .stdio_json,
        .status = result.status(),
        .started_at_ms = context.started_at_ms,
    };
}

test "opencode run command construction is deterministic" {
    const command = try buildRunCommand(
        std.testing.allocator,
        .{ .text = "Explain the code" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("opencode", command.argv[0]);
    try std.testing.expectEqualStrings("--pure", command.argv[1]);
    try std.testing.expectEqualStrings("run", command.argv[2]);
    try std.testing.expectEqualStrings("--format", command.argv[3]);
    try std.testing.expectEqualStrings("json", command.argv[4]);
    try std.testing.expectEqualStrings("--dir", command.argv[5]);
    try std.testing.expectEqualStrings("/workspace", command.argv[6]);
    try std.testing.expectEqualStrings("Explain the code", command.argv[7]);
}

test "opencode run adapter start emits normalized provider events" {
    var fake_bin = try @import("test_support.zig").FakeBin.init(std.testing.allocator);
    defer fake_bin.deinit();
    try fake_bin.writeExecutable("opencode",
        \\#!/bin/sh
        \\if [ "$1" = "--pure" ] && [ "$2" = "run" ] && [ "$3" = "--format" ] && [ "$4" = "json" ]; then
        \\  printf '{"type":"message","text":"hello"}\n'
        \\  printf '{"type":"result","result":"opencode fake done"}\n'
        \\  exit 0
        \\fi
        \\printf 'unexpected opencode args\n' >&2
        \\exit 9
        \\
    );

    var adapter = initRunAdapter();
    var recorder: @import("test_support.zig").AdapterEventRecorder = .{
        .expected_final = "opencode fake done",
    };
    const env = [_]adapters.EnvVar{fake_bin.pathEnv()};
    const run_id = try @import("../runtime/types.zig").RunId.init("run-opencode-start");
    const run = try adapter.start(&adapter, .{
        .allocator = std.testing.allocator,
        .run_id = run_id,
        .workspace_host_path = ".",
        .env = &env,
        .event_sink = recorder.sink(),
    }, .{ .text = "hello" });

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, run.status);
    try std.testing.expect(recorder.saw_process_spawned);
    try std.testing.expect(recorder.saw_process_exited);
    try std.testing.expect(recorder.saw_structured);
    try std.testing.expect(recorder.saw_final);
    try std.testing.expect(recorder.saw_expected_final);
}
